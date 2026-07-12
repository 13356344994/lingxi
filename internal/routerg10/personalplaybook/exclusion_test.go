package personalplaybook

import (
	"testing"

	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
)

func TestExclusionAcceptedRequiresAxisHashes(t *testing.T) {
	narrative := ExclusionNarrative{
		InstitutionExcludeReason: "capacity_cliff",
		RetailExcludeReason:      "min_lot_nonlinearity",
		BotExcludeReason:         "event_sparsity",
	}
	inst, retail, bot, err := BindExclusionAxisHashes(narrative)
	if err != nil {
		t.Fatal(err)
	}
	binding := ProofBinding{
		ExclusionProofHash:  "exclusion-hash",
		InstitutionAxisHash: inst,
		RetailAxisHash:      retail,
		BotAxisHash:         bot,
	}
	if err := ValidateExclusionAccepted(binding, narrative); err != nil {
		t.Fatalf("expected accepted: %v", err)
	}
	bad := binding
	bad.BotAxisHash = "tampered"
	if err := ValidateExclusionAccepted(bad, narrative); err == nil {
		t.Fatal("tampered bot axis hash must fail")
	}
}

func TestExclusionAcceptedRejectsNarrativeOnly(t *testing.T) {
	narrative := ExclusionNarrative{
		InstitutionExcludeReason: "capacity_cliff",
		RetailExcludeReason:      "min_lot_nonlinearity",
		BotExcludeReason:         "event_sparsity",
	}
	binding := ProofBinding{ExclusionProofHash: "exclusion-hash"}
	if err := ValidateExclusionAccepted(binding, narrative); err == nil {
		t.Fatal("narrative strings without axis hashes must fail")
	}
}

func TestPublicTriggerEventFamilyNotAlwaysTrue(t *testing.T) {
	spec := PublicTriggerSpec{
		Conditions: []PublicTriggerCondition{
			{Field: "event_family", Op: "eq", Note: "extreme_reversal"},
		},
	}
	featMatch := personalbrainBarFeatures("extreme_reversal")
	featMiss := personalbrainBarFeatures("extreme_continuation")
	if !publicTriggerFiresForTest(spec, featMatch) {
		t.Fatal("expected matching event_family to fire")
	}
	if publicTriggerFiresForTest(spec, featMiss) {
		t.Fatal("event_family must not fire on every bar")
	}
}

func TestPublicTriggerCrossVenueShockCluster(t *testing.T) {
	spec := PublicTriggerSpec{
		Conditions: []PublicTriggerCondition{
			{Field: "cross_venue_shock_cluster", Op: "eq", Note: "shock-a"},
			{Field: "visible_announcement_count", Op: "gte", Value: 1},
		},
	}
	match := personalbrainBarFeatures("")
	match.CrossVenueShockCluster = "shock-a"
	match.VisibleAnnouncementCount = 1
	miss := personalbrainBarFeatures("")
	miss.CrossVenueShockCluster = "shock-b"
	miss.VisibleAnnouncementCount = 1
	if !publicTriggerFiresForTest(spec, match) {
		t.Fatal("expected cross venue shock cluster match")
	}
	if publicTriggerFiresForTest(spec, miss) {
		t.Fatal("wrong shock cluster must not fire")
	}
}

func TestCertifiedPlaybookBindsAxisHashes(t *testing.T) {
	fix, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pb, err := BuildPlaybook(fix.CertifiedTapes[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	if pb.ProofBinding.InstitutionAxisHash == "" || pb.ProofBinding.RetailAxisHash == "" || pb.ProofBinding.BotAxisHash == "" {
		t.Fatal("certified playbook must bind triple axis hashes")
	}
	if err := ValidateExclusionAccepted(pb.ProofBinding, pb.ExclusionNarrative); err != nil {
		t.Fatalf("axis binding invalid: %v", err)
	}
	if pb.PublicTrigger.Conditions[0].Field != "visible_announcement_count" {
		t.Fatalf("announcement trigger = %s", pb.PublicTrigger.Conditions[0].Field)
	}
	_ = g10executionpolicy.ActionBoundedMaker
}

type testBarFeatures struct {
	EventFamily              string
	CrossVenueShockCluster   string
	VisibleAnnouncementCount int
}

func personalbrainBarFeatures(family string) testBarFeatures {
	return testBarFeatures{EventFamily: family}
}

func publicTriggerFiresForTest(spec PublicTriggerSpec, feat testBarFeatures) bool {
	for _, cond := range spec.Conditions {
		switch cond.Field {
		case "event_family":
			if feat.EventFamily != cond.Note {
				return false
			}
		case "cross_venue_shock_cluster":
			if feat.CrossVenueShockCluster != cond.Note {
				return false
			}
		case "visible_announcement_count":
			if float64(feat.VisibleAnnouncementCount) < cond.Value {
				return false
			}
		default:
			return false
		}
	}
	return len(spec.Conditions) > 0
}
