package walkforward

import (
	"fmt"
	"math"
	"sort"
)

// EvaluateWFGates applies frozen G10 walk-forward gates to fold replay results.
func EvaluateWFGates(foldResults []FoldResult, lofo []LOFOResult, daily DailyEquityReplay, attemptResults []AttemptSignResult) (status string, checks WFGateChecks, reason string) {
	checks = WFGateChecks{
		AllFoldsNetReturnNonNegative:   true,
		WorstFoldNetReturnNonNegative:  true,
		AllLOFONetReturnNonNegative:    true,
		AllDelayNetReturnNonNegative:   true,
		CostStressNetReturnNonNegative: true,
		Robustness:                     evaluateRobustness(daily),
		MaxDrawdownOK:                  true,
		CapacityFailRateOK:             true,
		NoLeverageSolvencyBreach:       true,
		HolmFamilyPass:                 true,
	}
	if len(foldResults) == 0 {
		return StatusInconclusive, checks, ReasonFoldNetReturn
	}
	worst := math.MaxFloat64
	for _, fold := range foldResults {
		if !fold.NetReturnNonNegative {
			checks.AllFoldsNetReturnNonNegative = false
		}
		if fold.BaseNetReturnFraction < worst {
			worst = fold.BaseNetReturnFraction
		}
	}
	if worst < 0 {
		checks.WorstFoldNetReturnNonNegative = false
	}
	for _, item := range lofo {
		if !item.NetReturnNonNegative {
			checks.AllLOFONetReturnNonNegative = false
			break
		}
	}
	for _, fold := range foldResults {
		for _, s := range fold.Scenarios {
			if !s.NetReturnNonNegative {
				if s.CostStressMultiplier > 1.0+1e-9 {
					checks.CostStressNetReturnNonNegative = false
				} else if s.DelayBars > 0 {
				 checks.AllDelayNetReturnNonNegative = false
				}
			}
			if s.DelayBars == 0 && s.CostStressMultiplier <= 1.0+1e-9 {
				if !MaxDrawdownOK(s.MaxDrawdownFraction) {
					checks.MaxDrawdownOK = false
				}
				if s.LeverageBreach || s.SolvencyBreach {
					checks.NoLeverageSolvencyBreach = false
				}
				if s.EventsSeen > 0 && !CapacityFailRateOK(s) {
					checks.CapacityFailRateOK = false
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
	if !checks.AllFoldsNetReturnNonNegative {
		return StatusWFFail, checks, ReasonFoldNetReturn
	}
	if !checks.WorstFoldNetReturnNonNegative {
		return StatusWFFail, checks, ReasonWorstFold
	}
	if !checks.AllLOFONetReturnNonNegative {
		return StatusWFFail, checks, ReasonLOFO
	}
	if !checks.AllDelayNetReturnNonNegative {
		return StatusWFFail, checks, ReasonDelay
	}
	if !checks.CostStressNetReturnNonNegative {
		return StatusWFFail, checks, ReasonCostStress
	}
	if !checks.Robustness.DayLeaveOneOutOK {
		return StatusWFFail, checks, ReasonDay
	}
	if !checks.Robustness.ShockDayPositiveOK {
		return StatusWFFail, checks, ReasonShock
	}
	if !checks.Robustness.SymbolClusterLOO_OK {
		return StatusWFFail, checks, ReasonSymbol
	}
	if !checks.MaxDrawdownOK {
		return StatusWFFail, checks, ReasonMaxDrawdown
	}
	if !checks.CapacityFailRateOK {
		return StatusWFFail, checks, ReasonCapacity
	}
	if !checks.NoLeverageSolvencyBreach {
		return StatusWFFail, checks, ReasonLeverageSolvency
	}
	if !checks.HolmFamilyPass {
		return StatusWFFail, checks, ReasonHolm
	}
	return StatusWFPass, checks, ""
}

func MaxDrawdownOK(dd float64) bool {
	return dd <= WFMaxDrawdownFraction+1e-12
}

func CapacityFailRateOK(s TierScenarioResult) bool {
	if s.EventsSeen <= 0 {
		return true
	}
	rate := float64(s.SkipCapacity+s.SkipBelowMinNotional+s.SkipBelowLotFloor) / float64(s.EventsSeen)
	return rate < WFMaxCapacityFailFraction+1e-12
}

func evaluateRobustness(daily DailyEquityReplay) RobustnessChecks {
	checks := RobustnessChecks{DayLeaveOneOutOK: true, ShockDayPositiveOK: true, SymbolClusterLOO_OK: true}
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
		if total-pnl < 0 {
			checks.DayLeaveOneOutOK = false
			break
		}
	}
	shockTotal := 0.0
	for _, pnl := range daily.ShockDayPnL {
		shockTotal += pnl
	}
	if len(daily.ShockDayPnL) > 0 {
		for _, pnl := range daily.ShockDayPnL {
			if shockTotal-pnl < 0 {
				checks.ShockDayPositiveOK = false
				break
			}
		}
	}
	if len(daily.SymbolPnL) > 1 {
		clusterTotal := 0.0
		for _, pnl := range daily.SymbolPnL {
			clusterTotal += pnl
		}
		for _, pnl := range daily.SymbolPnL {
			if clusterTotal-pnl < 0 {
				checks.SymbolClusterLOO_OK = false
				break
			}
		}
	}
	return checks
}

func BuildLOFOResults(folds []Fold, foldResults []FoldResult) []LOFOResult {
	if len(folds) != len(foldResults)/max(1, uniqueTierCount(foldResults)) {
		// grouped by tier externally
	}
	tiers := uniqueTiers(foldResults)
	out := make([]LOFOResult, 0, len(folds)*len(tiers))
	for _, tier := range tiers {
		for exclude := range folds {
			net := aggregateLOFONetReturn(foldResults, tier, exclude)
			out = append(out, LOFOResult{
				CapitalTier: tier, ExcludedFoldIndex: exclude,
				NetReturnFraction: net, NetReturnNonNegative: net >= 0,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CapitalTier != out[j].CapitalTier {
			return out[i].CapitalTier < out[j].CapitalTier
		}
		return out[i].ExcludedFoldIndex < out[j].ExcludedFoldIndex
	})
	return out
}

func SummarizeFoldResult(foldIndex, tier int, scenarios []TierScenarioResult) FoldResult {
	base := baseScenario(scenarios)
	baseNet := 0.0
	nonNeg := true
	if base != nil {
		baseNet = base.NetReturnFraction
		nonNeg = base.NetReturnNonNegative
	}
	return FoldResult{
		FoldIndex: foldIndex, CapitalTier: tier, Scenarios: scenarios,
		BaseNetReturnFraction: baseNet, NetReturnNonNegative: nonNeg,
	}
}

func baseScenario(scenarios []TierScenarioResult) *TierScenarioResult {
	for i := range scenarios {
		if scenarios[i].DelayBars == 0 && scenarios[i].CostStressMultiplier <= 1.0+1e-9 {
			return &scenarios[i]
		}
	}
	if len(scenarios) > 0 {
		return &scenarios[0]
	}
	return nil
}

func uniqueTiers(folds []FoldResult) []int {
	seen := map[int]bool{}
	var tiers []int
	for _, fold := range folds {
		if seen[fold.CapitalTier] {
			continue
		}
		seen[fold.CapitalTier] = true
		tiers = append(tiers, fold.CapitalTier)
	}
	sort.Ints(tiers)
	return tiers
}

func uniqueTierCount(folds []FoldResult) int {
	return len(uniqueTiers(folds))
}

func netReturnFraction(initial, final float64) float64 {
	if initial <= 0 {
		return 0
	}
	return (final - initial) / initial
}

// aggregateLOFONetReturn chains independent fold account returns for leave-one-fold-out.
// It never pools raw equity sums across folds.
func aggregateLOFONetReturn(foldResults []FoldResult, tier, exclude int) float64 {
	growth := 1.0
	count := 0
	for _, fold := range foldResults {
		if fold.CapitalTier != tier || fold.FoldIndex == exclude {
			continue
		}
		base := baseScenario(fold.Scenarios)
		if base == nil || base.InitialEquityUSDT <= 0 {
			continue
		}
		r := base.NetReturnFraction
		if r == 0 {
			r = netReturnFraction(base.InitialEquityUSDT, base.FinalEquityUSDT)
		}
		growth *= 1 + r
		count++
	}
	if count == 0 {
		return 0
	}
	return growth - 1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// CollapseTierStatuses returns playbook WF status allowing per-tier pass.
func CollapseTierStatuses(tierStatuses map[int]string) (status string, eligible []string) {
	status = StatusWFFail
	for tier, s := range tierStatuses {
		if s == StatusWFPass {
			if status != StatusWFPass {
				status = StatusWFPass
			}
			eligible = append(eligible, tierLabel(tier))
		}
	}
	sort.Strings(eligible)
	if len(eligible) == 0 {
		for _, s := range tierStatuses {
			if s == StatusInconclusive {
				return StatusInconclusive, nil
			}
		}
	}
	return status, eligible
}

func tierLabel(tier int) string {
	return fmt.Sprintf("%d", tier)
}
