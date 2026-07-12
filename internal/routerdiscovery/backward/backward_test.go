package backward

import (
	"math/rand"
	"testing"
	"time"

	"lingxi/internal/mechanism"
)

func baseTime() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func mkEvent(id, symbol, cluster string, decisionOffset time.Duration, payer mechanism.PayerRole, feature, vol, liq, net float64) Event {
	dt := baseTime().Add(decisionOffset)
	return Event{
		ID: id, Symbol: symbol, Cluster: cluster, Stratum: "s1",
		DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
		Payer: payer, Volatility: vol, Liquidity: liq,
		Direction: mechanism.DirectionLong,
		Features:  map[string]float64{"signal": feature}, NetOutcomeBPS: net,
	}
}

func basicSearchSpace() SearchSpace {
	space, _ := SealSearchSpace(SearchSpace{
		ID: "test-space-v1", Generation: "test-g1", ProtocolHash: "test-protocol", SourceDataHash: "test-data",
		Combinations: []Combination{
			{Key: "signal_high", FeatureKey: "signal", Op: OpGTE, Threshold: 0.8, MatchingRule: "same_time_symbol_volatility_liquidity", Mechanism: "funding_settlement", Direction: mechanism.DirectionLong},
		},
	})
	return space
}

// fixture with a real, consistent winner effect across six independent
// clusters, each winner matched against multiple loss/neutral controls.
// Six independent clusters keep the permutation placebo test's achievable
// p-value floor (~1/2^6) comfortably below realistic alpha budgets.
func matchedFixture() []Event {
	var out []Event
	for i, cluster := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		base := time.Duration(i) * 48 * time.Hour
		out = append(out,
			mkEvent("w"+cluster, "BTCUSDT", cluster, base, mechanism.PayerLongLiquidations, 0.9, 1.0, 1.0, 40),
			mkEvent("tl"+cluster, "BTCUSDT", cluster, base+30*time.Second, mechanism.PayerLongLiquidations, 0.9, 1.0, 1.0, -10),
			mkEvent("l"+cluster+"a", "BTCUSDT", cluster, base+time.Minute, mechanism.PayerLongLiquidations, 0.2, 1.0, 1.0, -30),
			mkEvent("l"+cluster+"b", "BTCUSDT", cluster, base+2*time.Minute, mechanism.PayerLongLiquidations, 0.1, 1.0, 1.0, -20),
			mkEvent("n"+cluster, "BTCUSDT", cluster, base+3*time.Minute, mechanism.PayerLongLiquidations, 0.3, 1.0, 1.0, 0),
		)
	}
	return out
}

func baseOptions() Options {
	return Options{
		SearchSpace:         basicSearchSpace(),
		MatchWindow:         time.Hour,
		VolatilityTolerance: 0.01,
		LiquidityTolerance:  0.01,
		FamilyWiseAlpha:     0.05,
		PermutationDraws:    500,
		BootstrapSamples:    500,
		Seed:                7,
		ProtocolHash:        "test-protocol",
		Generation:          "test-g1",
	}
}

func TestNominationRequiresMatchedControlsNotJustWinner(t *testing.T) {
	opts := baseOptions()
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
	if n.DiscoveryDataHash == "" {
		t.Fatal("discovery data hash must be recorded")
	}
	if n.SearchSpaceSize != 1 {
		t.Fatalf("search space size=%d", n.SearchSpaceSize)
	}
	if n.Rank != 1 {
		t.Fatalf("rank=%d", n.Rank)
	}
	if n.MatchingRule == "" {
		t.Fatal("matching rule must be recorded")
	}
	if n.WinnerCount != 6 {
		t.Fatalf("winner count=%d", n.WinnerCount)
	}
	if n.TreatmentCount != 12 || n.TreatmentLossCount != 6 {
		t.Fatalf("all firing events, including losses, must enter treatment: %+v", n)
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
	// A winner with no time/symbol/volatility/liquidity-matched loss or
	// neutral control anywhere in the pool: only-explains-winners.
	opts.Events = []Event{
		mkEvent("w1", "BTCUSDT", "c1", 0, mechanism.PayerLongLiquidations, 0.9, 1.0, 1.0, 40),
		// A control that exists but is on a different symbol so it can never match.
		mkEvent("l1", "ETHUSDT", "c1", time.Minute, mechanism.PayerLongLiquidations, 0.1, 1.0, 1.0, -30),
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

func TestUnregisteredSearchSpaceCombinationIsRejected(t *testing.T) {
	space := basicSearchSpace()
	tampered := space.Combinations[0]
	tampered.Threshold = 0.5 // same Key, different actual rule: a bait-and-switch rename.
	opts := baseOptions()
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
	events := matchedFixture()
	// Winners must resolve to one coherent payer mechanism; mixing two
	// distinct (but individually valid) payer mechanisms across winners
	// means there is no single mechanism this candidate can name.
	for i := range events {
		if events[i].Features["signal"] >= 0.8 && events[i].Cluster == "c1" {
			events[i].Payer = mechanism.PayerFundingPayers
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
	events := matchedFixture()
	// Poison one winner with a feature snapshot taken *after* decision time.
	poisoned := events[0]
	poisoned.ID = "poisoned"
	poisoned.FeatureAsOf = poisoned.DecisionTime.Add(time.Hour)
	poisoned.NetOutcomeBPS = 999 // an absurd outcome that must never influence any nomination.
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
	space, _ := SealSearchSpace(SearchSpace{
		ID: "multi-combo", Generation: "test-g1", ProtocolHash: "test-protocol", SourceDataHash: "test-data",
		Combinations: []Combination{
			{Key: "a", FeatureKey: "signal", Op: OpGTE, Threshold: 0.8, MatchingRule: "same_time_symbol_volatility_liquidity", Mechanism: "funding_settlement", Direction: mechanism.DirectionLong},
			{Key: "b", FeatureKey: "signal", Op: OpGTE, Threshold: 0.95, MatchingRule: "same_time_symbol_volatility_liquidity", Mechanism: "funding_settlement", Direction: mechanism.DirectionLong},
			{Key: "c", FeatureKey: "missing_feature", Op: OpGTE, Threshold: 0.5, MatchingRule: "same_time_symbol_volatility_liquidity", Mechanism: "funding_settlement", Direction: mechanism.DirectionLong},
		},
	})
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
	opts.Events = []Event{
		mkEvent("w1", "BTCUSDT", "only-cluster", 0, mechanism.PayerLongLiquidations, 0.9, 1.0, 1.0, 40),
		mkEvent("l1", "BTCUSDT", "only-cluster", time.Minute, mechanism.PayerLongLiquidations, 0.1, 1.0, 1.0, -30),
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

func TestPermutationPlaceboRejectsNoiseMatchedToControls(t *testing.T) {
	// Winners and controls drawn from the same distribution: no true
	// differential edge, only what looks like one from a single draw.
	diffs := []float64{3, -2, 4, -5, 1, -1, 2, -3}
	rng := rand.New(rand.NewSource(11))
	p := pairedPermutationPValue(diffs, 5000, rng)
	if p < 0.2 {
		t.Fatalf("placebo-like symmetric diffs should not produce a small p-value, got %v", p)
	}
}

func TestPermutationPlaceboAcceptsConsistentDirectionalDiffs(t *testing.T) {
	diffs := []float64{10, 12, 9, 11, 13, 10, 14, 9, 11, 12}
	rng := rand.New(rand.NewSource(11))
	p := pairedPermutationPValue(diffs, 5000, rng)
	if p > 0.05 {
		t.Fatalf("a consistently positive, direction-stable diff set should produce a small p-value, got %v", p)
	}
}

func TestRunIsDeterministicGivenSameSeed(t *testing.T) {
	opts := baseOptions()
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
	opts.Events = matchedFixture()
	opts.MatchWindow = 0
	if _, err := Run(opts); err == nil {
		t.Fatal("zero match window must be rejected, not treated as unlimited")
	}

	opts = baseOptions()
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
			t.Fatal("backward mining must never emit RESEARCH_PASS")
		}
	}
}
