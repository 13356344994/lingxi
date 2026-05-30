package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureEmergencyStopClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".halt-live")
	if err := ensureEmergencyStopClear(path); err != nil {
		t.Fatalf("expected clear kill switch to pass, got %v", err)
	}
	if err := os.WriteFile(path, []byte("halt"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := ensureEmergencyStopClear(path); err == nil {
		t.Fatal("expected emergency stop file to block startup")
	}
}

func TestFundingRateSniperEngineCheckDailyRiskLimitsTripsOnLoss(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	engine := &FundingRateSniperEngine{
		cfg:              cfg,
		logger:           log.Default(),
		runtimeReporter:  newRuntimeReporter(nil),
		dailyRiskDate:    tradingDayUTC(time.Now().UTC()),
		dailyRealizedPnL: -12,
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	err := engine.checkDailyRiskLimits("test")
	if err == nil {
		t.Fatal("expected daily loss limit to trip")
	}
	if !strings.Contains(err.Error(), "当日累计亏损") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !engine.dailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set")
	}
}

func TestNoteOrderPlacedQueuesDailyRiskPersistenceAsynchronously(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: filepath.Join(t.TempDir(), "live_daily_risk_state.json"),
		dailyRiskDate:      tradingDayUTC(time.Now().UTC()),
		dailyRiskPersistCh: make(chan persistedDailyRiskState, 1),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)
	engine.dailyRiskPersistCh <- persistedDailyRiskState{TradingDay: engine.dailyRiskDate}

	start := time.Now()
	if err := engine.noteOrderPlaced("batch_open_futures", "targets=1", 1, true); err != nil {
		t.Fatalf("noteOrderPlaced() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("expected noteOrderPlaced to avoid blocking on disk persistence, got %s", elapsed)
	}
	if engine.dailyOrderCount != 1 {
		t.Fatalf("expected daily order count to increment in memory, got %d", engine.dailyOrderCount)
	}
}

func TestStartDailyRiskStateWriterPersistsQueuedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live_daily_risk_state.json")
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.Default(),
		dailyRiskStatePath: path,
		dailyRiskPersistCh: make(chan persistedDailyRiskState, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.startDailyRiskStateWriter(ctx)
	engine.dailyRiskPersistCh <- persistedDailyRiskState{
		TradingDay:       tradingDayUTC(time.Now().UTC()),
		RealizedPnL:      -3.5,
		OrderCount:       2,
		OpenNotionalUSDT: 40,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			if !strings.Contains(string(data), "\"order_count\": 2") {
				t.Fatalf("unexpected persisted payload: %s", string(data))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected async daily risk writer to persist queued state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
