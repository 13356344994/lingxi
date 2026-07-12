package residualmap

import (
	g10protocol "lingxi/internal/routerg10/protocol"
)

// ClassifyResidual applies the frozen personal residual definition and rejection gates.
func ClassifyResidual(tiers map[string]TierCostBreakdown, item EventWorkItem, adj PersonalAdjustments) EvaluationResult {
	result := EvaluationResult{
		TierCosts: tiers,
	}
	result.RejectReasons = append(result.RejectReasons, rejectGates(tiers, item, adj)...)
	if len(result.RejectReasons) > 0 {
		return result
	}
	result.EligibleCapitalTiers, result.MaxCapacityUSDT, result.CapacityCliffUSDT, result.CapacityCliffReason =
		capacitySummary(tiers)
	result.LargeTierFailReasons = largeTierFailReasons(tiers)
	result.Accepted = true
	return result
}

func rejectGates(tiers map[string]TierCostBreakdown, item EventWorkItem, adj PersonalAdjustments) []string {
	var reasons []string
	if adj.CriticalUnknown && g10protocol.FingerprintUnknownBlocksOpen {
		reasons = append(reasons, RejectUnknownCriticalCosts)
	}
	if adj.CriticalNegative {
		reasons = append(reasons, RejectFingerprintNegative)
	}
	if !anyUserTierPasses(tiers) {
		reasons = append(reasons, RejectUserTierNegative)
	}
	if !largeTiersFail(tiers) {
		reasons = append(reasons, RejectNoLargeTierFail)
	}
	if isPublicScalable(tiers) {
		reasons = append(reasons, RejectPublicScalable)
	}
	if isSubsecondSpeedRequired(item) {
		reasons = append(reasons, RejectSubsecondSpeed)
	}
	if isPublicSimpleFactor(item, tiers) {
		reasons = append(reasons, RejectPublicSimpleFactor)
	}
	return reasons
}

func anyUserTierPasses(tiers map[string]TierCostBreakdown) bool {
	for _, label := range UserTierLabels {
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			continue
		}
		if tier.ConservativeBPS >= g10protocol.PersonalConservativeExpectedNetMin {
			return true
		}
	}
	return false
}

func largeTiersFail(tiers map[string]TierCostBreakdown) bool {
	for _, label := range LargeTierLabels {
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			return false
		}
		if !tier.CapacityFail {
			return false
		}
	}
	return true
}

func largeTierFailReasons(tiers map[string]TierCostBreakdown) []string {
	var out []string
	for _, label := range LargeTierLabels {
		tier, ok := tiers[label]
		if !ok {
			continue
		}
		if tier.CapacityFail {
			out = append(out, LargeTierFailCapacity)
		}
		if tier.ConservativeBPS < g10protocol.PersonalConservativeExpectedNetMin {
			out = append(out, LargeTierFailImpact)
		}
		if tier.MarginTimeFail {
			out = append(out, LargeTierFailMarginTime)
		}
		if tier.ExecutionComplexityFail {
			out = append(out, LargeTierFailExecution)
		}
	}
	return dedupeStrings(out)
}

func isPublicScalable(tiers map[string]TierCostBreakdown) bool {
	for _, label := range LargeTierLabels {
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			return false
		}
		if tier.ConservativeBPS < g10protocol.PersonalConservativeExpectedNetMin || tier.CapacityFail {
			return false
		}
	}
	return true
}

func isSubsecondSpeedRequired(item EventWorkItem) bool {
	return item.ProfileID == "okx_5m" && item.Event.DelayBars == 0 &&
		(item.Event.Horizon == "5m" || item.Event.Horizon == "15m")
}

func isPublicSimpleFactor(item EventWorkItem, tiers map[string]TierCostBreakdown) bool {
	if item.Event.EventFamily != "ohlcv_extreme_continuation" {
		return false
	}
	t30, ok30 := tiers["30"]
	t10k, ok10k := tiers["10000"]
	t100k, ok100k := tiers["100000"]
	if !ok30 || !ok10k || !ok100k {
		return false
	}
	if t30.CapitalUSDT <= 0 || t10k.CapitalUSDT <= 0 || t100k.CapitalUSDT <= 0 {
		return false
	}
	return t30.ConservativeBPS >= 0 &&
		t10k.ConservativeBPS >= 0 &&
		t100k.ConservativeBPS >= 0 &&
		!t10k.CapacityFail &&
		!t100k.CapacityFail
}

func capacitySummary(tiers map[string]TierCostBreakdown) (eligible []string, maxCap, cliff float64, cliffReason string) {
	order := []string{"actual", "30", "100", "1000", "10000", "100000"}
	for _, label := range order {
		tier, ok := tiers[label]
		if !ok || tier.CapitalUSDT <= 0 {
			continue
		}
		if tier.ConservativeBPS >= g10protocol.PersonalConservativeExpectedNetMin &&
			!tier.CapacityFail && !tier.ExecutionComplexityFail {
			eligible = append(eligible, label)
			maxCap = tier.CapitalUSDT
		} else if cliff == 0 {
			cliff = tier.CapitalUSDT
			cliffReason = capacityCliffReason(tier)
		}
	}
	return eligible, maxCap, cliff, cliffReason
}

func capacityCliffReason(tier TierCostBreakdown) string {
	switch {
	case tier.CapacityFail:
		return LargeTierFailCapacity
	case tier.ExecutionComplexityFail:
		return LargeTierFailExecution
	case tier.MarginTimeFail:
		return LargeTierFailMarginTime
	case tier.ConservativeBPS < g10protocol.PersonalConservativeExpectedNetMin:
		return LargeTierFailImpact
	default:
		return LargeTierFailImpact
	}
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
