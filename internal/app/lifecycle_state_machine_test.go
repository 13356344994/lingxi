package app

import "testing"

func TestNewLifecycleTransitionFromProtocol(t *testing.T) {
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	transition, err := NewLifecycleTransitionFromProtocol(result)
	if err != nil {
		t.Fatalf("NewLifecycleTransitionFromProtocol() error = %v", err)
	}
	if transition.From != LifecycleStageMonitoring || transition.To != LifecycleStagePreOpenCheck {
		t.Fatalf("transition = %s -> %s, want %s -> %s", transition.From, transition.To, LifecycleStageMonitoring, LifecycleStagePreOpenCheck)
	}
}

func TestNewLifecycleTransitionFromProtocolRejectsInvalidTransition(t *testing.T) {
	result := ProtocolResult{
		Protocol:     "close.execute",
		Summary:      "close sequence executed",
		CurrentStage: LifecycleStageClosing,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "invalid jump",
		},
	}

	if _, err := NewLifecycleTransitionFromProtocol(result); err == nil {
		t.Fatalf("NewLifecycleTransitionFromProtocol() expected error, got nil")
	}
}

func TestLifecycleStateMachineApplyProtocolResult(t *testing.T) {
	machine := NewLifecycleStateMachine(LifecycleStageMonitoring)
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	transition, err := machine.ApplyProtocolResult(result)
	if err != nil {
		t.Fatalf("ApplyProtocolResult() error = %v", err)
	}
	if transition.To != LifecycleStagePreOpenCheck {
		t.Fatalf("ApplyProtocolResult() to = %s, want %s", transition.To, LifecycleStagePreOpenCheck)
	}
	if machine.CurrentStage() != LifecycleStagePreOpenCheck {
		t.Fatalf("CurrentStage() = %s, want %s", machine.CurrentStage(), LifecycleStagePreOpenCheck)
	}
	if len(machine.Transitions()) != 1 {
		t.Fatalf("Transitions() len = %d, want 1", len(machine.Transitions()))
	}
}

func TestLifecycleStateMachineRejectsStageMismatch(t *testing.T) {
	machine := NewLifecycleStateMachine(LifecycleStageHolding)
	result := ProtocolResult{
		Protocol:     "open.prepare",
		Summary:      "open sequence prepared",
		CurrentStage: LifecycleStageMonitoring,
		Lifecycle: LifecycleSuggestion{
			NextStage: LifecycleStagePreOpenCheck,
			Reason:    "open preparation completed",
		},
	}

	if _, err := machine.ApplyProtocolResult(result); err == nil {
		t.Fatalf("ApplyProtocolResult() expected stage mismatch error, got nil")
	}
}
