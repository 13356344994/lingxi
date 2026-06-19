package app

import (
	"context"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func TestCarryCloseIocLimitPriceEscalatesThenPierce(t *testing.T) {
	cfg := CrossVenueExecutorConfig{TakerSlippageBudgetBps: 40}
	book := venue.BookTicker{BidPrice: 99, AskPrice: 101}

	p1, pierce := carryCloseIocLimitPrice(cfg, venue.SideBuy, book, 1)
	if pierce {
		t.Fatal("attempt 1 should not pierce")
	}
	want1 := 101 * (1 + 40.0/10000)
	if math.Abs(p1-want1) > 1e-9 {
		t.Fatalf("attempt 1 price=%v want %v", p1, want1)
	}

	p3, pierce := carryCloseIocLimitPrice(cfg, venue.SideBuy, book, 3)
	if pierce {
		t.Fatal("attempt 3 should not pierce")
	}
	want3 := 101 * (1 + 160.0/10000)
	if math.Abs(p3-want3) > 1e-9 {
		t.Fatalf("attempt 3 price=%v want %v", p3, want3)
	}

	p8, pierce := carryCloseIocLimitPrice(cfg, venue.SideBuy, book, 8)
	if !pierce {
		t.Fatal("attempt 8 should pierce")
	}
	if math.Abs(p8-101*crossVenueClosePierceBuyMultiplier) > 1e-9 {
		t.Fatalf("pierce buy price=%v", p8)
	}
}

func TestCarryShouldRebalanceHedgeDebounce(t *testing.T) {
	now := time.Now().UTC()
	if carryShouldRebalanceHedge(0.04, time.Time{}, now) {
		t.Fatal("below threshold should not rebalance")
	}
	if !carryShouldRebalanceHedge(0.10, time.Time{}, now) {
		t.Fatal("above threshold should rebalance")
	}
	if carryShouldRebalanceHedge(0.10, now.Add(-time.Minute), now) {
		t.Fatal("within min interval should not rebalance")
	}
	if !carryShouldRebalanceHedge(0.10, now.Add(-10*time.Minute), now) {
		t.Fatal("after min interval should rebalance")
	}
}

func TestSVSPCloseRetryUsesEscalatedLimitPrice(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	v.clearOnClose = false
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.TakerSlippageBudgetBps = 40
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusClosing, CloseReason: "escalate", CloseAttempts: 2,
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5}, PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "esc",
	}
	_ = e.closePosition(context.Background(), pos, true, "escalate")
	var lastPerpBuy float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp && req.Side == venue.SideBuy && req.ReduceOnly {
			lastPerpBuy = req.Price
		}
	}
	want := 101 * (1 + 160.0/10000)
	if math.Abs(lastPerpBuy-want) > 1e-6 {
		t.Fatalf("attempt 3 perp buy limit=%v want %v", lastPerpBuy, want)
	}
}

func TestSVSPManageRebalanceOnImbalance(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.6
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now.Add(-time.Hour), IdemPrefix: "rb",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.6}, PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	var spotSellQty float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			spotSellQty = req.Qty
		}
	}
	if math.Abs(spotSellQty-0.1) > 1e-9 {
		t.Fatalf("expected spot rebalance sell 0.1, got %.6f", spotSellQty)
	}
}

func TestSVSPManageNoRebalanceBelowThreshold(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.48, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now.Add(-time.Hour), IdemPrefix: "norb",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5}, PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.48},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	if len(v.placeRequests) != 0 {
		t.Fatalf("expected no rebalance orders, got %d", len(v.placeRequests))
	}
}

func TestCVSPCloseRetryUsesEscalatedLimitPrice(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	spot.balances["BTC"] = 0.5
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	perp.clearOnReduceOnly = false
	e := testCVSPExecutor(t, dir, spot, perp)
	cfg := e.cfg
	cfg.CrossVenueExecutor.TakerSlippageBudgetBps = 40
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e.cfg = cfg

	pos := CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusClosing, CloseReason: "escalate", CloseAttempts: 2,
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 0.5}, PerpLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
		OpenedAt: e.nowFn().Add(-time.Hour), IdemPrefix: "esc",
	}
	_ = e.closePosition(context.Background(), pos, true, "escalate")
	var lastPerpBuy float64
	for _, req := range perp.placeRequests {
		if req.Market == venue.MarketPerp && req.Side == venue.SideBuy && req.ReduceOnly {
			lastPerpBuy = req.Price
		}
	}
	want := 100.5 * (1 + 160.0/10000)
	// 价格按 tick(0.01) 量化(buy 向上取整),容忍一个 tick 的偏差。
	if math.Abs(lastPerpBuy-want) > 0.01+1e-9 {
		t.Fatalf("attempt 3 perp buy limit=%v want ~%v(±1 tick)", lastPerpBuy, want)
	}
}

func TestCVSPManageRebalanceOnImbalance(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	spot.balances["BTC"] = 0.6
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	e := testCVSPExecutor(t, dir, spot, perp)
	cfg := e.cfg
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e.cfg = cfg
	pos := CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn().Add(-time.Hour), IdemPrefix: "rb",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 0.6}, PerpLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	var spotSellQty float64
	for _, req := range spot.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			spotSellQty = req.Qty
		}
	}
	if math.Abs(spotSellQty-0.1) > 1e-9 {
		t.Fatalf("expected spot rebalance sell 0.1, got %.6f", spotSellQty)
	}
}
