package portfoliosim

import (
	g5protocol "lingxi/internal/routerg5/protocol"
)

// EvaluateGates applies frozen G5 validation gates to scenario results.
func EvaluateGates(scenarios []ScenarioResult) (status string, checks GateChecks, reason string) {
	checks = GateChecks{
		BaseNetReturnPositive:     true,
		AllDelayNetReturnPositive: true,
		AllEquityTiersPositive:    true,
		MaxDrawdownOK:             true,
		NoLeverageSolvencyBreach:  true,
		MinActiveDaysMet:          true,
		MaxSymbolContributionOK:   true,
	}
	if len(scenarios) == 0 {
		checks.SummaryNote = "INCONCLUSIVE: no scenarios; not VALIDATION_PASS"
		return StatusInconclusive, checks, ReasonCoverage
	}
	tierPositive := map[float64]bool{}
	for _, equity := range g5protocol.ValidationInitialEquities {
		tierPositive[equity] = true
	}
	var baseScenario *ScenarioResult
	delayPositive := map[int]bool{}
	for _, delay := range g5protocol.ValidationDelayBars {
		delayPositive[delay] = true
	}
	for i := range scenarios {
		s := &scenarios[i]
		if s.InitialEquityUSDT == g5protocol.ValidationInitialEquities[0] && s.DelayBars == 0 && s.CostStressMultiplier == 1.0 {
			cp := *s
			baseScenario = &cp
		}
		if s.ActiveDays < g5protocol.ValidationMinActiveDays {
			checks.MinActiveDaysMet = false
		}
		if !MaxDrawdownOK(s.MaxDrawdownFraction) {
			checks.MaxDrawdownOK = false
		}
		if s.LeverageBreach {
			checks.NoLeverageSolvencyBreach = false
		}
		if s.FinalEquityUSDT <= 0 || s.SolvencyBreach {
			checks.NoLeverageSolvencyBreach = false
		}
		if s.MaxSymbolContribution > g5protocol.ValidationMaxSymbolPositiveContributionFraction+1e-12 {
			checks.MaxSymbolContributionOK = false
		}
		if !PositiveReturn(s.InitialEquityUSDT, s.FinalEquityUSDT) {
			tierPositive[s.InitialEquityUSDT] = false
			if s.CostStressMultiplier <= 1.0+1e-9 {
				delayPositive[s.DelayBars] = false
				if s.DelayBars == 0 {
					checks.BaseNetReturnPositive = false
				}
			}
		} else if s.CostStressMultiplier <= 1.0+1e-9 {
			if _, ok := delayPositive[s.DelayBars]; !ok {
				delayPositive[s.DelayBars] = true
			}
		}
	}
	for _, equity := range g5protocol.ValidationInitialEquities {
		if !tierPositive[equity] {
			checks.AllEquityTiersPositive = false
			break
		}
	}
	for _, delay := range g5protocol.ValidationDelayBars {
		if !delayPositive[delay] {
			checks.AllDelayNetReturnPositive = false
			break
		}
	}
	if baseScenario == nil {
		checks.SummaryNote = "INCONCLUSIVE: base scenario missing; not VALIDATION_PASS"
		return StatusInconclusive, checks, ReasonCoverage
	}
	if !checks.NoLeverageSolvencyBreach {
		for _, s := range scenarios {
			if s.FinalEquityUSDT <= 0 || s.SolvencyBreach {
				return StatusValidationFail, checks, ReasonSolvency
			}
		}
		return StatusValidationFail, checks, ReasonLeverage
	}
	if !checks.MinActiveDaysMet {
		checks.SummaryNote = "INCONCLUSIVE: min_active_days not met; not VALIDATION_PASS"
		return StatusInconclusive, checks, ReasonCoverage
	}
	if !checks.MaxDrawdownOK {
		return StatusValidationFail, checks, ReasonDrawdown
	}
	if !checks.MaxSymbolContributionOK {
		return StatusValidationFail, checks, ReasonConcentration
	}
	if !checks.AllDelayNetReturnPositive {
		return StatusValidationFail, checks, ReasonDelay
	}
	if !checks.BaseNetReturnPositive {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	for _, s := range scenarios {
		if s.CostStressMultiplier > 1.0+1e-9 && !PositiveReturn(s.InitialEquityUSDT, s.FinalEquityUSDT) {
			return StatusValidationFail, checks, ReasonCost
		}
	}
	if !checks.AllEquityTiersPositive {
		return StatusValidationFail, checks, ReasonNoEdge
	}
	return StatusValidationPass, checks, ""
}
