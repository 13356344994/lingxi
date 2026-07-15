package episodic

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

const msPerDay = int64(24 * 60 * 60 * 1000)

func market(eventID string, decisionMS int64, feature0 float64, missing bool) modelapi.MarketState {
	m := modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: eventID, DecisionMS: decisionMS}
	m.Features[0] = feature0
	if missing {
		m.MissingBits = 1
	}
	return m
}

func account(leverage float64) modelapi.AccountContext {
	return modelapi.AccountContext{GrossLeverage: leverage}
}

func example(id string, m modelapi.MarketState, a modelapi.AccountContext, target modelapi.TargetIntent) modelapi.TrainingExample {
	return modelapi.TrainingExample{SchemaVersion: modelapi.SchemaVersion, Kind: modelapi.ExampleSynthetic, ExampleID: id, Market: m, Account: a, SyntheticTarget: target}
}

func flat() modelapi.TargetIntent { return modelapi.TargetIntent{TargetHorizonHours: 1} }
func long() modelapi.TargetIntent {
	return modelapi.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1}
}
func short() modelapi.TargetIntent {
	return modelapi.TargetIntent{TargetSignedPositionFraction: -1, TargetHorizonHours: 1}
}

// --- self-neighbor isolation -------------------------------------------------

func TestSyntheticSelfNeighborEmbargo(t *testing.T) {
	base := int64(1_700_000_000_000)
	examples := []modelapi.TrainingExample{
		example("outlier", market("self-outlier", base, 0.0, false), account(0), long()),
		example("near-1", market("near-1", base+30*msPerDay, 0.05, false), account(0), flat()),
		example("near-2", market("near-2", base+60*msPerDay, -0.05, false), account(0), flat()),
	}
	for i := 0; i < 7; i++ {
		examples = append(examples, example(fmt.Sprintf("filler-%d", i), market(fmt.Sprintf("filler-%d", i), base+int64(90+i*40)*msPerDay, 5.0, false), account(0), flat()))
	}
	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	// Query is the exact same decision event as "outlier": self-embargo must
	// exclude it, so the surrounding flat consensus must win, not its own
	// conflicting long() target.
	query := market("self-outlier", base, 0.0, false)
	intent, evidence, err := m.PredictWithEvidence(context.Background(), query, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != 0 {
		t.Fatalf("self-neighbor leaked into vote: intent=%#v", intent)
	}
	if evidence.EmbargoedCount < 1 {
		t.Fatalf("expected the exact self row to be embargoed, got evidence=%#v", evidence)
	}
	if evidence.NeighborCount != len(examples)-1 {
		t.Fatalf("neighbor count = %d, want %d (all rows minus the self row)", evidence.NeighborCount, len(examples)-1)
	}
}

// --- 24h self-symbol embargo (non-self, time-adjacent) ----------------------

func TestSyntheticEmbargoWindow(t *testing.T) {
	probeTime := int64(1_800_000_000_000)
	examples := []modelapi.TrainingExample{
		// Different event identity from the query, but only 10h away: must be
		// embargoed even though it is not literally the same event.
		example("adjacent-conflict", market("adjacent-conflict", probeTime+10*60*60*1000, 0.0, false), account(0), short()),
		example("far-truth-1", market("far-truth-1", probeTime+50*msPerDay, 0.02, false), account(0), flat()),
		example("far-truth-2", market("far-truth-2", probeTime-50*msPerDay, -0.02, false), account(0), flat()),
	}
	for i := 0; i < 5; i++ {
		examples = append(examples, example(fmt.Sprintf("filler-%d", i), market(fmt.Sprintf("filler-%d", i), probeTime+int64(200+i*40)*msPerDay, 5.0, false), account(0), flat()))
	}
	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	query := market("probe-embargo-test", probeTime, 0.0, false)
	intent, evidence, err := m.PredictWithEvidence(context.Background(), query, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != 0 {
		t.Fatalf("time-adjacent non-self row leaked into vote: intent=%#v", intent)
	}
	if evidence.EmbargoedCount < 1 {
		t.Fatalf("expected the 10h-adjacent row to be embargoed, got evidence=%#v", evidence)
	}
	if evidence.NeighborCount != len(examples)-1 {
		t.Fatalf("neighbor count = %d, want %d (all rows minus the embargoed row)", evidence.NeighborCount, len(examples)-1)
	}
}

// --- missing-aware standardized distance ------------------------------------

func TestSyntheticMissingAwareDistance(t *testing.T) {
	base := int64(1_900_000_000_000)
	var examples []modelapi.TrainingExample
	for i := 0; i < 4; i++ {
		examples = append(examples,
			example(fmt.Sprintf("present-%d", i), market(fmt.Sprintf("present-%d", i), base+int64(i*40)*msPerDay, 10.0, false), account(0), long()),
			example(fmt.Sprintf("missing-%d", i), market(fmt.Sprintf("missing-%d", i), base+int64(200+i*40)*msPerDay, 0.0, true), account(0), short()),
		)
	}
	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}

	// A present, exact-matching query must beat a same-magnitude-but-missing
	// candidate: missingness is a fixed penalty, never a free/zero-cost match.
	present := market("probe-present", base+900*msPerDay, 10.0, false)
	intent, err := m.Predict(context.Background(), present, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != 1 {
		t.Fatalf("present exact match should beat missing candidates, got %#v", intent)
	}

	// A missing query must not crash and must stay deterministic.
	missingQuery := market("probe-missing", base+901*msPerDay, 0.0, true)
	first, err := m.Predict(context.Background(), missingQuery, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("missing-query prediction invalid: %v", err)
	}
	second, err := m.Predict(context.Background(), missingQuery, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("missing-query prediction not deterministic: %#v vs %#v", first, second)
	}
}

// --- determinism -------------------------------------------------------------

func TestSyntheticDeterminism(t *testing.T) {
	examples := mixedFixtures()
	first, second := New(), New()
	firstPolicy, err := first.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := second.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPolicy, secondPolicy) {
		t.Fatalf("policy mismatch\n%#v\n%#v", firstPolicy, secondPolicy)
	}
	if first.ModelHash() == "" || first.ModelHash() != second.ModelHash() {
		t.Fatalf("model hash mismatch %q vs %q", first.ModelHash(), second.ModelHash())
	}
	if !reflect.DeepEqual(first.AttemptLedger(), second.AttemptLedger()) {
		t.Fatal("attempt ledger mismatch across identical fits")
	}
	if first.SelectedK() != second.SelectedK() || first.SelectedFeatureSet() != second.SelectedFeatureSet() {
		t.Fatalf("selected grid point mismatch: (%d,%s) vs (%d,%s)", first.SelectedK(), first.SelectedFeatureSet(), second.SelectedK(), second.SelectedFeatureSet())
	}
	probe := market("determinism-probe", 1_950_000_000_000, 3.0, false)
	i1, e1, err := first.PredictWithEvidence(context.Background(), probe, account(1))
	if err != nil {
		t.Fatal(err)
	}
	i2, e2, err := second.PredictWithEvidence(context.Background(), probe, account(1))
	if err != nil {
		t.Fatal(err)
	}
	if i1 != i2 {
		t.Fatalf("predict mismatch %#v vs %#v", i1, i2)
	}
	if e1 != e2 {
		t.Fatalf("evidence summary mismatch %#v vs %#v", e1, e2)
	}
}

// --- local-state (account context) driven actions ---------------------------

func TestSyntheticLocalStateActions(t *testing.T) {
	base := int64(2_000_000_000_000)
	var examples []modelapi.TrainingExample
	for i := 0; i < 6; i++ {
		examples = append(examples,
			example(fmt.Sprintf("low-lev-%d", i), market(fmt.Sprintf("low-lev-%d", i), base+int64(i*40)*msPerDay, 0, false), account(0), flat()),
			example(fmt.Sprintf("high-lev-%d", i), market(fmt.Sprintf("high-lev-%d", i), base+int64(300+i*40)*msPerDay, 0, false), account(5), long()),
		)
	}
	m := New()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	// Market features are constant across the whole fixture: only local
	// account state (leverage) distinguishes the two groups' actions.
	lowQuery := market("probe-low-lev", base+700*msPerDay, 0, false)
	lowIntent, err := m.Predict(context.Background(), lowQuery, account(0))
	if err != nil {
		t.Fatal(err)
	}
	if lowIntent.TargetSignedPositionFraction != 0 {
		t.Fatalf("low-leverage local state should yield flat action, got %#v", lowIntent)
	}
	highQuery := market("probe-high-lev", base+701*msPerDay, 0, false)
	highIntent, err := m.Predict(context.Background(), highQuery, account(5))
	if err != nil {
		t.Fatal(err)
	}
	if highIntent.TargetSignedPositionFraction != 1 {
		t.Fatalf("high-leverage local state should yield long action, got %#v", highIntent)
	}
	if lowIntent == highIntent {
		t.Fatal("local account state failed to change the recommended action")
	}
}

// --- grid, budget, and no-training-row-leak guarantees ----------------------

func TestSyntheticGridAndBudget(t *testing.T) {
	if Seed != 42 {
		t.Fatalf("seed want 42 got %d", Seed)
	}
	grid := enumerateGrid()
	if len(grid) == 0 || len(grid) > MaxAttempts {
		t.Fatalf("grid size %d outside (0,%d]", len(grid), MaxAttempts)
	}
	if len(KGrid)*len(FeatureSets) != len(grid) {
		t.Fatal("grid cardinality mismatch")
	}
	m := New()
	policy, err := m.Fit(context.Background(), mixedFixtures())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) > MaxLeaves {
		t.Fatalf("leaves %d > max %d", len(policy.Leaves), MaxLeaves)
	}
	entries := m.AttemptLedger()
	if len(entries) > MaxAttempts {
		t.Fatalf("attempt ledger %d > max %d", len(entries), MaxAttempts)
	}
	for _, e := range entries {
		if e.OwnerID != OwnerID {
			t.Fatalf("unexpected owner %s", e.OwnerID)
		}
	}
	if m.LabelsRead() {
		t.Fatal("labels_read must stay false")
	}
	k := m.SelectedK()
	if k != 16 && k != 32 && k != 64 {
		t.Fatalf("selected k %d not on frozen grid", k)
	}
	fs := m.SelectedFeatureSet()
	if fs != string(FeatureSetRequiredOnly) && fs != string(FeatureSetRequiredPlusOptional) {
		t.Fatalf("selected feature set %q not on frozen grid", fs)
	}
}

func TestSyntheticBudgetExhausted(t *testing.T) {
	m := New()
	examples := mixedFixtures()
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fit(context.Background(), examples); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("third fit error = %v, want budget rejection", err)
	}
}

func TestSyntheticRejectRealLabelKind(t *testing.T) {
	ex := example("x", market("x", 1, 0, false), account(0), flat())
	ex.Kind = "REAL_LABEL"
	m := New()
	if _, err := m.Fit(context.Background(), []modelapi.TrainingExample{ex}); err == nil {
		t.Fatal("real labels must be rejected")
	}
}

func TestSyntheticEvidenceDoesNotLeakTrainingRows(t *testing.T) {
	examples := mixedFixtures()
	m := New()
	policy, err := m.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := make([]string, 0, len(examples)*2)
	for _, ex := range examples {
		forbidden = append(forbidden, ex.ExampleID, ex.Market.EventID)
	}
	for _, leaf := range policy.Leaves {
		for _, token := range forbidden {
			if strings.Contains(leaf.LeafID, token) || strings.Contains(leaf.Predicate, token) || strings.Contains(leaf.RejectionReason, token) {
				t.Fatalf("policy leaf leaked training identity %q: %#v", token, leaf)
			}
		}
	}
	probe := market("leak-probe", 2_100_000_000_000, 1.0, false)
	_, evidence, err := m.PredictWithEvidence(context.Background(), probe, account(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range forbidden {
		if strings.Contains(evidence.Reason, token) {
			t.Fatalf("evidence reason leaked training identity %q: %q", token, evidence.Reason)
		}
	}
}

func mixedFixtures() []modelapi.TrainingExample {
	base := int64(1_600_000_000_000)
	var examples []modelapi.TrainingExample
	for i := 0; i < 20; i++ {
		x := float64(i % 5)
		lev := float64((i / 5) % 2 * 4)
		target := flat()
		if x >= 3 && lev > 2 {
			target = long()
		} else if x < 1 && lev <= 2 {
			target = short()
		}
		examples = append(examples, example(
			fmt.Sprintf("mixed-%02d", i),
			market(fmt.Sprintf("mixed-event-%02d", i), base+int64(i*40)*msPerDay, x, i%7 == 0),
			account(lev),
			target,
		))
	}
	return examples
}
