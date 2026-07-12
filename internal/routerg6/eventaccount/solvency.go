package eventaccount

import (
	"lingxi/internal/money"
	"lingxi/internal/portfolio"
	g6protocol "lingxi/internal/routerg6/protocol"
)

// SolvencyTrim selects positions to reduce when account solvency buffer is breached.
func SolvencyTrim(account *Account) (reduceOrder []string, breached bool) {
	slots := account.OpenSlots()
	if len(slots) == 0 {
		return nil, account.Core.SolvencyBreach || account.Core.LeverageBreach
	}
	required := account.Core.EquityUSDT * g6protocol.MaxAccountStressLossFraction
	positions := make([]portfolio.PositionRisk, 0, len(slots))
	for _, slot := range slots {
		positions = append(positions, portfolio.PositionRisk{
			ID:                 slot.Candidate.EventID,
			MarginalStressLoss: money.FromUSDT(slot.StressLoss),
			ReducibleNotional:  money.FromUSDT(slot.Notional),
		})
	}
	dec := portfolio.EvaluateSolvency(portfolio.SolvencyInputs{
		SolvencyEquity: money.FromUSDT(account.Core.EquityUSDT),
		RequiredBuffer: money.FromUSDT(required),
		Positions:      positions,
	})
	if !dec.Breached && !account.Core.SolvencyBreach && !account.Core.LeverageBreach {
		return nil, false
	}
	return dec.ReduceOrder, true
}

// ApplySolvencyTrims closes highest-stress slots until breach clears.
func ApplySolvencyTrims(account *Account, barIndex int, closeFn func(*Account, *EventSlot, string) error) error {
	order, breached := SolvencyTrim(account)
	if !breached {
		return nil
	}
	for _, eventID := range order {
		slot, ok := account.Slots[eventID]
		if !ok || slot.Closed {
			continue
		}
		if err := closeFn(account, slot, ExitSolvencyTrim); err != nil {
			return err
		}
		if !account.Core.SolvencyBreach && !account.Core.LeverageBreach {
			break
		}
	}
	return nil
}
