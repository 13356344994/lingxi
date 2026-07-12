package g2prize

import (
	"fmt"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/protocol"
)

func TestFrozenProductionParametersCanPassPlantedConditionalEdge(t *testing.T) {
	space, err := DefaultSearchSpace()
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	base := baseTime()
	skyState := "ret=low/te=mid/rv=high/dd=low/fund=mid"
	for i := 0; i < protocol.BackwardExactPermutationMaxPairs+1; i++ {
		symbol := fmt.Sprintf("S%02d", i)
		cluster := symbol
		events = append(events,
			Event{
				ID: "treated-" + symbol, Symbol: symbol, Cluster: cluster,
				SkyState: skyState, SorterDomain: "liquid", LiquidityBucket: "liq_6",
				Horizon: Horizon1h, DecisionTime: base.Add(time.Duration(i*48) * time.Hour),
				FeatureAsOf: base.Add(time.Duration(i*48)*time.Hour - time.Minute),
				Payer:       mechanism.PayerFundingPayers, Direction: mechanism.DirectionLong,
				Features: map[string]float64{"funding_sign_flip": 1}, NetOutcomeBPS: 40,
			},
			Event{
				ID: "control-" + symbol, Symbol: symbol, Cluster: cluster,
				SkyState: skyState, SorterDomain: "liquid", LiquidityBucket: "liq_6",
				Horizon: Horizon1h, DecisionTime: base.Add(time.Duration(i*48)*time.Hour + time.Minute),
				FeatureAsOf: base.Add(time.Duration(i*48) * time.Hour),
				Payer:       mechanism.PayerFundingPayers, Direction: mechanism.DirectionLong,
				Features: map[string]float64{"funding_sign_flip": 0}, NetOutcomeBPS: -20,
			},
		)
	}
	report, err := Run(Options{
		Events: events, SearchSpace: space,
		NeutralBandBPS:   protocol.NeutralBandBPS,
		MatchWindow:      time.Duration(protocol.BackwardMatchWindowMs) * time.Millisecond,
		PermutationDraws: protocol.BackwardPermutationDraws,
		BootstrapSamples: protocol.BackwardBootstrapSamples,
		Seed:             protocol.BackwardSeed, FamilyWiseAlpha: protocol.BackwardFamilyAlpha,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 || !report.Nominations[0].SurvivesSelectionTax {
		t.Fatalf("frozen production parameters made true edge impossible: nominations=%+v rejected=%+v", report.Nominations, report.Rejected)
	}
}

func TestProductionG2RejectsSelfSealedSplitSearchSpace(t *testing.T) {
	space, err := DefaultSearchSpace()
	if err != nil {
		t.Fatal(err)
	}
	space.DefinitionHash, space.ManifestHash = "", ""
	space.Combinations = space.Combinations[:1]
	space, err = SealSearchSpace(space)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(Options{
		Events: []Event{mkEvent(eventSpec{
			id: "e", symbol: "BTC", cluster: "c", sky: "ret=low/te=low/rv=low/dd=low/fund=low",
			sorter: "liquid", direction: mechanism.DirectionLong, feature: 1, net: 10,
		})},
		SearchSpace: space, MatchWindow: time.Hour, FamilyWiseAlpha: protocol.BackwardFamilyAlpha,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
	})
	if err == nil {
		t.Fatal("self-sealed split search space reset production alpha")
	}
}

func baseTime() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

type eventSpec struct {
	id, symbol, cluster, sky, sorter, liquidity string
	horizon                                     Horizon
	direction                                   mechanism.Direction
	offset                                      time.Duration
	feature, net                                float64
}

func mkEvent(spec eventSpec) Event {
	dt := baseTime().Add(spec.offset)
	horizon := spec.horizon
	if horizon == "" {
		horizon = Horizon1h
	}
	liquidity := spec.liquidity
	if liquidity == "" {
		liquidity = "liq_5"
	}
	return Event{
		ID: spec.id, Symbol: spec.symbol, Cluster: spec.cluster,
		SkyState: spec.sky, SorterDomain: spec.sorter, LiquidityBucket: liquidity,
		Horizon:      horizon,
		DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
		Payer: mechanism.PayerFundingPayers, Direction: spec.direction,
		Features: map[string]float64{"signal": spec.feature}, NetOutcomeBPS: spec.net,
	}
}

func sealedSpace(id string, combos ...Combination) SearchSpace {
	space, err := SealSearchSpace(SearchSpace{
		ID: id, Generation: "test-g2", ProtocolHash: "test-g2-protocol", SourceDataHash: "test-data",
		Combinations: combos,
	})
	if err != nil {
		panic(err)
	}
	return space
}

func stormDownLongCombo(key string) Combination {
	return Combination{
		Key: key, SkyState: "storm_down", SorterDomain: "liquid", Horizon: Horizon1h,
		FeatureKey: "signal", Op: OpGTE, Threshold: 0.8, MatchingRule: "same_sky_sorter_symbol_liquidity_cluster",
		Mechanism: "settlement_constraint", Direction: mechanism.DirectionLong,
	}
}

func baseOptions() Options {
	return Options{
		MatchWindow: time.Hour, FamilyWiseAlpha: 0.05,
		PermutationDraws: 500, BootstrapSamples: 500, Seed: 7,
		ProtocolHash: "test-g2-protocol", Generation: "test-g2",
	}
}

// matchedFixture mirrors g1's proven matched-pair layout: a real, consistent
// winner effect across six independent clusters within one
// SkyState+SorterDomain+direction+horizon slice, each winner matched
// against multiple loss/neutral controls in its own cluster.
func matchedFixture() []Event {
	var out []Event
	for i, cluster := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		base := time.Duration(i) * 48 * time.Hour
		out = append(out,
			mkEvent(eventSpec{id: "w" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base, feature: 0.9, net: 40}),
			mkEvent(eventSpec{id: "tl" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + 30*time.Second, feature: 0.9, net: -10}),
			mkEvent(eventSpec{id: "l" + cluster + "a", symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + time.Minute, feature: 0.2, net: -30}),
			mkEvent(eventSpec{id: "l" + cluster + "b", symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + 2*time.Minute, feature: 0.1, net: -20}),
			mkEvent(eventSpec{id: "n" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + 3*time.Minute, feature: 0.3, net: 0}),
		)
	}
	return out
}

func TestNominationRequiresMatchedControlsIncludingLossesInTreatment(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-1", stormDownLongCombo("leaf_a"))
	opts.Events = matchedFixture()
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 {
		t.Fatalf("expected 1 nomination, got %d (rejected=%v)", len(report.Nominations), report.Rejected)
	}
	n := report.Nominations[0]
	if n.Origin != OriginBackwardMined {
		t.Fatalf("origin=%s", n.Origin)
	}
	if n.Kind != KindCandidateNomination {
		t.Fatalf("kind=%s, must always be CANDIDATE_NOMINATION", n.Kind)
	}
	if n.SkyState != "storm_down" || n.SorterDomain != "liquid" || n.Horizon != Horizon1h {
		t.Fatalf("leaf identity not recorded: %+v", n)
	}
	if n.DiscoveryDataHash == "" {
		t.Fatal("discovery data hash must be recorded")
	}
	if n.WinnerCount != 6 {
		t.Fatalf("winner count=%d", n.WinnerCount)
	}
	if n.TreatmentCount != 12 || n.TreatmentLossCount != 6 {
		t.Fatalf("every firing event, including losses, must enter treatment: %+v", n)
	}
	if n.ControlCount == 0 || n.LossControlCount+n.NeutralControlCount == 0 {
		t.Fatalf("expected loss/neutral matched controls, got control=%d loss=%d neutral=%d",
			n.ControlCount, n.LossControlCount, n.NeutralControlCount)
	}
	if n.ClusterCount < minIndependentClusters || !n.ClusterBootstrapValid {
		t.Fatalf("expected valid cluster bootstrap across >=2 clusters, got clusters=%d valid=%v", n.ClusterCount, n.ClusterBootstrapValid)
	}
	if !n.SurvivesSelectionTax {
		t.Fatalf("expected a real, consistent effect to survive the selection tax; p=%v alpha=%v ci=%+v",
			n.PermutationPValue, n.SelectionTaxAlpha, n.ClusterBootstrapCI95)
	}
}

func TestWinnerOnlyWithoutMatchedControlsIsRejected(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-2", stormDownLongCombo("leaf_a"))
	opts.Events = []Event{
		mkEvent(eventSpec{id: "w1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
			direction: mechanism.DirectionLong, feature: 0.9, net: 40}),
		// A control that exists but on a different symbol, so it can never match.
		mkEvent(eventSpec{id: "l1", symbol: "ETHUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
			direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
	}
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 {
		t.Fatalf("winner-only search must not nominate, got %+v", report.Nominations)
	}
	if len(report.Rejected) != 1 || report.Rejected[0].Reason != ReasonOnlyWinnerNoControls {
		t.Fatalf("expected rejection reason %q, got %+v", ReasonOnlyWinnerNoControls, report.Rejected)
	}
}

// TestCrossStratumControlsCannotRescueWinnerOnly proves that a control
// sharing every dimension except exactly one of
// SkyState/SorterDomain/LiquidityBucket/Cluster/Horizon is still refused —
// matching is never "close enough" across the stratification boundary.
func TestCrossStratumControlsCannotRescueWinnerOnly(t *testing.T) {
	winner := mkEvent(eventSpec{id: "w1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
		liquidity: "liq_5", horizon: Horizon1h, direction: mechanism.DirectionLong, feature: 0.9, net: 40})

	cases := map[string]Event{
		"different_sky_state": mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "calm_up", sorter: "liquid",
			liquidity: "liq_5", horizon: Horizon1h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
		"different_sorter_domain": mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", sorter: "illiquid",
			liquidity: "liq_5", horizon: Horizon1h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
		"different_liquidity_bucket": mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
			liquidity: "liq_2", horizon: Horizon1h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
		"different_cluster": mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "c2", sky: "storm_down", sorter: "liquid",
			liquidity: "liq_5", horizon: Horizon1h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
		"different_horizon": mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
			liquidity: "liq_5", horizon: Horizon4h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
		"different_symbol": mkEvent(eventSpec{id: "l1", symbol: "ETHUSDT", cluster: "c1", sky: "storm_down", sorter: "liquid",
			liquidity: "liq_5", horizon: Horizon1h, direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
	}
	for name, control := range cases {
		t.Run(name, func(t *testing.T) {
			opts := baseOptions()
			opts.SearchSpace = sealedSpace("space-cross-"+name, stormDownLongCombo("leaf_a"))
			opts.Events = []Event{winner, control}
			report, err := Run(opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Nominations) != 0 {
				t.Fatalf("%s: cross-stratum control must not rescue a winner-only search, got %+v", name, report.Nominations)
			}
			if len(report.Rejected) != 1 || report.Rejected[0].Reason != ReasonOnlyWinnerNoControls {
				t.Fatalf("%s: expected %q, got %+v", name, ReasonOnlyWinnerNoControls, report.Rejected)
			}
		})
	}
}

// TestControlsAreOneToOneAndNeverReused proves that when a cluster has two
// winners but only one eligible control, the second winner cannot also
// claim that same control: the map's control pool must grow by clusters,
// never by silently letting one control stand in for multiple winners.
func TestControlsAreOneToOneAndNeverReused(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-3", stormDownLongCombo("leaf_a"))
	var events []Event
	for i, cluster := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		base := time.Duration(i) * 48 * time.Hour
		events = append(events,
			mkEvent(eventSpec{id: "wa" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base, feature: 0.9, net: 40}),
			mkEvent(eventSpec{id: "wb" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + time.Second, feature: 0.85, net: 45}),
			mkEvent(eventSpec{id: "only_control_" + cluster, symbol: "BTCUSDT", cluster: cluster, sky: "storm_down", sorter: "liquid",
				direction: mechanism.DirectionLong, offset: base + 2*time.Second, feature: 0.1, net: -30}),
		)
	}
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 1 {
		t.Fatalf("expected 1 nomination, got %d (rejected=%+v)", len(report.Nominations), report.Rejected)
	}
	n := report.Nominations[0]
	if n.TreatmentCount != 12 || n.WinnerCount != 12 {
		t.Fatalf("both winners per cluster must enter treatment: %+v", n)
	}
	if n.ControlCount != 6 {
		t.Fatalf("exactly one control per cluster exists; it must never be shared by both winners in that cluster: control_count=%d", n.ControlCount)
	}
}

func TestLongShortSymmetricDiscovery(t *testing.T) {
	longCombo := stormDownLongCombo("leaf_long")
	shortCombo := Combination{
		Key: "leaf_short", SkyState: "calm_up", SorterDomain: "liquid", Horizon: Horizon1h,
		FeatureKey: "signal", Op: OpLTE, Threshold: 0.2, MatchingRule: "same_sky_sorter_symbol_liquidity_cluster",
		Mechanism: "settlement_constraint", Direction: mechanism.DirectionShort,
	}
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-symmetric", longCombo, shortCombo)

	var events []Event
	events = append(events, matchedFixture()...)
	for i, cluster := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		base := time.Duration(100+i) * 48 * time.Hour
		events = append(events,
			mkEvent(eventSpec{id: "sw" + cluster, symbol: "ETHUSDT", cluster: cluster, sky: "calm_up", sorter: "liquid",
				direction: mechanism.DirectionShort, offset: base, feature: 0.1, net: 35}),
			mkEvent(eventSpec{id: "stl" + cluster, symbol: "ETHUSDT", cluster: cluster, sky: "calm_up", sorter: "liquid",
				direction: mechanism.DirectionShort, offset: base + 30*time.Second, feature: 0.1, net: -15}),
			mkEvent(eventSpec{id: "sl" + cluster + "a", symbol: "ETHUSDT", cluster: cluster, sky: "calm_up", sorter: "liquid",
				direction: mechanism.DirectionShort, offset: base + time.Minute, feature: 0.8, net: -25}),
			mkEvent(eventSpec{id: "sl" + cluster + "b", symbol: "ETHUSDT", cluster: cluster, sky: "calm_up", sorter: "liquid",
				direction: mechanism.DirectionShort, offset: base + 2*time.Minute, feature: 0.9, net: -20}),
			mkEvent(eventSpec{id: "sn" + cluster, symbol: "ETHUSDT", cluster: cluster, sky: "calm_up", sorter: "liquid",
				direction: mechanism.DirectionShort, offset: base + 3*time.Minute, feature: 0.5, net: 0}),
		)
	}
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 2 {
		t.Fatalf("expected both the long and the short conditional leaf to nominate independently, got %+v / rejected=%+v",
			report.Nominations, report.Rejected)
	}
	byKey := map[string]Nomination{}
	for _, n := range report.Nominations {
		byKey[n.Key] = n
	}
	long, ok := byKey["leaf_long"]
	if !ok || long.Direction != "long" {
		t.Fatalf("expected leaf_long to nominate as long, got %+v", byKey)
	}
	short, ok := byKey["leaf_short"]
	if !ok || short.Direction != "short" {
		t.Fatalf("expected leaf_short to nominate as short, got %+v", byKey)
	}
}

// TestMixedRegimeOverallNegativeButConditionalLeafDiscoverable is the core
// red-team requirement from the G2 plan: pooling every event together
// (ignoring SkyState/SorterDomain stratification) must show a net-negative
// mean, while the SkyState-conditioned combination that targets the one
// regime where a real, matched-control-validated edge exists must still be
// discoverable and survive the selection tax.
func TestMixedRegimeOverallNegativeButConditionalLeafDiscoverable(t *testing.T) {
	var events []Event
	events = append(events, matchedFixture()...) // storm_down/liquid: robust winner, sum = 6*(40-10-30-20+0) = -120
	for i := 0; i < 40; i++ {
		events = append(events, mkEvent(eventSpec{
			id: "calm_loss_" + itoa(i), symbol: "BTCUSDT", cluster: "calm-" + itoa(i%4), sky: "calm_up", sorter: "liquid",
			direction: mechanism.DirectionLong, offset: time.Duration(i) * time.Hour, feature: 0.1, net: -50,
		}))
	}

	var poolSum float64
	for _, e := range events {
		poolSum += e.NetOutcomeBPS
	}
	poolMean := poolSum / float64(len(events))
	if poolMean >= 0 {
		t.Fatalf("fixture setup error: pooled mean across the whole mixed regime must be negative, got %v", poolMean)
	}

	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-mixed", stormDownLongCombo("leaf_storm_down"), Combination{
		Key: "leaf_calm_up", SkyState: "calm_up", SorterDomain: "liquid", Horizon: Horizon1h,
		FeatureKey: "signal", Op: OpGTE, Threshold: 0.05, MatchingRule: "same_sky_sorter_symbol_liquidity_cluster",
		Mechanism: "settlement_constraint", Direction: mechanism.DirectionLong,
	})
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations)+len(report.Rejected) != 2 {
		t.Fatalf("every searched combination must be accounted for: %+v", report)
	}
	var found bool
	for _, n := range report.Nominations {
		if n.Key == "leaf_storm_down" {
			found = true
			if !n.SurvivesSelectionTax {
				t.Fatalf("conditional leaf must survive the selection tax despite the overall mixed regime being negative: %+v", n)
			}
			if n.NetEffectMeanBPS <= 0 {
				t.Fatalf("conditional leaf's own matched-control effect must be positive: %+v", n)
			}
		}
		if n.Key == "leaf_calm_up" {
			t.Fatalf("the losing calm_up regime must not itself nominate a winning leaf: %+v", n)
		}
	}
	if !found {
		t.Fatal("expected leaf_storm_down to be discoverable despite the pooled mixed regime being negative")
	}
}

func itoa(i int) string {
	digits := "0123456789"
	if i == 0 {
		return "0"
	}
	var out []byte
	for i > 0 {
		out = append([]byte{digits[i%10]}, out...)
		i /= 10
	}
	return string(out)
}

func TestUnregisteredSearchSpaceCombinationIsRejected(t *testing.T) {
	space := sealedSpace("space-4", stormDownLongCombo("leaf_a"))
	tampered := space.Combinations[0]
	tampered.Threshold = 0.5 // same Key, different actual rule: a bait-and-switch rename.
	opts := baseOptions()
	opts.SearchSpace = space
	opts.Events = matchedFixture()
	nomination, reason, err := evaluateCombination(matchedFixture(), tampered, space, normalizeOptions(opts))
	if err != nil {
		t.Fatal(err)
	}
	if reason != ReasonUnregisteredSearchSpace {
		t.Fatalf("expected %q, got reason=%q nomination=%+v", ReasonUnregisteredSearchSpace, reason, nomination)
	}
}

func TestUnknownPayerEventIsExcludedBeforeMining(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-5", stormDownLongCombo("leaf_a"))
	events := matchedFixture()
	events[0].Payer = mechanism.PayerUnknown
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExcludedEvents["no_payer_mechanism"] != 1 {
		t.Fatalf("expected the unknown-payer event to be excluded at load time, got %+v", report.ExcludedEvents)
	}
}

func TestInconsistentPayerAcrossWinnersIsRejected(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-6", stormDownLongCombo("leaf_a"))
	events := matchedFixture()
	for i := range events {
		if events[i].Features["signal"] >= 0.8 && events[i].Cluster == "c1" {
			events[i].Payer = mechanism.PayerFundingReceivers
		}
	}
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 {
		t.Fatalf("expected no nominations without one coherent payer mechanism, got %+v", report.Nominations)
	}
	found := false
	for _, r := range report.Rejected {
		if r.Reason == ReasonNoPayerMechanism {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q rejection, got %+v", ReasonNoPayerMechanism, report.Rejected)
	}
}

func TestPostDecisionFeatureIsExcludedNotMined(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-7", stormDownLongCombo("leaf_a"))
	events := matchedFixture()
	poisoned := events[0]
	poisoned.ID = "poisoned"
	poisoned.FeatureAsOf = poisoned.DecisionTime.Add(time.Hour)
	poisoned.NetOutcomeBPS = 999
	events = append(events, poisoned)
	opts.Events = events
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExcludedEvents["post_decision_feature"] != 1 {
		t.Fatalf("expected exactly 1 post_decision_feature exclusion, got %+v", report.ExcludedEvents)
	}
	if report.EventsEligible != len(events)-1 {
		t.Fatalf("poisoned event must not count as eligible: eligible=%d input=%d", report.EventsEligible, len(events))
	}
	for _, n := range report.Nominations {
		if n.NetEffectMeanBPS > 100 {
			t.Fatalf("poisoned post-decision event leaked into nomination effect: %+v", n)
		}
	}
}

func TestEveryCombinationChargesTheSelectionTaxBudget(t *testing.T) {
	space := sealedSpace("space-multi",
		stormDownLongCombo("a"),
		Combination{Key: "b", SkyState: "storm_down", SorterDomain: "liquid", Horizon: Horizon1h,
			FeatureKey: "signal", Op: OpGTE, Threshold: 0.95, MatchingRule: "same_sky_sorter_symbol_liquidity_cluster",
			Mechanism: "settlement_constraint", Direction: mechanism.DirectionLong},
		Combination{Key: "c", SkyState: "storm_down", SorterDomain: "liquid", Horizon: Horizon1h,
			FeatureKey: "missing_feature", Op: OpGTE, Threshold: 0.5, MatchingRule: "same_sky_sorter_symbol_liquidity_cluster",
			Mechanism: "settlement_constraint", Direction: mechanism.DirectionLong},
	)
	opts := baseOptions()
	opts.SearchSpace = space
	opts.Events = matchedFixture()
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if report.SearchSpaceSize != 3 {
		t.Fatalf("search space size must equal every registered combination, got %d", report.SearchSpaceSize)
	}
	if len(report.Nominations)+len(report.Rejected) != 3 {
		t.Fatalf("every combination must be accounted for: nominations=%d rejected=%d", len(report.Nominations), len(report.Rejected))
	}
	wantAlpha := opts.FamilyWiseAlpha / 3
	for _, n := range report.Nominations {
		if n.SelectionTaxAlpha != wantAlpha {
			t.Fatalf("per-combination alpha=%v, want %v (charged against all 3 searched combinations, not just nominated ones)", n.SelectionTaxAlpha, wantAlpha)
		}
	}
	if report.PerCombinationAlpha != wantAlpha {
		t.Fatalf("report per-combination alpha=%v, want %v", report.PerCombinationAlpha, wantAlpha)
	}
}

func TestSingleClusterCannotProduceValidBootstrapCI(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-8", stormDownLongCombo("leaf_a"))
	opts.Events = []Event{
		mkEvent(eventSpec{id: "w1", symbol: "BTCUSDT", cluster: "only-cluster", sky: "storm_down", sorter: "liquid",
			direction: mechanism.DirectionLong, feature: 0.9, net: 40}),
		mkEvent(eventSpec{id: "l1", symbol: "BTCUSDT", cluster: "only-cluster", sky: "storm_down", sorter: "liquid",
			direction: mechanism.DirectionLong, offset: time.Minute, feature: 0.1, net: -30}),
	}
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) != 0 || len(report.Rejected) != 1 ||
		report.Rejected[0].Reason != ReasonSelectionTaxFailed {
		t.Fatalf("single-cluster effect must be rejected, got nominations=%+v rejected=%+v", report.Nominations, report.Rejected)
	}
}

func TestRunIsDeterministicGivenSameSeed(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-9", stormDownLongCombo("leaf_a"))
	opts.Events = matchedFixture()
	r1, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Nominations) != len(r2.Nominations) {
		t.Fatalf("nomination counts differ across identical runs: %d vs %d", len(r1.Nominations), len(r2.Nominations))
	}
	for i := range r1.Nominations {
		if r1.Nominations[i].PermutationPValue != r2.Nominations[i].PermutationPValue {
			t.Fatalf("permutation p-value not reproducible: %v vs %v", r1.Nominations[i].PermutationPValue, r2.Nominations[i].PermutationPValue)
		}
	}
}

func TestRunRejectsUnboundedConfiguration(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-10", stormDownLongCombo("leaf_a"))
	opts.Events = matchedFixture()
	opts.MatchWindow = 0
	if _, err := Run(opts); err == nil {
		t.Fatal("zero match window must be rejected, not treated as unlimited")
	}

	opts = baseOptions()
	opts.SearchSpace = sealedSpace("space-11", stormDownLongCombo("leaf_a"))
	opts.Events = matchedFixture()
	opts.FamilyWiseAlpha = 0
	if _, err := Run(opts); err == nil {
		t.Fatal("zero family-wise alpha must be rejected")
	}

	opts = baseOptions()
	opts.Events = matchedFixture()
	opts.SearchSpace = SearchSpace{}
	if _, err := Run(opts); err == nil {
		t.Fatal("empty search space must be rejected")
	}
}

func TestNoNominationEverReachesResearchPass(t *testing.T) {
	opts := baseOptions()
	opts.SearchSpace = sealedSpace("space-12", stormDownLongCombo("leaf_a"))
	opts.Events = matchedFixture()
	report, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Nominations) == 0 {
		t.Fatal("fixture must produce at least one nomination for this assertion to be meaningful")
	}
	for _, n := range report.Nominations {
		if n.Kind != KindCandidateNomination {
			t.Fatalf("nomination kind must always be CANDIDATE_NOMINATION, got %q", n.Kind)
		}
		if n.Kind == "RESEARCH_PASS" {
			t.Fatal("g2 prize-map mining must never emit RESEARCH_PASS")
		}
	}
}

func TestCombinationRequiresSkyStateAndSorterDomain(t *testing.T) {
	opts := baseOptions()
	bad := stormDownLongCombo("leaf_a")
	bad.SkyState = ""
	opts.SearchSpace, _ = SealSearchSpace(SearchSpace{
		ID: "space-13", Generation: "test-g2", ProtocolHash: "test-g2-protocol", SourceDataHash: "test-data",
		Combinations: []Combination{bad},
	})
	opts.Events = matchedFixture()
	if _, err := Run(opts); err == nil {
		t.Fatal("combination missing sky state must be rejected at validation time")
	}
}

func TestCombinationRequiresValidHorizon(t *testing.T) {
	opts := baseOptions()
	bad := stormDownLongCombo("leaf_a")
	bad.Horizon = "3h"
	opts.SearchSpace, _ = SealSearchSpace(SearchSpace{
		ID: "space-14", Generation: "test-g2", ProtocolHash: "test-g2-protocol", SourceDataHash: "test-data",
		Combinations: []Combination{bad},
	})
	opts.Events = matchedFixture()
	if _, err := Run(opts); err == nil {
		t.Fatal("combination with an unrecognized horizon must be rejected at validation time")
	}
}
