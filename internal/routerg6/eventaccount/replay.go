package eventaccount

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/portfoliosim"
	g6protocol "lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/sizing"
)

// PendingEntry is one delayed event open awaiting execution.
type PendingEntry struct {
	Candidate     EventCandidate
	NotionalUSDT  float64
	StressLoss    float64
	ExecuteIndex  int
	ScenarioDelay int
	CostStress    float64
}

// PendingQueue holds delayed opens; mirrors G5 pending flush semantics.
type PendingQueue struct {
	Entries []PendingEntry
}

// Enqueue appends one pending entry.
func (q *PendingQueue) Enqueue(e PendingEntry) {
	q.Entries = append(q.Entries, e)
}

// FlushMatured extracts entries with ExecuteIndex <= maxExecuteIndex.
func (q *PendingQueue) FlushMatured(maxExecuteIndex int) []PendingEntry {
	if len(q.Entries) == 0 {
		return nil
	}
	var matured []PendingEntry
	remaining := q.Entries[:0]
	for _, e := range q.Entries {
		if e.ExecuteIndex <= maxExecuteIndex {
			matured = append(matured, e)
		} else {
			remaining = append(remaining, e)
		}
	}
	q.Entries = remaining
	sort.SliceStable(matured, func(i, j int) bool {
		if matured[i].ExecuteIndex != matured[j].ExecuteIndex {
			return matured[i].ExecuteIndex < matured[j].ExecuteIndex
		}
		return matured[i].Candidate.EventID < matured[j].Candidate.EventID
	})
	return matured
}

// DiscardAll drops unexpired entries at EOF.
func (q *PendingQueue) DiscardAll() int {
	n := len(q.Entries)
	q.Entries = nil
	return n
}

// ExecuteIndexForDelay returns decisionIndex+1+delay per frozen G6 rule.
func ExecuteIndexForDelay(decisionIndex, delayBars int) int {
	return portfoliosim.ExecuteIndexForDelay(decisionIndex, delayBars)
}

// PreparePendingExecution revalidates queued notional at execution equity (stale revalidate).
func PreparePendingExecution(entry PendingEntry, account *Account) (actionspace.Action, float64, float64, bool) {
	equity := account.Core.EquityUSDT
	if equity <= 0 || !account.AllowsNewEvent() {
		return actionspace.Action{}, 0, 0, false
	}
	sized := sizing.SizeEventNotional(sizing.Input{
		EquityUSDT:            equity,
		ConservativeEdgeUSDT:  entry.Candidate.ConservativeEdgeUSDT,
		StressLossUSDT:        entry.Candidate.StressLossUSDT,
		ReferenceNotionalUSDT: entry.Candidate.ReferenceNotional,
		MarginTimeSec:         entry.Candidate.MarginTimeSec,
		MinNotionalUSDT:       entry.Candidate.MinNotionalUSDT,
		LotNotionalUSDT:       entry.Candidate.LotNotionalUSDT,
		UsedAccountStressUSDT: account.UsedAccountStress(),
		UsedNotionalUSDT:      account.UsedNotional(),
	})
	if sized.Skipped || sized.NotionalUSDT <= 0 {
		return actionspace.Action{}, 0, 0, false
	}
	stress := sized.NotionalUSDT / entry.Candidate.ReferenceNotional * entry.Candidate.StressLossUSDT
	if entry.Candidate.ReferenceNotional <= 0 {
		stress = sized.StressBudgetUSDT
	}
	maxSize := sized.NotionalUSDT / (equity * float64(g6protocol.MaxAccountLeverage))
	if maxSize <= 1e-9 {
		return actionspace.Action{}, 0, 0, false
	}
	verb := actionspace.VerbOpenLong
	dir := actionspace.DirectionLong
	if entry.Candidate.Direction == "short" {
		verb = actionspace.VerbOpenShort
		dir = actionspace.DirectionShort
	}
	action := actionspace.Action{
		Symbol:        entry.Candidate.Symbol,
		Verb:          verb,
		Direction:     dir,
		TargetHorizon: "event",
		SizeFraction:  maxSize,
		DelayBars:     entry.ScenarioDelay,
	}
	return action, sized.NotionalUSDT, stress, true
}

// executionCost applies G4/G6 conservative cost proxy with optional stress multiplier.
func executionCost(turnover, costEstimateBPS, stress float64) float64 {
	if turnover <= 0 {
		return 0
	}
	bps := (g6protocol.RoundtripTakerFeeBPS + g6protocol.BaseSpreadSlippageFloorBPS + math.Max(0, costEstimateBPS)) * stress
	return turnover * bps / 10000
}

// openEventSlot records a newly opened event after execution.
func openEventSlot(account *Account, entry PendingEntry, notional, stress float64, executeBar int) {
	slot := &EventSlot{
		Candidate:  entry.Candidate,
		Notional:   notional,
		StressLoss: stress,
		ExecuteBar: executeBar,
		StopState:  StopErrorHolding,
	}
	account.Slots[entry.Candidate.EventID] = slot
	account.SymbolEvents[entry.Candidate.Symbol] = entry.Candidate.EventID
}

// closeEventSlot closes one event via StopError flat action at price.
func closeEventSlot(account *Account, slot *EventSlot, symbol string, price, costUSDT float64, reason string) error {
	state := account.Core.CurrentPositionState(symbol)
	if state.Side == actionspace.SideFlat {
		slot.Closed = true
		slot.StopState = StopErrorExit
		slot.ExitReason = reason
		delete(account.SymbolEvents, symbol)
		return nil
	}
	action := actionspace.Action{Verb: actionspace.VerbClose, TargetHorizon: "event", DelayBars: 0}
	if err := account.Core.ApplyExecution(symbol, state, action, price, costUSDT, 0); err != nil {
		return err
	}
	slot.Closed = true
	slot.StopState = StopErrorExit
	slot.ExitReason = reason
	delete(account.SymbolEvents, symbol)
	account.ExitCounts[reason]++
	return nil
}

// flushPending executes matured pending opens before scoring new events.
func flushPending(account *Account, queue *PendingQueue, matured []PendingEntry, entryPrice func(symbol string, executeIndex int) (float64, bool)) bool {
	traded := false
	for _, entry := range matured {
		action, notional, stress, ok := PreparePendingExecution(entry, account)
		if !ok {
			account.Core.PendingRejects++
			continue
		}
		price, ok := entryPrice(entry.Candidate.Symbol, entry.ExecuteIndex)
		if !ok || price <= 0 {
			account.Core.StaleRejects++
			continue
		}
		state := account.Core.CurrentPositionState(entry.Candidate.Symbol)
		cost := executionCost(notional, entry.Candidate.CostEstimateBPS, entry.CostStress)
		if err := account.Core.ApplyExecution(entry.Candidate.Symbol, state, action, price, cost, 0); err != nil {
			account.Core.PendingRejects++
			continue
		}
		openEventSlot(account, entry, notional, stress, entry.ExecuteIndex)
		traded = true
	}
	return traded
}

// BarReplayInput binds one decision bar for event account replay.
type BarReplayInput struct {
	BarIndex     int
	DecisionTime time.Time
	Candidates   []EventCandidate
	Prices       map[string]float64
	ScenarioDelay int
	CostStress   float64
}

// ReplayBar executes one bar: flush pending, exits, solvency, then queue new events.
func ReplayBar(account *Account, queue *PendingQueue, in BarReplayInput, entryPrice func(symbol string, executeIndex int) (float64, bool)) {
	account.MarkToMarket(in.Prices)
	matured := queue.FlushMatured(in.BarIndex)
	_ = flushPending(account, queue, matured, entryPrice)

	account.MarkToMarket(in.Prices)
	processExits(account, in, entryPrice)
	_ = ApplySolvencyTrims(account, in.BarIndex, func(a *Account, slot *EventSlot, reason string) error {
		price, ok := entryPrice(slot.Candidate.Symbol, in.BarIndex)
		if !ok {
			price = in.Prices[slot.Candidate.Symbol]
		}
		cost := executionCost(slot.Notional, slot.Candidate.CostEstimateBPS, in.CostStress)
		return closeEventSlot(a, slot, slot.Candidate.Symbol, price, cost, reason)
	})

	if !account.AllowsNewEvent() {
		return
	}
	admit := QueueAdmission{MaxConcurrent: sizing.MaxConcurrent(account.InitialEquity)}
	accepted, _ := admit.Select(account, in.Candidates)
	for _, c := range accepted {
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
		if sized.Skipped {
			continue
		}
		stress := sized.NotionalUSDT / c.ReferenceNotional * c.StressLossUSDT
		if c.ReferenceNotional <= 0 {
			stress = sized.StressBudgetUSDT
		}
		delay := in.ScenarioDelay
		if c.ScenarioDelay > 0 {
			delay = c.ScenarioDelay
		}
		stressMult := in.CostStress
		if c.CostStress > 0 {
			stressMult = c.CostStress
		}
		queue.Enqueue(PendingEntry{
			Candidate:     c,
			NotionalUSDT:  sized.NotionalUSDT,
			StressLoss:    stress,
			ExecuteIndex:  ExecuteIndexForDelay(in.BarIndex, delay),
			ScenarioDelay: delay,
			CostStress:    stressMult,
		})
	}
}

func processExits(account *Account, in BarReplayInput, entryPrice func(symbol string, executeIndex int) (float64, bool)) {
	for _, slot := range account.OpenSlots() {
		sig := ExitSignal{
			BarIndex:          in.BarIndex,
			InvalidatorFired:  slot.Candidate.InvalidatorAtBar > 0 && in.BarIndex >= slot.Candidate.InvalidatorAtBar,
			PayerGone:         slot.Candidate.PayerGoneAtBar > 0 && in.BarIndex >= slot.Candidate.PayerGoneAtBar,
			EventComplete:     slot.Candidate.CompleteAtBar > 0 && in.BarIndex >= slot.Candidate.CompleteAtBar,
			OutOfDistribution: slot.Candidate.OODAtBar > 0 && in.BarIndex >= slot.Candidate.OODAtBar,
			DeadlineReached:   slot.Candidate.DeadlineBarIndex > 0 && in.BarIndex >= slot.Candidate.DeadlineBarIndex,
		}
		next, reason, exit := EvaluateStopError(slot.StopState, sig)
		if !exit {
			continue
		}
		price, ok := entryPrice(slot.Candidate.Symbol, in.BarIndex)
		if !ok {
			price = in.Prices[slot.Candidate.Symbol]
		}
		cost := executionCost(slot.Notional, slot.Candidate.CostEstimateBPS, in.CostStress)
		_ = closeEventSlot(account, slot, slot.Candidate.Symbol, price, cost, reason)
		slot.StopState = next
	}
}

// ReplayScenario replays a slice of bars with funding between decision times.
func ReplayScenario(
	account *Account,
	bars []BarReplayInput,
	funding portfoliosim.FundingBundle,
	entryPrice func(symbol string, executeIndex int) (float64, bool),
) {
	queue := &PendingQueue{}
	var prev time.Time
	for i, bar := range bars {
		if i > 0 && !prev.IsZero() {
			symbols := openSymbols(account)
			account.ApplyHoldFunding(funding, symbols, prev, bar.DecisionTime, bar.CostStress)
			account.MarkToMarket(bar.Prices)
		}
		ReplayBar(account, queue, bar, entryPrice)
		prev = bar.DecisionTime
	}
	_ = queue.DiscardAll()
}

func openSymbols(account *Account) []string {
	syms := make([]string, 0, len(account.SymbolEvents))
	for sym := range account.SymbolEvents {
		syms = append(syms, sym)
	}
	sort.Strings(syms)
	return syms
}
