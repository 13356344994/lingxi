package tree

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

func TestSyntheticNonlinearInteraction(t *testing.T) {
	examples := makeInteractionFixtures()
	model := New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) < 3 || len(policy.Leaves) > MaxCandidateLeaf {
		t.Fatalf("expected bounded interaction leaves, got %d", len(policy.Leaves))
	}
	intent, err := model.Predict(context.Background(), market(1, 1, false), account(3))
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != 1 {
		t.Fatalf("interaction positive case = %#v, want long", intent)
	}
	intent, err = model.Predict(context.Background(), market(2, 1, false), account(1))
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetSignedPositionFraction != 0 {
		t.Fatalf("interaction negative case = %#v, want flat", intent)
	}
}

func TestSyntheticMissingFeatureProducesReplayablePredicate(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 0, 40)
	for i := 0; i < 40; i++ {
		missing := i < 20
		target := flat()
		if missing {
			target = short()
		}
		examples = append(examples, example(i, market(int64(i+1), 0, missing), account(0), target))
	}
	model := New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	foundMissing := false
	for _, leaf := range policy.Leaves {
		foundMissing = foundMissing || strings.Contains(leaf.Predicate, "own_return_1h IS MISSING")
	}
	if !foundMissing {
		t.Fatalf("missing-aware predicate not emitted: %#v", policy.Leaves)
	}
	intent, err := model.Predict(context.Background(), market(99, 0, true), account(0))
	if err != nil || intent.TargetSignedPositionFraction != -1 {
		t.Fatalf("missing prediction = %#v, %v", intent, err)
	}
}

func TestSyntheticDeterminism(t *testing.T) {
	examples := makeInteractionFixtures()
	first, second := New(), New()
	firstPolicy, err := first.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := second.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPolicy, secondPolicy) || first.ModelHash() != second.ModelHash() || !reflect.DeepEqual(first.AttemptLedger(), second.AttemptLedger()) {
		t.Fatal("same synthetic input and seed must replay identically")
	}
	if len(first.AttemptLedger()) != len(GridG1) {
		t.Fatalf("attempt ledger = %d, want %d", len(first.AttemptLedger()), len(GridG1))
	}
}

func TestSyntheticNoIdentityMemorization(t *testing.T) {
	model := New()
	if _, err := model.Fit(context.Background(), makeInteractionFixtures()); err != nil {
		t.Fatal(err)
	}
	a := market(1, 1, false)
	b := a
	b.EventID = "a-completely-different-event-id"
	b.DecisionMS = 9_999_999
	first, err := model.Predict(context.Background(), a, account(3))
	if err != nil {
		t.Fatal(err)
	}
	second, err := model.Predict(context.Background(), b, account(3))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("event identity changed prediction: %#v != %#v", first, second)
	}
}

func TestSyntheticBudgetReject(t *testing.T) {
	model := New()
	examples := makeInteractionFixtures()
	if _, err := model.Fit(context.Background(), examples); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Fit(context.Background(), examples); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("second grid search error = %v, want budget rejection", err)
	}
	if len(model.AttemptLedger()) != len(GridG1) {
		t.Fatalf("ledger mutated after rejection: %d", len(model.AttemptLedger()))
	}
}

func TestTrainingCapAndThresholdsAreBoundedAt100k(t *testing.T) {
	const rows = 100_000
	examples := make([]modelapi.TrainingExample, rows)
	for i := range examples {
		examples[i] = modelapi.TrainingExample{
			SchemaVersion:   modelapi.SchemaVersion,
			Kind:            modelapi.ExampleSynthetic,
			ExampleID:       "complexity-" + strconv.Itoa(i),
			Market:          market(int64(i+1), float64(i), false),
			Account:         account(0),
			SyntheticTarget: flat(),
		}
	}

	ranked := rankExamples(examples)
	if got := len(trainingSubset(ranked)); got != MaxTrainingRows {
		t.Fatalf("training rows = %d, want cap %d", got, MaxTrainingRows)
	}
	if got := len(quantileSplitIndices(rows, 10)); got != MaxThresholdsPerFeature {
		t.Fatalf("numeric thresholds = %d, want cap %d", got, MaxThresholdsPerFeature)
	}
	// One missingness candidate plus capped numeric candidates is the maximum
	// evaluated per feature at a node; this fixture does not call Fit.
	if got, want := 1+len(quantileSplitIndices(rows, 10)), 1+MaxThresholdsPerFeature; got != want {
		t.Fatalf("per-feature candidates = %d, want %d", got, want)
	}
}

func TestCappedTrainingIsIndependentOfInputOrder(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 9_000)
	for i := range examples {
		target := flat()
		if i%7 == 0 {
			target = long()
		}
		examples[i] = modelapi.TrainingExample{
			SchemaVersion:   modelapi.SchemaVersion,
			Kind:            modelapi.ExampleSynthetic,
			ExampleID:       "order-" + strconv.Itoa(i),
			Market:          market(int64(i+1), float64(i%40), false),
			Account:         account(float64(i % 3)),
			SyntheticTarget: target,
		}
	}
	reversed := append([]modelapi.TrainingExample(nil), examples...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}

	first, second := New(), New()
	firstPolicy, err := first.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	secondPolicy, err := second.Fit(context.Background(), reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstPolicy, secondPolicy) || first.ModelHash() != second.ModelHash() || !reflect.DeepEqual(first.AttemptLedger(), second.AttemptLedger()) {
		t.Fatal("capped fit changed when input order changed")
	}
}

func TestTrainingSelectionIgnoresTargetsAndPermutation(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 9_000)
	for i := range examples {
		examples[i] = modelapi.TrainingExample{
			SchemaVersion:   modelapi.SchemaVersion,
			Kind:            modelapi.ExampleDiscovery,
			ExampleID:       "selection-" + strconv.Itoa(i),
			Market:          market(int64(i+1), float64(i%19), false),
			Account:         account(float64(i % 5)),
			SyntheticTarget: flat(),
		}
	}
	changedTargets := append([]modelapi.TrainingExample(nil), examples...)
	for i := range changedTargets {
		if i%2 == 0 {
			changedTargets[i].SyntheticTarget = long()
		} else {
			changedTargets[i].SyntheticTarget = short()
		}
	}
	permuted := append([]modelapi.TrainingExample(nil), changedTargets...)
	for left, right := 0, len(permuted)-1; left < right; left, right = left+1, right-1 {
		permuted[left], permuted[right] = permuted[right], permuted[left]
	}

	selectedIDs := func(input []modelapi.TrainingExample) []string {
		selected := trainingSubset(rankExamples(input))
		ids := make([]string, len(selected))
		for i := range selected {
			ids[i] = selected[i].ExampleID
		}
		return ids
	}
	want := selectedIDs(examples)
	if got := selectedIDs(changedTargets); !reflect.DeepEqual(got, want) {
		t.Fatal("changing every discovery target changed selected ExampleID order")
	}
	if got := selectedIDs(permuted); !reflect.DeepEqual(got, want) {
		t.Fatal("input permutation changed selected ExampleID order")
	}
}

func TestSamplingHashCollisionUsesIdentityOrder(t *testing.T) {
	ranked := []rankedExample{
		{rankHash: "collision", example: modelapi.TrainingExample{ExampleID: "b", Market: modelapi.MarketState{EventID: "a", DecisionMS: 1}}},
		{rankHash: "collision", example: modelapi.TrainingExample{ExampleID: "a", Market: modelapi.MarketState{EventID: "b", DecisionMS: 2}}},
		{rankHash: "collision", example: modelapi.TrainingExample{ExampleID: "a", Market: modelapi.MarketState{EventID: "a", DecisionMS: 3}}},
		{rankHash: "collision", example: modelapi.TrainingExample{ExampleID: "a", Market: modelapi.MarketState{EventID: "a", DecisionMS: 2}}},
	}
	sort.Slice(ranked, func(i, j int) bool { return rankedExampleLess(ranked[i], ranked[j]) })
	got := make([]string, len(ranked))
	for i, item := range ranked {
		got[i] = item.example.ExampleID + "/" + item.example.Market.EventID + "/" + strconv.FormatInt(item.example.Market.DecisionMS, 10)
	}
	want := []string{"a/a/2", "a/a/3", "a/b/2", "b/a/1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collision identity order = %v, want %v", got, want)
	}
}

func TestCodeHashMatchesSubmissionBinding(t *testing.T) {
	const submissionHash = "e64967ba048fe4b7811d0626c41c426691b762a0bd5bf41469358a2cb7d61b46"
	if got := CodeHash(); got != submissionHash {
		t.Fatalf("CodeHash = %q, want submission binding %q", got, submissionHash)
	}
}

func makeInteractionFixtures() []modelapi.TrainingExample {
	examples := make([]modelapi.TrainingExample, 0, 80)
	for i := 0; i < 80; i++ {
		x := float64((i / 20) % 2)
		leverage := float64((i / 10) % 2 * 3)
		target := flat()
		if x > 0 && leverage > 2 {
			target = long()
		}
		examples = append(examples, example(i, market(int64(i+1), x, false), account(leverage), target))
	}
	return examples
}

func example(i int, m modelapi.MarketState, a modelapi.AccountContext, target modelapi.TargetIntent) modelapi.TrainingExample {
	return modelapi.TrainingExample{SchemaVersion: modelapi.SchemaVersion, Kind: modelapi.ExampleSynthetic, ExampleID: "synthetic-fixture-" + string(rune('a'+i)), Market: m, Account: a, SyntheticTarget: target}
}
func market(id int64, feature float64, missing bool) modelapi.MarketState {
	m := modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: "fixture-event-" + string(rune('a'+id%26)), DecisionMS: id, Features: [modelapi.FeatureCount]float64{}}
	m.Features[0] = feature
	if missing {
		m.MissingBits = 1
	}
	return m
}
func account(leverage float64) modelapi.AccountContext {
	return modelapi.AccountContext{GrossLeverage: leverage}
}
func flat() modelapi.TargetIntent { return modelapi.TargetIntent{TargetHorizonHours: 1} }
func long() modelapi.TargetIntent {
	return modelapi.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1}
}
func short() modelapi.TargetIntent {
	return modelapi.TargetIntent{TargetSignedPositionFraction: -1, TargetHorizonHours: 1}
}
