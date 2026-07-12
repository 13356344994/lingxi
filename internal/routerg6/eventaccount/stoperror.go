package eventaccount

import g6protocol "lingxi/internal/routerg6/protocol"

// StopErrorState tracks whether an event position has exited via StopError.
type StopErrorState string

const (
	StopErrorHolding StopErrorState = "holding"
	StopErrorExit    StopErrorState = "exit"
)

// Exit trigger reason constants aligned with frozen G6 protocol.
const (
	ExitEventComplete    = "event_complete"
	ExitPayerDisappeared = "payer_disappeared"
	ExitInvalidator      = "invalidator"
	ExitOOD              = "ood"
	ExitDeadline         = "deadline"
	ExitSolvencyTrim     = "solvency_trim"
)

// ExitSignal carries one bar's dynamic exit observation for an event slot.
type ExitSignal struct {
	BarIndex         int
	InvalidatorFired bool
	PayerGone        bool
	EventComplete    bool
	OutOfDistribution bool
	DeadlineReached  bool
}

// EvaluateStopError applies G6 dynamic exit triggers; deadline is fallback only.
func EvaluateStopError(prev StopErrorState, sig ExitSignal) (StopErrorState, string, bool) {
	if prev == StopErrorExit {
		return StopErrorExit, "already_exited", true
	}
	switch {
	case sig.EventComplete:
		return StopErrorExit, ExitEventComplete, true
	case sig.PayerGone:
		return StopErrorExit, ExitPayerDisappeared, true
	case sig.InvalidatorFired:
		return StopErrorExit, ExitInvalidator, true
	case sig.OutOfDistribution:
		return StopErrorExit, ExitOOD, true
	case sig.DeadlineReached:
		return StopErrorExit, ExitDeadline, true
	default:
		return StopErrorHolding, "", false
	}
}

// ExitTriggers returns frozen protocol exit trigger labels.
func ExitTriggers() []string {
	return append([]string(nil), g6protocol.StopErrorExitTriggers...)
}
