package candidates

import (
	"testing"
	"time"

	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg2/protocol"
)

func definition(sky, direction string) Definition {
	return Definition{
		SkyState: sky, SorterDomain: "liquid", Mechanism: protocol.G2PrizeMechanism,
		Variant: "release", Direction: direction, Payer: "funding_receivers",
		ForcedBehavior: "payer side changes at settlement", CannotWait: true,
		CompetitionDefense: "conditional sky and capacity", DeadlineRule: "next_settlement_or_horizon",
		Invalidators: []string{"deadline"}, RequiredData: []string{"okx_1h", "okx_funding"},
		CostModel: "full_cost_v2", Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DiscoveryDataHash: "data", Origin: registry.OriginBackward,
		SearchSpaceSize: 10, Rank: 1, MatchingRule: "same_sky_sorter_symbol", ControlCount: 20,
	}
}

func TestPrizeNominationStillNeedsForwardReview(t *testing.T) {
	nomination := g2prize.Nomination{
		Key: "sky_leaf", Kind: g2prize.KindCandidateNomination,
		SurvivesSelectionTax: true, SkyState: "ret=low", SorterDomain: "liquid",
		Payer: "funding_payers", Mechanism: protocol.G2PrizeMechanism, Direction: "short",
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash, DiscoveryDataHash: "data",
		SearchSpaceSize: 162, Rank: 1, MatchingRule: "matched", ControlCount: 20,
	}
	if _, err := FromPrizeNomination(nomination, PrizeRegistration{}); err == nil {
		t.Fatal("prize nomination without forward invalidators/data must fail")
	}
	def, err := FromPrizeNomination(nomination, PrizeRegistration{
		RequiredData: []string{"okx_1h", "okx_funding"},
		Invalidators: []string{"deadline"}, CostModel: "full_cost_g2",
		ForcedBehavior: "payer side flips at settlement", CompetitionDefense: "sky-conditioned capacity",
		DeadlineRule: "next_settlement_or_horizon",
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Register(registry.Registry{}, def, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Candidates[0].State != registry.StateNominated {
		t.Fatal("prize nomination skipped forward falsification")
	}
}

func TestSameMechanismMayRouteOppositeDirectionsInDifferentSkyStates(t *testing.T) {
	r, err := Register(registry.Registry{}, definition("storm_down", "short"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err = Register(r, definition("calm_up", "long"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 2 || r.Candidates[0].ID == r.Candidates[1].ID {
		t.Fatalf("conditional leaves collapsed: %+v", r.Candidates)
	}
}

func TestG1RulesCannotReviveInsideG2Path(t *testing.T) {
	def := definition("storm_down", "short")
	def.Variant = "negative_funding_receiver_release_short"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("g1 failed funding rule revived in g2")
	}
	def = definition("storm_down", "short")
	def.Mechanism = "panic_liquidation"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("g1 UNTESTABLE panic-liquidation mechanism revived in g2")
	}
	def = definition("storm_down", "short")
	def.Mechanism = "funding_post_settlement_release"
	def.Variant = "totally_new_name"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("renamed g1 funding economic mechanism revived in g2")
	}
}

func TestBackwardCandidateMustUseFrozenG2Mechanism(t *testing.T) {
	def := definition("ret=low", "long")
	def.Mechanism = "new_name_without_novelty_contract"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("backward candidate escaped frozen G2 mechanism contract")
	}
}

func TestCanonicalIDEncodesFullConditionalPath(t *testing.T) {
	def := definition("storm_down", "short")
	id := CanonicalID(def)
	if err := ValidateCanonicalID(id, def); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalID("short", def); err == nil {
		t.Fatal("direction-only id must be rejected")
	}
}
