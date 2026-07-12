package g3reverse

import (
	"fmt"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

func recheckPassRule() Rule {
	pattern, _ := coarseSkyPattern(normalizeTestSky("sky_pass"))
	rule := Rule{
		Ordinal: 1, SkyState: pattern, SorterDomain: "liquid", Horizon: Horizon1h,
		Direction:  mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.5, Quantile: 0.5}},
	}
	hash, _ := CanonicalRuleHash(rule)
	rule.CanonicalHash = hash
	return rule
}

func baseRecheckOptions(events []Event, nominations []Nomination, rules []Rule, manifest SearchManifest) RecheckOptions {
	return RecheckOptions{
		RecheckEvents: events, Nominations: nominations, Rules: rules, Manifest: manifest,
		RecheckAlpha: protocol.RecheckAlpha, MinSamples: protocol.G3MinRecheckSamples,
		MinClusters: protocol.G3MinRecheckClusters,
		Generation:  protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
}

func recheckEventsForRule(rule Rule, positive bool, clusters int, samplesPerCluster int) []Event {
	if samplesPerCluster <= 0 {
		samplesPerCluster = 3
	}
	var events []Event
	for i := 0; i < clusters; i++ {
		cluster := fmt.Sprintf("rc%02d", i)
		for j := 0; j < samplesPerCluster; j++ {
			net := 20.0
			if !positive {
				net = -15.0
			}
			offset := time.Duration(i*samplesPerCluster+j) * 24 * time.Hour
			events = append(events, Event{
				ID: fmt.Sprintf("r-%s-%d", cluster, j), Symbol: "BTCUSDT", Cluster: cluster,
				SkyState: fullTestSkyFromPattern(rule.SkyState), SorterDomain: rule.SorterDomain, LiquidityBucket: "liq_6",
				Horizon: rule.Horizon, DecisionTime: baseEvalTime().Add(offset),
				FeatureAsOf: baseEvalTime().Add(offset - time.Minute),
				EntryTime:   baseEvalTime().Add(offset),
				ExitTime:    baseEvalTime().Add(offset + time.Hour),
				EntryPrice:  100, ExitPrice: 100, Direction: rule.Direction,
				Features:      map[string]float64{"symbol_return_1h": 1.0},
				NetOutcomeBPS: net, OutcomeClass: classifyOutcome(net, protocol.NeutralBandBPS),
			})
		}
	}
	return events
}

func recheckEventsMixedClusters(rule Rule, clusterNets map[string]float64, samplesPerCluster int) []Event {
	var events []Event
	idx := 0
	for cluster, net := range clusterNets {
		for j := 0; j < samplesPerCluster; j++ {
			offset := time.Duration(idx) * 24 * time.Hour
			idx++
			events = append(events, Event{
				ID: fmt.Sprintf("r-%s-%d", cluster, j), Symbol: "BTCUSDT", Cluster: cluster,
				SkyState: fullTestSkyFromPattern(rule.SkyState), SorterDomain: rule.SorterDomain, LiquidityBucket: "liq_6",
				Horizon: rule.Horizon, DecisionTime: baseEvalTime().Add(offset),
				FeatureAsOf: baseEvalTime().Add(offset - time.Minute),
				EntryTime:   baseEvalTime().Add(offset), ExitTime: baseEvalTime().Add(offset + time.Hour),
				EntryPrice: 100, ExitPrice: 100, Direction: rule.Direction,
				Features:      map[string]float64{"symbol_return_1h": 1.0},
				NetOutcomeBPS: net, OutcomeClass: classifyOutcome(net, protocol.NeutralBandBPS),
			})
		}
	}
	return events
}

func TestRecheckHolmRejectsWeakerNomination(t *testing.T) {
	passRule := recheckPassRule()
	weakPattern, _ := coarseSkyPattern(normalizeTestSky("sky_weak"))
	weakRule := Rule{
		Ordinal: 3, SkyState: weakPattern, SorterDomain: "liquid", Horizon: Horizon1h,
		Direction:  mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.5, Quantile: 0.5}},
	}
	hash, _ := CanonicalRuleHash(weakRule)
	weakRule.CanonicalHash = hash
	rules := []Rule{passRule, weakRule}
	_, manifest, _ := sealTestManifestAndLedger(t, rules)

	strongClusters := map[string]float64{}
	for i := 0; i < 10; i++ {
		strongClusters[fmt.Sprintf("strong%02d", i)] = 20
	}
	weakClusters := map[string]float64{}
	for i := 0; i < protocol.G3MinRecheckClusters-1; i++ {
		weakClusters[fmt.Sprintf("weakp%02d", i)] = 20
	}
	weakClusters["weak-negative"] = -5

	var events []Event
	events = append(events, recheckEventsMixedClusters(passRule, strongClusters, 3)...)
	events = append(events, recheckEventsMixedClusters(weakRule, weakClusters, 3)...)

	nominations := []Nomination{
		{CanonicalRuleHash: passRule.CanonicalHash, RuleOrdinal: 1, Kind: KindCandidateNomination},
		{CanonicalRuleHash: weakRule.CanonicalHash, RuleOrdinal: 3, Kind: KindCandidateNomination},
	}
	report, err := Recheck(baseRecheckOptions(events, nominations, rules, manifest))
	if err != nil {
		t.Fatal(err)
	}
	byHash := map[string]RecheckVerdict{}
	for _, verdict := range report.Verdicts {
		byHash[verdict.CanonicalRuleHash] = verdict
	}
	if byHash[passRule.CanonicalHash].Status != RecheckVerdictPass {
		t.Fatalf("expected pass: %+v", byHash[passRule.CanonicalHash])
	}
	if byHash[weakRule.CanonicalHash].Status != RecheckVerdictClosed {
		t.Fatalf("expected closed: %+v", byHash[weakRule.CanonicalHash])
	}
}

func TestInductionPassRecheckFailCloses(t *testing.T) {
	rule := recheckPassRule()
	_, manifest, _ := sealTestManifestAndLedger(t, []Rule{rule})
	events := recheckEventsForRule(rule, false, protocol.G3MinRecheckClusters, 3)
	report, err := Recheck(baseRecheckOptions(events, []Nomination{{
		CanonicalRuleHash: rule.CanonicalHash, RuleOrdinal: 1, Kind: KindCandidateNomination,
	}}, []Rule{rule}, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Verdicts) != 1 || report.Verdicts[0].Status != RecheckVerdictClosed {
		t.Fatalf("expected CLOSED on negative recheck: %+v", report.Verdicts)
	}
}

func TestRecheckNeverEmitsResearchPass(t *testing.T) {
	rule := recheckPassRule()
	_, manifest, _ := sealTestManifestAndLedger(t, []Rule{rule})
	events := recheckEventsForRule(rule, true, protocol.G3MinRecheckClusters, 3)
	report, err := Recheck(baseRecheckOptions(events, []Nomination{{
		CanonicalRuleHash: rule.CanonicalHash, RuleOrdinal: 1, Kind: KindCandidateNomination,
	}}, []Rule{rule}, manifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, verdict := range report.Verdicts {
		if verdict.Status == "RESEARCH_PASS" {
			t.Fatal("recheck must never emit RESEARCH_PASS")
		}
	}
}

func TestRecheckInconclusiveOnSparseSamples(t *testing.T) {
	rule := recheckPassRule()
	_, manifest, _ := sealTestManifestAndLedger(t, []Rule{rule})
	events := recheckEventsForRule(rule, true, protocol.G3MinRecheckClusters-1, 3)
	report, err := Recheck(baseRecheckOptions(events, []Nomination{{
		CanonicalRuleHash: rule.CanonicalHash, RuleOrdinal: 1, Kind: KindCandidateNomination,
	}}, []Rule{rule}, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if report.MinClusters != 10 || report.Verdicts[0].ClusterCount != protocol.G3MinRecheckClusters-1 ||
		report.Verdicts[0].Status != RecheckVerdictInconclusive {
		t.Fatalf("expected INCONCLUSIVE, got %+v", report.Verdicts[0])
	}
}

func TestZeroNominationsAllowEmptyRecheckEvents(t *testing.T) {
	rules, manifest, _ := sealTestManifestAndLedger(t, nil)
	report, err := Recheck(baseRecheckOptions(nil, nil, rules, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if report.NominationCount != 0 || len(report.Verdicts) != 0 || report.RecheckDataHash == "" {
		t.Fatalf("unexpected empty recheck report: %+v", report)
	}
}

func TestEmptyRecheckEventsMarkNominationsInconclusive(t *testing.T) {
	rule := recheckPassRule()
	rules, manifest, _ := sealTestManifestAndLedger(t, []Rule{rule})
	report, err := Recheck(baseRecheckOptions(nil, []Nomination{{
		CanonicalRuleHash: rules[0].CanonicalHash,
		RuleOrdinal:       rules[0].Ordinal,
		Kind:              KindCandidateNomination,
	}}, rules, manifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Verdicts) != 1 || report.Verdicts[0].Status != RecheckVerdictInconclusive ||
		report.Verdicts[0].SampleCount != 0 {
		t.Fatalf("empty recheck must be explicit INCONCLUSIVE: %+v", report.Verdicts)
	}
}

func TestRecheckRejectsProductionParameterDrift(t *testing.T) {
	rule := recheckPassRule()
	rules, manifest, _ := sealTestManifestAndLedger(t, []Rule{rule})
	base := baseRecheckOptions(nil, nil, rules, manifest)
	tests := map[string]func(*RecheckOptions){
		"alpha":        func(o *RecheckOptions) { o.RecheckAlpha /= 2 },
		"min_samples":  func(o *RecheckOptions) { o.MinSamples-- },
		"min_clusters": func(o *RecheckOptions) { o.MinClusters-- },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := base
			mutate(&opts)
			if _, err := Recheck(opts); err == nil {
				t.Fatal("expected frozen recheck parameter drift rejection")
			}
		})
	}
}
