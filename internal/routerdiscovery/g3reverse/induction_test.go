package g3reverse

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

func testSigningKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func baseBindings() SearchManifestBindings {
	return SearchManifestBindings{
		SourceDataHash:        protocol.HistoryDataHash,
		CatalogDerivationHash: "catalog-derivation-test",
		EventManifestHash:     "event-manifest-test",
		InductionEventHash:    "",
		WinnerIndexHash:       "",
		CoverageHash:          "coverage-test",
	}
}

func makeEvent(id, sky, domain string, horizon Horizon, dir mechanism.Direction, features map[string]float64, missing []string, net float64, class OutcomeClass) Event {
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	sky = normalizeTestSky(sky)
	if !stringIn(domain, protocol.G3SorterDomains) {
		domain = protocol.G3SorterDomains[0]
	}
	return Event{
		ID: id, Symbol: "BTCUSDT", Cluster: "BTCUSDT",
		SkyState: sky, SorterDomain: domain, LiquidityBucket: "liq_6",
		Horizon: horizon, DecisionTime: decision, FeatureAsOf: decision,
		EntryTime: decision, ExitTime: decision.Add(time.Duration(horizon.Hours()) * time.Hour),
		EntryPrice: 100, ExitPrice: 100, Direction: dir,
		Features: features, MissingFeatures: missing,
		NetOutcomeBPS: net, OutcomeClass: class,
	}
}

func normalizeTestSky(label string) string {
	if strings.HasPrefix(label, "ret=") {
		return label
	}
	var hash uint32
	for _, r := range label {
		hash = hash*33 + uint32(r)
	}
	labels := protocol.CoarseSkyAxisLabels
	ret := labels[int(hash)%len(labels)]
	rv := labels[int(hash/3)%len(labels)]
	fund := labels[int(hash/9)%len(labels)]
	te := labels[int(hash/27)%len(labels)]
	dd := labels[int(hash/81)%len(labels)]
	return fmt.Sprintf("ret=%s/te=%s/rv=%s/dd=%s/fund=%s", ret, te, rv, dd, fund)
}

func coarseTestSky(t *testing.T, label string) string {
	t.Helper()
	pattern, err := coarseSkyPattern(normalizeTestSky(label))
	if err != nil {
		t.Fatal(err)
	}
	return pattern
}

func fullTestSkyFromPattern(pattern string) string {
	values, err := parseSkyState(pattern)
	if err != nil {
		return pattern
	}
	if values["te"] == "*" {
		values["te"] = "mid"
	}
	if values["dd"] == "*" {
		values["dd"] = "mid"
	}
	return fmt.Sprintf("ret=%s/te=%s/rv=%s/dd=%s/fund=%s",
		values["ret"], values["te"], values["rv"], values["dd"], values["fund"])
}

func TestFiresRequiresFullGroupAndPresentFeatures(t *testing.T) {
	rule := Rule{
		SkyState: coarseTestSky(t, "skyA"), SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0.5, Quantile: 0.5}},
	}
	ok := makeEvent("w1", "skyA", "domA", Horizon1h, mechanism.DirectionLong,
		map[string]float64{"symbol_return_1h": 1.0}, nil, 50, OutcomeWinner)
	fires, has := rule.Fires(ok)
	if !fires || !has {
		t.Fatalf("expected fire, fires=%v has=%v", fires, has)
	}

	wrongSky := ok
	wrongSky.SkyState = "skyB"
	if fires, _ = rule.Fires(wrongSky); fires {
		t.Fatal("wrong sky must not fire")
	}

	missing := ok
	missing.Features = map[string]float64{}
	missing.MissingFeatures = []string{"symbol_return_1h"}
	fires, has = rule.Fires(missing)
	if fires || has {
		t.Fatalf("missing feature must not fire: fires=%v has=%v", fires, has)
	}

	below := ok
	below.Features["symbol_return_1h"] = 0.1
	fires, has = rule.Fires(below)
	if fires || !has {
		t.Fatalf("below threshold: fires=%v has=%v", fires, has)
	}
}

func TestCoarsePatternFiresAcrossWildcardAxesOnly(t *testing.T) {
	pattern := "ret=low/te=*/rv=high/dd=*/fund=mid"
	rule := Rule{
		SkyState: pattern, SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon4h, Direction: mechanism.DirectionShort,
		Conditions: []RuleCondition{{
			FeatureKey: protocol.FeatureDerivationOrder[0], Op: OpLTE, Threshold: 1, Quantile: 0.25,
		}},
	}
	if err := ValidateRule(rule); err != nil {
		t.Fatal(err)
	}
	for _, full := range []string{
		"ret=low/te=low/rv=high/dd=low/fund=mid",
		"ret=low/te=high/rv=high/dd=high/fund=mid",
	} {
		event := makeEvent("match", full, protocol.G3SorterDomains[0], Horizon4h, mechanism.DirectionShort,
			map[string]float64{protocol.FeatureDerivationOrder[0]: 0.5}, nil, 1, OutcomeNeutral)
		if fires, has := rule.Fires(event); !fires || !has {
			t.Fatalf("pattern should fire on %q: fires=%v has=%v", full, fires, has)
		}
	}
	mismatch := makeEvent("miss",
		"ret=mid/te=low/rv=high/dd=low/fund=mid",
		protocol.G3SorterDomains[0], Horizon4h, mechanism.DirectionShort,
		map[string]float64{protocol.FeatureDerivationOrder[0]: 0.5}, nil, 1, OutcomeNeutral)
	if fires, _ := rule.Fires(mismatch); fires {
		t.Fatal("exact coarse ret axis mismatch must not fire")
	}
	for _, invalid := range []string{
		"te=*/ret=low/rv=high/dd=*/fund=mid",
		"ret=*/te=*/rv=high/dd=*/fund=mid",
		"ret=low/te=mid/rv=high/dd=*/fund=mid",
		"ret=low/te=*/rv=extreme/dd=*/fund=mid",
	} {
		bad := rule
		bad.SkyState = invalid
		if err := ValidateRule(bad); err == nil {
			t.Fatalf("expected invalid coarse pattern rejection: %s", invalid)
		}
	}
}

func TestValidateRuleRejectsForbiddenAndOutcomeFeatures(t *testing.T) {
	base := Rule{
		SkyState: coarseTestSky(t, "s"), SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpLTE, Threshold: 1, Quantile: 0.25}},
	}
	if err := ValidateRule(base); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"pnl", "net_outcome_bps", "outcome", "forward_return", "post_decision_features", "rubik_taker_5m_imbalance"} {
		r := base
		r.Conditions = []RuleCondition{{FeatureKey: bad, Op: OpLTE, Threshold: 1, Quantile: 0.25}}
		if err := ValidateRule(r); err == nil {
			t.Fatalf("expected reject for %q", bad)
		}
	}
}

func TestDerivePlantedWinnersGenerateExpectedRules(t *testing.T) {
	// Plant winners with descending decision times (reverse chronology) but
	// fixed feature ladder so quantiles are deterministic.
	sky, dom := "a_sky", "a_dom"
	var events []Event
	values := []float64{1, 2, 3, 4}
	for i, v := range values {
		id := eventID("BTCUSDT", int64(1000-i), Horizon1h, mechanism.DirectionLong)
		feat := map[string]float64{"symbol_return_1h": v}
		events = append(events, makeEvent(id, sky, dom, Horizon1h, mechanism.DirectionLong, feat, nil, 100, OutcomeWinner))
		events[len(events)-1].DecisionTime = time.UnixMilli(int64(1000 - i)).UTC()
	}
	// Overall-negative losses in same group must not block induction.
	for i := 0; i < 20; i++ {
		id := eventID("BTCUSDT", int64(2000+i), Horizon1h, mechanism.DirectionLong)
		events = append(events, makeEvent(id, sky, dom, Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": -1}, nil, -80, OutcomeLoss))
	}

	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.ActualDerivedRuleCount == 0 {
		t.Fatal("expected derived rules from planted winners")
	}
	if result.Summary.ActualDerivedRuleCount != len(result.Rules) {
		t.Fatal("actual count must equal rules slice (no padding)")
	}
	if result.Summary.ActualDerivedRuleCount > protocol.MaxDerivedSearchRules {
		t.Fatal("must not exceed total cap")
	}

	// First registered single must be catalog-first feature, q=0.25, LTE.
	var firstSingle *Rule
	for i := range result.Rules {
		if len(result.Rules[i].Conditions) == 1 {
			firstSingle = &result.Rules[i]
			break
		}
	}
	if firstSingle == nil {
		t.Fatal("expected at least one single-feature rule")
	}
	if firstSingle.Conditions[0].FeatureKey != protocol.FeatureCatalog[0] {
		t.Fatalf("first single feature=%q want catalog[0]=%q", firstSingle.Conditions[0].FeatureKey, protocol.FeatureCatalog[0])
	}
	if firstSingle.Conditions[0].Op != OpLTE || firstSingle.Conditions[0].Quantile != 0.25 {
		t.Fatalf("first single should be LTE@0.25, got op=%s q=%v", firstSingle.Conditions[0].Op, firstSingle.Conditions[0].Quantile)
	}
	wantThresh := empiricalQuantile(values, 0.25)
	if firstSingle.Conditions[0].Threshold != wantThresh {
		t.Fatalf("threshold=%v want %v", firstSingle.Conditions[0].Threshold, wantThresh)
	}
	if firstSingle.SkyState != coarseTestSky(t, sky) || firstSingle.SorterDomain != protocol.G3SorterDomains[0] {
		t.Fatal("group identity mismatch")
	}
}

func TestDeriveMixedOverallNegativeStillGenerates(t *testing.T) {
	sky, dom := "neg_sky", "neg_dom"
	var events []Event
	// Many large losses.
	for i := 0; i < 50; i++ {
		id := eventID("ETHUSDT", int64(i), Horizon4h, mechanism.DirectionShort)
		events = append(events, makeEvent(id, sky, dom, Horizon4h, mechanism.DirectionShort,
			map[string]float64{"symbol_return_4h": float64(i) * 0.01}, nil, -200, OutcomeLoss))
	}
	// Few winners with distinctive feature.
	for i := 0; i < 4; i++ {
		id := eventID("ETHUSDT", int64(1000+i), Horizon4h, mechanism.DirectionShort)
		events = append(events, makeEvent(id, sky, dom, Horizon4h, mechanism.DirectionShort,
			map[string]float64{"symbol_return_4h": 10 + float64(i)}, nil, 40, OutcomeWinner))
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rules) == 0 {
		t.Fatal("mixed overall-negative pool must still generate winner-based rules")
	}
}

func TestMissingMechanismFeatureAdvancesToNextFeatureForCoverage(t *testing.T) {
	firstFeature := protocol.FeatureDerivationOrder[0]
	fallbackFeature := protocol.FeatureDerivationOrder[4]
	events := []Event{
		makeEvent("first-feature", "ret=low/te=mid/rv=low/dd=mid/fund=low",
			protocol.G3SorterDomains[0], Horizon1h, mechanism.DirectionLong,
			map[string]float64{firstFeature: 1}, nil, 20, OutcomeWinner),
		makeEvent("fallback-feature", "ret=mid/te=mid/rv=mid/dd=mid/fund=mid",
			protocol.G3SorterDomains[0], Horizon1h, mechanism.DirectionLong,
			map[string]float64{fallbackFeature: 2}, protocol.FeatureDerivationOrder[:4], 20, OutcomeWinner),
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.ObservedGroupCount != 2 || result.Summary.CoveredGroupCount != 2 ||
		result.Summary.SingleRegistered != 2 {
		t.Fatalf("missing feature blocked coverage: %+v", result.Summary)
	}
	featuresBySky := map[string]string{}
	for _, rule := range result.Rules {
		if len(rule.Conditions) == 1 {
			featuresBySky[rule.SkyState] = rule.Conditions[0].FeatureKey
		}
	}
	if featuresBySky["ret=low/te=*/rv=low/dd=*/fund=low"] != firstFeature ||
		featuresBySky["ret=mid/te=*/rv=mid/dd=*/fund=mid"] != fallbackFeature {
		t.Fatalf("unexpected fixed-order fallback features: %+v", featuresBySky)
	}
}

func TestDeriveDeterministicAndNotPnLOrdered(t *testing.T) {
	skyA := "ret=high/te=mid/rv=high/dd=mid/fund=high"
	skyB := "ret=low/te=mid/rv=low/dd=mid/fund=low"
	var events []Event
	// Group B has huge PnL winners but lexicographically later sky label.
	for i := 0; i < 4; i++ {
		id := eventID("BTCUSDT", int64(10+i), Horizon1h, mechanism.DirectionLong)
		events = append(events, makeEvent(id, skyB, "dom", Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": 100 + float64(i), "symbol_return_4h": 1}, nil, 10000, OutcomeWinner))
	}
	// Group A has small PnL but comes first lexicographically.
	for i := 0; i < 4; i++ {
		id := eventID("BTCUSDT", int64(20+i), Horizon1h, mechanism.DirectionLong)
		events = append(events, makeEvent(id, skyA, "dom", Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": float64(i + 1), "symbol_return_4h": 2}, nil, 5, OutcomeWinner))
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.ManifestHash != second.Manifest.ManifestHash ||
		first.Ledger.LedgerHash != second.Ledger.LedgerHash ||
		first.Manifest.CanonicalDerivedRulesHash != second.Manifest.CanonicalDerivedRulesHash {
		t.Fatal("derivation must be deterministic")
	}
	if len(first.Rules) == 0 {
		t.Fatal("expected rules")
	}
	if first.Rules[0].SkyState != coarseTestSky(t, skyA) {
		t.Fatalf("rules must follow group lexicographic order, not PnL; got sky=%q", first.Rules[0].SkyState)
	}
}

func TestDeriveFixedStreamingStopsNotByPnL(t *testing.T) {
	// Two groups fill the single cap; dual enumeration then fills its cap.
	var events []Event
	for _, sky := range []string{
		"ret=low/te=mid/rv=low/dd=mid/fund=low",
		"ret=high/te=mid/rv=high/dd=mid/fund=high",
	} {
		for i := 0; i < 4; i++ {
			id := eventID("BTCUSDT", int64(len(events)+1), Horizon1h, mechanism.DirectionLong)
			// Give all catalog features so every feature emits rules.
			feat := map[string]float64{}
			for _, name := range protocol.FeatureCatalog {
				feat[name] = float64(i+1) * 0.1
			}
			net := 10.0
			if strings.Contains(sky, "ret=high") {
				net = 9999 // higher PnL must not jump the queue
			}
			events = append(events, makeEvent(id, sky, "dom", Horizon1h, mechanism.DirectionLong, feat, nil, net, OutcomeWinner))
		}
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.SingleRegistered > protocol.MaxSingleFeatureRules {
		t.Fatalf("single registered=%d over cap", result.Summary.SingleRegistered)
	}
	if result.Summary.DualRegistered > protocol.MaxDualFeatureRules {
		t.Fatalf("dual registered=%d over cap", result.Summary.DualRegistered)
	}
	if result.Summary.ActualDerivedRuleCount > protocol.MaxDerivedSearchRules {
		t.Fatal("total over cap")
	}
	counts := result.Summary.LedgerStatusCounts
	if counts[LedgerStatusTruncated] != 0 {
		t.Fatal("post-stop attempts must be not_considered and absent from ledger")
	}
	if counts[LedgerStatusRegistered] != result.Summary.ActualDerivedRuleCount {
		t.Fatal("registered ledger count must match actual rules")
	}
	if result.Summary.SingleRegistered != 2 ||
		result.Summary.DualRegistered != protocol.MaxDualFeatureRules ||
		len(result.Ledger.Entries) != 2+protocol.MaxDualFeatureRules {
		t.Fatalf("streaming caps mismatch: single=%d dual=%d ledger=%d",
			result.Summary.SingleRegistered, result.Summary.DualRegistered, len(result.Ledger.Entries))
	}
	if result.Summary.ObservedGroupCount != 2 || result.Summary.CoveredGroupCount != 2 {
		t.Fatalf("coverage mismatch: observed=%d covered=%d",
			result.Summary.ObservedGroupCount, result.Summary.CoveredGroupCount)
	}
	// Singles before duals among registered.
	seenDual := false
	for _, rule := range result.Rules {
		if len(rule.Conditions) == 2 {
			seenDual = true
		}
		if seenDual && len(rule.Conditions) == 1 {
			t.Fatal("single rules must come before dual rules; never PnL reorder")
		}
	}
}

func TestDeriveStreamingStressBoundedAndPrefixStable(t *testing.T) {
	var events []Event
	horizons := []Horizon{Horizon1h, Horizon4h, Horizon24h}
	directions := []mechanism.Direction{mechanism.DirectionLong, mechanism.DirectionShort}
	group := 0
	for _, ret := range protocol.CoarseSkyAxisLabels {
		for _, rv := range protocol.CoarseSkyAxisLabels {
			for _, fund := range protocol.CoarseSkyAxisLabels {
				for _, horizon := range horizons {
					for _, direction := range directions {
						sky := fmt.Sprintf("ret=%s/te=mid/rv=%s/dd=mid/fund=%s", ret, rv, fund)
						for winner := 0; winner < 4; winner++ {
							id := eventID("BTCUSDT", int64(len(events)+1), horizon, direction)
							features := make(map[string]float64, len(protocol.FeatureCatalog))
							for featureIndex, feature := range protocol.FeatureCatalog {
								features[feature] = float64((winner+1)*(featureIndex+1)) + float64(group)/1000
							}
							event := makeEvent(id, sky, protocol.G3SorterDomains[0], horizon, direction,
								features, nil, 20, OutcomeWinner)
							event.DecisionTime = time.Unix(int64(len(events)+1), 0).UTC()
							events = append(events, event)
						}
						group++
					}
				}
			}
		}
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	var progress []DerivationProgress
	stress, err := DeriveWithOptions(
		events, winners, baseBindings(), testSigningKey(), "test",
		DeriveOptions{
			ProgressEvery: 32,
			OnProgress: func(update DerivationProgress) {
				progress = append(progress, update)
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stress.Rules) != protocol.MaxDerivedSearchRules ||
		len(stress.Ledger.Entries) != protocol.MaxDerivedSearchRules {
		t.Fatalf("materialization bound violated: rules=%d ledger=%d",
			len(stress.Rules), len(stress.Ledger.Entries))
	}
	if stress.Summary.ObservedGroupCount != protocol.MaxCoarseObservedGroups ||
		stress.Summary.CoveredGroupCount != protocol.MaxCoarseObservedGroups ||
		len(stress.Summary.RulesPerGroup) != protocol.MaxCoarseObservedGroups {
		t.Fatalf("round-robin did not cover observed groups: observed=%d covered=%d map=%d",
			stress.Summary.ObservedGroupCount, stress.Summary.CoveredGroupCount,
			len(stress.Summary.RulesPerGroup))
	}
	if stress.Summary.SingleRegistered != protocol.MaxSingleFeatureRules ||
		stress.Summary.DualRegistered != protocol.MaxDualFeatureRules {
		t.Fatalf("v3 caps mismatch: single=%d dual=%d",
			stress.Summary.SingleRegistered, stress.Summary.DualRegistered)
	}
	if len(progress) == 0 {
		t.Fatal("expected streaming progress callbacks")
	}
	maxAttempts := 0
	for _, update := range progress {
		if update.Attempts > maxAttempts {
			maxAttempts = update.Attempts
		}
	}
	if maxAttempts != len(stress.Ledger.Entries) || maxAttempts != protocol.MaxDerivedSearchRules {
		t.Fatalf("attempt count not bounded at stop: attempts=%d ledger=%d", maxAttempts, len(stress.Ledger.Entries))
	}
	for _, entry := range stress.Ledger.Entries {
		if len(entry.SourceWinnerEventIDs) != 0 {
			t.Fatal("stress ledger copied unbounded source IDs")
		}
	}

	singleGroups := map[string]bool{}
	for _, rule := range stress.Rules[:protocol.MaxSingleFeatureRules] {
		singleGroups[rule.SkyState+"|"+rule.SorterDomain+"|"+string(rule.Horizon)+"|"+string(rule.Direction)] = true
	}
	if len(singleGroups) != protocol.MaxCoarseObservedGroups {
		t.Fatalf("single stage covered only %d groups; want %d", len(singleGroups), protocol.MaxCoarseObservedGroups)
	}
}

func TestLedgerCompleteStatusesAndProvenance(t *testing.T) {
	var events []Event
	for i := 0; i < 4; i++ {
		id := eventID("BTCUSDT", int64(i+1), Horizon1h, mechanism.DirectionLong)
		feat := map[string]float64{"symbol_return_1h": float64(i + 1)}
		events = append(events, makeEvent(id, "sky", "dom", Horizon1h, mechanism.DirectionLong, feat, nil, 20, OutcomeWinner))
	}
	// Same feature values again in a second identical group clone via same key —
	// also add a feature-missing winner path for considered.
	missingID := eventID("BTCUSDT", 99, Horizon24h, mechanism.DirectionShort)
	events = append(events, makeEvent(missingID, "other", "dom", Horizon24h, mechanism.DirectionShort,
		map[string]float64{}, append([]string{}, protocol.FeatureCatalog...), 30, OutcomeWinner))

	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Ledger.Validate(); err != nil {
		t.Fatal(err)
	}
	counts := result.Summary.LedgerStatusCounts
	if counts[LedgerStatusRegistered] == 0 {
		t.Fatal("expected registered entries")
	}
	if counts[LedgerStatusConsidered] == 0 {
		t.Fatal("expected considered entries for missing-feature group")
	}
	for _, entry := range result.Ledger.Entries {
		if entry.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
			t.Fatal("ledger must bind derivation algorithm config hash")
		}
		if len(entry.SourceWinnerEventIDs) != 0 {
			t.Fatal("streaming ledger must not copy source winner IDs")
		}
		if entry.Status == LedgerStatusRegistered &&
			(entry.SourceWinnerIDsHash == "" || entry.SourceWinnerCount == 0) {
			t.Fatal("registered entries must retain compact source provenance")
		}
		if entry.AlphaStage != "reverse_induction" {
			t.Fatal("alpha_stage mismatch")
		}
	}
	// Append-only ordinals.
	for i, entry := range result.Ledger.Entries {
		if entry.Ordinal != i+1 {
			t.Fatalf("ordinal=%d want %d", entry.Ordinal, i+1)
		}
	}
}

func TestSearchManifestTamperAndNoPadding(t *testing.T) {
	var events []Event
	for i := 0; i < 3; i++ {
		id := eventID("BTCUSDT", int64(i+1), Horizon1h, mechanism.DirectionLong)
		events = append(events, makeEvent(id, "sky", "dom", Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": float64(i + 1)}, nil, 20, OutcomeWinner))
	}
	winners, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Derive(events, winners, baseBindings(), testSigningKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ActualDerivedRuleCount != len(result.Rules) {
		t.Fatal("actual count must equal rules length")
	}
	if result.Manifest.ActualDerivedRuleCount == protocol.MaxDerivedSearchRules && len(result.Rules) < protocol.MaxDerivedSearchRules {
		t.Fatal("must not pad to 192")
	}
	if result.Manifest.DerivationAlgorithmConfigHash != protocol.DerivationAlgorithmConfigHash {
		t.Fatalf("algorithm hash=%q", result.Manifest.DerivationAlgorithmConfigHash)
	}
	if result.Manifest.CoverageHash == "" || result.Manifest.CoverageHash != result.Summary.CoverageHash {
		t.Fatal("search manifest must bind induction coverage hash")
	}
	if err := VerifySearchManifestSignature(result.Manifest, testSigningKey()); err != nil {
		t.Fatal(err)
	}
	tampered := result.Manifest
	tampered.ActualDerivedRuleCount = protocol.MaxDerivedSearchRules
	if err := VerifySearchManifestSignature(tampered, testSigningKey()); err == nil {
		t.Fatal("tampered search manifest must fail verification")
	}
	tamperedSig := result.Manifest
	tamperedSig.Signature = strings.Repeat("ab", 32)
	if err := VerifySearchManifestSignature(tamperedSig, testSigningKey()); err == nil {
		t.Fatal("bad signature must fail")
	}
	tamperedCoverage := result.Manifest
	tamperedCoverage.CoverageHash = "tampered"
	if err := VerifySearchManifestSignature(tamperedCoverage, testSigningKey()); err == nil {
		t.Fatal("coverage hash tamper must fail")
	}
}

func TestWinnerIndexMustReferenceRealWinners(t *testing.T) {
	events := []Event{
		makeEvent("e1", "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": 1}, nil, -10, OutcomeLoss),
	}
	fake := WinnerIndex{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		Entries: []WinnerIndexEntry{{
			EventID: "e1", Symbol: "BTCUSDT", SkyState: "s", SorterDomain: "d",
			Horizon: Horizon1h, Direction: "long", OutcomeClass: OutcomeWinner, NetOutcomeBPS: 99,
		}},
		IndexHash: "deadbeef",
	}
	if _, err := Derive(events, fake, baseBindings(), testSigningKey(), "test"); err == nil {
		t.Fatal("expected winner index validation failure")
	}
}

func TestWinnerIndexRejectsDuplicateOmitReorderAndTamper(t *testing.T) {
	var events []Event
	for i := 0; i < 3; i++ {
		id := eventID("BTCUSDT", int64(i+1), Horizon1h, mechanism.DirectionLong)
		event := makeEvent(id, "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{"symbol_return_1h": float64(i + 1)}, nil, 20, OutcomeWinner)
		event.DecisionTime = time.Unix(int64(i+1), 0).UTC()
		events = append(events, event)
	}
	canonical, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func([]WinnerIndexEntry) []WinnerIndexEntry
	}{
		{
			name: "duplicate",
			mutate: func(entries []WinnerIndexEntry) []WinnerIndexEntry {
				entries[1] = entries[0]
				return entries
			},
		},
		{
			name: "omit",
			mutate: func(entries []WinnerIndexEntry) []WinnerIndexEntry {
				return entries[:len(entries)-1]
			},
		},
		{
			name: "reorder",
			mutate: func(entries []WinnerIndexEntry) []WinnerIndexEntry {
				entries[0], entries[1] = entries[1], entries[0]
				return entries
			},
		},
		{
			name: "tamper",
			mutate: func(entries []WinnerIndexEntry) []WinnerIndexEntry {
				entries[0].NetOutcomeBPS++
				return entries
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := append([]WinnerIndexEntry(nil), canonical.Entries...)
			entries = tt.mutate(entries)
			hash, err := HashWinnerEntries(entries)
			if err != nil {
				t.Fatal(err)
			}
			supplied := WinnerIndex{
				Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
				Entries: entries, IndexHash: hash,
			}
			if err := validateWinnerIndex(events, supplied); err == nil {
				t.Fatalf("expected %s winner index rejection", tt.name)
			}
		})
	}

	badHash := canonical
	badHash.IndexHash = "self-hash-tampered"
	if err := validateWinnerIndex(events, badHash); err == nil {
		t.Fatal("expected entries self-hash mismatch rejection")
	}
}

func TestDualQuantilesUseCommonWinnerSamples(t *testing.T) {
	pairs := prioritizedDualPairs()
	if len(pairs) == 0 {
		t.Fatal("expected protocol-compatible dual pairs")
	}
	first, second := pairs[0].first, pairs[0].second
	winners := []Event{
		makeEvent("both-1", "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{first: 10, second: 100}, nil, 20, OutcomeWinner),
		makeEvent("first-only", "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{first: 1000}, []string{second}, 20, OutcomeWinner),
		makeEvent("second-only", "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{second: 2000}, []string{first}, 20, OutcomeWinner),
		makeEvent("both-2", "s", "d", Horizon1h, mechanism.DirectionLong,
			map[string]float64{first: 20, second: 200}, nil, 20, OutcomeWinner),
	}
	key := ruleGroupKey{
		SkyState: coarseTestSky(t, "s"), SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
	}
	axes, _ := parseSkyState(key.SkyState)
	key.ReturnBucket, key.VolBucket, key.FundingBucket = axes["ret"], axes["rv"], axes["fund"]
	index, err := BuildWinnerIndex(winners, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*Event, len(winners))
	for i := range winners {
		byID[winners[i].ID] = &winners[i]
	}
	rules, ledger, _, err := streamDerivation(
		[]ruleGroupKey{key}, index.Entries, byID, DeriveOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("expected streamed rules")
	}
	var firstPair *LedgerEntry
	for i := range ledger {
		entry := &ledger[i]
		if entry.RuleKind == "dual" && len(entry.Conditions) == 2 &&
			entry.Conditions[0].FeatureKey == first && entry.Conditions[1].FeatureKey == second &&
			entry.Conditions[0].Quantile == 0.25 && entry.Conditions[1].Quantile == 0.25 &&
			entry.Conditions[0].Op == OpLTE && entry.Conditions[1].Op == OpLTE {
			firstPair = entry
			break
		}
	}
	if firstPair == nil {
		t.Fatal("expected first frozen-order dual pair attempt")
	}
	if firstPair.AvailableFeatureSampleCount != 2 || firstPair.SourceWinnerCount != 2 {
		t.Fatalf("sample counts=%d/%d want 2 common winners",
			firstPair.AvailableFeatureSampleCount, firstPair.SourceWinnerCount)
	}
	wantHash, err := hashWinnerIDs([]string{"both-1", "both-2"})
	if err != nil {
		t.Fatal(err)
	}
	if firstPair.SourceWinnerIDsHash != wantHash || len(firstPair.SourceWinnerEventIDs) != 0 {
		t.Fatal("dual attempt must retain only compact common-sample provenance")
	}
	if got, want := firstPair.Conditions[0].Threshold, empiricalQuantile([]float64{10, 20}, 0.25); got != want {
		t.Fatalf("first threshold=%v want common-sample quantile %v", got, want)
	}
	if got, want := firstPair.Conditions[1].Threshold, empiricalQuantile([]float64{100, 200}, 0.25); got != want {
		t.Fatalf("second threshold=%v want common-sample quantile %v", got, want)
	}
}

func TestSearchManifestRejectsSourceDataHashDrift(t *testing.T) {
	ledger, err := SealLedger(AlphaLedger{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindings := baseBindings()
	bindings.SourceDataHash = "drifted-source-hash"
	bindings.InductionEventHash = "induction"
	bindings.WinnerIndexHash = "winners"
	if _, err := NewSearchManifest(nil, ledger, bindings); err == nil {
		t.Fatal("expected source data hash drift rejection")
	}

	validBindings := bindings
	validBindings.SourceDataHash = protocol.HistoryDataHash
	manifest, err := NewSearchManifest(nil, ledger, validBindings)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SourceDataHash = "drifted-source-hash"
	manifest, err = SealSearchManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("expected resealed source data hash drift rejection")
	}
}

func TestDualFiresRequiresBothFeatures(t *testing.T) {
	rule := Rule{
		SkyState: coarseTestSky(t, "s"), SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{
			{FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 0, Quantile: 0.5},
			{FeatureKey: "symbol_return_4h", Op: OpLTE, Threshold: 10, Quantile: 0.5},
		},
	}
	both := makeEvent("e", "s", "d", Horizon1h, mechanism.DirectionLong,
		map[string]float64{"symbol_return_1h": 1, "symbol_return_4h": 5}, nil, 1, OutcomeNeutral)
	fires, has := rule.Fires(both)
	if !fires || !has {
		t.Fatal("dual should fire when both present")
	}
	oneMissing := both
	oneMissing.Features = map[string]float64{"symbol_return_1h": 1}
	oneMissing.MissingFeatures = []string{"symbol_return_4h"}
	fires, has = rule.Fires(oneMissing)
	if fires || has {
		t.Fatalf("dual must not fire with missing second feature: fires=%v has=%v", fires, has)
	}
}

func TestCanonicalRuleHashStable(t *testing.T) {
	r := Rule{
		SkyState: coarseTestSky(t, "s"), SorterDomain: protocol.G3SorterDomains[0],
		Horizon: Horizon1h, Direction: mechanism.DirectionLong,
		Conditions: []RuleCondition{{FeatureKey: "symbol_return_1h", Op: OpLTE, Threshold: 1.25, Quantile: 0.5}},
	}
	a, err := CanonicalRuleHash(r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalRuleHash(r)
	if err != nil || a != b || a == "" {
		t.Fatalf("hash unstable: %q %q err=%v", a, b, err)
	}
	raw, _ := json.Marshal(r)
	var clone Rule
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	c, err := CanonicalRuleHash(clone)
	if err != nil || c != a {
		t.Fatal("canonical hash must ignore serialization noise")
	}
}
