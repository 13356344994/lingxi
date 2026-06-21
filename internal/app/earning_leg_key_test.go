package app

import (
	"reflect"
	"testing"
)

func TestEarningLegConflictSingleAndCrossSpotPerp(t *testing.T) {
	candidates := []UnifiedOpportunity{
		{
			ID:                "svsp:BTCUSDT:binance+binance:snap1",
			StrategyType:      UnifiedStrategySingleVenueSpotPerp,
			Symbol:            "BTCUSDT",
			ExpectedNetEVUSDT: 1.2,
			Legs: []UnifiedOpportunityLeg{
				{Venue: "binance", Market: "spot", Side: "buy"},
				{Venue: "binance", Market: "perp", Side: "short"},
			},
		},
		{
			ID:                "cvsp:BTCUSDT:okx+binance:snap1",
			StrategyType:      UnifiedStrategyCrossVenueSpotPerp,
			Symbol:            "BTCUSDT",
			ExpectedNetEVUSDT: 0.8,
			Legs: []UnifiedOpportunityLeg{
				{Venue: "okx", Market: "spot", Side: "buy"},
				{Venue: "binance", Market: "perp", Side: "short"},
			},
		},
	}

	summary, groups := diagnoseEarningLegConflicts(candidates)
	if summary.RawCandidateCount != 2 {
		t.Fatalf("raw candidates = %d, want 2", summary.RawCandidateCount)
	}
	if summary.UniqueKeyCount != 1 {
		t.Fatalf("unique keys = %d, want 1", summary.UniqueKeyCount)
	}
	if summary.ConflictKeyCount != 1 {
		t.Fatalf("conflict keys = %d, want 1", summary.ConflictKeyCount)
	}
	if summary.DuplicatedCandidateCount != 1 {
		t.Fatalf("duplicated candidates = %d, want 1", summary.DuplicatedCandidateCount)
	}
	if len(groups) != 1 || !groups[0].Conflict || len(groups[0].Candidates) != 2 {
		t.Fatalf("expected one conflict group with 2 candidates, got %+v", groups)
	}
	wantKey := EarningLegKey("BTCUSDT|binance|pos")
	if groups[0].Key != wantKey {
		t.Fatalf("group key = %q, want %q", groups[0].Key, wantKey)
	}
}

func TestEarningLegPosAndNegSameSymbolNoConflict(t *testing.T) {
	candidates := []UnifiedOpportunity{
		{
			ID:           "svsp:ETHUSDT:bybit+bybit:snap1",
			StrategyType: UnifiedStrategySingleVenueSpotPerp,
			Symbol:       "ETHUSDT",
			Legs: []UnifiedOpportunityLeg{
				{Venue: "bybit", Market: "perp", Side: "short"},
			},
		},
		{
			ID:           "svps:ETHUSDT:bybit+bybit:snap1",
			StrategyType: UnifiedStrategySingleVenuePerpSpot,
			Symbol:       "ETHUSDT",
			Legs: []UnifiedOpportunityLeg{
				{Venue: "bybit", Market: "perp", Side: "long"},
			},
		},
	}

	summary, groups := diagnoseEarningLegConflicts(candidates)
	if summary.UniqueKeyCount != 2 {
		t.Fatalf("unique keys = %d, want 2", summary.UniqueKeyCount)
	}
	if summary.ConflictKeyCount != 0 {
		t.Fatalf("conflict keys = %d, want 0", summary.ConflictKeyCount)
	}
	if summary.DuplicatedCandidateCount != 0 {
		t.Fatalf("duplicated candidates = %d, want 0", summary.DuplicatedCandidateCount)
	}
	for _, group := range groups {
		if group.Conflict {
			t.Fatalf("unexpected conflict in group %+v", group)
		}
	}
}

func TestEarningLegPerpPerpDualOccupiedPrimaryByAbsRate(t *testing.T) {
	opp := UnifiedOpportunity{
		ID:              "perp_perp:SOLUSDT:bybit+binance:snap1",
		StrategyType:    UnifiedStrategyPerpPerp,
		Symbol:          "SOLUSDT",
		HighFundingRate: 0.001,
		LowFundingRate:  -0.003,
		Legs: []UnifiedOpportunityLeg{
			{Venue: "bybit", Market: "perp", Side: "short"},
			{Venue: "binance", Market: "perp", Side: "long"},
		},
	}

	info := earningLegKeysForOpportunity(opp)
	if len(info.OccupiedEarningLegKeys) != 2 {
		t.Fatalf("occupied keys = %v, want 2", info.OccupiedEarningLegKeys)
	}
	wantPrimary := EarningLegKey("SOLUSDT|binance|neg")
	if info.PrimaryEarningLegKey != wantPrimary {
		t.Fatalf("primary key = %q, want %q", info.PrimaryEarningLegKey, wantPrimary)
	}
	wantOccupied := []EarningLegKey{
		EarningLegKey("SOLUSDT|binance|neg"),
		EarningLegKey("SOLUSDT|bybit|pos"),
	}
	if !reflect.DeepEqual(info.OccupiedEarningLegKeys, wantOccupied) {
		t.Fatalf("occupied keys = %v, want %v", info.OccupiedEarningLegKeys, wantOccupied)
	}
}

func TestDiagnoseEarningLegConflictsDoesNotMutateCandidates(t *testing.T) {
	candidates := []UnifiedOpportunity{
		{
			ID:           "svsp:BTCUSDT:binance+binance:snap1",
			StrategyType: UnifiedStrategySingleVenueSpotPerp,
			Symbol:       "BTCUSDT",
			Legs: []UnifiedOpportunityLeg{
				{Venue: "binance", Market: "spot", Side: "buy", Notional: 100},
				{Venue: "binance", Market: "perp", Side: "short", Notional: 100},
			},
			CapitalRequiredByVenue: map[string]float64{"binance": 50},
			RejectReasons:          []string{"soft"},
		},
		{
			ID:           "cvsp:BTCUSDT:okx+binance:snap1",
			StrategyType: UnifiedStrategyCrossVenueSpotPerp,
			Symbol:       "BTCUSDT",
			Legs: []UnifiedOpportunityLeg{
				{Venue: "okx", Market: "spot", Side: "buy", Notional: 80},
				{Venue: "binance", Market: "perp", Side: "short", Notional: 80},
			},
		},
	}
	before := append([]UnifiedOpportunity(nil), candidates...)
	beforeJSON := deepCopyCandidatesForTest(before)

	_, _ = diagnoseEarningLegConflicts(candidates)

	if !reflect.DeepEqual(candidates, before) {
		t.Fatalf("diagnose mutated candidate slice")
	}
	if !reflect.DeepEqual(deepCopyCandidatesForTest(candidates), beforeJSON) {
		t.Fatalf("diagnose mutated candidate contents")
	}
}

func deepCopyCandidatesForTest(in []UnifiedOpportunity) []UnifiedOpportunity {
	out := make([]UnifiedOpportunity, len(in))
	for i, opp := range in {
		out[i] = opp
		out[i].Legs = append([]UnifiedOpportunityLeg(nil), opp.Legs...)
		if opp.CapitalRequiredByVenue != nil {
			out[i].CapitalRequiredByVenue = make(map[string]float64, len(opp.CapitalRequiredByVenue))
			for k, v := range opp.CapitalRequiredByVenue {
				out[i].CapitalRequiredByVenue[k] = v
			}
		}
		if opp.RejectReasons != nil {
			out[i].RejectReasons = append([]string(nil), opp.RejectReasons...)
		}
	}
	return out
}
