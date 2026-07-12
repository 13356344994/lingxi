package personalbrain

import (
	"lingxi/internal/routerg6/eventaccount"
)

// DecideBar executes one bar: MTM, pending flush, StopError, solvency, enter gate,
// frozen execution comparison, position apply, BrainTrace v2 append.
func (b *Brain) DecideBar(in BarInput) (BarOutcome, error) {
	b.Account.MarkToMarket(in.Prices)
	matured := b.Pending.FlushMatured(in.BarIndex)
	_ = flushPending(b, matured, in, entryPriceAt(in))
	b.Account.MarkToMarket(in.Prices)

	_, makerCancelReason := b.ProcessMakerLifecycle(in)
	toxicForced, toxicReason := b.ApplyToxicFill(in, pickPrimaryEvent(in.Events))
	stopForced, _ := processStopErrors(b, in)
	sol := EvaluateSolvency(b.Account)
	solForced := false
	if SolvencyOverridesPlaybook(sol) {
		if err := applySolvencyAction(b, sol, in); err == nil {
			solForced = true
		}
	}

	primary := pickPrimaryEvent(in.Events)
	hasPos := hasPosition(b, in.Features.Symbol)
	enter := EvaluateEnterGate(primary, in, sol, hasPos, b.ToxicBlocked)
	var rejects []string
	if solForced {
		rejects = appendUnique(rejects, RejectSolvencyForced)
	}
	if stopForced {
		rejects = appendUnique(rejects, RejectStopErrorForced)
	}
	if toxicForced {
		rejects = appendUnique(rejects, toxicReason)
	}
	if makerCancelReason != "" {
		rejects = appendUnique(rejects, makerCancelReason)
	}
	rejects = appendUnique(rejects, enter.Reasons...)

	exec := ExecutionDecision{SelectedAction: ActionSkip}
	fillOrMiss := ""
	applied := "HOLD"
	posSize := positionSize(b, in.Features.Symbol)
	eventStress := 0.0

	if stopForced || solForced || toxicForced && toxicReason == ToxicFillExitStatus {
		if stopForced {
			applied = "CLOSE"
		} else if solForced && sol.Action != "" {
			applied = sol.Action
		} else if toxicReason == ToxicFillExitStatus {
			applied = "CLOSE"
		}
	} else if primary.EventID != "" {
		exec = CompareExecutionActions(primary, in)
		posAction, missReason := MapExecutionToPositionAction(exec, primary, enter)
		fillOrMiss = missReason

		switch enter.Status {
		case EnterCanEnter:
			openCount := len(b.Account.OpenSlots())
			maxC := MaxConcurrent(b.Config.InitialEquityUSDT)
			if openCount >= maxC && !hasPos {
				applied = "NO_TRADE"
				rejects = appendUnique(rejects, RejectConcurrencyFull)
			} else if b.ToxicBlocksAdd() && (posAction == "LONG" || posAction == "SHORT") && hasPos {
				applied = "HOLD"
				rejects = appendUnique(rejects, RejectToxicTopUpForbidden)
			} else if posAction == "LONG" || posAction == "SHORT" {
				applied, posSize, eventStress, rejects = b.applyOpen(primary, in, posAction, exec, rejects)
			} else {
				applied = posAction
			}
		case EnterMissedCorrectly:
			applied = "NO_TRADE"
			if fillOrMiss == "" {
				fillOrMiss = MissedProfitableMoveStatus
			}
		default:
			applied = "NO_TRADE"
		}
	} else if hasPos {
		if primary.ThesisHealth == ThesisInvalidated {
			applyClose(b, in.Features.Symbol, in, "thesis_health_fail")
			applied = "CLOSE"
			posSize = 0
		} else {
			applied = "HOLD"
		}
	} else {
		applied = "NO_TRADE"
	}

	annVis := VisibleAnnouncementSnapshot(in.Announcements, EventDecisionTime(in.DecisionTime), primary.Proofs.AnnouncementTapeHash)
	termRisk := ComputeTerminalRiskUsage(b.Account, eventStress)
	if eventStress > 0 {
		termRisk.EventOccupancyPct = eventStress / termRisk.EventTerminalBudgetUSDT * 100
	}

	trace, err := b.appendTrace(in, primary, enter, exec, applied, posSize, sol, stopForced, fillOrMiss, toxicReason, annVis, termRisk, rejects)
	if err != nil {
		return BarOutcome{}, err
	}
	return BarOutcome{
		Trace:            trace,
		EnterGate:        enter,
		Execution:        exec,
		AppliedAction:    applied,
		PositionSize:     posSize,
		SolvencyForced:   solForced,
		StopErrorForced:  stopForced,
		FillOrMissReason: fillOrMiss,
	}, nil
}

func (b *Brain) applyOpen(
	ev MatchedPersonalEvent,
	in BarInput,
	posAction string,
	exec ExecutionDecision,
	rejects []string,
) (string, float64, float64, []string) {
	dir := "long"
	if posAction == "SHORT" {
		dir = "short"
	}
	edge := ev.GrossLongBPS * b.Account.Core.EquityUSDT / 10000
	stress := edge * 0.1
	if dir == "short" {
		edge = ev.GrossShortBPS * b.Account.Core.EquityUSDT / 10000
		if edge < 0 {
			edge = -edge
		}
	}
	minNotional := ev.Proofs.DynamicMinNotional
	if minNotional <= 0 {
		minNotional = b.Config.MinNotionalUSDT
	}
	lotNotional := b.Config.LotStep * b.Config.ContractValue
	delay := in.ScenarioDelay
	if ev.Playbook.DelayBars > 0 {
		delay = ev.Playbook.DelayBars
	}
	corr := CountCorrelatedOpen(b.Account, ev.EventFamily)
	sized := SizeEventNotional(
		b.Account, edge, stress, 100, int64(ev.CompletionBars*3600),
		minNotional, lotNotional, ev.CapacityUSDT, ev.MaxSizeUSDT, corr,
	)
	if sized.Skipped {
		reason := RejectBelowMinNotional
		if sized.SkipReason == "leverage_cap" {
			reason = RejectConcurrencyFull
		}
		return "NO_TRADE", positionSize(b, in.Features.Symbol), 0, appendUnique(rejects, reason)
	}
	if TerminalRiskCeilingBlocksOpen(b.Account, sized.StressBudgetUSDT) {
		return "NO_TRADE", positionSize(b, in.Features.Symbol), 0, appendUnique(rejects, RejectTerminalRiskCeiling)
	}
	if !b.Config.TradeAllowed {
		return "NO_TRADE", positionSize(b, in.Features.Symbol), 0, appendUnique(rejects, "TRADE_ALLOWED_FALSE")
	}
	if SolvencyBlocksNewOpen(EvaluateSolvency(b.Account)) {
		return "NO_TRADE", positionSize(b, in.Features.Symbol), 0, appendUnique(rejects, RejectSolvencyForced)
	}

	if exec.SelectedAction == ActionBoundedMaker {
		b.PlaceMakerOrder(ev, in, sized.NotionalUSDT)
		cand := makeEventCandidate(b.Config, ev, in, dir, edge, sized.StressBudgetUSDT, sized.NotionalUSDT)
		b.Pending.Enqueue(eventaccount.PendingEntry{
			Candidate:     cand,
			NotionalUSDT:  sized.NotionalUSDT,
			StressLoss:    sized.StressBudgetUSDT,
			ExecuteIndex:  eventaccount.ExecuteIndexForDelay(in.BarIndex, delay),
			ScenarioDelay: delay,
			CostStress:    in.CostStress,
		})
		return posAction, sized.NotionalUSDT, sized.StressBudgetUSDT, rejects
	}

	if exec.SelectedAction == ActionConditionalMarketableLimit {
		cand := makeEventCandidate(b.Config, ev, in, dir, edge, sized.StressBudgetUSDT, sized.NotionalUSDT)
		b.Pending.Enqueue(eventaccount.PendingEntry{
			Candidate:     cand,
			NotionalUSDT:  sized.NotionalUSDT,
			StressLoss:    sized.StressBudgetUSDT,
			ExecuteIndex:  eventaccount.ExecuteIndexForDelay(in.BarIndex, delay),
			ScenarioDelay: delay,
			CostStress:    in.CostStress,
		})
		return posAction, sized.NotionalUSDT, sized.StressBudgetUSDT, rejects
	}

	return "NO_TRADE", positionSize(b, in.Features.Symbol), 0, rejects
}

func (b *Brain) appendTrace(
	in BarInput,
	ev MatchedPersonalEvent,
	enter EnterGateResult,
	exec ExecutionDecision,
	applied string,
	posSize float64,
	sol SolvencyDecision,
	stopForced bool,
	fillOrMiss, toxicReason string,
	annVis AnnouncementVisibility,
	termRisk TerminalRiskUsage,
	rejects []string,
) (BrainTraceEntry, error) {
	stopState := StopErrorHolding
	if stopForced {
		stopState = StopErrorExit
	}
	deadline := 0
	if ev.DeadlineBar > 0 {
		deadline = ev.DeadlineBar
	}
	constHash := b.Config.ConstitutionHash
	if constHash == "" && ev.Proofs.ConstitutionHash != "" {
		constHash = ev.Proofs.ConstitutionHash
	}
	entry := BrainTraceEntry{
		SchemaVersion:          TraceSchemaVersion,
		Generation:             Generation,
		ProtocolHash:           b.Config.ProtocolHash,
		BarIndex:               in.BarIndex,
		DecisionTime:           EventDecisionTime(in.DecisionTime),
		Symbol:                 in.Features.Symbol,
		EventFamily:            ev.EventFamily,
		EventPhase:             ev.EventPhase,
		ThesisHealth:           ev.ThesisHealth,
		ConstitutionHash:       constHash,
		ExclusionProofHash:     ev.Proofs.ExclusionProofHash,
		AnnouncementVisibility: annVis,
		EntryProofHash:         ev.Proofs.EntryProofHash,
		ExecutionPolicyHash:    ev.Proofs.ExecutionPolicyHash,
		ExecutionActions:         exec.ActionsCompared,
		MakerSelectionBias:       ev.Playbook.ExecutionNarrative.MakerQuadrant,
		EnterGate:                enter.Status,
		FillOrMissReason:         fillOrMiss,
		ToxicFillReason:          toxicReason,
		ExitProofHash:            ev.Proofs.ExitProofHash,
		CrowdingStress:           CrowdingStressLabel(ev),
		TerminalRiskUsage:        termRisk,
		RejectReasons:            uniqueStrings(append(exec.RejectReasons, rejects...)),
		FinalAction:              applied,
		PositionSize:             posSize,
		Deadline:                 deadline,
		StopErrorState:           stopState,
		SolvencyState:            sol.State,
		TradeAllowed:             b.Config.TradeAllowed,
	}
	return b.Trace.Append(entry)
}
