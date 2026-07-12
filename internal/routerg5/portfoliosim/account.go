package portfoliosim

import (
	"fmt"
	"math"

	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// NewAccount creates an empty account at initial equity.
func NewAccount(initialEquity float64) *Account {
	return &Account{
		CashUSDT:      initialEquity,
		EquityUSDT:    initialEquity,
		InitialEquity: initialEquity,
		Positions:     map[string]actionspace.PositionState{},
		PeakEquity:    initialEquity,
		ActiveDays:    map[string]bool{},
		SymbolPnL:     map[string]float64{},
		ActionCounts:  map[string]int{},
	}
}

// PositionUnrealizedPnL marks one position at price without realizing.
func PositionUnrealizedPnL(state actionspace.PositionState, price float64) float64 {
	if state.Side == actionspace.SideFlat || state.SizeFraction <= 0 || state.BasisPrice <= 0 || price <= 0 {
		return 0
	}
	notional := actionspace.NotionalUSDT(state)
	var grossBPS float64
	switch state.Side {
	case actionspace.SideLong:
		grossBPS = (price/state.BasisPrice - 1) * 10000
	case actionspace.SideShort:
		grossBPS = (1 - price/state.BasisPrice) * 10000
	default:
		return 0
	}
	return notional * grossBPS / 10000
}

// TotalFixedNotional sums frozen entry notionals across open positions.
func (a *Account) TotalFixedNotional() float64 {
	total := 0.0
	for _, pos := range a.Positions {
		total += actionspace.NotionalUSDT(pos)
	}
	return total
}

// EffectiveLeverage returns sum(fixed notional) / equity after mark-to-market.
func (a *Account) EffectiveLeverage() float64 {
	if a.EquityUSDT <= 0 {
		return math.Inf(1)
	}
	return a.TotalFixedNotional() / a.EquityUSDT
}

// AllowsIncrease reports whether account risk gates permit new exposure.
func (a *Account) AllowsIncrease() bool {
	return a.AllowsRiskIncrease()
}

// AllowsRiskIncrease reports whether open/increase/reverse risk actions are permitted.
func (a *Account) AllowsRiskIncrease() bool {
	return !a.LeverageBreach && !a.SolvencyBreach
}

func (a *Account) checkRiskAfterMark() {
	if a.EquityUSDT <= 0 {
		a.SolvencyBreach = true
	}
	if a.EffectiveLeverage() > float64(g5protocol.MaxAccountLeverage)+1e-9 {
		a.LeverageBreach = true
	}
	if a.CashUSDT <= 0 && len(a.Positions) == 0 {
		a.SolvencyBreach = true
	}
}

// MarkToMarket updates equity from cash plus per-position unrealized PnL.
func (a *Account) MarkToMarket(prices map[string]float64) {
	equity := a.CashUSDT
	for symbol, pos := range a.Positions {
		price := prices[symbol]
		if price <= 0 {
			price = pos.BasisPrice
		}
		equity += PositionUnrealizedPnL(pos, price)
	}
	a.EquityUSDT = equity
	if a.EquityUSDT > a.PeakEquity {
		a.PeakEquity = a.EquityUSDT
	}
	if a.PeakEquity > 0 {
		dd := (a.PeakEquity - a.EquityUSDT) / a.PeakEquity
		if dd > a.MaxDrawdown {
			a.MaxDrawdown = dd
		}
	}
	a.checkRiskAfterMark()
}

// ApplyExecution applies one accepted action with entry price, costs, and optional funding.
func (a *Account) ApplyExecution(symbol string, startState actionspace.PositionState, action actionspace.Action, entryPrice float64, costUSDT, fundingUSDT float64) error {
	if actionspace.IncreasesOrMaintainsRisk(action.Verb) && !a.AllowsRiskIncrease() {
		return fmt.Errorf("portfoliosim: %s rejected after leverage/solvency breach", action.Verb)
	}
	startState.EquityUSDT = a.EquityUSDT
	prevUnrealized := PositionUnrealizedPnL(startState, entryPrice)
	post, turnover, err := actionspace.ApplyTransition(startState, action, entryPrice)
	if err != nil {
		return err
	}
	post.EquityUSDT = a.EquityUSDT
	newUnrealized := PositionUnrealizedPnL(post, entryPrice)
	realized := prevUnrealized - newUnrealized
	delta := realized - costUSDT - fundingUSDT
	a.CashUSDT += delta
	a.TurnoverUSDT += turnover
	a.ActionCounts[action.Verb]++
	a.SymbolPnL[symbol] += delta
	if post.Side == actionspace.SideFlat {
		delete(a.Positions, symbol)
	} else {
		a.Positions[symbol] = post
	}
	a.checkRiskAfterMark()
	return nil
}

// CurrentPositionState returns live position state for one symbol.
func (a *Account) CurrentPositionState(symbol string) actionspace.PositionState {
	pos, ok := a.Positions[symbol]
	if !ok {
		return actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: a.EquityUSDT}
	}
	return pos
}

// RecordDecision tracks activity for coverage metrics.
func (a *Account) RecordDecision(day string, traded bool) {
	a.TotalBars++
	if !traded {
		a.NoTradeBars++
	}
	if traded {
		a.ActiveDays[day] = true
	}
}

// FinalizeStats returns aggregate metrics.
func (a *Account) FinalizeStats() (activeDays int, noTradeFraction float64, maxSymbolContribution float64) {
	activeDays = len(a.ActiveDays)
	if a.TotalBars > 0 {
		noTradeFraction = float64(a.NoTradeBars) / float64(a.TotalBars)
	}
	var totalPositive float64
	for _, pnl := range a.SymbolPnL {
		if pnl > 0 {
			totalPositive += pnl
		}
	}
	if totalPositive <= 0 {
		return activeDays, noTradeFraction, 0
	}
	maxSym := 0.0
	for _, pnl := range a.SymbolPnL {
		if pnl > 0 && pnl/totalPositive > maxSym {
			maxSym = pnl / totalPositive
		}
	}
	return activeDays, noTradeFraction, maxSym
}

// CapacityRejectRate returns rejected/total proposals.
func (a *Account) CapacityRejectRate() float64 {
	if a.TotalProposals == 0 {
		return 0
	}
	return float64(a.CapacityRejects) / float64(a.TotalProposals)
}

// EquitySummary builds compact curve digest.
func (a *Account) EquitySummary(points int) EquityCurveSummary {
	return EquityCurveSummary{
		StartEquity:  a.InitialEquity,
		EndEquity:    a.EquityUSDT,
		PeakEquity:   a.PeakEquity,
		TroughEquity: math.Max(0, a.PeakEquity*(1-a.MaxDrawdown)),
		Points:       points,
	}
}
