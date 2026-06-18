package app

import (
	"context"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/okx"
)

func TestLiquidityNotionalCapDepthOnlyDeepBook(t *testing.T) {
	depth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 100}},
		Bids: []venue.DepthLevel{{Price: 99.99, Qty: 100}},
	}
	cap := liquidityNotionalCap(depth, venue.SideBuy, 20, 0, 0)
	if cap < 9990 || cap > 10010 {
		t.Fatalf("deep book cap=%.4f want ~10000", cap)
	}
}

func TestLiquidityNotionalCapDepthThinBook(t *testing.T) {
	depth := venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
	}
	cap := liquidityNotionalCap(depth, venue.SideSell, 20, 0, 0)
	if cap >= 50 {
		t.Fatalf("thin book cap=%.4f want <50", cap)
	}
	if cap <= 0 {
		t.Fatal("thin book cap should be >0")
	}
	slip, ok := simulateTakerSlippageBps(venue.SideSell, depth.Bids, cap, 100)
	if !ok || slip > 20+0.5 {
		t.Fatalf("cap slip=%.2f ok=%v want <=20", slip, ok)
	}
}

func TestLiquidityNotionalCapParticipationOnly(t *testing.T) {
	cap := liquidityNotionalCap(venue.Depth{}, venue.SideBuy, 0, 1_000_000, 0.01)
	if math.Abs(cap-10_000) > 1e-9 {
		t.Fatalf("participation cap=%.2f want 10000", cap)
	}
}

func TestLiquidityNotionalCapMinOfDepthAndParticipation(t *testing.T) {
	depth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 30}},
	}
	cap := liquidityNotionalCap(depth, venue.SideBuy, 20, 10_000_000, 0.01)
	if math.Abs(cap-3000) > 1e-6 {
		t.Fatalf("min cap=%.2f want 3000 (depth < 1%% vol)", cap)
	}
}

func TestLiquidityNotionalCapDisabledReturnsInf(t *testing.T) {
	depth := venue.Depth{Asks: []venue.DepthLevel{{Price: 1, Qty: 1}}}
	if cap := liquidityNotionalCap(depth, venue.SideBuy, 0, 0, 0); cap != math.MaxFloat64 {
		t.Fatalf("disabled cap=%v want +Inf", cap)
	}
	if cap := liquidityNotionalCap(venue.Depth{}, venue.SideBuy, 20, 0, 0); cap != math.MaxFloat64 {
		t.Fatalf("missing depth cap=%v want +Inf", cap)
	}
}

func TestDepthNotionalWithinSlippageBoundary(t *testing.T) {
	levels := []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}}
	cap := depthNotionalWithinSlippage(venue.SideSell, levels, 20, 100)
	if cap <= 0 {
		t.Fatal("expected positive cap within 20bps")
	}
	slipAtCap, ok := simulateTakerSlippageBps(venue.SideSell, levels, cap, 100)
	if !ok || slipAtCap > 20+0.5 {
		t.Fatalf("slip at cap=%.2f ok=%v", slipAtCap, ok)
	}
	slipOver, okOver := simulateTakerSlippageBps(venue.SideSell, levels, cap*1.05, 100)
	if okOver && slipOver <= 20 {
		t.Fatalf("expected slip >20 above cap, got %.2f at %.2f", slipOver, cap*1.05)
	}
}

func TestCrossVenueSpotPerpTradableNotionalLiquidityAdaptive(t *testing.T) {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	exec.SizingMaxSlippageBps = 20
	exec.MaxPositionUSDT = 50
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)

	thinSpot := CrossVenueSpotPerpMarketSnap{
		SpotMid: 100,
		PerpMid: 100,
		HasSpot: true,
		HasPerp: true,
		SpotDepth: &venue.Depth{
			Asks: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 100.5, Qty: 100}},
		},
		PerpDepth: &venue.Depth{
			Bids: []venue.DepthLevel{{Price: 100, Qty: 100}},
		},
	}
	liqCap := crossVenueSpotPerpLiquidityNotionalCap(cfg.Exec, thinSpot, thinSpot)
	thinNotional := crossVenueSpotPerpTradableNotional(cfg, thinSpot, thinSpot)
	if liqCap >= 50 || thinNotional >= 50 {
		t.Fatalf("thin spot depth liqCap=%.4f notional=%.2f want both <50", liqCap, thinNotional)
	}

	deep := CrossVenueSpotPerpMarketSnap{
		SpotMid:           100,
		PerpMid:           100,
		HasSpot:           true,
		HasPerp:           true,
		Volume24hSpotUSDT: 10_000_000,
		Volume24hPerpUSDT: 10_000_000,
		SpotDepth: &venue.Depth{
			Asks: []venue.DepthLevel{{Price: 100, Qty: 1000}},
		},
		PerpDepth: &venue.Depth{
			Bids: []venue.DepthLevel{{Price: 100, Qty: 1000}},
		},
	}
	deepNotional := crossVenueSpotPerpTradableNotional(cfg, deep, deep)
	if math.Abs(deepNotional-50) > 1e-9 {
		t.Fatalf("deep book notional=%.2f want max_position 50", deepNotional)
	}
}

func TestCrossVenueSpotPerpTradableNotionalSizingSwitchZeroRegression(t *testing.T) {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	exec.SizingMaxSlippageBps = 0
	exec.SizingMaxParticipationPct24h = 0
	exec.MaxPositionUSDT = 50
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	spotMkt := CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	}
	if got := crossVenueSpotPerpTradableNotional(cfg, spotMkt, spotMkt); got != 0 {
		t.Fatalf("legacy insufficient depth should reject, got %.2f", got)
	}
	spotMkt.SpotDepthUSDT = 5000
	if got := crossVenueSpotPerpTradableNotional(cfg, spotMkt, spotMkt); math.Abs(got-50) > 1e-9 {
		t.Fatalf("legacy sufficient depth got %.2f want 50", got)
	}
}

func TestCrossVenueGateLiquidityAdaptiveSizing(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 100, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.book = venue.BookTicker{BidPrice: 100, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
	}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}},
	}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg.SlippageBudgetBps = 0
	cfg.SizingMaxSlippageBps = 20
	cfg.SizingMaxParticipationPct24h = 0
	cfg.MaxPositionUSDT = 50
	cfg.MaxOpenSlippageBps = 500
	res := evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.001, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("expected gate pass with reduced notional: %s", res.Reason)
	}
	if res.NotionalUSDT >= 50 {
		t.Fatalf("gate notional=%.2f want <50 on thin high leg", res.NotionalUSDT)
	}
	if res.NotionalUSDT <= 0 {
		t.Fatal("gate notional should be >0")
	}
}

func TestApplyLiquiditySizingCapNoDepthConservative(t *testing.T) {
	got := applyLiquiditySizingCap(150, math.MaxFloat64, 0.25)
	if math.Abs(got-37.5) > 1e-9 {
		t.Fatalf("no-depth cap=%.2f want 37.5", got)
	}
	if got := applyLiquiditySizingCap(150, math.MaxFloat64, 1.0); math.Abs(got-150) > 1e-9 {
		t.Fatalf("legacy factor=1 cap=%.2f want 150", got)
	}
	if got := applyLiquiditySizingCap(150, 80, 0.25); math.Abs(got-80) > 1e-9 {
		t.Fatalf("with depth cap=%.2f want 80", got)
	}
}

func TestCrossVenuePerpPerpScanTradableNotionalThinDepth(t *testing.T) {
	thinDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 0.05}},
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}},
	}
	high := &perpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, depth: thinDepth}
	low := &perpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: okx.VenueName}, depth: thinDepth}
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg.MaxPositionUSDT = 150
	cfg.SizingMaxSlippageBps = 20
	cfg.SizingMaxParticipationPct24h = 0
	got := crossVenuePerpPerpScanTradableNotional(
		context.Background(), cfg,
		map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low},
		binance.VenueName, okx.VenueName, "BTC-USDT",
	)
	if got >= 150 {
		t.Fatalf("thin depth scan notional=%.2f want <150", got)
	}
	if got <= 0 {
		t.Fatal("scan notional should be >0")
	}
}

func TestCrossVenuePerpPerpScanTradableNotionalNoDepthConservative(t *testing.T) {
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg.MaxPositionUSDT = 150
	cfg.SizingMaxSlippageBps = 20
	got := crossVenuePerpPerpScanTradableNotional(
		context.Background(), cfg,
		map[string]venue.Venue{binance.VenueName: &perpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}}},
		binance.VenueName, okx.VenueName, "BTC-USDT",
	)
	if math.Abs(got-37.5) > 1e-9 {
		t.Fatalf("missing low venue scan notional=%.2f want 37.5", got)
	}
}

type perpDepthVenue struct {
	fakeFundingVenue
	depth venue.Depth
}

func (v *perpDepthVenue) FetchDepth(context.Context, venue.Market, venue.Symbol, int) (venue.Depth, error) {
	return v.depth, nil
}
