package policy

import (
	"fmt"
	"math"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// BuildProposal converts the best scored action into an account proposal.
func BuildProposal(scored ScoredAction, equityUSDT float64) (Proposal, bool) {
	action := scored.Action
	if action.Verb == actionspace.VerbFlat || action.SizeFraction <= 0 {
		return Proposal{}, false
	}
	if action.Verb == actionspace.VerbHold {
		return Proposal{}, false
	}
	cluster := ClusterFor(scored.Features, scored.Symbol)
	notional := equityUSDT * action.SizeFraction * float64(g5protocol.MaxAccountLeverage)
	if notional <= 0 {
		return Proposal{}, false
	}
	stressLoss := notional * 0.05
	if isFinite(scored.Features["vol_cost_penalty"]) {
		stressLoss *= 1 + scored.Features["vol_cost_penalty"]
	}
	marginTime := int64(3600)
	if action.TargetHorizon == "4h" || action.TargetHorizon == "240m" {
		marginTime = 4 * 3600
	}
	if action.TargetHorizon == "24h" {
		marginTime = 24 * 3600
	}
	return Proposal{
		ID:               fmt.Sprintf("%s|%s|%s", scored.Symbol, action.Verb, action.TargetHorizon),
		Symbol:           scored.Symbol,
		Cluster:          cluster,
		Action:           action,
		StateLabel:       scored.StateLabel,
		StartState:       scored.StartState,
		Score:            scored.Score,
		SizeFraction:     action.SizeFraction,
		NotionalUSDT:     notional,
		StressLossUSDT:   stressLoss,
		MarginUSDT:       notional / float64(g5protocol.MaxAccountLeverage),
		MarginTimeSec:    marginTime,
		CapacityHeadroom: capacityHeadroom(scored.Features),
		CostEstimateBPS:  costEstimateBPS(scored.Features),
		LiquidityScore:   liquidityScore(scored.Features),
	}, true
}

// ClusterFor assigns BTC beta / sector proxy cluster for budget caps.
func ClusterFor(features map[string]float64, symbol string) string {
	beta := features["symbol_beta"]
	if isFinite(beta) {
		if beta >= 1.2 {
			return "btc_beta_high"
		}
		if beta <= 0.8 {
			return "btc_beta_low"
		}
		return "btc_beta_mid"
	}
	if len(symbol) >= 3 {
		return "sector_" + symbol[:3]
	}
	return "sector_other"
}

func capacityHeadroom(features map[string]float64) float64 {
	h := features["capacity_headroom"]
	if !isFinite(h) {
		return 0
	}
	return math.Max(0, math.Min(1, h))
}

func costEstimateBPS(features map[string]float64) float64 {
	raw := features["cost_estimate"]
	if !isFinite(raw) || raw <= 0 {
		return 0
	}
	incremental := raw - g5protocol.RoundtripTakerFeeBPS - g5protocol.BaseSpreadSlippageFloorBPS
	return math.Max(0, incremental)
}

func liquidityScore(features map[string]float64) float64 {
	v := features["liquidity_score"]
	if !isFinite(v) {
		return 0
	}
	return v
}
