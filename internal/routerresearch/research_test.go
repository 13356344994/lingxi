package routerresearch

import (
	"fmt"
	"testing"
	"time"
)

func observations(candidate string, clusters int, value float64) []Observation {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]Observation, 0, clusters*3)
	for i := 0; i < clusters; i++ {
		for j := 0; j < 3; j++ {
			out = append(out, Observation{
				CandidateID:  candidate,
				DecisionTime: base.Add(time.Duration(i*72+j*12) * time.Hour),
				ClusterID:    fmt.Sprintf("%s-cluster-%02d", candidate, i),
				NetBPS:       value,
			})
		}
	}
	return out
}

func frozenOptions() Options {
	return Options{
		Folds: 3, Purge: time.Hour, Embargo: time.Hour,
		MinSamples: 30, MinClusters: 10, FamilyWiseAlpha: 0.01,
		ResearchDataHash: "research-data",
	}
}

func TestResearchPassRequiresForwardFoldsClustersAndHolm(t *testing.T) {
	report, err := Run(observations("candidate", 20, 5), frozenOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Verdicts) != 1 || !report.Verdicts[0].Pass || report.Verdicts[0].Reason != "PASS" {
		t.Fatalf("unexpected verdict: %+v", report.Verdicts)
	}
	if err := ValidateReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestInsufficientHistoryIsInconclusiveNotFutureWait(t *testing.T) {
	report, err := Run(observations("candidate", 2, 100), frozenOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdicts[0].Pass || report.Verdicts[0].Reason != "INCONCLUSIVE" {
		t.Fatalf("insufficient history must not pass: %+v", report.Verdicts[0])
	}
}

func TestNegativeCandidateFailsRobustness(t *testing.T) {
	report, err := Run(observations("candidate", 20, -1), frozenOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdicts[0].Pass || report.Verdicts[0].Reason != "ROBUSTNESS_GATE" {
		t.Fatalf("negative candidate must fail: %+v", report.Verdicts[0])
	}
}
