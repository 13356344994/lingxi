package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const crossVenueRecoveryActionObserve = "observe"
const crossVenueRecoveryActionImport = "import"
const crossVenueRecoveryActionFlatten = "flatten"
const crossVenueRecoveryActionHalt = "halt"

func crossVenueSnapshotID(ts time.Time) string {
	ts = ts.UTC()
	return fmt.Sprintf("cvsnap-%s", ts.Format("20060102T150405.000000000"))
}

func crossVenueDecisionCycleID(ts time.Time) string {
	ts = ts.UTC()
	return fmt.Sprintf("cvcycle-%s", ts.Format("20060102T150405.000000000"))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxFloat64(vals ...float64) float64 {
	best := 0.0
	for i, v := range vals {
		if i == 0 || v > best {
			best = v
		}
	}
	return best
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() {
		return a
	}
	if a.Before(b) {
		return a
	}
	return b
}

func crossVenueSnapshotAgeSeconds(snapshotTime, now time.Time) float64 {
	if snapshotTime.IsZero() || now.Before(snapshotTime) {
		return 0
	}
	return now.Sub(snapshotTime).Seconds()
}

func crossVenueComparableWindowHours(now, highNext, lowNext time.Time) float64 {
	// 异周期 perp-perp:窗口取两腿下次结算的较早者(至最近结算前的可比持有期),非单一 interval。
	end := minTime(highNext, lowNext)
	if end.IsZero() || !end.After(now) {
		return 0
	}
	return end.Sub(now).Hours()
}

func crossVenuePostFundingCooldownActive(now, nextFunding time.Time, intervalHours int, cooldown time.Duration) bool {
	// 每条腿独立用自身 interval 判断结算后冷却,异周期(如 4h vs 1h)不会互相误挡。
	if cooldown <= 0 || nextFunding.IsZero() || intervalHours <= 0 {
		return false
	}
	prev := nextFunding.Add(-time.Duration(intervalHours) * time.Hour)
	return !now.Before(prev) && now.Sub(prev) < cooldown
}

func crossVenueCompareAllowed(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition, now time.Time) bool {
	// 可比窗口与结算后冷却均按各腿自己的 NextFundingTime/IntervalHours 独立评估(见 crossVenueComparableWindowHours / crossVenuePostFundingCooldownActive)。
	if pos == nil {
		return false
	}
	if pos.CurrentComparableWindowHours > 0 && pos.CurrentComparableWindowHours < crossVenueMinComparableWindowHours(cfg) {
		return false
	}
	cd := time.Duration(cfg.PostFundingCooldownSeconds) * time.Second
	if cd <= 0 {
		return true
	}
	if crossVenuePostFundingCooldownActive(now, pos.HighNextFundingTime, pos.HighIntervalHours, cd) {
		return false
	}
	if crossVenuePostFundingCooldownActive(now, pos.LowNextFundingTime, pos.LowIntervalHours, cd) {
		return false
	}
	return true
}

func crossVenueMinComparableWindowHours(cfg CrossVenueExecutorConfig) float64 {
	if cfg.MinComparableWindowHours > 0 {
		return cfg.MinComparableWindowHours
	}
	return 0.25
}

func crossVenueMaxCompareAgeSeconds(cfg CrossVenueExecutorConfig, fallback int) float64 {
	if cfg.MaxCompareAgeSeconds > 0 {
		return float64(cfg.MaxCompareAgeSeconds)
	}
	if fallback > 0 {
		return float64(fallback)
	}
	return 900
}

func crossVenueExpectedFundingPerHour(notional float64, highRate float64, highInt int, lowRate float64, lowInt int) float64 {
	if notional <= 0 {
		return 0
	}
	if highInt <= 0 {
		highInt = 8
	}
	if lowInt <= 0 {
		lowInt = 8
	}
	return notional * ((highRate / float64(highInt)) - (lowRate / float64(lowInt)))
}

// crossVenueExpectedFundingOverHold 按短持窗口内实际 funding 结算次数估算跨所 spread 收入(非 24h 线性外推)。
func crossVenueExpectedFundingOverHold(now time.Time, holdHours float64, notional float64, opp CrossVenueOpportunity) float64 {
	if notional <= 0 {
		return 0
	}
	if holdHours <= 0 {
		holdHours = crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
	}
	if holdHours <= 0 {
		return 0
	}
	highInt := opp.HighIntervalHours
	if highInt <= 0 {
		highInt = 8
	}
	lowInt := opp.LowIntervalHours
	if lowInt <= 0 {
		lowInt = 8
	}
	highSettlements := crossVenueSpotPerpFundingSettlementCount(now, holdHours, highInt, opp.HighNextFundingTime)
	lowSettlements := crossVenueSpotPerpFundingSettlementCount(now, holdHours, lowInt, opp.LowNextFundingTime)
	return notional*opp.HighFundingRate*float64(highSettlements) - notional*opp.LowFundingRate*float64(lowSettlements)
}

func crossVenueMinNetProfitUSDT(cfg CrossVenueExecutorConfig) float64 {
	if cfg.MinNetProfitUSDT < 0 {
		return 0
	}
	return cfg.MinNetProfitUSDT
}

func crossVenueExpectedOpenCostUSDT(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	return notional*crossVenuePerpFeeBps(cfg, opp.HighVenue)/10000 +
		notional*crossVenuePerpFeeBps(cfg, opp.LowVenue)/10000
}

func crossVenueOpenEVRoundTripFeeUSDT(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	return notional*crossVenueTakerFeeBps(cfg, opp.HighVenue)/10000 +
		notional*crossVenueTakerFeeBps(cfg, opp.LowVenue)/10000
}

func crossVenueExpectedCloseCostUSDT(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional float64) float64 {
	return crossVenueExpectedOpenCostUSDT(cfg, opp, notional)
}

func crossVenueExpectedSlippageUSDT(cfg CrossVenueExecutorConfig, notional float64, legs int) float64 {
	if notional <= 0 || legs <= 0 {
		return 0
	}
	bps := crossVenueEVSlippageFallbackBpsPerLeg(cfg)
	return float64(legs) * notional * bps / 10000
}

// crossVenueEffectiveMarginOccupied perp-perp 两腿合计保证金 = 2 × 名义 / MaxEffectiveLeverage。
func crossVenueEffectiveMarginOccupied(cfg CrossVenueExecutorConfig, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	lev := cfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	return 2 * notional / lev
}

func crossVenueRecoveryQualityForPair(highVenue, lowVenue string) float64 {
	score := 1.0
	for _, name := range []string{strings.ToLower(strings.TrimSpace(highVenue)), strings.ToLower(strings.TrimSpace(lowVenue))} {
		switch name {
		case "bybit":
			score *= 0.95
		case "binance":
			score *= 0.85
		case "okx", "gate":
			score *= 0.65
		default:
			score *= 0.70
		}
	}
	return clamp01(score)
}

func crossVenueExecutionScore(slippageUSDT, notional float64) float64 {
	if notional <= 0 {
		return 0.5
	}
	drag := slippageUSDT / maxFloat64(notional, 1)
	return clamp01(1 - drag*20)
}

func crossVenueLiquidityScore(notional float64, comparableWindowHours float64) float64 {
	if notional <= 0 {
		return 0.5
	}
	score := 0.60
	if comparableWindowHours >= 8 {
		score += 0.20
	} else if comparableWindowHours >= 1 {
		score += 0.10
	}
	return clamp01(score)
}

func crossVenueStabilityScore(windowHours float64, persistenceScans int) float64 {
	score := clamp01(windowHours / 8)
	if persistenceScans > 1 {
		score = clamp01(score + 0.15)
	}
	return score
}

func crossVenueHistoryQualityScore(samples, persistenceScans int) float64 {
	if persistenceScans <= 0 {
		persistenceScans = 1
	}
	if samples <= 0 {
		return 0
	}
	return clamp01(float64(samples) / float64(persistenceScans))
}

const (
	defaultSpreadPersistenceScans = 2
	// crossVenueExtremeSpreadConfidenceBypassAPR 与 scanner crossVenueExtremeSpreadMarkAPR 对齐:
	// 当下 spread 超过此阈值时,豁免历史置信度砍仓(ExtremeSpread 亦同)。
	crossVenueExtremeSpreadConfidenceBypassAPR = 2.0
	// crossVenuePerpPerpDefaultEntryBasisConvergence 入场基差 EV 默认收敛折现(与 spot-perp 口径一致)。
	crossVenuePerpPerpDefaultEntryBasisConvergence = 0.50
)

func crossVenueConfidenceSizeMultiplier(floor, historyQuality, stabilityScore float64) float64 {
	if floor <= 0 {
		floor = 0.6
	}
	if floor > 1 {
		floor = 1
	}
	conf := clamp01((clamp01(historyQuality) + clamp01(stabilityScore)) / 2)
	return floor + (1-floor)*conf
}

func carryFundingHistoryQuality(fund CrossVenueSpotPerpFundingSnap) float64 {
	sampleScore := crossVenueHistoryQualityScore(fund.FundingHistorySamples, defaultSpreadPersistenceScans)
	if fund.SpikeThreshold > 0 {
		return clamp01((sampleScore + 0.75) / 2)
	}
	return sampleScore
}

func carryFundingVolScale(gate CarryFundingEntryGateConfig, volPerHour float64, volDataAvailable bool) float64 {
	gate = normalizeCarryFundingEntryGateConfig(gate)
	if volDataAvailable && gate.VolRefPerHour > 0 {
		vs := 1 - volPerHour/gate.VolRefPerHour
		if vs < gate.MinVolScale {
			vs = gate.MinVolScale
		}
		if vs > 1 {
			vs = 1
		}
		return vs
	}
	return gate.MissingVolScale
}

func carryConfidenceSizeMultiplier(execCfg CrossVenueExecutorConfig, fund CrossVenueSpotPerpFundingSnap, volPerHour float64, volDataAvailable bool, gate CarryFundingEntryGateConfig) float64 {
	floor := execCfg.LowConfidenceSizeFloorFraction
	if floor <= 0 {
		floor = 0.6
	}
	conf := clamp01(carryFundingHistoryQuality(fund))
	conf *= clamp01(carryFundingVolScale(gate, volPerHour, volDataAvailable))
	return floor + (1-floor)*conf
}

func carrySizedNotionalAfterConfidence(maxNotional float64, execCfg CrossVenueExecutorConfig, fund CrossVenueSpotPerpFundingSnap, volPerHour float64, volDataAvailable bool) (float64, float64, bool) {
	mult := carryConfidenceSizeMultiplier(execCfg, fund, volPerHour, volDataAvailable, execCfg.FundingEntryGate())
	sized := maxNotional * mult
	if sized+1e-9 < crossVenueSpotPerpMinTradableNotionalUSDT {
		return 0, mult, false
	}
	return sized, mult, true
}

func crossVenueObservableRiskCost(notional, freshnessAgeSeconds, comparableWindowHours, recoveryQuality float64) float64 {
	if notional <= 0 {
		return 0
	}
	freshnessPenalty := notional * clamp01(freshnessAgeSeconds/900) * 0.0005
	windowPenalty := 0.0
	if comparableWindowHours > 0 && comparableWindowHours < 1 {
		windowPenalty = notional * (1 - comparableWindowHours) * 0.0005
	}
	recoveryPenalty := notional * clamp01(1-recoveryQuality) * 0.0007
	return freshnessPenalty + windowPenalty + recoveryPenalty
}

func crossVenuePolicyBuffer(openCost, closeCost float64) float64 {
	return (openCost + closeCost) * 0.10
}

func crossVenueEstimateOpportunityMetrics(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, now time.Time) CrossVenueOpportunity {
	return crossVenueEstimateOpportunityMetricsEVRates(cfg, opp, now, opp.HighFundingRate, opp.LowFundingRate)
}

// crossVenueEstimateOpportunityMetricsEVRates 用指定费率算 spreadEV/NetEV,保留 opp 展示用 SpreadAPR/HighFundingRate 不变。
func crossVenueEstimateOpportunityMetricsEVRates(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, now time.Time, evHighRate, evLowRate float64) CrossVenueOpportunity {
	return crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, opp, now, evHighRate, evLowRate, nil)
}

// crossVenueEstimateOpportunityMetricsEVRatesWithLegs 与 EVRates 一致;滑点优先用深度腿,缺深度回退固定 bps。
func crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, now time.Time, evHighRate, evLowRate float64, slipLegs []crossVenueEVSlippageLeg) CrossVenueOpportunity {
	notional := opp.TradableNotional
	if notional <= 0 {
		notional = cfg.MaxPositionUSDT
	}
	if notional <= 0 {
		notional = 50
	}
	windowHours := crossVenueOpenEVHoldHours(cfg, now, opp, crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime))
	evOpp := opp
	evOpp.HighFundingRate = evHighRate
	evOpp.LowFundingRate = evLowRate
	spreadEV := crossVenueOpenGateExpectedFundingUSDT(cfg, now, windowHours, notional, evOpp)
	openCost := crossVenueOpenEVRoundTripFeeUSDT(cfg, opp, notional)
	closeCost := openCost
	expectedSlippage := crossVenueExpectedSlippageUSDT(cfg, notional, 4)
	if len(slipLegs) > 0 {
		expectedSlippage = crossVenueExpectedSlippageUSDTWithLegs(cfg, notional, slipLegs)
	}
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(notional, opp.FreshnessAgeSeconds, windowHours, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT()
	midDev := crossVenueMidDevFromSlipLegs(opp, slipLegs)
	entryBasisCost := crossVenuePerpPerpEntryBasisCostUSDT(cfg, midDev, notional, opp)
	netEV := spreadEV - openCost - closeCost - expectedSlippage - observableRiskCost - policyBuffer - dustCost - entryBasisCost
	effectiveMargin := crossVenueEffectiveMarginOccupied(cfg, notional)
	capitalEfficiency := 0.0
	if effectiveMargin > 0 {
		capitalEfficiency = netEV / effectiveMargin
	}
	historyQuality := clamp01(opp.HistoryQualityScore)
	liquidityScore := clamp01(opp.LiquidityScore)
	if liquidityScore == 0 {
		liquidityScore = crossVenueLiquidityScore(notional, windowHours)
	}
	stabilityScore := clamp01(opp.StabilityScore)
	if stabilityScore == 0 {
		stabilityScore = crossVenueStabilityScore(windowHours, 0)
	}
	executionScore := clamp01(opp.ExecutionScore)
	if executionScore == 0 {
		executionScore = crossVenueExecutionScore(expectedSlippage, notional)
	}
	opp.SnapshotTime = now
	opp.ExpectedHoldHours = windowHours
	opp.ComparableWindowHours = windowHours
	opp.SpreadEV = spreadEV
	opp.OpenCost = openCost
	opp.CloseCost = closeCost
	opp.ExpectedSlippage = expectedSlippage
	opp.ObservableRiskCost = observableRiskCost
	opp.PolicyBuffer = policyBuffer
	opp.NetEV = netEV
	opp.CapitalEfficiencyEV = capitalEfficiency
	opp.HistoryQualityScore = historyQuality
	opp.LiquidityScore = liquidityScore
	opp.StabilityScore = stabilityScore
	opp.ExecutionScore = executionScore
	opp.RecoveryQualityScore = recoveryQuality
	if windowHours > 0 {
		riskWeight := maxFloat64(1, 1+(1-executionScore)+(1-recoveryQuality))
		opp.RiskAdjustedEVPerHour = netEV / windowHours / riskWeight
	}
	if opp.NetEV <= crossVenueMinNetProfitUSDT(cfg) {
		opp.NoTradeReason = "net_ev<=0"
	}
	if opp.TradableNotional <= 0 {
		opp.TradableNotional = notional
	}
	return opp
}

// perpPerpNotionalClampCtx 名义网格点对齐两腿 minNotional/步进;零值 ok 时仅 cap 上界。
type perpPerpNotionalClampCtx struct {
	highInstr venue.Instrument
	lowInstr  venue.Instrument
	highCaps  venue.Capabilities
	lowCaps   venue.Capabilities
	refMid    float64
	ok        bool
}

func crossVenuePerpPerpNotionalClampCtx(ctx context.Context, venues map[string]venue.Venue, opp CrossVenueOpportunity, slipLegs []crossVenueEVSlippageLeg) perpPerpNotionalClampCtx {
	out := perpPerpNotionalClampCtx{}
	if ctx == nil || venues == nil {
		return out
	}
	highV := venues[opp.HighVenue]
	lowV := venues[opp.LowVenue]
	if highV == nil || lowV == nil {
		return out
	}
	highInstr, okH := lookupVenueInstrument(ctx, highV, venue.Symbol(opp.Symbol))
	lowInstr, okL := lookupVenueInstrument(ctx, lowV, venue.Symbol(opp.Symbol))
	if !okH || !okL {
		return out
	}
	out.highInstr = highInstr
	out.lowInstr = lowInstr
	out.highCaps = highV.Capabilities()
	out.lowCaps = lowV.Capabilities()
	out.refMid = crossVenuePerpPerpRefMidFromSlipLegs(slipLegs)
	if out.refMid <= 0 {
		out.refMid = crossVenuePerpPerpMidFromDepthOrBook(ctx, highV, venue.Symbol(opp.Symbol))
	}
	out.ok = out.refMid > 0
	return out
}

func crossVenuePerpPerpRefMidFromSlipLegs(slipLegs []crossVenueEVSlippageLeg) float64 {
	var sum, n float64
	for _, leg := range slipLegs {
		if leg.RefMid > 0 {
			sum += leg.RefMid
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

// clampToVenueStep 将名义 cap 到 tradableCap 并按两腿步进/minNotional 对齐;不可交易则返回 0。
func clampToVenueStep(n float64, _ CrossVenueOpportunity, tradableCap float64, clamp perpPerpNotionalClampCtx) float64 {
	if n <= 0 {
		return 0
	}
	if tradableCap > 0 && n > tradableCap+1e-9 {
		n = tradableCap
	}
	if !clamp.ok || clamp.refMid <= 0 {
		return n
	}
	qty := alignCrossVenueQty(n, clamp.refMid, clamp.highInstr, clamp.lowInstr, clamp.highCaps, clamp.lowCaps)
	if qty <= 0 {
		return 0
	}
	if !crossVenueQtyTradable(qty, clamp.refMid, clamp.highInstr) || !crossVenueQtyTradable(qty, clamp.refMid, clamp.lowInstr) {
		return 0
	}
	return qty * clamp.refMid
}

func appendPerpPerpNotionalGridPoint(grid []float64, n float64) []float64 {
	if n <= 0 {
		return grid
	}
	for _, g := range grid {
		if math.Abs(g-n) < 1e-6 {
			return grid
		}
	}
	return append(grid, n)
}

// crossVenuePerpPerpSearchBestNotional 在离散名义网格上搜 netEV 最大点(每点用深度滑点同口径 EV)。
func crossVenuePerpPerpSearchBestNotional(
	cfg CrossVenueExecutorConfig,
	opp CrossVenueOpportunity,
	now time.Time,
	evHigh, evLow, tradableCap float64,
	slipLegs []crossVenueEVSlippageLeg,
	clamp perpPerpNotionalClampCtx,
) (CrossVenueOpportunity, float64) {
	grid := []float64{25, 50, 75, 100, 150}
	grid = appendPerpPerpNotionalGridPoint(grid, tradableCap)
	best := math.Inf(-1)
	var bestOpp CrossVenueOpportunity
	var bestNotional float64
	for _, n := range grid {
		n = clampToVenueStep(n, opp, tradableCap, clamp)
		if n <= 0 {
			continue
		}
		cand := opp
		cand.TradableNotional = n
		cand = crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, cand, now, evHigh, evLow, slipLegs)
		if cand.NetEV > best {
			best = cand.NetEV
			bestOpp = cand
			bestNotional = n
		}
	}
	if math.IsInf(best, -1) {
		fallback := clampToVenueStep(tradableCap, opp, tradableCap, clamp)
		if fallback <= 0 && tradableCap > 0 {
			fallback = tradableCap
		}
		opp.TradableNotional = fallback
		return crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, opp, now, evHigh, evLow, slipLegs), fallback
	}
	return bestOpp, bestNotional
}

// crossVenuePerpPerpEntryBasisCostUSDT 开仓时两腿 mid 偏离锁入的基差成本(收敛折现后从 netEV 扣除)。
// 公式:偏离比例 × 名义 × EntryBasisConvergenceFraction;仅计成本侧,不把有利基差当收入。
// 极端价差放宽 mid 闸时,只对超出 MidPriceDeviationMax 的增量偏离计基差(放宽区间本身不计)。
func crossVenuePerpPerpEntryBasisCostUSDT(cfg CrossVenueExecutorConfig, midDev, notional float64, opp CrossVenueOpportunity) float64 {
	if midDev <= 0 || notional <= 0 {
		return 0
	}
	billableDev := midDev
	if crossVenueBypassesConfidenceDownsize(opp) {
		normalMax := cfg.MidPriceDeviationMax
		if normalMax <= 0 {
			normalMax = 0.03
		}
		billableDev = midDev - normalMax
		if billableDev < 0 {
			billableDev = 0
		}
	}
	conv := cfg.EntryBasisConvergenceFraction
	if conv <= 0 {
		conv = crossVenuePerpPerpDefaultEntryBasisConvergence
	}
	return billableDev * notional * conv
}

func crossVenueMidDevFromSlipLegs(opp CrossVenueOpportunity, slipLegs []crossVenueEVSlippageLeg) float64 {
	if len(slipLegs) >= 2 && slipLegs[0].RefMid > 0 && slipLegs[1].RefMid > 0 {
		return crossVenueMidDeviation(slipLegs[0].RefMid, slipLegs[1].RefMid, crossVenueOppHighMult(opp), crossVenueOppLowMult(opp))
	}
	return 0
}

func crossVenueResolvedMidPriceDeviationMax(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity) float64 {
	maxDev := cfg.MidPriceDeviationMax
	if maxDev <= 0 {
		maxDev = 0.03
	}
	if crossVenueBypassesConfidenceDownsize(opp) {
		extremeMax := cfg.MidPriceDeviationMaxExtreme
		if extremeMax <= 0 {
			extremeMax = 0.08
		}
		maxDev = extremeMax
	}
	return maxDev
}

// crossVenueBypassesConfidenceDownsize 极端价差机会:当下可观测 spread 足够强,不因历史样本少砍仓。
func crossVenueBypassesConfidenceDownsize(opp CrossVenueOpportunity) bool {
	if opp.ExtremeSpread {
		return true
	}
	return opp.SpreadAPR > crossVenueExtremeSpreadConfidenceBypassAPR
}

func crossVenueFinalizeOpportunityRanking(cfg CrossVenueExecutorConfig, opps []CrossVenueOpportunity) {
	floor := cfg.LowConfidenceSizeFloorFraction
	for i := range opps {
		opps[i].RankScore = opps[i].NetEV
		confMult := crossVenueConfidenceSizeMultiplier(floor, opps[i].HistoryQualityScore, opps[i].StabilityScore)
		if crossVenueBypassesConfidenceDownsize(opps[i]) {
			confMult = 1.0
		}
		capNorm := 0.0
		if opps[i].CapitalEfficiencyEV > 0 {
			capNorm = clamp01(opps[i].CapitalEfficiencyEV)
		}
		baseMult := clamp01((capNorm + opps[i].ExecutionScore + opps[i].LiquidityScore + opps[i].RecoveryQualityScore) / 4)
		opps[i].SizeMultiplier = clamp01(baseMult * confMult)
		if opps[i].NetEV <= crossVenueMinNetProfitUSDT(cfg) {
			opps[i].SizeMultiplier = 0
		}
		opps[i].TradableNotional *= opps[i].SizeMultiplier
	}
}

func crossVenueComputeCurrentFutureEV(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition, now time.Time) (futureEV, capitalEfficiency, riskAdjusted, exitCost, switchCost, comparableWindow, riskScore float64) {
	if pos == nil {
		return 0, 0, 0, 0, 0, 0, 1
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	notional := crossVenuePositionNotionalUSDT(pos)
	windowHours := pos.CurrentComparableWindowHours
	if windowHours <= 0 {
		windowHours = crossVenueComparableWindowHours(now, pos.HighNextFundingTime, pos.LowNextFundingTime)
	}
	if windowHours <= 0 {
		return 0, 0, 0, 0, 0, 0, clamp01(1 - (pos.ExecutionScore+pos.RecoveryQualityScore)/2)
	}
	opp := CrossVenueOpportunity{
		HighVenue:           pos.HighVenue,
		LowVenue:            pos.LowVenue,
		HighFundingRate:     pos.HighFundingRate,
		LowFundingRate:      pos.LowFundingRate,
		HighIntervalHours:   pos.HighIntervalHours,
		LowIntervalHours:    pos.LowIntervalHours,
		HighNextFundingTime: pos.HighNextFundingTime,
		LowNextFundingTime:  pos.LowNextFundingTime,
	}
	remainingSpreadEV := crossVenueExpectedFundingOverHold(now, windowHours, notional, opp)
	remainingSpreadEV *= carryScannerNormalizedFundingCaptureProbability(0)
	exitCost = crossVenueExpectedCloseCostUSDT(cfg, opp, notional) + crossVenueExpectedSlippageUSDT(cfg, notional, 2)
	futureRiskCost := crossVenueObservableRiskCost(notional, pos.FreshnessAgeSeconds, windowHours, pos.RecoveryQualityScore)
	futureEV = remainingSpreadEV - exitCost - futureRiskCost
	effectiveMargin := crossVenueEffectiveMarginOccupied(cfg, notional)
	if effectiveMargin > 0 {
		capitalEfficiency = futureEV / effectiveMargin
	}
	if windowHours > 0 {
		riskWeight := maxFloat64(1, 1+(1-pos.ExecutionScore)+(1-pos.RecoveryQualityScore))
		riskAdjusted = futureEV / windowHours / riskWeight
	}
	switchCost = exitCost + crossVenueExpectedOpenCostUSDT(cfg, opp, notional) + crossVenueExpectedSlippageUSDT(cfg, notional, 2)
	riskScore = clamp01(1 - (pos.ExecutionScore+pos.RecoveryQualityScore)/2)
	return futureEV, capitalEfficiency, riskAdjusted, exitCost, switchCost, windowHours, riskScore
}

// crossVenueUpcomingSettlementNetFundingUSDT 统计 lead 窗口内即将结算各腿的净 funding(正=收,负=付)。
func crossVenueUpcomingSettlementNetFundingUSDT(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition, now time.Time) (netFunding float64, settling bool) {
	if pos == nil {
		return 0, false
	}
	notional := crossVenuePositionNotionalUSDT(pos)
	if notional <= 0 {
		return 0, false
	}
	lead := cfg.PreFundingCloseLeadSeconds
	if lead <= 0 {
		lead = 120
	}
	leadDur := time.Duration(lead) * time.Second
	highDue := !pos.HighNextFundingTime.IsZero() && pos.HighNextFundingTime.After(now) && !pos.HighNextFundingTime.After(now.Add(leadDur))
	lowDue := !pos.LowNextFundingTime.IsZero() && pos.LowNextFundingTime.After(now) && !pos.LowNextFundingTime.After(now.Add(leadDur))
	if highDue {
		// 高费腿空头:正费率收,负费率付
		netFunding += notional * pos.HighFundingRate
		settling = true
	}
	if lowDue {
		// 低费腿多头:正费率付,负费率收
		netFunding -= notional * pos.LowFundingRate
		settling = true
	}
	return netFunding, settling
}

// crossVenuePreFundingAdverseCloseReason 结算前净不利资费即平:优先级高于 N 连轮/MinHold。
func crossVenuePreFundingAdverseCloseReason(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition, now time.Time) string {
	if pos == nil || !cfg.CloseBeforeAdverseFundingEnabled {
		return ""
	}
	netFunding, settling := crossVenueUpcomingSettlementNetFundingUSDT(cfg, pos, now)
	if !settling || netFunding >= 0 {
		return ""
	}
	return fmt.Sprintf("结算前不利资费 %.4f USDT(净付,即平)", netFunding)
}
