package app

import "strings"

type crossVenueOpenSelectionMode string

const (
	crossVenueOpenReject   crossVenueOpenSelectionMode = "reject"
	crossVenueOpenStrict   crossVenueOpenSelectionMode = "strict"
	crossVenueOpenFallback crossVenueOpenSelectionMode = "fallback"
)

func crossVenueMinOpenNotionalUSDT() float64 { return 5 }

func crossVenueOpportunityIsStrict(opp CrossVenueOpportunity) bool {
	if opp.NetEV <= 0 || opp.CapitalEfficiencyEV <= 0 || opp.RiskAdjustedEVPerHour <= 0 {
		return false
	}
	if opp.TradableNotional > 0 && opp.TradableNotional < crossVenueMinOpenNotionalUSDT() {
		return false
	}
	return opp.ExecutionScore >= 0.60 &&
		opp.RecoveryQualityScore >= 0.60 &&
		opp.LiquidityScore >= 0.60
}

func crossVenueOpportunityIsAcceptableFallback(opp CrossVenueOpportunity) bool {
	if opp.NetEV <= 0 || opp.CapitalEfficiencyEV <= 0 || opp.RiskAdjustedEVPerHour <= 0 {
		return false
	}
	if opp.TradableNotional > 0 && opp.TradableNotional < crossVenueMinOpenNotionalUSDT() {
		return false
	}
	return opp.ExecutionScore >= 0.50 &&
		opp.RecoveryQualityScore >= 0.50 &&
		opp.LiquidityScore >= 0.35
}

func crossVenueOpportunityIsRecoverableSwitchTarget(opp CrossVenueOpportunity) bool {
	if opp.NetEV <= 0 || opp.CapitalEfficiencyEV <= 0 || opp.RiskAdjustedEVPerHour <= 0 {
		return false
	}
	if opp.TradableNotional > 0 && opp.TradableNotional < crossVenueMinOpenNotionalUSDT() {
		return false
	}
	return opp.ExecutionScore >= 0.35 && opp.LiquidityScore >= 0.30
}

func crossVenueSelectOpenMode(opp CrossVenueOpportunity, strictExists bool) (crossVenueOpenSelectionMode, string) {
	if crossVenueOpportunityIsStrict(opp) {
		return crossVenueOpenStrict, ""
	}
	if strictExists {
		return crossVenueOpenReject, "存在更高质量 strict 候选,跳过软补位"
	}
	if crossVenueOpportunityIsAcceptableFallback(opp) {
		return crossVenueOpenFallback, ""
	}
	reasons := make([]string, 0, 5)
	if opp.NetEV <= 0 {
		reasons = append(reasons, "net_ev<=0")
	}
	if opp.ExecutionScore < 0.50 {
		reasons = append(reasons, "execution<0.50")
	}
	if opp.RecoveryQualityScore < 0.50 {
		reasons = append(reasons, "recovery<0.50")
	}
	if opp.LiquidityScore < 0.35 {
		reasons = append(reasons, "liquidity<0.35")
	}
	if opp.TradableNotional > 0 && opp.TradableNotional < crossVenueMinOpenNotionalUSDT() {
		reasons = append(reasons, "notional_too_small")
	}
	return crossVenueOpenReject, strings.Join(reasons, ",")
}

func crossVenueApplyAcceptableFallback(opp CrossVenueOpportunity) CrossVenueOpportunity {
	const shrinkRatio = 0.5
	opp.SizeMultiplier *= shrinkRatio
	if opp.SizeMultiplier < 0.1 {
		opp.SizeMultiplier = 0.1
	}
	if opp.TradableNotional > 0 {
		opp.TradableNotional *= shrinkRatio
	}
	if opp.TradableQty > 0 {
		opp.TradableQty *= shrinkRatio
	}
	crossVenueScaleOpportunityLinearEconomics(&opp, shrinkRatio)
	opp.RankScore = opp.NetEV
	if strings.TrimSpace(opp.NoTradeReason) == "" {
		opp.NoTradeReason = "acceptable_fallback"
	}
	return opp
}

func crossVenueCapGateNotional(gate crossVenueGateResult, opp CrossVenueOpportunity) crossVenueGateResult {
	if opp.TradableNotional >= crossVenueMinOpenNotionalUSDT() && gate.NotionalUSDT > opp.TradableNotional {
		gate.NotionalUSDT = opp.TradableNotional
	}
	return gate
}
