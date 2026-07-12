package walkforward

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg6/eventaccount"
	g6protocol "lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/sizing"
)

// DailyEquityReplay tracks per-day equity deltas for sign and robustness tests.
type DailyEquityReplay struct {
	DailyPnL    map[string]float64
	ShockDayPnL map[string]float64
	ClusterPnL  map[string]float64
	SymbolPnL   map[string]float64
}

// ReplayStats aggregates execution feasibility counters.
type ReplayStats struct {
	EventsSeen           int
	EventsTraded         int
	EventsSkipped        int
	SkipBelowMinNotional int
	SkipBelowLotFloor    int
	SkipCapacity         int
	SkipConcurrencyCap   int
}

func (s *ReplayStats) add(other ReplayStats) {
	s.EventsSeen += other.EventsSeen
	s.EventsTraded += other.EventsTraded
	s.EventsSkipped += other.EventsSkipped
	s.SkipBelowMinNotional += other.SkipBelowMinNotional
	s.SkipBelowLotFloor += other.SkipBelowLotFloor
	s.SkipCapacity += other.SkipCapacity
	s.SkipConcurrencyCap += other.SkipConcurrencyCap
}

// ReplayFoldEvents replays one fold's events for one capital tier across delay/cost scenarios.
func ReplayFoldEvents(
	events []WFEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	sizingInput FrozenSizingInput,
) ([]TierScenarioResult, DailyEquityReplay, ReplayStats) {
	scenarios := make([]TierScenarioResult, 0, len(g6protocol.DelayBars)*2)
	var daily DailyEquityReplay
	var stats ReplayStats
	for _, delay := range g6protocol.DelayBars {
		for _, stressMult := range []float64{1.0, g6protocol.CostStressMultiplier} {
			if stressMult > 1.0+1e-9 && delay != 0 {
				continue
			}
			result, dayTrack, replayStats := replayScenario(events, barsBySymbol, fundingBySymbol, initialEquity, delay, stressMult, sizingInput)
			scenarios = append(scenarios, result)
			if delay == 0 && stressMult <= 1.0+1e-9 {
				mergeDaily(&daily, dayTrack)
			}
			if delay == 0 && stressMult <= 1.0+1e-9 {
				stats.add(replayStats)
			}
		}
	}
	return scenarios, daily, stats
}

func replayScenario(
	events []WFEvent,
	barsBySymbol map[string][]profitmap.Bar,
	fundingBySymbol map[string][]profitmap.FundingPoint,
	initialEquity float64,
	delayBars int,
	costStress float64,
	sizingInput FrozenSizingInput,
) (TierScenarioResult, DailyEquityReplay, ReplayStats) {
	account := eventaccount.NewAccount(initialEquity)
	stats := ReplayStats{EventsSeen: len(events)}
	if len(events) == 0 {
		return TierScenarioResult{
			CapitalTier:          int(initialEquity),
			DelayBars:            delayBars,
			CostStressMultiplier: costStress,
			InitialEquityUSDT:    initialEquity,
			FinalEquityUSDT:      initialEquity,
		}, DailyEquityReplay{
			DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
			ClusterPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
		}, stats
	}

	sorted := append([]WFEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].DecisionTime.Equal(sorted[j].DecisionTime) {
			return sorted[i].EventID < sorted[j].EventID
		}
		return sorted[i].DecisionTime.Before(sorted[j].DecisionTime)
	})

	steps := buildReplayTimeline(sorted, barsBySymbol, delayBars, sizingInput)

	daily := DailyEquityReplay{
		DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
		ClusterPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
	}
	fundingBundle := BuildFundingBundle(fundingBySymbol)
	prevEquity := initialEquity
	queue := &eventaccount.PendingQueue{}
	var prev time.Time
	for _, step := range steps {
		if !prev.IsZero() {
			symbols := openSymbols(account)
			account.ApplyHoldFunding(fundingBundle, symbols, prev, step.time, costStress)
			account.MarkToMarket(step.prices)
		}
		before := account.Core.EquityUSDT
		eventaccount.ReplayBar(account, queue, eventaccount.BarReplayInput{
			BarIndex:      step.barIndex,
			DecisionTime:  step.time,
			Candidates:    step.candidates,
			Prices:        step.prices,
			ScenarioDelay: delayBars,
			CostStress:    costStress,
		}, entryPriceFor(barsBySymbol))
		after := account.Core.EquityUSDT
		delta := after - before
		if math.Abs(delta) > 1e-9 {
			day := step.time.UTC().Format("2006-01-02")
			daily.DailyPnL[day] += delta
			for _, c := range step.candidates {
				daily.SymbolPnL[c.Symbol] += delta / float64(len(step.candidates))
				for _, ev := range sorted {
					if ev.EventID == c.EventID {
						if ev.ShockCluster != "" && ev.ShockCluster != "none" {
							daily.ShockDayPnL[ev.ShockCluster] += delta / float64(len(step.candidates))
							daily.ClusterPnL[ev.ShockCluster] += delta / float64(len(step.candidates))
						}
						break
					}
				}
			}
		}
		prevEquity = after
		prev = step.time
	}
	_ = prevEquity

	stats.EventsSkipped = account.QueueRejects + account.OverlapRejects + account.Core.PendingRejects + account.Core.StaleRejects
	stats.EventsTraded = countClosedSlots(account)
	stats.SkipConcurrencyCap = account.QueueRejects
	for _, ev := range sorted {
		if ev.CapacityFail {
			stats.SkipCapacity++
		}
	}
	for _, step := range steps {
		for _, c := range step.candidates {
			countSizingSkip(&stats, account, c)
		}
	}

	return TierScenarioResult{
		CapitalTier:          int(initialEquity),
		DelayBars:            delayBars,
		CostStressMultiplier: costStress,
		InitialEquityUSDT:    initialEquity,
		FinalEquityUSDT:      account.Core.EquityUSDT,
		NetReturnFraction:    netReturnFraction(initialEquity, account.Core.EquityUSDT),
		NetReturnPositive:    account.Core.EquityUSDT > initialEquity,
		EventsSeen:           stats.EventsSeen,
		EventsTraded:         stats.EventsTraded,
		EventsSkipped:        stats.EventsSkipped,
		SkipBelowMinNotional: stats.SkipBelowMinNotional,
		SkipBelowLotFloor:    stats.SkipBelowLotFloor,
		SkipCapacity:         stats.SkipCapacity,
		SkipConcurrencyCap:   stats.SkipConcurrencyCap,
		MaxDrawdownFraction:  account.Core.MaxDrawdown,
		LeverageBreach:       account.Core.LeverageBreach,
		SolvencyBreach:       account.Core.SolvencyBreach,
	}, daily, stats
}

func wfEventToCandidate(ev WFEvent, delayBars int, sizingInput FrozenSizingInput) eventaccount.EventCandidate {
	ref := sizingInput.ReferenceNotional
	if ref <= 0 {
		ref = ev.ReferenceNotional
	}
	edgeBPS := sizingInput.ConservativeMeanBPS
	if edgeBPS <= 0 {
		edgeBPS = g6protocol.WFSizingEdgeFloorBPS
	}
	stressBPS := sizingInput.StressConservativeMeanBPS
	edgeUSDT := edgeBPS * ref / 10000
	stressUSDT := math.Max(stressBPS*ref/10000, ref*g6protocol.MaxEventStressLossFraction)
	if stressUSDT <= 0 {
		stressUSDT = ref * g6protocol.MaxEventStressLossFraction
	}
	exitBar := ev.DecisionBarIndex + 1 + delayBars + ev.HorizonBarCount
	if exitBar <= 0 {
		exitBar = ev.CompleteAtBar
	}
	return eventaccount.EventCandidate{
		EventID:              ev.EventID,
		Symbol:               ev.Symbol,
		Direction:            ev.Direction,
		EventFamily:          ev.EventFamily,
		DecisionTime:         ev.DecisionTime,
		DecisionBarIndex:     ev.DecisionBarIndex,
		WindowEndBarIndex:    exitBar,
		ConservativeEdgeUSDT: edgeUSDT,
		StressLossUSDT:       stressUSDT,
		ReferenceNotional:    ref,
		MarginTimeSec:        ev.MarginTimeSec,
		MinNotionalUSDT:      MinNotionalProxyUSDT,
		LotNotionalUSDT:      LotNotionalProxyUSDT,
		CostEstimateBPS:      ev.CostEstimateBPS,
		ScenarioDelay:        delayBars,
		DeadlineBarIndex:     exitBar,
		CompleteAtBar:        exitBar,
	}
}

type timelineStep struct {
	time       time.Time
	barIndex   int
	candidates []eventaccount.EventCandidate
	prices     map[string]float64
}

func buildReplayTimeline(events []WFEvent, barsBySymbol map[string][]profitmap.Bar, delayBars int, sizingInput FrozenSizingInput) []*timelineStep {
	if len(events) == 0 {
		return nil
	}
	byTime := map[string]*timelineStep{}
	for _, ev := range events {
		bars, ok := barsBySymbol[ev.Symbol]
		if !ok || ev.DecisionBarIndex < 0 || ev.DecisionBarIndex >= len(bars) {
			continue
		}
		candidate := wfEventToCandidate(ev, delayBars, sizingInput)
		exitBar := candidate.CompleteAtBar
		if exitBar <= 0 {
			exitBar = ev.DecisionBarIndex
		}
		for idx := ev.DecisionBarIndex; idx <= exitBar && idx < len(bars); idx++ {
			t := bars[idx].OpenTime
			key := t.UTC().Format(time.RFC3339Nano)
			step, ok := byTime[key]
			if !ok {
				step = &timelineStep{
					time:     t,
					barIndex: idx,
					prices:   map[string]float64{},
				}
				byTime[key] = step
			}
			step.prices[ev.Symbol] = bars[idx].Close
			if idx > step.barIndex {
				step.barIndex = idx
			}
		}
		decisionKey := ev.DecisionTime.UTC().Format(time.RFC3339Nano)
		step := byTime[decisionKey]
		if step == nil {
			step = &timelineStep{
				time:     ev.DecisionTime,
				barIndex: ev.DecisionBarIndex,
				prices:   map[string]float64{},
			}
			byTime[decisionKey] = step
		}
		step.candidates = append(step.candidates, candidate)
		if step.prices[ev.Symbol] == 0 && ev.DecisionBarIndex < len(bars) {
			step.prices[ev.Symbol] = bars[ev.DecisionBarIndex].Close
		}
	}
	steps := make([]*timelineStep, 0, len(byTime))
	for _, step := range byTime {
		steps = append(steps, step)
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].time.Equal(steps[j].time) {
			return steps[i].barIndex < steps[j].barIndex
		}
		return steps[i].time.Before(steps[j].time)
	})
	return steps
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
	if dst.ClusterPnL == nil {
		dst.ClusterPnL = map[string]float64{}
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
	for k, v := range src.ClusterPnL {
		dst.ClusterPnL[k] += v
	}
	for k, v := range src.SymbolPnL {
		dst.SymbolPnL[k] += v
	}
}

// SummarizeFoldResult picks base scenario metrics for one fold/tier.
func SummarizeFoldResult(foldIndex int, capitalTier int, scenarios []TierScenarioResult) FoldResult {
	base := baseScenario(scenarios)
	result := FoldResult{FoldIndex: foldIndex, CapitalTier: capitalTier, Scenarios: scenarios}
	if base != nil {
		result.BaseNetReturnFraction = base.NetReturnFraction
		result.NetReturnPositive = base.NetReturnFraction > 0
	}
	return result
}

func baseScenario(scenarios []TierScenarioResult) *TierScenarioResult {
	for i := range scenarios {
		if scenarios[i].DelayBars == 0 && scenarios[i].CostStressMultiplier <= 1.0+1e-9 {
			return &scenarios[i]
		}
	}
	if len(scenarios) > 0 {
		return &scenarios[0]
	}
	return nil
}

// PositiveReturn reports whether final equity exceeds initial.
func PositiveReturn(initial, final float64) bool {
	return final > initial
}

// BuildFundingBundle converts per-symbol funding points for replay.
func BuildFundingBundle(bySymbol map[string][]profitmap.FundingPoint) portfoliosim.FundingBundle {
	return portfoliosim.FundingBundle{BySymbol: bySymbol}
}

// MaxConcurrentForTier returns frozen concurrent cap for one equity tier.
func MaxConcurrentForTier(equity float64) int {
	return sizing.MaxConcurrent(equity)
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
		EquityUSDT:            account.Core.EquityUSDT,
		ConservativeEdgeUSDT:  c.ConservativeEdgeUSDT,
		StressLossUSDT:        c.StressLossUSDT,
		ReferenceNotionalUSDT: c.ReferenceNotional,
		MarginTimeSec:         c.MarginTimeSec,
		MinNotionalUSDT:       c.MinNotionalUSDT,
		LotNotionalUSDT:       c.LotNotionalUSDT,
		UsedAccountStressUSDT: account.UsedAccountStress(),
		UsedNotionalUSDT:      account.UsedNotional(),
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
