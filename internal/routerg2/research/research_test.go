package research

import (
	"fmt"
	"testing"
	"time"

	"lingxi/internal/routerresearch"
)

func testBoundary() Boundary {
	return Boundary{
		DiscoveryEnd: time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
		ForwardStart: time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC),
		WFStart:      time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		End:          time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
	}
}

func testOptions() Options {
	return Options{
		MinForwardSamples: 10, MinForwardClusters: 10, DiscoveryEmbargo: 24 * time.Hour,
		ForwardAlpha: 0.01,
		WF: routerresearch.Options{
			Folds: 2, Purge: time.Hour, Embargo: time.Hour,
			MinSamples: 20, MinClusters: 10, FamilyWiseAlpha: 0.01,
			ResearchDataHash: "wf-data",
		},
	}
}

func candidateHistory(id string, value float64) []routerresearch.Observation {
	var out []routerresearch.Observation
	for i := 0; i < 10; i++ {
		out = append(out, routerresearch.Observation{
			CandidateID: id, DecisionTime: time.Date(2024, 1, 12+i*3, 0, 0, 0, 0, time.UTC),
			ClusterID: fmt.Sprintf("%s-forward-%d", id, i), NetBPS: value,
		})
	}
	for i := 0; i < 30; i++ {
		out = append(out, routerresearch.Observation{
			CandidateID: id, DecisionTime: time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(i*7) * 24 * time.Hour),
			ClusterID: fmt.Sprintf("%s-wf-%d", id, i), NetBPS: value,
		})
	}
	return out
}

func TestConditionalStableLeafCanPassWhileOtherSkyLeafCloses(t *testing.T) {
	observations := append(candidateHistory("storm/short", 5), candidateHistory("calm/long", -5)...)
	report, err := Run(observations, testBoundary(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, result := range report.Results {
		status[result.CandidateID] = result.Status
	}
	if status["storm/short"] != "RESEARCH_PASS" || status["calm/long"] != "CLOSED" {
		t.Fatalf("conditional leaves not judged independently: %+v", report.Results)
	}
}

func TestDiscoveryOnlyProfitCannotRescueForwardFailure(t *testing.T) {
	observations := []routerresearch.Observation{{
		CandidateID: "candidate", DecisionTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		ClusterID: "discovery", NetBPS: 10000,
	}}
	observations = append(observations, candidateHistory("candidate", -1)...)
	report, err := Run(observations, testBoundary(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 || report.Results[0].Status != "CLOSED" {
		t.Fatalf("discovery outcome leaked into forward judgment: %+v", report.Results)
	}
}

func TestInsufficientForwardHistoryClosesWithoutFutureWait(t *testing.T) {
	history := candidateHistory("candidate", 5)
	history = history[9:] // only one forward observation remains
	report, err := Run(history, testBoundary(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Status != "CLOSED" || report.Results[0].Reason != "INCONCLUSIVE" {
		t.Fatalf("insufficient history must close now: %+v", report.Results[0])
	}
}

func TestForwardHolmRejectsFamilyThatExceedsAllocatedAlpha(t *testing.T) {
	observations := append(candidateHistory("a", 5), candidateHistory("b", 5)...)
	opts := testOptions()
	opts.ForwardAlpha = 0.001
	report, err := Run(observations, testBoundary(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range report.Results {
		if result.Status != "CLOSED" || result.Reason != "FORWARD_MULTIPLE_TESTING" {
			t.Fatalf("forward Holm failed closed incorrectly: %+v", report.Results)
		}
	}
}
