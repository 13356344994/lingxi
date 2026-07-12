package examevaluator

import (
	"strconv"

	g6protocol "lingxi/internal/routerg6/protocol"
)

// GateChecks records frozen exam gate outcomes aggregated across blocks.
type GateChecks struct {
	BaseNetReturnPositive         bool
	AllDelayNetReturnPositive     bool
	MinIndependentDaysMet         bool
	NoLeverageSolvencyBreach      bool
	CostStressNetReturnPositive   bool
	ShockBlockLOO_OK              bool
	MaxConcurrentByCapitalOK      bool
}

// EvaluateExamGates applies frozen G6 exam gates to streamed fixture blocks.
func EvaluateExamGates(blocks []FixtureBlock) (pass bool, reason string, checks GateChecks) {
	checks = GateChecks{
		BaseNetReturnPositive:       true,
		AllDelayNetReturnPositive:   true,
		MinIndependentDaysMet:       true,
		NoLeverageSolvencyBreach:    true,
		CostStressNetReturnPositive: true,
		ShockBlockLOO_OK:            true,
		MaxConcurrentByCapitalOK:    true,
	}
	if len(blocks) < g6protocol.ExamFixtureMinBlocks {
		return false, "ROBUSTNESS_GATE", checks
	}
	for _, block := range blocks {
		m := block.AccountMetrics
		if m.IndependentDays < g6protocol.ExamMinIndependentDays {
			checks.MinIndependentDaysMet = false
		}
		if m.LeverageBreach || m.SolvencyBreach {
			checks.NoLeverageSolvencyBreach = false
		}
		if !positiveReturn(m.InitialEquityUSDT, m.FinalEquityUSDT) {
			checks.BaseNetReturnPositive = false
		}
		for _, delay := range g6protocol.ValidationDelayBars {
			key := delayKey(delay)
			if ok, exists := block.Delay.NetReturnPositiveByDelay[key]; !exists || !ok {
				checks.AllDelayNetReturnPositive = false
			}
		}
		if block.Cost.CostStressMultiplier != g6protocol.ExamCostStressMultiplier ||
			!block.Cost.NetReturnPositive {
			checks.CostStressNetReturnPositive = false
		}
		if !block.Shock.ShockBlockLOO_OK {
			checks.ShockBlockLOO_OK = false
		}
		if !block.Concurrent.MaxConcurrentByCapitalOK {
			checks.MaxConcurrentByCapitalOK = false
		}
	}
	if !checks.NoLeverageSolvencyBreach {
		return false, "RISK_GATE", checks
	}
	if !checks.MinIndependentDaysMet {
		return false, "ROBUSTNESS_GATE", checks
	}
	if !checks.ShockBlockLOO_OK || !checks.MaxConcurrentByCapitalOK {
		return false, "ROBUSTNESS_GATE", checks
	}
	if !checks.AllDelayNetReturnPositive {
		return false, "EXECUTION_GATE", checks
	}
	if !checks.CostStressNetReturnPositive {
		return false, "EXECUTION_GATE", checks
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
