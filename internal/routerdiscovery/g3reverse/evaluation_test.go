package g3reverse

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

func baseEvalTime() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

type evalEventSpec struct {
	id, symbol, cluster, sky, domain, liquidity string
	horizon                                     Horizon
	direction                                   mechanism.Direction
	offset                                      time.Duration
	feature                                     float64
	net                                         float64
	class                                       OutcomeClass
	featureAsOfOffset                           time.Duration
}

func mkEvalEvent(spec evalEventSpec) Event {
	dt := baseEvalTime().Add(spec.offset)
	horizon := spec.horizon
	if horizon == "" {
		horizon = Horizon1h
	}
	liquidity := spec.liquidity
	if liquidity == "" {
		liquidity = "liq_6"
	}
	featureAsOf := dt.Add(spec.featureAsOfOffset)
	if featureAsOf.IsZero() {
		featureAsOf = dt.Add(-time.Minute)
	}
	class := spec.class
	if class == "" {
		class = classifyOutcome(spec.net, protocol.NeutralBandBPS)
	}
	sky := normalizeTestSky(spec.sky)
	return Event{
		ID: spec.id, Symbol: spec.symbol, Cluster: spec.cluster,
		SkyState: sky, SorterDomain: spec.domain, LiquidityBucket: liquidity,
		Horizon: horizon, DecisionTime: dt, FeatureAsOf: featureAsOf,
		EntryTime: dt, ExitTime: dt.Add(time.Duration(horizon.Hours()) * time.Hour),
		EntryPrice: 100, ExitPrice: 100, Direction: spec.direction,
		Features:      map[string]float64{"symbol_return_1h": spec.feature},
		NetOutcomeBPS: spec.net, OutcomeClass: class,
	}
}

func stormRule() Rule {
	pattern, _ := coarseSkyPattern(normalizeTestSky("storm_down"))
	return Rule{
		Ordinal: 1, SkyState: pattern, SorterDomain: "liquid",
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.8, Quantile: 0.5,
		}},
	}
}

func mustCoarsePattern(label string) string {
	pattern, _ := coarseSkyPattern(normalizeTestSky(label))
	return pattern
}

func sealTestManifestAndLedger(t *testing.T, rules []Rule) ([]Rule, SearchManifest, AlphaLedger) {
	t.Helper()
	for i := range rules {
		hash, err := CanonicalRuleHash(rules[i])
		if err != nil {
			t.Fatal(err)
		}
		rules[i].CanonicalHash = hash
	}
	entries := make([]LedgerEntry, len(rules))
	for i, rule := range rules {
		entries[i] = LedgerEntry{
			Ordinal: i + 1, CanonicalRuleHash: rule.CanonicalHash,
			DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
			AlphaStage:                    "reverse_induction", Status: LedgerStatusRegistered,
			SkyState: rule.SkyState, SorterDomain: rule.SorterDomain,
			Horizon: rule.Horizon, Direction: rule.Direction,
			RuleKind: "single", Conditions: rule.Conditions,
			SourceWinnerEventIDs: []string{"w1"}, AvailableFeatureSampleCount: 1,
		}
	}
	ledger, err := SealLedger(AlphaLedger{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Entries:                       entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewSearchManifest(rules, ledger, SearchManifestBindings{
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "catalog-test",
		EventManifestHash: "event-manifest-test", InductionEventHash: "induction-test",
		WinnerIndexHash: "winner-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return rules, manifest, ledger
}

func baseEvalOptions(events []Event, rules []Rule, manifest SearchManifest, ledger AlphaLedger) EvaluationOptions {
	return EvaluationOptions{
		Events: events, Rules: rules, Manifest: manifest, Ledger: ledger,
		NeutralBandBPS:        protocol.ReverseInductionNeutralBandBPS,
		MatchWindow:           time.Duration(protocol.ReverseInductionMatchWindowMs) * time.Millisecond,
		PermutationDraws:      protocol.ReverseInductionPermutationDraws,
		BootstrapSamples:      protocol.ReverseInductionBootstrapSamples,
		ExactPermutationMax:   protocol.ReverseInductionExactPermMax,
		Seed:                  protocol.ReverseInductionSeed,
		ReverseInductionAlpha: protocol.ReverseInductionAlpha,
		Generation:            protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	}
}

func matchedEvalFixture() []Event {
	var out []Event
	for i := 0; i < protocol.MinInductionClusters; i++ {
		cluster := fmt.Sprintf("c%02d", i)
		base := time.Duration(i) * 48 * time.Hour
		out = append(out,
			mkEvalEvent(evalEventSpec{id: "w" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base, feature: 0.9, net: 40, class: OutcomeWinner}),
			mkEvalEvent(evalEventSpec{id: "tl" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + 30*time.Second, feature: 0.9, net: -10, class: OutcomeLoss}),
			mkEvalEvent(evalEventSpec{id: "l" + cluster + "a", symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + time.Minute, feature: 0.2, net: -30, class: OutcomeLoss}),
			mkEvalEvent(evalEventSpec{id: "l" + cluster + "b", symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + 2*time.Minute, feature: 0.1, net: -20, class: OutcomeLoss}),
			mkEvalEvent(evalEventSpec{id: "n" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + 3*time.Minute, feature: 0.3, net: 0, class: OutcomeNeutral}),
		)
	}
	return out
}

func TestNominationRequiresMatchedControlsIncludingLossesInTreatment(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	report, _, err := EvaluateInduction(baseEvalOptions(matchedEvalFixture(), rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 {
		t.Fatalf("expected 1 nomination, got %d rejected=%+v", len(report.Nominations), report.RejectedRules)
	}
	n := report.Nominations[0]
	if n.Kind != KindCandidateNomination {
		t.Fatalf("kind=%s", n.Kind)
	}
	if n.TreatmentCount != 2*protocol.MinInductionClusters ||
		n.TreatmentLossCount != protocol.MinInductionClusters {
		t.Fatalf("treatment must include losses: %+v", n)
	}
	if n.ControlCount == 0 || n.LossControlCount+n.NeutralControlCount == 0 {
		t.Fatalf("expected matched controls: %+v", n)
	}
}

func TestWinnerOnlyWithoutMatchedControlsIsRejected(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	events := []Event{
		mkEvalEvent(evalEventSpec{id: "w1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", domain: "liquid",
			direction: mechanism.DirectionLong, feature: 0.9, net: 40, class: OutcomeWinner}),
		mkEvalEvent(evalEventSpec{id: "l1", symbol: "ETHUSDT", cluster: "c1", sky: "storm_down", domain: "liquid",
			direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss}),
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 || len(report.RejectedRules) != 1 ||
		report.RejectedRules[0].Reason != ReasonOnlyWinnerNoControls {
		t.Fatalf("expected only_winner rejection, got %+v %+v", report.Nominations, report.RejectedRules)
	}
}

func TestCrossStratumControlsCannotRescueWinnerOnly(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	winner := mkEvalEvent(evalEventSpec{id: "w1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", domain: "liquid",
		direction: mechanism.DirectionLong, feature: 0.9, net: 40, class: OutcomeWinner})
	cases := map[string]evalEventSpec{
		"different_sky_state":        {id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "calm_up", domain: "liquid", direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
		"different_sorter_domain":    {id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", domain: "illiquid", direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
		"different_liquidity_bucket": {id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", domain: "liquid", liquidity: "liq_2", direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
		"different_cluster":          {id: "l1", symbol: "BTCUSDT", cluster: "c2", sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
		"different_horizon":          {id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", domain: "liquid", horizon: Horizon4h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
		"different_symbol":           {id: "l1", symbol: "ETHUSDT", cluster: "c1", sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			report, _, err := EvaluateInduction(baseEvalOptions([]Event{winner, mkEvalEvent(spec)}, rules, manifest, ledger))
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Nominations) != 0 || report.RejectedRules[0].Reason != ReasonOnlyWinnerNoControls {
				t.Fatalf("cross-stratum control must not rescue: %+v", report)
			}
		})
	}
}

func TestControlsAreOneToOneAndNeverReused(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	var events []Event
	for i := 0; i < protocol.ReverseInductionExactPermMax+1; i++ {
		cluster := fmt.Sprintf("c%02d", i)
		base := time.Duration(i) * 48 * time.Hour
		events = append(events,
			mkEvalEvent(evalEventSpec{id: "wa" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base, feature: 0.9, net: 40, class: OutcomeWinner}),
			mkEvalEvent(evalEventSpec{id: "wb" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + time.Second, feature: 0.85, net: 45, class: OutcomeWinner}),
			mkEvalEvent(evalEventSpec{id: "only_" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", domain: "liquid",
				direction: mechanism.DirectionLong, offset: base + 2*time.Second, feature: 0.1, net: -30, class: OutcomeLoss}),
		)
	}
	report, summary, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 {
		t.Fatalf("expected nomination, got %+v", report)
	}
	if report.Nominations[0].ControlCount != protocol.ReverseInductionExactPermMax+1 {
		t.Fatalf("control reuse must be rejected: control_count=%d", report.Nominations[0].ControlCount)
	}
	if !summary.Rules[0].ReusedControlRejected {
		t.Fatal("expected reused control rejection flag")
	}
}

func TestMixedRegimeOverallNegativeButConditionalLeafDiscoverable(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	events := matchedEvalFixture()
	for i := 0; i < 40; i++ {
		events = append(events, mkEvalEvent(evalEventSpec{
			id: "calm_" + itoa(i), symbol: "BTCUSDT", cluster: "calm-" + itoa(i%4),
			sky: "calm_up", domain: "liquid", direction: mechanism.DirectionLong,
			offset: time.Duration(i) * time.Hour, feature: 0.1, net: -50, class: OutcomeLoss,
		}))
	}
	var poolSum float64
	for _, event := range events {
		poolSum += event.NetOutcomeBPS
	}
	if poolSum/float64(len(events)) >= 0 {
		t.Fatal("fixture pooled mean must be negative")
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 || !report.Nominations[0].SurvivesSelectionTax {
		t.Fatalf("conditional leaf must survive: %+v", report)
	}
}

func TestSingleClusterCannotProduceValidBootstrapCI(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	events := []Event{
		mkEvalEvent(evalEventSpec{id: "w1", symbol: "BTCUSDT", cluster: "only", sky: "storm_down", domain: "liquid",
			direction: mechanism.DirectionLong, feature: 0.9, net: 40, class: OutcomeWinner}),
		mkEvalEvent(evalEventSpec{id: "l1", symbol: "BTCUSDT", cluster: "only", sky: "storm_down", domain: "liquid",
			direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30, class: OutcomeLoss}),
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 || report.RejectedRules[0].Reason != ReasonSelectionTaxFailed {
		t.Fatalf("single cluster must fail selection tax: %+v", report)
	}
}

func TestPostDecisionFeatureIsExcludedNotMined(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	events := matchedEvalFixture()
	poisoned := events[0]
	poisoned.ID = "poisoned"
	poisoned.FeatureAsOf = poisoned.DecisionTime.Add(time.Hour)
	poisoned.NetOutcomeBPS = 999
	events = append(events, poisoned)
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if report.ExcludedEvents[ReasonPostDecisionFeature] != 1 {
		t.Fatalf("expected post_decision exclusion: %+v", report.ExcludedEvents)
	}
}

func TestEveryRuleMustBeNominatedOrRejected(t *testing.T) {
	rule := stormRule()
	badRule := Rule{
		Ordinal: 2, SkyState: mustCoarsePattern("calm_up"), SorterDomain: "liquid", Horizon: Horizon1h,
		Direction:  mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.95, Quantile: 0.75}},
	}
	hash, err := CanonicalRuleHash(badRule)
	if err != nil {
		t.Fatal(err)
	}
	badRule.CanonicalHash = hash
	rules := []Rule{rule, badRule}
	rules, manifest, ledger := sealTestManifestAndLedger(t, rules)
	report, _, err := EvaluateInduction(baseEvalOptions(matchedEvalFixture(), rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations)+len(report.RejectedRules) != 2 {
		t.Fatalf("every rule must be accounted for: %+v", report)
	}
}

func TestFrozenProductionParametersCanPassPlantedConditionalEdge(t *testing.T) {
	pattern, _ := coarseSkyPattern(normalizeTestSky("a_sky"))
	rule := Rule{
		Ordinal: 1, SkyState: pattern, SorterDomain: "liquid", Horizon: Horizon1h,
		Direction:  mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.5, Quantile: 0.5}},
	}
	hash, err := CanonicalRuleHash(rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.CanonicalHash = hash
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	var events []Event
	for i := 0; i < protocol.ReverseInductionExactPermMax+1; i++ {
		symbol := fmt.Sprintf("S%02d", i)
		events = append(events,
			mkEvalEvent(evalEventSpec{id: "t" + symbol, symbol: symbol, cluster: symbol, sky: "a_sky", domain: "liquid",
				direction: mechanism.DirectionLong, offset: time.Duration(i*48) * time.Hour, feature: 1, net: 40, class: OutcomeWinner}),
			mkEvalEvent(evalEventSpec{id: "c" + symbol, symbol: symbol, cluster: symbol, sky: "a_sky", domain: "liquid",
				direction: mechanism.DirectionLong, offset: time.Duration(i*48)*time.Hour + time.Minute, feature: 0, net: -20, class: OutcomeLoss}),
		)
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 || !report.Nominations[0].SurvivesSelectionTax {
		t.Fatalf("production parameters must allow true edge: %+v rejected=%+v", report.Nominations, report.RejectedRules)
	}
}

func TestMatchedControlsRequireExactFullSkyAcrossWildcardAxes(t *testing.T) {
	treatedFullSky := "ret=low/te=mid/rv=high/dd=mid/fund=low"
	rule := Rule{
		Ordinal: 1, SkyState: "ret=low/te=*/rv=high/dd=*/fund=low",
		SorterDomain: "liquid", Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.8, Quantile: 0.5,
		}},
	}
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	treated := mkEvalEvent(evalEventSpec{
		id: "treated", symbol: "BTCUSDT", cluster: "cluster",
		sky: treatedFullSky, domain: "liquid", direction: mechanism.DirectionLong,
		feature: 0.9, net: 40, class: OutcomeWinner,
	})
	for name, controlSky := range map[string]string{
		"different_te": "ret=low/te=high/rv=high/dd=mid/fund=low",
		"different_dd": "ret=low/te=mid/rv=high/dd=high/fund=low",
	} {
		t.Run(name, func(t *testing.T) {
			control := mkEvalEvent(evalEventSpec{
				id: "control", symbol: "BTCUSDT", cluster: "cluster",
				sky: controlSky, domain: "liquid", direction: mechanism.DirectionLong,
				offset: time.Minute, feature: 0.1, net: -20, class: OutcomeLoss,
			})
			if fires, _ := rules[0].Fires(control); fires {
				t.Fatal("non-triggering control fixture unexpectedly fires")
			}
			triggerProbe := control
			triggerProbe.Features = map[string]float64{"symbol_return_1h": 0.9}
			if fires, _ := rules[0].Fires(triggerProbe); !fires {
				t.Fatal("coarse Rule pattern must trigger across te/dd wildcard axes")
			}
			report, _, err := EvaluateInduction(baseEvalOptions(
				[]Event{treated, control}, rules, manifest, ledger,
			))
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Nominations) != 0 || len(report.RejectedRules) != 1 ||
				report.RejectedRules[0].Reason != ReasonOnlyWinnerNoControls {
				t.Fatalf("control crossed full Sky %s boundary: %+v", name, report)
			}
		})
	}
}

func TestWinnerControlsAreEligibleAndReported(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	var events []Event
	for i := 0; i < protocol.MinInductionClusters; i++ {
		cluster := fmt.Sprintf("winner-control-%02d", i)
		offset := time.Duration(i) * 24 * time.Hour
		events = append(events,
			mkEvalEvent(evalEventSpec{
				id: "treated-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset, feature: 0.9, net: 40, class: OutcomeWinner,
			}),
			mkEvalEvent(evalEventSpec{
				id: "control-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset + time.Minute, feature: 0.1, net: 30, class: OutcomeWinner,
			}),
		)
	}
	report, controls, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 {
		t.Fatalf("winner controls must remain eligible: %+v", report)
	}
	nomination := report.Nominations[0]
	if nomination.WinnerControlCount != protocol.MinInductionClusters ||
		nomination.LossControlCount != 0 || nomination.NeutralControlCount != 0 {
		t.Fatalf("control outcome distribution is dishonest: %+v", nomination)
	}
	if controls.Rules[0].WinnerControlCount != protocol.MinInductionClusters {
		t.Fatalf("control summary omitted winner controls: %+v", controls.Rules[0])
	}
}

func TestFewerThanMinimumInductionClustersRejected(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	var events []Event
	for i := 0; i < protocol.MinInductionClusters-1; i++ {
		cluster := fmt.Sprintf("sparse-%02d", i)
		offset := time.Duration(i) * 24 * time.Hour
		events = append(events,
			mkEvalEvent(evalEventSpec{
				id: "treated-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset, feature: 0.9, net: 40, class: OutcomeWinner,
			}),
			mkEvalEvent(evalEventSpec{
				id: "control-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "storm_down", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset + time.Minute, feature: 0.1, net: -20, class: OutcomeLoss,
			}),
		)
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 || len(report.RejectedRules) != 1 ||
		report.RejectedRules[0].Reason != ReasonSelectionTaxFailed {
		t.Fatalf("fewer than %d clusters must fail: %+v", protocol.MinInductionClusters, report)
	}
}

func TestWinnerDerivedThresholdOnPureNoiseDoesNotNominate(t *testing.T) {
	rng := rand.New(rand.NewSource(20260712))
	highOutcomes := balancedShuffledSigns(rng, 40)
	lowOutcomes := balancedShuffledSigns(rng, 40)
	var events []Event
	var winnerFeatureValues []float64
	for i := 0; i < 40; i++ {
		cluster := fmt.Sprintf("noise-%02d", i)
		offset := time.Duration(i) * 24 * time.Hour
		highClass := classifyOutcome(highOutcomes[i], protocol.NeutralBandBPS)
		lowClass := classifyOutcome(lowOutcomes[i], protocol.NeutralBandBPS)
		events = append(events,
			mkEvalEvent(evalEventSpec{
				id: "high-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "noise", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset, feature: 0.75, net: highOutcomes[i], class: highClass,
			}),
			mkEvalEvent(evalEventSpec{
				id: "low-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "noise", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset + time.Minute, feature: 0.25, net: lowOutcomes[i], class: lowClass,
			}),
		)
		if highClass == OutcomeWinner {
			winnerFeatureValues = append(winnerFeatureValues, 0.75)
		}
		if lowClass == OutcomeWinner {
			winnerFeatureValues = append(winnerFeatureValues, 0.25)
		}
	}
	rule := Rule{
		Ordinal: 1, SkyState: mustCoarsePattern("noise"), SorterDomain: "liquid",
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: OpGTE,
			Threshold: empiricalQuantile(winnerFeatureValues, 0.5), Quantile: 0.5,
		}},
	}
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 {
		t.Fatalf("winner-derived threshold systematically nominated pure noise: %+v", report.Nominations)
	}
}

func TestMaxRulesAndTwentyOnePairStrongEdgeIsReachable(t *testing.T) {
	rules := make([]Rule, 0, protocol.MaxDerivedSearchRules)
	for i := 0; i < protocol.MaxDerivedSearchRules-1; i++ {
		rules = append(rules, Rule{
			Ordinal: i + 1, SkyState: mustCoarsePattern(fmt.Sprintf("unused-%03d", i)),
			SorterDomain: "liquid", Horizon: Horizon1h, Direction: mechanism.DirectionLong,
			Conditions: []RuleCondition{{
				FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 1000 + float64(i), Quantile: 0.5,
			}},
		})
	}
	edgeOrdinal := protocol.MaxDerivedSearchRules
	rules = append(rules, Rule{
		Ordinal: edgeOrdinal, SkyState: mustCoarsePattern("max-rules-edge"), SorterDomain: "liquid",
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.5, Quantile: 0.5,
		}},
	})
	rules, manifest, ledger := sealTestManifestAndLedger(t, rules)
	var events []Event
	for i := 0; i < protocol.ReverseInductionExactPermMax+1; i++ {
		cluster := fmt.Sprintf("edge-%02d", i)
		offset := time.Duration(i) * 24 * time.Hour
		events = append(events,
			mkEvalEvent(evalEventSpec{
				id: "treated-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "max-rules-edge", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset, feature: 1, net: 40, class: OutcomeWinner,
			}),
			mkEvalEvent(evalEventSpec{
				id: "control-" + cluster, symbol: "BTCUSDT", cluster: cluster,
				sky: "max-rules-edge", domain: "liquid", direction: mechanism.DirectionLong,
				offset: offset + time.Minute, feature: 0, net: -20, class: OutcomeLoss,
			}),
		)
	}
	report, _, err := EvaluateInduction(baseEvalOptions(events, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if report.ActualDerivedRuleCount != protocol.MaxDerivedSearchRules ||
		report.PerRuleAlpha != protocol.ReverseInductionAlpha/float64(protocol.MaxDerivedSearchRules) {
		t.Fatalf("incorrect maximum-rule selection tax: %+v", report)
	}
	if len(report.Nominations) != 1 ||
		report.Nominations[0].RuleOrdinal != edgeOrdinal ||
		report.Nominations[0].PermutationPValue > report.PerRuleAlpha ||
		report.Nominations[0].PermutationDraws != 100000 {
		t.Fatalf("100000-draw p-value resolution made 21-pair edge unreachable: nominations=%+v alpha=%g",
			report.Nominations, report.PerRuleAlpha)
	}
}

func balancedShuffledSigns(rng *rand.Rand, count int) []float64 {
	values := make([]float64, count)
	for i := range values {
		if i < count/2 {
			values[i] = 20
		} else {
			values[i] = -20
		}
	}
	rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
	return values
}

func TestNoNominationEverReachesResearchPass(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	report, _, err := EvaluateInduction(baseEvalOptions(matchedEvalFixture(), rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range report.Nominations {
		if n.Kind != KindCandidateNomination {
			t.Fatalf("kind must be CANDIDATE_NOMINATION, got %q", n.Kind)
		}
	}
}

func TestZeroDerivedRulesProduceEmptyReports(t *testing.T) {
	rules, manifest, ledger := sealTestManifestAndLedger(t, nil)
	report, controls, err := EvaluateInduction(baseEvalOptions(nil, rules, manifest, ledger))
	if err != nil {
		t.Fatal(err)
	}
	if report.ActualDerivedRuleCount != 0 || report.PerRuleAlpha != 0 ||
		len(report.Nominations) != 0 || len(report.RejectedRules) != 0 {
		t.Fatalf("unexpected zero-rule report: %+v", report)
	}
	if len(controls.Rules) != 0 || controls.InductionDataHash == "" {
		t.Fatalf("unexpected zero-rule control summary: %+v", controls)
	}
}

func TestEvaluationRejectsProductionParameterDrift(t *testing.T) {
	rule := stormRule()
	rules, manifest, ledger := sealTestManifestAndLedger(t, []Rule{rule})
	base := baseEvalOptions(matchedEvalFixture(), rules, manifest, ledger)
	tests := map[string]func(*EvaluationOptions){
		"permutation_draws": func(o *EvaluationOptions) { o.PermutationDraws-- },
		"exact_max":         func(o *EvaluationOptions) { o.ExactPermutationMax-- },
		"bootstrap":         func(o *EvaluationOptions) { o.BootstrapSamples-- },
		"alpha":             func(o *EvaluationOptions) { o.ReverseInductionAlpha /= 2 },
		"window":            func(o *EvaluationOptions) { o.MatchWindow -= time.Hour },
		"neutral_band":      func(o *EvaluationOptions) { o.NeutralBandBPS = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			opts := base
			mutate(&opts)
			if _, _, err := EvaluateInduction(opts); err == nil {
				t.Fatal("expected frozen production parameter drift rejection")
			}
		})
	}
}

func TestDiscoveryReceiptSealSignVerifyAndTamperFail(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	receipt, err := NewDiscoveryReceipt(DiscoveryReceiptBindings{
		EventManifestHash:       "event-file-hash",
		SearchManifestHash:      "search-file-hash",
		InductionEvaluationHash: "evaluation-file-hash",
		RecheckReportHash:       "recheck-file-hash",
		NominationsHash:         "nominations-file-hash",
		ControlSummaryHash:      "controls-file-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = SignDiscoveryReceipt(receipt, key, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDiscoveryReceipt(receipt, key); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*DiscoveryReceipt){
		"recheck_report": func(r *DiscoveryReceipt) { r.RecheckReportHash = "tampered" },
		"nominations":    func(r *DiscoveryReceipt) { r.NominationsHash = "tampered" },
		"signature":      func(r *DiscoveryReceipt) { r.Signature = "00" },
		"exam_actions":   func(r *DiscoveryReceipt) { r.ExamActions = 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			tampered := receipt
			mutate(&tampered)
			if err := VerifyDiscoveryReceipt(tampered, key); err == nil {
				t.Fatal("expected discovery receipt tamper rejection")
			}
		})
	}
	if _, err := SignDiscoveryReceipt(receipt, []byte("short"), "test"); err == nil {
		t.Fatal("expected short HMAC key rejection")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := "0123456789"
	var out []byte
	for i > 0 {
		out = append([]byte{digits[i%10]}, out...)
		i /= 10
	}
	return string(out)
}
