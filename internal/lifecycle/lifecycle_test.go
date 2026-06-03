package lifecycle

import "testing"

func TestHappyPathTransitions(t *testing.T) {
	path := []State{StateNone, StatePendingValidation, StateActive, StateClosing, StateClosed}
	for i := 0; i+1 < len(path); i++ {
		if err := ValidateTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("expected legal transition %s->%s: %v", path[i], path[i+1], err)
		}
	}
}

func TestIdempotentSelfTransition(t *testing.T) {
	if err := ValidateTransition(StateActive, StateActive); err != nil {
		t.Fatalf("self-transition should be legal: %v", err)
	}
}

func TestIllegalTransitions(t *testing.T) {
	illegal := [][2]State{
		{StateClosed, StateActive},            // 终态不可复活
		{StateActive, StatePendingValidation}, // 不可回到开仓待校验
		{StateNone, StateActive},              // 必须先经过 pending_validation
		{StateClosing, StateActive},           // 平仓中不可回到持仓
		{StateAborted, StateClosing},          // 干净终态无后继
	}
	for _, pair := range illegal {
		if err := ValidateTransition(pair[0], pair[1]); err == nil {
			t.Fatalf("expected illegal transition %s->%s to error", pair[0], pair[1])
		}
	}
	if err := ValidateTransition(StateActive, ""); err == nil {
		t.Fatalf("transition to empty state must error")
	}
}

func TestIntentGate(t *testing.T) {
	allowed := []struct {
		state  State
		intent Intent
	}{
		{StateNone, IntentOpenLeg},
		{StatePendingValidation, IntentRevertLeg},
		{StateActive, IntentCloseLeg},
		{StateActive, IntentRescueReduce},
		{StateClosing, IntentCloseLeg},
		{StateClosedWithResidual, IntentRescueReduce},
		{StateAbortedWithResidual, IntentRescueReduce},
	}
	for _, c := range allowed {
		if err := GuardIntent(c.state, c.intent); err != nil {
			t.Fatalf("expected intent %s allowed in %s: %v", c.intent, c.state, err)
		}
	}

	denied := []struct {
		state  State
		intent Intent
	}{
		{StateActive, IntentOpenLeg},      // 持仓中不可再开腿
		{StateClosed, IntentCloseLeg},     // 干净终态禁止下单
		{StateClosed, IntentRescueReduce}, // 干净终态连救援也无需
		{StateAborted, IntentOpenLeg},
		{StateClosing, IntentOpenLeg},
	}
	for _, c := range denied {
		if err := GuardIntent(c.state, c.intent); err == nil {
			t.Fatalf("expected intent %s denied in %s", c.intent, c.state)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	if !IsTerminal(StateClosed) || !IsTerminal(StateAborted) {
		t.Fatalf("closed/aborted should be terminal")
	}
	if IsTerminal(StateClosedWithResidual) || IsTerminal(StateActive) {
		t.Fatalf("residual/active should not be clean-terminal")
	}
}
