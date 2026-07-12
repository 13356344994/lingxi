package personalbrain

import (
	"lingxi/internal/routerg10/executionpolicy"
)

// EvaluateToxicFill inspects execution sim for toxic fill signals.
func EvaluateToxicFill(in BarInput, ev MatchedPersonalEvent) string {
	tr := executionTriggerFromBar(ev, in)
	return executionpolicy.EvaluateToxicFill(tr)
}

// ApplyToxicFill handles TOXIC_FILL_REVALIDATE / TOXIC_FILL_EXIT and blocks top-up.
func (b *Brain) ApplyToxicFill(in BarInput, ev MatchedPersonalEvent) (forced bool, reason string) {
	status := EvaluateToxicFill(in, ev)
	if status == "" {
		return false, ""
	}
	b.ToxicBlocked = true
	if b.MakerOrder != nil {
		b.MakerOrder = nil
	}
	if status == ToxicFillExitStatus || ev.ThesisHealth == ThesisInvalidated || !in.ExecutionSim.ThesisStillValid {
		for _, slot := range b.Account.OpenSlots() {
			if slot.Candidate.Symbol != in.Features.Symbol {
				continue
			}
			price := in.Prices[in.Features.Symbol]
			if price <= 0 {
				price = in.Features.Price
			}
			_ = closeSlot(b, slot, price, in.CostStress, ToxicFillExitStatus)
			return true, ToxicFillExitStatus
		}
		return true, ToxicFillExitStatus
	}
	return true, ToxicFillRevalidateStatus
}

// ToxicBlocksAdd reports whether ADD/top-up is forbidden after toxic fill.
func (b *Brain) ToxicBlocksAdd() bool {
	return b.ToxicBlocked
}
