package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

// TestRecoverActiveOpenHedgesForFlatten 钉死应急强平的台账恢复:从落盘台账重建的裸仓必须携带强平所需的关键字段——
// 负 carry 还币数量(SpotBaseQty)、名义校验参考价(MarkPrice)、carry 方向与生命周期态,缺一会导致强平下错数量/方向。
func TestRecoverActiveOpenHedgesForFlatten(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), liveOpenHedgesStateName)
	entries := []persistedOpenHedgeEntry{
		{
			Symbol:             "ETHUSDT",
			Pair:               "ETHUSDT",
			Direction:          fundingCarryDirectionNegative,
			SpotBaseAsset:      "ETH",
			HedgeQty:           1.5,
			SpotBaseQty:        1.5,
			ReferenceSpotPrice: 3200,
		},
		{
			Symbol:             "BTCUSDT",
			Pair:               "BTCUSDT",
			Direction:          fundingCarryDirectionPositive,
			SpotBaseAsset:      "BTC",
			HedgeQty:           0.02,
			SpotBaseQty:        0.02,
			ReferenceSpotPrice: 100000,
		},
	}
	if err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{Hedges: entries}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	engine := &FundingRateSniperEngine{openHedgeStatePath: statePath}
	hedges, err := engine.recoverActiveOpenHedgesForFlatten()
	if err != nil {
		t.Fatalf("recoverActiveOpenHedgesForFlatten error: %v", err)
	}
	if len(hedges) != 2 {
		t.Fatalf("expected 2 hedges, got %d", len(hedges))
	}

	bySymbol := make(map[string]OpenHedge, len(hedges))
	for _, h := range hedges {
		bySymbol[h.Target.Symbol] = h
	}

	neg := bySymbol["ETHUSDT"]
	if neg.carryDirection() != fundingCarryDirectionNegative {
		t.Fatalf("ETHUSDT 应恢复为负 carry, got %v", neg.carryDirection())
	}
	if parseFloat(neg.SpotBaseQty) != 1.5 {
		t.Fatalf("负 carry 还币数量应恢复为 1.5, got %s", neg.SpotBaseQty)
	}
	if neg.LastKnownSpotPrice() != 3200 {
		t.Fatalf("参考现货价应恢复为 3200, got %.2f", neg.LastKnownSpotPrice())
	}
	if neg.lifecycleOrderGate() != OpenHedgeLifecycleActive {
		t.Fatalf("恢复的裸仓生命周期应为 active, got %q", neg.lifecycleOrderGate())
	}

	pos := bySymbol["BTCUSDT"]
	if pos.carryDirection() != fundingCarryDirectionPositive {
		t.Fatalf("BTCUSDT 应恢复为正 carry, got %v", pos.carryDirection())
	}
}

// TestRecoverActiveOpenHedgesForFlattenEmpty 钉死:无台账文件时恢复返回空集且不报错(对应 flatten「无裸仓」路径)。
func TestRecoverActiveOpenHedgesForFlattenEmpty(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), liveOpenHedgesStateName)
	engine := &FundingRateSniperEngine{openHedgeStatePath: statePath}
	hedges, err := engine.recoverActiveOpenHedgesForFlatten()
	if err != nil {
		t.Fatalf("empty ledger should not error: %v", err)
	}
	if len(hedges) != 0 {
		t.Fatalf("expected 0 hedges from empty ledger, got %d", len(hedges))
	}
}

// TestCountActiveStrategyPositions 钉死五类策略台账在场仓统计(供 flatten/watchdog 预判)。
func TestCountActiveStrategyPositions(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	cvStore, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = cvStore.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now,
		HighLeg: CrossVenueLegState{Qty: 0.1}, LowLeg: CrossVenueLegState{Qty: 0.1},
	})
	svspStore, err := newSingleVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = svspStore.upsert(SingleVenueSpotPerpPosition{
		Symbol: "ETH-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now,
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.2},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.2},
	})

	total, byName, err := countActiveStrategyPositions(dir)
	if err != nil {
		t.Fatalf("countActiveStrategyPositions: %v", err)
	}
	if total != 2 {
		t.Fatalf("total=%d want 2", total)
	}
	if byName["cross_venue_perp_perp"] != 1 || byName["single_venue_spot_perp"] != 1 {
		t.Fatalf("unexpected byName: %#v", byName)
	}

	emptyDir := t.TempDir()
	total, _, err = countActiveStrategyPositions(emptyDir)
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if total != 0 {
		t.Fatalf("empty dir total=%d want 0", total)
	}
}

// TestEmergencyFlattenFiveStrategyStores 钉死 CLI flatten 五类策略路径:各 store 有合成在场仓时 CloseAllPositions 清台账、不下单。
func TestEmergencyFlattenFiveStrategyStores(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	deps := buildEmergencyFlattenTestDeps(t, dir, now)

	resp := flattenStrategyPositionsReport(context.Background(), deps, true)
	if resp.TotalClosed != 5 {
		t.Fatalf("total_closed=%d want 5", resp.TotalClosed)
	}
	for _, r := range resp.Results {
		if r.Skipped {
			t.Fatalf("strategy %s should not be skipped", r.Strategy)
		}
		if len(r.Errors) > 0 {
			t.Fatalf("strategy %s errors: %v", r.Strategy, r.Errors)
		}
	}
	assertStrategyStoresCleared(t, dir)
}

// TestEmergencyFlattenFiveStrategyStoresEmpty 钉死:五类 store 均为空时 flatten 跳过不报错。
func TestEmergencyFlattenFiveStrategyStoresEmpty(t *testing.T) {
	dir := t.TempDir()
	deps := strategyFlattenDeps{
		CrossVenue: mustNewCrossVenueExecutorForTest(t, dir),
		SVSP:       NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: newSVSPTestVenue(binance.VenueName)}, dir, testSVSPLogger()),
		CVSP:       NewCrossVenueSpotPerpExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{binance.VenueName: newCVSPTestVenue(binance.VenueName)}, dir, nil),
		SVPS:       NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: newSVPSTestVenue(binance.VenueName, true)}, dir, testSVPSLogger()),
		CVPS:       NewCrossVenuePerpSpotExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{binance.VenueName: newCVPSTestVenue(binance.VenueName, true)}, dir, nil),
	}
	resp := flattenStrategyPositionsReport(context.Background(), deps, true)
	if resp.TotalClosed != 0 {
		t.Fatalf("total_closed=%d want 0", resp.TotalClosed)
	}
	for _, r := range resp.Results {
		if len(r.Errors) > 0 {
			t.Fatalf("empty stores should not error: %s %v", r.Strategy, r.Errors)
		}
	}
}

func buildEmergencyFlattenTestDeps(t *testing.T, dir string, now time.Time) strategyFlattenDeps {
	t.Helper()

	cvStore, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = cvStore.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now,
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		IdemPrefix: "ef",
	})
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	cvExec := &CrossVenueExecutor{
		cfg:    testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  cvStore,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}

	svspV := newSVSPTestVenue(binance.VenueName)
	svspExec := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: svspV}, dir, testSVSPLogger())
	svspExec.nowFn = func() time.Time { return now }
	_ = svspExec.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "ETH-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "ef",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.3},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.3},
	})

	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	cvspExec := NewCrossVenueSpotPerpExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{
		binance.VenueName: spot, bybit.VenueName: perp,
	}, dir, nil)
	cvspExec.nowFn = func() time.Time { return now }
	_ = cvspExec.persistPosition(CrossVenueSpotPerpPosition{
		Symbol: "SOL-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: now, IdemPrefix: "ef",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 0.4},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 0.4},
	})

	svpsV := newSVPSTestVenue(binance.VenueName, true)
	svpsExec := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: svpsV}, dir, testSVPSLogger())
	svpsExec.nowFn = func() time.Time { return now }
	_ = svpsExec.persistPosition(SingleVenuePerpSpotPosition{
		Symbol: "DOGE-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "ef",
		SpotLeg: SingleVenuePerpSpotLegState{Qty: 0.6},
		PerpLeg: SingleVenuePerpSpotLegState{Qty: 0.6},
	})

	cvpsSpot := newCVPSTestVenue(binance.VenueName, true)
	cvpsPerp := newCVPSTestVenue(bybit.VenueName, false)
	cvpsExec := NewCrossVenuePerpSpotExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{
		binance.VenueName: cvpsSpot, bybit.VenueName: cvpsPerp,
	}, dir, nil)
	cvpsExec.nowFn = func() time.Time { return now }
	_ = cvpsExec.persistPosition(CrossVenuePerpSpotPosition{
		Symbol: "XRP-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: now, IdemPrefix: "ef",
		SpotLeg: CrossVenuePerpSpotLegState{Qty: 0.7},
		PerpLeg: CrossVenuePerpSpotLegState{Qty: 0.7},
	})

	if n := atomic.LoadInt32(high.placeCount) + atomic.LoadInt32(low.placeCount) +
		atomic.LoadInt32(svspV.placeCount) + atomic.LoadInt32(spot.placeCount) +
		atomic.LoadInt32(perp.placeCount) + atomic.LoadInt32(svpsV.placeCount) +
		atomic.LoadInt32(cvpsSpot.placeCount) + atomic.LoadInt32(cvpsPerp.placeCount); n != 0 {
		t.Fatalf("setup should not place orders, got %d", n)
	}

	return strategyFlattenDeps{
		CrossVenue: cvExec,
		SVSP:       svspExec,
		CVSP:       cvspExec,
		SVPS:       svpsExec,
		CVPS:       cvpsExec,
	}
}

func mustNewCrossVenueExecutorForTest(t *testing.T, dir string) *CrossVenueExecutor {
	t.Helper()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return &CrossVenueExecutor{
		cfg:    testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{bybit.VenueName: newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
}

func assertStrategyStoresCleared(t *testing.T, dir string) {
	t.Helper()
	cvStore, _ := newCrossVenuePositionStore(dir)
	if ps, _ := cvStore.load(); len(activeCrossVenuePositions(ps)) != 0 {
		t.Fatalf("crossvenue active=%d", len(activeCrossVenuePositions(ps)))
	}
	svspStore, _ := newSingleVenueSpotPerpPositionStore(dir)
	if ps, _ := svspStore.load(); len(activeSingleVenueSpotPerpPositions(ps)) != 0 {
		t.Fatalf("svsp active=%d", len(activeSingleVenueSpotPerpPositions(ps)))
	}
	cvspStore, _ := newCrossVenueSpotPerpPositionStore(dir)
	if ps, _ := cvspStore.load(); len(activeCrossVenueSpotPerpPositions(ps)) != 0 {
		t.Fatalf("cvsp active=%d", len(activeCrossVenueSpotPerpPositions(ps)))
	}
	svpsStore, _ := newSingleVenuePerpSpotPositionStore(dir)
	if ps, _ := svpsStore.load(); len(activeSingleVenuePerpSpotPositions(ps)) != 0 {
		t.Fatalf("svps active=%d", len(activeSingleVenuePerpSpotPositions(ps)))
	}
	cvpsStore, _ := newCrossVenuePerpSpotPositionStore(dir)
	if ps, _ := cvpsStore.load(); len(activeCrossVenuePerpSpotPositions(ps)) != 0 {
		t.Fatalf("cvps active=%d", len(activeCrossVenuePerpSpotPositions(ps)))
	}
}

// TestWatchdogStrategyPositionsDetection 轻量校验 watchdog 对五类 positions.json 的检测逻辑。
func TestWatchdogStrategyPositionsDetection(t *testing.T) {
	dir := t.TempDir()
	closedOnly := filepath.Join(dir, "closed.json")
	if err := os.WriteFile(closedOnly, []byte(`{"positions":[{"symbol":"BTC-USDT","status":"closed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	openFile := filepath.Join(dir, "open.json")
	if err := os.WriteFile(openFile, []byte(`{"positions":[{"symbol":"ETH-USDT","status":"open"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if watchdogPositionsFileHasActive(closedOnly) {
		t.Fatal("closed-only ledger should not be active")
	}
	if !watchdogPositionsFileHasActive(openFile) {
		t.Fatal("open ledger should be active")
	}
}

// watchdogPositionsFileHasActive 与 deploy/watchdog.sh strategy_positions_file_has_active 同语义(Go 侧单测替身)。
func watchdogPositionsFileHasActive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	trimmed := string(data)
	if !containsSymbolField(trimmed) {
		return false
	}
	// jq 等价:status != closed
	if containsNonClosedStatus(trimmed) {
		return true
	}
	return false
}

func containsSymbolField(s string) bool {
	return len(s) > 0 && (contains(s, `"symbol"`) || contains(s, `"Symbol"`))
}

func containsNonClosedStatus(s string) bool {
	for _, st := range []string{`"status":"open"`, `"status": "open"`, `"status":"opening_`, `"status":"closing"`, `"status":"orphan`} {
		if contains(s, st) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
