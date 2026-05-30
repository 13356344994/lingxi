package main

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeControllerRecordLiveEngineStatusPersistsStageContext(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}

	controller.status.Mode = string(RuntimeModeLive)
	controller.status.Running = true
	controller.recordLiveEngineStatus(LiveRuntimeSnapshot{
		Stage:               StageMonitor,
		StageReason:         "进入监控阶段，目标数=3",
		StageTransitionedAt: "2026-05-29T12:00:00Z",
	})

	status := controller.Status()
	if status.CurrentStage != string(StageMonitor) {
		t.Fatalf("expected stage monitor, got %s", status.CurrentStage)
	}
	if status.StageReason != "进入监控阶段，目标数=3" {
		t.Fatalf("unexpected stage reason: %s", status.StageReason)
	}
	if status.StageTransitionedAt != "2026-05-29T12:00:00Z" {
		t.Fatalf("unexpected stage transitioned_at: %s", status.StageTransitionedAt)
	}
}

func TestRuntimeControllerRecordExecutionEventUpdatesTraceIDs(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}

	controller.recordExecutionEvent(ExecutionIncidentRecord{
		RunID:         "live-20260529T120000Z-aaaabbbb",
		CycleID:       "live-20260529T120000Z-aaaabbbb-cycle-ccccdddd",
		RecordedAt:    "2026-05-29T12:01:00Z",
		Level:         "incident",
		Category:      RiskCategoryExecution,
		Severity:      RiskSeverityWarning,
		Phase:         "open",
		Symbol:        "BTCUSDT",
		Action:        "incident",
		IncidentCount: 1,
		IncidentLimit: 3,
		Detail:        "timeout",
	})

	status := controller.Status()
	if status.RunID != "live-20260529T120000Z-aaaabbbb" {
		t.Fatalf("unexpected run id: %s", status.RunID)
	}
	if status.CurrentCycleID != "live-20260529T120000Z-aaaabbbb-cycle-ccccdddd" {
		t.Fatalf("unexpected cycle id: %s", status.CurrentCycleID)
	}
}

func TestValidateLocalDashboardAddrRejectsNonLocalAddress(t *testing.T) {
	if err := validateLocalDashboardAddr("127.0.0.1:8080"); err != nil {
		t.Fatalf("expected localhost bind to pass, got %v", err)
	}
	if err := validateLocalDashboardAddr("0.0.0.0:8080"); err == nil {
		t.Fatal("expected non-local bind to be rejected")
	}
}

func TestEnsureCooldownExpiredRejectsFutureCooldown(t *testing.T) {
	if err := ensureCooldownExpired(time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatalf("expected expired cooldown to pass, got %v", err)
	}
	if err := ensureCooldownExpired(time.Now().UTC().Add(time.Minute).Format(time.RFC3339)); err == nil {
		t.Fatal("expected future cooldown to be rejected")
	}
}

func TestRuntimeControllerSetStageLockedAppendsTransitionLog(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}
	controller.logs.mu.Lock()
	controller.logs.lastCleanupAt = time.Now().UTC()
	controller.logs.mu.Unlock()

	controller.mu.Lock()
	controller.status.RunID = "live-20260529T120000Z-11223344"
	controller.setStageLocked(StageReady, "准备完成", "controller")
	controller.setStageLocked(StageScan, "开始扫描", "controller")
	controller.mu.Unlock()

	logs, err := controller.RecentLogs(10)
	if err != nil {
		t.Fatalf("RecentLogs() error = %v", err)
	}
	found := false
	for _, record := range logs {
		if record.RunID == "live-20260529T120000Z-11223344" && record.Message == "状态迁移: ready -> scan | reason=开始扫描" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected stage transition log, got %+v", logs)
	}
}

func TestRuntimeControllerUpdateRiskConfigRejectsAggressiveRelaxation(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}

	req := controller.CurrentRiskConfig()
	req.PerSymbolQuoteUSDT = req.PerSymbolQuoteUSDT * 1.2
	if _, err := controller.UpdateRiskConfig(req); err == nil {
		t.Fatal("expected aggressive runtime relaxation to be rejected")
	}
}

func TestRuntimeControllerUpdateRiskConfigRejectsOnlineModelChange(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}

	req := controller.CurrentRiskConfig()
	req.MinNetProfitRate = req.MinNetProfitRate + 0.001
	if _, err := controller.UpdateRiskConfig(req); err == nil {
		t.Fatal("expected online model parameter change to be rejected")
	}
}

func TestRuntimeControllerUpdateRiskConfigAllowsConservativeTightening(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	controller, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController() error = %v", err)
	}

	original := controller.CurrentRiskConfig()
	req := controller.CurrentRiskConfig()
	req.PerSymbolQuoteUSDT = req.PerSymbolQuoteUSDT * 0.8
	req.MaxCycleOpenNotionalUSDT = req.MaxCycleOpenNotionalUSDT * 0.8
	req.MaxCycleLossUSDT = req.MaxCycleLossUSDT * 0.8
	req.SymbolAbnormalCooldown = "12m"
	req.PlatformMinorCooldown = "6m"
	req.PlatformAbnormalCooldown = "20m"
	req.PlatformCriticalCooldown = "40m"
	req.MaxDailyLossUSDT = req.MaxDailyLossUSDT * 0.8
	req.MaxDailyOrderCount = req.MaxDailyOrderCount - 5
	req.MaxDailyOpenNotionalUSDT = req.MaxDailyOpenNotionalUSDT * 0.8

	updated, err := controller.UpdateRiskConfig(req)
	if err != nil {
		t.Fatalf("UpdateRiskConfig() error = %v", err)
	}
	if updated.PerSymbolQuoteUSDT >= original.PerSymbolQuoteUSDT {
		t.Fatalf("expected per-symbol quote to be tightened, got %.2f", updated.PerSymbolQuoteUSDT)
	}
	if updated.PlatformCriticalCooldown != "40m0s" {
		t.Fatalf("expected critical cooldown to be extended, got %s", updated.PlatformCriticalCooldown)
	}
}
