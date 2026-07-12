package research

import (
	"fmt"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
	"lingxi/internal/routerresearch"
)

const (
	testPatternA = "ret=low/te=*/rv=mid/dd=*/fund=high"
	testPatternB = "ret=high/te=*/rv=low/dd=*/fund=low"
	testFullSkyA = "ret=low/te=high/rv=mid/dd=low/fund=high"
	testFullSkyB = "ret=high/te=mid/rv=low/dd=high/fund=low"
)

func testBoundary() Boundary {
	return Boundary{
		ForwardStart: time.Date(2024, 1, 11, 0, 0, 0, 0, time.UTC),
		ForwardEnd:   time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		WFStart:      time.Date(2024, 3, 8, 0, 0, 0, 0, time.UTC),
		End:          time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
	}
}

func testOptions() Options {
	return Options{
		MinForwardSamples:  protocol.G3MinForwardSamples,
		MinForwardClusters: protocol.G3MinForwardClusters,
		ForwardAlpha:       protocol.ForwardAlpha,
		WF: routerresearch.Options{
			Folds:      protocol.G3WalkForwardFolds,
			Purge:      time.Duration(protocol.G3PurgeHours) * time.Hour,
			Embargo:    time.Duration(protocol.G3WFEmbargoHours) * time.Hour,
			MinSamples: protocol.G3WFMinSamples, MinClusters: protocol.G3WFMinClusters,
			FamilyWiseAlpha:  protocol.WalkForwardAlpha,
			ResearchDataHash: "wf-data",
		},
	}
}

func firingRule(sky string) g3reverse.Rule {
	rule := g3reverse.Rule{
		Ordinal: 1, SkyState: sky, SorterDomain: "liquid", Horizon: g3reverse.Horizon1h,
		Direction: mechanism.DirectionLong,
		Conditions: []g3reverse.RuleCondition{{
			FeatureKey: "rubik_lsratio_1h", Op: g3reverse.OpGTE, Threshold: 0.5, Quantile: 0.5,
		}},
	}
	hash, _ := g3reverse.CanonicalRuleHash(rule)
	rule.CanonicalHash = hash
	return rule
}

func event(id, sky, cluster string, when time.Time, feature, net float64) g3reverse.Event {
	return g3reverse.Event{
		ID: id, Symbol: "BTCUSDT", Cluster: cluster, SkyState: sky, SorterDomain: "liquid",
		LiquidityBucket: "liq_6", Horizon: g3reverse.Horizon1h, DecisionTime: when,
		FeatureAsOf: when.Add(-time.Minute), EntryTime: when, ExitTime: when.Add(time.Hour),
		EntryPrice: 100, ExitPrice: 100, Direction: mechanism.DirectionLong,
		Features: map[string]float64{"rubik_lsratio_1h": feature}, NetOutcomeBPS: net,
		OutcomeClass: g3reverse.OutcomeWinner,
	}
}

func candidateHistory(id, pattern, fullSky string, value float64) (forward, wf []g3reverse.Event) {
	rule := firingRule(pattern)
	for i := 0; i < 35; i++ {
		forward = append(forward, event(
			fmt.Sprintf("%s-f-%d", id, i), fullSky, fmt.Sprintf("%s-forward-%d", id, i),
			time.Date(2024, 1, 12, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*24*time.Hour), 1.0, value,
		))
	}
	for i := 0; i < 60; i++ {
		wf = append(wf, event(
			fmt.Sprintf("%s-w-%d", id, i), fullSky, fmt.Sprintf("%s-wf-%d", id, i),
			time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC).Add(time.Duration(i*4)*24*time.Hour), 1.0, value,
		))
	}
	_ = rule
	return forward, wf
}

func TestConditionalStableLeafCanPassWhileOtherSkyLeafCloses(t *testing.T) {
	passForward, passWF := candidateHistory("pass", testPatternA, testFullSkyA, 5)
	failForward, failWF := candidateHistory("fail", testPatternB, testFullSkyB, -5)
	report, err := Run(
		[]CandidateRule{
			{CandidateID: "pass", Rule: firingRule(testPatternA)},
			{CandidateID: "fail", Rule: firingRule(testPatternB)},
		},
		append(passForward, failForward...), append(passWF, failWF...),
		testBoundary(), testOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, result := range report.Results {
		status[result.CandidateID] = result.Status
	}
	if status["pass"] != "RESEARCH_PASS" || status["fail"] != "CLOSED" {
		t.Fatalf("conditional leaves not judged independently: %+v", report.Results)
	}
}

func TestForwardFailureClosesCandidate(t *testing.T) {
	forward, wf := candidateHistory("candidate", testPatternA, testFullSkyA, -1)
	report, err := Run(
		[]CandidateRule{{CandidateID: "candidate", Rule: firingRule(testPatternA)}},
		forward, wf, testBoundary(), testOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Status != "CLOSED" {
		t.Fatalf("forward fail must close: %+v", report.Results[0])
	}
}

func TestWalkForwardFailureClosesAfterForwardPass(t *testing.T) {
	forward, _ := candidateHistory("candidate", testPatternA, testFullSkyA, 5)
	var wf []g3reverse.Event
	for i := 0; i < 60; i++ {
		wf = append(wf, event(
			fmt.Sprintf("w-%d", i), testFullSkyA, fmt.Sprintf("wf-%d", i),
			time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC).Add(time.Duration(i*4)*24*time.Hour),
			1.0, -2,
		))
	}
	report, err := Run(
		[]CandidateRule{{CandidateID: "candidate", Rule: firingRule(testPatternA)}},
		forward, wf, testBoundary(), testOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Status != "CLOSED" {
		t.Fatalf("wf fail must close: %+v", report.Results[0])
	}
}

func TestProductionOptionDriftRejected(t *testing.T) {
	f1, w1 := candidateHistory("a", testPatternA, testFullSkyA, 5)
	opts := testOptions()
	opts.ForwardAlpha = 0.0001
	if _, err := Run(
		[]CandidateRule{{CandidateID: "a", Rule: firingRule(testPatternA)}},
		f1, w1, testBoundary(), opts,
	); err == nil {
		t.Fatal("production option drift must fail")
	}
}

func TestInsufficientForwardHistoryIsInconclusive(t *testing.T) {
	forward, wf := candidateHistory("candidate", testPatternA, testFullSkyA, 5)
	forward = forward[34:]
	report, err := Run(
		[]CandidateRule{{CandidateID: "candidate", Rule: firingRule(testPatternA)}},
		forward, wf, testBoundary(), testOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Results[0].Reason != "INCONCLUSIVE" {
		t.Fatalf("insufficient forward must be inconclusive: %+v", report.Results[0])
	}
}

func TestUsesFrozenProductionGates(t *testing.T) {
	opts := Options{
		MinForwardSamples:  protocol.G3MinForwardSamples,
		MinForwardClusters: protocol.G3MinForwardClusters,
		ForwardAlpha:       protocol.ForwardAlpha,
		WF: routerresearch.Options{
			Folds:      protocol.G3WalkForwardFolds,
			Purge:      time.Duration(protocol.G3PurgeHours) * time.Hour,
			Embargo:    time.Duration(protocol.G3WFEmbargoHours) * time.Hour,
			MinSamples: protocol.G3WFMinSamples, MinClusters: protocol.G3WFMinClusters,
			FamilyWiseAlpha: protocol.WalkForwardAlpha, ResearchDataHash: "wf",
		},
	}
	if opts.ForwardAlpha != 0.002 || opts.WF.FamilyWiseAlpha != 0.002 {
		t.Fatal("frozen alpha mismatch")
	}
}

func TestBoundaryEmbargoAndEventLeakRejected(t *testing.T) {
	forward, wf := candidateHistory("candidate", testPatternA, testFullSkyA, 5)
	boundary := testBoundary()
	boundary.WFStart = boundary.ForwardEnd.Add(167 * time.Hour)
	if _, err := Run(
		[]CandidateRule{{CandidateID: "candidate", Rule: firingRule(testPatternA)}},
		forward, wf, boundary, testOptions(),
	); err == nil {
		t.Fatal("forward-to-WF embargo drift must fail")
	}
	boundary = testBoundary()
	forward[0].DecisionTime = boundary.ForwardEnd
	if _, err := Run(
		[]CandidateRule{{CandidateID: "candidate", Rule: firingRule(testPatternA)}},
		forward, wf, boundary, testOptions(),
	); err == nil {
		t.Fatal("out-of-window event must fail instead of being skipped")
	}
}

func TestDuplicateCandidateOrRuleRejected(t *testing.T) {
	forward, wf := candidateHistory("candidate", testPatternA, testFullSkyA, 5)
	rule := firingRule(testPatternA)
	if _, err := Run(
		[]CandidateRule{{CandidateID: "a", Rule: rule}, {CandidateID: "a", Rule: firingRule(testPatternB)}},
		forward, wf, testBoundary(), testOptions(),
	); err == nil {
		t.Fatal("duplicate candidate id must fail")
	}
	if _, err := Run(
		[]CandidateRule{{CandidateID: "a", Rule: rule}, {CandidateID: "b", Rule: rule}},
		forward, wf, testBoundary(), testOptions(),
	); err == nil {
		t.Fatal("duplicate frozen rule must fail")
	}
}
