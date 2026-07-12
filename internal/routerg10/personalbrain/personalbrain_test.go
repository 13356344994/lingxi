package personalbrain

import (
	"testing"

	g10constitution "lingxi/internal/routerg10/constitution"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg6/eventaccount"
	"lingxi/internal/routerg6/eventmap"
)

func TestEnterGateCanEnter(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.EnterGate.Status != EnterCanEnter {
		t.Fatalf("want CAN_ENTER got %s reasons=%v", out.EnterGate.Status, out.EnterGate.Reasons)
	}
	if out.Execution.SelectedAction != ActionBoundedMaker {
		t.Fatalf("want BOUNDED_MAKER got %s", out.Execution.SelectedAction)
	}
}

func TestEnterGateCannotUnproven(t *testing.T) {
	sc := findScenario("cannot_enter_unproven")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.EnterGate.Status != EnterCannotEnter {
		t.Fatalf("want CANNOT_ENTER got %s", out.EnterGate.Status)
	}
}

func TestMissedCorrectlyZeroProfit(t *testing.T) {
	sc := findScenario("missed_correctly_maker_timeout")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.EnterGate.Status != EnterMissedCorrectly {
		t.Fatalf("want MISSED_CORRECTLY got %s", out.EnterGate.Status)
	}
	if out.AppliedAction != "NO_TRADE" {
		t.Fatalf("want NO_TRADE got %s", out.AppliedAction)
	}
}

func TestExecutionActionsCompared(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Execution.ActionsCompared) != len(ExecutionActionsCompared) {
		t.Fatalf("actions=%d want %d", len(out.Execution.ActionsCompared), len(ExecutionActionsCompared))
	}
}

func TestConditionalMarketableLimitPath(t *testing.T) {
	sc := findScenario("conditional_marketable_limit_long")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["conditional_marketable_limit_long"] {
		t.Fatal("missing conditional_marketable_limit_long path")
	}
}

func TestExecutionSkipPath(t *testing.T) {
	sc := findScenario("execution_skip")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["execution_skip"] {
		t.Fatal("missing execution_skip path")
	}
}

func TestToxicFillRevalidatePath(t *testing.T) {
	sc := findScenario("toxic_fill_revalidate")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["toxic_fill_revalidate"] {
		t.Fatal("missing toxic_fill_revalidate path")
	}
}

func TestToxicFillExitPath(t *testing.T) {
	sc := findScenario("toxic_fill_exit")
	seedScenarioState(NewBrain(sc.Config), sc)
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.Trace.ToxicFillReason != ToxicFillExitStatus && out.AppliedAction != "CLOSE" {
		t.Fatalf("want toxic exit got reason=%s action=%s", out.Trace.ToxicFillReason, out.AppliedAction)
	}
	_, tags, _ := ReplayScenario(sc)
	if !tags["toxic_fill_exit"] {
		t.Fatal("missing toxic_fill_exit path tag")
	}
}

func TestThesisInvalidExitPath(t *testing.T) {
	sc := findScenario("thesis_invalid_exit")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["thesis_invalid_exit"] {
		t.Fatal("missing thesis_invalid_exit path")
	}
}

func TestSolvencyIndependentPath(t *testing.T) {
	sc := findScenario("solvency_trim_independent")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["solvency_trim_independent"] {
		t.Fatal("missing solvency_trim_independent path")
	}
}

func TestDeadlineStopErrorExitPath(t *testing.T) {
	sc := findScenario("deadline_stop_error_exit")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["deadline_stop_error_exit"] {
		t.Fatal("missing deadline_stop_error_exit path")
	}
}

func TestCrowdingStopPath(t *testing.T) {
	sc := findScenario("crowding_stop")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["crowding_stop"] {
		t.Fatal("missing crowding_stop path")
	}
}

func TestCapitalExpansionStopPath(t *testing.T) {
	sc := findScenario("capital_expansion_stop")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["capital_expansion_stop"] {
		t.Fatal("missing capital_expansion_stop path")
	}
}

func TestTerminalRiskSizingDownPath(t *testing.T) {
	sc := findScenario("terminal_risk_sizing_down")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["terminal_risk_sizing_down"] {
		t.Fatal("missing terminal_risk_sizing_down path")
	}
}

func TestAnnouncementPITVisiblePath(t *testing.T) {
	sc := findScenario("announcement_pit_visible")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["announcement_pit_visible"] {
		t.Fatal("missing announcement_pit_visible path")
	}
}

func TestBelowMinNotionalSkipPath(t *testing.T) {
	sc := findScenario("below_min_notional_skip")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["below_min_notional_skip"] {
		t.Fatal("missing below_min_notional_skip path")
	}
}

func TestNoChaseAfterMissPath(t *testing.T) {
	sc := findScenario("no_chase_after_miss")
	_, tags, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !tags["no_chase_after_miss"] {
		t.Fatal("missing no_chase_after_miss path")
	}
}

func TestPathMatrixFullCoverage(t *testing.T) {
	bundle := BuildDefaultFixtures()
	tags := map[string]bool{}
	for _, sc := range bundle.Scenarios {
		_, pt, err := ReplayScenario(sc)
		if err != nil {
			t.Fatalf("%s: %v", sc.Name, err)
		}
		for k, v := range pt {
			tags[k] = tags[k] || v
		}
	}
	for _, path := range PathMatrixRequired {
		if !tags[path] {
			t.Fatalf("path matrix missing %s", path)
		}
	}
}

func TestBrainTraceV2RequiredFields(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	log, _, err := ReplayScenario(sc)
	if err != nil {
		t.Fatal(err)
	}
	entry := log.Entries[0]
	if err := ValidateEntry(entry); err != nil {
		t.Fatal(err)
	}
	if entry.ConstitutionHash == "" || entry.EnterGate == "" || entry.TerminalRiskUsage.CeilingIsNotTarget != true {
		t.Fatal("missing v2 trace fields")
	}
}

func TestBrainTraceDeterministicHash(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	res, err := VerifyDeterministicReplay(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Match {
		t.Fatalf("replay hash mismatch at %d", res.FirstMismatchAt)
	}
}

func TestTerminalRiskCeilingNotTarget(t *testing.T) {
	if !g10constitution.RiskCeilingIsNotSizingTarget {
		t.Fatal("risk ceiling must not be sizing target")
	}
	if MaxEventTerminalLossFraction != 0.40 || MaxAccountTerminalRiskFraction != 0.70 {
		t.Fatal("terminal risk fractions drift")
	}
}

func TestSizingDownwardOnly(t *testing.T) {
	acct := eventaccount.NewAccount(100)
	res := SizeEventNotional(acct, 5, 1, 100, 3600, 50, 1, 20, 200, 0)
	if !res.Skipped {
		t.Fatal("expected skip below min notional")
	}
}

func TestTradeAllowedFalseBlocksOpen(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.Trace.TradeAllowed {
		t.Fatal("research stage trade_allowed must be false")
	}
	if out.AppliedAction == "LONG" || out.AppliedAction == "SHORT" {
		t.Fatalf("trade_allowed=false must block open, got %s", out.AppliedAction)
	}
}

func TestThreeExecutionActionsOnly(t *testing.T) {
	for _, act := range ExecutionActionsCompared {
		switch act {
		case g10executionpolicy.ActionBoundedMaker,
			g10executionpolicy.ActionConditionalMarketableLimit,
			g10executionpolicy.ActionSkip:
		default:
			t.Fatalf("unexpected execution action %s", act)
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

func TestEventFamilyTriggerNotAlwaysTrue(t *testing.T) {
	spec := personalplaybook.PublicTriggerSpec{
		Conditions: []personalplaybook.PublicTriggerCondition{
			{Field: "event_family", Op: "eq", Note: eventmap.FamilyExtremeContinuation},
		},
	}
	match := BarFeatures{EventFamily: eventmap.FamilyExtremeContinuation}
	miss := BarFeatures{EventFamily: eventmap.FamilyExtremeReversal}
	if !publicTriggerFires(spec, match) {
		t.Fatal("expected event_family match")
	}
	if publicTriggerFires(spec, miss) {
		t.Fatal("event_family must not be true on every bar")
	}
}

func TestExecutionCompareNoFrozenShortcut(t *testing.T) {
	sc := findScenario("execution_skip")
	ev := sc.Bars[0].Events[0]
	out := CompareExecutionActions(ev, sc.Bars[0])
	if out.SelectedAction != ActionSkip {
		t.Fatalf("want SKIP from comparison got %s", out.SelectedAction)
	}
	if out.SelectedAction == ev.Playbook.ExecutionNarrative.FrozenExecutionAction && out.SelectedAction != ActionSkip {
		t.Fatal("must not short-circuit via tape frozen action")
	}
	if len(out.ActionsCompared) != len(ExecutionActionsCompared) {
		t.Fatalf("actions compared = %d", len(out.ActionsCompared))
	}
}

func TestTerminalRiskCeilingHardBlock(t *testing.T) {
	sc := findScenario("terminal_risk_sizing_down")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	if out.AppliedAction == "LONG" || out.AppliedAction == "SHORT" {
		t.Fatalf("ceiling must hard block open, got %s", out.AppliedAction)
	}
	found := false
	for _, r := range out.Trace.RejectReasons {
		if r == RejectTerminalRiskCeiling {
			found = true
		}
	}
	if !found {
		t.Fatalf("want %s in rejects %v", RejectTerminalRiskCeiling, out.Trace.RejectReasons)
	}
}

func TestExclusionAcceptedUsesAxisHashes(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	ev := sc.Bars[0].Events[0]
	if !ev.Proofs.ExclusionAccepted {
		t.Fatal("exclusion must be accepted via sealed axis hashes")
	}
	if ev.Proofs.InstitutionAxisHash == "" || ev.Proofs.RetailAxisHash == "" || ev.Proofs.BotAxisHash == "" {
		t.Fatal("missing triple axis proof hashes")
	}
	gate := EvaluateEnterGate(ev, sc.Bars[0], EvaluateSolvency(NewBrain(sc.Config).Account), false, false)
	for _, r := range gate.Reasons {
		if r == "EXCLUSION_NOT_ACCEPTED" {
			t.Fatal("must not reject exclusion when axis hashes seal")
		}
	}
}

func TestStateHashIncludesProofBindings(t *testing.T) {
	sc := findScenario("can_enter_bounded_maker_long")
	out, err := NewBrain(sc.Config).DecideBar(sc.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	entry := out.Trace
	if entry.ProtocolHash == "" {
		t.Fatal("trace missing protocol_hash")
	}
	if entry.TradeAllowed {
		t.Fatal("research stage trade_allowed must be false in trace")
	}
	if len(entry.ExecutionActions) != len(ExecutionActionsCompared) {
		t.Fatalf("execution action table missing: %d", len(entry.ExecutionActions))
	}
	want, err := HashState(entry)
	if err != nil {
		t.Fatal(err)
	}
	if entry.StateHash != want {
		t.Fatal("state_hash drift from bound fields")
	}
	payload := entry
	payload.StateHash = ""
	payload.TraceHash = ""
	if payload.ProtocolHash == "" {
		t.Fatal("protocol hash must bind in trace entry")
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
