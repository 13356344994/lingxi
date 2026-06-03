package app

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的「配置规整 + 实验计划 + 运行期
// 参数访问器」—— historicalConfig 默认值填充、批量实验计划展开与参数解析、单腿名义/对冲数量/
// 流动性份额/候选上限等纯参数推导（同包搬移，行为不变），让主文件收敛到「编排 + 模拟状态机」
// 职责并向单文件 ≤ 800 行门禁靠拢。

func (r *BacktestRunner) historicalConfig() historicalConfig {
	hcfg := r.cfg.HistoricalConfig
	zeroHistorical := HistoricalConfig{}
	cfg := historicalConfig{
		DefaultLookbackDays:                    hcfg.DefaultLookbackDays,
		InitialEquityUSDT:                      hcfg.InitialEquityUSDT,
		StopEquityUSDT:                         hcfg.StopEquityUSDT,
		PerLegNotionalUSDT:                     hcfg.PerLegNotionalUSDT,
		FundingDirectionMode:                   hcfg.FundingDirectionMode,
		MaxFundingCyclesHeld:                   hcfg.MaxFundingCyclesHeld,
		MaxPerLegEquityShare:                   hcfg.MaxPerLegEquityShare,
		SpotTakerFeeRate:                       hcfg.SpotTakerFeeRate,
		FuturesTakerFeeRate:                    hcfg.FuturesTakerFeeRate,
		AssumedHourlyBorrowInterestRate:        hcfg.AssumedHourlyBorrowInterestRate,
		BorrowInterestMinBillingHours:          hcfg.BorrowInterestMinBillingHours,
		NegativeCarrySpotEntryImpactMultiplier: hcfg.NegativeCarrySpotEntryImpactMultiplier,
		NegativeCarrySpotExitImpactMultiplier:  hcfg.NegativeCarrySpotExitImpactMultiplier,
		CrossStableQuoteMaxDeviationRate:       hcfg.CrossStableQuoteMaxDeviationRate,
		ResidualFillHaircutEnabled:             hcfg.ResidualFillHaircutEnabled,
		ExpectedLegFillRatio:                   hcfg.ExpectedLegFillRatio,
		LegRejectProbability:                   hcfg.LegRejectProbability,
		EmergencyHedgeSpreadMultiplier:         hcfg.EmergencyHedgeSpreadMultiplier,
		FundingPhaseLead:                       hcfg.FundingPhaseLead,
		FundingFetchConcurrency:                hcfg.FundingFetchConcurrency,
		FundingRetryMax:                        hcfg.FundingRetryMax,
		CacheDir:                               hcfg.CacheDir,
		ReferenceKlineLookback:                 hcfg.ReferenceKlineLookback,
		EstimateKlineLookback:                  hcfg.EstimateKlineLookback,
		SafetyBufferFloorUSDT:                  hcfg.SafetyBufferFloorUSDT,
		SafetyBufferCostMultiplier:             hcfg.SafetyBufferCostMultiplier,
		MaxDrawdownRate:                        hcfg.MaxDrawdownRate,
		MaxConsecutiveLosses:                   hcfg.MaxConsecutiveLosses,
		MaxFundingRate:                         hcfg.MaxFundingRate,
		MaxFundingRateJump:                     hcfg.MaxFundingRateJump,
		MaxSingleTradeLossUSDT:                 hcfg.MaxSingleTradeLossUSDT,
		FundingFetchSlot:                       hcfg.FundingFetchSlot,
		FundingFetchTimeout:                    hcfg.FundingFetchTimeout,
		GroupEvaluationTimeout:                 hcfg.GroupEvaluationTimeout,
		CandidateBuildTimeout:                  hcfg.CandidateBuildTimeout,
		CandidateFinalizeTimeout:               hcfg.CandidateFinalizeTimeout,
		ScreeningConcurrency:                   hcfg.ScreeningConcurrency,
		DeepCandidateLimit:                     hcfg.DeepCandidateLimit,
		CoarseSymbolLimit:                      hcfg.CoarseSymbolLimit,
		FundingGroupTopSymbolsPerTime:          hcfg.FundingGroupTopSymbolsPerTime,
		LiquidityShareMin:                      hcfg.LiquidityShareMin,
		LiquidityShareFloor:                    hcfg.LiquidityShareFloor,
		LiquidityShareMax:                      hcfg.LiquidityShareMax,
		SevereLiquidityShareCap:                hcfg.SevereLiquidityShareCap,
		SevereLiquidityShareMultiplier:         hcfg.SevereLiquidityShareMultiplier,
		SevereLiquidityShareMax:                hcfg.SevereLiquidityShareMax,
	}
	if cfg.DefaultLookbackDays <= 0 {
		cfg.DefaultLookbackDays = 30
	}
	if cfg.InitialEquityUSDT <= 0 {
		cfg.InitialEquityUSDT = 20
	}
	if cfg.StopEquityUSDT <= 0 || cfg.StopEquityUSDT >= cfg.InitialEquityUSDT {
		cfg.StopEquityUSDT = cfg.InitialEquityUSDT * 0.5
	}
	if cfg.PerLegNotionalUSDT <= 0 {
		cfg.PerLegNotionalUSDT = 10
	}
	if cfg.MaxFundingCyclesHeld <= 0 {
		cfg.MaxFundingCyclesHeld = 1
	}
	if strings.TrimSpace(cfg.FundingDirectionMode) == "" {
		cfg.FundingDirectionMode = historicalFundingModeBoth
	}
	cfg.FundingDirectionMode = normalizeHistoricalFundingDirectionMode(cfg.FundingDirectionMode)
	if cfg.MaxPerLegEquityShare <= 0 || cfg.MaxPerLegEquityShare > 1 {
		cfg.MaxPerLegEquityShare = 0.5
	}
	if cfg.AssumedHourlyBorrowInterestRate < 0 || (cfg.AssumedHourlyBorrowInterestRate == 0 && hcfg == zeroHistorical) {
		cfg.AssumedHourlyBorrowInterestRate = 0.00005
	}
	if cfg.BorrowInterestMinBillingHours <= 0 {
		cfg.BorrowInterestMinBillingHours = 1
	}
	if cfg.NegativeCarrySpotEntryImpactMultiplier < 1 {
		cfg.NegativeCarrySpotEntryImpactMultiplier = 1.5
	}
	if cfg.NegativeCarrySpotExitImpactMultiplier < 1 {
		cfg.NegativeCarrySpotExitImpactMultiplier = 1.5
	}
	if cfg.CrossStableQuoteMaxDeviationRate < 0 || (cfg.CrossStableQuoteMaxDeviationRate == 0 && hcfg == zeroHistorical) {
		cfg.CrossStableQuoteMaxDeviationRate = 0.0050
	}
	if hcfg == zeroHistorical {
		// 完全未配置时默认开启残腿/部分成交 haircut，杜绝「完美对冲」假设。
		cfg.ResidualFillHaircutEnabled = true
	}
	if cfg.ExpectedLegFillRatio <= 0 || cfg.ExpectedLegFillRatio > 1 {
		cfg.ExpectedLegFillRatio = 0.99
	}
	if cfg.LegRejectProbability < 0 {
		cfg.LegRejectProbability = 0
	}
	if cfg.LegRejectProbability > 1 {
		cfg.LegRejectProbability = 1
	}
	if cfg.LegRejectProbability == 0 && hcfg == zeroHistorical {
		cfg.LegRejectProbability = 0.01
	}
	if cfg.EmergencyHedgeSpreadMultiplier < 1 {
		cfg.EmergencyHedgeSpreadMultiplier = 4.0
	}
	if cfg.MaxFundingRate <= 0 {
		cfg.MaxFundingRate = 0.03
	}
	if cfg.MaxFundingRateJump <= 0 {
		cfg.MaxFundingRateJump = 0.015
	}
	if cfg.MaxSingleTradeLossUSDT <= 0 {
		cfg.MaxSingleTradeLossUSDT = math.Max(1, cfg.InitialEquityUSDT*0.075)
	}
	if cfg.SpotTakerFeeRate < 0 || (cfg.SpotTakerFeeRate == 0 && hcfg == zeroHistorical) {
		cfg.SpotTakerFeeRate = 0.0010
	}
	if cfg.FuturesTakerFeeRate < 0 || (cfg.FuturesTakerFeeRate == 0 && hcfg == zeroHistorical) {
		cfg.FuturesTakerFeeRate = 0.0005
	}
	if cfg.FundingPhaseLead <= 0 {
		cfg.FundingPhaseLead = r.cfg.StrategyConfig.MonitorStopLead
		if cfg.FundingPhaseLead <= 0 {
			cfg.FundingPhaseLead = 30 * time.Second
		}
	}
	if cfg.FundingFetchConcurrency <= 0 {
		cfg.FundingFetchConcurrency = 2
	}
	if cfg.FundingRetryMax <= 0 {
		cfg.FundingRetryMax = 6
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		recordDir := r.recordDir()
		if strings.TrimSpace(recordDir) == "" {
			recordDir = "records"
		}
		cfg.CacheDir = filepath.Join(recordDir, "historical_cache")
	}
	if cfg.ReferenceKlineLookback <= 0 {
		cfg.ReferenceKlineLookback = 60
	}
	if cfg.EstimateKlineLookback <= 0 {
		cfg.EstimateKlineLookback = 180
	}
	if cfg.SafetyBufferFloorUSDT <= 0 {
		cfg.SafetyBufferFloorUSDT = 0.001
	}
	if cfg.SafetyBufferCostMultiplier <= 0 {
		cfg.SafetyBufferCostMultiplier = 0.35
	}
	if cfg.MaxDrawdownRate <= 0 || cfg.MaxDrawdownRate >= 1 {
		cfg.MaxDrawdownRate = 0.25
	}
	if cfg.MaxConsecutiveLosses <= 0 {
		cfg.MaxConsecutiveLosses = 2
	}
	if cfg.FundingFetchSlot <= 0 {
		cfg.FundingFetchSlot = 0
	}
	if cfg.FundingFetchTimeout <= 0 {
		cfg.FundingFetchTimeout = 45 * time.Second
	}
	if cfg.GroupEvaluationTimeout <= 0 {
		cfg.GroupEvaluationTimeout = 90 * time.Second
	}
	if cfg.CandidateBuildTimeout <= 0 {
		cfg.CandidateBuildTimeout = 20 * time.Second
	}
	if cfg.CandidateFinalizeTimeout <= 0 {
		cfg.CandidateFinalizeTimeout = 20 * time.Second
	}
	if cfg.ScreeningConcurrency <= 0 {
		cfg.ScreeningConcurrency = 10
	}
	if cfg.DeepCandidateLimit <= 0 {
		cfg.DeepCandidateLimit = 24
	}
	if cfg.CoarseSymbolLimit <= 0 {
		cfg.CoarseSymbolLimit = maxInt(cfg.DeepCandidateLimit*4, monthlyFundingTopSymbolsPerTime*2)
	}
	if cfg.FundingGroupTopSymbolsPerTime <= 0 {
		cfg.FundingGroupTopSymbolsPerTime = monthlyFundingTopSymbolsPerTime
	}
	if cfg.CoarseSymbolLimit < cfg.FundingGroupTopSymbolsPerTime {
		cfg.CoarseSymbolLimit = cfg.FundingGroupTopSymbolsPerTime
	}
	if cfg.LiquidityShareMin <= 0 || cfg.LiquidityShareMin > 1 {
		cfg.LiquidityShareMin = 0.01
	}
	if cfg.LiquidityShareFloor <= 0 || cfg.LiquidityShareFloor > 1 {
		cfg.LiquidityShareFloor = 0.08
	}
	if cfg.LiquidityShareMax <= 0 || cfg.LiquidityShareMax > 1 {
		cfg.LiquidityShareMax = 0.25
	}
	if cfg.LiquidityShareMax < cfg.LiquidityShareMin {
		cfg.LiquidityShareMax = cfg.LiquidityShareMin
	}
	if cfg.SevereLiquidityShareCap <= 0 || cfg.SevereLiquidityShareCap > 1 {
		cfg.SevereLiquidityShareCap = 0.20
	}
	if cfg.SevereLiquidityShareMultiplier <= 1 {
		cfg.SevereLiquidityShareMultiplier = 2.5
	}
	if cfg.SevereLiquidityShareMax <= 0 || cfg.SevereLiquidityShareMax > 1 {
		cfg.SevereLiquidityShareMax = 0.50
	}
	if cfg.SevereLiquidityShareMax < cfg.SevereLiquidityShareCap {
		cfg.SevereLiquidityShareMax = cfg.SevereLiquidityShareCap
	}
	return cfg
}

func (r *BacktestRunner) monthlyExperimentPlans() []monthlyExperimentPlan {
	if !r.cfg.HistoricalConfig.BatchExperimentEnabled {
		return []monthlyExperimentPlan{{Index: 1, Total: 1, Cfg: r.cfg}}
	}
	spotFees := parseMonthlyFloatList(r.cfg.HistoricalConfig.BatchSpotTakerFeeRates, []float64{r.cfg.HistoricalConfig.SpotTakerFeeRate})
	perLegs := parseMonthlyFloatList(r.cfg.HistoricalConfig.BatchPerLegNotionalUSDT, []float64{r.cfg.HistoricalConfig.PerLegNotionalUSDT})
	holdingCycles := parseMonthlyIntList(r.cfg.HistoricalConfig.BatchMaxFundingCyclesHeld, []int{r.cfg.HistoricalConfig.MaxFundingCyclesHeld})
	plans := make([]monthlyExperimentPlan, 0, len(spotFees)*len(perLegs)*len(holdingCycles))
	index := 1
	for _, spotFee := range spotFees {
		for _, perLeg := range perLegs {
			for _, cycles := range holdingCycles {
				cfg := r.cfg
				cfg.HistoricalConfig.BatchExperimentEnabled = false
				cfg.HistoricalConfig.SpotTakerFeeRate = spotFee
				cfg.HistoricalConfig.PerLegNotionalUSDT = perLeg
				cfg.HistoricalConfig.MaxFundingCyclesHeld = maxInt(cycles, 1)
				cfg = alignHistoricalExperimentCapital(cfg)
				plans = append(plans, monthlyExperimentPlan{
					Index: index,
					Cfg:   cfg,
				})
				index++
			}
		}
	}
	for idx := range plans {
		plans[idx].Total = len(plans)
	}
	if len(plans) == 0 {
		return []monthlyExperimentPlan{{Index: 1, Total: 1, Cfg: r.cfg}}
	}
	return plans
}

func parseMonthlyFloatList(raw string, fallback []float64) []float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	values := make([]float64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value := parseFloat(part)
		key := fmt.Sprintf("%.8f", value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func parseMonthlyIntList(raw string, fallback []int) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	seen := make(map[int]struct{}, len(parts))
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value := int(parseFloat(part))
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func (r *BacktestRunner) runMonthlyExperimentPlans(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, groups []monthlyEventGroup, periodLabel string, fetchStats monthlyFundingFetchStats, experiments []monthlyExperimentPlan) ([]BacktestCSVRecord, []MonthlySimulationLogRecord, int, error) {
	allRecords := make([]BacktestCSVRecord, 0, len(experiments)*8)
	allLogs := make([]MonthlySimulationLogRecord, 0, len(experiments)*32)
	totalTrades := 0
	for _, experiment := range experiments {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, err
		}
		r.reportProgress(EngineStage("evaluate_groups"), fmt.Sprintf("实验 %d/%d: 现货费率=%.4f 单腿=%.2f funding=%d", experiment.Index, experiment.Total, experiment.Cfg.HistoricalConfig.SpotTakerFeeRate, experiment.Cfg.HistoricalConfig.PerLegNotionalUSDT, experiment.Cfg.HistoricalConfig.MaxFundingCyclesHeld), experiment.Index-1, experiment.Total)
		experimentRunner := &BacktestRunner{
			store:        r.store,
			logger:       r.logger,
			cfg:          experiment.Cfg,
			progressHook: nil,
		}
		tradeSamples, logs, simStats, stopReason, err := experimentRunner.simulateMonthlyFundingGroups(ctx, client, cache, groups, periodLabel)
		if err != nil {
			return nil, nil, 0, err
		}
		localFetchStats := fetchStats
		localFetchStats.RequestSummary = client.HistoricalRequestSummary()
		logs = append(logs, experimentRunner.buildMonthlyRunSummaryLogRecord(periodLabel, localFetchStats, simStats, stopReason, experiment))
		records := buildBacktestRecords(experimentRunner.cfg, tradeSamples)
		records = append(records, experimentRunner.buildMonthlySummaryRecord(periodLabel, tradeSamples, localFetchStats, simStats, stopReason))
		records = append(records, experimentRunner.buildMonthlyStressRecords(periodLabel, tradeSamples, localFetchStats, simStats)...)
		allLogs = append(allLogs, logs...)
		allRecords = append(allRecords, records...)
		totalTrades += len(tradeSamples)
	}
	return allRecords, allLogs, totalTrades, nil
}

func (r *BacktestRunner) monthlyHoldingCycles() int {
	return maxInt(1, r.historicalConfig().MaxFundingCyclesHeld)
}

func (r *BacktestRunner) monthlyExitTime(fundingTime time.Time) time.Time {
	cycles := r.monthlyHoldingCycles()
	return fundingTime.Add(time.Duration(cycles-1) * 8 * time.Hour)
}

func (r *BacktestRunner) monthlyScenarioName(base string) string {
	hcfg := r.historicalConfig()
	perLegLabel := hcfg.PerLegNotionalUSDT
	if perLegLabel <= 0 {
		perLegLabel = r.monthlyPerLegNotional()
	}
	return fmt.Sprintf("%s[mode=%s,spot=%.4f,futures=%.4f,per_leg=%.2f,hold=%d]", base, normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode), hcfg.SpotTakerFeeRate, hcfg.FuturesTakerFeeRate, perLegLabel, r.monthlyHoldingCycles())
}

func (r *BacktestRunner) monthlyExperimentNotes() []string {
	hcfg := r.historicalConfig()
	notes := []string{
		fmt.Sprintf("batchExperimentEnabled=%t", r.cfg.HistoricalConfig.BatchExperimentEnabled),
		"fundingTimeline=actual_history",
		"fundingDirectionMode=" + normalizeHistoricalFundingDirectionMode(hcfg.FundingDirectionMode),
		fmt.Sprintf("configuredPerLegNotionalUSDT=%.2f", r.cfg.HistoricalConfig.PerLegNotionalUSDT),
		fmt.Sprintf("spotFeeRate=%.4f", hcfg.SpotTakerFeeRate),
		fmt.Sprintf("futuresFeeRate=%.4f", hcfg.FuturesTakerFeeRate),
		fmt.Sprintf("assumedHourlyBorrowInterestRate=%.4f", hcfg.AssumedHourlyBorrowInterestRate),
		fmt.Sprintf("borrowInterestMinBillingHours=%d", hcfg.BorrowInterestMinBillingHours),
		fmt.Sprintf("negativeCarrySpotEntryImpactMultiplier=%.2f", hcfg.NegativeCarrySpotEntryImpactMultiplier),
		fmt.Sprintf("negativeCarrySpotExitImpactMultiplier=%.2f", hcfg.NegativeCarrySpotExitImpactMultiplier),
		fmt.Sprintf("crossStableQuoteMaxDeviationRate=%.4f", hcfg.CrossStableQuoteMaxDeviationRate),
		fmt.Sprintf("residualFillHaircutEnabled=%t", hcfg.ResidualFillHaircutEnabled),
		fmt.Sprintf("expectedLegFillRatio=%.4f", hcfg.ExpectedLegFillRatio),
		fmt.Sprintf("legRejectProbability=%.4f", hcfg.LegRejectProbability),
		fmt.Sprintf("emergencyHedgeSpreadMultiplier=%.2f", hcfg.EmergencyHedgeSpreadMultiplier),
		fmt.Sprintf("perLegNotionalUSDT=%.2f", r.monthlyPerLegNotional()),
		fmt.Sprintf("maxFundingCyclesHeld=%d", r.monthlyHoldingCycles()),
	}
	if len(r.borrowHourlyInterestByAsset) > 0 {
		notes = append(notes, fmt.Sprintf("borrowHourlyRateAssets=%d", len(r.borrowHourlyInterestByAsset)))
	}
	return notes
}

func (r *BacktestRunner) hourlyBorrowInterestRateForSymbol(client *BinanceClient, symbol string) float64 {
	rate := r.historicalConfig().AssumedHourlyBorrowInterestRate
	if r == nil || client == nil || len(r.borrowHourlyInterestByAsset) == 0 {
		return rate
	}
	if meta, ok := client.HistoricalSpotMetaForFuture(symbol); ok {
		asset := strings.ToUpper(strings.TrimSpace(meta.BaseAsset))
		if asset != "" {
			if actualRate, ok := r.borrowHourlyInterestByAsset[asset]; ok && actualRate > 0 {
				return actualRate
			}
		}
	}
	return rate
}

func (r *BacktestRunner) sumMonthlyFundingIncome(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, direction fundingCarryDirection, hedgeQty float64, markPrice float64, fundingTime time.Time) (float64, error) {
	if hedgeQty <= 0 || markPrice <= 0 {
		return 0, nil
	}
	schedule, err := cache.getFundingSchedule(ctx, client, symbol, fundingTime, r.monthlyHoldingCycles())
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, item := range schedule {
		total += historicalFundingIncome(direction, hedgeQty, markPrice, parseFloat(item.FundingRate), 1)
	}
	return total, nil
}

func (r *BacktestRunner) monthlySafetyBuffer(entryExecutionCost float64, projectedExitCost float64, openBasisPenalty float64) float64 {
	hcfg := r.historicalConfig()
	explicitCost := math.Max(0, entryExecutionCost)*0.50 +
		math.Max(0, projectedExitCost)*0.25 +
		math.Max(0, openBasisPenalty)*0.50
	return math.Max(hcfg.SafetyBufferFloorUSDT, explicitCost*hcfg.SafetyBufferCostMultiplier)
}

func (r *BacktestRunner) monthlyPerLegNotional() float64 {
	hcfg := r.historicalConfig()
	notional := hcfg.PerLegNotionalUSDT
	// 非批量实验时与实盘 Strategy.PerSymbolQuoteUSDT 对齐，避免 10/20 USDT 玩具规模自欺。
	if !r.cfg.HistoricalConfig.BatchExperimentEnabled {
		if r.cfg.StrategyConfig.PerSymbolQuoteUSDT > 0 {
			notional = r.cfg.StrategyConfig.PerSymbolQuoteUSDT
		}
	} else if r.cfg.StrategyConfig.PerSymbolQuoteUSDT > 0 {
		notional = math.Min(notional, r.cfg.StrategyConfig.PerSymbolQuoteUSDT)
	}
	maxPerLegByEquity := hcfg.InitialEquityUSDT * hcfg.MaxPerLegEquityShare
	if maxPerLegByEquity > 0 {
		notional = math.Min(notional, maxPerLegByEquity)
	}
	return math.Max(1, notional)
}

// monthlyHedgeQty 按与实盘相同的步长/最小数量/最小名义规则计算对冲数量。
// 规则缺失时退化为精度截断，仍保证数量为正。
func monthlyHedgeQty(perLegNotional, spotPrice, futuresPrice float64, spotRules, futuresRules SymbolTradeRules) (float64, string) {
	if perLegNotional <= 0 || spotPrice <= 0 || futuresPrice <= 0 {
		return 0, "invalid_price_or_notional"
	}
	raw := math.Min(perLegNotional/spotPrice, perLegNotional/futuresPrice)
	qty := normalizeQuantity(raw, futuresRules, 3)
	if qty <= 0 {
		return 0, "hedge_qty_below_step"
	}
	refPrice := math.Max(spotPrice, futuresPrice)
	if !isQuantityTradable(qty, refPrice, futuresRules) {
		return 0, "hedge_qty_below_min_notional"
	}
	if spotRules.StepSize > 0 || spotRules.MinQty > 0 || spotRules.MinNotional > 0 {
		spotQty := normalizeQuantity(raw, spotRules, 3)
		if spotQty > 0 && spotQty < qty {
			qty = spotQty
		}
		if !isQuantityTradable(qty, refPrice, spotRules) {
			return 0, "hedge_qty_below_min_notional"
		}
	}
	return qty, ""
}

func (r *BacktestRunner) monthlyLiquidityShareLimit() float64 {
	hcfg := r.historicalConfig()
	limit := r.cfg.MaxVisibleDepthShare
	if limit <= 0 {
		limit = hcfg.LiquidityShareFloor
	}
	return math.Max(hcfg.LiquidityShareMin, math.Min(hcfg.LiquidityShareMax, limit))
}

func (r *BacktestRunner) monthlySevereLiquidityShareLimit(baseLimit float64) float64 {
	hcfg := r.historicalConfig()
	if baseLimit <= 0 {
		baseLimit = r.monthlyLiquidityShareLimit()
	}
	return math.Max(hcfg.SevereLiquidityShareCap, math.Min(hcfg.SevereLiquidityShareMax, baseLimit*hcfg.SevereLiquidityShareMultiplier))
}

func (r *BacktestRunner) monthlyScreeningCandidateLimit(groupSize int) int {
	if groupSize <= 0 {
		return 0
	}
	hcfg := r.historicalConfig()
	screeningRounds := int(hcfg.GroupEvaluationTimeout / maxDuration(hcfg.CandidateBuildTimeout, time.Second))
	if screeningRounds <= 1 {
		screeningRounds = 2
	}
	limitByBudget := hcfg.ScreeningConcurrency * maxInt(2, screeningRounds-1)
	limit := maxInt(hcfg.DeepCandidateLimit*2, limitByBudget)
	if limit < hcfg.DeepCandidateLimit {
		limit = hcfg.DeepCandidateLimit
	}
	limit = minInt(limit, maxInt(hcfg.DeepCandidateLimit*3, 72))
	return minInt(groupSize, limit)
}

func (r *BacktestRunner) monthlyCoarseSymbolLimit(totalSymbols int) int {
	if totalSymbols <= 0 {
		return 0
	}
	limit := r.historicalConfig().CoarseSymbolLimit
	if limit <= 0 {
		limit = maxInt(r.historicalConfig().DeepCandidateLimit*4, monthlyFundingTopSymbolsPerTime*2)
	}
	return minInt(totalSymbols, limit)
}

func trimMonthlyFundingGroupItems(items []FundingRateHistoryItem, limit int) []FundingRateHistoryItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	trimmed := make([]FundingRateHistoryItem, limit)
	copy(trimmed, items[:limit])
	return trimmed
}

// sortFundingItemsBySymbol 按 symbol 名做确定性排序。
// 用于「点位预估之前」的分组阶段——此时没有点位预估信号，任何基于已实现费率的排序都会引入前视偏差，
// 因此这里只做与结果无关的字典序排序，把真正的择优交给下游的点位预估预选。
func sortFundingItemsBySymbol(items []FundingRateHistoryItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Symbol < items[j].Symbol
	})
}

// monthlyPreEstimateUniverseCap 是「点位预估之前」候选宇宙的兜底上限。
// 正常情况下 coarse 粗筛已把 symbol 数限制在 CoarseSymbolLimit 内，此处只在 coarse 降级(回退全量)时
// 兜底防止候选爆炸。取值不低于 FundingGroupTopSymbolsPerTime，确保下游点位预估仍有足够候选可择优。
func (r *BacktestRunner) monthlyPreEstimateUniverseCap() int {
	hcfg := r.historicalConfig()
	cap := hcfg.CoarseSymbolLimit
	if cap < hcfg.FundingGroupTopSymbolsPerTime {
		cap = hcfg.FundingGroupTopSymbolsPerTime
	}
	if cap <= 0 {
		cap = monthlyFundingTopSymbolsPerTime
	}
	return cap
}
