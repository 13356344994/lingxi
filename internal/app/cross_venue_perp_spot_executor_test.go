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

// cvpsTestVenue 是跨所 perp-spot 执行器单测用的 fake venue.Venue。
type cvpsTestVenue struct {
	fakeFundingVenue
	name                  string
	supportsNegativeCarry bool
	maxBorrowable         float64
	bookSpot              venue.BookTicker
	bookPerp              venue.BookTicker
	balances              map[string]float64
	borrowedBase          map[string]float64
	positions             []venue.Position
	placeCount            *int32
	borrowRepayCount      *int32
	placeRequests         []venue.OrderRequest
	placeFailSpot         bool
	placeFailNext         bool
	spotUnknownNext       bool
	perpPartialFillNext   bool
	perpPartialFillRatio  float64
	clearOnReduceOnly     bool
}

func newCVPSTestVenue(name string, supportsBorrow bool) *cvpsTestVenue {
	pc := int32(0)
	br := int32(0)
	return &cvpsTestVenue{
		fakeFundingVenue:      fakeFundingVenue{name: name},
		name:                  name,
		supportsNegativeCarry: supportsBorrow,
		maxBorrowable:         1000,
		bookSpot: venue.BookTicker{
			BidPrice: 99, AskPrice: 100, BidQty: 1000, AskQty: 1000,
		},
		bookPerp: venue.BookTicker{
			BidPrice: 99.5, AskPrice: 100.5, BidQty: 1000, AskQty: 1000,
		},
		balances:          map[string]float64{"USDT": 10000},
		borrowedBase:      make(map[string]float64),
		placeCount:        &pc,
		borrowRepayCount:  &br,
		clearOnReduceOnly: true,
	}
}

func (f *cvpsTestVenue) Name() string { return f.name }

func (f *cvpsTestVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{SupportsNegativeCarry: f.supportsNegativeCarry}
}

func (f *cvpsTestVenue) MaxBorrowable(_ context.Context, _ string) (float64, error) {
	if f.maxBorrowable <= 0 {
		return 1000, nil
	}
	return f.maxBorrowable, nil
}

func (f *cvpsTestVenue) FetchBookTicker(_ context.Context, m venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	if m == venue.MarketSpot {
		return f.bookSpot, nil
	}
	return f.bookPerp, nil
}

func (f *cvpsTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	byAsset := make(map[string]venue.Balance, len(f.balances)+len(f.borrowedBase))
	for asset, qty := range f.balances {
		byAsset[asset] = venue.Balance{Asset: asset, Free: qty}
	}
	for asset, borrowed := range f.borrowedBase {
		if borrowed > executionQtyTolerance {
			byAsset[asset] = venue.Balance{Asset: asset, Free: -borrowed}
		}
	}
	return venue.Balances{ByAsset: byAsset}, nil
}

func (f *cvpsTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *cvpsTestVenue) BorrowRepay(_ context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	atomic.AddInt32(f.borrowRepayCount, 1)
	asset = strings.ToUpper(asset)
	switch dir {
	case venue.BorrowDirBorrow:
		f.borrowedBase[asset] += amt
	case venue.BorrowDirRepay:
		f.borrowedBase[asset] = math.Max(0, f.borrowedBase[asset]-amt)
	}
	return nil
}

func (f *cvpsTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	f.placeRequests = append(f.placeRequests, req)
	if f.placeFailNext {
		f.placeFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated fail")
	}
	if req.Market == venue.MarketSpot && f.spotUnknownNext && req.MarginEffect == venue.MarginEffectBorrow {
		f.spotUnknownNext = false
		base := "BTC"
		f.borrowedBase[base] += req.Qty
		f.balances["USDT"] += req.Qty * req.Price
		return venue.OrderResult{
			Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
			Status: "unknown", ExecutedQty: req.Qty, AvgPrice: req.Price,
		}, errors.New("simulated spot unknown")
	}
	if f.placeFailSpot && req.Market == venue.MarketSpot {
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated market fail")
	}
	if req.Market == venue.MarketSpot && req.MarginEffect == venue.MarginEffectBorrow && !f.supportsNegativeCarry {
		return venue.OrderResult{Status: "rejected"}, errors.New("borrow unsupported")
	}

	execQty := req.Qty
	if req.Market == venue.MarketPerp && f.perpPartialFillNext {
		f.perpPartialFillNext = false
		ratio := f.perpPartialFillRatio
		if ratio <= 0 {
			ratio = 0.5
		}
		execQty = req.Qty * ratio
	}
	base := "BTC"
	switch req.Market {
	case venue.MarketPerp:
		if req.Side == venue.SideBuy && !req.ReduceOnly {
			f.positions = []venue.Position{{
				Symbol: req.Symbol, PositionAmt: execQty, EntryPrice: req.Price,
			}}
		}
		if req.ReduceOnly && req.Side == venue.SideSell && f.clearOnReduceOnly {
			f.positions = nil
		}
	case venue.MarketSpot:
		if req.Side == venue.SideSell && req.MarginEffect == venue.MarginEffectBorrow {
			f.borrowedBase[base] += req.Qty
			f.balances["USDT"] += req.Qty * req.Price
		}
		if req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			f.borrowedBase[base] = math.Max(0, f.borrowedBase[base]-req.Qty)
			f.balances["USDT"] -= req.Qty * req.Price
		}
	}

	return venue.OrderResult{
		Symbol:        req.Symbol,
		Market:        req.Market,
		ClientOrderID: req.ClientOrderID,
		Status:        "filled",
		ExecutedQty:   execQty,
		AvgPrice:      req.Price,
		CumQuoteUSDT:  req.Qty * req.Price,
	}, nil
}

func (f *cvpsTestVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}

func (f *cvpsTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
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

var _ venue.Venue = (*cvpsTestVenue)(nil)

func testCVPSExecutor(t *testing.T, dir string, spotV, perpV *cvpsTestVenue) *CrossVenuePerpSpotExecutor {
	t.Helper()
	cfg := testCrossVenueExecutorConfig()
	e := NewCrossVenuePerpSpotExecutor(cfg, map[string]venue.Venue{
		spotV.name: spotV,
		perpV.name: perpV,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	return e
}

func testCVPSOpportunity(spotVenue, perpVenue string, qty float64) UnifiedOpportunity {
	notional := qty * 100
	return UnifiedOpportunity{
		ID:                "cross_venue_perp_spot:BTC-USDT:" + perpVenue + "+" + spotVenue + ":test",
		StrategyType:      UnifiedStrategyCrossVenuePerpSpot,
		Symbol:            "BTC-USDT",
		ExpectedNetEVUSDT: 1.5,
		ExpectedHoldHours: 8,
		Legs: []UnifiedOpportunityLeg{
			{Venue: perpVenue, Market: "perp", Side: "long", Qty: qty, Notional: notional},
			{Venue: spotVenue, Market: "spot", Side: "short", Qty: qty, Notional: notional},
		},
	}
}

func TestCrossVenuePerpSpotUnsupportedBorrowSkipped(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, false)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil {
		t.Fatal("expected unsupported borrow venue to be rejected")
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatal("must not place orders when borrow unsupported")
	}
}

func TestCrossVenuePerpSpotOffOpenNoOrders(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), false)
	if err != nil {
		t.Fatalf("executeOpen dry-run: %v", err)
	}
	if pos.Symbol != "" {
		t.Fatalf("dry-run must not synthesize position, got %+v", pos)
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatalf("dry-run must not place orders")
	}
	assertRealLedgerUntouched(t, dir, "crossvenueperpspot/positions.json")
}

func TestCrossVenuePerpSpotLiveOpenBothLegs(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
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
	if spotReq.Market != venue.MarketSpot || spotReq.Side != venue.SideSell || spotReq.MarginEffect != venue.MarginEffectBorrow {
		t.Fatalf("spot leg wrong: market=%s side=%s margin=%v", spotReq.Market, spotReq.Side, spotReq.MarginEffect)
	}
	if perpReq.Market != venue.MarketPerp || perpReq.Side != venue.SideBuy {
		t.Fatalf("perp leg wrong: market=%s side=%s", perpReq.Market, perpReq.Side)
	}
	if perpReq.ReduceOnly {
		t.Fatal("open perp leg must not be reduce-only")
	}
}

func TestCrossVenuePerpSpotLiveOpenSecondLegFailRollback(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	spot.placeFailSpot = false
	perp := newCVPSTestVenue(bybit.VenueName, false)
	perp.placeFailNext = true
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil {
		t.Fatal("expected second leg failure")
	}
	var sawRollback bool
	for _, req := range spot.placeRequests {
		if req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Fatalf("expected spot buy+repay rollback, requests=%+v", spot.placeRequests)
	}
	if spot.borrowedBase["BTC"] > executionQtyTolerance {
		t.Fatalf("borrow should be rolled back, got %.6f", spot.borrowedBase["BTC"])
	}
}

func TestCrossVenuePerpSpotFirstLegUnknownBlocksRollback(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	spot.spotUnknownNext = true
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected first-leg unknown to block rollback, got pos=%+v err=%v", pos, err)
	}
	if len(perp.placeRequests) != 0 {
		t.Fatalf("second leg must not be attempted, perp places=%d", len(perp.placeRequests))
	}
	for _, req := range spot.placeRequests {
		if req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			t.Fatal("first-leg unknown must not trigger spot rollback repay")
		}
	}
	loaded := e.Positions()
	if len(loaded) != 1 {
		t.Fatalf("expected position retained for reconcile, got %d", len(loaded))
	}
	if loaded[0].SpotLeg.Qty <= executionQtyTolerance {
		t.Fatalf("expected partial first leg qty recorded, got %.6f", loaded[0].SpotLeg.Qty)
	}
}

func TestCrossVenuePerpSpotFirstLegExplicitFailNoSecondLeg(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	spot.placeFailNext = true
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err == nil {
		t.Fatal("expected first-leg explicit failure")
	}
	if len(perp.placeRequests) != 0 {
		t.Fatal("explicit first-leg failure must not attempt perp hedge")
	}
}

type cvpsDeadManVenue struct {
	*cvpsTestVenue
	arms int
}

func (v *cvpsDeadManVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (v *cvpsDeadManVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	v.arms++
	return nil
}

func TestCrossVenuePerpSpotManageOpenArmsDeadManForPerpVenue(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perpBase := newCVPSTestVenue(okx.VenueName, false)
	perp := &cvpsDeadManVenue{cvpsTestVenue: perpBase}
	spot.borrowedBase["BTC"] = 0.01
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.01, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	e := NewCrossVenuePerpSpotExecutor(cfg, map[string]venue.Venue{
		binance.VenueName: spot,
		okx.VenueName:     perp,
	}, dir, nil)
	e.nowFn = func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }

	pos := CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: okx.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "cvps-deadman",
		SpotLeg: CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.01, EntryPrice: 100},
		PerpLeg: CrossVenuePerpSpotLegState{Venue: okx.VenueName, Market: "perp", Side: "long", Qty: 0.01, EntryPrice: 100},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	if perp.arms != 1 {
		t.Fatalf("expected dead-man arm once for perp venue, got %d", perp.arms)
	}
}

func TestCrossVenuePerpSpotManageOpenSkipsUnsupportedDeadManVenue(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	spot.borrowedBase["BTC"] = 0.01
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.01, EntryPrice: 100}}
	e := testCVPSExecutor(t, dir, spot, perp)

	pos := CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn(), UpdatedAt: e.nowFn(), IdemPrefix: "cvps-no-deadman",
		SpotLeg: CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.01, EntryPrice: 100},
		PerpLeg: CrossVenuePerpSpotLegState{Venue: bybit.VenueName, Market: "perp", Side: "long", Qty: 0.01, EntryPrice: 100},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
}

func TestCrossVenuePerpSpotPositionPersistLoad(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	e2 := NewCrossVenuePerpSpotExecutor(testCrossVenueExecutorConfig(), map[string]venue.Venue{
		binance.VenueName: spot,
		bybit.VenueName:   perp,
	}, dir, nil)
	loaded := e2.Positions()
	if len(loaded) != 1 {
		t.Fatalf("expected 1 position, got %d", len(loaded))
	}
	if loaded[0].Symbol != pos.Symbol || loaded[0].Status != CVPSStatusOpen {
		t.Fatalf("loaded mismatch: %+v", loaded[0])
	}
}

func TestCrossVenuePerpSpotCloseSettlement(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	pos, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.01), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	spot.borrowedBase["BTC"] = pos.SpotLeg.Qty
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: pos.PerpLeg.Qty, EntryPrice: 100}}

	if err := e.closePosition(context.Background(), pos, true, "test close"); err != nil {
		t.Fatalf("close: %v", err)
	}
	var sawRepay bool
	for _, req := range spot.placeRequests {
		if req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			sawRepay = true
		}
	}
	if !sawRepay {
		t.Fatal("close should buy spot with MarginEffectRepay")
	}
	settlePath := filepath.Join(crossVenuePerpSpotRecordDir(dir), crossVenuePerpSpotSettlementsFile)
	data, err := os.ReadFile(settlePath)
	if err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "test close") {
		t.Fatalf("settlement missing close reason: %s", data)
	}
	if !strings.Contains(content, "BTC-USDT") {
		t.Fatalf("settlement missing symbol: %s", data)
	}
	remaining := e.Positions()
	if len(remaining) != 0 {
		t.Fatalf("expected position removed after close, got %d", len(remaining))
	}
}

func TestCrossVenuePerpSpotReconcileOrphanLeg(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true

	pos := CrossVenuePerpSpotPosition{
		Symbol:     "BTC-USDT",
		SpotVenue:  binance.VenueName,
		PerpVenue:  bybit.VenueName,
		Status:     CVPSStatusOpen,
		OpenedAt:   e.nowFn(),
		UpdatedAt:  e.nowFn(),
		IdemPrefix: "cvps-test",
		SpotLeg:    CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.01},
		PerpLeg:    CrossVenuePerpSpotLegState{Venue: bybit.VenueName, Market: "perp", Side: "long", Qty: 0.01},
	}
	_ = e.persistPosition(pos)
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.01, EntryPrice: 100}}

	e.reconcileOnStart(context.Background(), true)
	var sawFlatten bool
	for _, req := range perp.placeRequests {
		if req.ReduceOnly && req.Side == venue.SideSell {
			sawFlatten = true
		}
	}
	if !sawFlatten {
		t.Fatal("reconcile should flatten orphan perp leg")
	}
}

func TestCrossVenuePerpSpotReconcileIgnoresUntrackedPerpPerpPosition(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.004, EntryPrice: 110000}}

	e.reconcileOnStart(context.Background(), true)
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatal("reconcile must not flatten untracked perp-perp position")
	}
}

func TestCrossVenuePerpSpotCloseSkipsWhenMeasuredZero(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)
	pos := CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn(), IdemPrefix: "zero-measured",
		SpotLeg: CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.01},
		PerpLeg: CrossVenuePerpSpotLegState{Venue: bybit.VenueName, Market: "perp", Side: "long", Qty: 0.01},
	}
	if err := e.closePosition(context.Background(), pos, true, "zero"); err == nil {
		t.Fatal("expected close to skip when measured qty is zero")
	}
	if atomic.LoadInt32(spot.placeCount)+atomic.LoadInt32(perp.placeCount) != 0 {
		t.Fatal("measured=0 must not place blind flatten orders")
	}
}

func TestCrossVenuePerpSpotBorrowLimitExceededSkipsOpen(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	spot.maxBorrowable = 0.1
	perp := newCVPSTestVenue(bybit.VenueName, false)
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.5), true)
	if err == nil || !strings.Contains(err.Error(), "借币额度不足") {
		t.Fatalf("expected borrow limit rejection, got %v", err)
	}
	if atomic.LoadInt32(spot.placeCount) != 0 {
		t.Fatal("borrow limit exceeded must not place orders")
	}
}

func TestCrossVenuePerpSpotPerpPartialFillTriggersRollback(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	perp.perpPartialFillNext = true
	perp.perpPartialFillRatio = 0.4
	e := testCVPSExecutor(t, dir, spot, perp)

	_, err := e.executeOpen(context.Background(), testCVPSOpportunity(binance.VenueName, bybit.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected partial perp fill to fail open")
	}
	if spot.borrowedBase["BTC"] > executionQtyTolerance {
		t.Fatalf("partial fill must rollback borrow, got %.6f", spot.borrowedBase["BTC"])
	}
}

func TestCrossVenuePerpSpotManageOpenNoResurrectAfterFinalize(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("dust_orphan", func(t *testing.T) {
		dir := t.TempDir()
		spot := newCVPSTestVenue(binance.VenueName, true)
		perp := newCVPSTestVenue(bybit.VenueName, false)
		perp.bookPerp = venue.BookTicker{BidPrice: 99, AskPrice: 101, BidQty: 1000, AskQty: 1000}
		e := testCVPSExecutor(t, dir, spot, perp)
		e.cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0

		pos := CrossVenuePerpSpotPosition{
			Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
			Status: CVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "cvps-dust-resurrect",
			SpotLeg: CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
			PerpLeg: CrossVenuePerpSpotLegState{Venue: bybit.VenueName, Market: "perp", Side: "long", Qty: 0.00008, EntryPrice: 100},
		}
		perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.00008, EntryPrice: 100}}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertCVPSManageOpenFinalizeOnce(t, e, ctx)
	})

	t.Run("both_legs_flat", func(t *testing.T) {
		dir := t.TempDir()
		spot := newCVPSTestVenue(binance.VenueName, true)
		perp := newCVPSTestVenue(bybit.VenueName, false)
		e := testCVPSExecutor(t, dir, spot, perp)

		pos := CrossVenuePerpSpotPosition{
			Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
			Status: CVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "cvps-flat-resurrect",
			SpotLeg: CrossVenuePerpSpotLegState{Venue: binance.VenueName, Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
			PerpLeg: CrossVenuePerpSpotLegState{Venue: bybit.VenueName, Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
		}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertCVPSManageOpenFinalizeOnce(t, e, ctx)
	})
}

func assertCVPSManageOpenFinalizeOnce(t *testing.T, e *CrossVenuePerpSpotExecutor, ctx context.Context) {
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

func TestCVPSCloseRetryUsesEscalatedLimitPrice(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	spot.borrowedBase["BTC"] = 0.5
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	perp.clearOnReduceOnly = false
	e := testCVPSExecutor(t, dir, spot, perp)
	cfg := e.cfg
	cfg.CrossVenueExecutor.TakerSlippageBudgetBps = 40
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e.cfg = cfg

	pos := CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusClosing, CloseReason: "escalate", CloseAttempts: 2,
		SpotLeg: CrossVenuePerpSpotLegState{Qty: 0.5}, PerpLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
		OpenedAt: e.nowFn().Add(-time.Hour), IdemPrefix: "esc",
	}
	_ = e.closePosition(context.Background(), pos, true, "escalate")
	var lastPerpSell float64
	for _, req := range perp.placeRequests {
		if req.Market == venue.MarketPerp && req.Side == venue.SideSell && req.ReduceOnly {
			lastPerpSell = req.Price
		}
	}
	want := 99.5 * (1 - 160.0/10000)
	if math.Abs(lastPerpSell-want) > 1e-6 {
		t.Fatalf("attempt 3 perp sell limit=%v want %v", lastPerpSell, want)
	}
}

func TestCVPSManageRebalanceOnImbalance(t *testing.T) {
	dir := t.TempDir()
	spot := newCVPSTestVenue(binance.VenueName, true)
	perp := newCVPSTestVenue(bybit.VenueName, false)
	spot.borrowedBase["BTC"] = 0.6
	perp.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	e := testCVPSExecutor(t, dir, spot, perp)
	cfg := e.cfg
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e.cfg = cfg
	pos := CrossVenuePerpSpotPosition{
		Symbol: "BTC-USDT", SpotVenue: binance.VenueName, PerpVenue: bybit.VenueName,
		Status: CVPSStatusOpen, OpenedAt: e.nowFn().Add(-time.Hour), IdemPrefix: "rb",
		SpotLeg: CrossVenuePerpSpotLegState{Qty: 0.6}, PerpLeg: CrossVenuePerpSpotLegState{Qty: 0.5},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	var spotBuyQty float64
	for _, req := range spot.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy {
			spotBuyQty = req.Qty
		}
	}
	if math.Abs(spotBuyQty-0.1) > 1e-9 {
		t.Fatalf("expected spot rebalance buy 0.1, got %.6f", spotBuyQty)
	}
}
