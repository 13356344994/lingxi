package pathvalidation

import (
	"math"
	"testing"

	datav3 "lingxi/internal/datadrivenv3"
	"lingxi/internal/datadrivenv3/counterfactual"
	"lingxi/internal/datadrivenv3/modelapi"
)

func flatBars(decision int64, hours int64, high, low, close float64) map[int64]counterfactual.Bar {
	bars := map[int64]counterfactual.Bar{}
	for i := int64(0); i <= hours; i++ {
		ts := decision + i*hourMS
		bars[ts] = counterfactual.Bar{TimeMS: ts, Open: close, High: high, Low: low, Close: close, RollingQuoteVolumeUSDT: 1_000_000}
	}
	return bars
}

func stateRowFixture(partition string, decisionMS int64) datav3.StateRowV2 {
	return datav3.StateRowV2{
		SchemaVersion: datav3.StateSchemaVersionV2,
		EventID:       "fixture-event",
		Instrument:    "BTC-USDT-SWAP",
		DecisionMS:    decisionMS,
		Partition:     partition,
	}
}

func flatFundingFixture() counterfactual.FundingSource {
	return counterfactual.FundingSource{
		Events:       map[string][]counterfactual.FundingEvent{},
		CompleteDays: map[string]bool{"1970-01-02": true, "1970-01-03": true},
	}
}

func TestPrepareRowRefusesDiscoveryAndRouterHoldout(t *testing.T) {
	decision := int64(24 * hourMS)
	bars := flatBars(decision, 30, 100.01, 99.99, 100)
	funding := flatFundingFixture()
	for _, partition := range []string{"discovery", "router_holdout"} {
		candidate := counterfactual.Candidate{Row: stateRowFixture(partition, decision), ContextID: "FLAT|age=0|fraction=0", TierUSDT: 100}
		if _, ok, err := PrepareRow(candidate, bars, funding); err == nil && ok {
			t.Fatalf("expected partition %q to be refused", partition)
		} else if err == nil && !ok {
			t.Fatalf("expected partition %q to fail with an explicit error, not a silent non-eligible result", partition)
		}
	}
}

func TestPrepareRowAcceptsValidationAndLeafWF(t *testing.T) {
	decision := int64(24 * hourMS)
	bars := flatBars(decision, 30, 100.01, 99.99, 100)
	funding := flatFundingFixture()
	for _, partition := range []string{"validation", "leaf_wf"} {
		candidate := counterfactual.Candidate{Row: stateRowFixture(partition, decision), ContextID: "FLAT|age=0|fraction=0", TierUSDT: 100}
		prepared, ok, err := PrepareRow(candidate, bars, funding)
		if err != nil {
			t.Fatalf("partition %q: %v", partition, err)
		}
		if !ok {
			t.Fatalf("partition %q: fixture unexpectedly ineligible", partition)
		}
		if prepared.Account.RetainedCapitalBaseUSDT != 100 {
			t.Fatalf("partition %q: unexpected prepared account: %+v", partition, prepared.Account)
		}
	}
}

func TestCostStressIncreasesExecutionCost(t *testing.T) {
	decision := int64(24 * hourMS)
	volatile := flatBars(decision, 30, 110, 90, 100)
	funding := flatFundingFixture()
	candidate := counterfactual.Candidate{Row: stateRowFixture("validation", decision), ContextID: "FLAT|age=0|fraction=0", TierUSDT: 100}
	prepared, ok, err := PrepareRow(candidate, volatile, funding)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fixture not eligible")
	}
	target := modelapi.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1, DelayBars: 0}
	base, err := EvaluateIntent(prepared, target, 1)
	if err != nil {
		t.Fatal(err)
	}
	stressed, err := EvaluateIntent(prepared, target, CostStressMultiplier)
	if err != nil {
		t.Fatal(err)
	}
	if base.Ruin || stressed.Ruin {
		t.Fatal("fixture unexpectedly ruined")
	}
	if !(stressed.NetReward < base.NetReward) {
		t.Fatalf("expected 1.5x cost stress to strictly reduce net reward: base=%.6f stressed=%.6f", base.NetReward, stressed.NetReward)
	}
}

func TestDelayVariantsChangeExecutionOrigin(t *testing.T) {
	decision := int64(24 * hourMS)
	bars := flatBars(decision, 30, 100.01, 99.99, 100)
	// A materially different bar at delay=2's execution timestamp makes the
	// realized entry price (and therefore reward) diverge from delay=0.
	bars[decision+2*hourMS] = counterfactual.Bar{TimeMS: decision + 2*hourMS, Open: 90, High: 90.01, Low: 89.99, Close: 90, RollingQuoteVolumeUSDT: 1_000_000}
	funding := flatFundingFixture()
	candidate := counterfactual.Candidate{Row: stateRowFixture("leaf_wf", decision), ContextID: "FLAT|age=0|fraction=0", TierUSDT: 100}
	prepared, ok, err := PrepareRow(candidate, bars, funding)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fixture not eligible")
	}
	var rewards [4]float64
	for d := 0; d < 4; d++ {
		out, err := EvaluateIntent(prepared, modelapi.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1, DelayBars: d}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if out.Ruin {
			t.Fatalf("delay %d unexpectedly ruined", d)
		}
		rewards[d] = out.NetReward
	}
	if math.Abs(rewards[2]-rewards[0]) < 1e-9 {
		t.Fatalf("expected delay to change realized entry price/cost: delay0=%.9f delay2=%.9f", rewards[0], rewards[2])
	}
}

func TestEvaluateIntentRejectsOffGridIntent(t *testing.T) {
	decision := int64(24 * hourMS)
	bars := flatBars(decision, 30, 100.01, 99.99, 100)
	funding := flatFundingFixture()
	candidate := counterfactual.Candidate{Row: stateRowFixture("validation", decision), ContextID: "FLAT|age=0|fraction=0", TierUSDT: 100}
	prepared, ok, err := PrepareRow(candidate, bars, funding)
	if err != nil || !ok {
		t.Fatalf("fixture setup failed: ok=%v err=%v", ok, err)
	}
	if _, err := EvaluateIntent(prepared, modelapi.TargetIntent{TargetSignedPositionFraction: 0.33, TargetHorizonHours: 1, DelayBars: 0}, 1); err == nil {
		t.Fatal("expected an off-grid target fraction to be rejected")
	}
}
