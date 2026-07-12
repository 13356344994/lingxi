package residualmap

import (
	"math"

	g10protocol "lingxi/internal/routerg10/protocol"
	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/profitmap"
)

// CostBase holds decision-bar execution proxies shared across tiers.
type CostBase struct {
	RollingQuoteVolumeUSDT float64
	BarHigh                float64
	BarLow                 float64
	BarMid                 float64
	FundingBPS             float64
	HoldHours              float64
	ProfileID              string
	Horizon                string
	DelayBars              int
}

// PersonalAdjustments carries fingerprint-derived fee/slip/latency deltas.
type PersonalAdjustments struct {
	FeeDeltaBPS      float64
	SlipDeltaBPS     float64
	LatencyP90MS     float64
	MakerWaitP90MS   float64
	CriticalUnknown  bool
	CriticalNegative bool
}

// TierCapitalUSDT resolves one tier label to notional USDT.
func TierCapitalUSDT(label string, actualEquity float64) float64 {
	if label == g10protocol.ActualEquityTierLabel {
		if actualEquity > 0 {
			return actualEquity
		}
		return 0
	}
	for i, want := range TierLabels {
		if want == label && i > 0 {
			return TierNumericUSDT[i-1]
		}
	}
	return 0
}

// ComputeTierCosts evaluates all frozen residual tiers for one gross outcome.
func ComputeTierCosts(grossBPS float64, base CostBase, adj PersonalAdjustments, actualEquity float64) map[string]TierCostBreakdown {
	out := make(map[string]TierCostBreakdown, len(TierLabels))
	for _, label := range TierLabels {
		capital := TierCapitalUSDT(label, actualEquity)
		out[label] = computeOneTier(grossBPS, capital, label, base, adj)
	}
	return out
}

func computeOneTier(grossBPS, capital float64, label string, base CostBase, adj PersonalAdjustments) TierCostBreakdown {
	if capital <= 0 {
		return TierCostBreakdown{
			TierLabel:   label,
			CapitalUSDT: 0,
			GrossBPS:    grossBPS,
		}
	}
	in := profitmap.CostInputs{
		CapitalUSDT:            capital,
		RollingQuoteVolumeUSDT: base.RollingQuoteVolumeUSDT,
		BarHigh:                base.BarHigh,
		BarLow:                 base.BarLow,
		BarMid:                 base.BarMid,
		FundingBPS:             base.FundingBPS,
	}
	_, conservative, capacityFail := profitmap.ComputeReturns(grossBPS, in)

	capacityImpact := g4protocol.CapacityImpactScaleBPS * capital / math.Max(base.RollingQuoteVolumeUSDT, 1)
	if capacityImpact > g4protocol.CapacityImpactCapBPS {
		capacityImpact = g4protocol.CapacityImpactCapBPS
	}
	mid := math.Max(base.BarMid, 1e-12)
	toxicity := (base.BarHigh - base.BarLow) / mid * 10000 * g4protocol.BarRangeToxicityScale
	if toxicity > g4protocol.BarRangeToxicityCapBPS {
		toxicity = g4protocol.BarRangeToxicityCapBPS
	}

	feeBPS := g4protocol.RoundtripTakerFeeBPS + adj.FeeDeltaBPS
	slipBPS := g4protocol.BaseSpreadSlippageFloorBPS + adj.SlipDeltaBPS
	impactBPS := capacityImpact + toxicity
	fundingBPS := base.FundingBPS

	conservative = grossBPS - feeBPS - slipBPS - impactBPS - fundingBPS

	marginTimeFail := largeTierMarginTimeFail(label, capital, base)
	execFail := largeTierExecutionFail(label, base, adj)

	return TierCostBreakdown{
		TierLabel:               label,
		CapitalUSDT:             capital,
		GrossBPS:                grossBPS,
		FeeBPS:                  feeBPS,
		SlipBPS:                 slipBPS,
		ImpactBPS:               impactBPS,
		FundingBPS:              fundingBPS,
		ConservativeBPS:         conservative,
		CapacityFail:            capacityFail,
		MarginTimeFail:          marginTimeFail,
		ExecutionComplexityFail: execFail,
	}
}

func largeTierMarginTimeFail(label string, capital float64, base CostBase) bool {
	if !isLargeTier(label) {
		return false
	}
	if base.HoldHours <= 0 {
		return false
	}
	occupation := capital * base.HoldHours / 24.0
	return occupation >= 25000 && base.FundingBPS >= 5
}

func largeTierExecutionFail(label string, base CostBase, adj PersonalAdjustments) bool {
	if !isLargeTier(label) {
		return false
	}
	if base.ProfileID != "okx_5m" {
		return false
	}
	if base.DelayBars > 0 {
		return false
	}
	if base.Horizon != "5m" && base.Horizon != "15m" {
		return false
	}
	return adj.LatencyP90MS > 500 || adj.MakerWaitP90MS > 2000
}

func isLargeTier(label string) bool {
	return label == "10000" || label == "100000"
}

func isUserTier(label string) bool {
	for _, want := range UserTierLabels {
		if label == want {
			return true
		}
	}
	return false
}
