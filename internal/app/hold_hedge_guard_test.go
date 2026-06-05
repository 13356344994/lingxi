package app

import (
	"io"
	"log"
	"testing"
	"time"
)

func newHoldHedgeGuardEngine(t *testing.T, direction fundingCarryDirection) *FundingRateSniperEngine {
	t.Helper()
	engine := &FundingRateSniperEngine{
		cfg:                        DefaultEngineConfig(),
		logger:                     log.New(io.Discard, "", 0),
		holdSpotTickerCache:        NewBookTickerCache(),
		holdFuturesTickerCache:     NewBookTickerCache(),
		futuresRealtimePositions:   make(map[string]futuresRealtimePosition),
		spotRealtimeBalances:       make(map[string]float64),
		symbolPanicFlattenInFlight: make(map[string]time.Time),
	}
	engine.cfg.RiskConfig.HoldHedgeGuardEnabled = true
	engine.activeOpenHedges = []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC", Direction: direction}},
	}
	// 合约腿实时仓位（正 carry 为合约空头，方向不影响 |notional|）。
	engine.futuresRealtimePositions["BTCUSDT"] = futuresRealtimePosition{Symbol: "BTCUSDT", PositionAmt: -0.001, MarkPrice: 60000}
	// 现货价：常驻行情流缓存。
	engine.holdSpotTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "60000", AskPrice: "60000"})
	return engine
}

func TestHoldHedgeGuardTriggersOnImbalance(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	// 现货腿名义 ~90 vs 合约腿 ~60 → 偏离率 33% > 默认 10% → 应触发。
	engine.spotRealtimeBalances["BTC"] = 0.0015

	symbol, reason := engine.shouldTriggerHoldHedgeFlatten()
	if symbol != "BTCUSDT" {
		t.Fatalf("expected imbalance to trigger flatten for BTCUSDT, got symbol=%q reason=%q", symbol, reason)
	}
	if _, ok := engine.symbolPanicFlattenInFlight["BTCUSDT"]; !ok {
		t.Fatal("trigger should claim the per-symbol in-flight guard")
	}
}

func TestHoldHedgeGuardSilentWhenBalanced(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	// 两腿名义相等 → 不触发。
	engine.spotRealtimeBalances["BTC"] = 0.001

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("balanced hedge should not trigger, got %q", symbol)
	}
}

func TestHoldHedgeGuardDisabledByDefault(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	engine.cfg.RiskConfig.HoldHedgeGuardEnabled = false
	engine.spotRealtimeBalances["BTC"] = 0.0015 // 明显偏离

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("guard disabled should never trigger, got %q", symbol)
	}
}

func TestHoldHedgeGuardSkipsNegativeCarryWhenMarginDataMissing(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionNegative)
	engine.spotRealtimeBalances["BTC"] = 0.0015 // 即便偏离

	// 负 carry 现货腿在全仓杠杆;无 margin 健康快照(lastMarginHealthAt 为零)→ 不臆测,跳过避免误平。
	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("negative carry without fresh margin data should be skipped, got %q", symbol)
	}
}

func TestHoldHedgeGuardTriggersNegativeCarryWithFreshMarginData(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionNegative)
	// 负 carry 借币空头规模用 cross-margin 逐资产快照 abs(NetAsset) 测量:0.0015 BTC ~ 名义 90,
	// 合约腿名义 ~60 → 偏离率 33% > 默认 10% → 应触发。
	engine.crossMarginAssetBalances = map[string]CrossMarginAssetBalance{
		"BTC": {Asset: "BTC", Borrowed: 0.0015, NetAsset: -0.0015},
	}
	engine.lastMarginHealthAt = time.Now().UTC()

	symbol, reason := engine.shouldTriggerHoldHedgeFlatten()
	if symbol != "BTCUSDT" {
		t.Fatalf("负 carry 两腿漂移应触发,got symbol=%q reason=%q", symbol, reason)
	}
}

func TestHoldHedgeGuardSkipsNegativeCarryWhenMarginDataStale(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionNegative)
	engine.crossMarginAssetBalances = map[string]CrossMarginAssetBalance{
		"BTC": {Asset: "BTC", Borrowed: 0.0015, NetAsset: -0.0015},
	}
	// 过期快照(超过 marginHealthStaleAfter)→ 不据此动作,避免拿过期数据误平。
	engine.lastMarginHealthAt = time.Now().UTC().Add(-2 * marginHealthStaleAfter)

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("过期 margin 数据不应触发,got %q", symbol)
	}
}

func TestHoldHedgeGuardSkipsWhenSpotPriceMissing(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	engine.holdSpotTickerCache = NewBookTickerCache() // 清空现货价
	engine.spotRealtimeBalances["BTC"] = 0.0015

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("missing fresh spot price should skip evaluation, got %q", symbol)
	}
}

func TestHoldHedgeGuardSkipsWhenLegMeasurementMissing(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	// 现货余额缺失（事件驱动尚未推送）→ 不臆测「腿没了」，跳过避免误平。
	delete(engine.spotRealtimeBalances, "BTC")

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("missing spot balance should skip (avoid false flatten), got %q", symbol)
	}
}

func TestHoldHedgeGuardSkipsWhenAlreadyInFlight(t *testing.T) {
	engine := newHoldHedgeGuardEngine(t, fundingCarryDirectionPositive)
	engine.spotRealtimeBalances["BTC"] = 0.0015
	engine.symbolPanicFlattenInFlight["BTCUSDT"] = time.Now().UTC()

	if symbol, _ := engine.shouldTriggerHoldHedgeFlatten(); symbol != "" {
		t.Fatalf("symbol already flattening should be skipped, got %q", symbol)
	}
}
