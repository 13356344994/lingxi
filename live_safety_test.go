package main

import (
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
