package changepoint

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

const (
	featOwnRMSVol24h = 4
	featBTCReturn24h = 10
	featQuoteVol24h  = 6
)

func synthExample(id string, day int64, vol, btcRet float64, missing uint64, frac float64) modelapi.TrainingExample {
	var features [modelapi.FeatureCount]float64
	features[featOwnRMSVol24h] = vol
	features[featBTCReturn24h] = btcRet
	features[featQuoteVol24h] = 1e6
	features[0] = vol * 0.1 // own_return_1h correlated with regime
	return modelapi.TrainingExample{
		SchemaVersion: modelapi.SchemaVersion,
		Kind:          modelapi.ExampleSynthetic,
		ExampleID:     id,
		Market: modelapi.MarketState{
			SchemaVersion: modelapi.SchemaVersion,
			EventID:       "evt-" + id,
			DecisionMS:    day*MSPerDay + 3_600_000,
			Features:      features,
			MissingBits:   missing,
		},
		Account: modelapi.AccountContext{CashUSDT: 100, MarkedEquityUSDT: 100, RetainedCapitalBaseUSDT: 100, FreeEquityUSDT: 100},
		SyntheticTarget: modelapi.TargetIntent{
			TargetSignedPositionFraction: frac,
			TargetHorizonHours:           1,
			DelayBars:                    0,
		},
	}
}

func twoRegimeSeries() []modelapi.TrainingExample {
	out := make([]modelapi.TrainingExample, 0, 40)
	for d := int64(0); d < 20; d++ {
		out = append(out, synthExample(fmt.Sprintf("a-%02d", d), d, 0.05, -0.02, 0, 0.5))
	}
	for d := int64(20); d < 40; d++ {
		out = append(out, synthExample(fmt.Sprintf("b-%02d", d), d, 0.01, 0.03, 0, -0.5))
	}
	return out
}

func rareStateSeries() []modelapi.TrainingExample {
	// 45 + 10 + 25 day blocks: imbalanced third regime is rare under equal-share rule.
	out := make([]modelapi.TrainingExample, 0, 80)
	for d := int64(0); d < 45; d++ {
		out = append(out, synthExample(fmt.Sprintf("r0-%02d", d), d, 0.04, 0.01, 0, 0.25))
	}
	for d := int64(45); d < 55; d++ {
		out = append(out, synthExample(fmt.Sprintf("rare-%02d", d), d, 0.20, -0.08, 0, -1))
	}
	for d := int64(55); d < 80; d++ {
		out = append(out, synthExample(fmt.Sprintf("r2-%02d", d), d, 0.015, 0.02, 0, 0.1))
	}
	return out
}

func TestChangeDetectionSynthetic(t *testing.T) {
	m := New()
	policy, err := m.Fit(context.Background(), twoRegimeSeries())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) < 2 {
		t.Fatalf("expected >=2 regime leaves, got %d", len(policy.Leaves))
	}
	for _, leaf := range policy.Leaves {
		if containsAbsoluteDate(leaf.Predicate) {
			t.Fatalf("absolute date in predicate: %s", leaf.Predicate)
		}
		if !strings.Contains(leaf.Predicate, "regime_id=") {
			t.Fatalf("missing regime_id in predicate: %s", leaf.Predicate)
		}
	}
	high := synthExample("probe-high", 100, 0.05, -0.02, 0, 0)
	low := synthExample("probe-low", 101, 0.01, 0.03, 0, 0)
	ih, err := m.Predict(context.Background(), high.Market, high.Account)
	if err != nil {
		t.Fatal(err)
	}
	il, err := m.Predict(context.Background(), low.Market, low.Account)
	if err != nil {
		t.Fatal(err)
	}
	if ih.TargetSignedPositionFraction == il.TargetSignedPositionFraction {
		t.Fatalf("expected distinct intents across regimes, got both %v", ih)
	}
	if ih.TargetSignedPositionFraction != 0.5 && ih.TargetSignedPositionFraction != -0.5 {
		t.Fatalf("unexpected high-vol intent %#v", ih)
	}
	if il.TargetSignedPositionFraction != 0.5 && il.TargetSignedPositionFraction != -0.5 {
		t.Fatalf("unexpected low-vol intent %#v", il)
	}
	if ih.TargetSignedPositionFraction == il.TargetSignedPositionFraction {
		t.Fatal("change-point failed to separate regime actions")
	}
}

func TestRareStateSynthetic(t *testing.T) {
	m := New()
	policy, err := m.Fit(context.Background(), rareStateSeries())
	if err != nil {
		t.Fatal(err)
	}
	rareLeaf := false
	for _, leaf := range policy.Leaves {
		if strings.Contains(leaf.Predicate, "rare_state=true") {
			rareLeaf = true
			if !strings.Contains(leaf.Predicate, "reason=") {
				t.Fatalf("rare leaf missing reason: %s", leaf.Predicate)
			}
		}
	}
	if !rareLeaf {
		// Inspect regimes directly — selected grid must surface a rare state on this fixture.
		found := false
		for _, r := range m.Regimes() {
			if r.Rare {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected rare-state regime on imbalanced fixture; regimes=%#v leaves=%d", m.Regimes(), len(policy.Leaves))
		}
		t.Fatal("rare regime present but predicate missing rare_state=true")
	}
	spike := synthExample("probe-rare", 200, 0.20, -0.08, 0, 0)
	intent, err := m.Predict(context.Background(), spike.Market, spike.Account)
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != -1 {
		t.Fatalf("rare-state intent want -1 got %#v", intent)
	}
}

func TestMinSegmentConstraint(t *testing.T) {
	blocks, err := buildDayBlocks(twoRegimeSeries())
	if err != nil {
		t.Fatal(err)
	}
	// 40 days, regime_count=3, min=20 → unsatisfiable (needs 60).
	if _, _, err := segmentBlocks(blocks, 3, 20, 1.0); err == nil {
		t.Fatal("expected min-segment impossibility for regime=3 min=20 on 40 days")
	}
	bounds, _, err := segmentBlocks(blocks, 2, 20, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bounds {
		if b[1]-b[0] < 20 {
			t.Fatalf("segment shorter than min_segment_days: %#v", b)
		}
	}
	// Fit must only select feasible configs; policy leaves reflect chosen regimes.
	m := New()
	policy, err := m.Fit(context.Background(), twoRegimeSeries())
	if err != nil {
		t.Fatal(err)
	}
	if m.Selected().MinSegmentDays != 10 && m.Selected().MinSegmentDays != 20 {
		t.Fatalf("selected min_segment_days out of grid: %d", m.Selected().MinSegmentDays)
	}
	if len(policy.Leaves) > MaxLeaves {
		t.Fatalf("leaves %d > max %d", len(policy.Leaves), MaxLeaves)
	}
}

func TestDeterminismSynthetic(t *testing.T) {
	examples := rareStateSeries()
	m1 := New()
	m2 := New()
	p1, err := m1.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := m2.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if hashPolicy(p1) != hashPolicy(p2) {
		t.Fatalf("policy hash mismatch\n%s\n%s", hashPolicy(p1), hashPolicy(p2))
	}
	if m1.LedgerHead() == "" || m1.LedgerHead() != m2.LedgerHead() {
		t.Fatalf("ledger head mismatch %q vs %q", m1.LedgerHead(), m2.LedgerHead())
	}
	if m1.Selected() != m2.Selected() {
		t.Fatalf("selected hyperparams mismatch %#v vs %#v", m1.Selected(), m2.Selected())
	}
	probe := synthExample("det", 300, 0.04, 0.01, 0, 0)
	i1, err := m1.Predict(context.Background(), probe.Market, probe.Account)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := m2.Predict(context.Background(), probe.Market, probe.Account)
	if err != nil {
		t.Fatal(err)
	}
	if i1 != i2 {
		t.Fatalf("predict mismatch %#v vs %#v", i1, i2)
	}
}

func TestBudgetAndGrid(t *testing.T) {
	grid := enumerateGrid()
	if len(grid) == 0 || len(grid) > MaxAttempts {
		t.Fatalf("grid size %d outside (0,%d]", len(grid), MaxAttempts)
	}
	if len(RegimeCounts)*len(MinSegmentDays)*len(ChangePenalties) != len(grid) {
		t.Fatal("grid cardinality mismatch")
	}
	seen := map[string]struct{}{}
	for _, hp := range grid {
		if hp.RegimeCount < 2 || hp.RegimeCount > 4 {
			t.Fatalf("regime_count out of frozen grid: %d", hp.RegimeCount)
		}
		if hp.MinSegmentDays != 10 && hp.MinSegmentDays != 20 {
			t.Fatalf("min_segment_days out of frozen grid: %d", hp.MinSegmentDays)
		}
		if hp.ChangePenalty != 1.0 && hp.ChangePenalty != 2.0 {
			t.Fatalf("change_penalty out of frozen grid: %v", hp.ChangePenalty)
		}
		if _, dup := seen[hp.AttemptID]; dup {
			t.Fatalf("duplicate attempt id %s", hp.AttemptID)
		}
		seen[hp.AttemptID] = struct{}{}
	}
	m := New()
	policy, err := m.Fit(context.Background(), twoRegimeSeries())
	if err != nil {
		t.Fatal(err)
	}
	entries := m.Ledger()
	fitAttempts := 0
	for _, e := range entries {
		if e.Operation == "FIT_GRID" {
			fitAttempts++
		}
		if e.OwnerID != OwnerID {
			t.Fatalf("unexpected owner %s", e.OwnerID)
		}
	}
	if fitAttempts > MaxAttempts {
		t.Fatalf("fit attempts %d > max %d", fitAttempts, MaxAttempts)
	}
	if len(policy.Leaves) > MaxLeaves {
		t.Fatalf("leaves %d > max %d", len(policy.Leaves), MaxLeaves)
	}
	if m.LabelsRead() {
		t.Fatal("labels_read must stay false")
	}
	if Seed != 42 {
		t.Fatalf("seed want 42 got %d", Seed)
	}
}

func TestMissingAwarePredict(t *testing.T) {
	m := New()
	if _, err := m.Fit(context.Background(), twoRegimeSeries()); err != nil {
		t.Fatal(err)
	}
	// Vol present, other bits missing — must still resolve via missing-aware distance/clauses.
	var features [modelapi.FeatureCount]float64
	features[featOwnRMSVol24h] = 0.05
	market := modelapi.MarketState{
		SchemaVersion: modelapi.SchemaVersion,
		EventID:       "missing-probe",
		DecisionMS:    999 * MSPerDay,
		Features:      features,
		MissingBits:   ^uint64(0) & ((1 << modelapi.FeatureCount) - 1) &^ (1 << featOwnRMSVol24h),
	}
	intent, err := m.Predict(context.Background(), market, modelapi.AccountContext{})
	if err != nil {
		t.Fatal(err)
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectRealLabelKind(t *testing.T) {
	ex := synthExample("x", 1, 0.02, 0, 0, 0)
	ex.Kind = "REAL_LABEL"
	m := New()
	if _, err := m.Fit(context.Background(), []modelapi.TrainingExample{ex}); err == nil {
		t.Fatal("real labels must be rejected")
	}
}

func TestNoAbsoluteDatePredictFeature(t *testing.T) {
	m := New()
	policy, err := m.Fit(context.Background(), twoRegimeSeries())
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range policy.Leaves {
		lower := strings.ToLower(leaf.Predicate)
		for _, banned := range []string{"decision_ms", "calendar_date", "yyyymmdd", "unix_day=", "absolute_date"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("banned token %q in %s", banned, leaf.Predicate)
			}
		}
	}
	// Same relative state at different absolute times must yield the same intent.
	a := synthExample("t0", 10, 0.05, -0.02, 0, 0)
	b := synthExample("t1", 10_000, 0.05, -0.02, 0, 0)
	ia, err := m.Predict(context.Background(), a.Market, a.Account)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := m.Predict(context.Background(), b.Market, b.Account)
	if err != nil {
		t.Fatal(err)
	}
	if ia != ib {
		t.Fatalf("absolute time changed prediction: %#v vs %#v", ia, ib)
	}
}

func TestCodeHashStable(t *testing.T) {
	h1 := CodeHash()
	h2 := CodeHash()
	if h1 == "" || h1 != h2 {
		t.Fatalf("code hash unstable %q %q", h1, h2)
	}
	if h1 == modelapi.APIHash() {
		t.Fatal("owner code hash must not equal bare API hash")
	}
}
