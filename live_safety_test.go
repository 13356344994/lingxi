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

func TestEvaluateOpenDepthStabilityTripsOnVisibleDepthCollapse(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	target := &MonitoredTarget{
		LastObserveTime:            time.Now().UTC(),
		LastSpotAsk:                100,
		LastFuturesBid:             101,
		LastSpotVisibleNotional:    1000,
		LastFuturesVisibleNotional: 1000,
	}
	spotDepth := ParsedOrderBookDepth{
		Asks:       []DepthLevel{{Price: 100.2, Quantity: 4}},
		Bids:       []DepthLevel{{Price: 99.8, Quantity: 4}},
		ReceivedAt: time.Now().UTC(),
	}
	futuresDepth := ParsedOrderBookDepth{
		Bids:       []DepthLevel{{Price: 100.7, Quantity: 4}},
		Asks:       []DepthLevel{{Price: 101.1, Quantity: 4}},
		ReceivedAt: time.Now().UTC(),
	}

	decision, detail, unstable, spotVisible, futuresVisible, ratio := engine.evaluateOpenDepthStability(target, fundingCarryDirectionPositive, spotDepth, futuresDepth, 100.2, 100.7)
	if !unstable {
		t.Fatal("expected visible depth collapse to be treated as unstable")
	}
	if decision.Action != "symbol_cooldown" {
		t.Fatalf("expected symbol cooldown, got %+v", decision)
	}
	if !strings.Contains(detail, "可见深度快速塌陷") {
		t.Fatalf("unexpected stability detail: %s", detail)
	}
	if spotVisible <= 0 || futuresVisible <= 0 {
		t.Fatalf("expected visible notionals to be returned, got spot=%.4f futures=%.4f", spotVisible, futuresVisible)
	}
	if ratio >= 0.65 {
		t.Fatalf("expected collapsed ratio below cooldown threshold, got %.4f", ratio)
	}
}
