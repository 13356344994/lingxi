package walkforward

import (
	"math"
	"sort"

	g6protocol "lingxi/internal/routerg6/protocol"
)

// EvaluateWFGates applies frozen G6 walk-forward gates to fold replay results.
func EvaluateWFGates(foldResults []FoldResult, lofo []LOFOResult, daily DailyEquityReplay, attemptResults []AttemptSignResult) (status string, checks WFGateChecks, reason string) {
	checks = WFGateChecks{
		AllFoldsNetReturnPositive:   true,
		WorstFoldNetReturnPositive:  true,
		AllLOFONetReturnPositive:    true,
		AllDelayNetReturnPositive:   true,
		CostStressNetReturnPositive: true,
		Robustness:                  evaluateRobustness(daily),
		MaxDrawdownOK:               true,
		CapacityFailRateOK:          true,
		NoLeverageSolvencyBreach:    true,
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
			if !PositiveReturn(s.InitialEquityUSDT, s.FinalEquityUSDT) {
				if s.CostStressMultiplier > 1.0+1e-9 {
					checks.CostStressNetReturnPositive = false
				} else {
					checks.AllDelayNetReturnPositive = false
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

// MaxDrawdownOK checks frozen WF max drawdown cap.
func MaxDrawdownOK(dd float64) bool {
	return dd <= WFMaxDrawdownFraction+1e-12
}

// CapacityFailRateOK checks frozen WF capacity skip ceiling on one scenario.
func CapacityFailRateOK(s TierScenarioResult) bool {
	if s.EventsSeen <= 0 {
		return true
	}
	rate := float64(s.SkipBelowMinNotional+s.SkipBelowLotFloor+s.SkipCapacity) / float64(s.EventsSeen)
	return rate < WFMaxCapacityFailFraction+1e-12
}

func evaluateRobustness(daily DailyEquityReplay) RobustnessChecks {
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

// BuildLOFOResults computes leave-one-fold-out net return checks per capital tier.
func BuildLOFOResults(folds []Fold, foldResults []FoldResult) []LOFOResult {
	if len(folds) != len(foldResults) {
		return nil
	}
	tiers := uniqueTiers(foldResults)
	out := make([]LOFOResult, 0, len(folds)*len(tiers))
	for _, tier := range tiers {
		for exclude := range folds {
			sumInitial := 0.0
			sumFinal := 0.0
			for i, fold := range foldResults {
				if fold.CapitalTier != tier || i == exclude {
					continue
				}
				base := baseScenario(fold.Scenarios)
				if base == nil {
					continue
				}
				sumInitial += base.InitialEquityUSDT
				sumFinal += base.FinalEquityUSDT
			}
			net := netReturnFraction(sumInitial, sumFinal)
			out = append(out, LOFOResult{
				CapitalTier:       tier,
				ExcludedFoldIndex: exclude,
				NetReturnFraction: net,
				NetReturnPositive: net > 0,
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

// EvaluateCardAcrossTiers collapses tier gate outcomes to one card status.
func EvaluateCardAcrossTiers(tierStatuses []string) (status, reason string) {
	status = StatusWFPass
	rank := map[string]int{
		StatusWFPass:                0,
		StatusWFFail:                1,
		StatusInconclusive:          2,
		StatusSkippedValidationFail: 3,
	}
	for i, s := range tierStatuses {
		if rank[s] > rank[status] {
			status = s
			reason = tierStatusesReason(i)
		}
	}
	return status, reason
}

func tierStatusesReason(_ int) string { return ReasonFoldNetReturn }

// AllTiersMustPass returns whether every capital tier passed WF gates.
func AllTiersMustPass(results []CardWFResult) bool {
	for _, r := range results {
		if r.Status != StatusWFPass {
			return false
		}
	}
	return len(results) > 0
}

// GateChecksForTier filters fold results to one capital tier.
func GateChecksForTier(allFolds []FoldResult, lofo []LOFOResult, daily DailyEquityReplay, attempts []AttemptSignResult, tier int) (string, WFGateChecks, string) {
	var tierFolds []FoldResult
	for _, fold := range allFolds {
		if fold.CapitalTier == tier {
			tierFolds = append(tierFolds, fold)
		}
	}
	var tierLOFO []LOFOResult
	for _, item := range lofo {
		if item.CapitalTier == tier {
			tierLOFO = append(tierLOFO, item)
		}
	}
	return EvaluateWFGates(tierFolds, tierLOFO, daily, attempts)
}

// WorstTierStatus picks the worst status across capital tiers for one card.
func WorstTierStatus(tierStatuses map[int]string) string {
	worst := StatusWFPass
	rank := map[string]int{StatusWFPass: 0, StatusWFFail: 1, StatusInconclusive: 2}
	for _, s := range tierStatuses {
		if rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// TierEquity returns frozen initial equity for one tier.
func TierEquity(tier int) float64 {
	for _, eq := range g6protocol.CapitalTiersUSDT {
		if int(eq) == tier {
			return eq
		}
	}
	return float64(tier)
}
