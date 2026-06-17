package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

// cvspTestVenue 是跨所 spot-perp 执行器单测用的 fake venue.Venue。
type cvspTestVenue struct {
	fakeFundingVenue
	name            string
	bookSpot        venue.BookTicker
	bookPerp        venue.BookTicker
	balances        map[string]float64
	positions       []venue.Position
	placeCount        *int32
	placeRequests     []venue.OrderRequest
	placeFailSpot     bool
	placeFailNext     bool
	clearOnReduceOnly bool
}

func newCVSPTestVenue(name string) *cvspTestVenue {
	pc := int32(0)
	return &cvspTestVenue{
		fakeFundingVenue: fakeFundingVenue{name: name},
		name:             name,
		bookSpot: venue.BookTicker{
			BidPrice: 99, AskPrice: 100, BidQty: 1000, AskQty: 1000,
		},
		bookPerp: venue.BookTicker{
			BidPrice: 99.5, AskPrice: 100.5, BidQty: 1000, AskQty: 1000,
		},
		balances:  map[string]float64{"USDT": 10000},
		placeCount: &pc,
		clearOnReduceOnly: true,
	}
}

func (f *cvspTestVenue) Name() string { return f.name }

func (f *cvspTestVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{}
}

func (f *cvspTestVenue) FetchBookTicker(_ context.Context, m venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	if m == venue.MarketSpot {
		return f.bookSpot, nil
	}
	return f.bookPerp, nil
}

func (f *cvspTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	byAsset := make(map[string]venue.Balance, len(f.balances))
	for asset, qty := range f.balances {
		byAsset[asset] = venue.Balance{Asset: asset, Free: qty}
	}
	return venue.Balances{ByAsset: byAsset}, nil
}

func (f *cvspTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *cvspTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	f.placeRequests = append(f.placeRequests, req)
	if f.placeFailNext {
		f.placeFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated fail")
	}
	if f.placeFailSpot && req.Market == venue.MarketSpot {
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated market fail")
	}

	base := "BTC"
	switch req.Market {
	case venue.MarketPerp:
		if req.Side == venue.SideSell && !req.ReduceOnly {
			f.positions = []venue.Position{{
				Symbol: req.Symbol, PositionAmt: -req.Qty, EntryPrice: req.Price,
			}}
		}
		if req.ReduceOnly && req.Side == venue.SideBuy && f.clearOnReduceOnly {
			f.positions = nil
		}
	case venue.MarketSpot:
		if req.Side == venue.SideBuy {
			cost := req.Qty * req.Price
			f.balances["USDT"] -= cost
			f.balances[base] += req.Qty
		}
		if req.Side == venue.SideSell {
			f.balances[base] -= req.Qty
			f.balances["USDT"] += req.Qty * req.Price
		}
	}

	return venue.OrderResult{
		Symbol:        req.Symbol,
		Market:        req.Market,
		ClientOrderID: req.ClientOrderID,
		Status:        "filled",
		ExecutedQty:   req.Qty,
		AvgPrice:      req.Price,
		CumQuoteUSDT:  req.Qty * req.Price,
	}, nil
}

func (f *cvspTestVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}

func (f *cvspTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	for _, req := range f.placeRequests {
		if req.ClientOrderID == clientOrderID {
			return venue.OrderResult{
				Symbol: req.Symbol, Market: req.Market, ClientOrderID: clientOrderID,
				Status: "filled", ExecutedQty: req.Qty, AvgPrice: req.Price,
			}, nil
		}
	}
	return venue.OrderResult{Status: "unknown", ClientOrderID: clientOrderID}, nil
}

var _ venue.Venue = (*cvspTestVenue)(nil)

func testCVSPExecutor(t *testing.T, dir string, spotV, perpV *cvspTestVenue) *CrossVenueSpotPerpExecutor {
	t.Helper()
	cfg := testCrossVenueExecutorConfig()
	e := NewCrossVenueSpotPerpExecutor(cfg, map[string]venue.Venue{
		spotV.name: spotV,
		perpV.name: perpV,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	return e
}

func testCVSPOpportunity(spotVenue, perpVenue string, qty float64) UnifiedOpportunity {
	notional := qty * 100
	return UnifiedOpportunity{
		ID:             "cross_venue_spot_perp:BTC-USDT:" + spotVenue + "+" + perpVenue + ":test",
		StrategyType:   UnifiedStrategyCrossVenueSpotPerp,
		Symbol:         "BTC-USDT",
		ExpectedNetEVUSDT: 1.5,
		ExpectedHoldHours: 8,
		Legs: []UnifiedOpportunityLeg{
			{Venue: spotVenue, Market: "spot", Side: "buy", Qty: qty, Notional: notional},
			{Venue: perpVenue, Market: "perp", Side: "short", Qty: qty, Notional: notional},
		},
	}
}

func TestCrossVenueSpotPerpShadowOpenNoOrders(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), false)
	if err != nil {
		t.Fatalf("executeOpen shadow: %v", err)
	}
	if pos.Status != CVSPStatusOpen {
		t.Fatalf("expected open status, got %s", pos.Status)
	}
	if pos.SpotLeg.Qty != 0.01 || pos.PerpLeg.Qty != 0.01 {
		t.Fatalf("unexpected synthetic qty: spot=%v perp=%v", pos.SpotLeg.Qty, pos.PerpLeg.Qty)
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatalf("shadow must not place orders")
	}
}

func TestCrossVenueSpotPerpLiveOpenBothLegs(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err != nil {
		t.Fatalf("executeOpen live: %v", err)
	}
	if len(spot.placeRequests) != 1 {
		t.Fatalf("expected 1 spot order, got %d", len(spot.placeRequests))
	}
	if len(perp.placeRequests) != 1 {
		t.Fatalf("expected 1 perp order, got %d", len(perp.placeRequests))
	}
	spotReq := spot.placeRequests[0]
	perpReq := perp.placeRequests[0]
	if spotReq.Market != venue.MarketSpot || spotReq.Side != venue.SideBuy {
		t.Fatalf("spot leg wrong: market=%s side=%s", spotReq.Market, spotReq.Side)
	}
	if perpReq.Market != venue.MarketPerp || perpReq.Side != venue.SideSell {
		t.Fatalf("perp leg wrong: market=%s side=%s", perpReq.Market, perpReq.Side)
	}
	if perpReq.ReduceOnly {
		t.Fatal("open perp leg must not be reduce-only")
	}
}

func TestCrossVenueSpotPerpLiveOpenSecondLegFailRollback(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	spot.placeFailSpot = true
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil {
		t.Fatal("expected second leg failure")
	}
	var sawRollback bool
	for _, req := range perp.placeRequests {
		if req.ReduceOnly && req.Side == venue.SideBuy && req.Market == venue.MarketPerp {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Fatalf("expected perp rollback buy reduce-only, requests=%+v", perp.placeRequests)
	}
}

func TestCrossVenueSpotPerpPositionPersistLoad(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	e2 := NewCrossVenueSpotPerpExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{
		binance.VenueName: spot,
		bybit.VenueName:   perp,
	}, dir, nil)
	loaded := e2.Positions()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 position, got %d", len(loaded))
	}
	if loaded[0].Symbol != pos.Symbol || loaded[0].Status != CVSPStatusOpen {
		t.Fatalf("loaded mismatch: %+v", loaded[0])
	}
}

func TestCrossVenueSpotPerpCloseSettlement(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	spot.balances["BTC"] = pos.SpotLeg.Qty
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -pos.PerpLeg.Qty, EntryPrice: 100}}

	if err := e.closePosition(context.Background(), pos, true, "test close"); err != nil {
		t.Fatalf("close: %v", err)
	}
	settlePath := filepath.Join(crossVenueSpotPerpRecordDir(dir), crossVenueSpotPerpSettlementsFile)
	data, err := os.ReadFile(settlePath)
	if err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	if !strings.Contains(string(data), "test close") {
		t.Fatalf("settlement missing close reason: %s", data)
	}
	if !strings.Contains(string(data), "BTC-USDT") {
		t.Fatalf("settlement missing symbol: %s", data)
	}
	remaining := e.Positions()
	if len(remaining) != 0 {
		t.Fatalf("expected position removed after close, got %d", len(remaining))
	}
}

func TestCrossVenueSpotPerpReconcileOrphanLeg(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true

	pos := CrossVenueSpotPerpPosition{
		Symbol:    "BTC-USDT",
		SpotVenue: binance.VenueName,
		PerpVenue: bybit.VenueName,
		Status:    CVSPStatusOpen,
		OpenedAt:  e.nowFn(),
		UpdatedAt: e.nowFn(),
		IdemPrefix: "cvsp-test",
		SpotLeg:   CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01},
		PerpLeg:   CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
	}
	_ = e.persistPosition(pos)
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.01, EntryPrice: 100}}

	e.reconcileOnStart(context.Background())
	var sawRollback bool
	for _, req := range perp.placeRequests {
		if req.ReduceOnly && req.Side == venue.SideBuy {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Fatal("reconcile should flatten orphan perp leg")
	}
}
