package app

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

// cvspTestVenue 是跨所 spot-perp 执行器单测用的 fake venue.Venue。
type cvspTestVenue struct {
	fakeFundingVenue
	name              string
	bookSpot          venue.BookTicker
	bookPerp          venue.BookTicker
	balances          map[string]float64
	positions         []venue.Position
	placeCount        *int32
	placeRequests     []venue.OrderRequest
	placeFailSpot     bool
	placeFailNext     bool
	perpUnknownNext   bool
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
		balances:          map[string]float64{"USDT": 10000},
		placeCount:        &pc,
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
	if req.Market == venue.MarketPerp && f.perpUnknownNext {
		f.perpUnknownNext = false
		f.positions = []venue.Position{{
			Symbol: req.Symbol, PositionAmt: -req.Qty, EntryPrice: req.Price,
		}}
		return venue.OrderResult{
			Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
			Status: "unknown", ExecutedQty: req.Qty, AvgPrice: req.Price,
		}, errors.New("simulated perp unknown")
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
var _ venue.PerpMarginProvider = (*cvspTestVenue)(nil)

func (f *cvspTestVenue) FetchPerpAvailableUSDT(context.Context) (float64, error) {
	if f.balances == nil {
		return 0, nil
	}
	return f.balances["USDT"], nil
}

func (f *cvspTestVenue) TopUpPerpMarginUSDT(context.Context, float64) (float64, error) {
	return 0, nil
}

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
		ID:                "cross_venue_spot_perp:BTC-USDT:" + spotVenue + "+" + perpVenue + ":test",
		StrategyType:      UnifiedStrategyCrossVenueSpotPerp,
		Symbol:            "BTC-USDT",
		ExpectedNetEVUSDT: 1.5,
		ExpectedHoldHours: 8,
		Legs: []UnifiedOpportunityLeg{
			{Venue: spotVenue, Market: "spot", Side: "buy", Qty: qty, Notional: notional},
			{Venue: perpVenue, Market: "perp", Side: "short", Qty: qty, Notional: notional},
		},
	}
}

func TestCrossVenueSpotPerpOffOpenNoOrders(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), false)
	if err != nil {
		t.Fatalf("executeOpen dry-run: %v", err)
	}
	if pos.Symbol != "" {
		t.Fatalf("dry-run must not synthesize position, got %+v", pos)
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatalf("dry-run must not place orders")
	}
	assertRealLedgerUntouched(t, dir, "crossvenuespotperp/positions.json")
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

func TestCrossVenueSpotPerpFirstLegUnknownBlocksRollback(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	perp.perpUnknownNext = true
	e := testCVSPExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected first-leg unknown to block rollback, got pos=%+v err=%v", pos, err)
	}
	if len(spot.placeRequests) != 0 {
		t.Fatalf("second leg must not be attempted, spot places=%d", len(spot.placeRequests))
	}
	for _, req := range perp.placeRequests {
		if req.ReduceOnly {
			t.Fatal("first-leg unknown must not trigger perp rollback")
		}
	}
	loaded := e.Positions()
	if len(loaded) != 1 {
		t.Fatalf("expected position retained for reconcile, got %d", len(loaded))
	}
	if loaded[0].PerpLeg.Qty <= executionQtyTolerance {
		t.Fatalf("expected partial first leg qty recorded, got %.6f", loaded[0].PerpLeg.Qty)
	}
}

func TestCrossVenueSpotPerpFirstLegExplicitFailNoSecondLeg(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	perp.placeFailNext = true
	e := testCVSPExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVSPOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil {
		t.Fatal("expected first-leg explicit failure")
	}
	if len(spot.placeRequests) != 0 {
		t.Fatal("explicit first-leg failure must not attempt spot hedge")
	}
}

type cvspDeadManVenue struct {
	*cvspTestVenue
	arms int
}

func (v *cvspDeadManVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (v *cvspDeadManVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	v.arms++
	return nil
}

func TestCrossVenueSpotPerpManageOpenArmsDeadManForPerpVenue(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perpBase := newCVSPTestVenue(okx.VenueName)
	perp := &cvspDeadManVenue{cvspTestVenue: perpBase}
	spot.balances["BTC"] = 0.01
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.01, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	e := NewCrossVenueSpotPerpExecutor(cfg, map[string]venue.Venue{
		binance.VenueName: spot,
		okx.VenueName:     perp,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }

	pos := CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: okx.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "cvsp-deadman",
		SpotLeg: CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01, EntryPrice: 100},
		PerpLeg: CrossVenueSpotPerpLegState{Venue: okx.VenueName, Market: "perp", Side: "short", Qty: 0.01, EntryPrice: 100},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	if perp.arms != 1 {
		t.Fatalf("expected dead-man arm once for perp venue, got %d", perp.arms)
	}
}

func TestCrossVenueSpotPerpManageOpenSkipsUnsupportedDeadManVenue(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	spot.balances["BTC"] = 0.01
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.01, EntryPrice: 100}}
	e := testCVSPExecutor(t, dir, spot, perp)

	pos := CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "cvsp-no-deadman",
		SpotLeg: CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01, EntryPrice: 100},
		PerpLeg: CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01, EntryPrice: 100},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
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
		Symbol:     "BTC-USDT",
		SpotVenue:  binance.VenueName,
		PerpVenue:  bybit.VenueName,
		Status:     CVSPStatusOpen,
		OpenedAt:   e.nowFn(),
		UpdatedAt:  e.nowFn(),
		IdemPrefix: "cvsp-test",
		SpotLeg:    CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01},
		PerpLeg:    CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
	}
	_ = e.persistPosition(pos)
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.01, EntryPrice: 100}}

	e.reconcileOnStart(context.Background(), true)
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

func TestCrossVenueSpotPerpReconcileIgnoresUntrackedPerpPerpPosition(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.004, EntryPrice: 110000}}

	e.reconcileOnStart(context.Background(), true)
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatal("reconcile must not flatten untracked perp-perp position")
	}
}

func TestCrossVenueSpotPerpCloseSkipsWhenMeasuredZero(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)
	pos := CrossVenueSpotPerpPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVSPStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "zero-measured",
		SpotLeg: CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01},
		PerpLeg: CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
	}
	if err := e.closePosition(context.Background(), pos, true, "zero"); err == nil {
		t.Fatal("expected close to skip when measured qty is zero")
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatal("measured=0 must not place blind flatten orders")
	}
}

func TestCrossVenueSpotPerpDustOrphanClosesAndFreesSlot(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	spot.bookSpot = venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041, BidQty: 1000, AskQty: 1000}
	e := testCVSPExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true

	pos := CrossVenueSpotPerpPosition{
		Symbol:        "PHA-USDT",
		SpotVenue:     binance.VenueName,
		PerpVenue:     bybit.VenueName,
		Status:        CVSPStatusClosing,
		OpportunityID: "cvsp-pha-test",
		OpenedAt:      e.nowFn().Add(-time.Hour),
		UpdatedAt:     e.nowFn(),
		IdemPrefix:    "cvsp-pha",
		CloseReason:   "continuation_score<0",
		SpotLeg:       CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 1.01, EntryPrice: 0.04},
		PerpLeg:       CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
	}
	spot.balances["PHA"] = 1.01
	perp.positions = nil
	_ = e.persistPosition(pos)

	if err := e.closePosition(context.Background(), pos, true, "rotation close"); err != nil {
		t.Fatalf("dust orphan close: %v", err)
	}
	if len(e.Positions()) != 0 {
		t.Fatalf("expected position removed after dust close, got %d", len(e.Positions()))
	}
	if e.CountRealHeldPositions(context.Background()) != 0 {
		t.Fatal("dust-closed position must not occupy real held slot")
	}
}

func TestCrossVenueSpotPerpNakedPerpOrphanStillCritical(t *testing.T) {
	dir := t.TempDir()
	spot := newCVSPTestVenue(binance.VenueName)
	perp := newCVSPTestVenue(bybit.VenueName)
	e := testCVSPExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true

	pos := CrossVenueSpotPerpPosition{
		Symbol:     "PHA-USDT",
		SpotVenue:  binance.VenueName,
		PerpVenue:  bybit.VenueName,
		Status:     CVSPStatusOpen,
		OpenedAt:   e.nowFn(),
		UpdatedAt:  e.nowFn(),
		IdemPrefix: "cvsp-naked",
		SpotLeg:    CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 10, EntryPrice: 0.04},
		PerpLeg:    CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 10, EntryPrice: 0.04},
	}
	perp.positions = []venue.Position{{Symbol: "PHA-USDT", PositionAmt: -10, EntryPrice: 0.04}}
	_ = e.persistPosition(pos)

	_, _ = e.managePosition(context.Background(), pos, true)
	remaining := e.Positions()
	if len(remaining) != 1 {
		t.Fatalf("naked perp orphan must stay active, got %d positions", len(remaining))
	}
	if remaining[0].Status != CVSPStatusOrphanSingleLeg {
		t.Fatalf("expected orphan_single_leg, got %s", remaining[0].Status)
	}
}

func TestCrossVenueSpotPerpManageOpenNoResurrectAfterFinalize(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("dust_orphan", func(t *testing.T) {
		dir := t.TempDir()
		spot := newCVSPTestVenue(binance.VenueName)
		perp := newCVSPTestVenue(bybit.VenueName)
		spot.bookSpot = venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041, BidQty: 1000, AskQty: 1000}
		e := testCVSPExecutor(t, dir, spot, perp)
		e.cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0

		pos := CrossVenueSpotPerpPosition{
			Symbol: "PHA-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
			Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "cvsp-dust-resurrect",
			SpotLeg: CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.1875, EntryPrice: 0.04},
			PerpLeg: CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
		}
		spot.balances["PHA"] = 0.1875
		perp.positions = nil
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertCVSPManageOpenFinalizeOnce(t, e, ctx)
	})

	t.Run("both_legs_flat", func(t *testing.T) {
		dir := t.TempDir()
		spot := newCVSPTestVenue(binance.VenueName)
		perp := newCVSPTestVenue(bybit.VenueName)
		e := testCVSPExecutor(t, dir, spot, perp)

		pos := CrossVenueSpotPerpPosition{
			Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
			Status: CVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "cvsp-flat-resurrect",
			SpotLeg: CrossVenueSpotPerpLegState{Venue: binance.VenueName, Market: "spot", Side: "buy", Qty: 0.01},
			PerpLeg: CrossVenueSpotPerpLegState{Venue: bybit.VenueName, Market: "perp", Side: "short", Qty: 0.01},
		}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertCVSPManageOpenFinalizeOnce(t, e, ctx)
	})
}

func assertCVSPManageOpenFinalizeOnce(t *testing.T, e *CrossVenueSpotPerpExecutor, ctx context.Context) {
	t.Helper()
	e.manageOpenPositions(ctx, true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatalf("round1: active position must be removed, got %d", len(e.ListActivePositions()))
	}
	settlements, err := e.ListRecentSettlements(0)
	if err != nil {
		t.Fatalf("round1 settlements: %v", err)
	}
	if len(settlements) != 1 {
		t.Fatalf("round1: expected 1 settlement, got %d", len(settlements))
	}

	e.manageOpenPositions(ctx, true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatalf("round2: position resurrected, got %d active", len(e.ListActivePositions()))
	}
	settlements2, err := e.ListRecentSettlements(0)
	if err != nil {
		t.Fatalf("round2 settlements: %v", err)
	}
	if len(settlements2) != 1 {
		t.Fatalf("round2: duplicate settlement churn, got %d", len(settlements2))
	}
}

func TestCrossVenueSpotPerpEstimateFundingAccrualInterval4h(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	e := &CrossVenueSpotPerpExecutor{nowFn: func() time.Time { return now }}
	pos := &CrossVenueSpotPerpPosition{
		NotionalUSDT:      1000,
		PerpFundingRate:   0.0008,
		PerpIntervalHours: 4,
		EstFundingUSDT:    0,
		OpenedAt:          now.Add(-4 * time.Hour),
		LastAccrualAt:     now.Add(-4 * time.Hour),
	}
	got := e.estimateFundingAccrual(pos)
	legacy := 1000.0 * 0.0008 * 4 / 8
	if math.Abs(got-legacy*2) > 1e-9 {
		t.Fatalf("4h accrual=%v want 2x legacy /8=%v", got, legacy*2)
	}
}

func TestCrossVenueSpotPerpAccrualIntervalFallback8(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	e := &CrossVenueSpotPerpExecutor{nowFn: func() time.Time { return now }}
	pos := &CrossVenueSpotPerpPosition{
		NotionalUSDT:    800,
		PerpFundingRate: 0.001,
		EstFundingUSDT:  0,
		OpenedAt:        now.Add(-8 * time.Hour),
		LastAccrualAt:   now.Add(-8 * time.Hour),
	}
	got := e.estimateFundingAccrual(pos)
	want := 800.0 * 0.001
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("fallback 8h accrual=%v want %v", got, want)
	}
}

func TestDecideRotationExcludesCVSPRollbackBlocked(t *testing.T) {
	now := time.Now().UTC()
	e := &CrossVenueSpotPerpExecutor{
		nowFn:           func() time.Time { return now },
		rollbackBlocked: make(map[string]time.Time),
	}
	e.blockSymbolNewOpen("BEL-USDT")
	if !e.OpenBlockedForSymbol("BEL-USDT") {
		t.Fatal("expected rollback block active for BEL-USDT")
	}

	blocked := RotationOpenBlocked(func(strategyType, symbol string) bool {
		return strategyType == UnifiedStrategyCrossVenueSpotPerp && e.OpenBlockedForSymbol(symbol)
	})
	bel := testRotationOpp("bel", UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", 30, 100, "bybit", "gate")
	eth := testRotationOpp("eth", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 15, 100, "bybit", "gate")
	available := map[string]float64{"bybit": 500, "gate": 500}

	plan := DecideRotationWithBlocked(nil, nil, []UnifiedOpportunity{bel, eth}, available, DefaultRotationConfig(), blocked)
	var opens []RotationAction
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			opens = append(opens, act)
		}
	}
	if len(opens) != 1 {
		t.Fatalf("expected 1 OPEN (ETH only), got %d: %+v", len(opens), plan.Actions)
	}
	if opens[0].Symbol != "ETH-USDT" {
		t.Fatalf("OPEN symbol = %q, want ETH-USDT", opens[0].Symbol)
	}
}
