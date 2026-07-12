package personalbrain

import (
	"lingxi/internal/routerg6/eventaccount"
)

func processStopErrors(b *Brain, in BarInput) (forced bool, reason string) {
	for _, slot := range b.Account.OpenSlots() {
		if thesis := findEventThesisForSlot(in, slot); thesis == ThesisInvalidated {
			price := in.Prices[slot.Candidate.Symbol]
			if price <= 0 {
				price = in.Features.Price
			}
			_ = closeSlot(b, slot, price, in.CostStress, "thesis_health_fail")
			return true, "thesis_health_fail"
		}
		if toxic := EvaluateToxicFill(in, MatchedPersonalEvent{EventID: slot.Candidate.EventID, ThesisHealth: findEventThesisForSlot(in, slot)}); toxic == ToxicFillExitStatus {
			price := in.Prices[slot.Candidate.Symbol]
			if price <= 0 {
				price = in.Features.Price
			}
			_ = closeSlot(b, slot, price, in.CostStress, ToxicFillExitStatus)
			return true, ToxicFillExitStatus
		}
		sig := eventaccount.ExitSignal{
			BarIndex:          in.BarIndex,
			InvalidatorFired:  slot.Candidate.InvalidatorAtBar > 0 && in.BarIndex >= slot.Candidate.InvalidatorAtBar,
			PayerGone:         slot.Candidate.PayerGoneAtBar > 0 && in.BarIndex >= slot.Candidate.PayerGoneAtBar,
			EventComplete:     slot.Candidate.CompleteAtBar > 0 && in.BarIndex >= slot.Candidate.CompleteAtBar,
			OutOfDistribution: slot.Candidate.OODAtBar > 0 && in.BarIndex >= slot.Candidate.OODAtBar,
			DeadlineReached:   slot.Candidate.DeadlineBarIndex > 0 && in.BarIndex >= slot.Candidate.DeadlineBarIndex,
		}
		next, exitReason, exit := eventaccount.EvaluateStopError(slot.StopState, sig)
		if !exit {
			continue
		}
		price := in.Prices[slot.Candidate.Symbol]
		if price <= 0 {
			price = in.Features.Price
		}
		_ = closeSlot(b, slot, price, in.CostStress, exitReason)
		slot.StopState = next
		return true, exitReason
	}
	return false, ""
}

func findEventThesis(in BarInput) string {
	for _, ev := range in.Events {
		if ev.EventID != "" {
			return ev.ThesisHealth
		}
	}
	return ""
}

func findEventThesisForSlot(in BarInput, slot *eventaccount.EventSlot) string {
	for _, ev := range in.Events {
		if ev.EventID == slot.Candidate.EventID || ev.EventFamily == slot.Candidate.EventFamily {
			return ev.ThesisHealth
		}
	}
	return ""
}
