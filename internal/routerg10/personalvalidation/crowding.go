package personalvalidation

// CrowdingLevels are the frozen micro-competitor ladder (1/3/5).
var CrowdingLevels = []int{1, 3, 5}

// BuildCrowdingReport aggregates crowding stress and capacity expansion stops.
func BuildCrowdingReport(events []TriggerEvent) CrowdingReport {
	report := CrowdingReport{LevelsTested: append([]int(nil), CrowdingLevels...)}
	report.FailuresByLevel = make([]int, len(CrowdingLevels))
	for _, ev := range events {
		if ev.CapitalExpansionStop {
			report.CapacityExpansionStops++
		}
		if !ev.CrowdingPassed {
			report.AnyCrowdingFail = true
			for i, lvl := range CrowdingLevels {
				if ev.CrowdingLevel >= lvl {
					report.FailuresByLevel[i]++
				}
			}
		}
	}
	return report
}

// EvaluateCrowdingAtLevel returns whether crowding at given competitor count would block entry.
func EvaluateCrowdingAtLevel(baseEdgeBPS float64, competitors int) bool {
	collapse := float64(competitors) * 2.5
	return baseEdgeBPS-collapse < 0
}
