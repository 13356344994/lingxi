package main

import (
	"testing"
	"time"
)

func TestRuntimeReporterEmitWithReasonTransitionsAndPublishesSnapshot(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                            DefaultEngineConfig(),
		currentStage:                   StageReady,
		symbolCooldowns:                make(map[string]symbolCooldownState),
		runID:                          "live-20260529T120000Z-11112222",
		currentCycleID:                 "live-20260529T120000Z-11112222-cycle-33334444",
		protectedIOCAttempts:           6,
		protectedIOCCanceled:           1,
		protectedIOCExpired:            1,
		protectedIOCPartial:            2,
		protectedIOCUnfilledSum:        1.8,
		lastManagedDrawdownAt:          time.Date(2026, 5, 31, 1, 10, 0, 0, time.UTC),
		lastManagedDrawdownAction:      "symbol_cut_completed | BTCUSDT | alerts=flattened",
		managedDrawdownSummary:         "tracked=2 managedLoss=65.000000 trigger=50.000000 worst=BTCUSDT upnl=-60.000000 qty=1.00000000",
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
}
