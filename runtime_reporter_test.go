package main

import "testing"

func TestRuntimeReporterEmitWithReasonTransitionsAndPublishesSnapshot(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                     DefaultEngineConfig(),
		currentStage:            StageReady,
		symbolCooldowns:         make(map[string]symbolCooldownState),
		runID:                   "live-20260529T120000Z-11112222",
		currentCycleID:          "live-20260529T120000Z-11112222-cycle-33334444",
		protectedIOCAttempts:    6,
		protectedIOCCanceled:    1,
		protectedIOCExpired:     1,
		protectedIOCPartial:     2,
		protectedIOCUnfilledSum: 1.8,
	}
	reporter := newRuntimeReporter(engine)

	var snapshot LiveRuntimeSnapshot
	engine.liveStatusHook = func(s LiveRuntimeSnapshot) {
		snapshot = s
	}

	reporter.EmitWithReason(StageScan, 3, "开始扫描")

	if engine.currentStage != StageScan {
		t.Fatalf("expected engine stage scan, got %s", engine.currentStage)
	}
	if snapshot.Stage != StageScan {
		t.Fatalf("expected snapshot stage scan, got %s", snapshot.Stage)
	}
	if snapshot.StageReason != "开始扫描" {
		t.Fatalf("unexpected stage reason: %s", snapshot.StageReason)
	}
	if snapshot.CurrentTargets != 3 {
		t.Fatalf("unexpected current targets: %d", snapshot.CurrentTargets)
	}
	if snapshot.RunID != engine.runID || snapshot.CycleID != engine.currentCycleID {
		t.Fatalf("unexpected trace ids: %+v", snapshot)
	}
	if snapshot.ProtectedIOCAttempts != 6 || snapshot.ProtectedIOCCanceled != 1 || snapshot.ProtectedIOCExpired != 1 || snapshot.ProtectedIOCPartial != 2 {
		t.Fatalf("unexpected protected ioc counters: %+v", snapshot)
	}
	if snapshot.ProtectedIOCAvgUnfilledRatio != 0.3 {
		t.Fatalf("unexpected protected ioc avg unfilled ratio: %v", snapshot.ProtectedIOCAvgUnfilledRatio)
	}
	if snapshot.ProtectedIOCCancelRate != (2.0 / 6.0) {
		t.Fatalf("unexpected protected ioc cancel rate: %v", snapshot.ProtectedIOCCancelRate)
	}
}
