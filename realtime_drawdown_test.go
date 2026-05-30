package main

import (
	"log"
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
