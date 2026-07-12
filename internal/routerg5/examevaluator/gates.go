package examevaluator

import (
	"strconv"

	g5protocol "lingxi/internal/routerg5/protocol"
)

// GateChecks records frozen exam gate outcomes aggregated across blocks.
type GateChecks struct {
	BaseNetReturnPositive       bool
	AllDelayNetReturnPositive   bool
	MaxDrawdownOK               bool
	MinActiveDaysMet            bool
	MaxSymbolContributionOK     bool
	NoLeverageSolvencyBreach    bool
	CostStressNetReturnPositive bool
	ClusterLOO_OK               bool
	TopKOK                      bool
}

// EvaluateExamGates applies frozen G5 exam gates to streamed fixture blocks.
func EvaluateExamGates(blocks []FixtureBlock) (pass bool, reason string, checks GateChecks) {
	checks = GateChecks{
		BaseNetReturnPositive:       true,
		AllDelayNetReturnPositive:   true,
		MaxDrawdownOK:               true,
		MinActiveDaysMet:            true,
		MaxSymbolContributionOK:     true,
		NoLeverageSolvencyBreach:    true,
		CostStressNetReturnPositive: true,
		ClusterLOO_OK:               true,
		TopKOK:                      true,
	}
	if len(blocks) < g5protocol.ExamFixtureMinBlocks {
		return false, "ROBUSTNESS_GATE", checks
	}
	for _, block := range blocks {
		m := block.AccountMetrics
		if m.ActiveDays < g5protocol.ExamMinActiveDays {
			checks.MinActiveDaysMet = false
		}
		if m.MaxDrawdownFraction > g5protocol.ExamMaxDrawdownFraction+1e-12 {
			checks.MaxDrawdownOK = false
		}
		if m.LeverageBreach || m.SolvencyBreach {
			checks.NoLeverageSolvencyBreach = false
		}
		if m.MaxSymbolPositiveContributionFraction > g5protocol.ExamMaxSymbolPositiveContributionFraction+1e-12 {
			checks.MaxSymbolContributionOK = false
		}
		if !positiveReturn(m.InitialEquityUSDT, m.FinalEquityUSDT) {
			checks.BaseNetReturnPositive = false
		}
		for _, delay := range g5protocol.ValidationDelayBars {
			key := delayKey(delay)
			if ok, exists := block.Delay.NetReturnPositiveByDelay[key]; !exists || !ok {
				checks.AllDelayNetReturnPositive = false
			}
		}
		if block.Cost.CostStressMultiplier != g5protocol.ExamCostStressMultiplier ||
			!block.Cost.NetReturnPositive {
			checks.CostStressNetReturnPositive = false
		}
		if !block.Cluster.ClusterLOO_OK {
			checks.ClusterLOO_OK = false
		}
		if !block.Actions.TopKOK {
			checks.TopKOK = false
		}
	}
	if !checks.NoLeverageSolvencyBreach {
		return false, "RISK_GATE", checks
	}
	if !checks.MaxDrawdownOK {
		return false, "RISK_GATE", checks
	}
	if !checks.MinActiveDaysMet {
		return false, "ROBUSTNESS_GATE", checks
	}
	if !checks.MaxSymbolContributionOK {
		return false, "ROBUSTNESS_GATE", checks
	}
	if !checks.AllDelayNetReturnPositive {
		return false, "EXECUTION_GATE", checks
	}
	if !checks.CostStressNetReturnPositive {
		return false, "EXECUTION_GATE", checks
	}
	if !checks.ClusterLOO_OK || !checks.TopKOK {
		return false, "ROBUSTNESS_GATE", checks
	}
	if !checks.BaseNetReturnPositive {
		return false, "ROBUSTNESS_GATE", checks
	}
	return true, "PASS", checks
}

func positiveReturn(initial, final float64) bool {
	return initial > 0 && final > initial
}

func delayKey(delay int) string {
	return strconv.Itoa(delay)
}
