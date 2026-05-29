package main

import "testing"

func TestEngineStageTransitionAllowsExpectedPath(t *testing.T) {
	cases := []struct {
		from EngineStage
		to   EngineStage
	}{
		{from: StageReady, to: StageScan},
		{from: StageScan, to: StageMonitor},
		{from: StageMonitor, to: StageOpen},
		{from: StageOpen, to: StageClose},
		{from: StageClose, to: StageCompleted},
		{from: StageCooldown, to: StageReady},
		{from: StageCompleted, to: StageScan},
		{from: StageOpenEmpty, to: StageScan},
	}
	for _, tc := range cases {
		if _, err := NewEngineStageTransition(tc.from, tc.to, "test"); err != nil {
			t.Fatalf("expected transition %s -> %s to be allowed: %v", tc.from, tc.to, err)
		}
	}
}

func TestEngineStageTransitionRejectsInvalidJump(t *testing.T) {
	_, err := NewEngineStageTransition(StageReady, StageClose, "skip")
	if err == nil {
		t.Fatal("expected invalid transition error")
	}
}

func TestEngineStageTransitionAllowsTerminalExit(t *testing.T) {
	for _, terminal := range []EngineStage{StageStopped, StageFailed} {
		if _, err := NewEngineStageTransition(StageMonitor, terminal, "terminal"); err != nil {
			t.Fatalf("expected terminal transition to %s to be allowed: %v", terminal, err)
		}
	}
}
