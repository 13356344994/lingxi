package personalvalidation

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
)

// FrozenSizingInput carries conservative edge for replay sizing.
type FrozenSizingInput struct {
	ConservativeMeanBPS       float64
	StressConservativeMeanBPS float64
	ReferenceNotional         float64
}

// ReplayTierEvents replays validation triggers through personalbrain.DecideBar account paths.
func ReplayTierEvents(
	events []TriggerEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	sizing FrozenSizingInput,
	pb personalplaybook.PersonalWildPlaybook,
	proofs ProofBundle,
) ([]TierScenarioResult, DailyEquityReplay, ReplayStats, TierReplayStats) {
	scenarios := make([]TierScenarioResult, 0, len(ValidationDelayBars)*3)
	var daily DailyEquityReplay
	var stats ReplayStats
	for _, delay := range ValidationDelayBars {
		for _, stressMult := range []float64{1.0, CostStressMultiplier} {
			if stressMult > 1.0+1e-9 && delay != 0 {
				continue
			}
			result, dayTrack, replayStats := replayScenario(events, barsBySymbol, fundingBySymbol, initialEquity, delay, stressMult, false, sizing, pb, proofs)
			scenarios = append(scenarios, result)
			if delay == 0 && stressMult <= 1.0+1e-9 {
				mergeDaily(&daily, dayTrack)
				stats = replayStats
			}
		}
		p90Result, _, p90Stats := replayScenario(events, barsBySymbol, fundingBySymbol, initialEquity, delay, P90SlippageStressMultiplier, true, sizing, pb, proofs)
		p90Result.P90SlippageStress = true
		scenarios = append(scenarios, p90Result)
		if delay == 0 {
			stats.ToxicRevalidateCount += p90Stats.ToxicRevalidateCount
			stats.ToxicExitCount += p90Stats.ToxicExitCount
		}
	}
	replayStats := buildTierReplayStats(events, daily, stats)
	return scenarios, daily, stats, replayStats
}

func replayScenario(
	events []TriggerEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	delayBars int,
	costStress float64,
	p90Slip bool,
	sizing FrozenSizingInput,
	pb personalplaybook.PersonalWildPlaybook,
	proofs ProofBundle,
) (TierScenarioResult, DailyEquityReplay, ReplayStats) {
	cfg := personalbrain.Config{
		InitialEquityUSDT: initialEquity,
		ConstitutionHash:  pb.ProofBinding.ConstitutionHash,
		MinNotionalUSDT:   pb.EntryNarrative.DynamicMinNotionalUSDT,
		ProtocolHash:      ProtocolHash,
		TradeAllowed:      true,
	}
	b := personalbrain.NewBrain(cfg)
	stats := ReplayStats{TriggersSeen: len(events)}
	daily := DailyEquityReplay{
		DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
		EventPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
	}
	if len(events) == 0 {
		return TierScenarioResult{
			CapitalTier: int(initialEquity), DelayBars: delayBars, CostStressMultiplier: costStress,
			P90SlippageStress: p90Slip, InitialEquityUSDT: initialEquity, FinalEquityUSDT: initialEquity,
			NetReturnNonNegative: true,
		}, daily, stats
	}

	steps := buildBrainReplayTimeline(events, barsBySymbol, delayBars)
	fundingBundle := portfoliosim.FundingBundle{BySymbol: fundingBySymbol}
	eventsByID := map[string]*TriggerEvent{}
	for i := range events {
		eventsByID[events[i].EventID] = &events[i]
	}
	var prev time.Time
	terminalHit := false
	for _, step := range steps {
		if !prev.IsZero() {
			symbols := b.OpenSymbols()
			b.ApplyHoldFunding(fundingBundle, symbols, prev, step.time, costStress)
			b.Account.MarkToMarket(step.prices)
		}
		before := b.Account.Core.EquityUSDT
		for _, eventID := range step.eventIDs {
			ev := eventsByID[eventID]
			if ev == nil {
				continue
			}
			if err := replayTriggerViaDecideBar(b, ev, pb, step, delayBars, costStress, p90Slip, sizing, initialEquity, proofs, &stats); err != nil {
				continue
			}
			if ev.CapitalExpansionStop {
				terminalHit = true
			}
		}
		b.Account.MarkToMarket(step.prices)
		after := b.Account.Core.EquityUSDT
		delta := after - before
		if math.Abs(delta) > 1e-9 {
			day := step.time.UTC().Format("2006-01-02")
			daily.DailyPnL[day] += delta
			for _, eventID := range step.eventIDs {
				ev := eventsByID[eventID]
				if ev == nil {
					continue
				}
				share := delta / float64(maxInt(1, len(step.eventIDs)))
				daily.EventPnL[ev.EventID] += share
				daily.SymbolPnL[ev.Symbol] += share
				if ev.ShockCluster != "" && ev.ShockCluster != "none" {
					daily.ShockDayPnL[ev.ShockCluster] += share
				}
			}
		}
		prev = step.time
	}
	_ = b.DiscardPending()
	return TierScenarioResult{
		CapitalTier: int(initialEquity), DelayBars: delayBars, CostStressMultiplier: costStress,
		P90SlippageStress: p90Slip, InitialEquityUSDT: initialEquity, FinalEquityUSDT: b.Account.Core.EquityUSDT,
		NetReturnFraction: netReturnFraction(initialEquity, b.Account.Core.EquityUSDT),
		NetReturnNonNegative: b.Account.Core.EquityUSDT >= initialEquity,
		TriggersSeen: stats.TriggersSeen, TriggersTraded: stats.TriggersTraded,
		TriggersSkipped: stats.TriggersSkipped, TriggersMissed: stats.TriggersMissed,
		MaxDrawdownFraction: b.Account.Core.MaxDrawdown,
		LeverageBreach: b.Account.Core.LeverageBreach, SolvencyBreach: b.Account.Core.SolvencyBreach,
		TerminalRiskCeilingHit: terminalHit,
	}, daily, stats
}

func replayTriggerViaDecideBar(
	b *personalbrain.Brain,
	ev *TriggerEvent,
	pb personalplaybook.PersonalWildPlaybook,
	step replayStep,
	delayBars int,
	costStress float64,
	p90Slip bool,
	sizing FrozenSizingInput,
	initialEquity float64,
	proofs ProofBundle,
	stats *ReplayStats,
) error {
	if ev.EnterGate == personalbrain.EnterMissedCorrectly {
		stats.TriggersMissed++
		if ev.SubsequentlyProfitable {
			stats.MissedProfitableMove++
		}
		return nil
	}
	if ev.NoTrade || ev.FrozenAction == executionpolicy.ActionSkip {
		stats.TriggersSkipped++
		return nil
	}
	if ev.CapacityFail {
		stats.SkipCapacity++
		stats.CapacityFails++
		return nil
	}
	if pb.EntryNarrative.DynamicMinNotionalUSDT > initialEquity*0.5 {
		stats.SkipBelowMinNotional++
		return nil
	}
	if ev.ToxicRevalidate {
		stats.ToxicRevalidateCount++
	}
	if ev.ToxicExit {
		stats.ToxicExitCount++
	}

	bar := buildBarInputFromTrigger(ev, pb, step, delayBars, costStress, initialEquity, proofs)
	out, err := b.DecideBar(bar)
	if err != nil {
		return err
	}
	if out.AppliedAction == "LONG" || out.AppliedAction == "SHORT" {
		stats.TriggersTraded++
	}
	if p90Slip && stats.TriggersTraded > 0 {
		_ = sizing
	}

	exitStep := replayStep{
		time:     step.time.Add(time.Duration(ev.HorizonBarCount) * time.Hour),
		barIndex: ev.DecisionBarIndex + delayBars + ev.HorizonBarCount,
		prices:   map[string]float64{},
	}
	if px, ok := step.prices[ev.Symbol]; ok {
		exitStep.prices[ev.Symbol] = px
	}
	exitBar := buildBarInputFromTrigger(ev, pb, exitStep, delayBars, costStress, initialEquity, proofs)
	exitBar.Events[0].ThesisHealth = personalbrain.ThesisInvalidated
	exitBar.Events[0].CompleteBar = exitStep.barIndex
	exitBar.BarIndex = exitStep.barIndex
	if _, err := b.DecideBar(exitBar); err != nil {
		return err
	}
	return nil
}

func buildBarInputFromTrigger(
	ev *TriggerEvent,
	pb personalplaybook.PersonalWildPlaybook,
	step replayStep,
	delayBars int,
	costStress float64,
	initialEquity float64,
	proofs ProofBundle,
) personalbrain.BarInput {
	price := step.prices[ev.Symbol]
	if price <= 0 {
		price = 100
	}
	sim := personalbrain.ExecutionSimState{
		MakerFilled: ev.MakerFilled, MakerTouchedNotFilled: ev.MakerTouchedNotFilled,
		SubsequentlyProfitable: ev.SubsequentlyProfitable, PartialFill: ev.PartialFill,
		ToxicThroughPrice: ev.ThroughPrice, ToxicFastFullFill: ev.ToxicRevalidate,
		ThesisStillValid: !ev.ToxicExit,
		RemainingEdgeBPS: ev.ConservativeBPS,
	}
	feat := personalbrain.BarFeatures{
		Symbol: ev.Symbol, ProfileID: ev.ProfileID,
		ReturnBPS: ev.ReturnBPS, PriorReturnBPS: ev.PriorReturnBPS, Price: price,
	}
	matched := personalbrain.MatchPersonalPlaybooks([]personalplaybook.PersonalWildPlaybook{pb}, feat, ev.DecisionBarIndex)
	if len(matched) == 0 {
		matched = []personalbrain.MatchedPersonalEvent{{
			Playbook: pb, EventID: ev.EventID, EventFamily: ev.EventFamily, Direction: ev.Direction,
		}}
	}
	me := enrichMatchedEvent(matched[0], pb, proofs, ev.EventID, ev.ExitProofOK, ev.CrowdingPassed, exclusionmap.CrowdingStress{}, ev.FrozenAction)
	return personalbrain.BarInput{
		BarIndex: step.barIndex, DecisionTime: step.time,
		Features: feat, Events: []personalbrain.MatchedPersonalEvent{me},
		Announcements: proofs.Announcements, Prices: step.prices,
		ScenarioDelay: delayBars, CostStress: costStress, ExecutionSim: sim,
		CapitalTierUSDT: initialEquity,
	}
}

func sizingNotional(sizing FrozenSizingInput, equity float64, pb personalplaybook.PersonalWildPlaybook) float64 {
	if sizing.ReferenceNotional > 0 {
		return minFloat(sizing.ReferenceNotional, pb.RiskNarrative.MaxSizeUSDT)
	}
	return minFloat(equity, pb.RiskNarrative.MaxSizeUSDT)
}

type replayStep struct {
	time     time.Time
	barIndex int
	eventIDs []string
	prices   map[string]float64
}

func buildBrainReplayTimeline(events []TriggerEvent, barsBySymbol map[string][]profitmap.Bar, delayBars int) []replayStep {
	type keyed struct {
		time  time.Time
		index int
		ev    TriggerEvent
		kind  string
	}
	var items []keyed
	for _, ev := range events {
		decisionIdx := ev.DecisionBarIndex + delayBars
		t := ev.DecisionTime
		if delayBars > 0 {
			t = t.Add(time.Duration(delayBars) * time.Hour)
		}
		items = append(items, keyed{time: t, index: decisionIdx, ev: ev, kind: "decision"})
		exitIdx := decisionIdx + ev.HorizonBarCount
		exitTime := t.Add(time.Duration(ev.HorizonBarCount) * time.Hour)
		items = append(items, keyed{time: exitTime, index: exitIdx, ev: ev, kind: "exit"})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].time.Equal(items[j].time) {
			if items[i].index == items[j].index {
				return items[i].kind < items[j].kind
			}
			return items[i].index < items[j].index
		}
		return items[i].time.Before(items[j].time)
	})
	byTime := map[string]*replayStep{}
	var order []string
	for _, item := range items {
		key := item.time.UTC().Format(time.RFC3339Nano) + "|" + item.kind
		step, ok := byTime[key]
		if !ok {
			step = &replayStep{time: item.time, barIndex: item.index, prices: map[string]float64{}}
			byTime[key] = step
			order = append(order, key)
		}
		if item.kind == "decision" {
			step.eventIDs = append(step.eventIDs, item.ev.EventID)
		}
		if bars, ok := barsBySymbol[item.ev.Symbol]; ok && item.index < len(bars) {
			step.prices[item.ev.Symbol] = bars[item.index].Close
		} else {
			step.prices[item.ev.Symbol] = 100
		}
	}
	out := make([]replayStep, 0, len(order))
	for _, key := range order {
		out = append(out, *byTime[key])
	}
	return out
}

func buildReplayTimeline(events []TriggerEvent, barsBySymbol map[string][]profitmap.Bar, delayBars int) []replayStep {
	return buildBrainReplayTimeline(events, barsBySymbol, delayBars)
}

func buildTierReplayStats(events []TriggerEvent, daily DailyEquityReplay, stats ReplayStats) TierReplayStats {
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

// FrozenSizingFromEvents computes conservative mean edge from traded triggers only.
// Missed/unfilled triggers contribute 0 BPS to sizing.
func FrozenSizingFromEvents(events []TriggerEvent, tier int) FrozenSizingInput {
	if len(events) == 0 {
		return FrozenSizingInput{ReferenceNotional: float64(tier)}
	}
	sum, stressSum, n := 0.0, 0.0, 0.0
	ref := 0.0
	for _, ev := range events {
		if ev.EnterGate == personalbrain.EnterMissedCorrectly || ev.NoTrade || ev.FrozenAction == executionpolicy.ActionSkip {
			continue
		}
		if !ev.MakerFilled && ev.MakerTouchedNotFilled {
			continue
		}
		sum += ev.ConservativeBPS
		stressSum += ev.StressBPS
		n++
		if ev.ReferenceNotional > ref {
			ref = ev.ReferenceNotional
		}
	}
	if n == 0 {
		return FrozenSizingInput{ReferenceNotional: float64(tier)}
	}
	return FrozenSizingInput{
		ConservativeMeanBPS:       sum / n,
		StressConservativeMeanBPS: stressSum / n,
		ReferenceNotional:         ref,
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BuildMakerPathStats aggregates maker quadrants from all triggers.
func BuildMakerPathStats(events []TriggerEvent) MakerPathStats {
	triggers := make([]executionpolicy.TriggerRecord, 0, len(events))
	for _, ev := range events {
		tr := executionpolicy.TriggerRecord{
			TriggerID: ev.EventID, Symbol: ev.Symbol, Side: ev.Direction,
			MakerFilled: ev.MakerFilled, MakerTouched: ev.MakerTouchedNotFilled,
			SubsequentlyProfitable: ev.SubsequentlyProfitable,
			PartialFill: ev.PartialFill, ThroughPrice: ev.ThroughPrice,
			NetPnLUSDT: ev.NetPnLUSDT,
		}
		if ev.FastFill {
			tr.FillWaitMS = executionpolicy.FastFillThresholdMS / 2
		} else if ev.MakerFilled {
			tr.FillWaitMS = executionpolicy.FastFillThresholdMS * 2
		}
		triggers = append(triggers, tr)
	}
	stats := executionpolicy.BuildMakerStats(triggers)
	gate := executionpolicy.EvaluateMakerFillSelectionGate(stats)
	out := MakerPathStats{
		Quadrants: stats.Quadrants, PFill: stats.PFill,
		ExpectedNetGivenFilled: stats.ExpectedNetGivenFilled,
		PartialFillCount: stats.PartialFillCount, ThroughPriceCount: stats.ThroughPriceCount,
		MakerGateForbid: gate.ForbidMaker, MakerGateReasons: gate.Reasons,
	}
	for _, tr := range triggers {
		if tr.MakerFilled && tr.FillWaitMS <= executionpolicy.FastFillThresholdMS {
			out.FastFillCount++
		} else if tr.MakerFilled {
			out.SlowFillCount++
		}
	}
	return out
}
