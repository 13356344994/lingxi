package eventaccount

import (
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/portfoliosim"
)

// EventCandidate is one scored event awaiting queue admission.
type EventCandidate struct {
	EventID              string
	Symbol               string
	Direction            string
	EventFamily          string
	DecisionTime         time.Time
	DecisionBarIndex     int
	WindowEndBarIndex    int
	ConservativeEdgeUSDT float64
	StressLossUSDT       float64
	ReferenceNotional    float64
	MarginTimeSec        int64
	MinNotionalUSDT      float64
	LotNotionalUSDT      float64
	CostEstimateBPS      float64
	ScenarioDelay        int
	CostStress           float64
	DeadlineBarIndex     int
	InvalidatorAtBar     int
	PayerGoneAtBar       int
	CompleteAtBar        int
	OODAtBar             int
}

// EventSlot tracks one open event position keyed by event_id.
type EventSlot struct {
	Candidate   EventCandidate
	Notional    float64
	StressLoss  float64
	ExecuteBar  int
	StopState   StopErrorState
	ExitReason  string
	Closed      bool
}

// Account wraps G5 portfoliosim fixed-notional accounting with G6 event slots.
type Account struct {
	Core           *portfoliosim.Account
	InitialEquity  float64
	Slots          map[string]*EventSlot
	SymbolEvents   map[string]string
	OverlapRejects int
	QueueRejects   int
	ExitCounts     map[string]int
}

// NewAccount creates an empty G6 event account at initial equity.
func NewAccount(initialEquity float64) *Account {
	return &Account{
		Core:          portfoliosim.NewAccount(initialEquity),
		InitialEquity: initialEquity,
		Slots:         map[string]*EventSlot{},
		SymbolEvents:  map[string]string{},
		ExitCounts:    map[string]int{},
	}
}

// OpenSlots returns active (non-closed) event slots.
func (a *Account) OpenSlots() []*EventSlot {
	out := make([]*EventSlot, 0, len(a.Slots))
	for _, slot := range a.Slots {
		if slot != nil && !slot.Closed {
			out = append(out, slot)
		}
	}
	return out
}

// UsedAccountStress sums open event stress budgets.
func (a *Account) UsedAccountStress() float64 {
	total := 0.0
	for _, slot := range a.OpenSlots() {
		total += slot.StressLoss
	}
	return total
}

// UsedNotional sums fixed notionals across open slots.
func (a *Account) UsedNotional() float64 {
	return a.Core.TotalFixedNotional()
}

// AllowsNewEvent reports whether risk gates permit another event open.
func (a *Account) AllowsNewEvent() bool {
	return a.Core.AllowsRiskIncrease()
}

// PositionState returns G5 position state for one symbol.
func (a *Account) PositionState(symbol string) actionspace.PositionState {
	return a.Core.CurrentPositionState(symbol)
}

// MarkToMarket updates equity from cash plus unrealized PnL.
func (a *Account) MarkToMarket(prices map[string]float64) {
	a.Core.MarkToMarket(prices)
}

// ApplyHoldFunding deducts funding between decision times via G5 bundle.
func (a *Account) ApplyHoldFunding(bundle portfoliosim.FundingBundle, symbols []string, start, end time.Time, stress float64) {
	a.Core.ApplyHoldFunding(bundle, symbols, start, end, stress)
}
