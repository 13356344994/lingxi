package brain

import (
	"lingxi/internal/routerg6/eventaccount"
	g6protocol "lingxi/internal/routerg6/protocol"
)

const solvencyWarningStressFraction = 0.8

// SolvencyDecision carries independent solvency trim/close intent.
type SolvencyDecision struct {
	Breached    bool     `json:"breached"`
	State       string   `json:"state"`
	Action      string   `json:"action,omitempty"`
	EventID     string   `json:"event_id,omitempty"`
	ReduceOrder []string `json:"reduce_order,omitempty"`
}

// EvaluateSolvency runs the independent solvency machine; playbook cannot disable it.
func EvaluateSolvency(account *eventaccount.Account) SolvencyDecision {
	order, breached := eventaccount.SolvencyTrim(account)
	state := SolvencyStateOK
	if account.Core.LeverageBreach {
		state = SolvencyStateBreached
		breached = true
	} else if account.Core.SolvencyBreach {
		state = SolvencyStateBreached
		breached = true
	} else if accountStressWarning(account) {
		state = SolvencyStateWarning
		if len(order) == 0 {
			order = warningTrimOrder(account)
		}
	}
	dec := SolvencyDecision{
		Breached:    breached,
		State:       state,
		ReduceOrder: order,
	}
	if breached && len(order) > 0 {
		dec.Action = "TRIM"
		dec.EventID = order[0]
	} else if breached {
		dec.Action = "CLOSE"
		if len(order) > 0 {
			dec.EventID = order[0]
		}
	} else if state == SolvencyStateWarning && len(order) > 0 {
		dec.Action = "TRIM"
		dec.EventID = order[0]
	}
	return dec
}

// SolvencyBlocksNewOpen reports whether solvency warning/breach forbids new opens.
func SolvencyBlocksNewOpen(sol SolvencyDecision) bool {
	return sol.Breached || sol.State == SolvencyStateWarning
}

// SolvencyOverridesPlaybook reports whether solvency forces trim/close over playbook hold.
func SolvencyOverridesPlaybook(sol SolvencyDecision) bool {
	if sol.Breached && (sol.Action == "TRIM" || sol.Action == "CLOSE") {
		return true
	}
	return sol.State == SolvencyStateWarning && sol.Action == "TRIM"
}

func accountStressWarning(account *eventaccount.Account) bool {
	if account.Core.SolvencyBreach || account.Core.LeverageBreach {
		return false
	}
	maxStress := account.Core.EquityUSDT * g6protocol.MaxAccountStressLossFraction
	if maxStress <= 0 {
		return false
	}
	return account.UsedAccountStress() >= maxStress*solvencyWarningStressFraction
}

func warningTrimOrder(account *eventaccount.Account) []string {
	slots := account.OpenSlots()
	if len(slots) == 0 {
		return nil
	}
	best := slots[0]
	for _, slot := range slots[1:] {
		if slot.StressLoss > best.StressLoss {
			best = slot
		}
	}
	return []string{best.Candidate.EventID}
}
