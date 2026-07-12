package candidates

import (
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerdiscovery/registry"
	"lingxi/internal/routerg3/protocol"
)

const testSkyPattern = "ret=low/te=*/rv=mid/dd=*/fund=high"

func nominationForRule(rule g3reverse.Rule, manifest g3reverse.SearchManifest, rank int) g3reverse.Nomination {
	return g3reverse.Nomination{
		RuleOrdinal: rule.Ordinal, CanonicalRuleHash: rule.CanonicalHash,
		Origin: g3reverse.OriginReverseInduction, Kind: g3reverse.KindCandidateNomination,
		SearchManifestHash: manifest.ManifestHash, SearchSpaceID: protocol.ReverseInductionSearchSpaceID,
		ActualDerivedRuleCount: 1, Rank: rank, MatchingRule: protocol.ReverseInductionMatchRule,
		SkyState: rule.SkyState, SorterDomain: rule.SorterDomain, Horizon: rule.Horizon,
		Direction: string(rule.Direction), ControlCount: 10,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
}

func passVerdict(rule g3reverse.Rule, manifest g3reverse.SearchManifest) g3reverse.RecheckVerdict {
	return g3reverse.RecheckVerdict{
		CanonicalRuleHash: rule.CanonicalHash, Status: g3reverse.RecheckVerdictPass,
		RecheckDataHash: "recheck-data", SearchManifestHash: manifest.ManifestHash,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
}

func sealedManifest(t *testing.T, rule g3reverse.Rule) g3reverse.SearchManifest {
	t.Helper()
	ledger, err := g3reverse.SealLedger(g3reverse.AlphaLedger{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Entries: []g3reverse.LedgerEntry{{
			Ordinal: 1, CanonicalRuleHash: rule.CanonicalHash,
			DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
			AlphaStage:                    "reverse_induction", Status: g3reverse.LedgerStatusRegistered,
			SkyState: rule.SkyState, SorterDomain: rule.SorterDomain,
			Horizon: rule.Horizon, Direction: rule.Direction, RuleKind: "single",
			Conditions: rule.Conditions, SourceWinnerEventIDs: []string{"w1"},
			AvailableFeatureSampleCount: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := g3reverse.NewSearchManifest([]g3reverse.Rule{rule}, ledger, g3reverse.SearchManifestBindings{
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "catalog-test",
		EventManifestHash: "event-test", InductionEventHash: "induction-test", WinnerIndexHash: "winner-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func ruleWithFeatures(sky string, features ...string) g3reverse.Rule {
	conds := make([]g3reverse.RuleCondition, len(features))
	for i, feature := range features {
		conds[i] = g3reverse.RuleCondition{FeatureKey: feature, Op: g3reverse.OpGTE, Threshold: 0.5, Quantile: 0.5}
	}
	rule := g3reverse.Rule{
		Ordinal: 1, SkyState: sky, SorterDomain: "liquid", Horizon: g3reverse.Horizon1h,
		Direction: mechanism.DirectionLong, Conditions: conds,
	}
	hash, _ := g3reverse.CanonicalRuleHash(rule)
	rule.CanonicalHash = hash
	return rule
}

func TestThreeMechanismFamiliesAttribute(t *testing.T) {
	cases := []struct {
		feature   string
		mechanism string
	}{
		{"okx_bybit_funding_spread", "cross_venue_funding_pressure_rebalance"},
		{"rubik_taker_imbalance_1h", "urgent_taker_impact_reversion"},
		{"rubik_lsratio_1h", "crowded_leverage_rebalance"},
	}
	for _, tc := range cases {
		rule := ruleWithFeatures(testSkyPattern, tc.feature)
		manifest := sealedManifest(t, rule)
		attr, err := AttributeRule(rule)
		if err != nil {
			t.Fatal(err)
		}
		if attr.Status != StatusAttributed || attr.Attribution.Mechanism != tc.mechanism {
			t.Fatalf("feature %s: %+v", tc.feature, attr)
		}
		def, err := FromRecheckPassNomination(nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest)
		if err != nil {
			t.Fatalf("feature %s: %v", tc.feature, err)
		}
		if def.Mechanism != tc.mechanism {
			t.Fatalf("feature %s mechanism=%s", tc.feature, def.Mechanism)
		}
	}
}

func TestConditionerOnlyRuleIsNarrativeOnly(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "symbol_return_1h", "symbol_realized_vol_1h")
	attr, err := AttributeRule(rule)
	if err != nil {
		t.Fatal(err)
	}
	if attr.Status != StatusNarrativeOnly {
		t.Fatalf("status=%s", attr.Status)
	}
	manifest := sealedManifest(t, rule)
	if _, err := FromRecheckPassNomination(nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest); err == nil {
		t.Fatal("conditioner-only rule must not register")
	}
}

func TestAmbiguousDualFamilyRuleIsClosed(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "okx_bybit_funding_spread", "rubik_taker_imbalance_1h")
	attr, err := AttributeRule(rule)
	if err != nil {
		t.Fatal(err)
	}
	if attr.Status != StatusAmbiguous {
		t.Fatalf("status=%s", attr.Status)
	}
	manifest := sealedManifest(t, rule)
	if _, err := FromRecheckPassNomination(nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest); err == nil {
		t.Fatal("ambiguous dual-family rule must not register")
	}
}

func TestSemanticClosuresReturnTypedDecisions(t *testing.T) {
	tests := []struct {
		features []string
		status   string
	}{
		{[]string{"symbol_return_1h"}, StatusNarrativeOnly},
		{[]string{"rubik_lsratio_1h", "rubik_taker_imbalance_1h"}, StatusAmbiguous},
	}
	for _, tc := range tests {
		rule := ruleWithFeatures(testSkyPattern, tc.features...)
		manifest := sealedManifest(t, rule)
		_, decision, err := DecideNomination(
			nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest,
		)
		if err != nil {
			t.Fatalf("semantic closure must not be structural error: %v", err)
		}
		if decision.Status != tc.status || decision.Reason == "" ||
			decision.CanonicalRuleHash != rule.CanonicalHash {
			t.Fatalf("decision=%+v", decision)
		}
	}
}

func TestGraveyardMechanismCannotRegister(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "okx_bybit_funding_spread")
	manifest := sealedManifest(t, rule)
	nom := nominationForRule(rule, manifest, 1)
	verdict := passVerdict(rule, manifest)
	def, err := FromRecheckPassNomination(nom, verdict, rule, manifest)
	if err != nil {
		t.Fatal(err)
	}
	def.Mechanism = "funding_payer_flip_rebalance"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("g2 graveyard mechanism revived")
	}
	def, _ = FromRecheckPassNomination(nom, verdict, rule, manifest)
	def.Mechanism = "panic_liquidation"
	if _, err := Register(registry.Registry{}, def, time.Now()); err == nil {
		t.Fatal("panic liquidation revived")
	}
}

func TestRecheckFailCannotRegister(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "rubik_lsratio_1h")
	manifest := sealedManifest(t, rule)
	verdict := passVerdict(rule, manifest)
	verdict.Status = g3reverse.RecheckVerdictClosed
	if _, err := FromRecheckPassNomination(nominationForRule(rule, manifest, 1), verdict, rule, manifest); err == nil {
		t.Fatal("recheck fail must not register")
	}
}

func TestCanonicalIDEncodesSkySorterMechanismRuleHashDirection(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "rubik_lsratio_1h")
	manifest := sealedManifest(t, rule)
	def, err := FromRecheckPassNomination(nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest)
	if err != nil {
		t.Fatal(err)
	}
	id := CanonicalID(def)
	if err := ValidateCanonicalID(id, def); err != nil {
		t.Fatal(err)
	}
	if id != "ret=low_te=*_rv=mid_dd=*_fund=high/liquid/crowded_leverage_rebalance/"+rule.CanonicalHash+"/long" {
		t.Fatalf("id=%s", id)
	}
	candidate, err := ToRegistry(def)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.SupportDomain[0] != "sky_pattern="+testSkyPattern {
		t.Fatalf("support domain lost frozen pattern: %+v", candidate.SupportDomain)
	}
}

func TestConditionerPlusMechanismAttributesSameFamily(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "symbol_return_1h", "rubik_lsratio_1h")
	attr, err := AttributeRule(rule)
	if err != nil {
		t.Fatal(err)
	}
	if attr.Status != StatusAttributed || attr.Attribution.Mechanism != "crowded_leverage_rebalance" {
		t.Fatalf("conditioner+mechanism should attribute to mechanism family: %+v", attr)
	}
}

func TestBackwardCandidateRequiresMatchedControls(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "rubik_lsratio_1h")
	manifest := sealedManifest(t, rule)
	nom := nominationForRule(rule, manifest, 1)
	nom.ControlCount = 0
	if _, err := FromRecheckPassNomination(nom, passVerdict(rule, manifest), rule, manifest); err == nil {
		t.Fatal("missing controls must fail")
	}
}

func TestCandidateRejectsArbitrarySkyWildcards(t *testing.T) {
	rule := ruleWithFeatures(testSkyPattern, "rubik_lsratio_1h")
	manifest := sealedManifest(t, rule)
	def, err := FromRecheckPassNomination(
		nominationForRule(rule, manifest, 1), passVerdict(rule, manifest), rule, manifest,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"ret=*/te=*/rv=mid/dd=*/fund=high",
		"ret=low/te=*/rv=*/dd=*/fund=high",
		"ret=low/te=*/rv=mid/dd=*/fund=*",
		"ret=low/te=mid/rv=mid/dd=*/fund=high",
		"ret=low/te=*/rv=mid/dd=low/fund=high",
	} {
		def.SkyState = invalid
		if _, err := ToRegistry(def); err == nil {
			t.Fatalf("invalid sky wildcard pattern accepted: %s", invalid)
		}
	}
}
