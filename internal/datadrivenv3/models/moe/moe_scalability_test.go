package moe

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

// syntheticScaleExamples builds n sealed synthetic examples with a clean,
// state-dependent, bidirectional target. The generator is fully deterministic
// (its own fixed seed, unrelated to the model Seed) and never carries a reward
// label, dataset shard, or peer output; it only exercises the fit path at
// production scale.
func syntheticScaleExamples(n int) []modelapi.TrainingExample {
	out := make([]modelapi.TrainingExample, n)
	rng := rand.New(rand.NewSource(0xC0FFEE))
	horizons := []int{1, 4, 24}
	for i := 0; i < n; i++ {
		var f [modelapi.FeatureCount]float64
		for j := 0; j < modelapi.FeatureCount; j++ {
			f[j] = rng.NormFloat64() * 0.1
		}
		// own_return_1h (f[0]) drives the dominant, bidirectional split; a
		// secondary feature keeps the low-magnitude region state-dependent too.
		var frac float64
		switch {
		case f[0] > 0.06:
			frac = -0.5
		case f[0] < -0.06:
			frac = 0.5
		case f[8] > 0:
			frac = 0.1
		default:
			frac = -0.1
		}
		out[i] = mkExample(fmt.Sprintf("syn-%07d", i), f, 0, frac, horizons[i%3], i%4)
	}
	return out
}

func sumLeafExamples(leaves []LeafInfo) int {
	total := 0
	for _, l := range leaves {
		total += l.ExampleCount
	}
	return total
}

// TestScalabilityComplexityBoundLargeSynthetic fits 100k synthetic rows and
// asserts the search only ever fit on the bounded MaxFitRows subsample, which is
// what keeps the split search away from the old O(n^2) blow-up. It also confirms
// the frozen attempt/leaf budgets still hold at scale.
func TestScalabilityComplexityBoundLargeSynthetic(t *testing.T) {
	const n = 100_000
	examples := syntheticScaleExamples(n)

	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatalf("fit at scale failed: %v", err)
	}

	fitRows := sumLeafExamples(m.Leaves())
	if fitRows > MaxFitRows {
		t.Fatalf("fit used %d rows, exceeding the row cap %d", fitRows, MaxFitRows)
	}
	// With n far above the cap, every fit-row slot must be filled: this is the
	// concrete evidence that fit cost is bounded independent of dataset size.
	if fitRows != MaxFitRows {
		t.Fatalf("expected the fit to saturate the row cap %d, used %d", MaxFitRows, fitRows)
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
	if len(m.Leaves()) < 2 {
		t.Fatalf("mixture collapsed at scale to %d expert(s)", len(m.Leaves()))
	}
}

// TestPolicyPredictsArbitraryFullStateAtScale confirms that although fitting is
// bounded to a subsample, the fitted policy still routes and predicts a valid,
// on-grid intent for arbitrary full decision-time states, including ones far
// outside the fit subsample and ones with a missing split feature.
func TestPolicyPredictsArbitraryFullStateAtScale(t *testing.T) {
	examples := syntheticScaleExamples(100_000)
	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatalf("fit failed: %v", err)
	}

	rng := rand.New(rand.NewSource(7))
	for probe := 0; probe < 2000; probe++ {
		var f [modelapi.FeatureCount]float64
		for j := 0; j < modelapi.FeatureCount; j++ {
			f[j] = rng.NormFloat64() * 5.0 // deliberately outside the fit range
		}
		state := modelapi.MarketState{
			SchemaVersion: modelapi.SchemaVersion,
			EventID:       fmt.Sprintf("probe-%d", probe),
			DecisionMS:    1,
			Features:      f,
		}
		if probe%3 == 0 {
			state.Features[0] = 0
			state.MissingBits = uint64(1) << 0 // own_return_1h unavailable
		}
		intent, err := m.Predict(context.Background(), state, modelapi.AccountContext{})
		if err != nil {
			t.Fatalf("predict on arbitrary full state failed: %v", err)
		}
		if err := intent.Validate(); err != nil {
			t.Fatalf("predicted intent left the frozen grid: %v", err)
		}
	}
}

// TestPermutationDeterminismAtScale is the core order-independence guarantee:
// fitting on a 100k input and on a deterministically shuffled copy of that same
// input must yield a bit-identical policy, ledger, choice, and predictions. This
// also proves the fit-row subsample selection itself is order-independent, since
// n greatly exceeds MaxFitRows.
func TestPermutationDeterminismAtScale(t *testing.T) {
	base := syntheticScaleExamples(100_000)

	shuffled := make([]modelapi.TrainingExample, len(base))
	copy(shuffled, base)
	r := rand.New(rand.NewSource(20260714))
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	// A permutation that is guaranteed non-trivial.
	if len(shuffled) > 1 && shuffled[0].ExampleID == base[0].ExampleID {
		shuffled[0], shuffled[1] = shuffled[1], shuffled[0]
	}

	a, b := New(), New()
	pa, err := a.Fit(context.Background(), base)
	if err != nil {
		t.Fatalf("fit on base failed: %v", err)
	}
	pb, err := b.Fit(context.Background(), shuffled)
	if err != nil {
		t.Fatalf("fit on permuted input failed: %v", err)
	}

	ja, _ := json.Marshal(pa)
	jb, _ := json.Marshal(pb)
	if string(ja) != string(jb) {
		t.Fatalf("policy depends on input order:\n%s\n%s", ja, jb)
	}
	if a.HeadHash() != b.HeadHash() {
		t.Fatalf("ledger head depends on input order: %q vs %q", a.HeadHash(), b.HeadHash())
	}
	if a.Choice() != b.Choice() {
		t.Fatalf("choice depends on input order: %+v vs %+v", a.Choice(), b.Choice())
	}

	rng := rand.New(rand.NewSource(11))
	for probe := 0; probe < 1000; probe++ {
		var f [modelapi.FeatureCount]float64
		for j := 0; j < modelapi.FeatureCount; j++ {
			f[j] = rng.NormFloat64()
		}
		state := modelapi.MarketState{
			SchemaVersion: modelapi.SchemaVersion,
			EventID:       fmt.Sprintf("perm-probe-%d", probe),
			DecisionMS:    1,
			Features:      f,
		}
		ia, err := a.Predict(context.Background(), state, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		ib, err := b.Predict(context.Background(), state, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		if ia != ib {
			t.Fatalf("prediction depends on input order at probe %d: %+v vs %+v", probe, ia, ib)
		}
	}
}

// TestSelectFitExamplesIsBoundedAndOrderIndependent unit-tests the subsample
// selector directly: the size is capped, and the selected set (and its canonical
// order) is invariant under input permutation.
func TestSelectFitExamplesIsBoundedAndOrderIndependent(t *testing.T) {
	base := syntheticScaleExamples(50_000)
	shuffled := make([]modelapi.TrainingExample, len(base))
	copy(shuffled, base)
	r := rand.New(rand.NewSource(3))
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	sa := selectFitExamples(base, MaxFitRows)
	sb := selectFitExamples(shuffled, MaxFitRows)
	if len(sa) != MaxFitRows {
		t.Fatalf("expected exactly %d rows, got %d", MaxFitRows, len(sa))
	}
	if len(sa) != len(sb) {
		t.Fatalf("subsample size depends on order: %d vs %d", len(sa), len(sb))
	}
	for i := range sa {
		if sa[i].ExampleID != sb[i].ExampleID {
			t.Fatalf("subsample selection/order depends on input order at %d: %q vs %q", i, sa[i].ExampleID, sb[i].ExampleID)
		}
	}

	// A tiny input is passed through untouched (still canonically ordered).
	small := syntheticScaleExamples(10)
	if got := selectFitExamples(small, MaxFitRows); len(got) != 10 {
		t.Fatalf("under-cap input should keep all rows, got %d", len(got))
	}
}

// TestFitRowSelectionIgnoresTargets proves the production cap cannot depend on
// discovery labels: replacing every target while preserving row identity leaves
// the selected ExampleID set and canonical order unchanged. The changed-target
// input is also permuted to retain the input-order-independence guarantee.
func TestFitRowSelectionIgnoresTargets(t *testing.T) {
	base := syntheticScaleExamples(50_000)
	changed := make([]modelapi.TrainingExample, len(base))
	copy(changed, base)
	for i := range changed {
		changed[i].SyntheticTarget.TargetSignedPositionFraction *= -1
		changed[i].SyntheticTarget.TargetHorizonHours = []int{24, 1, 4}[i%3]
		changed[i].SyntheticTarget.DelayBars = (i + 1) % 4
		if changed[i].SyntheticTarget == base[i].SyntheticTarget {
			t.Fatalf("target was not changed at row %d", i)
		}
	}
	r := rand.New(rand.NewSource(42))
	r.Shuffle(len(changed), func(i, j int) { changed[i], changed[j] = changed[j], changed[i] })

	selectedBase := selectFitExamples(base, MaxFitRows)
	selectedChanged := selectFitExamples(changed, MaxFitRows)
	if len(selectedBase) != len(selectedChanged) {
		t.Fatalf("target changes altered selection size: %d vs %d", len(selectedBase), len(selectedChanged))
	}
	for i := range selectedBase {
		if selectedBase[i].ExampleID != selectedChanged[i].ExampleID {
			t.Fatalf("target changes altered selection/order at %d: %q vs %q",
				i, selectedBase[i].ExampleID, selectedChanged[i].ExampleID)
		}
	}
}

// TestQuantileThresholdsBounded verifies the per-feature threshold cap: however
// many distinct sorted values are supplied, the candidate count never exceeds
// the cap, and small inputs are returned verbatim.
func TestQuantileThresholdsBounded(t *testing.T) {
	dense := make([]float64, 100_000)
	for i := range dense {
		dense[i] = float64(i) * 1e-3
	}
	for _, maxT := range []int{1, 2, 7, MaxThresholdsPerFeature} {
		got := quantileThresholds(dense, maxT)
		if len(got) > maxT {
			t.Fatalf("maxT=%d produced %d thresholds", maxT, len(got))
		}
		for k := 1; k < len(got); k++ {
			if got[k] <= got[k-1] {
				t.Fatalf("quantile thresholds not strictly increasing at %d: %v", k, got)
			}
		}
	}

	few := []float64{0, 1, 2, 3}
	if got := quantileThresholds(few, MaxThresholdsPerFeature); len(got) != 3 {
		t.Fatalf("expected all 3 midpoints for a small input, got %d", len(got))
	}
	if got := quantileThresholds([]float64{5, 5, 5}, MaxThresholdsPerFeature); len(got) != 0 {
		t.Fatalf("a constant feature must offer no split, got %d", len(got))
	}
}

// TestScaleFixtureIsSeed42Deterministic logs the at-scale manifest values and
// asserts repeated fits are bit-identical, i.e. seed-42 determinism holds beyond
// the small contract fixture.
func TestScaleFixtureIsSeed42Deterministic(t *testing.T) {
	examples := syntheticScaleExamples(100_000)
	a, b := New(), New()
	pa, err := a.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if pa.PolicyID != pb.PolicyID || a.HeadHash() != b.HeadHash() {
		t.Fatalf("scale fit not deterministic under seed %d", Seed)
	}
	t.Logf("scale-100k policy_id=%s head=%s choice=%+v leaves=%d",
		pa.PolicyID, a.HeadHash(), a.Choice(), len(a.Leaves()))
}
