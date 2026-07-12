package validation

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg6/eventaccount"
	g6protocol "lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/sizing"
	"lingxi/internal/routerg10/brain"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
)

// ReplayTierEvents replays validation events for one capital tier across delay/cost scenarios.
func ReplayTierEvents(
	events []ReplayEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	sizingInput FrozenSizingInput,
	pb playbook.FrozenEventPlaybook,
	fp fingerprint.PersonalExecutionFingerprint,
) ([]TierScenarioResult, DailyEquityReplay, ReplayStats, TierReplayStats) {
	scenarios := make([]TierScenarioResult, 0, len(protocol.ValidationDelayBars)*2)
	var daily DailyEquityReplay
	var stats ReplayStats
	for _, delay := range protocol.ValidationDelayBars {
		for _, stressMult := range []float64{1.0, protocol.CostStressMultiplier} {
			if stressMult > 1.0+1e-9 && delay != 0 {
				continue
			}
			result, dayTrack, replayStats := replayScenario(events, barsBySymbol, fundingBySymbol, initialEquity, delay, stressMult, sizingInput, pb, fp)
			scenarios = append(scenarios, result)
			if delay == 0 && stressMult <= 1.0+1e-9 {
				mergeDaily(&daily, dayTrack)
				stats = replayStats
			}
		}
	}
	replayStats := buildTierReplayStats(events, daily, stats)
	return scenarios, daily, stats, replayStats
}

func replayScenario(
	events []ReplayEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	delayBars int,
	costStress float64,
	sizingInput FrozenSizingInput,
	pb playbook.FrozenEventPlaybook,
	fp fingerprint.PersonalExecutionFingerprint,
) (TierScenarioResult, DailyEquityReplay, ReplayStats) {
	account := eventaccount.NewAccount(initialEquity)
	stats := ReplayStats{EventsSeen: len(events)}
	emptyDaily := DailyEquityReplay{
		DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
		EventPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
	}
	if len(events) == 0 {
		return TierScenarioResult{
			CapitalTier: int(initialEquity), DelayBars: delayBars, CostStressMultiplier: costStress,
			InitialEquityUSDT: initialEquity, FinalEquityUSDT: initialEquity, NetReturnNonNegative: true,
		}, emptyDaily, stats
	}

	steps := buildReplayTimeline(events, barsBySymbol, delayBars, sizingInput)
	daily := DailyEquityReplay{
		DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
		EventPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
	}
	fundingBundle := portfoliosim.FundingBundle{BySymbol: fundingBySymbol}
	queue := &eventaccount.PendingQueue{}
	var prev time.Time
	eventsByID := map[string]*ReplayEvent{}
	for i := range events {
		eventsByID[events[i].EventID] = &events[i]
	}
	for _, step := range steps {
		if !prev.IsZero() {
			symbols := openSymbols(account)
			account.ApplyHoldFunding(fundingBundle, symbols, prev, step.time, costStress)
			account.MarkToMarket(step.prices)
		}
		before := account.Core.EquityUSDT
		slotsBefore := snapshotSlots(account)
		candidates := make([]eventaccount.EventCandidate, 0, len(step.eventIDs))
		for _, eventID := range step.eventIDs {
			ev := eventsByID[eventID]
			if ev == nil {
				continue
			}
			if ev.DirectionDeferred {
				resolveReplayDirection(account, ev, pb, fp)
			}
			if ev.NoTrade || ev.Direction == "flat" {
				continue
			}
			candidates = append(candidates, replayEventToCandidate(*ev, delayBars, sizingInput))
		}
		eventaccount.ReplayBar(account, queue, eventaccount.BarReplayInput{
			BarIndex: step.barIndex, DecisionTime: step.time, Candidates: candidates,
			Prices: step.prices, ScenarioDelay: delayBars, CostStress: costStress,
		}, entryPriceFor(barsBySymbol))
		after := account.Core.EquityUSDT
		delta := after - before
		if math.Abs(delta) > 1e-9 {
			day := step.time.UTC().Format("2006-01-02")
			daily.DailyPnL[day] += delta
			attributeBarDelta(delta, slotsBefore, snapshotSlots(account), candidates, events, &daily, day)
		}
		prev = step.time
	}

	stats.EventsSkipped = account.QueueRejects + account.OverlapRejects + account.Core.PendingRejects + account.Core.StaleRejects
	stats.EventsTraded = countClosedSlots(account)
	stats.SkipConcurrencyCap = account.QueueRejects
	for _, ev := range events {
		if ev.CapacityFail {
			stats.SkipCapacity++
			stats.CapacityFails++
		}
	}
	for _, step := range steps {
		for _, eventID := range step.eventIDs {
			ev := eventsByID[eventID]
			if ev == nil {
				continue
			}
			countSizingSkip(&stats, account, replayEventToCandidate(*ev, delayBars, sizingInput))
		}
	}

	return TierScenarioResult{
		CapitalTier: int(initialEquity), DelayBars: delayBars, CostStressMultiplier: costStress,
		InitialEquityUSDT: initialEquity, FinalEquityUSDT: account.Core.EquityUSDT,
		NetReturnFraction: netReturnFraction(initialEquity, account.Core.EquityUSDT),
		NetReturnNonNegative: account.Core.EquityUSDT >= initialEquity,
		EventsSeen: stats.EventsSeen, EventsTraded: stats.EventsTraded, EventsSkipped: stats.EventsSkipped,
		SkipConcurrencyCap: stats.SkipConcurrencyCap, SkipCapacity: stats.SkipCapacity,
		SkipBelowMinNotional: stats.SkipBelowMinNotional, SkipBelowLotFloor: stats.SkipBelowLotFloor,
		MaxDrawdownFraction: account.Core.MaxDrawdown,
		LeverageBreach: account.Core.LeverageBreach, SolvencyBreach: account.Core.SolvencyBreach,
	}, daily, stats
}

type slotSnapshot struct {
	eventID  string
	symbol   string
	notional float64
	closed   bool
}

func snapshotSlots(account *eventaccount.Account) map[string]slotSnapshot {
	out := map[string]slotSnapshot{}
	for id, slot := range account.Slots {
		if slot == nil {
			continue
		}
		out[id] = slotSnapshot{
			eventID: id, symbol: slot.Candidate.Symbol,
			notional: slot.Notional, closed: slot.Closed,
		}
	}
	return out
}

func attributeBarDelta(
	delta float64,
	before, after map[string]slotSnapshot,
	candidates []eventaccount.EventCandidate,
	events []ReplayEvent,
	daily *DailyEquityReplay,
	day string,
) {
	type weight struct {
		eventID string
		symbol  string
		w       float64
	}
	var weights []weight
	closed := map[string]bool{}
	for id, snap := range after {
		prev, ok := before[id]
		if ok && !prev.closed && snap.closed {
			closed[id] = true
			weights = append(weights, weight{eventID: id, symbol: snap.symbol, w: math.Max(snap.notional, 1)})
		}
	}
	if len(weights) == 0 {
		for id, snap := range after {
			if snap.closed {
				continue
			}
			weights = append(weights, weight{eventID: id, symbol: snap.symbol, w: math.Max(snap.notional, 1)})
		}
	}
	if len(weights) == 0 {
		for _, c := range candidates {
			weights = append(weights, weight{eventID: c.EventID, symbol: c.Symbol, w: math.Max(c.ReferenceNotional, 1)})
		}
	}
	if len(weights) == 0 {
		return
	}
	total := 0.0
	for _, item := range weights {
		total += item.w
	}
	if total <= 0 {
		return
	}
	shockByEvent := map[string]string{}
	for _, ev := range events {
		shockByEvent[ev.EventID] = ev.ShockCluster
	}
	for _, item := range weights {
		share := delta * item.w / total
		daily.EventPnL[item.eventID] += share
		daily.SymbolPnL[item.symbol] += share
		if cluster := shockByEvent[item.eventID]; cluster != "" && cluster != "none" {
			daily.ShockDayPnL[cluster] += share
		}
	}
}

func buildTierReplayStats(events []ReplayEvent, daily DailyEquityReplay, stats ReplayStats) TierReplayStats {
	days := map[string]bool{}
	symbols := map[string]bool{}
	shocks := map[string]bool{}
	var cliff float64
	capFails := 0
	for _, ev := range events {
		days[ev.Day] = true
		symbols[ev.Symbol] = true
		if ev.ShockCluster != "" && ev.ShockCluster != "none" {
			shocks[ev.ShockCluster] = true
		}
		if ev.CapacityFail {
			capFails++
		}
		if ev.CapacityCliffUSDT > 0 && (cliff == 0 || ev.CapacityCliffUSDT < cliff) {
			cliff = ev.CapacityCliffUSDT
		}
	}
	positiveSum := 0.0
	maxPositive := 0.0
	for _, pnl := range daily.EventPnL {
		if pnl > 0 {
			positiveSum += pnl
			if pnl > maxPositive {
				maxPositive = pnl
			}
		}
	}
	dayPositive := map[string]float64{}
	for day, pnl := range daily.DailyPnL {
		if pnl > 0 {
			dayPositive[day] = pnl
		}
	}
	nonzero, positive := shockBlockSigns(daily.ShockDayPnL)
	n := float64(len(events))
	capRate := 0.0
	if n > 0 {
		capRate = float64(capFails) / n
	}
	maxEvent := 0.0
	if positiveSum > 0 {
		maxEvent = maxPositive / positiveSum
	}
	return TierReplayStats{
		Count: len(events), UniqueDays: len(days), UniqueSymbols: len(symbols),
		UniqueShockBlocks: len(shocks), CapacityFailRate: capRate, CapacityCliffUSDT: cliff,
		MaxPositiveContributionFraction: maxEvent,
		MaxDayPositiveContributionFraction: maxDayContribution(dayPositive),
		NonzeroShockBlocks: nonzero, PositiveShockBlocks: positive,
	}
}

func shockBlockSigns(shockPnL map[string]float64) (nonzero, positive int) {
	for _, pnl := range shockPnL {
		if math.Abs(pnl) < 1e-9 {
			continue
		}
		nonzero++
		if pnl > 0 {
			positive++
		}
	}
	return nonzero, positive
}

func maxDayContribution(dayPositive map[string]float64) float64 {
	total := 0.0
	for _, v := range dayPositive {
		total += v
	}
	if total <= 0 {
		return 0
	}
	max := 0.0
	for _, v := range dayPositive {
		if v/total > max {
			max = v / total
		}
	}
	return max
}

func netReturnFraction(initial, final float64) float64 {
	if initial <= 0 {
		return 0
	}
	return (final - initial) / initial
}

func mergeDaily(dst *DailyEquityReplay, src DailyEquityReplay) {
	if dst.DailyPnL == nil {
		dst.DailyPnL = map[string]float64{}
	}
	if dst.ShockDayPnL == nil {
		dst.ShockDayPnL = map[string]float64{}
	}
	if dst.EventPnL == nil {
		dst.EventPnL = map[string]float64{}
	}
	if dst.SymbolPnL == nil {
		dst.SymbolPnL = map[string]float64{}
	}
	for k, v := range src.DailyPnL {
		dst.DailyPnL[k] += v
	}
	for k, v := range src.ShockDayPnL {
		dst.ShockDayPnL[k] += v
	}
	for k, v := range src.EventPnL {
		dst.EventPnL[k] += v
	}
	for k, v := range src.SymbolPnL {
		dst.SymbolPnL[k] += v
	}
}

type timelineStep struct {
	time     time.Time
	barIndex int
	prices   map[string]float64
	eventIDs []string
}

func buildReplayTimeline(
	events []ReplayEvent,
	barsBySymbol map[string][]profitmap.Bar,
	delayBars int,
	sizingInput FrozenSizingInput,
) []timelineStep {
	byTime := map[string]*timelineStep{}
	for _, ev := range events {
		if !ev.DirectionDeferred && (ev.NoTrade || ev.Direction == "flat") {
			continue
		}
		bars, ok := barsBySymbol[ev.Symbol]
		if !ok || ev.DecisionBarIndex < 0 || ev.DecisionBarIndex >= len(bars) {
			continue
		}
		decisionKey := ev.DecisionTime.UTC().Format(time.RFC3339Nano)
		step := byTime[decisionKey]
		if step == nil {
			step = &timelineStep{time: ev.DecisionTime, barIndex: ev.DecisionBarIndex, prices: map[string]float64{}}
			byTime[decisionKey] = step
		}
		step.eventIDs = append(step.eventIDs, ev.EventID)
		if step.prices[ev.Symbol] == 0 && ev.DecisionBarIndex < len(bars) {
			step.prices[ev.Symbol] = bars[ev.DecisionBarIndex].Close
		}
		exitBar := ev.DecisionBarIndex + 1 + delayBars + ev.HorizonBarCount
		if exitBar <= 0 {
			exitBar = ev.CompleteAtBar
		}
		for idx := ev.DecisionBarIndex; idx <= exitBar && idx < len(bars); idx++ {
			t := bars[idx].OpenTime
			key := t.UTC().Format(time.RFC3339Nano)
			mtmStep, ok := byTime[key]
			if !ok {
				mtmStep = &timelineStep{time: t, barIndex: idx, prices: map[string]float64{}}
				byTime[key] = mtmStep
			}
			mtmStep.prices[ev.Symbol] = bars[idx].Close
			if idx > mtmStep.barIndex {
				mtmStep.barIndex = idx
			}
		}
	}
	steps := make([]timelineStep, 0, len(byTime))
	for _, step := range byTime {
		steps = append(steps, *step)
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].time.Equal(steps[j].time) {
			return steps[i].barIndex < steps[j].barIndex
		}
		return steps[i].time.Before(steps[j].time)
	})
	return steps
}

func resolveReplayDirection(
	account *eventaccount.Account,
	ev *ReplayEvent,
	pb playbook.FrozenEventPlaybook,
	fp fingerprint.PersonalExecutionFingerprint,
) {
	features := FeatureSnapshot{ReturnBPS: ev.ReturnBPS, PriorReturnBPS: ev.PriorReturnBPS}
	inst := LookupInstrumentConstraints(fp, ev.Symbol)
	sol := brain.EvaluateSolvency(account)
	solvent := !brain.SolvencyBlocksNewOpen(sol) && account.AllowsNewEvent()
	barFeatures := brain.BarFeatures{
		Symbol: ev.Symbol, ReturnBPS: ev.ReturnBPS, PriorReturnBPS: ev.PriorReturnBPS,
	}
	costBase := residualmap.CostBase{ProfileID: pb.ProfileID}
	pick := PickBrainDirection(
		pb, features, barFeatures, fp, costBase, inst, account.Core.EquityUSDT,
		ev.LongConservativeBPS, ev.ShortConservativeBPS, ev.LongStressBPS, ev.ShortStressBPS,
		solvent,
	)
	ev.Direction = pick.Direction
	ev.ConservativeBPS = pick.ConservativeBPS
	ev.StressBPS = pick.StressBPS
	ev.NoTrade = pick.NoTrade
	ev.BrainAction = pick.Action
	ev.DirectionDeferred = false
}

func replayEventToCandidate(ev ReplayEvent, delayBars int, sizingInput FrozenSizingInput) eventaccount.EventCandidate {
	ref := sizingInput.ReferenceNotional
	if ref <= 0 {
		ref = ev.ReferenceNotional
	}
	edgeBPS := ev.ConservativeBPS
	if sizingInput.ConservativeMeanBPS > 0 {
		edgeBPS = sizingInput.ConservativeMeanBPS
	}
	stressBPS := ev.StressBPS
	if sizingInput.StressConservativeMeanBPS > 0 {
		stressBPS = sizingInput.StressConservativeMeanBPS
	}
	edgeUSDT := edgeBPS * ref / 10000
	stressUSDT := math.Max(stressBPS*ref/10000, ref*g6protocol.MaxEventStressLossFraction)
	if stressUSDT <= 0 {
		stressUSDT = ref * g6protocol.MaxEventStressLossFraction
	}
	exitBar := ev.DecisionBarIndex + 1 + delayBars + ev.HorizonBarCount
	if exitBar <= 0 {
		exitBar = ev.CompleteAtBar
	}
	minNotional := ev.MinNotionalUSDT
	lotNotional := ev.LotNotionalUSDT
	if minNotional <= 0 {
		minNotional = 1
	}
	if lotNotional <= 0 {
		lotNotional = minNotional
	}
	return eventaccount.EventCandidate{
		EventID: ev.EventID, Symbol: ev.Symbol, Direction: ev.Direction, EventFamily: ev.EventFamily,
		DecisionTime: ev.DecisionTime, DecisionBarIndex: ev.DecisionBarIndex, WindowEndBarIndex: exitBar,
		ConservativeEdgeUSDT: edgeUSDT, StressLossUSDT: stressUSDT, ReferenceNotional: ref,
		MinNotionalUSDT: minNotional, LotNotionalUSDT: lotNotional,
		CostEstimateBPS: math.Max(0, ev.ConservativeBPS-ev.StressBPS), ScenarioDelay: delayBars,
		DeadlineBarIndex: exitBar, CompleteAtBar: exitBar,
	}
}

func entryPriceFor(barsBySymbol map[string][]profitmap.Bar) func(string, int) (float64, bool) {
	return func(symbol string, executeIndex int) (float64, bool) {
		bars, ok := barsBySymbol[symbol]
		if !ok || executeIndex < 0 || executeIndex >= len(bars) {
			return 0, false
		}
		price := bars[executeIndex].Open
		return price, price > 0
	}
}

func openSymbols(account *eventaccount.Account) []string {
	syms := make([]string, 0, len(account.SymbolEvents))
	for sym := range account.SymbolEvents {
		syms = append(syms, sym)
	}
	sort.Strings(syms)
	return syms
}

func countClosedSlots(account *eventaccount.Account) int {
	n := 0
	for _, slot := range account.Slots {
		if slot != nil {
			n++
		}
	}
	return n
}

func countSizingSkip(stats *ReplayStats, account *eventaccount.Account, c eventaccount.EventCandidate) {
	sized := sizing.SizeEventNotional(sizing.Input{
		EquityUSDT: account.Core.EquityUSDT, ConservativeEdgeUSDT: c.ConservativeEdgeUSDT,
		StressLossUSDT: c.StressLossUSDT, ReferenceNotionalUSDT: c.ReferenceNotional,
		MarginTimeSec: c.MarginTimeSec, MinNotionalUSDT: c.MinNotionalUSDT,
		LotNotionalUSDT: c.LotNotionalUSDT, UsedAccountStressUSDT: account.UsedAccountStress(),
		UsedNotionalUSDT: account.UsedNotional(),
	})
	if !sized.Skipped {
		return
	}
	switch sized.SkipReason {
	case sizing.SkipBelowMinNotional:
		stats.SkipBelowMinNotional++
	case sizing.SkipBelowLotFloor:
		stats.SkipBelowLotFloor++
	}
}

// FrozenSizingFromEvents freezes conservative edge from replayed events for one tier.
func FrozenSizingFromEvents(events []ReplayEvent, capitalTier int) FrozenSizingInput {
	ref := float64(capitalTier)
	var sum, stressSum float64
	var n int
	for _, ev := range events {
		if ev.NoTrade || ev.Direction == "flat" {
			continue
		}
		sum += ev.ConservativeBPS
		stressSum += ev.StressBPS
		n++
	}
	out := FrozenSizingInput{ReferenceNotional: ref}
	if n > 0 {
		out.ConservativeMeanBPS = sum / float64(n)
		out.StressConservativeMeanBPS = stressSum / float64(n)
	}
	return out
}
