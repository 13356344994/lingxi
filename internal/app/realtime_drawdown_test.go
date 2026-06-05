package app

import (
	"log"
	"strings"
	"testing"
	"time"
)

// TestCombinedManagedUnrealizedNetsSpotLeg 钉死 P0 修复:组合级浮亏口径把现货腿实时市值纳入。
// 合约腿单边浮亏 -25,但 delta 中性现货腿等额获利 → 组合接近中性,不应误触熔断;现货腿少赚导致
// 真实组合净亏 → 仍应触发。
func TestCombinedManagedUnrealizedNetsSpotLeg(t *testing.T) {
	build := func(spotPrice string) *FundingRateSniperEngine {
		cache := NewBookTickerCache()
		cache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: spotPrice, AskPrice: spotPrice}, time.Now().UTC())
		eng := &FundingRateSniperEngine{
			cfg:             DefaultEngineConfig(),
			logger:          log.Default(),
			spotTickerCache: cache,
			futuresRealtimePositions: map[string]futuresRealtimePosition{
				// 价格上行:合约空头浮亏 -25。
				"BTCUSDT": {Symbol: "BTCUSDT", PositionAmt: -1, UnrealizedProfit: -25},
			},
			portfolioManager: portfolioManager{
				activeOpenHedges: []OpenHedge{{
					Target:           FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"},
					Direction:        fundingCarryDirectionPositive,
					HedgeQty:         1,
					SpotBaseQty:      "1",
					OpenSpotQuoteQty: 100, // 入场现货价 100
				}},
				exposureLocks: make(map[string]exposureLockState),
			},
		}
		eng.cfg.RiskConfig.MaxCycleLossUSDT = 20
		return eng
	}

	// 现货涨到 125:现货多头 +25,组合净 ≈ 0 → 不触发(旧口径会按 -25 误触)。
	hedged := build("125")
	if trip, _ := hedged.shouldTriggerRealtimeDrawdownPanic(); trip {
		t.Fatal("对冲组合净接近中性,不应触发实时熔断(旧口径只看合约腿会误触)")
	}

	// 现货只涨到 105:现货多头 +5,组合净亏 -20 ≥ 限额 → 触发。
	drifted := build("105")
	if trip, reason := drifted.shouldTriggerRealtimeDrawdownPanic(); !trip {
		t.Fatalf("组合真实净亏达限额应触发,got trip=false reason=%q", reason)
	}
}

func TestShouldTriggerRealtimeDrawdownPanicTripsOnManagedUnrealizedLoss(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.Default(),
		futuresRealtimePositions: map[string]futuresRealtimePosition{
			"BTCUSDT": {Symbol: "BTCUSDT", PositionAmt: 1, UnrealizedProfit: -25},
			"ETHUSDT": {Symbol: "ETHUSDT", PositionAmt: -1, UnrealizedProfit: 5},
		},
		portfolioManager: portfolioManager{
			activeOpenHedges: []OpenHedge{
				{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}, HedgeQty: 1},
			},
			exposureLocks: make(map[string]exposureLockState),
		},
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
		portfolioManager:       portfolioManager{exposureLocks: map[string]exposureLockState{"BTCUSDT": {}}},
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
		portfolioManager: portfolioManager{
			activeOpenHedges: []OpenHedge{
				{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}, HedgeQty: 1},
				{Target: FundingCandidate{Symbol: "ETHUSDT", Pair: "ETHUSDT", SpotBaseAsset: "ETH"}, HedgeQty: 1},
			},
			exposureLocks: make(map[string]exposureLockState),
		},
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
