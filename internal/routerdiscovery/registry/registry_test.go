package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/routerdiscovery/backward"
	"lingxi/internal/routerdiscovery/forward"
)

func candidate(id string, origin Origin) Candidate {
	return Candidate{
		ID: id, Origin: origin, Generation: "router-g1", ProtocolHash: "protocol",
		DiscoveryDataHash: "data", SearchSpaceSize: 10, Rank: 1,
		Payer: "forced_actor", Mechanism: "settlement_constraint", Direction: "long",
		ForcedBehavior: "forced settlement", CannotWait: true,
		CompetitionDefense: "bounded capacity", DeadlineRule: "deadline",
		SupportDomain: []string{"volatility=high"}, Invalidators: []string{"deadline"},
		CostModel: "full_cost_v1", RequiredData: []string{"okx_1h"},
		MatchingRule: "same_time_symbol_volatility", ControlCount: 5,
	}
}

func TestBackwardNominationStillRequiresForwardReview(t *testing.T) {
	nomination := backward.Nomination{
		Key: "funding_negative", Kind: backward.KindCandidateNomination,
		Origin: backward.OriginBackwardMined, SurvivesSelectionTax: true,
		Generation: "router-g1", ProtocolHash: "protocol", DiscoveryDataHash: "data",
		SearchSpaceSize: 2, Rank: 1, MatchingRule: "matched", ControlCount: 20,
		Payer: "funding_receivers", Mechanism: "funding_post_settlement_release", Direction: "short",
	}
	if _, err := FromBackwardNomination(nomination, BackwardRegistration{ID: "candidate"}); err == nil {
		t.Fatal("backward nomination without forward invalidators/data/support must be rejected")
	}
	candidate, err := FromBackwardNomination(nomination, BackwardRegistration{
		ID: "funding.release.short", CostModel: "full_cost_v1",
		RequiredData:  []string{"okx_funding", "okx_1h"},
		SupportDomain: []string{"funding_negative"}, Invalidators: []string{"settlement_effect_expired"},
		ForcedBehavior: "settlement rebalance", CompetitionDefense: "conditional capacity",
		DeadlineRule: "next_settlement",
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Register(Registry{}, candidate, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Candidates[0].State != StateNominated {
		t.Fatal("backward nomination must not skip forward falsification")
	}
}

func TestForwardDTORegistersThroughCommonRegistry(t *testing.T) {
	dto := forward.CandidateDTO{
		ID: "funding.collection.long", Payer: "funding_payers",
		Mechanism: "funding_settlement", Direction: "long",
		ForcedBehavior: "funding settlement", CannotWait: true,
		CompetitionDefense: "capacity", DeadlineUnixMs: 123,
		ObservableInvalidators: []string{"deadline"}, HistoryFamilies: []string{"okx_funding"},
		SupportDomain: []string{"funding_extreme"},
	}
	c := FromForward(dto, ForwardRegistration{
		Generation: "router-g1", ProtocolHash: "protocol", DiscoveryDataHash: "data",
		SearchSpaceSize: 1, Rank: 1, CostModel: "full_cost_v1",
	})
	if _, err := Register(Registry{}, c, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryStoreRejectsTampering(t *testing.T) {
	r, err := Register(Registry{}, candidate("c1", OriginForward), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := Save(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for i := range raw {
		if raw[i] == 'c' {
			raw[i] = 'x'
			break
		}
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("tampered registry must fail hash verification")
	}
}

func TestBackwardCandidateCannotSkipForwardFalsification(t *testing.T) {
	now := time.Unix(1, 0)
	r, err := Register(Registry{}, candidate("c1", OriginBackward), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordForwardFalsification(r, "c1", true, "evidence", "PASS", now); err == nil {
		t.Fatal("nominated candidate must freeze before forward falsification")
	}
	r, err = Freeze(r, "c1", now)
	if err != nil {
		t.Fatal(err)
	}
	r, err = RecordForwardFalsification(r, "c1", true, "evidence", "PASS", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Candidates[0].State != StateResearchPass {
		t.Fatalf("state=%s", r.Candidates[0].State)
	}
	leaf, err := ToRouterLeaf(r.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.State != "RESEARCH_PASS" || leaf.Variant != "c1" {
		t.Fatalf("unexpected router leaf: %+v", leaf)
	}
}

func TestSemanticDuplicateCannotReenterUnderNewNameOrGeneration(t *testing.T) {
	r, err := Register(Registry{}, candidate("c1", OriginBackward), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := candidate("renamed", OriginBackward)
	duplicate.Generation = "router-g2"
	if _, err := Register(r, duplicate, time.Now()); err == nil {
		t.Fatal("semantic duplicate must not reenter under new id/generation")
	}
}

func TestClosedCandidateCannotReopen(t *testing.T) {
	r, _ := Register(Registry{}, candidate("c1", OriginForward), time.Now())
	r, _ = Freeze(r, "c1", time.Now())
	r, err := RecordForwardFalsification(r, "c1", false, "evidence", "ROBUSTNESS_GATE", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Freeze(r, "c1", time.Now()); err == nil {
		t.Fatal("closed candidate must be permanent")
	}
}

func TestUntestableForcedLiquidationLeavesCannotRevive(t *testing.T) {
	blocked := candidate("panic_liquidation.relay.long_squeezed.short", OriginForward)
	if _, err := Register(Registry{}, blocked, time.Now()); err == nil {
		t.Fatal("UNTESTABLE forced-liquidation leaf must remain closed")
	}
	renamed := candidate("totally-new-name", OriginForward)
	renamed.Mechanism = "panic_liquidation"
	if _, err := Register(Registry{}, renamed, time.Now()); err == nil {
		t.Fatal("renaming a frozen panic-liquidation mechanism must not revive it")
	}
}
