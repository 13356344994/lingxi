package app

import (
	"log"
	"strings"
	"testing"
)

func TestShouldTriggerRealtimeDrawdownPanicTripsOnManagedUnrealizedLoss(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.Default(),
		futuresRealtimePositions: map[string]futuresRealtimePosition{
			"BTCUSDT": {Symbol: "BTCUSDT", PositionAmt: 1, UnrealizedProfit: -25},
			"ETHUSDT": {Symbol: "ETHUSDT", PositionAmt: -1, UnrealizedProfit: 5},
		},
		activeOpenHedges: []OpenHedge{
			{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}, HedgeQty: 1},
		},
		exposureLocks: make(map[string]exposureLockState),
	}
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 20

	trip, reason := engine.shouldTriggerRealtimeDrawdownPanic()
	if !trip {
		t.Fatal("expected realtime managed loss to trigger panic flatten")
	}
	if reason == "" {
		t.Fatal("expected panic reason to be populated")
	}
}

func TestManagedExposureTargetsIncludeExposureLocks(t *testing.T) {
	engine := &FundingRateSniperEngine{
		goldenSpotPairByFuture: map[string]string{"BTCUSDT": "BTCUSDT"},
		spotMeta:               map[string]SpotSymbol{"BTCUSDT": {BaseAsset: "BTC"}},
		exposureLocks:          map[string]exposureLockState{"BTCUSDT": {}},
	}

	targets := engine.managedExposureTargets()
	if len(targets) != 1 {
		t.Fatalf("expected one managed exposure target, got %+v", targets)
	}
	if targets[0].Pair != "BTCUSDT" || targets[0].BaseAsset != "BTC" {
		t.Fatalf("unexpected managed exposure target: %+v", targets[0])
	}
}

func TestShouldTriggerManagedSymbolDrawdownCutTargetsWorstSymbol(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxCycleLossUSDT = 100
	engine := &FundingRateSniperEngine{
		cfg:    cfg,
		logger: log.Default(),
		futuresRealtimePositions: map[string]futuresRealtimePosition{
			"BTCUSDT": {Symbol: "BTCUSDT", PositionAmt: 1, UnrealizedProfit: -60},
			"ETHUSDT": {Symbol: "ETHUSDT", PositionAmt: 1, UnrealizedProfit: -15},
		},
		activeOpenHedges: []OpenHedge{
			{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}, HedgeQty: 1},
			{Target: FundingCandidate{Symbol: "ETHUSDT", Pair: "ETHUSDT", SpotBaseAsset: "ETH"}, HedgeQty: 1},
		},
		exposureLocks: make(map[string]exposureLockState),
	}

	symbol, reason := engine.shouldTriggerManagedSymbolDrawdownCut()
	if symbol != "BTCUSDT" {
		t.Fatalf("expected BTCUSDT to be cut first, got %s", symbol)
	}
	if reason == "" {
		t.Fatal("expected poison symbol cut reason to be populated")
	}
	if _, ok := engine.symbolPanicFlattenInFlight["BTCUSDT"]; !ok {
		t.Fatal("expected in-flight symbol flatten marker to be set")
	}
	if engine.managedDrawdownSummary == "" || engine.managedDrawdownSummary == "-" {
		t.Fatalf("expected managed drawdown summary to be populated, got %q", engine.managedDrawdownSummary)
	}
	if got := engine.managedDrawdownSummary; got == "" || got != "" && !containsAll(got, "BTCUSDT", "managedLoss", "trigger") {
		t.Fatalf("unexpected managed drawdown summary: %q", got)
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
