package personalbrain

import (
	"sort"

	"lingxi/internal/datatruth"
)

const (
	singleActionDominanceFraction = 0.85
	neverExitBarsThreshold        = 48
	alwaysDirectionalFraction     = 0.90
	degeneracyStatus              = "DEGENERATE"
)

var exitActions = map[string]bool{
	"CLOSE": true, "TRIM": true, "REVERSE": true,
}

var directionalActions = map[string]bool{
	"LONG": true, "SHORT": true, "ADD": true, "REVERSE": true,
}

// DetectDegeneracy evaluates action distribution and exit behavior.
func DetectDegeneracy(log TraceLog, pathTags map[string]bool) (DegeneracyReport, error) {
	report := DegeneracyReport{
		SchemaVersion:     TraceSchemaVersion,
		TotalBars:         len(log.Entries),
		ActionCounts:      map[string]int{},
		PathMatrixCovered: map[string]bool{},
		Status:            "OK",
	}
	if len(log.Entries) == 0 {
		report.Status = "EMPTY"
		return sealDegeneracyReport(report)
	}
	report.Generation = log.Entries[0].Generation
	report.ProtocolHash = log.Entries[0].ProtocolHash

	lastExit := -1
	for i, e := range log.Entries {
		report.ActionCounts[e.FinalAction]++
		if exitActions[e.FinalAction] || e.StopErrorState == StopErrorExit {
			lastExit = i
		}
	}
	if lastExit >= 0 {
		report.BarsSinceLastExit = len(log.Entries) - 1 - lastExit
	} else {
		report.BarsSinceLastExit = len(log.Entries)
	}

	dominant, domFrac := dominantAction(report.ActionCounts, report.TotalBars)
	report.DominantAction = dominant
	report.DominantFraction = domFrac

	dirCount := 0
	for action, n := range report.ActionCounts {
		if directionalActions[action] {
			dirCount += n
		}
	}
	if report.TotalBars > 0 {
		report.DirectionalFraction = float64(dirCount) / float64(report.TotalBars)
	}

	if domFrac > singleActionDominanceFraction {
		report.Degenerate = true
		report.DegeneracyReasons = append(report.DegeneracyReasons, "single_action_dominance")
	}
	if report.BarsSinceLastExit >= neverExitBarsThreshold {
		report.Degenerate = true
		report.DegeneracyReasons = append(report.DegeneracyReasons, "never_exit")
	}
	if report.DirectionalFraction > alwaysDirectionalFraction {
		report.Degenerate = true
		report.DegeneracyReasons = append(report.DegeneracyReasons, "always_directional")
	}

	for _, path := range PathMatrixRequired {
		covered := false
		if pathTags != nil {
			covered = pathTags[path]
		}
		report.PathMatrixCovered[path] = covered
		if !covered {
			report.PathMatrixMissing = append(report.PathMatrixMissing, path)
		}
	}
	sort.Strings(report.PathMatrixMissing)

	if report.Degenerate {
		report.Status = degeneracyStatus
	}
	return sealDegeneracyReport(report)
}

func dominantAction(counts map[string]int, total int) (string, float64) {
	if total == 0 {
		return "", 0
	}
	bestAction := ""
	bestCount := 0
	for action, n := range counts {
		if n > bestCount {
			bestCount = n
			bestAction = action
		}
	}
	return bestAction, float64(bestCount) / float64(total)
}

func sealDegeneracyReport(report DegeneracyReport) (DegeneracyReport, error) {
	payload := report
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		return DegeneracyReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}
