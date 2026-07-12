package brain

import (
	"encoding/json"
	"testing"

	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg6/eventaccount"
)

func TestEightActionsPerBar(t *testing.T) {
	sc := findScenario("continuation_long")
	b := NewBrain(sc.Config)
	out, err := b.DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Counterfactual.Table.Actions) != len(protocol.BrainActions) {
		t.Fatalf("actions=%d want %d", len(out.Counterfactual.Table.Actions), len(protocol.BrainActions))
	}
}

func TestPathMatrixDataDistortion(t *testing.T) {
	assertPathAction(t, "data_distortion_no_trade", "NO_TRADE")
}

func TestPathMatrixContinuationLong(t *testing.T) {
	assertPathAction(t, "continuation_long", "LONG")
}

func TestPathMatrixFailedBreakoutShort(t *testing.T) {
	assertPathAction(t, "failed_breakout_short", "SHORT")
}

func TestPathMatrixNegativeMemory(t *testing.T) {
	sc := findScenario("negative_memory_no_trade")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["negative_memory_no_trade"] {
		t.Fatal("missing negative_memory path tag")
	}
	log, _, _ := ReplayScenario(sc)
	last := log.Entries[len(log.Entries)-1]
	if last.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE got %s", last.FinalAction)
	}
}

func TestPathMatrixEventCompleteClose(t *testing.T) {
	sc := findScenario("event_complete_close")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["event_complete_close"] {
		t.Fatal("missing event_complete_close tag")
	}
	last := lastEntry(sc)
	if last.FinalAction != "CLOSE" {
		t.Fatalf("want CLOSE got %s", last.FinalAction)
	}
}

func TestPathMatrixSolvencyTrim(t *testing.T) {
	sc := findScenario("solvency_trim")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["solvency_trim"] {
		t.Fatal("missing solvency_trim tag")
	}
	action := lastEntry(sc).FinalAction
	if action != "TRIM" && action != "CLOSE" {
		t.Fatalf("want TRIM or CLOSE got %s", action)
	}
}

func TestPathMatrixConcurrencyFull(t *testing.T) {
	sc := findScenario("concurrency_full_reject")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["concurrency_full_reject"] {
		t.Fatal("missing concurrency_full_reject tag")
	}
}

func TestPathMatrixBelowMinNotional(t *testing.T) {
	sc := findScenario("below_min_notional_skip")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["below_min_notional_skip"] {
		t.Fatal("missing below_min_notional_skip tag")
	}
}

func TestStressSizingDownwardOnly(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	res := SizeEventNotional(acct, 5, 1, 100, 3600, 50, 1, 20, 0)
	if !res.Skipped {
		t.Fatal("expected skip below min notional")
	}
	res2 := SizeEventNotional(acct, 5, 1, 100, 3600, 5, 1, 20, 2)
	if res2.Skipped || res2.NotionalUSDT <= 0 {
		t.Fatal("expected sized notional with capacity clip")
	}
}

func TestThreeTierConcurrency(t *testing.T) {
	for _, tc := range []struct {
		equity float64
		want   int
	}{
		{30, 1}, {100, 2}, {1000, 3},
	} {
		if got := MaxConcurrent(tc.equity); got != tc.want {
			t.Fatalf("equity %v max concurrent=%d want %d", tc.equity, got, tc.want)
		}
	}
}

func TestLeverageCapFive(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	res := SizeEventNotional(acct, 10, 1, 100, 3600, 1, 1, 0, 0)
	if !res.Skipped && res.NotionalUSDT > 100*float64(MaxAccountLeverage) {
		t.Fatalf("notional %v exceeds leverage headroom", res.NotionalUSDT)
	}
}

func TestSolvencyIndependentOfPlaybook(t *testing.T) {
	sc := findScenario("solvency_trim")
	b := NewBrain(sc.Config)
	seedScenarioState(b, sc)
	out, err := b.DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if !out.SolvencyForced {
		t.Fatal("solvency must force action")
	}
	if out.AppliedAction != "TRIM" && out.AppliedAction != "CLOSE" {
		t.Fatalf("solvency action=%s", out.AppliedAction)
	}
}

func TestStopErrorTriggers(t *testing.T) {
	sc := findScenario("event_complete_close")
	b := NewBrain(sc.Config)
	seedScenarioState(b, sc)
	out, err := b.DecideBar(sc.Bars[len(sc.Bars)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !out.StopErrorForced {
		t.Fatal("stop error must force close")
	}
	if out.AppliedAction != "CLOSE" {
		t.Fatalf("want CLOSE got %s", out.AppliedAction)
	}
}

func TestBrainTraceDeterministicHash(t *testing.T) {
	sc := findScenario("continuation_long")
	res, err := VerifyDeterministicReplay(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Match {
		t.Fatalf("replay hash mismatch at %d", res.FirstMismatchAt)
	}
}

func TestBrainTraceRequiredFields(t *testing.T) {
	sc := findScenario("continuation_long")
	log, _, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	entry := log.Entries[0]
	for _, field := range []string{
		entry.StateHash, entry.EventFamily, entry.FingerprintVersion,
		entry.CounterfactualTable.TableHash, entry.FinalAction,
		entry.StopErrorState, entry.SolvencyState, entry.TraceHash,
	} {
		if field == "" {
			t.Fatal("missing required trace field")
		}
	}
	if err := audit.ValidateEntry(entry); err != nil {
		t.Fatal(err)
	}
}

func TestStateHashExcludesOutcomeLabels(t *testing.T) {
	sc := findScenario("continuation_long")
	log, _, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	entry := log.Entries[0]
	for _, hit := range entry.MemoryHits {
		raw, _ := json.Marshal(hit)
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		for _, forbidden := range []string{"outcome_win", "conservative_net_bps", "win", "outcome"} {
			if _, ok := m[forbidden]; ok {
				t.Fatalf("memory hit must not contain %s", forbidden)
			}
		}
	}
	h1, _ := audit.HashState(entry)
	if h1 == "" {
		t.Fatal("state hash required")
	}
}

func TestStopErrorNotOverriddenByCounterfactual(t *testing.T) {
	sc := findScenario("event_complete_close")
	b := NewBrain(sc.Config)
	seedScenarioState(b, sc)
	out, err := b.DecideBar(sc.Bars[len(sc.Bars)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !out.StopErrorForced {
		t.Fatal("stop error must force")
	}
	if out.AppliedAction != "CLOSE" {
		t.Fatalf("stop error must close, got %s", out.AppliedAction)
	}
	if out.Counterfactual.FinalAction == "LONG" || out.Counterfactual.FinalAction == "SHORT" {
		// cf table may still score opens, but applied must not follow them
	}
	if out.AppliedAction != "CLOSE" {
		t.Fatalf("cf must not override stop error, got %s", out.AppliedAction)
	}
}

func TestSolvencyWarningBlocksNewOpen(t *testing.T) {
	sc := findScenario("continuation_long")
	b := NewBrain(sc.Config)
	b.Account.Slots["warn"] = b.Account.Slots["sol"]
	b.Account.Core.EquityUSDT = 100
	b.Account.Slots["warn"] = &eventaccount.EventSlot{
		Candidate: eventaccount.EventCandidate{EventID: "warn", Symbol: sc.Bars[0].Features.Symbol},
		Notional: 20, StressLoss: 1.6, ExecuteBar: 0, StopState: eventaccount.StopErrorHolding,
	}
	b.Account.SymbolEvents[sc.Bars[0].Features.Symbol] = "warn"
	out, err := b.DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	sol := EvaluateSolvency(b.Account)
	if sol.State == SolvencyStateWarning && out.AppliedAction == "LONG" {
		t.Fatalf("solvency warning must block new open, got %s", out.AppliedAction)
	}
}

func TestPickPrimaryEventUsesPersonalRank(t *testing.T) {
	cfg := defaultFixtureConfig()
	b := NewBrain(cfg)
	in := BuildDefaultFixtures().Scenarios[0].Bars[0]
	highGross := MatchedEvent{EventID: "gross", EventFamily: "ohlcv_extreme_continuation", GrossLongBPS: 150, GrossShortBPS: -10, CompletionBars: 48}
	lowGrossBetterNet := MatchedEvent{EventID: "personal", EventFamily: "ohlcv_extreme_continuation", GrossLongBPS: 76, GrossShortBPS: -5, CompletionBars: 1, CapacityUSDT: 500}
	picked := pickPrimaryEvent(b, in, []MatchedEvent{highGross, lowGrossBetterNet})
	if picked.EventID != "personal" {
		t.Fatalf("expected personal-ranked event, got %s", picked.EventID)
	}
}

func TestUnknownTriggerFailClosed(t *testing.T) {
	feat := BarFeatures{ReturnBPS: 50, PriorReturnBPS: 40, BarRangeBPS: 45, Price: 100}
	spec := playbook.TriggerSpec{Conditions: []playbook.TriggerCondition{{Field: "unknown_feature", Op: "gte", Value: 1}}}
	if triggerFires(spec, feat) {
		t.Fatal("unknown trigger field must fail closed")
	}
}

func TestFixtureUsesKnownFingerprint(t *testing.T) {
	cfg := defaultFixtureConfig()
	if cfg.Fingerprint.FingerprintHash == "" || cfg.PersonalAdj.CriticalUnknown {
		t.Fatal("fixture must bind explicit known fingerprint")
	}
}

func TestDegeneracyHealthyFixture(t *testing.T) {
	bundle := BuildDefaultFixtures()
	pathTags := map[string]bool{}
	for _, sc := range bundle.Scenarios {
		_, tags, err := ReplayScenario(sc)
		if err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		for k, v := range tags {
			pathTags[k] = pathTags[k] || v
		}
	}
	for _, path := range PathMatrixRequired {
		if !pathTags[path] {
			t.Fatalf("path matrix missing %s", path)
		}
	}
}

func TestFixtureBundleDeterministic(t *testing.T) {
	b1 := BuildDefaultFixtures()
	b2 := BuildDefaultFixtures()
	if b1.BundleHash != b2.BundleHash {
		t.Fatal("fixture bundle hash drift")
	}
}

func TestCounterfactualIntegrationBreakeven(t *testing.T) {
	sc := findScenario("data_distortion_no_trade")
	ctx, _ := NewBrain(sc.Config).buildDecisionContext(sc.Bars[0], sc.Bars[0].Events[0], SolvencyDecision{State: SolvencyStateOK})
	dec, err := counterfactual.Decide(ctx, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if dec.FinalAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE got %s", dec.FinalAction)
	}
}

func assertPathAction(t *testing.T, name, want string) {
	t.Helper()
	sc := findScenario(name)
	log, _, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if log.Entries[0].FinalAction != want {
		t.Fatalf("%s: want %s got %s rejects=%v", name, want, log.Entries[0].FinalAction, log.Entries[0].RejectReasons)
	}
}

func findScenario(name string) FixtureScenario {
	for _, sc := range BuildDefaultFixtures().Scenarios {
		if sc.Name == name {
			return sc
		}
	}
	panic("scenario not found: " + name)
}

func lastEntry(sc FixtureScenario) audit.BrainTraceEntry {
	log, _, _ := ReplayScenario(sc)
	return log.Entries[len(log.Entries)-1]
}
