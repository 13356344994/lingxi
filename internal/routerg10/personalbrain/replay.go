package personalbrain

import (
	"lingxi/internal/routerg6/eventaccount"
)

// ReplayScenario replays scripted bars and returns trace log plus path coverage.
func ReplayScenario(sc FixtureScenario) (TraceLog, map[string]bool, error) {
	b := NewBrain(sc.Config)
	seedScenarioState(b, sc)
	pathTags := map[string]bool{}
	for _, bar := range sc.Bars {
		out, err := b.DecideBar(bar)
		if err != nil {
			return TraceLog{}, nil, err
		}
		tagPath(out, sc.Name, pathTags)
	}
	_ = b.DiscardPending()
	return *b.Trace, pathTags, nil
}

func tagPath(out BarOutcome, scenarioName string, tags map[string]bool) {
	if scenarioName != "" {
		tags[scenarioName] = true
	}
	for _, r := range out.Trace.RejectReasons {
		switch r {
		case RejectCrowdingStop:
			tags["crowding_stop"] = true
		case RejectCapitalExpansionStop:
			tags["capital_expansion_stop"] = true
		case RejectBelowMinNotional:
			tags["below_min_notional_skip"] = true
		case RejectNoChaseAfterMiss, MissedProfitableMoveStatus:
			tags["no_chase_after_miss"] = true
		case RejectTerminalRiskCeiling:
			tags["terminal_risk_sizing_down"] = true
		}
	}
	if out.Trace.EnterGate == EnterCanEnter && out.Execution.SelectedAction == ActionBoundedMaker {
		tags["can_enter_bounded_maker_long"] = true
	}
	if out.Trace.EnterGate == EnterCannotEnter {
		for _, r := range out.Trace.RejectReasons {
			if r == RejectEntryUnproven || r == EntryUnprovenStatus || r == "PLAYBOOK_NOT_CERTIFIED" {
				tags["cannot_enter_unproven"] = true
			}
		}
	}
	if out.Trace.EnterGate == EnterMissedCorrectly {
		tags["missed_correctly_maker_timeout"] = true
	}
	if out.Execution.SelectedAction == ActionConditionalMarketableLimit && out.Trace.EnterGate == EnterCanEnter {
		tags["conditional_marketable_limit_long"] = true
	}
	if out.Execution.SelectedAction == ActionSkip && out.Trace.EnterGate == EnterCanEnter {
		tags["execution_skip"] = true
	}
	if out.Trace.ToxicFillReason == ToxicFillRevalidateStatus {
		tags["toxic_fill_revalidate"] = true
	}
	if out.Trace.ToxicFillReason == ToxicFillExitStatus || out.AppliedAction == "CLOSE" && out.Trace.ToxicFillReason != "" {
		tags["toxic_fill_exit"] = true
	}
	if out.StopErrorForced && out.Trace.RejectReasons != nil {
		for _, r := range out.Trace.RejectReasons {
			if r == "thesis_health_fail" {
				tags["thesis_invalid_exit"] = true
			}
		}
	}
	if out.AppliedAction == "CLOSE" {
		for _, r := range append(out.Trace.RejectReasons, out.FillOrMissReason) {
			if r == "thesis_health_fail" {
				tags["thesis_invalid_exit"] = true
			}
		}
	}
	if out.SolvencyForced && (out.AppliedAction == "TRIM" || out.AppliedAction == "CLOSE") {
		tags["solvency_trim_independent"] = true
	}
	if out.StopErrorForced {
		for _, r := range out.Trace.RejectReasons {
			if r == RejectStopErrorForced {
				tags["deadline_stop_error_exit"] = true
			}
		}
	}
	if out.Trace.AnnouncementVisibility.VisibleCount > 0 {
		tags["announcement_pit_visible"] = true
	}
}

func seedScenarioState(b *Brain, sc FixtureScenario) {
	switch sc.Name {
	case "deadline_stop_error_exit", "thesis_invalid_exit", "toxic_fill_exit":
		if len(sc.Bars) == 0 {
			return
		}
		ev := sc.Bars[0].Events[0]
		cand := makeEventCandidate(b.Config, ev, sc.Bars[0], ev.Direction, 0.5, 0.5, 50)
		b.Account.Slots[cand.EventID] = &eventaccount.EventSlot{
			Candidate: cand, Notional: 50, StressLoss: 0.25,
			ExecuteBar: 1, StopState: eventaccount.StopErrorHolding,
		}
		b.Account.SymbolEvents[cand.Symbol] = cand.EventID
	case "solvency_trim_independent":
		if len(sc.Bars) == 0 {
			return
		}
		ev := MatchedPersonalEvent{EventID: "sol", EventFamily: "test"}
		cand := makeEventCandidate(b.Config, ev, sc.Bars[0], "long", 0.1, 1.0, 25)
		cand.Symbol = sc.Bars[0].Features.Symbol
		b.Account.Slots["sol"] = &eventaccount.EventSlot{
			Candidate: cand, Notional: 25, StressLoss: 1.0,
			ExecuteBar: 0, StopState: eventaccount.StopErrorHolding,
		}
		b.Account.SymbolEvents[cand.Symbol] = "sol"
		b.Account.Core.CashUSDT = 5
		b.Account.Core.EquityUSDT = 30
		b.Account.Core.SolvencyBreach = true
	}
}

// VerifyDeterministicReplay runs scenario twice and checks trace hashes match.
func VerifyDeterministicReplay(sc FixtureScenario) (ReplayResult, error) {
	log1, _, err := ReplayScenario(sc)
	if err != nil {
		return ReplayResult{}, err
	}
	log2, _, err := ReplayScenario(sc)
	if err != nil {
		return ReplayResult{}, err
	}
	return VerifyReplay(log1, log2), nil
}
