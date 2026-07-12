package walkforward

import (
	"math"
	"sort"

	"lingxi/internal/routerg5/portfoliosim"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// EvaluateWFGates applies frozen G5 walk-forward gates to fold replay results.
func EvaluateWFGates(foldResults []FoldResult, lofo []LOFOResult, daily portfoliosim.DailyEquityReplay, attemptResults []AttemptSignResult) (status string, checks WFGateChecks, reason string) {
	checks = WFGateChecks{
		AllFoldsNetReturnPositive:   true,
		WorstFoldNetReturnPositive:  true,
		AllLOFONetReturnPositive:    true,
		AllDelayNetReturnPositive:   true,
		CostStressNetReturnPositive: true,
		Robustness:                  evaluateRobustness(daily),
		HolmFamilyPass:              true,
	}
	if len(foldResults) == 0 {
		return StatusInconclusive, checks, ReasonFoldNetReturn
	}
	worst := math.MaxFloat64
	for _, fold := range foldResults {
		if !fold.NetReturnPositive {
			checks.AllFoldsNetReturnPositive = false
		}
		if fold.BaseNetReturnFraction < worst {
			worst = fold.BaseNetReturnFraction
		}
	}
	if worst <= 0 {
		checks.WorstFoldNetReturnPositive = false
	}
	for _, item := range lofo {
		if !item.NetReturnPositive {
			checks.AllLOFONetReturnPositive = false
			break
		}
	}
	for _, fold := range foldResults {
		for _, s := range fold.Scenarios {
			if !portfoliosim.PositiveReturn(s.InitialEquityUSDT, s.FinalEquityUSDT) {
				if s.CostStressMultiplier > 1.0+1e-9 {
					checks.CostStressNetReturnPositive = false
				} else {
					checks.AllDelayNetReturnPositive = false
				}
			}
		}
	}
	for _, attempt := range attemptResults {
		if attempt.Selected && !attempt.HolmPass {
			checks.HolmFamilyPass = false
			break
		}
	}
	if !checks.AllFoldsNetReturnPositive {
		return StatusWFFail, checks, ReasonFoldNetReturn
	}
	if !checks.WorstFoldNetReturnPositive {
		return StatusWFFail, checks, ReasonWorstFold
	}
	if !checks.AllLOFONetReturnPositive {
		return StatusWFFail, checks, ReasonLOFO
	}
	if !checks.AllDelayNetReturnPositive {
		return StatusWFFail, checks, ReasonDelay
	}
	if !checks.CostStressNetReturnPositive {
		return StatusWFFail, checks, ReasonCostStress
	}
	if !checks.Robustness.DayLeaveOneOutOK {
		return StatusWFFail, checks, ReasonDay
	}
	if !checks.Robustness.ShockDayPositiveOK {
		return StatusWFFail, checks, ReasonShock
	}
	if !checks.Robustness.SymbolClusterLOO_OK {
		return StatusWFFail, checks, ReasonCluster
	}
	if !checks.HolmFamilyPass {
		return StatusWFFail, checks, ReasonHolm
	}
	return StatusWFPass, checks, ""
}

func evaluateRobustness(daily portfoliosim.DailyEquityReplay) RobustnessChecks {
	checks := RobustnessChecks{
		DayLeaveOneOutOK:    true,
		ShockDayPositiveOK:  true,
		SymbolClusterLOO_OK: true,
	}
	if len(daily.DailyPnL) == 0 {
		checks.DayLeaveOneOutOK = false
		checks.ShockDayPositiveOK = false
		return checks
	}
	total := 0.0
	for _, pnl := range daily.DailyPnL {
		total += pnl
	}
	for _, pnl := range daily.DailyPnL {
		if total-pnl <= 0 {
			checks.DayLeaveOneOutOK = false
			break
		}
	}
	shockTotal := 0.0
	for _, pnl := range daily.ShockDayPnL {
		shockTotal += pnl
	}
	if len(daily.ShockDayPnL) > 0 && shockTotal <= 0 {
		checks.ShockDayPositiveOK = false
	}
	if len(daily.ClusterPnL) > 1 {
		clusterTotal := 0.0
		for _, pnl := range daily.ClusterPnL {
			clusterTotal += pnl
		}
		for _, pnl := range daily.ClusterPnL {
			if clusterTotal-pnl <= 0 {
				checks.SymbolClusterLOO_OK = false
				break
			}
		}
	}
	return checks
}

// BuildLOFOResults computes leave-one-fold-out net return checks.
func BuildLOFOResults(folds []Fold, foldResults []FoldResult) []LOFOResult {
	if len(folds) != len(foldResults) {
		return nil
	}
	out := make([]LOFOResult, 0, len(folds))
	for exclude := range folds {
		sumInitial := 0.0
		sumFinal := 0.0
		for i, fold := range foldResults {
			if i == exclude {
				continue
			}
			base := portfoliosim.BaseScenario(fold.Scenarios)
			if base == nil {
				continue
			}
			sumInitial += base.InitialEquityUSDT
			sumFinal += base.FinalEquityUSDT
		}
		net := portfoliosim.NetReturnFraction(sumInitial, sumFinal)
		out = append(out, LOFOResult{
			ExcludedFoldIndex: exclude,
			NetReturnFraction: net,
			NetReturnPositive: net > 0,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExcludedFoldIndex < out[j].ExcludedFoldIndex })
	return out
}

// SummarizeFoldResult picks base scenario metrics for one fold.
func SummarizeFoldResult(foldIndex int, scenarios []portfoliosim.ScenarioResult) FoldResult {
	base := portfoliosim.BaseScenario(scenarios)
	result := FoldResult{FoldIndex: foldIndex, Scenarios: scenarios}
	if base != nil {
		result.BaseNetReturnFraction = base.NetReturnFraction
		result.NetReturnPositive = base.NetReturnFraction > 0
	}
	return result
}

// MaxSymbolContributionOK checks frozen concentration cap.
func MaxSymbolContributionOK(fraction float64) bool {
	return fraction <= g5protocol.ValidationMaxSymbolPositiveContributionFraction+1e-12
}
