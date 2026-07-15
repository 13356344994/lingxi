package neutralrunner

import (
	"context"
	"errors"
	"testing"

	"lingxi/internal/datadrivenv3/accountstate"
	"lingxi/internal/datadrivenv3/modelapi"
)

type flatModel struct{}

func (flatModel) Fit(context.Context, []modelapi.TrainingExample) (modelapi.Policy, error) {
	return modelapi.Policy{}, errors.New("not used by runner fixture")
}
func (flatModel) Predict(context.Context, modelapi.MarketState, modelapi.AccountContext) (modelapi.TargetIntent, error) {
	return modelapi.TargetIntent{TargetHorizonHours: 1}, nil
}

func fixtureMarkets() []modelapi.MarketState {
	return []modelapi.MarketState{
		{SchemaVersion: modelapi.SchemaVersion, EventID: "synthetic-1", DecisionMS: 1},
		{SchemaVersion: modelapi.SchemaVersion, EventID: "synthetic-2", DecisionMS: 2},
	}
}

func validBinding(sharedHash string) OwnerBinding {
	protocols := make(map[string]string, len(EffectiveProtocolHashes))
	for generation, hash := range EffectiveProtocolHashes {
		protocols[generation] = hash
	}
	return OwnerBinding{
		OwnerID: "terra", OwnerCodeHash: "synthetic-owner-code", GridHash: modelapi.GridHash(), SharedInputHash: sharedHash,
		ProtocolHashes: protocols, DatasetManifest: DatasetManifestHash, DatasetReviewer: DatasetReviewerHash, AccountFixture: AccountFixtureHash,
	}
}

func TestSealedProviderAndSharedRunnerSyntheticFixture(t *testing.T) {
	if _, err := (SealedLabelProvider{}).Reward(context.Background(), "anything"); !errors.Is(err, ErrLabelsSealed) {
		t.Fatalf("real labels must remain sealed, got %v", err)
	}
	markets := fixtureMarkets()
	sharedHash := SharedInputHash(markets)
	input := Input{
		AccountConfig: accountstate.Config{InitialCashUSDT: 100, RetainedCapitalBaseUSDT: 100, NotionalPerFractionUSDT: 100},
		Markets:       markets,
		Ticks: []accountstate.Tick{
			{DecisionMS: 1, MarkPrice: 100},
			{DecisionMS: 2, MarkPrice: 100},
		},
		SharedHash: sharedHash,
	}
	var ledger modelapi.AttemptLedger
	result, err := Run(context.Background(), validBinding(sharedHash), flatModel{}, input, &ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Traces) != 2 || len(result.AttemptLedger) != 1 || result.AttemptLedger[0].PreviousHash != "" {
		t.Fatalf("unexpected synthetic result: %#v", result)
	}
}

func TestBindingMismatchAndAtomicBarrier(t *testing.T) {
	markets := fixtureMarkets()
	sharedHash := SharedInputHash(markets)
	binding := validBinding(sharedHash)
	binding.GridHash = "drift"
	if err := binding.Validate(sharedHash); err == nil {
		t.Fatal("grid drift must fail closed")
	}
	barrier, err := NewAtomicOutputBarrier([]string{"terra", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if output, ready, err := barrier.Submit(Result{OwnerID: "terra"}); err != nil || ready || output != nil {
		t.Fatalf("partial output escaped barrier: output=%v ready=%v err=%v", output, ready, err)
	}
	output, ready, err := barrier.Submit(Result{OwnerID: "opus"})
	if err != nil || !ready || len(output) != 2 {
		t.Fatalf("barrier did not atomically release all outputs: %v %v %v", output, ready, err)
	}
}
