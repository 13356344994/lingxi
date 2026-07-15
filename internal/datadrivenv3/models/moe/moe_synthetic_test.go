package moe

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

// mkExample builds a sealed synthetic training example. It never carries a
// reward label; only the frozen synthetic target intent is attached.
func mkExample(id string, features [modelapi.FeatureCount]float64, missing uint64, frac float64, horizon, delay int) modelapi.TrainingExample {
	return modelapi.TrainingExample{
		SchemaVersion: modelapi.SchemaVersion,
		Kind:          modelapi.ExampleSynthetic,
		ExampleID:     id,
		Market: modelapi.MarketState{
			SchemaVersion: modelapi.SchemaVersion,
			EventID:       "state-" + id,
			DecisionMS:    1,
			Features:      features,
			MissingBits:   missing,
		},
		SyntheticTarget: modelapi.TargetIntent{
			TargetSignedPositionFraction: frac,
			TargetHorizonHours:           horizon,
			DelayBars:                    delay,
		},
	}
}

// stateRouterExamples encodes a clean, state-dependent rule: when the short
// horizon own-return is positive the synthetic target fades short, otherwise it
// leans long. The two regions carry opposite signed directions.
func stateRouterExamples() []modelapi.TrainingExample {
	out := make([]modelapi.TrainingExample, 0, 24)
	for k := 0; k < 24; k++ {
		var f [modelapi.FeatureCount]float64
		r := float64(k-12) * 0.01
		f[0] = r                        // own_return_1h drives the split
		f[8] = float64(k%5)*0.01 - 0.02 // btc_return_1h, weak nuisance
		f[3] = 0.2 + float64(k%3)*0.05  // own_rms_vol_4h, present spread
		frac := 0.5
		if r > 0 {
			frac = -0.5
		}
		out = append(out, mkExample(fmt.Sprintf("%02d", k), f, 0, frac, 1, 0))
	}
	return out
}

func marketWith(feature0 float64) modelapi.MarketState {
	var f [modelapi.FeatureCount]float64
	f[0] = feature0
	f[3] = 0.25
	return modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: "probe", DecisionMS: 1, Features: f}
}

func TestFitRoutesDistinctStatesToDistinctExperts(t *testing.T) {
	m := New()
	if _, err := m.Fit(context.Background(), stateRouterExamples()); err != nil {
		t.Fatalf("fit failed: %v", err)
	}
	if got := len(m.Leaves()); got < 2 {
		t.Fatalf("mixture collapsed to %d expert(s); want multi-expert", got)
	}
	if m.Choice().Experts < 2 {
		t.Fatalf("selected fewer than 2 experts: %+v", m.Choice())
	}

	bull := marketWith(-0.10)
	bear := marketWith(0.10)
	bullExpert, err := m.RouteExpert(bull)
	if err != nil {
		t.Fatal(err)
	}
	bearExpert, err := m.RouteExpert(bear)
	if err != nil {
		t.Fatal(err)
	}
	if bullExpert == bearExpert {
		t.Fatalf("opposite states routed to the same expert %d", bullExpert)
	}

	bullIntent, err := m.Predict(context.Background(), bull, modelapi.AccountContext{})
	if err != nil {
		t.Fatal(err)
	}
	bearIntent, err := m.Predict(context.Background(), bear, modelapi.AccountContext{})
	if err != nil {
		t.Fatal(err)
	}
	if bullIntent.TargetSignedPositionFraction <= 0 {
		t.Fatalf("down-move state did not lean long: %+v", bullIntent)
	}
	if bearIntent.TargetSignedPositionFraction >= 0 {
		t.Fatalf("up-move state did not fade short: %+v", bearIntent)
	}
	if err := bullIntent.Validate(); err != nil {
		t.Fatalf("intent left the frozen grid: %v", err)
	}
}

func TestPredicatesAreReplayableAndPolicyMatchesExperts(t *testing.T) {
	m := New()
	policy, err := m.Fit(context.Background(), stateRouterExamples())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) != len(m.Leaves()) {
		t.Fatalf("policy leaves %d != expert regions %d", len(policy.Leaves), len(m.Leaves()))
	}
	sawSplit := false
	for _, leaf := range m.Leaves() {
		if leaf.Predicate == "" {
			t.Fatal("empty predicate is not replayable")
		}
		if leaf.Predicate != "ALL_STATES" {
			sawSplit = true
		}
		if err := leaf.Representative.Validate(); err != nil {
			t.Fatalf("representative intent off grid: %v", err)
		}
	}
	if !sawSplit {
		t.Fatal("no gate split predicate was produced")
	}
}

func TestMissingAwareRoutingFollowsFrozenBranch(t *testing.T) {
	m := New()
	if _, err := m.Fit(context.Background(), stateRouterExamples()); err != nil {
		t.Fatal(err)
	}
	present := marketWith(0.10)
	missing := present
	missing.Features[0] = 0
	missing.MissingBits = uint64(1) << 0 // own_return_1h unavailable

	got, err := m.Predict(context.Background(), missing, modelapi.AccountContext{})
	if err != nil {
		t.Fatalf("missing-aware predict failed: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("missing-aware intent off grid: %v", err)
	}
	// Routing must be well defined even though the split feature is absent.
	if _, err := m.RouteExpert(missing); err != nil {
		t.Fatalf("routing with a missing split feature failed: %v", err)
	}
}

func TestDeterministicUnderSeed42(t *testing.T) {
	examples := stateRouterExamples()
	a, b := New(), New()
	pa, err := a.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(pa)
	jb, _ := json.Marshal(pb)
	if string(ja) != string(jb) {
		t.Fatalf("policy not deterministic:\n%s\n%s", ja, jb)
	}
	if a.HeadHash() != b.HeadHash() || a.HeadHash() == "" {
		t.Fatalf("ledger head hash not deterministic: %q vs %q", a.HeadHash(), b.HeadHash())
	}
	for _, f0 := range []float64{-0.1, -0.02, 0.02, 0.1} {
		ia, err := a.Predict(context.Background(), marketWith(f0), modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		ib, err := b.Predict(context.Background(), marketWith(f0), modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		if ia != ib {
			t.Fatalf("prediction not deterministic at %.2f: %+v vs %+v", f0, ia, ib)
		}
	}
}

func TestBudgetRespectsAttemptAndLeafCaps(t *testing.T) {
	m := New()
	if _, err := m.Fit(context.Background(), stateRouterExamples()); err != nil {
		t.Fatal(err)
	}
	c := m.Choice()
	if c.Attempts > MaxAttempts {
		t.Fatalf("attempts %d exceed budget %d", c.Attempts, MaxAttempts)
	}
	if c.LeavesUsed > MaxLeaves {
		t.Fatalf("leaves %d exceed budget %d", c.LeavesUsed, MaxLeaves)
	}
	if c.Seed != Seed {
		t.Fatalf("seed drift: %d", c.Seed)
	}

	tight := New()
	if _, err := tight.fitWithBudget(context.Background(), stateRouterExamples(), 1, MaxLeaves); err != nil {
		t.Fatal(err)
	}
	if tight.Choice().Attempts != 1 {
		t.Fatalf("attempt cap of 1 not honored: %d", tight.Choice().Attempts)
	}

	capped := New()
	if _, err := capped.fitWithBudget(context.Background(), stateRouterExamples(), MaxAttempts, 3); err != nil {
		t.Fatal(err)
	}
	if capped.Choice().LeavesUsed > 3 {
		t.Fatalf("leaf cap of 3 not honored: %d", capped.Choice().LeavesUsed)
	}
}

func TestL1PenaltyShrinksActiveWeights(t *testing.T) {
	n := 20
	feats := make([][modelapi.FeatureCount]float64, n)
	pres := make([][modelapi.FeatureCount]bool, n)
	examples := make([]modelapi.TrainingExample, n)
	idx := make([]int, n)
	y := make([]float64, n)
	for i := 0; i < n; i++ {
		a := float64(i%10) - 4.5     // strong, informative feature
		b := float64((i*7)%11) - 5.0 // independent, weak feature
		feats[i][0] = a
		feats[i][8] = b
		for j := 0; j < modelapi.FeatureCount; j++ {
			pres[i][j] = true
		}
		y[i] = 0.05*a + 0.008*b
		examples[i] = mkExample(fmt.Sprintf("l1-%02d", i), feats[i], 0, 0, 1, 0)
		idx[i] = i
	}

	tiny := buildExpert(0, y, feats, pres, examples, idx, 0.0001).nonzeroWeights()
	low := buildExpert(0, y, feats, pres, examples, idx, 0.001).nonzeroWeights()
	high := buildExpert(0, y, feats, pres, examples, idx, 0.01).nonzeroWeights()
	huge := buildExpert(0, y, feats, pres, examples, idx, 100.0).nonzeroWeights()

	if tiny < 1 {
		t.Fatalf("negligible L1 should keep the informative feature active, got %d", tiny)
	}
	if high > low {
		t.Fatalf("stronger L1 increased active weights: low=%d high=%d", low, high)
	}
	if huge != 0 {
		t.Fatalf("saturating L1 should zero every weight, got %d", huge)
	}
}

func TestDegenerateTargetsRejected(t *testing.T) {
	out := make([]modelapi.TrainingExample, 0, 12)
	for k := 0; k < 12; k++ {
		var f [modelapi.FeatureCount]float64
		f[0] = float64(k) * 0.01
		out = append(out, mkExample(fmt.Sprintf("flat-%02d", k), f, 0, 0.5, 1, 0))
	}
	m := New()
	if _, err := m.Fit(context.Background(), out); err == nil {
		t.Fatal("a fixed always-long target set must be rejected as degenerate")
	}
}

func TestUnfittedPredictErrorsAndLedgerGrows(t *testing.T) {
	m := New()
	if _, err := m.Predict(context.Background(), marketWith(0.1), modelapi.AccountContext{}); err == nil {
		t.Fatal("predict before fit must fail closed")
	}
	if _, err := m.Fit(context.Background(), stateRouterExamples()); err != nil {
		t.Fatal(err)
	}
	entries := m.Ledger()
	if len(entries) < 3 {
		t.Fatalf("expected begin/attempt/select ledger entries, got %d", len(entries))
	}
	if entries[0].PreviousHash != "" {
		t.Fatal("first ledger entry must have no predecessor")
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].PreviousHash != entries[i-1].EntryHash {
			t.Fatalf("ledger hash chain broken at %d", i)
		}
	}
}
