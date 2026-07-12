package counterfactual

import (
	"math"

	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg10/residualmap"
)

// AdverseStressBPS computes credible worst-case loss from fee/slip/impact/funding/delay
// even when personal conservative net is non-negative.
func AdverseStressBPS(tier residualmap.TierCostBreakdown, delayBars int) float64 {
	adverse := tier.FeeBPS + tier.SlipBPS + tier.ImpactBPS + math.Abs(tier.FundingBPS)
	if delayBars > 0 {
		adverse += float64(delayBars) * g4protocol.BaseSpreadSlippageFloorBPS
	}
	stress := adverse * CostStressMultiplier
	if tier.ConservativeBPS < 0 {
		lossStress := -tier.ConservativeBPS * CostStressMultiplier
		if lossStress > stress {
			stress = lossStress
		}
	}
	return math.Max(stress, 1e-6)
}

func resolvePersonalAdj(ctx DecisionContext, direction string) residualmap.PersonalAdjustments {
	adj := residualmap.LookupPersonalAdjustments(ctx.Fingerprint, ctx.Symbol, direction)
	if adj.CriticalUnknown {
		return adj
	}
	if ctx.PersonalAdj.CriticalUnknown {
		adj.CriticalUnknown = true
		return adj
	}
	if ctx.PersonalAdj.CriticalNegative {
		adj.CriticalNegative = true
	}
	if ctx.Fingerprint.FingerprintHash == "" {
		adj.CriticalUnknown = true
	}
	return adj
}

func openDirection(action string, ctx DecisionContext) string {
	switch action {
	case "SHORT", "REVERSE":
		if ctx.Position.HasPosition && ctx.Position.Direction == "long" {
			return "short"
		}
		if ctx.Position.HasPosition && ctx.Position.Direction == "short" {
			return "long"
		}
		return "short"
	case "LONG", "ADD":
		if ctx.Position.HasPosition && ctx.Position.Direction == "short" {
			return "long"
		}
		return "long"
	default:
		if ctx.Position.HasPosition && ctx.Position.Direction != "" {
			return ctx.Position.Direction
		}
		return "long"
	}
}
