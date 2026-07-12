package personalbrain

import (
	"lingxi/internal/routerg10/executionpolicy"
)

// CompareExecutionActions freezes BOUNDED_MAKER / CONDITIONAL_MARKETABLE_LIMIT / SKIP
// at decision time using only decision-visible inputs — never realized outcome or
// pre-selected FrozenExecutionAction from the tape.
func CompareExecutionActions(ev MatchedPersonalEvent, in BarInput) ExecutionDecision {
	tr := executionTriggerFromBar(ev, in)
	actions := []executionpolicy.ActionEvaluation{
		executionpolicy.FreezeBoundedMaker(tr),
		executionpolicy.FreezeConditionalMarketableLimit(tr),
		executionpolicy.FreezeSkip(),
	}
	gate := executionpolicy.MakerFillSelectionGate{ForbidMaker: ev.Proofs.MakerGateForbid}
	if gate.ForbidMaker {
		for i := range actions {
			if actions[i].Action == ActionBoundedMaker {
				actions[i].Allowed = false
				actions[i].BlockReason = RejectMakerGateForbid
			}
		}
	}
	selected := executionpolicy.SelectFrozenAction(actions, gate)
	var rejects []string
	if !actionAllowed(actions, selected) {
		rejects = append(rejects, RejectExecutionNotAllowed)
		selected = ActionSkip
	}
	if selected == ActionBoundedMaker && gate.ForbidMaker {
		rejects = append(rejects, RejectMakerGateForbid)
		selected = ActionSkip
	}
	return ExecutionDecision{
		ActionsCompared: actions,
		SelectedAction:  selected,
		RejectReasons:   rejects,
	}
}

func executionTriggerFromBar(ev MatchedPersonalEvent, in BarInput) executionpolicy.TriggerRecord {
	sim := in.ExecutionSim
	side := ev.Direction
	if side == "" {
		side = "long"
	}
	tr := executionpolicy.TriggerRecord{
		TriggerID:              ev.EventID,
		DecisionTime:           in.DecisionTime,
		Symbol:                 in.Features.Symbol,
		Side:                   side,
		Price:                  in.Features.Price,
		MakerTouched:           sim.MakerTouchedNotFilled,
		MakerFilled:            false,
		SubsequentlyProfitable: false,
		PartialFill:            sim.PartialFill,
		RemainingEdgeBPS:       sim.RemainingEdgeBPS,
		SpreadBPS:              sim.SpreadBPS,
		FeeBPS:                 sim.FeeBPS,
		LatencyMS:              sim.LatencyMS,
		MaxPriceCap:            sim.MaxPriceCap,
		MaxSlippageBPS:         sim.MaxSlippageBPS,
		MinRemainingEdgeBPS:    sim.MinRemainingEdgeBPS,
		TimeoutMS:              sim.TimeoutMS,
		ToxicFastFullFill:      sim.ToxicFastFullFill,
		ToxicThroughPrice:      sim.ToxicThroughPrice,
		ToxicBookSyncBad:       sim.ToxicBookSyncBad,
		ThesisStillValid:       sim.ThesisStillValid || ev.ThesisHealth != ThesisInvalidated,
	}
	if tr.SpreadBPS == 0 {
		tr.SpreadBPS = 3
	}
	if tr.FeeBPS == 0 {
		tr.FeeBPS = 2
	}
	if tr.TimeoutMS == 0 {
		tr.TimeoutMS = 3000
	}
	if tr.RemainingEdgeBPS == 0 {
		tr.RemainingEdgeBPS = 20
	}
	if tr.MinRemainingEdgeBPS == 0 {
		tr.MinRemainingEdgeBPS = 5
	}
	return tr
}

func actionAllowed(actions []executionpolicy.ActionEvaluation, selected string) bool {
	for _, a := range actions {
		if a.Action == selected {
			return a.Allowed
		}
	}
	return false
}

// MapExecutionToPositionAction maps frozen execution to brain position action.
// SKIP and blocked paths become NO_TRADE; maker/cml map to LONG/SHORT by direction.
func MapExecutionToPositionAction(exec ExecutionDecision, ev MatchedPersonalEvent, enter EnterGateResult) (string, string) {
	if enter.Status == EnterCannotEnter {
		return "NO_TRADE", ""
	}
	if enter.Status == EnterMissedCorrectly {
		return "NO_TRADE", MissedProfitableMoveStatus
	}
	switch exec.SelectedAction {
	case ActionSkip:
		return "NO_TRADE", "EXECUTION_SKIP"
	case ActionBoundedMaker, ActionConditionalMarketableLimit:
		dir := ev.Direction
		if dir == "short" {
			return "SHORT", ""
		}
		return "LONG", ""
	default:
		return "NO_TRADE", RejectExecutionNotAllowed
	}
}

// ProcessMakerLifecycle handles bounded maker timeout cancel; unexecuted profit is zero.
func (b *Brain) ProcessMakerLifecycle(in BarInput) (canceled bool, reason string) {
	if b.MakerOrder == nil {
		return false, ""
	}
	elapsed := in.BarIndex - b.MakerOrder.PlacedBar
	if b.MakerOrder.TimeoutBars > 0 && elapsed >= b.MakerOrder.TimeoutBars {
		if !in.ExecutionSim.MakerFilled {
			b.MakerOrder = nil
			return true, "MAKER_TIMEOUT_CANCEL"
		}
	}
	if in.ExecutionSim.MakerFilled && b.MakerOrder != nil {
		b.MakerOrder = nil
	}
	return false, ""
}

// PlaceMakerOrder records a bounded maker attempt with timeout.
func (b *Brain) PlaceMakerOrder(ev MatchedPersonalEvent, in BarInput, notional float64) {
	timeout := in.ExecutionSim.MakerTimeoutBars
	if timeout <= 0 {
		timeout = 2
	}
	b.MakerOrder = &MakerOrderState{
		EventID:      ev.EventID,
		Symbol:       in.Features.Symbol,
		Direction:    ev.Direction,
		NotionalUSDT: notional,
		PlacedBar:    in.BarIndex,
		TimeoutBars:  timeout,
		FrozenAction: ActionBoundedMaker,
	}
}
