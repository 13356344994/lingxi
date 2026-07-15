package pathvalidation

import (
	"context"
	"fmt"
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
	"lingxi/internal/datadrivenv3/models/changepoint"
	"lingxi/internal/datadrivenv3/models/episodic"
	"lingxi/internal/datadrivenv3/models/moe"
	"lingxi/internal/datadrivenv3/models/tree"
)

// These tests fit each owner's real model on synthetic (never sealed) data
// and confirm this package's predicate replay adapter reproduces the real
// model's own Predict decision, for every case this validation owner claims
// is safely replayable. They are the fidelity evidence backing every
// MatchUnique classification produced by Replay.

func syntheticExample(id string, features [modelapi.FeatureCount]float64, missing uint64, target modelapi.TargetIntent) modelapi.TrainingExample {
	return modelapi.TrainingExample{
		SchemaVersion: modelapi.SchemaVersion,
		Kind:          modelapi.ExampleSynthetic,
		ExampleID:     id,
		Market: modelapi.MarketState{
			SchemaVersion: modelapi.SchemaVersion,
			EventID:       "evt-" + id,
			DecisionMS:    1,
			Features:      features,
			MissingBits:   missing,
		},
		SyntheticTarget: target,
	}
}

func marketState(id string, features [modelapi.FeatureCount]float64, missing uint64) modelapi.MarketState {
	return modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: id, DecisionMS: 1, Features: features, MissingBits: missing}
}

func TestTerraReplayMatchesRealTreePredictExhaustively(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 0, 200)
	for i := 0; i < 200; i++ {
		var f [modelapi.FeatureCount]float64
		btcReturn := float64(i%10-5) * 0.01
		ownReturn := float64((i/10)%10-5) * 0.01
		liquidity := float64(i%7) / 7
		f[10] = btcReturn // btc_return_24h
		f[2] = ownReturn  // own_return_24h
		f[16] = liquidity // cross_section_liquidity_rank
		missing := uint64(0)
		if i%13 == 0 {
			missing |= 1 << 10
		}
		if i%17 == 0 {
			missing |= 1 << 2
		}
		frac := 0.0
		switch {
		case btcReturn < 0 && liquidity > 0.5:
			frac = 1
		case btcReturn < 0:
			frac = -1
		case ownReturn > 0:
			frac = 0.5
		default:
			frac = -0.5
		}
		examples = append(examples, syntheticExample(fmt.Sprintf("terra-%03d", i), f, missing,
			modelapi.TargetIntent{TargetSignedPositionFraction: frac, TargetHorizonHours: 24, DelayBars: 1}))
	}
	model := tree.New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) < 2 {
		t.Fatalf("expected a multi-leaf tree, got %d leaves", len(policy.Leaves))
	}

	checked := 0
	for i := 0; i < 200; i++ {
		var f [modelapi.FeatureCount]float64
		f[10] = float64(i%11-5) * 0.013
		f[2] = float64((i/11)%11-5) * 0.011
		f[16] = float64(i%9) / 9
		missing := uint64(0)
		if i%5 == 0 {
			missing |= 1 << 10
		}
		if i%7 == 0 {
			missing |= 1 << 2
		}
		market := marketState(fmt.Sprintf("probe-%d", i), f, missing)
		account := modelapi.AccountContext{}

		want, err := model.Predict(context.Background(), market, account)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := Replay("terra", policy, market, account)
		if err != nil {
			t.Fatalf("probe %d: replay error: %v", i, err)
		}
		if outcome.Status != MatchUnique {
			t.Fatalf("probe %d: expected MatchUnique, got %s (matches=%d)", i, outcome.Status, outcome.MatchCount)
		}
		if outcome.Intent != want {
			t.Fatalf("probe %d: replay intent %+v != real Predict intent %+v", i, outcome.Intent, want)
		}
		checked++
	}
	if checked != 200 {
		t.Fatalf("checked %d probes, want 200", checked)
	}
}

func TestOpusGatePredicateAdapterMatchesRealGateRoutingButPolicyIsUnreplayable(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 0, 64)
	for i := 0; i < 64; i++ {
		var f [modelapi.FeatureCount]float64
		r := float64(i-32) * 0.01
		f[0] = r
		f[8] = float64(i%5)*0.01 - 0.02
		frac := 0.5
		if r > 0 {
			frac = -0.5
		}
		examples = append(examples, syntheticExample(fmt.Sprintf("opus-%02d", i), f, 0,
			modelapi.TargetIntent{TargetSignedPositionFraction: frac, TargetHorizonHours: 1, DelayBars: 0}))
	}
	model := moe.New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) < 2 {
		t.Fatalf("expected a multi-expert mixture, got %d leaves", len(policy.Leaves))
	}

	for i := 0; i < 64; i++ {
		var f [modelapi.FeatureCount]float64
		f[0] = float64(i-32) * 0.007
		f[3] = 0.2
		market := marketState(fmt.Sprintf("opus-probe-%d", i), f, 0)
		wantExpert, err := model.RouteExpert(market)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := replayGate(policy, market)
		if err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		if outcome.Status != MatchUnique {
			t.Fatalf("probe %d: expected MatchUnique, got %s", i, outcome.Status)
		}
		if outcome.LeafIndex != wantExpert {
			t.Fatalf("probe %d: replay routed to leaf %d, real gate routed to expert %d", i, outcome.LeafIndex, wantExpert)
		}
		policyOutcome, err := Replay("opus", policy, market, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		if policyOutcome.Status != MatchModelUnreplayable {
			t.Fatalf("probe %d: representative intent must not stand in for Predict, got %s", i, policyOutcome.Status)
		}
	}
}

func TestGrokReplayMatchesRealPredictOnUniqueRegimeMatches(t *testing.T) {
	const msPerDay = int64(24 * 60 * 60 * 1000)
	examples := make([]modelapi.TrainingExample, 0, 60)
	for d := int64(0); d < 30; d++ {
		var f [modelapi.FeatureCount]float64
		f[4] = 0.05 // own_rms_vol_24h
		f[10] = -0.02
		f[6] = 1e6
		examples = append(examples, modelapi.TrainingExample{
			SchemaVersion: modelapi.SchemaVersion, Kind: modelapi.ExampleSynthetic, ExampleID: fmt.Sprintf("a-%02d", d),
			Market:          modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: fmt.Sprintf("evt-a-%02d", d), DecisionMS: d*msPerDay + 3_600_000, Features: f},
			SyntheticTarget: modelapi.TargetIntent{TargetSignedPositionFraction: 0.5, TargetHorizonHours: 1, DelayBars: 0},
		})
	}
	for d := int64(30); d < 60; d++ {
		var f [modelapi.FeatureCount]float64
		f[4] = 0.01
		f[10] = 0.03
		f[6] = 1e6
		examples = append(examples, modelapi.TrainingExample{
			SchemaVersion: modelapi.SchemaVersion, Kind: modelapi.ExampleSynthetic, ExampleID: fmt.Sprintf("b-%02d", d),
			Market:          modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: fmt.Sprintf("evt-b-%02d", d), DecisionMS: d*msPerDay + 3_600_000, Features: f},
			SyntheticTarget: modelapi.TargetIntent{TargetSignedPositionFraction: -0.5, TargetHorizonHours: 1, DelayBars: 0},
		})
	}
	model := changepoint.New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) < 2 {
		t.Fatalf("expected at least 2 regimes, got %d", len(policy.Leaves))
	}

	// Query points centered squarely inside each training regime's own
	// feature values must be unique, replayable matches that agree with the
	// real model's Predict output.
	uniqueChecks := 0
	for _, probe := range []struct {
		vol, btcReturn float64
	}{{0.05, -0.02}, {0.01, 0.03}} {
		var f [modelapi.FeatureCount]float64
		f[4] = probe.vol
		f[10] = probe.btcReturn
		f[6] = 1e6
		market := modelapi.MarketState{SchemaVersion: modelapi.SchemaVersion, EventID: "probe", DecisionMS: 1, Features: f}
		want, err := model.Predict(context.Background(), market, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := Replay("grok", policy, market, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Status == MatchUnique {
			uniqueChecks++
			if outcome.Intent != want {
				t.Fatalf("grok unique-match probe: replay intent %+v != real Predict intent %+v", outcome.Intent, want)
			}
		}
	}
	if uniqueChecks == 0 {
		t.Fatal("expected at least one unique, verifiable regime match in this fixture")
	}
}

func TestSonnetReplayIsAlwaysUnreplayable(t *testing.T) {
	examples := make([]modelapi.TrainingExample, 0, 40)
	for i := 0; i < 40; i++ {
		var f [modelapi.FeatureCount]float64
		f[0] = float64(i) * 0.001
		frac := 0.5
		if i%2 == 0 {
			frac = -0.5
		}
		examples = append(examples, syntheticExample(fmt.Sprintf("sonnet-%02d", i), f, 0,
			modelapi.TargetIntent{TargetSignedPositionFraction: frac, TargetHorizonHours: 1, DelayBars: 0}))
	}
	model := episodic.New()
	policy, err := model.Fit(context.Background(), examples)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Leaves) == 0 {
		t.Fatal("expected at least one aggregated evidence leaf")
	}
	for i := 0; i < 20; i++ {
		var f [modelapi.FeatureCount]float64
		f[0] = float64(i) * 0.0007
		market := marketState(fmt.Sprintf("sonnet-probe-%d", i), f, 0)
		outcome, err := Replay("sonnet", policy, market, modelapi.AccountContext{})
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Status != MatchModelUnreplayable {
			t.Fatalf("probe %d: expected MODEL_UNREPLAYABLE, got %s", i, outcome.Status)
		}
		if outcome.LeafIndex != -1 {
			t.Fatalf("probe %d: unreplayable outcome must never claim a leaf, got %d", i, outcome.LeafIndex)
		}
	}
}
