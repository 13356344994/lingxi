package app

import (
	"fmt"
	"strings"
	"time"
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
	end := minTime(highNext, lowNext)
	if end.IsZero() || !end.After(now) {
		return 0
	}
	return end.Sub(now).Hours()
}

func crossVenuePostFundingCooldownActive(now, nextFunding time.Time, intervalHours int, cooldown time.Duration) bool {
	if cooldown <= 0 || nextFunding.IsZero() || intervalHours <= 0 {
		return false
	}
	prev := nextFunding.Add(-time.Duration(intervalHours) * time.Hour)
	return !now.Before(prev) && now.Sub(prev) < cooldown
}

func crossVenueCompareAllowed(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition, now time.Time) bool {
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

func crossVenueEffectiveMarginOccupied(cfg CrossVenueExecutorConfig, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	lev := cfg.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	marginFactor := cfg.MarginSafetyFactor
	if marginFactor <= 0 {
		marginFactor = 1.5
	}
	return 2 * notional / lev * marginFactor
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

const defaultSpreadPersistenceScans = 2

func crossVenueConfidenceSizeMultiplier(floor, historyQuality, stabilityScore float64) float64 {
	if floor <= 0 {
		floor = 0.25
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
		floor = 0.25
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
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(notional, opp.FreshnessAgeSeconds, windowHours, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT()
	netEV := spreadEV - openCost - closeCost - expectedSlippage - observableRiskCost - policyBuffer - dustCost
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

func crossVenueFinalizeOpportunityRanking(cfg CrossVenueExecutorConfig, opps []CrossVenueOpportunity) {
	floor := cfg.LowConfidenceSizeFloorFraction
	for i := range opps {
		opps[i].RankScore = opps[i].NetEV
		confMult := crossVenueConfidenceSizeMultiplier(floor, opps[i].HistoryQualityScore, opps[i].StabilityScore)
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

func crossVenueComputeCurrentFutureEV(cfg CrossVenueExecutorConfig, pos *CrossVenuePosition) (futureEV, capitalEfficiency, riskAdjusted, exitCost, switchCost, comparableWindow, riskScore float64) {
	if pos == nil {
		return 0, 0, 0, 0, 0, 0, 1
	}
	notional := crossVenuePositionNotionalUSDT(pos)
	windowHours := pos.CurrentComparableWindowHours
	if windowHours <= 0 {
		windowHours = crossVenueComparableWindowHours(time.Now().UTC(), pos.HighNextFundingTime, pos.LowNextFundingTime)
	}
	if windowHours <= 0 {
		return 0, 0, 0, 0, 0, 0, clamp01(1 - (pos.ExecutionScore+pos.RecoveryQualityScore)/2)
	}
	fundingPerHour := crossVenueExpectedFundingPerHour(notional, pos.HighFundingRate, pos.HighIntervalHours, pos.LowFundingRate, pos.LowIntervalHours)
	remainingSpreadEV := fundingPerHour * windowHours
	opp := CrossVenueOpportunity{HighVenue: pos.HighVenue, LowVenue: pos.LowVenue}
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
