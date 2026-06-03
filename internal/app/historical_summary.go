package app

import (
	"context"
	"errors"
	"fmt"
	"lingxi/internal/money"
	"math"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的「跳过原因归一 + 汇总/压力测试
// 记录构造」—— 候选跳过原因、月度回放汇总记录、三类压力情景(针刺/资金费反转/平台停机)记录与运行
// 摘要日志，以及 recordDir 访问器（同包搬移，行为不变），让主文件收敛到「编排 + 模拟状态机」
// 职责并向单文件 ≤ 800 行门禁靠拢。

func monthlyBuildSkipReason(baseReason string, err error) string {
	if err == nil {
		return baseReason
	}
	if errors.Is(err, context.Canceled) {
		return "candidate_build_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "candidate_build_timeout"
	}
	return baseReason
}

func monthlySkippedCandidate(item FundingRateHistoryItem, fundingTime time.Time, reason string, err error, candidateBuildTimeout time.Duration) monthlyCandidate {
	notes := []string{"conservativeSkip=true"}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			notes = append(notes, "canceled=true")
		} else if errors.Is(err, context.DeadlineExceeded) {
			notes = append(notes, fmt.Sprintf("timeout=%s", candidateBuildTimeout))
		}
		notes = append(notes, err.Error())
	}
	return monthlyCandidate{
		Symbol:          item.Symbol,
		FundingTime:     fundingTime,
		ExitTime:        fundingTime,
		HoldingCycles:   1,
		ScreeningScore:  parseFloat(item.ScreeningScore),
		FundingRate:     parseFloat(item.FundingRate),
		FundingIncome:   0,
		EstimatedNetPnL: 0,
		RealizedNetPnL:  0,
		Reason:          reason,
		Notes:           notes,
	}
}

func (r *BacktestRunner) buildMonthlySummaryRecord(periodLabel string, trades []BacktestTradeSample, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, stopReason string) BacktestCSVRecord {
	// 资金费/交易盈亏/手续费汇总改用 money 定点内核累加，消除 float64 多笔连加的舍入漂移（Gate 4）。
	var totalFundingM, totalTradingM, totalFeesM money.Micros
	wins := 0
	maxDrawdown := 0.0
	for _, trade := range trades {
		totalFundingM = totalFundingM.Add(money.FromUSDT(trade.FundingIncome))
		totalTradingM = totalTradingM.Add(money.FromUSDT(trade.TradingPnL))
		totalFeesM = totalFeesM.Add(money.FromUSDT(trade.TotalFees))
		if trade.Win {
			wins++
		}
		if trade.Drawdown < maxDrawdown {
			maxDrawdown = trade.Drawdown
		}
	}
	totalFunding := totalFundingM.USDT()
	totalTrading := totalTradingM.USDT()
	totalFees := totalFeesM.USDT()

	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}
	notes := []string{
		"stop=" + stopReason,
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("skipped=%d", simStats.SkippedGroups),
		fmt.Sprintf("fetchSuccessSymbols=%d", fetchStats.SuccessSymbols),
		fmt.Sprintf("fetchFailures=%d", fetchStats.FailureCount),
		fmt.Sprintf("fetchSuccessRate=%.4f", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount)),
		fmt.Sprintf("dataComplete=%t", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount) >= monthlyFundingDataCompleteFloor),
	}
	notes = append(notes, r.monthlyExperimentNotes()...)
	if skipSummary := summarizeMonthlyReasonCounts(simStats.SkipReasons, nil); skipSummary != "" {
		notes = append(notes, "groupSkips="+skipSummary)
	}
	if candidateSummary := summarizeMonthlyReasonCounts(simStats.CandidateReasons, nil); candidateSummary != "" {
		notes = append(notes, "candidateReasons="+candidateSummary)
	}
	if timeoutSummary := summarizeMonthlyTimeoutReasons(simStats.CandidateReasons); timeoutSummary != "" {
		notes = append(notes, "timeouts="+timeoutSummary)
	}
	if fetchSummary := summarizeMonthlyReasonCounts(fetchStats.FailureReasons, fetchStats.FailureSamples); fetchSummary != "" {
		notes = append(notes, "fetchReasons="+fetchSummary)
	}
	if strings.TrimSpace(fetchStats.RequestSummary) != "" {
		notes = append(notes, "requestSummary="+fetchStats.RequestSummary)
	}
	if strings.TrimSpace(simStats.CacheSummary) != "" {
		notes = append(notes, "klineCache="+simStats.CacheSummary)
	}

	return BacktestCSVRecord{
		TraceRecordMetadata: buildTraceRecordMetadata(r.cfg),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Scenario:            r.monthlyScenarioName("MonthlyMarketReplaySummary"),
		Symbol:              "ALL",
		Period:              periodLabel,
		Trades:              len(trades),
		WinRate:             formatFloat(winRate),
		FundingIncome:       formatFloat(totalFunding),
		TradingPnL:          formatFloat(totalTrading),
		TotalFees:           formatFloat(totalFees),
		NetPnL:              formatFloat(totalFundingM.Add(totalTradingM).Sub(totalFeesM).USDT()),
		MaxDrawdown:         formatFloat(maxDrawdown),
		Notes:               strings.Join(notes, " | "),
	}
}

func (r *BacktestRunner) buildMonthlyStressRecords(periodLabel string, trades []BacktestTradeSample, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats) []BacktestCSVRecord {
	if len(trades) == 0 {
		return nil
	}
	hcfg := r.historicalConfig()
	records := make([]BacktestCSVRecord, 0, 3)
	metadata := buildTraceRecordMetadata(r.cfg)

	needleSamples := applyMonthlyStressNeedle(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressNeedleShock"),
		periodLabel,
		needleSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=needle_shock", "assumption=entry_exit_price_spike"},
	))

	fundingSamples := applyMonthlyStressFundingSpike(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressFundingSpike"),
		periodLabel,
		fundingSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=funding_spike", "assumption=funding_income_reversal"},
	))

	downtimeSamples := applyMonthlyStressPlatformDowntime(trades, hcfg.PerLegNotionalUSDT)
	records = append(records, buildStressSummaryRecord(
		r.monthlyScenarioName("MonthlyStressPlatformDowntime"),
		periodLabel,
		downtimeSamples,
		hcfg.InitialEquityUSDT,
		fetchStats,
		simStats,
		metadata,
		[]string{"stress=platform_downtime", "assumption=exit_delay_and_emergency_hedge"},
	))

	return records
}

func applyMonthlyStressNeedle(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for _, sample := range samples {
		extraTradingLoss := math.Max(perLegNotional*0.02, math.Abs(sample.TradingPnL)*1.25+sample.TotalFees)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressNeedleShock", 0, -extraTradingLoss, 0, fmt.Sprintf("needleLoss=%.6f", extraTradingLoss)))
	}
	return stressed
}

func applyMonthlyStressFundingSpike(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for _, sample := range samples {
		fundingShock := math.Max(perLegNotional*0.008, math.Abs(sample.FundingIncome)*1.6)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressFundingSpike", -fundingShock, 0, 0, fmt.Sprintf("fundingShock=%.6f", fundingShock)))
	}
	return stressed
}

func applyMonthlyStressPlatformDowntime(samples []BacktestTradeSample, perLegNotional float64) []BacktestTradeSample {
	stressed := make([]BacktestTradeSample, 0, len(samples))
	for idx, sample := range samples {
		if idx%3 != 0 {
			stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressPlatformDowntime", 0, 0, 0, "downtimeImpact=none"))
			continue
		}
		emergencyLoss := math.Max(perLegNotional*0.03, math.Abs(sample.TradingPnL)+sample.TotalFees*2+0.3)
		extraFees := math.Max(0.05, sample.TotalFees*0.5)
		stressed = append(stressed, withStressAdjustment(sample, "MonthlyStressPlatformDowntime", 0, -emergencyLoss, extraFees, fmt.Sprintf("downtimeLoss=%.6f extraFees=%.6f", emergencyLoss, extraFees)))
	}
	return stressed
}

func withStressAdjustment(sample BacktestTradeSample, scenario string, fundingDelta float64, tradingDelta float64, feeDelta float64, note string) BacktestTradeSample {
	adjusted := sample
	adjusted.Scenario = scenario
	adjusted.FundingIncome += fundingDelta
	adjusted.TradingPnL += tradingDelta
	adjusted.TotalFees += feeDelta
	adjusted.Drawdown = 0
	adjusted.Win = adjusted.FundingIncome+adjusted.TradingPnL-adjusted.TotalFees > 0
	if strings.TrimSpace(note) != "" {
		if strings.TrimSpace(adjusted.Notes) == "" {
			adjusted.Notes = note
		} else {
			adjusted.Notes += " | " + note
		}
	}
	return adjusted
}

func buildStressSummaryRecord(scenario string, periodLabel string, trades []BacktestTradeSample, initialEquity float64, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, metadata TraceRecordMetadata, extraNotes []string) BacktestCSVRecord {
	// 资金费/交易盈亏/手续费与权益曲线在多笔累加时改用 money 定点内核做整数累加，消除 float64
	// 连加的舍入漂移，让压力测试汇总的净值/回撤与逐笔记账逐位一致（Gate 4）。
	var totalFundingM, totalTradingM, totalFeesM money.Micros
	wins := 0
	equityM := money.FromUSDT(initialEquity)
	peakEquityM := equityM
	var maxDrawdownM money.Micros
	for _, trade := range trades {
		fundingM := money.FromUSDT(trade.FundingIncome)
		tradingM := money.FromUSDT(trade.TradingPnL)
		feesM := money.FromUSDT(trade.TotalFees)
		totalFundingM = totalFundingM.Add(fundingM)
		totalTradingM = totalTradingM.Add(tradingM)
		totalFeesM = totalFeesM.Add(feesM)
		netPnLM := fundingM.Add(tradingM).Sub(feesM)
		if netPnLM.IsPositive() {
			wins++
		}
		equityM = equityM.Add(netPnLM)
		if equityM > peakEquityM {
			peakEquityM = equityM
		}
		if drawdownM := peakEquityM.Sub(equityM); drawdownM.Neg() < maxDrawdownM {
			maxDrawdownM = drawdownM.Neg()
		}
	}
	totalFunding := totalFundingM.USDT()
	totalTrading := totalTradingM.USDT()
	totalFees := totalFeesM.USDT()
	maxDrawdown := maxDrawdownM.USDT()
	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades))
	}
	notes := []string{
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("fetchSuccessRate=%.4f", fetchStats.successRate(fetchStats.SuccessSymbols+fetchStats.FailureCount)),
	}
	notes = append(notes, extraNotes...)
	return BacktestCSVRecord{
		TraceRecordMetadata: metadata.normalized(),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		Scenario:            scenario,
		Symbol:              "ALL",
		Period:              periodLabel,
		Trades:              len(trades),
		WinRate:             formatFloat(winRate),
		FundingIncome:       formatFloat(totalFunding),
		TradingPnL:          formatFloat(totalTrading),
		TotalFees:           formatFloat(totalFees),
		NetPnL:              formatFloat(totalFundingM.Add(totalTradingM).Sub(totalFeesM).USDT()),
		MaxDrawdown:         formatFloat(maxDrawdown),
		Notes:               strings.Join(notes, " | "),
	}
}

func (r *BacktestRunner) buildMonthlyRunSummaryLogRecord(periodLabel string, fetchStats monthlyFundingFetchStats, simStats monthlySimulationStats, stopReason string, experiment monthlyExperimentPlan) MonthlySimulationLogRecord {
	notes := []string{
		"period=" + periodLabel,
		"stop=" + stopReason,
		fmt.Sprintf("experiment=%d/%d", experiment.Index, experiment.Total),
		fmt.Sprintf("groups=%d", simStats.GroupCount),
		fmt.Sprintf("evaluated=%d", simStats.EvaluatedCandidates),
		fmt.Sprintf("skipped=%d", simStats.SkippedGroups),
		fmt.Sprintf("fetchSuccessSymbols=%d", fetchStats.SuccessSymbols),
		fmt.Sprintf("fetchFailures=%d", fetchStats.FailureCount),
	}
	notes = append(notes, r.monthlyExperimentNotes()...)
	if fetchSummary := summarizeMonthlyReasonCounts(fetchStats.FailureReasons, fetchStats.FailureSamples); fetchSummary != "" {
		notes = append(notes, "fetchReasons="+fetchSummary)
	}
	if strings.TrimSpace(fetchStats.RequestSummary) != "" {
		notes = append(notes, "requestSummary="+fetchStats.RequestSummary)
	}
	if strings.TrimSpace(simStats.CacheSummary) != "" {
		notes = append(notes, "klineCache="+simStats.CacheSummary)
	}
	return MonthlySimulationLogRecord{
		RecordedAt:  time.Now().UTC().Format(time.RFC3339),
		Action:      "run_summary",
		Symbol:      "ALL",
		Reason:      stopReason,
		Notes:       strings.Join(notes, " | "),
		FundingTime: periodLabel,
	}
}

func summarizeMonthlyTimeoutReasons(reasonCounts map[string]int) string {
	if len(reasonCounts) == 0 {
		return ""
	}
	timeoutKeys := []string{
		"candidate_build_timeout",
		"candidate_finalize_timeout",
		"group_evaluation_timeout",
	}
	parts := make([]string, 0, len(timeoutKeys))
	for _, key := range timeoutKeys {
		count := reasonCounts[key]
		if count <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", key, count))
	}
	return strings.Join(parts, ",")
}

func (r *BacktestRunner) recordDir() string {
	if r.store != nil {
		return r.store.RecordDir()
	}
	return r.cfg.NetworkConfig.RecordDir
}
