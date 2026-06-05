package app

import (
	"context"
	"fmt"
	"lingxi/internal/money"
	"math"
	"strings"
	"time"
)

type fundingSettlementAttemptResult struct {
	TotalFundingIncome    float64
	ExpectedFundingIncome float64
	Alerts                []string
	PendingSymbols        []string
	FundingIncomeBySymbol map[string]float64
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
		// 已升级人工处置的记录不再自动重试(R34/S5-E),避免无限静默重试,待人工确认/清理。
		if record.Escalated {
			continue
		}
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

// shouldEscalatePendingFunding 判定一笔迟迟未到账的 pending funding 是否应升级为人工处置(纯函数,可单测)。
// 触发任一阈值即升级:重试次数 ≥ maxAttempts(>0) 或 自创建以来挂起年龄 ≥ maxAge(>0)。返回是否升级与原因。
func shouldEscalatePendingFunding(attemptCount int, recordedAt time.Time, now time.Time, maxAttempts int, maxAge time.Duration) (bool, string) {
	if maxAttempts > 0 && attemptCount >= maxAttempts {
		return true, fmt.Sprintf("资金费结算重试达上限 %d 次仍未到账", maxAttempts)
	}
	if maxAge > 0 && !recordedAt.IsZero() && now.Sub(recordedAt) >= maxAge {
		return true, fmt.Sprintf("资金费结算挂起超过 %s 仍未到账", maxAge.Round(time.Minute))
	}
	return false, ""
}

// escalatePendingFundingSettlement 执行 R34 升级:发 critical 告警 + cycle 隔离(回退平仓时记入的日内净利)+ 标记
// Escalated 落盘停重试。幂等:Escalated 已置位则只更新 LastError 不重复隔离/告警。
func (e *FundingRateSniperEngine) escalatePendingFundingSettlement(record PendingFundingSettlementRecord, reason string, now time.Time) error {
	if record.Escalated {
		return nil
	}
	record.Escalated = true
	record.LastAttemptAt = now.Format(time.RFC3339)
	record.LastError = "已升级人工处置: " + reason
	// cycle 隔离:平仓时已把(不含 funding 的)平仓净利记入当日已实现盈亏(noteDailySettlement)。该周期 funding
	// 迟迟不到账、净利不完整,继续留在连亏/回撤口径里会误触发熔断 → 回退这笔记入,把该 cycle 从风控口径中隔离。
	if math.Abs(record.Settlement.NetPnL) > executionQtyTolerance {
		if err := e.noteDailySettlement(-record.Settlement.NetPnL); err != nil && e.logger != nil {
			e.logger.Printf("pending funding 隔离回退日内净利失败: cycle=%s err=%v", record.CycleID, err)
		}
	}
	detail := fmt.Sprintf("cycle=%s %s;已停止自动重试并把该周期从连亏/回撤口径隔离,需人工核实资金费流水后处理", record.CycleID, reason)
	e.emitDomainEvents([]DomainEvent{{
		Type:       EventTypePendingFundingStuck,
		Level:      EventLevelCritical,
		OccurredAt: now,
		RunID:      record.RunID,
		CycleID:    record.CycleID,
		Reason:     detail,
	}})
	if e.logger != nil {
		e.logger.Printf("[CRITICAL] pending funding 升级人工处置: %s", detail)
	}
	return e.reportStore.UpsertPendingFundingSettlement(record)
}

func (e *FundingRateSniperEngine) processPendingFundingSettlement(record PendingFundingSettlementRecord) error {
	if e == nil || e.reportStore == nil {
		return nil
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result := e.settleFundingIncomeDetailed(ctx, record.Hedges, record.Settlement.BeforeSnapshot, record.Settlement.FundingTimePoint, record.rollingHoldSymbolSet())
	if len(result.PendingSymbols) > 0 {
		record.AttemptCount++
		record.LastAttemptAt = now.Format(time.RFC3339)
		record.NextAttemptAt = e.nextFundingSettlementAttemptAt(record.AttemptCount).Format(time.RFC3339)
		record.LastError = fmt.Sprintf("资金费流水尚未齐全: %s", strings.Join(result.PendingSymbols, ","))
		// R34/S5-E:重试达上限或挂起超时 → 升级 critical 告警 + cycle 隔离 + 停重试,不再无限静默等待。
		recordedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(record.RecordedAt))
		if escalate, reason := shouldEscalatePendingFunding(record.AttemptCount, recordedAt, now, e.cfg.RiskConfig.MaxFundingSettlementAttempts, e.cfg.RiskConfig.MaxFundingSettlementAge); escalate {
			return e.escalatePendingFundingSettlement(record, reason, now)
		}
		if err := e.reportStore.UpsertPendingFundingSettlement(record); err != nil {
			return err
		}
		if e.logger != nil {
			e.logger.Printf("资金费异步结算继续等待，cycle=%s pending_symbols=%s next_attempt_at=%s attempt=%d", record.CycleID, strings.Join(result.PendingSymbols, ","), record.NextAttemptAt, record.AttemptCount)
		}
		return nil
	}

	updated := record.Settlement
	updated.FundingIncome = result.TotalFundingIncome
	updated.ExpectedFundingIncome = result.ExpectedFundingIncome
	updated.FundingIncomeDiff = money.Sum(result.TotalFundingIncome, -result.ExpectedFundingIncome).USDT()
	// 资金费实收到账后，落袋净利以定点求和重算：funding + 现货/合约交易盈亏 - 手续费 - 借币成本，
	// 与开仓阶段(close_protocol)同一套 money 口径，避免 float64 连加漂移使净利与对账值逐分偏离。
	updated.NetPnL = money.Sum(
		result.TotalFundingIncome,
		updated.SpotTradingPnL,
		updated.FuturesTradingPnL,
		-updated.TotalFees,
		-updated.TotalBorrowCosts,
	).USDT()
	updated.FundingIncomePending = false
	updated.FundingSettledAt = now

	// 更新单币种利润归因
	for i, attr := range updated.Attributions {
		if actualIncome, ok := result.FundingIncomeBySymbol[attr.Symbol]; ok {
			updated.Attributions[i].FundingIncome = actualIncome
			updated.Attributions[i].NetPnL += actualIncome
		}
	}

	// M-S3.0 观测:资金费到账后 NetPnL 已变,重算完整净利影子口径并落观测行(纯观测,零行为变更)。
	updated.applyFrictionObservation()
	e.logFrictionObservation(updated, "funding_settled")

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
	// funding 实收落袋后，updated.NetPnL 才是该周期的真实净利——此刻才能判定本周期是赚是亏。
	// 连续亏损熔断必须挂在这里，而非平仓时(close 阶段净利还不含 funding，几乎必为负会误判)。
	if err := e.noteCycleFinalSettlement(updated.NetPnL); err != nil && e.logger != nil {
		e.logger.Printf("连续亏损熔断复核触发停机: cycle=%s netPnL=%.6f err=%v", updated.CycleID, updated.NetPnL, err)
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

func (e *FundingRateSniperEngine) settleFundingIncomeDetailed(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, fundingTime time.Time, keptSymbols map[string]struct{}) fundingSettlementAttemptResult {
	startTime := fundingTime.Add(-1 * time.Minute)
	endTime := fundingTime.Add(2 * time.Minute)
	symbolSet := make(map[string]struct{}, len(hedges))
	expectedFundingBySymbol := make(map[string]float64, len(hedges))
	for _, hedge := range hedges {
		symbolSet[hedge.Target.Symbol] = struct{}{}
		expectedFundingBySymbol[hedge.Target.Symbol] += estimateExpectedFundingIncome(hedge)
	}

	result := fundingSettlementAttemptResult{
		Alerts:                make([]string, 0),
		PendingSymbols:        make([]string, 0),
		FundingIncomeBySymbol: make(map[string]float64, len(symbolSet)),
	}
	for symbol := range symbolSet {
		beforeQty := beforeSnapshot.FuturesPositionAmounts[symbol]
		// 「开仓前已有持仓」对单周期是异常(无法把资金费干净归因到本周期开的仓)→ 跳过归因。
		// 但滚动续持(keep)仓是上周期持有过来的,beforeQty≠0 是正常预期,其资金费正属于本周期,必须放行归因(M-S4.4b)。
		_, kept := keptSymbols[symbol]
		if !kept && math.Abs(beforeQty) >= e.cfg.RiskConfig.ResidualPositionAlertQty {
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
		result.FundingIncomeBySymbol[symbol] = actualFundingIncome
		diff := actualFundingIncome - expectedFundingBySymbol[symbol]
		if math.Abs(diff) >= e.cfg.RiskConfig.FundingIncomeDiffAlertUSDT {
			result.Alerts = append(result.Alerts, fmt.Sprintf("资金费到账偏差过大 %s: 实收=%.6f 理论=%.6f 偏差=%.6f", symbol, actualFundingIncome, expectedFundingBySymbol[symbol], diff))
		}
	}
	return result
}
