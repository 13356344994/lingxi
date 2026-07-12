package brain

import (
	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/portfoliosim"
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
			return audit.TraceLog{}, nil, err
		}
		tagPath(out, pathTags)
	}
	_ = b.DiscardPending()
	return *b.Trace, pathTags, nil
}

// ReplayScenarioWithFunding replays with G5 funding between bars.
func ReplayScenarioWithFunding(sc FixtureScenario, funding portfoliosim.FundingBundle) (*Brain, map[string]bool, error) {
	b := NewBrain(sc.Config)
	pathTags := map[string]bool{}
	var prevBar *BarInput
	for i := range sc.Bars {
		bar := sc.Bars[i]
		if prevBar != nil {
			syms := b.OpenSymbols()
			b.ApplyHoldFunding(funding, syms, prevBar.DecisionTime, bar.DecisionTime, bar.CostStress)
		}
		out, err := b.DecideBar(bar)
		if err != nil {
			return nil, nil, err
		}
		tagPath(out, pathTags)
		cp := bar
		prevBar = &cp
	}
	_ = b.DiscardPending()
	return b, pathTags, nil
}

// TraceLog aliases audit trace for convenience.
type TraceLog = audit.TraceLog

func tagPath(out BarOutcome, tags map[string]bool) {
	for _, r := range out.Trace.RejectReasons {
		switch r {
		case counterfactual.RejectOOD:
			tags["data_distortion_no_trade"] = true
		case counterfactual.RejectMemoryVeto:
			tags["negative_memory_no_trade"] = true
		case RejectConcurrencyFull:
			tags["concurrency_full_reject"] = true
		case RejectBelowMinNotional, counterfactual.RejectMinLotInfeasible:
			tags["below_min_notional_skip"] = true
		}
	}
	if out.Trace.FinalAction == "NO_TRADE" && out.Trace.StopErrorState == StopErrorHolding {
		for _, row := range out.Trace.CounterfactualTable.Actions {
			for _, r := range row.RejectReasons {
				if r == counterfactual.RejectOOD {
					tags["data_distortion_no_trade"] = true
				}
			}
		}
	}
	if out.AppliedAction == "TRIM" || (out.SolvencyForced && out.AppliedAction == "CLOSE") {
		tags["solvency_trim"] = true
	}
	switch out.Trace.FinalAction {
	case "LONG":
		tags["continuation_long"] = true
	case "SHORT":
		tags["failed_breakout_short"] = true
	case "CLOSE":
		if out.StopErrorForced {
			tags["event_complete_close"] = true
		}
	case "TRIM":
		// tagged via SolvencyForced above
	}
}

func seedScenarioState(b *Brain, sc FixtureScenario) {
	switch sc.Name {
	case "event_complete_close":
		cand := makeEventCandidate(b.Config, completeEvent(1, 5), sc.Bars[0], "long", 0.5, 0.5, 50)
		b.Account.Slots[cand.EventID] = &eventaccount.EventSlot{
			Candidate: cand, Notional: 50, StressLoss: 0.25,
			ExecuteBar: 1, StopState: eventaccount.StopErrorHolding,
		}
		b.Account.SymbolEvents[cand.Symbol] = cand.EventID
		state := actionspace.PositionState{
			Side: actionspace.SideLong, SizeFraction: 50 / b.Config.InitialEquityUSDT,
			BasisPrice: 100, EquityUSDT: b.Config.InitialEquityUSDT,
		}
		b.Account.Core.Positions[cand.Symbol] = state
	case "concurrency_full_reject":
		cand := makeEventCandidate(b.Config, matchedContinuationEvent(sc.Bars[0].Features, 1), sc.Bars[0], "long", 0.5, 0.5, 10)
		b.Account.Slots[cand.EventID] = &eventaccount.EventSlot{
			Candidate: cand, Notional: 10, StressLoss: 0.15,
			ExecuteBar: 1, StopState: eventaccount.StopErrorHolding,
		}
		b.Account.SymbolEvents[cand.Symbol] = cand.EventID
		concurState := actionspace.PositionState{
			Side: actionspace.SideLong, SizeFraction: 10 / 30,
			BasisPrice: 100, EquityUSDT: 30,
		}
		b.Account.Core.Positions[cand.Symbol] = concurState
		b.Account.Core.EquityUSDT = 30
	case "solvency_trim":
		cand := makeEventCandidate(b.Config, MatchedEvent{EventID: "sol", EventFamily: "test"}, sc.Bars[0], "long", 0.1, 1.0, 25)
		cand.Symbol = sc.Bars[0].Features.Symbol
		b.Account.Slots["sol"] = &eventaccount.EventSlot{
			Candidate: cand, Notional: 25, StressLoss: 1.0,
			ExecuteBar: 0, StopState: eventaccount.StopErrorHolding,
		}
		b.Account.SymbolEvents[cand.Symbol] = "sol"
		b.Account.Core.CashUSDT = 5
		b.Account.Core.EquityUSDT = 30
		b.Account.Core.SolvencyBreach = true
		solState := actionspace.PositionState{
			Side: actionspace.SideLong, SizeFraction: 25 / 30,
			BasisPrice: 100, EquityUSDT: 30,
		}
		b.Account.Core.Positions[cand.Symbol] = solState
	}
}

// VerifyDeterministicReplay runs scenario twice and checks trace hashes match.
func VerifyDeterministicReplay(sc FixtureScenario) (audit.ReplayResult, error) {
	log1, _, err := ReplayScenario(sc)
	if err != nil {
		return audit.ReplayResult{}, err
	}
	log2, _, err := ReplayScenario(sc)
	if err != nil {
		return audit.ReplayResult{}, err
	}
	return audit.VerifyReplay(log1, log2), nil
}
