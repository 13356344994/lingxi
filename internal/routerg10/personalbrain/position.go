package personalbrain

import (
	"math"
	"sort"
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg6/eventaccount"
)

// NewBrain creates a G10P brain at initial equity with empty account and trace log.
func NewBrain(cfg Config) *Brain {
	if cfg.ProtocolHash == "" {
		cfg.ProtocolHash = ProtocolHash
	}
	if !cfg.TradeAllowed {
		cfg.TradeAllowed = TradeAllowedDefault
	}
	return &Brain{
		Config:  cfg,
		Account: eventaccount.NewAccount(cfg.InitialEquityUSDT),
		Pending: &eventaccount.PendingQueue{},
		Trace:   NewTraceLog(),
	}
}

func flushPending(b *Brain, matured []eventaccount.PendingEntry, in BarInput, priceFn func(string, int) (float64, bool)) bool {
	traded := false
	for _, entry := range matured {
		action, notional, stress, ok := eventaccount.PreparePendingExecution(entry, b.Account)
		if !ok {
			b.Account.Core.PendingRejects++
			continue
		}
		price, ok := priceFn(entry.Candidate.Symbol, entry.ExecuteIndex)
		if !ok || price <= 0 {
			b.Account.Core.StaleRejects++
			continue
		}
		state := b.Account.Core.CurrentPositionState(entry.Candidate.Symbol)
		cost := executionCost(notional, entry.Candidate.CostEstimateBPS, entry.CostStress)
		if err := b.Account.Core.ApplyExecution(entry.Candidate.Symbol, state, action, price, cost, 0); err != nil {
			b.Account.Core.PendingRejects++
			continue
		}
		openSlot(b, entry, notional, stress, entry.ExecuteIndex)
		traded = true
	}
	return traded
}

func openSlot(b *Brain, entry eventaccount.PendingEntry, notional, stress float64, executeBar int) {
	slot := &eventaccount.EventSlot{
		Candidate:  entry.Candidate,
		Notional:   notional,
		StressLoss: stress,
		ExecuteBar: executeBar,
		StopState:  eventaccount.StopErrorHolding,
	}
	b.Account.Slots[entry.Candidate.EventID] = slot
	b.Account.SymbolEvents[entry.Candidate.Symbol] = entry.Candidate.EventID
}

func closeSlot(b *Brain, slot *eventaccount.EventSlot, price, costStress float64, reason string) error {
	symbol := slot.Candidate.Symbol
	state := b.Account.Core.CurrentPositionState(symbol)
	if state.Side == actionspace.SideFlat {
		slot.Closed = true
		slot.StopState = eventaccount.StopErrorExit
		slot.ExitReason = reason
		delete(b.Account.SymbolEvents, symbol)
		return nil
	}
	action := actionspace.Action{Verb: actionspace.VerbClose, TargetHorizon: "event", DelayBars: 0}
	cost := executionCost(slot.Notional, slot.Candidate.CostEstimateBPS, costStress)
	if err := b.Account.Core.ApplyExecution(symbol, state, action, price, cost, 0); err != nil {
		return err
	}
	slot.Closed = true
	slot.StopState = eventaccount.StopErrorExit
	slot.ExitReason = reason
	delete(b.Account.SymbolEvents, symbol)
	b.Account.ExitCounts[reason]++
	return nil
}

func applyTrim(b *Brain, symbol string, in BarInput) {
	state := b.Account.Core.CurrentPositionState(symbol)
	if state.Side == actionspace.SideFlat || state.SizeFraction <= 0 {
		return
	}
	trim := actionspace.Action{
		Verb:          actionspace.VerbTrim,
		TargetHorizon: "event",
		SizeFraction:  state.SizeFraction * 0.5,
		DelayBars:     0,
	}
	price := in.Prices[symbol]
	if price <= 0 {
		price = in.Features.Price
	}
	_ = b.Account.Core.ApplyExecution(symbol, state, trim, price, 0, 0)
}

func applyClose(b *Brain, symbol string, in BarInput, reason string) {
	for _, slot := range b.Account.OpenSlots() {
		if slot.Candidate.Symbol != symbol {
			continue
		}
		price := in.Prices[symbol]
		if price <= 0 {
			price = in.Features.Price
		}
		_ = closeSlot(b, slot, price, in.CostStress, reason)
	}
}

func executionCost(turnover, costEstimateBPS, stress float64) float64 {
	if turnover <= 0 {
		return 0
	}
	if stress <= 0 {
		stress = 1
	}
	bps := (10.0 + 2.0 + math.Max(0, costEstimateBPS)) * stress
	return turnover * bps / 10000
}

func entryPriceAt(in BarInput) func(string, int) (float64, bool) {
	return func(symbol string, _ int) (float64, bool) {
		if p, ok := in.Prices[symbol]; ok && p > 0 {
			return p, true
		}
		if in.Features.Symbol == symbol && in.Features.Price > 0 {
			return in.Features.Price, true
		}
		return 0, false
	}
}

func makeEventCandidate(cfg Config, ev MatchedPersonalEvent, in BarInput, dir string, edge, stress, notional float64) eventaccount.EventCandidate {
	return eventaccount.EventCandidate{
		EventID:              ev.EventID,
		Symbol:               in.Features.Symbol,
		Direction:            dir,
		EventFamily:          ev.EventFamily,
		DecisionTime:         in.DecisionTime,
		DecisionBarIndex:     in.BarIndex,
		WindowEndBarIndex:    ev.DeadlineBar,
		ConservativeEdgeUSDT: edge,
		StressLossUSDT:       stress,
		ReferenceNotional:    notional,
		MarginTimeSec:        int64(ev.CompletionBars * 3600),
		MinNotionalUSDT:      cfg.MinNotionalUSDT,
		LotNotionalUSDT:      cfg.LotStep * cfg.ContractValue,
		DeadlineBarIndex:     ev.DeadlineBar,
		InvalidatorAtBar:     ev.InvalidatorBar,
		CompleteAtBar:        ev.CompleteBar,
		PayerGoneAtBar:       ev.PayerGoneBar,
		OODAtBar:             ev.OODBar,
		ScenarioDelay:        in.ScenarioDelay,
		CostStress:           in.CostStress,
	}
}

func positionSize(b *Brain, symbol string) float64 {
	return actionspace.NotionalUSDT(b.Account.Core.CurrentPositionState(symbol))
}

func hasPosition(b *Brain, symbol string) bool {
	return b.Account.Core.CurrentPositionState(symbol).Side != actionspace.SideFlat
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func appendUnique(dst []string, items ...string) []string {
	for _, item := range items {
		found := false
		for _, d := range dst {
			if d == item {
				found = true
				break
			}
		}
		if !found && item != "" {
			dst = append(dst, item)
		}
	}
	return dst
}

// ApplyHoldFunding applies G5 funding between bars.
func (b *Brain) ApplyHoldFunding(bundle portfoliosim.FundingBundle, symbols []string, start, end time.Time, stress float64) {
	b.Account.ApplyHoldFunding(bundle, symbols, start, end, stress)
}

// DiscardPending drops unexpired pending at EOF per frozen rule.
func (b *Brain) DiscardPending() int {
	return b.Pending.DiscardAll()
}

// OpenSymbols returns symbols with active event slots.
func (b *Brain) OpenSymbols() []string {
	syms := make([]string, 0, len(b.Account.SymbolEvents))
	for sym := range b.Account.SymbolEvents {
		syms = append(syms, sym)
	}
	sort.Strings(syms)
	return syms
}

// EventDecisionTime returns canonical decision timestamp for hashing.
func EventDecisionTime(t time.Time) time.Time {
	return t.UTC()
}
