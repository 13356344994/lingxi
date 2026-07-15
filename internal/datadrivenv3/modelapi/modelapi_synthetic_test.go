package modelapi

import "testing"

func TestSyntheticDiscoveryExamplesAndAppendOnlyLedger(t *testing.T) {
	example := TrainingExample{
		SchemaVersion: SchemaVersion,
		Kind:          ExampleSynthetic,
		ExampleID:     "fixture-1",
		Market:        MarketState{SchemaVersion: SchemaVersion, EventID: "market-1", DecisionMS: 1},
		SyntheticTarget: TargetIntent{
			TargetHorizonHours: 1,
		},
	}
	if err := ValidateTrainingExamples([]TrainingExample{example}); err != nil {
		t.Fatal(err)
	}
	example.Kind = "REAL_LABEL"
	if err := ValidateTrainingExamples([]TrainingExample{example}); err == nil {
		t.Fatal("real labels must be rejected")
	}
	var ledger AttemptLedger
	first, err := ledger.Append("first", "terra", "FIT", "synthetic-input")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Append("second", "terra", "PREDICT", "synthetic-input")
	if err != nil {
		t.Fatal(err)
	}
	if first.EntryHash == "" || second.PreviousHash != first.EntryHash || len(ledger.Entries()) != 2 {
		t.Fatalf("append-only hash chain invalid: %#v %#v", first, second)
	}
}
