package portfoliosim

import (
	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// PreparePendingExecution rescales a queued proposal to the approved notional cap at execution equity.
func PreparePendingExecution(pa PendingAction, account *Account) (actionspace.Action, bool) {
	prop := pa.Proposal
	equity := account.EquityUSDT
	if equity <= 0 {
		account.PendingRejects++
		return actionspace.Action{}, false
	}
	current := account.CurrentPositionState(prop.Symbol)
	action := prop.Action
	action.DelayBars = pa.ScenarioDelay
	if actionspace.IncreasesOrMaintainsRisk(action.Verb) && !account.AllowsRiskIncrease() {
		account.PendingRejects++
		return actionspace.Action{}, false
	}
	approved := prop.NotionalUSDT
	if approved <= 0 {
		account.PendingRejects++
		return actionspace.Action{}, false
	}

	switch action.Verb {
	case actionspace.VerbIncrease:
		if !account.AllowsRiskIncrease() {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		maxSize := approved / (equity * float64(g5protocol.MaxAccountLeverage))
		if cap := actionspace.MaxAdditionalSize(current); maxSize > cap {
			maxSize = cap
		}
		if maxSize <= 1e-9 {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		if action.SizeFraction > maxSize+1e-9 {
			action.SizeFraction = maxSize
			account.PendingClips++
		}
	case actionspace.VerbOpenLong, actionspace.VerbOpenShort, actionspace.VerbReverse:
		if !account.AllowsRiskIncrease() {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		maxSize := approved / (equity * float64(g5protocol.MaxAccountLeverage))
		if maxSize <= 1e-9 {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		if action.SizeFraction > maxSize+1e-9 {
			action.SizeFraction = maxSize
			account.PendingClips++
		}
	case actionspace.VerbTrim:
		priorNotional := actionspace.NotionalUSDT(current)
		if priorNotional <= 0 || current.SizeFraction <= 0 {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		maxTrimFraction := (approved / priorNotional) * current.SizeFraction
		if maxTrimFraction > current.SizeFraction {
			maxTrimFraction = current.SizeFraction
		}
		if maxTrimFraction <= 1e-9 {
			account.PendingRejects++
			return actionspace.Action{}, false
		}
		if action.SizeFraction > maxTrimFraction+1e-9 {
			action.SizeFraction = maxTrimFraction
			account.PendingClips++
		}
	}

	if err := actionspace.ValidateTransition(current, action); err != nil {
		account.PendingRejects++
		return actionspace.Action{}, false
	}
	if wouldBreachLeverage(account, current, action) {
		account.PendingRejects++
		return actionspace.Action{}, false
	}
	return action, true
}

func wouldBreachLeverage(account *Account, state actionspace.PositionState, action actionspace.Action) bool {
	equity := account.EquityUSDT
	if equity <= 0 {
		return true
	}
	entryPrice := state.BasisPrice
	if entryPrice <= 0 {
		entryPrice = 1
	}
	post, _, err := actionspace.ApplyTransition(state, action, entryPrice)
	if err != nil {
		return true
	}
	total := account.TotalFixedNotional() - actionspace.NotionalUSDT(state) + actionspace.NotionalUSDT(post)
	return total/equity > float64(g5protocol.MaxAccountLeverage)+1e-9
}
