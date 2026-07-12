package mechanism

import (
	"fmt"
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/validation"
)

func TestPreliminaryWFGatesPass(t *testing.T) {
	stats := validation.FiringStats{
		Count: 35, UniqueDays: 12, UniqueSymbols: 6,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1.5,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.10,
		CapacityFailRate:                0.2,
	}
	checks, status := evaluatePreliminaryWFGates(stats, 12)
	if status != "" {
		t.Fatalf("expected pass preliminary gates, got status=%s", status)
	}
	if !checks.SignTestEligible {
		t.Fatal("expected sign test eligibility")
	}
}

func TestPreliminaryWFGatesInconclusiveCoverage(t *testing.T) {
	stats := validation.FiringStats{Count: 10, UniqueDays: 4, UniqueSymbols: 3}
	_, status := evaluatePreliminaryWFGates(stats, 4)
	if status != protocol.StrictWFInsufficientSampleState {
		t.Fatalf("status=%s", status)
	}
}

func TestPreliminaryWFGatesClosedMeans(t *testing.T) {
	stats := validation.FiringStats{
		Count: 35, UniqueDays: 12, UniqueSymbols: 6,
		FeeNetMeanBPS: -1, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.10, CapacityFailRate: 0.1,
	}
	_, status := evaluatePreliminaryWFGates(stats, 12)
	if status != protocol.StrictWFGateFailureState {
		t.Fatalf("status=%s", status)
	}
}

func TestPreliminaryWFGatesClosedCapacity(t *testing.T) {
	stats := validation.FiringStats{
		Count: 35, UniqueDays: 12, UniqueSymbols: 6,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.10, CapacityFailRate: 0.6,
	}
	_, status := evaluatePreliminaryWFGates(stats, 12)
	if status != protocol.StrictWFGateFailureState {
		t.Fatalf("status=%s", status)
	}
}

func TestHolmRejectsWeakerCandidate(t *testing.T) {
	results := []WFResult{
		{
			CanonicalHash: "strong",
			GateChecks:    WFGateChecks{SignTestEligible: true},
			SignPValue:    0.001,
			TemplateID:    protocol.CrossAssetTemplateID,
			Mechanism:     protocol.CrossAssetMechanism,
		},
		{
			CanonicalHash: "weak",
			GateChecks:    WFGateChecks{SignTestEligible: true},
			SignPValue:    0.02,
			TemplateID:    protocol.SessionTemplateID,
			Mechanism:     protocol.SessionMechanism,
		},
	}
	applyHolm(results, 0.01)
	if results[0].Status != protocol.StrictWFPassState {
		t.Fatalf("strong=%+v", results[0])
	}
	if results[1].Status != protocol.StrictWFGateFailureState {
		t.Fatalf("weak=%+v", results[1])
	}
}

func TestHolmPassBothStrong(t *testing.T) {
	results := []WFResult{
		{CanonicalHash: "a", GateChecks: WFGateChecks{SignTestEligible: true}, SignPValue: 0.001},
		{CanonicalHash: "b", GateChecks: WFGateChecks{SignTestEligible: true}, SignPValue: 0.002},
	}
	applyHolm(results, 0.01)
	for _, result := range results {
		if result.Status != protocol.StrictWFPassState {
			t.Fatalf("expected pass: %+v", result)
		}
	}
}

func TestTwoSidedSignPValueMoreExtremeSkew(t *testing.T) {
	pBalanced := twoSidedBinomialSignPValue(10, 5)
	pSkew := twoSidedBinomialSignPValue(10, 9)
	if pBalanced <= pSkew {
		t.Fatalf("balanced=%v skew=%v", pBalanced, pSkew)
	}
}

func TestSignPValueStableLargeN(t *testing.T) {
	for _, tc := range []struct {
		n, positive int
	}{
		{100, 80},
		{1000, 600},
		{1000, 1000},
	} {
		p := twoSidedBinomialSignPValue(tc.n, tc.positive)
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			t.Fatalf("n=%d positive=%d p=%v", tc.n, tc.positive, p)
		}
		if tc.positive > tc.n/2 && p >= 1 {
			t.Fatalf("n=%d positive=%d unexpectedly uninformative p=%v", tc.n, tc.positive, p)
		}
	}
}

func TestTwentyFourHourHorizonBlocksHaveEmbargoGap(t *testing.T) {
	anchor := time.Unix(0, 0).UTC()
	first, ok := independentBlockKey(anchor.Add(time.Hour), 24*time.Hour)
	if !ok {
		t.Fatal("first observation window rejected")
	}
	if _, ok := independentBlockKey(anchor.Add(25*time.Hour), 24*time.Hour); ok {
		t.Fatal("24h embargo observation must not enter a block")
	}
	second, ok := independentBlockKey(anchor.Add(49*time.Hour), 24*time.Hour)
	if !ok || first == second {
		t.Fatalf("second independent block missing: first=%s second=%s ok=%v", first, second, ok)
	}
}

func TestManualEvidenceFixtureExercisesStrictWF(t *testing.T) {
	decision := DecideAttributionWithIndependentEvidence([]string{
		"btc_relative_return", "btc_beta_proxy",
	}, protocol.CrossAssetMechanism, true)
	item := SurvivorWorkItem{
		Survivor: validation.SurvivorEntry{
			Ordinal: 1, CanonicalHash: "manual", ProfileID: "okx_1h",
			CellKey: profitmap.CellKey{Horizon: "1h"},
		},
		Decision: decision,
	}
	acc := newWFAccumulator(time.Hour)
	anchor := time.Unix(0, 0).UTC()
	step := 25 * time.Hour
	for block := 0; block < 10; block++ {
		for event := 0; event < 3; event++ {
			at := anchor.Add(time.Duration(block)*step + time.Duration(event+1)*time.Hour)
			acc.add(wfSample{
				Symbol:                fmt.Sprintf("S%d", (block*3+event)%5),
				DecisionTime:          at,
				GrossBPS:              5,
				FeeNetBPS:             4,
				ConservativeBPS:       3,
				DelayConservativeBPS:  2,
				StressConservativeBPS: 1,
			}, fmt.Sprintf("%d-%d", block, event))
		}
	}
	results := BuildWFResults(
		[]SurvivorWorkItem{item},
		map[string]*wfAccumulator{"manual": acc},
	)
	if len(results) != 1 || results[0].Status != protocol.StrictWFPassState {
		t.Fatalf("manual evidence WF result=%+v", results)
	}
	if results[0].NonzeroBlockCount != 10 {
		t.Fatalf("blocks=%d", results[0].NonzeroBlockCount)
	}
}

func TestBuildWFResultsInconclusiveWithoutAccumulator(t *testing.T) {
	items := []SurvivorWorkItem{{
		Survivor: validation.SurvivorEntry{Ordinal: 1, CanonicalHash: "h", ProfileID: "okx_1h"},
		Decision: AttributionDecision{AttributionStatus: protocol.AttributionExplained, TemplateID: protocol.CrossAssetTemplateID, Mechanism: protocol.CrossAssetMechanism},
	}}
	results := BuildWFResults(items, map[string]*wfAccumulator{})
	if len(results) != 1 || results[0].Status != protocol.StrictWFInsufficientSampleState {
		t.Fatalf("results=%+v", results)
	}
}

func TestWFPartitionLeakRejected(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b := profitmap.Boundaries{
		Start: base, DiscoveryEnd: base.Add(48 * time.Hour),
		ValidationStart: base.Add(50 * time.Hour), ValidationEnd: base.Add(72 * time.Hour),
		WFStart: base.Add(74 * time.Hour), End: base.Add(96 * time.Hour),
	}
	if err := RejectWFPartitionLeak(b, base.Add(60*time.Hour)); err == nil {
		t.Fatal("validation slice must fail walk-forward leak check")
	}
	if err := RejectWFPartitionLeak(b, base.Add(80*time.Hour)); err != nil {
		t.Fatalf("walk-forward slice must pass: %v", err)
	}
}
