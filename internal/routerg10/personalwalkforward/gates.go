package personalwalkforward

import (
	"math"
	"sort"
	"time"

	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

// EvaluateWFGates applies frozen G10P walk-forward gates to fold replay results.
func EvaluateWFGates(foldResults []FoldResult, lofo []LOFOResult, daily DailyEquityReplay, leaveOne g10personalvalidation.LeaveOneReport) (status string, checks WFGateChecks, reason string) {
	checks = WFGateChecks{
		AllFoldsNetReturnNonNegative: true, WorstFoldNetReturnNonNegative: true,
		AllLOFONetReturnNonNegative: true, AllDelayNetReturnNonNegative: true,
		CostStressNetReturnNonNegative: true, P90SlippageNetReturnNonNegative: true,
		Robustness: RobustnessChecks{
			DayLeaveOneOutOK: leaveOne.DayLOOOK, ShockDayPositiveOK: leaveOne.ShockLOOOK,
			SymbolClusterLOO_OK: leaveOne.SymbolLOOOK, AnnouncementLeaveOneOutOK: leaveOne.AnnouncementLOOOK,
			ExchangeLeaveOneOutOK: leaveOne.ExchangeLOOOK,
		},
		MaxDrawdownOK: true, CapacityFailRateOK: true, NoLeverageSolvencyBreach: true,
	}
	if len(foldResults) == 0 {
		return StatusInconclusive, checks, ReasonInsufficientEvents
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
				if s.P90SlippageStress {
					checks.P90SlippageNetReturnNonNegative = false
				} else if s.CostStressMultiplier > 1.0+1e-9 {
					checks.CostStressNetReturnNonNegative = false
				} else if s.DelayBars > 0 {
					checks.AllDelayNetReturnNonNegative = false
				}
			}
			if s.DelayBars == 0 && s.CostStressMultiplier <= 1.0+1e-9 && !s.P90SlippageStress {
				if !maxDrawdownOK(s.MaxDrawdownFraction) {
					checks.MaxDrawdownOK = false
				}
				if s.LeverageBreach || s.SolvencyBreach {
					checks.NoLeverageSolvencyBreach = false
				}
				if s.TriggersSeen > 0 && capacityFailRate(s) >= WFMaxCapacityFailFraction {
					checks.CapacityFailRateOK = false
				}
			}
		}
	}
	if !checks.AllFoldsNetReturnNonNegative {
		return StatusWFFail, checks, ReasonFoldNetReturn
	}
	if !checks.AllLOFONetReturnNonNegative {
		return StatusWFFail, checks, ReasonLOFO
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
	if !checks.MaxDrawdownOK || !checks.CapacityFailRateOK || !checks.NoLeverageSolvencyBreach {
		return StatusWFFail, checks, ReasonFoldNetReturn
	}
	if !checks.AllDelayNetReturnNonNegative || !checks.CostStressNetReturnNonNegative || !checks.P90SlippageNetReturnNonNegative {
		return StatusWFFail, checks, ReasonFoldNetReturn
	}
	return StatusWFPass, checks, ""
}

func maxDrawdownOK(dd float64) bool {
	return dd <= WFMaxDrawdownFraction+1e-12
}

func capacityFailRate(s TierScenarioResult) float64 {
	if s.TriggersSeen <= 0 {
		return 0
	}
	return float64(s.TriggersSkipped) / float64(s.TriggersSeen)
}

// BuildLOFOResults computes leave-one-fold-out net return checks per capital tier.
func BuildLOFOResults(folds []Fold, foldResults []FoldResult) []LOFOResult {
	if len(folds) == 0 || len(foldResults) == 0 {
		return nil
	}
	byTier := map[int][]FoldResult{}
	for _, fr := range foldResults {
		byTier[fr.CapitalTier] = append(byTier[fr.CapitalTier], fr)
	}
	var out []LOFOResult
	for tier, results := range byTier {
		chain := 1.0
		for _, fr := range results {
			chain *= 1 + fr.BaseNetReturnFraction
		}
		for i, fr := range results {
			excluded := 1.0
			for j, other := range results {
				if i == j {
					continue
				}
				excluded *= 1 + other.BaseNetReturnFraction
			}
			net := excluded - 1
			out = append(out, LOFOResult{
				CapitalTier: tier, ExcludedFoldIndex: fr.FoldIndex,
				NetReturnFraction: net, NetReturnNonNegative: net >= 0,
			})
		}
		_ = chain
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CapitalTier != out[j].CapitalTier {
			return out[i].CapitalTier < out[j].CapitalTier
		}
		return out[i].ExcludedFoldIndex < out[j].ExcludedFoldIndex
	})
	return out
}

// BuildFolds splits sorted WF event timestamps into foldCount chronological folds with embargo.
func BuildFolds(timestamps []string, foldCount int, embargo time.Duration) ([]Fold, error) {
	if foldCount < 1 {
		return nil, ErrInsufficientFolds
	}
	if len(timestamps) < foldCount {
		return nil, ErrInsufficientFolds
	}
	sort.Strings(timestamps)
	chunk := len(timestamps) / foldCount
	folds := make([]Fold, 0, foldCount)
	var prevEnd time.Time
	for i := 0; i < foldCount; i++ {
		start := i * chunk
		end := start + chunk
		if i == foldCount-1 {
			end = len(timestamps)
		}
		slice := timestamps[start:end]
		filtered := make([]string, 0, len(slice))
		for _, ts := range slice {
			t, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				return nil, err
			}
			if i > 0 && embargo > 0 && !t.After(prevEnd.Add(embargo)) {
				continue
			}
			filtered = append(filtered, ts)
		}
		if len(filtered) == 0 {
			return nil, ErrInsufficientFolds
		}
		startTime, _ := time.Parse(time.RFC3339Nano, filtered[0])
		endTime, _ := time.Parse(time.RFC3339Nano, filtered[len(filtered)-1])
		prevEnd = endTime
		folds = append(folds, Fold{Index: i, Timestamps: filtered, StartTime: startTime, EndTime: endTime, EventCount: len(filtered)})
	}
	return folds, nil
}

// ErrInsufficientFolds indicates too few WF events for fold partition.
var ErrInsufficientFolds = errString("insufficient wf events for folds")

type errString string

func (e errString) Error() string { return string(e) }

// PartitionEventsByFold assigns events to folds by decision timestamp.
func PartitionEventsByFold(events []WFEvent, folds []Fold) map[int][]WFEvent {
	out := map[int][]WFEvent{}
	for _, fold := range folds {
		out[fold.Index] = nil
	}
	tsToFold := map[string]int{}
	for _, fold := range folds {
		for _, ts := range fold.Timestamps {
			tsToFold[ts] = fold.Index
		}
	}
	for _, ev := range events {
		ts := ev.DecisionTime.UTC().Format(time.RFC3339Nano)
		if idx, ok := tsToFold[ts]; ok {
			out[idx] = append(out[idx], ev)
		}
	}
	return out
}

// PurgeEmbargoDuration returns embargo between WF folds for one profile/horizon.
func PurgeEmbargoDuration(profileID, horizon string) time.Duration {
	_ = profileID
	switch horizon {
	case "5m":
		return 2 * time.Hour
	case "1h":
		return 6 * time.Hour
	default:
		return 12 * time.Hour
	}
}
