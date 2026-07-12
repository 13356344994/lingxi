package forward

import (
	"errors"
	"testing"

	"lingxi/internal/mechanism"
	"lingxi/internal/researchboundary"
)

func TestValidateRejectsNarrativeOnlyFixture(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	candidate := CandidateDefinition{
		ID:        "story_only",
		Narrative: "market looks emotional; maybe it should reverse soon",
	}
	if err := candidate.Validate(policy); !errors.Is(err, ErrNoPayer) {
		t.Fatalf("want ErrNoPayer, got %v", err)
	}
}

func TestValidateRejectsNonDecisionTimeField(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	candidate := validCandidateFixture()
	candidate.DecisionTimeFields = []string{"event_time_unix_ms", "future_return_5m"}
	if err := candidate.Validate(policy); !errors.Is(err, ErrNonDecisionTimeField) {
		t.Fatalf("want ErrNonDecisionTimeField, got %v", err)
	}
}

func TestValidateRejectsUnknownHistoryFamily(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	candidate := validCandidateFixture()
	candidate.HistoryFamilies = []string{"okx_liquidation", "non_existing_family"}
	if err := candidate.Validate(policy); !errors.Is(err, ErrUnknownHistoryFamily) {
		t.Fatalf("want ErrUnknownHistoryFamily, got %v", err)
	}
}

func TestValidateRejectsFrozenUntestableLeaf(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	candidate := validCandidateFixture()
	candidate.ID = "panic_liquidation.relay.long_squeezed.short"
	if err := candidate.Validate(policy); !errors.Is(err, ErrFrozenUntestableLeaf) {
		t.Fatalf("want ErrFrozenUntestableLeaf, got %v", err)
	}
	candidate = validCandidateFixture()
	candidate.ID = "renamed-new-leaf"
	candidate.Mechanism = "panic_liquidation"
	if err := candidate.Validate(policy); !errors.Is(err, ErrFrozenUntestableLeaf) {
		t.Fatalf("renamed panic liquidation mechanism must remain frozen, got %v", err)
	}
}

func TestValidateRejectsUntestableRevival(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	candidate := validCandidateFixture()
	candidate.HistoricalTestability = researchboundary.Untestable
	if err := candidate.Validate(policy); !errors.Is(err, ErrUntestableRevival) {
		t.Fatalf("want ErrUntestableRevival, got %v", err)
	}
}

func TestToDTONormalizesArrays(t *testing.T) {
	candidate := validCandidateFixture()
	candidate.DecisionTimeFields = []string{"spread_bps", "event_time_unix_ms", "spread_bps"}
	candidate.HistoryFamilies = []string{"market_mids", "okx_liquidation", "market_mids"}
	candidate.SupportDomain = []string{"panic_liquidation", "panic_liquidation", "high_vol"}

	dto := candidate.ToDTO()
	if len(dto.DecisionTimeFields) != 2 || dto.DecisionTimeFields[0] != "event_time_unix_ms" || dto.DecisionTimeFields[1] != "spread_bps" {
		t.Fatalf("unexpected decision-time fields: %+v", dto.DecisionTimeFields)
	}
	if len(dto.HistoryFamilies) != 2 || dto.HistoryFamilies[0] != "market_mids" || dto.HistoryFamilies[1] != "okx_liquidation" {
		t.Fatalf("unexpected history families: %+v", dto.HistoryFamilies)
	}
	if len(dto.SupportDomain) != 2 || dto.SupportDomain[0] != "high_vol" || dto.SupportDomain[1] != "panic_liquidation" {
		t.Fatalf("unexpected support domain: %+v", dto.SupportDomain)
	}
}

func TestValidateAcceptsStrictCandidate(t *testing.T) {
	policy := NewValidationPolicy(
		[]string{"event_time_unix_ms", "spread_bps", "funding_rate"},
		[]string{"okx_liquidation", "market_mids", "okx_funding", "okx_1h"},
	)
	if err := validCandidateFixture().Validate(policy); err != nil {
		t.Fatalf("candidate should pass strict validation: %v", err)
	}
}

func validCandidateFixture() CandidateDefinition {
	return CandidateDefinition{
		ID:                 "funding_settlement.collection.long",
		Payer:              mechanism.PayerFundingPayers,
		Mechanism:          "funding_settlement_collection",
		ForcedBehavior:     "funding payers must settle or close before the public clock",
		CannotWait:         true,
		CompetitionDefense: "queue-priority execution and adverse-selection guard",
		DeadlineUnixMs:     1730000000000,
		ObservableInvalidators: []Invalidator{
			{Metric: "remaining_forced_notional", Condition: "<= 0", Observable: true},
		},
		DecisionTimeFields:    []string{"event_time_unix_ms", "funding_rate"},
		HistoryFamilies:       []string{"okx_funding", "okx_1h"},
		Direction:             mechanism.DirectionLong,
		SupportDomain:         []string{"funding_settlement", "high_vol"},
		HistoricalTestability: researchboundary.Testable,
		Narrative:             "forced-flow continuation with explicit runtime falsifier",
	}
}
