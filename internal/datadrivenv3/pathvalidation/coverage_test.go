package pathvalidation

import (
	"testing"

	"lingxi/internal/datadrivenv3/modelapi"
)

func TestScanForbiddenTokensDetectsForbiddenFields(t *testing.T) {
	policy := modelapi.Policy{Leaves: []modelapi.CandidateLeaf{
		{LeafID: "clean", Predicate: "btc_return_24h IS PRESENT AND btc_return_24h > 0"},
		{LeafID: "dirty", Predicate: "symbol_id = BTC-USDT-SWAP"},
	}}
	offenders := ScanForbiddenTokens(policy)
	if len(offenders) != 1 || offenders[0] != "dirty" {
		t.Fatalf("expected only the dirty leaf flagged, got %+v", offenders)
	}
}

func TestComputeActivationCoverageClassifiesEveryStatus(t *testing.T) {
	outcomes := []ReplayOutcome{
		{Status: MatchUnique, Intent: modelapi.TargetIntent{TargetSignedPositionFraction: 1, TargetHorizonHours: 1}},
		{Status: MatchUnique, Intent: modelapi.TargetIntent{TargetSignedPositionFraction: 0, TargetHorizonHours: 1}},
		{Status: MatchDefault, Intent: modelapi.TargetIntent{TargetSignedPositionFraction: 0, TargetHorizonHours: 1}},
		{Status: MatchAmbiguousTiebreak},
		{Status: MatchModelUnreplayable},
	}
	c := ComputeActivationCoverage("terra", "validation", outcomes)
	if c.EligibleRows != 5 {
		t.Fatalf("eligible=%d want 5", c.EligibleRows)
	}
	if c.MatchedNonDefaultRows != 2 {
		t.Fatalf("matched_non_default=%d want 2", c.MatchedNonDefaultRows)
	}
	if c.DefaultIntentRows != 1 {
		t.Fatalf("default=%d want 1", c.DefaultIntentRows)
	}
	if c.AmbiguousRows != 1 {
		t.Fatalf("ambiguous=%d want 1", c.AmbiguousRows)
	}
	if c.ModelUnreplayableRows != 1 {
		t.Fatalf("unreplayable=%d want 1", c.ModelUnreplayableRows)
	}
	if c.NoTradeRows != 2 {
		t.Fatalf("no_trade=%d want 2 (one MatchUnique flat + one MatchDefault flat)", c.NoTradeRows)
	}
}
