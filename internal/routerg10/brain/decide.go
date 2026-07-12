package brain

import (
	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/episodic"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg6/eventaccount"
)

// NewBrain creates a brain at initial equity with empty account and trace log.
func NewBrain(cfg Config) *Brain {
	if cfg.ProtocolHash == "" {
		cfg.ProtocolHash = ProtocolHash
	}
	return &Brain{
		Config:  cfg,
		Account: eventaccount.NewAccount(cfg.InitialEquityUSDT),
		Pending: &eventaccount.PendingQueue{},
		Trace:   audit.NewTraceLog(),
	}
}

// DecideBar executes one bar: MTM, pending flush, StopError, solvency, counterfactual, apply.
func (b *Brain) DecideBar(in BarInput) (BarOutcome, error) {
	b.Account.MarkToMarket(in.Prices)
	matured := b.Pending.FlushMatured(in.BarIndex)
	_ = flushPending(b, matured, in, entryPriceAt(in))

	b.Account.MarkToMarket(in.Prices)
	stopForced, _ := processStopErrors(b, in)
	sol := EvaluateSolvency(b.Account)
	solForced := false
	if SolvencyOverridesPlaybook(sol) {
		if err := applySolvencyAction(b, sol, in); err == nil {
			solForced = true
		}
	}

	primary := pickPrimaryEvent(b, in, in.Events)
	ctx, rejects := b.buildDecisionContext(in, primary, sol)
	if solForced {
		rejects = appendUnique(rejects, RejectSolvencyForced)
	}
	if stopForced {
		rejects = appendUnique(rejects, RejectStopErrorForced)
	}

	var cf counterfactual.Decision
	if stopForced || solForced {
		cf = forcedCounterfactualDecision(ctx, b.Config.ProtocolHash, stopForced, solForced)
	} else {
		var err error
		cf, err = counterfactual.Decide(ctx, b.Config.ProtocolHash)
		if err != nil {
			return BarOutcome{}, err
		}
	}

	applied := cf.FinalAction
	posSize := positionSize(b, in.Features.Symbol)
	if solForced && sol.Action != "" {
		applied = sol.Action
	} else if stopForced {
		applied = "CLOSE"
	} else {
		applied, posSize, rejects = b.applyDecision(cf, primary, in, rejects)
	}

	trace, err := b.appendTrace(in, primary, ctx, cf, applied, posSize, sol, stopForced, rejects)
	if err != nil {
		return BarOutcome{}, err
	}
	return BarOutcome{
		Trace:           trace,
		Counterfactual:  cf,
		AppliedAction:   applied,
		PositionSize:    posSize,
		SolvencyForced:  solForced,
		StopErrorForced: stopForced,
	}, nil
}

func (b *Brain) buildDecisionContext(in BarInput, ev MatchedEvent, sol SolvencyDecision) (counterfactual.DecisionContext, []string) {
	var rejects []string
	hasEvent := ev.EventID != ""
	maxC := MaxConcurrent(b.Config.InitialEquityUSDT)
	open := len(b.Account.OpenSlots())
	pos := positionContext(b, in.Features.Symbol, maxC, open)

	q := episodic.QueryVector{
		Symbol:      in.Features.Symbol,
		ProfileID:   in.Features.ProfileID,
		EventFamily: ev.EventFamily,
		EventPhase:  ev.EventPhase,
		State: episodic.StateVector{
			ReturnBPS:         in.Features.ReturnBPS,
			PriorReturnBPS:    in.Features.PriorReturnBPS,
			BTCRelativeBPS:    in.Features.BTCRelativeBPS,
			BarRangeBPS:       in.Features.BarRangeBPS,
			SessionBucket:     in.Features.SessionBucket,
			LiquidityBucket:   in.Features.LiquidityBucket,
			RollingVolumeUSDT: in.Features.RollingVolumeUSDT,
		},
		PersonalExec: episodic.PersonalExecSnapshot{
			FingerprintKnown: !b.Config.PersonalAdj.CriticalUnknown,
		},
	}
	memory := episodic.MemoryAdvice{SchemaVersion: episodic.SchemaVersionAdvice, SizeScale: 1, AdviceCodes: []string{episodic.AdviceNoVeto}}
	if b.Config.Memory != nil {
		if hits, err := b.Config.Memory.Retrieve(q, nil); err == nil {
			memory = episodic.Advise(hits, episodic.DefaultResidual())
		}
	}

	costBase := in.CostBase
	if costBase.ProfileID == "" {
		costBase = residualmap.CostBase{
			ProfileID:              in.Features.ProfileID,
			RollingQuoteVolumeUSDT: in.Features.RollingVolumeUSDT,
			BarHigh:                in.Features.Price * 1.001,
			BarLow:                 in.Features.Price * 0.999,
			BarMid:                 in.Features.Price,
			DelayBars:              in.ScenarioDelay,
		}
	}

	ctx := counterfactual.DecisionContext{
		Symbol:          in.Features.Symbol,
		ProfileID:       in.Features.ProfileID,
		DecisionTime:    EventDecisionTime(in.DecisionTime),
		EquityUSDT:      b.Account.Core.EquityUSDT,
		Event: counterfactual.EventContext{
			HasEvent:      hasEvent,
			EventFamily:   ev.EventFamily,
			PlaybookHash:  ev.Playbook.PlaybookHash,
			Horizon:       ev.Playbook.Horizon,
			GrossLongBPS:  ev.GrossLongBPS,
			GrossShortBPS: ev.GrossShortBPS,
			DelayBars:     in.ScenarioDelay,
		},
		Position:        pos,
		Query:           q,
		Memory:          memory,
		Fingerprint:     b.Config.Fingerprint,
		CostBase:        costBase,
		PersonalAdj:     residualmap.LookupPersonalAdjustments(b.Config.Fingerprint, in.Features.Symbol, "long"),
		MinNotionalUSDT: b.Config.MinNotionalUSDT,
		LotStep:         b.Config.LotStep,
		ContractValue:   b.Config.ContractValue,
		OOD:             in.Features.OOD,
		Solvent:         !SolvencyBlocksNewOpen(sol) && b.Account.AllowsNewEvent(),
		StressBudgetBPS: MaxEventStressLossFraction * 10000,
		CompletionBars:  ev.CompletionBars,
		CapacityUSDT:    ev.CapacityUSDT,
	}
	if open >= maxC && !pos.HasPosition {
		rejects = append(rejects, RejectConcurrencyFull)
	}
	return ctx, rejects
}

func (b *Brain) applyDecision(
	cf counterfactual.Decision,
	ev MatchedEvent,
	in BarInput,
	rejects []string,
) (string, float64, []string) {
	action := cf.FinalAction
	symbol := in.Features.Symbol
	posSize := positionSize(b, symbol)

	switch action {
	case "LONG", "SHORT":
		if !b.Account.AllowsNewEvent() {
			return "NO_TRADE", posSize, appendUnique(rejects, counterfactual.RejectStressInsolvent)
		}
		openCount := len(b.Account.OpenSlots())
		maxC := MaxConcurrent(b.Config.InitialEquityUSDT)
		hasPos := positionSize(b, symbol) > 0
		if openCount >= maxC && !hasPos {
			return "NO_TRADE", posSize, appendUnique(rejects, RejectConcurrencyFull)
		}
		if openCount >= maxC {
			rejects = appendUnique(rejects, RejectConcurrencyFull)
			return "HOLD", posSize, rejects
		}
		dir := "long"
		if action == "SHORT" {
			dir = "short"
		}
		edge := ev.GrossLongBPS * b.Account.Core.EquityUSDT / 10000
		if dir == "short" {
			edge = ev.GrossShortBPS * b.Account.Core.EquityUSDT / 10000
		}
		adj := residualmap.LookupPersonalAdjustments(b.Config.Fingerprint, symbol, dir)
		grossBPS := ev.GrossLongBPS
		if dir == "short" {
			grossBPS = ev.GrossShortBPS
		}
		tiers := residualmap.ComputeTierCosts(grossBPS, in.CostBase, adj, b.Account.Core.EquityUSDT)
		tier := tiers[g10protocol.ActualEquityTierLabel]
		if tier.CapitalUSDT == 0 {
			tier = tiers["30"]
		}
		delay := in.ScenarioDelay
		if ev.Playbook.DelayBars > 0 {
			delay = ev.Playbook.DelayBars
		}
		stressBPS := counterfactual.AdverseStressBPS(tier, delay)
		stress := stressBPS * b.Account.Core.EquityUSDT / 10000
		corr := CountCorrelatedOpen(b.Account, ev.EventFamily)
		sized := SizeEventNotional(
			b.Account, edge, stress, 100, int64(ev.CompletionBars*3600),
			b.Config.MinNotionalUSDT, b.Config.LotStep*b.Config.ContractValue, ev.CapacityUSDT, corr,
		)
		if sized.Skipped {
			reason := RejectBelowMinNotional
			if sized.SkipReason == "leverage_cap" {
				reason = RejectConcurrencyFull
			}
			return "NO_TRADE", posSize, appendUnique(rejects, reason)
		}
		cand := makeEventCandidate(b.Config, ev, in, dir, edge, stress, sized.NotionalUSDT)
		b.Pending.Enqueue(eventaccount.PendingEntry{
			Candidate:     cand,
			NotionalUSDT:  sized.NotionalUSDT,
			StressLoss:    sized.StressBudgetUSDT,
			ExecuteIndex:  eventaccount.ExecuteIndexForDelay(in.BarIndex, delay),
			ScenarioDelay: delay,
			CostStress:    in.CostStress,
		})
		posSize = sized.NotionalUSDT
	case "TRIM":
		applyTrim(b, symbol, in)
		posSize = positionSize(b, symbol) * 0.5
	case "CLOSE":
		applyClose(b, symbol, in, "brain_close")
		posSize = 0
	case "REVERSE":
		applyClose(b, symbol, in, "reverse_prep")
		// re-open handled next bar via pending if counterfactual allowed
	case "ADD":
		// sizing handled like open with reduced scale from counterfactual
		posSize = positionSize(b, symbol)
	case "HOLD", "NO_TRADE":
		// no execution
	}
	return action, posSize, rejects
}

func (b *Brain) appendTrace(
	in BarInput,
	ev MatchedEvent,
	ctx counterfactual.DecisionContext,
	cf counterfactual.Decision,
	applied string,
	posSize float64,
	sol SolvencyDecision,
	stopForced bool,
	rejects []string,
) (audit.BrainTraceEntry, error) {
	stopState := StopErrorHolding
	if stopForced {
		stopState = StopErrorExit
	}
	deadline := 0
	if ev.DeadlineBar > 0 {
		deadline = ev.DeadlineBar
	}
	entry := audit.BrainTraceEntry{
		SchemaVersion:       audit.SchemaVersion,
		Generation:          Generation,
		ProtocolHash:        b.Config.ProtocolHash,
		BarIndex:            in.BarIndex,
		DecisionTime:        EventDecisionTime(in.DecisionTime),
		Symbol:              in.Features.Symbol,
		EventFamily:         ev.EventFamily,
		EventPhase:          ev.EventPhase,
		ThesisHealth:        ev.ThesisHealth,
		FingerprintVersion:  b.Config.Fingerprint.FingerprintHash,
		MemoryHits:          episodic.TraceHitsFrom(ctx.Memory.Hits),
		CounterfactualTable: cf.Table,
		RejectReasons:       uniqueStrings(append(cf.RejectReasons, rejects...)),
		FinalAction:         applied,
		PositionSize:        posSize,
		Deadline:            deadline,
		StopErrorState:      stopState,
		SolvencyState:       sol.State,
	}
	return b.Trace.Append(entry)
}

func forcedCounterfactualDecision(
	ctx counterfactual.DecisionContext,
	protocolHash string,
	stopForced, solForced bool,
) counterfactual.Decision {
	action := "HOLD"
	if stopForced {
		action = "CLOSE"
	} else if solForced {
		action = "TRIM"
	}
	table, _ := counterfactual.Compare(ctx, protocolHash)
	return counterfactual.Decision{
		SchemaVersion: counterfactual.SchemaVersionDecision,
		Table:         table,
		FinalAction:   action,
		RejectReasons: []string{},
		SizeScale:     0,
	}
}

func positionContext(b *Brain, symbol string, maxC, open int) counterfactual.PositionContext {
	state := b.Account.Core.CurrentPositionState(symbol)
	has := state.Side != actionspace.SideFlat
	dir := ""
	if has {
		dir = string(state.Side)
	}
	return counterfactual.PositionContext{
		HasPosition:      has,
		Direction:        dir,
		NotionalUSDT:     actionspace.NotionalUSDT(state),
		ConcurrentEvents: open,
		MaxConcurrent:    maxC,
	}
}

func positionSize(b *Brain, symbol string) float64 {
	return actionspace.NotionalUSDT(b.Account.Core.CurrentPositionState(symbol))
}

func processStopErrors(b *Brain, in BarInput) (forced bool, reason string) {
	for _, slot := range b.Account.OpenSlots() {
		sig := eventaccount.ExitSignal{
			BarIndex:          in.BarIndex,
			InvalidatorFired:  slot.Candidate.InvalidatorAtBar > 0 && in.BarIndex >= slot.Candidate.InvalidatorAtBar,
			PayerGone:         slot.Candidate.PayerGoneAtBar > 0 && in.BarIndex >= slot.Candidate.PayerGoneAtBar,
			EventComplete:     slot.Candidate.CompleteAtBar > 0 && in.BarIndex >= slot.Candidate.CompleteAtBar,
			OutOfDistribution: slot.Candidate.OODAtBar > 0 && in.BarIndex >= slot.Candidate.OODAtBar,
			DeadlineReached:   slot.Candidate.DeadlineBarIndex > 0 && in.BarIndex >= slot.Candidate.DeadlineBarIndex,
		}
		next, exitReason, exit := eventaccount.EvaluateStopError(slot.StopState, sig)
		if !exit {
			continue
		}
		price := in.Prices[slot.Candidate.Symbol]
		if price <= 0 {
			price = in.Features.Price
		}
		_ = closeSlot(b, slot, price, in.CostStress, exitReason)
		slot.StopState = next
		return true, exitReason
	}
	return false, ""
}

func applySolvencyAction(b *Brain, sol SolvencyDecision, in BarInput) error {
	if sol.Action == "TRIM" && sol.EventID != "" {
		if slot, ok := b.Account.Slots[sol.EventID]; ok && !slot.Closed {
			applyTrim(b, slot.Candidate.Symbol, in)
			return nil
		}
	}
	return eventaccount.ApplySolvencyTrims(b.Account, in.BarIndex, func(a *eventaccount.Account, slot *eventaccount.EventSlot, reason string) error {
		price := in.Prices[slot.Candidate.Symbol]
		if price <= 0 {
			price = in.Features.Price
		}
		return closeSlot(b, slot, price, in.CostStress, reason)
	})
}

