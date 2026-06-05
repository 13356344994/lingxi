package app

import (
	"context"
	"fmt"
	"lingxi/internal/fillmodel"
	"math"
	"strings"
	"sync"
	"time"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载开仓前的成本/资金费稳定性/余额保障类校验
// （目标成本校验、借贷成本、资金费稳定性采样、计划拆分与并发策略、USDT/BNB 余额准备、
// 平仓窗口等待，同包搬移、行为不变），向单文件 ≤ 800 行门禁靠拢。

func (e *FundingRateSniperEngine) checkTargetCost(ctx context.Context, target *MonitoredTarget, consecutiveBreaches *int) {
	if err := e.refreshTargetFundingEstimate(ctx, target, false); err != nil {
		e.logger.Printf("监控 %s 时刷新资金费预估失败: %v", target.Candidate.Symbol, err)
		return
	}
	spotDepth, futuresDepth, err := e.fetchDualOrderBookDepth(ctx, target.Candidate.Pair, target.Candidate.Symbol)
	if err != nil {
		e.logger.Printf("监控 %s 时获取深度盘口失败: %v", target.Candidate.Symbol, err)
		return
	}

	futuresQty := target.Candidate.OrderQuantity
	profile := GetSymbolRiskProfile(target.Candidate.Symbol)
	if futuresQty <= 0 {
		futuresRule := e.futuresRules[target.Candidate.Symbol]
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / target.Candidate.MarkPrice
		futuresQty = normalizeQuantity(rawOrderQty, futuresRule, target.Candidate.QuantityPrecision)
		target.Candidate.OrderQuantity = futuresQty
	}
	if futuresQty <= 0 {
		return
	}
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)

	spotRule := e.spotRules[target.Candidate.Pair]
	spotQty := normalizeQuantity(futuresQty, spotRule, target.Candidate.SpotBasePrecision)
	requiredQty := math.Min(futuresQty, spotQty)
	if requiredQty <= 0 {
		return
	}

	spotLevels, futuresLevels := openDepthLevelsForDirection(direction, spotDepth, futuresDepth)
	spotVWAP, spotBestAsk, spotFilled, spotEnough := computeAverageFillPrice(spotLevels, requiredQty)
	futuresVWAP, futuresBestBid, futuresFilled, futuresEnough := computeAverageFillPrice(futuresLevels, requiredQty)
	if !spotEnough || !futuresEnough || spotVWAP <= 0 || futuresVWAP <= 0 {
		shortQty := requiredQty - math.Min(spotFilled, futuresFilled)
		coverageRatio := 0.0
		if requiredQty > 0 {
			coverageRatio = math.Min(spotFilled/requiredQty, futuresFilled/requiredQty)
		}
		risk := EvaluateExecutionCoverageRisk("depth", coverageRatio)
		e.handleMarketRisk(target, consecutiveBreaches, -1, risk, fmt.Sprintf("连续观察阶段发现盘口深度不足，coverage=%.4f 所需数量=%.8f 缺口=%.8f", coverageRatio, requiredQty, shortQty))
		return
	}
	if risk, detail, unstable, spotVisible, futuresVisible, stabilityRatio := e.evaluateOpenDepthStability(target, direction, spotDepth, futuresDepth, spotBestAsk, futuresBestBid); unstable {
		target.LastSpotVisibleNotional = spotVisible
		target.LastFuturesVisibleNotional = futuresVisible
		target.LastDepthStabilityRatio = stabilityRatio
		e.handleMarketRisk(target, consecutiveBreaches, target.LastNetProfit, risk, detail)
		return
	}

	// 真实开仓方向按 funding carry direction 选择现货/合约对应吃单侧，再按该方向评估入场冲击成本。
	spread := openEntrySpreadRate(direction, spotVWAP, futuresVWAP)
	borrowReserveRate := e.liveBorrowReserveRate(target.Candidate, spotVWAP, e.serverNow())
	borrowReserveUSDT := target.Candidate.OrderQuantity * spotVWAP * borrowReserveRate
	roundTripNotional := target.Candidate.OrderQuantity * spotVWAP
	residualReserveRate, residualReserveUSDT := e.liveResidualReserveRate(roundTripNotional, spread)
	spotVisibleNotional, futuresVisibleNotional := visibleDepthNotionalForDirection(direction, spotDepth, futuresDepth)
	baseNetProfit := estimateReservedNetProfitRate(target.Candidate.EstimatedRate, spread, e.cfg.StrategyConfig.FeeReserveRate, e.cfg.StrategyConfig.ExitSpreadReserveRate, borrowReserveRate) - residualReserveRate
	// 冲击成本入闸(M3.2/§16.3):默认出厂姿态始终把往返市场冲击纳入盈亏平衡(ImpactReserveInGateEnabled),
	// 与 DynamicSizing 解耦;任一开启即扣冲击。置 false 退回旧的「仅 DynamicSizing 才扣」行为。
	reserveImpact := e.cfg.StrategyConfig.ImpactReserveInGateEnabled || e.cfg.PortfolioConfig.DynamicSizingEnabled
	netProfit, impactReserveRate := impactAdjustedNetProfitRate(baseNetProfit, reserveImpact, roundTripNotional, spotVisibleNotional, futuresVisibleNotional)

	target.LastSpotAsk = spotBestAsk
	target.LastFuturesBid = futuresBestBid
	target.LastSpotVWAP = spotVWAP
	target.LastFuturesVWAP = futuresVWAP
	target.LastSpotVisibleNotional = spotVisibleNotional
	target.LastFuturesVisibleNotional = futuresVisibleNotional
	target.LastSpread = spread
	target.LastNetProfit = netProfit
	target.LastBorrowReserveRate = borrowReserveRate
	target.LastBorrowReserveUSDT = borrowReserveUSDT
	target.LastResidualReserveRate = residualReserveRate
	target.LastResidualReserveUSDT = residualReserveUSDT
	target.LastImpactReserveRate = impactReserveRate
	target.LastImpactReserveUSDT = impactReserveRate * roundTripNotional
	target.LastObserveTime = time.Now()
	e.updateCurrentMonitorTargetLabel(target)

	strategy := NewDefaultFundingStrategy()
	action, detail := strategy.EvaluateMonitorTick(target.Candidate, netProfit, profile, e.cfg, *consecutiveBreaches)

	if action == StrategyActionReject {
		e.invalidateMonitorTarget(target, netProfit, detail)
		return
	}

	if action == StrategyActionObserve {
		e.applyMonitorObserveBreach(target, consecutiveBreaches, netProfit, detail)
		return
	}

	*consecutiveBreaches = 0
	target.BreachCount = 0
	e.transitionMonitorState(target, "healthy", detail)
}

func estimateReservedNetProfitRate(estimatedFundingRate float64, entrySpreadRate float64, roundTripFeeRate float64, exitSpreadReserveRate float64, borrowReserveRate float64) float64 {
	return estimatedFundingRate - entrySpreadRate - roundTripFeeRate - maxFloat(exitSpreadReserveRate, 0) - maxFloat(borrowReserveRate, 0)
}

// liveResidualReserveRate 用引擎与回测共享的 fillmodel.CostModel，按当前盘口入场价差/费用/退出价差
// 预留口径，估算一次往返对冲因「无法 100% 同量成交 / 单腿被拒」而需要预留的残腿期望成本，返回
// （相对往返名义的预留率, 预留 USDT）。这样实盘的盈利门禁与回测计提同一套残腿成本，不再乐观假设
// 完美对冲（Gate 3：回测↔实盘共注入同一套成交/费用模型）。
func (e *FundingRateSniperEngine) liveResidualReserveRate(roundTripNotional float64, entrySpreadRate float64) (float64, float64) {
	if e.costModel == nil || roundTripNotional <= 0 {
		return 0, 0
	}
	feeRate := maxFloat(e.cfg.StrategyConfig.FeeReserveRate, 0)
	exitReserve := maxFloat(e.cfg.StrategyConfig.ExitSpreadReserveRate, 0)
	openBasis := maxFloat(entrySpreadRate, 0)
	breakdown := e.costModel.ResidualFillCost(fillmodel.RoundTrip{
		OpenNotional:      roundTripNotional,
		CloseNotional:     roundTripNotional,
		OpenFrictionRate:  feeRate + openBasis,
		CloseFrictionRate: feeRate + exitReserve,
		OpenBasisRate:     openBasis,
		CloseBasisRate:    exitReserve,
	})
	if breakdown.HaircutUSDT <= 0 {
		return 0, 0
	}
	return breakdown.HaircutUSDT / roundTripNotional, breakdown.HaircutUSDT
}

func (e *FundingRateSniperEngine) hourlyBorrowInterestRateForAsset(asset string) float64 {
	rate := e.cfg.HistoricalConfig.AssumedHourlyBorrowInterestRate
	if e == nil || len(e.borrowHourlyInterestByAsset) == 0 {
		return rate
	}
	token := strings.ToUpper(strings.TrimSpace(asset))
	if token == "" {
		return rate
	}
	if actualRate, ok := e.borrowHourlyInterestByAsset[token]; ok && actualRate > 0 {
		return actualRate
	}
	return rate
}

func (e *FundingRateSniperEngine) liveBorrowReserveCost(candidate FundingCandidate, spotReferencePrice float64, entryTime time.Time) float64 {
	if spotReferencePrice <= 0 {
		spotReferencePrice = candidate.MarkPrice
	}
	if spotReferencePrice <= 0 || candidate.OrderQuantity <= 0 {
		return 0
	}
	direction := resolvedFundingCarryDirection(candidate.Direction, candidate.EstimatedRate)
	exitTime := candidate.NextFundingTime
	if exitTime.IsZero() || !exitTime.After(entryTime) {
		exitTime = entryTime
	}
	spotNotional := candidate.OrderQuantity * spotReferencePrice
	billedHours := historicalBorrowInterestHours(direction, entryTime, exitTime, e.cfg.HistoricalConfig.BorrowInterestMinBillingHours)
	return historicalBorrowInterestCost(direction, spotNotional, e.hourlyBorrowInterestRateForAsset(candidate.SpotBaseAsset), billedHours)
}

// settlementBorrowCost 计算一次负 carry 对冲在「实际持仓期」内累计的真实借币利息(USDT)。
// 正 carry 不借币，返回 0。利息 = 借入名义(开仓现货名义 OpenSpotQuoteQty) × 每小时利率(优先真实 per-asset 利率)
// × 实际计费小时数(开仓到平仓，按小时向上取整，至少 BorrowInterestMinBillingHours)。
// 开仓时间取自现货成交回执 TransactTime；缺失时退化为最小计费小时，保证利息不被低估为 0。
// 这样负 carry 的核心成本不再被结算静默记为 0，落袋净利与实盘借币摩擦更一致。
func (e *FundingRateSniperEngine) settlementBorrowCost(hedge OpenHedge, closeTime time.Time) float64 {
	direction := hedge.carryDirection()
	if direction != fundingCarryDirectionNegative {
		return 0
	}
	borrowedNotional := hedge.OpenSpotQuoteQty
	if borrowedNotional <= 0 {
		return 0
	}
	entryTime := closeTime
	if hedge.SpotOrder.TransactTime > 0 {
		entryTime = time.UnixMilli(hedge.SpotOrder.TransactTime).UTC()
	}
	billedHours := historicalBorrowInterestHours(direction, entryTime, closeTime, e.cfg.HistoricalConfig.BorrowInterestMinBillingHours)
	hourlyRate := e.hourlyBorrowInterestRateForAsset(hedge.Target.SpotBaseAsset)
	return historicalBorrowInterestCost(direction, borrowedNotional, hourlyRate, billedHours)
}

func (e *FundingRateSniperEngine) liveBorrowReserveRate(candidate FundingCandidate, spotReferencePrice float64, entryTime time.Time) float64 {
	if spotReferencePrice <= 0 {
		spotReferencePrice = candidate.MarkPrice
	}
	spotNotional := candidate.OrderQuantity * spotReferencePrice
	if spotNotional <= 0 {
		return 0
	}
	return e.liveBorrowReserveCost(candidate, spotReferencePrice, entryTime) / spotNotional
}

const (
	preOpenFundingStabilitySamples       = 3
	preOpenFundingStabilityMinSamples    = 1
	preOpenFundingStabilityMaxInterval   = 250 * time.Millisecond
	preOpenFundingStabilityMinInterval   = 75 * time.Millisecond
	preOpenFundingStabilityRefreshBudget = 400 * time.Millisecond
)

func fundingStabilitySampleCount(window time.Duration) int {
	switch {
	case window < 250*time.Millisecond:
		return preOpenFundingStabilityMinSamples
	case window < 500*time.Millisecond:
		return 2
	default:
		return preOpenFundingStabilitySamples
	}
}

func fundingStabilitySleepInterval(window time.Duration, samples int) time.Duration {
	if samples <= 1 || window <= 0 {
		return 0
	}
	interval := window / time.Duration(samples)
	if interval > preOpenFundingStabilityMaxInterval {
		interval = preOpenFundingStabilityMaxInterval
	}
	if interval < preOpenFundingStabilityMinInterval {
		interval = preOpenFundingStabilityMinInterval
	}
	return interval
}

func (e *FundingRateSniperEngine) filterOpenPlansByFundingStability(ctx context.Context, plans []openSequencePlan, fireAt time.Time) ([]openSequencePlan, []string) {
	if len(plans) == 0 {
		return nil, nil
	}
	filtered := make([]openSequencePlan, 0, len(plans))
	alerts := make([]string, 0, len(plans))
	for _, plan := range plans {
		ok, detail := e.verifyPreparedPlanFundingStability(ctx, &plan, fireAt)
		if ok {
			filtered = append(filtered, plan)
			continue
		}
		if plan.target != nil {
			e.invalidateMonitorTarget(plan.target, plan.target.LastNetProfit, fmt.Sprintf("战前 funding 稳定性门未通过: %s", detail))
		}
		alerts = append(alerts, fmt.Sprintf("%s %s", plan.target.Candidate.Symbol, detail))
	}
	return filtered, alerts
}

func (e *FundingRateSniperEngine) verifyPreparedPlanFundingStability(ctx context.Context, plan *openSequencePlan, fireAt time.Time) (bool, string) {
	if e == nil || plan == nil || plan.target == nil {
		return false, "plan 为空"
	}
	target := plan.target
	profile := GetSymbolRiskProfile(target.Candidate.Symbol)
	baselineDirection := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if baselineDirection == "" {
		return false, "目标方向为空"
	}
	window := fireAt.Sub(e.serverNow())
	if window < 0 {
		window = 0
	}
	samples := fundingStabilitySampleCount(window)
	interval := fundingStabilitySleepInterval(window, samples)
	lastRate := target.Candidate.EstimatedRate
	lastJump := target.Candidate.FundingRateJump
	lastBasis := target.Candidate.BasisRate
	for sample := 0; sample < samples; sample++ {
		refreshBudget := preOpenFundingStabilityRefreshBudget
		if remaining := fireAt.Sub(e.serverNow()); !fireAt.IsZero() && remaining > 0 {
			refreshBudget = minDurationValue(refreshBudget, remaining)
		}
		if refreshBudget <= 0 {
			refreshBudget = preOpenFundingStabilityMinInterval
		}
		refreshCtx, cancel := context.WithTimeout(ctx, refreshBudget)
		err := e.refreshTargetFundingEstimate(refreshCtx, target, true)
		cancel()
		if err != nil {
			return false, fmt.Sprintf("sample=%d funding refresh 失败: %v", sample+1, err)
		}
		currentDirection := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
		lastRate = target.Candidate.EstimatedRate
		lastJump = target.Candidate.FundingRateJump
		lastBasis = target.Candidate.BasisRate
		if currentDirection != baselineDirection {
			return false, fmt.Sprintf("sample=%d funding 方向翻转: baseline=%s current=%s rate=%.6f", sample+1, baselineDirection, currentDirection, target.Candidate.EstimatedRate)
		}
		strategy := NewDefaultFundingStrategy()
		action, _ := strategy.EvaluateScanCandidate(
			target.Candidate,
			FundingSnapshot{
				EstimatedRate: target.Candidate.EstimatedRate,
				Direction:     currentDirection,
				FundingJump:   target.Candidate.FundingRateJump,
				BasisRate:     target.Candidate.BasisRate,
			},
			profile,
			e.cfg,
		)
		if action == StrategyActionReject {
			return false, fmt.Sprintf("sample=%d funding 预估不再满足入场门槛: rate=%.6f", sample+1, target.Candidate.EstimatedRate)
		}
		if math.Abs(target.Candidate.BasisRate) > e.cfg.StrategyConfig.MaxScanBasisRate {
			return false, fmt.Sprintf("sample=%d basis 超阈值: basis=%.6f", sample+1, target.Candidate.BasisRate)
		}
		if sample+1 < samples && interval > 0 {
			if !sleepWithContext(ctx, interval) {
				return false, "funding 稳定性采样被取消"
			}
		}
	}
	return true, fmt.Sprintf("samples=%d direction=%s rate=%.6f jump=%.6f basis=%.6f", samples, baselineDirection, lastRate, lastJump, lastBasis)
}

func newOpenSequenceBuildState(beforeSnapshot CycleAccountSnapshot, cfg EngineConfig) openSequenceBuildState {
	projected := make(map[string]float64, len(beforeSnapshot.FuturesPositionAmounts))
	for symbol, qty := range beforeSnapshot.FuturesPositionAmounts {
		projected[strings.ToUpper(strings.TrimSpace(symbol))] = math.Abs(qty)
	}
	return openSequenceBuildState{
		remainingSpotUSDT:      beforeSnapshot.SpotUSDTFree,
		remainingFuturesUSDT:   beforeSnapshot.FuturesUSDTAvailable,
		remainingCycleNotional: cfg.RiskConfig.MaxCycleOpenNotionalUSDT,
		projectedFuturesAbsQty: projected,
	}
}

func effectiveOpenHedgeMaxConcurrency(cfg EngineConfig) int {
	if cfg.ExecutionConfig.OpenHedgeMaxConcurrency > 0 {
		return cfg.ExecutionConfig.OpenHedgeMaxConcurrency
	}
	return 1
}

func configuredSymbolMatch(rawList string, symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return false
	}
	for _, raw := range strings.Split(rawList, ",") {
		token := strings.ToUpper(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		if token == symbol || token == futuresBaseAsset(FuturesSymbol{Symbol: symbol, Pair: symbol, MarginAsset: "USDT"}) {
			return true
		}
	}
	return false
}

func shouldUseParallelOpenHedge(cfg EngineConfig, symbol string) bool {
	return configuredSymbolMatch(cfg.ExecutionConfig.ParallelOpenSymbols, symbol)
}

func shouldUseParallelCloseHedge(cfg EngineConfig, symbol string) bool {
	rawList := cfg.ExecutionConfig.ParallelCloseSymbols
	if strings.TrimSpace(rawList) == "" {
		rawList = cfg.ExecutionConfig.ParallelOpenSymbols
	}
	return configuredSymbolMatch(rawList, symbol)
}

func splitOpenSequencePlans(cfg EngineConfig, plans []openSequencePlan) ([]openSequencePlan, []openSequencePlan) {
	sequential := make([]openSequencePlan, 0, len(plans))
	parallel := make([]openSequencePlan, 0, len(plans))
	for _, plan := range plans {
		if shouldUseParallelOpenHedge(cfg, plan.target.Candidate.Symbol) {
			parallel = append(parallel, plan)
			continue
		}
		sequential = append(sequential, plan)
	}
	return sequential, parallel
}

func splitCloseSequencePlans(cfg EngineConfig, plans []closeSequencePlan) ([]closeSequencePlan, []closeSequencePlan) {
	sequential := make([]closeSequencePlan, 0, len(plans))
	parallel := make([]closeSequencePlan, 0, len(plans))
	for _, plan := range plans {
		if shouldUseParallelCloseHedge(cfg, plan.hedge.Target.Symbol) {
			parallel = append(parallel, plan)
			continue
		}
		sequential = append(sequential, plan)
	}
	return sequential, parallel
}

func (e *FundingRateSniperEngine) isLiveSpotPairFundable(pair string) bool {
	meta, ok := e.spotMeta[pair]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(meta.QuoteAsset), strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset))
}

func (e *FundingRateSniperEngine) desiredOpenUSDTPerAccount(targets []*MonitoredTarget) float64 {
	total := 0.0
	for _, target := range targets {
		total += e.cfg.StrategyConfig.PerSymbolQuoteUSDT * GetSymbolRiskProfile(target.Candidate.Symbol).PerSymbolQuoteMultiplier
	}
	if total <= 0 {
		total = float64(len(targets)) * e.cfg.StrategyConfig.PerSymbolQuoteUSDT
	}
	if e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0 && total > e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT {
		total = e.cfg.RiskConfig.MaxCycleOpenNotionalUSDT
	}
	return maxFloat(total, 0)
}

func (e *FundingRateSniperEngine) ensureUSDTFundingBalancesForOpen(ctx context.Context, targets []*MonitoredTarget) error {
	if !e.cfg.ExecutionConfig.AutoBalanceUSDT || len(targets) == 0 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset), "USDT") {
		return nil
	}

	_, spotUSDT, err := e.fetchSpotAccountBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货 USDT 余额失败: %w", err)
	}
	_, futuresAvailableUSDT, _, err := e.fetchFuturesAccountState(ctx)
	if err != nil {
		return fmt.Errorf("读取合约 USDT 余额失败: %w", err)
	}
	totalUSDT := spotUSDT + futuresAvailableUSDT
	if totalUSDT <= executionQtyTolerance {
		return nil
	}

	perAccountTarget := e.desiredOpenUSDTPerAccount(targets)
	if perAccountTarget <= executionQtyTolerance {
		return nil
	}
	if totalUSDT < 2*perAccountTarget {
		perAccountTarget = totalUSDT / 2
	}

	const transferEpsilon = 1e-6
	switch {
	case spotUSDT+transferEpsilon < perAccountTarget && futuresAvailableUSDT > perAccountTarget+transferEpsilon:
		transferAmount := math.Min(perAccountTarget-spotUSDT, futuresAvailableUSDT-perAccountTarget)
		if transferAmount <= transferEpsilon {
			return nil
		}
		e.logger.Printf("模块四开仓前自动平衡 USDT，从 UM futures 转到 spot，amount=%.6f", transferAmount)
		return e.transferAssetBetweenAccounts(ctx, "UMFUTURE_MAIN", "USDT", transferAmount)
	case futuresAvailableUSDT+transferEpsilon < perAccountTarget && spotUSDT > perAccountTarget+transferEpsilon:
		transferAmount := math.Min(perAccountTarget-futuresAvailableUSDT, spotUSDT-perAccountTarget)
		if transferAmount <= transferEpsilon {
			return nil
		}
		e.logger.Printf("模块四开仓前自动平衡 USDT，从 spot 转到 UM futures，amount=%.6f", transferAmount)
		return e.transferAssetBetweenAccounts(ctx, "MAIN_UMFUTURE", "USDT", transferAmount)
	default:
		return nil
	}
}

func (e *FundingRateSniperEngine) ensureSpotBNBFeeCoverageForOpen(ctx context.Context, targets []*MonitoredTarget) error {
	if len(targets) == 0 {
		return nil
	}
	freeBalances, _, err := e.fetchSpotAccountFreeBalances(ctx)
	if err != nil {
		return fmt.Errorf("读取现货账户余额失败: %w", err)
	}
	bnbFree := freeBalances["BNB"]
	if bnbFree <= executionQtyTolerance {
		return fmt.Errorf("现货账户 BNB 可用余额不足，已拒绝开仓以避免手续费从基础资产扣除")
	}
	bnbPriceUSDT, err := e.fetchSpotAssetUSDTPrice(ctx, "BNB")
	if err != nil {
		return fmt.Errorf("获取 BNB/USDT 价格失败，无法校验手续费覆盖: %w", err)
	}
	if bnbPriceUSDT <= 0 {
		return fmt.Errorf("获取 BNB/USDT 价格失败，无法校验手续费覆盖: 无效价格 %.8f", bnbPriceUSDT)
	}
	requiredFeeUSDT := 0.0
	for _, target := range targets {
		if target == nil {
			continue
		}
		referencePrice := maxFloat(target.LastSpotAsk, target.Candidate.MarkPrice)
		if referencePrice <= 0 {
			referencePrice = target.LastSpotVWAP
		}
		if referencePrice <= 0 || target.Candidate.OrderQuantity <= 0 {
			continue
		}
		requiredFeeUSDT += target.Candidate.OrderQuantity * referencePrice * e.cfg.HistoricalConfig.SpotTakerFeeRate
	}
	if requiredFeeUSDT <= executionQtyTolerance {
		return nil
	}
	requiredBNB := (requiredFeeUSDT * spotFeeBNBSafetyMultiplier) / bnbPriceUSDT
	if bnbFree+executionQtyTolerance < requiredBNB {
		return fmt.Errorf(
			"现货账户 BNB 可用余额不足，已拒绝开仓: free=%.8f required=%.8f fee_usdt=%.6f bnb_price=%.6f",
			bnbFree,
			requiredBNB,
			requiredFeeUSDT,
			bnbPriceUSDT,
		)
	}
	return nil
}

func (e *FundingRateSniperEngine) waitForCloseExecutionWindow(ctx context.Context, hedges []OpenHedge, fundingTime time.Time) (time.Time, []string, bool, error) {
	alerts := make([]string, 0, 2)
	if syncErr := e.syncServerTimeOffset(ctx); syncErr != nil {
		e.logger.Printf("模块五平仓前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	if e.serverNow().Before(fundingTime) {
		if err := e.sleepUntilServerTime(ctx, fundingTime); err != nil {
			return time.Time{}, nil, false, err
		}
	}

	deadline := e.scheduler().CloseDeadline(fundingTime)
	interval := e.cfg.ExecutionConfig.ExitMonitorInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	var lastMonitorErr error
	for {
		if err := e.checkLiveSafetyGuards("close_monitor"); err != nil {
			return time.Time{}, nil, false, err
		}
		now := e.serverNow()
		ready, worstAdverseSpread, monitorErr := e.closeWindowReady(ctx, hedges)
		if monitorErr == nil && ready {
			return now, alerts, false, nil
		}
		if monitorErr != nil {
			lastMonitorErr = monitorErr
			e.logger.Printf("模块五退出监控暂未就绪: %v", monitorErr)
		}
		if !now.Before(deadline) {
			preferPassiveSpot := false
			if lastMonitorErr != nil {
				alerts = append(alerts, fmt.Sprintf("退出监控到达最晚时限，忽略最近盘口异常: %v", lastMonitorErr))
			}
			if worstAdverseSpread > e.cfg.StrategyConfig.ExitSpreadReserveRate {
				alerts = append(alerts, fmt.Sprintf("退出监控到达最晚时限，按强制平仓执行: adverseSpread=%.6f reserve=%.6f", worstAdverseSpread, e.cfg.StrategyConfig.ExitSpreadReserveRate))
				preferPassiveSpot = true
			}
			return now, alerts, preferPassiveSpot, nil
		}
		nextCheckAt := now.Add(interval)
		if nextCheckAt.After(deadline) {
			nextCheckAt = deadline
		}
		timer := time.NewTimer(time.Until(nextCheckAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Time{}, nil, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *FundingRateSniperEngine) closeWindowReady(ctx context.Context, hedges []OpenHedge) (bool, float64, error) {
	worstAdverseSpread := 0.0
	for _, hedge := range hedges {
		spotTicker, futuresTicker, err := e.fetchDualBookTicker(ctx, hedge.Target.Pair, hedge.Target.Symbol)
		if err != nil {
			return false, worstAdverseSpread, err
		}
		adverseSpread, ok := adverseCloseSpreadRate(hedge.carryDirection(), spotTicker, futuresTicker)
		if !ok {
			return false, worstAdverseSpread, fmt.Errorf("退出监控盘口异常: symbol=%s", hedge.Target.Symbol)
		}
		if adverseSpread > worstAdverseSpread {
			worstAdverseSpread = adverseSpread
		}
		if adverseSpread > e.cfg.StrategyConfig.ExitSpreadReserveRate {
			return false, worstAdverseSpread, nil
		}
	}
	return true, worstAdverseSpread, nil
}

func adverseCloseSpreadRate(direction fundingCarryDirection, spotTicker BookTicker, futuresTicker BookTicker) (float64, bool) {
	switch resolvedFundingCarryDirection(direction, 0) {
	case fundingCarryDirectionNegative:
		spotAsk := parseFloat(spotTicker.AskPrice)
		futuresBid := parseFloat(futuresTicker.BidPrice)
		if spotAsk <= 0 || futuresBid <= 0 {
			return 0, false
		}
		if spotAsk <= futuresBid {
			return 0, true
		}
		return (spotAsk - futuresBid) / futuresBid, true
	default:
		spotBid := parseFloat(spotTicker.BidPrice)
		futuresAsk := parseFloat(futuresTicker.AskPrice)
		if spotBid <= 0 || futuresAsk <= 0 {
			return 0, false
		}
		if futuresAsk <= spotBid {
			return 0, true
		}
		return (futuresAsk - spotBid) / spotBid, true
	}
}

func runBoundedConcurrentTasks(ctx context.Context, maxConcurrency int, total int, worker func(context.Context, int) error) error {
	if total <= 0 {
		return nil
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}

	// 首错即取消(errgroup 语义):派生可取消的子 ctx,任一 worker 返回错误立即 cancel,
	// 让在途/未启动的兄弟任务尽早收到 ctx 取消而停止推进,缩小开平仓时的残腿窗口与恢复复杂度。
	// 已开始的下单本身受现有「裸腿守卫 + 恢复协议」兜底,这里只负责不再扩大新敞口。
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		firstErr error
		mu       sync.Mutex
	)
	slots := make(chan struct{}, maxConcurrency)
	for idx := 0; idx < total; idx++ {
		if taskCtx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := worker(taskCtx, i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(idx)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
