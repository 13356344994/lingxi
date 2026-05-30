package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

type fundingSettlementAttemptResult struct {
	TotalFundingIncome    float64
	ExpectedFundingIncome float64
	Alerts                []string
	PendingSymbols        []string
}

func (e *FundingRateSniperEngine) startFundingSettlementWorker(ctx context.Context) {
	if e == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if e.fundingSettlementWakeCh == nil {
		e.fundingSettlementWakeCh = make(chan struct{}, 1)
	}
	e.fundingSettlementWorkerOnce.Do(func() {
		go e.runFundingSettlementWorker(ctx)
	})
}

func (e *FundingRateSniperEngine) wakeFundingSettlementWorker() {
	if e == nil || e.fundingSettlementWakeCh == nil {
		return
	}
	select {
	case e.fundingSettlementWakeCh <- struct{}{}:
	default:
	}
}

func (e *FundingRateSniperEngine) runFundingSettlementWorker(ctx context.Context) {
	interval := e.effectiveFundingSettlementDelay()
	if interval <= 0 {
		interval = defaultFundingSettlementDelay
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := e.processDuePendingFundingSettlements(); err != nil && e.logger != nil {
			e.logger.Printf("处理待结算资金费任务失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-e.fundingSettlementWakeCh:
		}
	}
}

func (e *FundingRateSniperEngine) processDuePendingFundingSettlements() error {
	if e == nil || e.reportStore == nil {
		return nil
	}
	records, err := e.reportStore.ListPendingFundingSettlements(0)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, record := range records {
		nextAttemptAt, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(record.NextAttemptAt))
		if parseErr == nil && nextAttemptAt.After(now) {
			continue
		}
		if err := e.processPendingFundingSettlement(record); err != nil {
			return err
		}
	}
	return nil
}

func (e *FundingRateSniperEngine) processPendingFundingSettlement(record PendingFundingSettlementRecord) error {
	if e == nil || e.reportStore == nil {
		return nil
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result := e.settleFundingIncomeDetailed(ctx, record.Hedges, record.Settlement.BeforeSnapshot, record.Settlement.FundingTimePoint)
	if len(result.PendingSymbols) > 0 {
		record.AttemptCount++
		record.LastAttemptAt = now.Format(time.RFC3339)
		record.NextAttemptAt = e.nextFundingSettlementAttemptAt(record.AttemptCount).Format(time.RFC3339)
		record.LastError = fmt.Sprintf("资金费流水尚未齐全: %s", strings.Join(result.PendingSymbols, ","))
		if err := e.reportStore.UpsertPendingFundingSettlement(record); err != nil {
			return err
		}
		if e.logger != nil {
			e.logger.Printf("资金费异步结算继续等待，cycle=%s pending_symbols=%s next_attempt_at=%s", record.CycleID, strings.Join(result.PendingSymbols, ","), record.NextAttemptAt)
		}
		return nil
	}

	updated := record.Settlement
	updated.FundingIncome = result.TotalFundingIncome
	updated.ExpectedFundingIncome = result.ExpectedFundingIncome
	updated.FundingIncomeDiff = result.TotalFundingIncome - result.ExpectedFundingIncome
	updated.NetPnL = result.TotalFundingIncome + updated.SpotTradingPnL + updated.FuturesTradingPnL - updated.TotalFees
	updated.FundingIncomePending = false
	updated.FundingSettledAt = now
	alerts := append([]string(nil), updated.Alerts...)
	alerts = filterOutFundingPendingAlerts(alerts)
	alerts = append(alerts, result.Alerts...)
	updated.Alerts = alerts
	updated.HasAlert = len(alerts) > 0
	if err := e.reportStore.UpdateSettlement(updated); err != nil {
		return err
	}
	if err := e.reportStore.DeletePendingFundingSettlement(record.RunID, record.CycleID); err != nil {
		return err
	}
	deltaFunding := updated.FundingIncome - record.Settlement.FundingIncome
	if math.Abs(deltaFunding) > executionQtyTolerance {
		if err := e.noteDailySettlement(deltaFunding); err != nil && e.logger != nil {
			e.logger.Printf("异步资金费结算写入日内盈亏失败: %v", err)
		}
	}
	if e.logger != nil {
		e.logger.Printf(
			"异步资金费结算完成，cycle=%s 实收=%.6f 理论=%.6f 偏差=%.6f 结算完成时间=%s",
			updated.CycleID,
			updated.FundingIncome,
			updated.ExpectedFundingIncome,
			updated.FundingIncomeDiff,
			updated.FundingSettledAt.Format("15:04:05"),
		)
	}
	return nil
}

func (e *FundingRateSniperEngine) nextFundingSettlementAttemptAt(attempt int) time.Time {
	delay := e.effectiveFundingSettlementDelay()
	if delay <= 0 {
		delay = defaultFundingSettlementDelay
	}
	if attempt <= 1 {
		return time.Now().UTC().Add(delay)
	}
	scale := time.Duration(minInt(attempt, 4))
	return time.Now().UTC().Add(delay * scale)
}

func (e *FundingRateSniperEngine) settleFundingIncomeDetailed(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingTime time.Time) fundingSettlementAttemptResult {
	startTime := fundingTime.Add(-1 * time.Minute)
	endTime := fundingTime.Add(2 * time.Minute)
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	result := fundingSettlementAttemptResult{
		Alerts:         make([]string, 0),
		PendingSymbols: make([]string, 0),
	}
	for symbol := range symbolSet {
		beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
		if math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
			result.Alerts = append(result.Alerts, fmt.Sprintf("资金费归因已跳过 %s: 开仓前已有持仓 positionAmt=%.10f", symbol, beforeQty))
			continue
		}

		income, err := e.fetchFundingIncome(ctx, startTime, endTime, symbol)
		if err != nil {
			result.Alerts = append(result.Alerts, fmt.Sprintf("资金费查询失败 %s: %v", symbol, err))
			result.PendingSymbols = append(result.PendingSymbols, symbol)
			continue
		}

		actualFundingIncome := 0.0
		matchedRecords := 0
		for _, item := range income {
			if item.Symbol != symbol {
				continue
			}
			matchedRecords++
			actualFundingIncome += parseFloat(item.Income)
		}
		if matchedRecords == 0 && math.Abs(expectedFundingBySymbol[symbol]) > executionQtyTolerance {
			result.Alerts = append(result.Alerts, fmt.Sprintf("资金费流水尚未到账 %s: 理论=%.6f", symbol, expectedFundingBySymbol[symbol]))
			result.PendingSymbols = append(result.PendingSymbols, symbol)
			continue
		}

		result.TotalFundingIncome += actualFundingIncome
		result.ExpectedFundingIncome += expectedFundingBySymbol[symbol]
		diff := actualFundingIncome - expectedFundingBySymbol[symbol]
		if math.Abs(diff) >= e.cfg.RiskConfig.FundingIncomeDiffAlertUSDT {
			result.Alerts = append(result.Alerts, fmt.Sprintf("资金费到账偏差过大 %s: 实收=%.6f 理论=%.6f 偏差=%.6f", symbol, actualFundingIncome, expectedFundingBySymbol[symbol], diff))
		}
	}
	return result
}
