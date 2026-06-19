package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestCrossVenueCloseAllPositionsSyntheticLedgerOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = store.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now,
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		IdemPrefix: "syn",
	})
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	cfg := testCrossVenueExecutorConfig()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(high.placeCount) + atomic.LoadInt32(low.placeCount); n != 0 {
		t.Fatalf("synthetic ledger must not place orders, got %d", n)
	}
	ps, _ := store.load()
	if len(activeCrossVenuePositions(ps)) != 0 {
		t.Fatalf("ledger should be cleared, got %d active", len(activeCrossVenuePositions(ps)))
	}
}

func TestCrossVenueCloseAllPositionsRealExposure(t *testing.T) {
	dir := t.TempDir()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = store.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now,
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		IdemPrefix: "live",
	})
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	high.clearOnReduceOnly = true
	low.clearOnReduceOnly = true
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(high.placeCount) + atomic.LoadInt32(low.placeCount); n < 2 {
		t.Fatalf("expected close orders for both legs, got %d", n)
	}
	ps, _ := store.load()
	if len(activeCrossVenuePositions(ps)) != 0 {
		t.Fatalf("ledger should be cleared after close")
	}
}

func TestSVSPCloseAllPositionsSyntheticLedgerOnly(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	_ = e.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "syn",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("synthetic ledger must not place orders")
	}
	ps, _ := e.store.load()
	if len(activeSingleVenueSpotPerpPositions(ps)) != 0 {
		t.Fatal("ledger should be cleared")
	}
}

func TestSVSPCloseAllPositionsRealExposure(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	_ = e.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "live",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if atomic.LoadInt32(v.placeCount) < 2 {
		t.Fatalf("expected close orders, got %d", atomic.LoadInt32(v.placeCount))
	}
}

func TestSVPSCloseAllPositionsSyntheticLedgerOnly(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }
	_ = e.persistPosition(SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "syn",
		SpotLeg: SingleVenuePerpSpotLegState{Qty: 0.5},
		PerpLeg: SingleVenuePerpSpotLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("synthetic ledger must not place orders")
	}
}

func TestCVSPCloseAllPositionsSyntheticLedgerOnly(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)
	_ = e.persistPosition(CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "syn",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(spot.placeCount) + atomic.LoadInt32(perp.placeCount); n != 0 {
		t.Fatalf("synthetic ledger must not place orders, got %d", n)
	}
}

func TestCVSPCloseAllPositionsRealExposure(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	spot.balances["BTC"] = 0.5
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewCrossVenueSpotPerpExecutor(cfg, map[string]venue.Venue{
		binance.VenueName: spot,
		bybit.VenueName:   perp,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	_ = e.persistPosition(CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "live",
		SpotLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: CrossVenueSpotPerpLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(spot.placeCount) + atomic.LoadInt32(perp.placeCount); n < 2 {
		t.Fatalf("expected close orders, got %d", n)
	}
}

func TestCVPSCloseAllPositionsSyntheticLedgerOnly(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)
	_ = e.persistPosition(CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "syn",
		SpotLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
		PerpLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(spot.placeCount) + atomic.LoadInt32(perp.placeCount); n != 0 {
		t.Fatalf("synthetic ledger must not place orders, got %d", n)
	}
}

func TestCVPSCloseAllPositionsRealExposure(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	spot.borrowedBase["BTC"] = 0.5
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewCrossVenuePerpSpotExecutor(cfg, map[string]venue.Venue{
		binance.VenueName: spot,
		bybit.VenueName:   perp,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	_ = e.persistPosition(CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "live",
		SpotLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
		PerpLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
	})

	closed, errs := e.CloseAllPositions(context.Background(), true)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if closed != 1 {
		t.Fatalf("closed=%d want 1", closed)
	}
	if n := atomic.LoadInt32(spot.placeCount) + atomic.LoadInt32(perp.placeCount); n < 2 {
		t.Fatalf("expected close orders, got %d", n)
	}
}

func TestStrategyFlattenAllAPI(t *testing.T) {
	dir := t.TempDir()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = store.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now,
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		IdemPrefix: "api",
	})
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	cfg := testCrossVenueExecutorConfig()
	exec := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	svspV := newSVSPTestVenue(binance.VenueName)
	svspDir := t.TempDir()
	svspExec := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: svspV}, svspDir, testSVSPLogger())
	svspExec.nowFn = func() time.Time { return now }
	_ = svspExec.persistPosition(SingleVenueSpotPerpPosition{
		Symbol: "ETH-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "api",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.3},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.3},
	})

	s := &DashboardServer{
		crossVenueExecutor: exec,
		svspExec:           svspExec,
		controlToken:       "test-secret",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/strategy/flatten-all", bytes.NewReader([]byte(`{"confirm":"FLATTEN"}`)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Token", "test-secret")
	rec := httptest.NewRecorder()
	s.handleStrategyFlattenAllAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp StrategyFlattenAllResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.TotalClosed != 2 {
		t.Fatalf("total_closed=%d want 2", resp.TotalClosed)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("results=%d want 5 strategies", len(resp.Results))
	}
	if atomic.LoadInt32(high.placeCount)+atomic.LoadInt32(low.placeCount)+atomic.LoadInt32(svspV.placeCount) != 0 {
		t.Fatal("synthetic positions must not trigger orders")
	}
}

func TestStrategyFlattenAllAPI_RequiresConfirm(t *testing.T) {
	s := &DashboardServer{controlToken: "test-secret"}
	req := httptest.NewRequest(http.MethodPost, "/api/strategy/flatten-all", bytes.NewReader([]byte("{}")))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Dashboard-Token", "test-secret")
	rec := httptest.NewRecorder()
	s.handleStrategyFlattenAllAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}
