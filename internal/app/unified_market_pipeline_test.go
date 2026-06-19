package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func TestDefaultUnifiedMarketFetchMarketsByVenueConcurrent(t *testing.T) {
	var peak int32
	var active int32
	v := &concurrentCountingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		onFetch: func() {
			cur := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&active, -1)
		},
	}
	symbols := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		symbols = append(symbols, fmt.Sprintf("SYM%02d-USDT", i))
	}
	got := defaultUnifiedMarketFetchMarketsByVenue(context.Background(), map[string]venue.Venue{
		binance.VenueName: v,
	}, symbols)
	if len(got[binance.VenueName]) != len(symbols) {
		t.Fatalf("market snaps = %d, want %d", len(got[binance.VenueName]), len(symbols))
	}
	if peak < 2 {
		t.Fatalf("peak concurrent fetches = %d, want >= 2 (worker pool active)", peak)
	}
	if peak > unifiedMarketFetchConcurrency {
		t.Fatalf("peak concurrent fetches = %d, want <= %d", peak, unifiedMarketFetchConcurrency)
	}
}

type concurrentCountingVenue struct {
	fakeFundingVenue
	onFetch func()
}

func (v *concurrentCountingVenue) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	if v.onFetch != nil {
		v.onFetch()
	}
	return venue.BookTicker{BidPrice: 100, AskPrice: 101}, nil
}

// depthTrackingVenue 记录 FetchDepth/FetchRecentQuoteVolume 调用,用于二次拉深度单测。
type depthTrackingVenue struct {
	fakeFundingVenue
	depthBySym      map[string]venue.Depth
	volSpotBySym    map[string]float64
	volPerpBySym    map[string]float64
	depthErrBySym   map[string]error
	volErr          error
	depthCalls      atomic.Int32
	depthCallsBySym map[string]int
	mu              sync.Mutex
}

func (v *depthTrackingVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{BidPrice: 100, AskPrice: 100.01}, nil
}

func (v *depthTrackingVenue) FetchDepth(_ context.Context, m venue.Market, s venue.Symbol, _ int) (venue.Depth, error) {
	v.depthCalls.Add(1)
	v.mu.Lock()
	v.depthCallsBySym[string(s)]++
	v.mu.Unlock()
	if v.depthErrBySym != nil {
		if err, ok := v.depthErrBySym[string(s)]; ok && err != nil {
			return venue.Depth{}, err
		}
	}
	if v.depthBySym != nil {
		if d, ok := v.depthBySym[string(s)]; ok {
			return d, nil
		}
	}
	return venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 1000}},
		Bids: []venue.DepthLevel{{Price: 100, Qty: 1000}},
	}, nil
}

func (v *depthTrackingVenue) FetchRecentQuoteVolume(_ context.Context, m venue.Market, s venue.Symbol, _ time.Duration) (float64, error) {
	if v.volErr != nil {
		return 0, v.volErr
	}
	key := string(s)
	switch m {
	case venue.MarketSpot:
		if v.volSpotBySym != nil {
			if vol, ok := v.volSpotBySym[key]; ok {
				return vol, nil
			}
		}
	case venue.MarketPerp:
		if v.volPerpBySym != nil {
			if vol, ok := v.volPerpBySym[key]; ok {
				return vol, nil
			}
		}
	}
	return 0, venue.ErrUnsupported
}

func TestUnifiedMarketEnrichMarketsDepthVolumeFillsFields(t *testing.T) {
	thinDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 100.5, Qty: 100}},
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
	}
	deepDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 1000}},
		Bids: []venue.DepthLevel{{Price: 100, Qty: 1000}},
	}
	binanceV := &depthTrackingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		depthBySym: map[string]venue.Depth{
			"THIN-USDT": thinDepth,
			"DEEP-USDT": deepDepth,
		},
		volSpotBySym: map[string]float64{
			"THIN-USDT": 1_000_000,
			"DEEP-USDT": 10_000_000,
		},
		volPerpBySym: map[string]float64{
			"THIN-USDT": 1_000_000,
			"DEEP-USDT": 10_000_000,
		},
		depthCallsBySym: make(map[string]int),
	}
	venues := map[string]venue.Venue{binance.VenueName: binanceV}
	markets := map[string]map[string]CrossVenueSpotPerpMarketSnap{
		binance.VenueName: {
			"THIN-USDT": {SpotMid: 100, PerpMid: 100, HasSpot: true, HasPerp: true},
			"DEEP-USDT": {SpotMid: 100, PerpMid: 100, HasSpot: true, HasPerp: true},
		},
	}
	unifiedMarketEnrichMarketsWithDepthVolume(context.Background(), venues, markets, []string{"THIN-USDT", "DEEP-USDT"})

	thin := markets[binance.VenueName]["THIN-USDT"]
	if thin.SpotDepth == nil || thin.PerpDepth == nil {
		t.Fatal("expected depth filled for THIN-USDT")
	}
	if thin.Volume24hSpotUSDT != 1_000_000 || thin.Volume24hPerpUSDT != 1_000_000 {
		t.Fatalf("THIN volume spot=%.0f perp=%.0f", thin.Volume24hSpotUSDT, thin.Volume24hPerpUSDT)
	}
	deep := markets[binance.VenueName]["DEEP-USDT"]
	if deep.SpotDepth == nil || deep.PerpDepth == nil {
		t.Fatal("expected depth filled for DEEP-USDT")
	}
	if deep.Volume24hSpotUSDT != 10_000_000 || deep.Volume24hPerpUSDT != 10_000_000 {
		t.Fatalf("DEEP volume spot=%.0f perp=%.0f", deep.Volume24hSpotUSDT, deep.Volume24hPerpUSDT)
	}

	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	exec.SizingMaxSlippageBps = 20
	exec.MaxPositionUSDT = 50
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	thinNotional := crossVenueSpotPerpTradableNotional(cfg, thin, thin)
	if thinNotional >= 50 {
		t.Fatalf("thin book notional=%.2f want <50", thinNotional)
	}
	deepNotional := crossVenueSpotPerpTradableNotional(cfg, deep, deep)
	if math.Abs(deepNotional-50) > 1e-9 {
		t.Fatalf("deep book notional=%.2f want max_position 50", deepNotional)
	}
}

func TestUnifiedMarketEnrichMarketsDepthVolumeOnlyTopN(t *testing.T) {
	v := &depthTrackingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		depthCallsBySym:  make(map[string]int),
	}
	venues := map[string]venue.Venue{binance.VenueName: v}
	bySym := make(map[string]CrossVenueSpotPerpMarketSnap, rotationScanDepthFetchMaxSymbols+10)
	symbols := make([]string, 0, rotationScanDepthFetchMaxSymbols+10)
	for i := 0; i < rotationScanDepthFetchMaxSymbols+10; i++ {
		sym := fmt.Sprintf("SYM%03d-USDT", i)
		symbols = append(symbols, sym)
		bySym[sym] = CrossVenueSpotPerpMarketSnap{SpotMid: 100, PerpMid: 100, HasSpot: true, HasPerp: true}
	}
	markets := map[string]map[string]CrossVenueSpotPerpMarketSnap{binance.VenueName: bySym}
	unifiedMarketEnrichMarketsWithDepthVolume(context.Background(), venues, markets, symbols)

	if v.depthCallsBySym[symbols[0]] == 0 {
		t.Fatalf("expected depth fetch for first symbol %s", symbols[0])
	}
	if v.depthCallsBySym[symbols[rotationScanDepthFetchMaxSymbols-1]] == 0 {
		t.Fatalf("expected depth fetch for symbol at limit index")
	}
	if v.depthCallsBySym[symbols[rotationScanDepthFetchMaxSymbols]] != 0 {
		t.Fatalf("symbol beyond limit %s should not be fetched", symbols[rotationScanDepthFetchMaxSymbols])
	}
}

func TestUnifiedMarketEnrichMarketsDepthVolumeFetchFailureDegrades(t *testing.T) {
	v := &depthTrackingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		depthErrBySym: map[string]error{
			"FAIL-USDT": errors.New("depth unavailable"),
		},
		volErr:          venue.ErrUnsupported,
		depthCallsBySym: make(map[string]int),
	}
	venues := map[string]venue.Venue{binance.VenueName: v}
	markets := map[string]map[string]CrossVenueSpotPerpMarketSnap{
		binance.VenueName: {
			"FAIL-USDT": {SpotMid: 100, PerpMid: 100, HasSpot: true, HasPerp: true},
		},
	}
	unifiedMarketEnrichMarketsWithDepthVolume(context.Background(), venues, markets, []string{"FAIL-USDT"})

	mkt := markets[binance.VenueName]["FAIL-USDT"]
	if mkt.SpotDepth != nil || mkt.PerpDepth != nil {
		t.Fatal("depth fetch failure should leave depth nil")
	}
	if mkt.Volume24hSpotUSDT != 0 || mkt.Volume24hPerpUSDT != 0 {
		t.Fatal("volume error should leave volume zero")
	}
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	exec.SizingMaxSlippageBps = 20
	exec.MaxPositionUSDT = 50
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	if got := crossVenueSpotPerpTradableNotional(cfg, mkt, mkt); math.Abs(got-12.5) > 1e-9 {
		t.Fatalf("missing liquidity data should use conservative factor 0.25×max_position, got %.2f", got)
	}
	exec.SizingConservativeNoDepthFactor = 1.0
	cfg = DefaultCrossVenueSpotPerpScannerConfig(exec)
	if got := crossVenueSpotPerpTradableNotional(cfg, mkt, mkt); math.Abs(got-50) > 1e-9 {
		t.Fatalf("factor=1.0 should degrade to max_position, got %.2f", got)
	}
}

func TestCrossVenueSpotPerpTradableNotionalBeyondDepthFetchLimitConservative(t *testing.T) {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	exec.SizingMaxSlippageBps = 20
	exec.MaxPositionUSDT = 100
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	// 无二次拉深度:MarketSnap 无 Depth/Volume → 保守系数而非满额。
	mkt := CrossVenueSpotPerpMarketSnap{SpotMid: 100, PerpMid: 100, HasSpot: true, HasPerp: true}
	got := crossVenueSpotPerpTradableNotional(cfg, mkt, mkt)
	if math.Abs(got-25) > 1e-9 {
		t.Fatalf("no depth symbol notional=%.2f want 25 (100×0.25)", got)
	}
}

func TestUnifiedRotationRunner_ScanCandidatesLiquidityAdaptiveDepth(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	thinDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100000, Qty: 0.0003}},
		Bids: []venue.DepthLevel{{Price: 100000, Qty: 0.0003}},
	}
	binanceV := &depthTrackingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		depthBySym:       map[string]venue.Depth{"BTC-USDT": thinDepth},
		depthCallsBySym:  make(map[string]int),
	}
	bybitV := &depthTrackingVenue{
		fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName},
		depthBySym:       map[string]venue.Depth{"BTC-USDT": thinDepth},
		depthCallsBySym:  make(map[string]int),
	}
	r := &UnifiedRotationRunner{
		bindings: map[string]*rotationExecutorBinding{},
		venues:   map[string]venue.Venue{binance.VenueName: binanceV, bybit.VenueName: bybitV},
		nowFn:    func() time.Time { return now },
		execCfg: func() CrossVenueExecutorConfig {
			cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
			cfg.SizingMaxSlippageBps = 20
			return cfg
		}(),
		rotationCfg: DefaultRotationConfig(),
		fetchDeps: unifiedMarketPipelineFetchDeps{
			fetchMarkets: func(_ context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
				out := make(map[string]map[string]CrossVenueSpotPerpMarketSnap)
				for venueName, v := range venues {
					bySym := make(map[string]CrossVenueSpotPerpMarketSnap)
					for _, sym := range symbols {
						if _, err := v.FetchBookTicker(context.Background(), venue.MarketSpot, venue.Symbol(sym)); err == nil {
							bySym[sym] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
								SpotMid: 100000, PerpMid: 100050, HasSpot: true, HasPerp: true,
							})
						}
					}
					out[venueName] = bySym
				}
				return out
			},
			fetchAvailableUSDT: func(context.Context, map[string]venue.Venue, []string) map[string]float64 {
				return map[string]float64{binance.VenueName: 200, bybit.VenueName: 200}
			},
		},
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "liq-snap",
		ScannedAt:  now,
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {
				"BTC-USDT": {Rate: 0.0003, IntervalHours: 8, NextFundingTime: nextFunding},
			},
			binance.VenueName: {
				"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
			},
		},
	}
	candidates, _, err := r.defaultScanRotationCandidates(context.Background(), snap)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if binanceV.depthCallsBySym["BTC-USDT"] == 0 {
		t.Fatal("expected secondary depth fetch for BTC-USDT on binance")
	}
	var cvsp *UnifiedOpportunity
	for i := range candidates {
		if candidates[i].StrategyType == UnifiedStrategyCrossVenueSpotPerp && candidates[i].Symbol == "BTC-USDT" {
			cvsp = &candidates[i]
			break
		}
	}
	if cvsp == nil {
		t.Fatal("expected cross-venue spot-perp candidate for BTC-USDT")
	}
	if len(cvsp.Legs) == 0 || cvsp.Legs[0].Notional >= 50 {
		t.Fatalf("thin book carry notional=%.2f want <50", cvsp.Legs[0].Notional)
	}
}

func TestUnifiedMarketApplyInstrumentMetaCanonicalKey(t *testing.T) {
	instrBySym := map[string]map[venue.Market]venue.Instrument{
		"BEL-USDT": {
			venue.MarketSpot: {TickSize: 0.0001, StepSize: 0.01, MinNotional: 5},
			venue.MarketPerp: {TickSize: 0.0001, StepSize: 0.1, MinNotional: 5},
		},
	}
	mkt := CrossVenueSpotPerpMarketSnap{}
	unifiedMarketApplyInstrumentMeta(&mkt, "bel-usdt", instrBySym)
	if mkt.SpotTickSize != 0.0001 {
		t.Fatalf("SpotTickSize = %v, want 0.0001", mkt.SpotTickSize)
	}
	if mkt.SpotStepSize != 0.01 {
		t.Fatalf("SpotStepSize = %v, want 0.01", mkt.SpotStepSize)
	}
	if mkt.PerpTickSize != 0.0001 {
		t.Fatalf("PerpTickSize = %v, want 0.0001", mkt.PerpTickSize)
	}
}
