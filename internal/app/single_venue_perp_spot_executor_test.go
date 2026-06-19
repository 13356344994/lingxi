package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

// svpsTestVenue 是同所 perp-spot 单测用 fake venue(支持借币空现货+多永续)。
type svpsTestVenue struct {
	fakeFundingVenue
	supportsNegativeCarry  bool
	maxBorrowable          float64
	book                   venue.BookTicker
	balanceUSDT            float64
	spotBalances           map[string]float64
	borrowedBase           map[string]float64
	positions              []venue.Position
	placeCount             *int32
	borrowRepayCount       *int32
	placeRequests          []venue.OrderRequest
	spotFailNext           bool
	spotUnknownNext        bool
	spotNotionalRejectNext bool
	spotMinNotional        float64
	perpFailNext           bool
	perpPartialFillNext    bool
	perpPartialFillRatio   float64
	clearOnClose           bool
	fundingIncome          []venue.FundingIncome
	instruments            []venue.Instrument
}

func newSVPSTestVenue(name string, supportsBorrow bool) *svpsTestVenue {
	pc := int32(0)
	br := int32(0)
	return &svpsTestVenue{
		fakeFundingVenue:      fakeFundingVenue{name: name},
		supportsNegativeCarry: supportsBorrow,
		maxBorrowable:         1000,
		book: venue.BookTicker{
			BidPrice: 99,
			AskPrice: 101,
			BidQty:   1000,
			AskQty:   1000,
		},
		balanceUSDT:      10000,
		spotBalances:     make(map[string]float64),
		borrowedBase:     make(map[string]float64),
		placeCount:       &pc,
		borrowRepayCount: &br,
		clearOnClose:     true,
		instruments:      defaultCarryTestInstruments(),
	}
}

func (f *svpsTestVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{SupportsNegativeCarry: f.supportsNegativeCarry}
}

func (f *svpsTestVenue) MaxBorrowable(_ context.Context, _ string) (float64, error) {
	if f.maxBorrowable <= 0 {
		return 1000, nil
	}
	return f.maxBorrowable, nil
}

func (f *svpsTestVenue) FetchBookTicker(_ context.Context, _ venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	return f.book, nil
}

func (f *svpsTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	byAsset := map[string]venue.Balance{
		"USDT": {Asset: "USDT", Free: f.balanceUSDT},
	}
	for asset, qty := range f.spotBalances {
		byAsset[asset] = venue.Balance{Asset: asset, Free: qty}
	}
	for asset, borrowed := range f.borrowedBase {
		if borrowed > executionQtyTolerance {
			byAsset[asset] = venue.Balance{Asset: asset, Free: -borrowed}
		}
	}
	return venue.Balances{ByAsset: byAsset}, nil
}

func (f *svpsTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *svpsTestVenue) BorrowRepay(_ context.Context, asset string, amt float64, dir venue.BorrowDir) error {
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

func (f *svpsTestVenue) FetchInstruments(_ context.Context) ([]venue.Instrument, error) {
	if f.spotMinNotional > 0 {
		return []venue.Instrument{
			{Symbol: venue.Symbol("FLUX-USDT"), Market: venue.MarketSpot, StepSize: 0.001, TickSize: 0.01, MinNotional: f.spotMinNotional},
			{Symbol: venue.Symbol("BTC-USDT"), Market: venue.MarketSpot, StepSize: 0.001, TickSize: 0.01, MinNotional: f.spotMinNotional},
			{Symbol: venue.Symbol("BTC-USDT"), Market: venue.MarketPerp, StepSize: 0.001, TickSize: 0.01, MinNotional: f.spotMinNotional},
		}, nil
	}
	if len(f.instruments) == 0 {
		return defaultCarryTestInstruments(), nil
	}
	return f.instruments, nil
}

func (f *svpsTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	f.placeRequests = append(f.placeRequests, req)
	if req.Market == venue.MarketSpot && f.spotNotionalRejectNext {
		f.spotNotionalRejectNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("HTTP 400 code=-1013 msg=Filter failure: NOTIONAL")
	}
	if req.Market == venue.MarketSpot && f.spotFailNext {
		f.spotFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated spot fail")
	}
	if req.Market == venue.MarketSpot && f.spotUnknownNext {
		f.spotUnknownNext = false
		base := baseAssetFromCanonicalSymbol(string(req.Symbol))
		if req.Side == venue.SideSell && req.MarginEffect == venue.MarginEffectBorrow {
			f.borrowedBase[base] += req.Qty
			f.balanceUSDT += req.Qty * req.Price
		}
		return venue.OrderResult{
			Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
			Status: "unknown", ExecutedQty: req.Qty, AvgPrice: req.Price,
		}, errors.New("simulated spot unknown")
	}
	if req.Market == venue.MarketPerp && f.perpFailNext {
		f.perpFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated perp fail")
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
	if req.Market == venue.MarketSpot && req.MarginEffect == venue.MarginEffectBorrow && !f.supportsNegativeCarry {
		return venue.OrderResult{Status: "rejected"}, errors.New("borrow unsupported")
	}

	base := baseAssetFromCanonicalSymbol(string(req.Symbol))
	switch req.Market {
	case venue.MarketSpot:
		switch req.Side {
		case venue.SideSell:
			if req.MarginEffect == venue.MarginEffectBorrow {
				f.borrowedBase[base] += req.Qty
				f.balanceUSDT += req.Qty * req.Price
			} else {
				f.spotBalances[base] = math.Max(0, f.spotBalances[base]-req.Qty)
				f.balanceUSDT += req.Qty * req.Price
			}
		case venue.SideBuy:
			if req.MarginEffect == venue.MarginEffectRepay {
				f.borrowedBase[base] = math.Max(0, f.borrowedBase[base]-req.Qty)
			} else {
				f.spotBalances[base] += req.Qty
			}
			f.balanceUSDT -= req.Qty * req.Price
		}
	case venue.MarketPerp:
		switch req.Side {
		case venue.SideBuy:
			if req.ReduceOnly && f.clearOnClose {
				f.positions = nil
			} else {
				f.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: execQty, EntryPrice: req.Price, MarkPrice: req.Price, Leverage: 2}}
			}
		case venue.SideSell:
			if req.ReduceOnly && f.clearOnClose {
				f.positions = nil
			} else {
				f.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: -execQty, EntryPrice: req.Price, MarkPrice: req.Price, Leverage: 2}}
			}
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

func (f *svpsTestVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}

func (f *svpsTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	for i := len(f.placeRequests) - 1; i >= 0; i-- {
		req := f.placeRequests[i]
		if req.ClientOrderID == clientOrderID {
			return venue.OrderResult{
				Symbol: req.Symbol, Market: req.Market, ClientOrderID: clientOrderID,
				Status: "filled", ExecutedQty: req.Qty, AvgPrice: req.Price,
			}, nil
		}
	}
	return venue.OrderResult{Status: "unknown", ClientOrderID: clientOrderID}, nil
}

func (f *svpsTestVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return f.fundingIncome, nil
}

var _ venue.Venue = (*svpsTestVenue)(nil)
var _ venue.PerpMarginProvider = (*svpsTestVenue)(nil)

func (f *svpsTestVenue) FetchPerpAvailableUSDT(context.Context) (float64, error) {
	return f.balanceUSDT, nil
}

func (f *svpsTestVenue) TopUpPerpMarginUSDT(context.Context, float64) (float64, error) {
	return 0, nil
}

func testSVPSExecutorConfig() EngineConfig {
	cfg := DefaultEngineConfig()
	cfg.CrossVenueExecutor = CrossVenueExecutorConfig{
		Enabled:                true,
		LiveTradingEnabled:     false,
		SlippageBudgetBps:      20,
		TakerSlippageBudgetBps: 40,
		MaxHoldHours:           72,
		MaxNakedSeconds:        30,
		MakerFirst:             false,
	}
	return cfg
}

func testSVPSLogger() *log.Logger {
	return log.New(os.Stderr, "[svps-test] ", log.LstdFlags)
}

func mustSVPSStore(t *testing.T, dir string) *singleVenuePerpSpotPositionStore {
	t.Helper()
	store, err := newSingleVenuePerpSpotPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testSVPSOpportunity(venueName string, qty float64) UnifiedOpportunity {
	return UnifiedOpportunity{
		ID:                "svps-test-1",
		StrategyType:      UnifiedStrategySingleVenuePerpSpot,
		Symbol:            "BTC-USDT",
		ExpectedNetEVUSDT: 1.5,
		ExpectedHoldHours: 8,
		Legs: []UnifiedOpportunityLeg{
			{Venue: venueName, Market: "perp", Side: "long", Qty: qty, Notional: qty * 100},
			{Venue: venueName, Market: "spot", Side: "short", Qty: qty, Notional: qty * 100},
		},
	}
}

func TestSVPSUnsupportedBorrowVenueSkipped(t *testing.T) {
	v := newSVPSTestVenue(binance.VenueName, false)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())

	_, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected unsupported borrow venue to be rejected")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("must not PlaceOrder on unsupported venue, got %d", *v.placeCount)
	}
}

func TestSVPSOffModeNoPlaceOrder(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), false)
	if err != nil {
		t.Fatalf("dry-run open: %v", err)
	}
	if pos.Symbol != "" {
		t.Fatalf("dry-run must not synthesize position, got %+v", pos)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("dry-run must not PlaceOrder, got %d", *v.placeCount)
	}
	if atomic.LoadInt32(v.borrowRepayCount) != 0 {
		t.Fatal("dry-run must not BorrowRepay")
	}
	assertRealLedgerUntouched(t, dir, "singlevenueperpspot/positions.json")
}

func TestSVPSLiveOpenBothLegsCorrect(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err != nil {
		t.Fatalf("live open: %v", err)
	}
	if pos.Status != SVPSStatusOpen {
		t.Fatalf("status=%s want open", pos.Status)
	}
	if n := atomic.LoadInt32(v.placeCount); n != 2 {
		t.Fatalf("expected 2 orders, got %d", n)
	}
	var spotBorrowSell, perpBuy bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell && req.MarginEffect == venue.MarginEffectBorrow {
			spotBorrowSell = true
		}
		if req.Market == venue.MarketPerp && req.Side == venue.SideBuy && !req.ReduceOnly {
			perpBuy = true
		}
	}
	if !spotBorrowSell || !perpBuy {
		t.Fatalf("leg directions wrong: spotBorrowSell=%v perpBuy=%v reqs=%+v", spotBorrowSell, perpBuy, v.placeRequests)
	}
	if v.borrowedBase["BTC"] <= 0 {
		t.Fatal("expected borrowed BTC after open")
	}
	if len(v.positions) == 0 || v.positions[0].PositionAmt <= 0 {
		t.Fatalf("expected long perp position, got %+v", v.positions)
	}
}

func TestSVPSPerpLegFailRollbackSpot(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.perpFailNext = true
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected perp leg failure")
	}
	if n := atomic.LoadInt32(v.placeCount); n < 3 {
		t.Fatalf("expected spot borrow + perp fail + spot repay rollback, got %d orders", n)
	}
	var rollbackRepay bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			rollbackRepay = true
		}
	}
	if !rollbackRepay {
		t.Fatal("expected spot buy+repay rollback")
	}
	if v.borrowedBase["BTC"] > executionQtyTolerance {
		t.Fatalf("borrow should be rolled back, BTC borrowed=%.6f", v.borrowedBase["BTC"])
	}
}

func TestSVPSPositionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := mustSVPSStore(t, dir)
	now := time.Now().UTC()
	p := SingleVenuePerpSpotPosition{
		Symbol: "ETH-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "test-eth",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 1},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 1},
	}
	if err := store.upsert(p); err != nil {
		t.Fatal(err)
	}
	ps, err := store.load()
	if err != nil || len(ps) != 1 || ps[0].Symbol != "ETH-USDT" {
		t.Fatalf("load=%v err=%v", ps, err)
	}
	if ps[0].PositionKey == "" {
		t.Fatal("expected position key")
	}
}

func TestSVPSCloseRecordsSettlement(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.borrowedBase["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	v.fundingIncome = []venue.FundingIncome{{Income: 0.8}}
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now.Add(-10 * time.Hour), IdemPrefix: "close-test",
		SpotLeg:      SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg:      SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
		NotionalUSDT: 50, OpenExpectedNetEV: 1.0, ExpectedHoldHours: 8,
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatal(err)
	}

	if err := e.closePosition(context.Background(), pos, true, "测试平仓"); err != nil {
		t.Fatalf("close: %v", err)
	}
	var sawRepay bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			sawRepay = true
		}
	}
	if !sawRepay {
		t.Fatal("close should buy spot with MarginEffectRepay")
	}
	path := filepath.Join(singleVenuePerpSpotRecordDir(dir), svpsSettlementsLogName)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("settlements file: %v", err)
	}
	defer f.Close()
	var rec SingleVenuePerpSpotSettlement
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Symbol == "BTC-USDT" {
			break
		}
	}
	if rec.Symbol != "BTC-USDT" {
		t.Fatal("settlement record not found")
	}
	if rec.RealizedFunding != 0.8 {
		t.Fatalf("funding=%.4f want 0.8", rec.RealizedFunding)
	}
	ps, _ := e.store.load()
	if len(activeSingleVenuePerpSpotPositions(ps)) != 0 {
		t.Fatalf("expected position removed after close, got %d", len(ps))
	}
}

func TestSVPSExecutorLoadsPersistedPositions(t *testing.T) {
	dir := t.TempDir()
	store := mustSVPSStore(t, dir)
	now := time.Now().UTC()
	_ = store.upsert(SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "load-test",
	})
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{}, dir, testSVPSLogger())
	if len(e.snapshotPositions()) != 1 {
		t.Fatalf("expected 1 loaded position, got %d", len(e.snapshotPositions()))
	}
}

func TestSVPSOffCloseNoPlaceOrder(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }
	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "off-close",
		SpotLeg: SingleVenuePerpSpotLegState{Qty: 0.5}, PerpLeg: SingleVenuePerpSpotLegState{Qty: 0.5},
	}
	if err := e.closePosition(context.Background(), pos, false, "off"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("OFF close must not PlaceOrder")
	}
}

func TestSVPSPerpPartialFillMatchesAtFilledQty(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.perpPartialFillNext = true
	v.perpPartialFillRatio = 0.4
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err != nil {
		t.Fatalf("partial perp fill should match at filled qty: %v", err)
	}
	if pos.Status != SVPSStatusOpen {
		t.Fatalf("status = %s, want open", pos.Status)
	}
	wantQty := 0.5 * 0.4
	if math.Abs(pos.PerpLeg.Qty-wantQty) > executionQtyTolerance {
		t.Fatalf("perp qty = %.6f, want ≈%.6f", pos.PerpLeg.Qty, wantQty)
	}
	if math.Abs(pos.SpotLeg.Qty-wantQty) > executionQtyTolerance {
		t.Fatalf("spot qty = %.6f, want ≈%.6f", pos.SpotLeg.Qty, wantQty)
	}
	if math.Abs(v.borrowedBase["BTC"]-wantQty) > executionQtyTolerance {
		t.Fatalf("borrowed = %.6f, want ≈%.6f", v.borrowedBase["BTC"], wantQty)
	}
}

func TestSVPSBorrowLimitExceededSkipsOpen(t *testing.T) {
	v := newSVPSTestVenue(binance.VenueName, true)
	v.maxBorrowable = 0.1
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())

	_, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err == nil || !strings.Contains(err.Error(), "借币额度不足") {
		t.Fatalf("expected borrow limit rejection, got %v", err)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("borrow limit exceeded must not place orders")
	}
}

func TestSVPSCloseUsesLedgerCappedNotAccountTotal(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.spotBalances["BTC"] = 2.0
	v.borrowedBase["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "cap-close",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.closePosition(context.Background(), pos, true, "cap"); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			if req.Qty > 0.5+executionQtyTolerance {
				t.Fatalf("close must not sell user-owned spot beyond ledger, qty=%.6f", req.Qty)
			}
		}
	}
}

func TestSVPSReconcileIgnoresUntrackedPerpPerpPosition(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.004, MarkPrice: 110000, EntryPrice: 110000, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	e.reconcileOnStart(context.Background(), true)
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("reconcile must not flatten untracked perp-perp position, got %d orders", atomic.LoadInt32(v.placeCount))
	}
}

func TestSVPSFirstLegUnknownBlocksRollback(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.spotUnknownNext = true
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected first-leg unknown to block rollback, got %v", err)
	}
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp {
			t.Fatal("first-leg unknown must not attempt perp hedge")
		}
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy && req.MarginEffect == venue.MarginEffectRepay {
			t.Fatal("first-leg unknown must not trigger rollback repay")
		}
	}
	loaded := e.ListActivePositions()
	if len(loaded) != 1 {
		t.Fatalf("expected position retained for reconcile, got %d", len(loaded))
	}
	if loaded[0].SpotLeg.Qty <= executionQtyTolerance {
		t.Fatalf("expected partial first leg qty recorded, got %.6f", loaded[0].SpotLeg.Qty)
	}
}

func TestSVPSFirstLegExplicitFailNoSecondLeg(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.spotFailNext = true
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVPSOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected first-leg explicit failure")
	}
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp {
			t.Fatal("explicit first-leg failure must not attempt perp hedge")
		}
	}
}

type svpsDeadManVenue struct {
	*svpsTestVenue
	arms int
}

func (v *svpsDeadManVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{SupportsNegativeCarry: true, HasNativeDeadMan: true}
}

func (v *svpsDeadManVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	v.arms++
	return nil
}

func TestSVPSManageOpenArmsDeadManForPerpVenue(t *testing.T) {
	now := time.Now().UTC()
	base := newSVPSTestVenue(binance.VenueName, true)
	v := &svpsDeadManVenue{svpsTestVenue: base}
	v.borrowedBase["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, EntryPrice: 100, MarkPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-deadman",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	if v.arms != 1 {
		t.Fatalf("expected dead-man arm once for perp venue, got %d", v.arms)
	}
}

func TestSVPSManageOpenSkipsUnsupportedDeadManVenue(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.borrowedBase["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, EntryPrice: 100, MarkPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-no-deadman",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
}

func TestSVPSCloseSkipsWhenMeasuredZero(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, IdemPrefix: "zero-measured",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.closePosition(context.Background(), pos, true, "zero"); err == nil {
		t.Fatal("expected close to skip when measured qty is zero")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("measured=0 must not place blind flatten orders, got %d", atomic.LoadInt32(v.placeCount))
	}
}

func TestSingleVenuePerpSpotManageOpenNoResurrectAfterFinalize(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("dust_orphan", func(t *testing.T) {
		dir := t.TempDir()
		v := newSVPSTestVenue(binance.VenueName, true)
		v.book = venue.BookTicker{BidPrice: 99, AskPrice: 101, BidQty: 1000, AskQty: 1000}
		cfg := testSVPSExecutorConfig()
		cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
		e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
		e.nowFn = func() time.Time { return now }

		pos := SingleVenuePerpSpotPosition{
			Symbol: "BTC-USDT", Venue: binance.VenueName,
			Status: SVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-dust-resurrect",
			NextFundingTime: carryTestPastFundingTime(now),
			SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
			PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.00008, EntryPrice: 100},
		}
		v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.00008, EntryPrice: 100}}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertSVPSManageOpenFinalizeOnce(t, e, ctx)
	})

	t.Run("both_legs_flat", func(t *testing.T) {
		dir := t.TempDir()
		v := newSVPSTestVenue(binance.VenueName, true)
		e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
		e.nowFn = func() time.Time { return now }

		pos := SingleVenuePerpSpotPosition{
			Symbol: "BTC-USDT", Venue: binance.VenueName,
			Status: SVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-flat-resurrect",
			SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
			PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
		}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertSVPSManageOpenFinalizeOnce(t, e, ctx)
	})
}

func assertSVPSManageOpenFinalizeOnce(t *testing.T, e *SingleVenuePerpSpotExecutor, ctx context.Context) {
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

func TestMarginShortQtyFromBalanceReadsNegativeNet(t *testing.T) {
	bal := venue.Balances{ByAsset: map[string]venue.Balance{
		"BTC": {Asset: "BTC", Free: -0.75},
	}}
	if got := marginShortQtyFromBalance(bal, "BTC"); got != 0.75 {
		t.Fatalf("margin short=%.4f want 0.75", got)
	}
}

func TestSVPSSpotShortDustOrphanFinalizesWithRepay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.book = venue.BookTicker{BidPrice: 0.047, AskPrice: 0.049}
	v.borrowedBase["FLUX"] = 0.10459
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "FLUX-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-flux-dust",
		NextFundingTime: carryTestPastFundingTime(now),
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.10459, EntryPrice: 0.048},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 0.048},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}

	e.manageOpenPositions(ctx, true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatalf("dust orphan must finalize position, got %d active", len(e.ListActivePositions()))
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("sub-minNotional dust must not place close orders, got %d", atomic.LoadInt32(v.placeCount))
	}
	if atomic.LoadInt32(v.borrowRepayCount) != 1 {
		t.Fatalf("dust spot short should best-effort repay once, got %d", atomic.LoadInt32(v.borrowRepayCount))
	}
	settlements, err := e.ListRecentSettlements(0)
	if err != nil || len(settlements) != 1 {
		t.Fatalf("expected 1 settlement, got %v err=%v", len(settlements), err)
	}
	if !strings.Contains(settlements[0].CloseReason, "dust_residual") {
		t.Fatalf("close reason = %q, want dust_residual", settlements[0].CloseReason)
	}

	e.manageOpenPositions(ctx, true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatal("second manage must not resurrect dust-closed position")
	}
	settlements2, err := e.ListRecentSettlements(0)
	if err != nil || len(settlements2) != 1 {
		t.Fatalf("duplicate settlement churn, got %d", len(settlements2))
	}
}

func TestSVPSCloseSpotShortNotionalRejectDustFinalizes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.book = venue.BookTicker{BidPrice: 0.047, AskPrice: 0.049}
	v.borrowedBase["FLUX"] = 0.10459
	v.spotNotionalRejectNext = true
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "FLUX-USDT", Venue: binance.VenueName,
		Status: SVPSStatusClosing, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-flux-notional",
		NextFundingTime: carryTestPastFundingTime(now),
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.10459, EntryPrice: 0.048},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 0.048},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}

	if err := e.closePosition(ctx, pos, true, "test_close"); err != nil {
		t.Fatalf("closePosition dust finalize should succeed, got %v", err)
	}
	if len(e.ListActivePositions()) != 0 {
		t.Fatal("closing dust must remove active position")
	}
	if atomic.LoadInt32(v.borrowRepayCount) != 1 {
		t.Fatalf("expected repay after NOTIONAL reject, got %d", atomic.LoadInt32(v.borrowRepayCount))
	}
}

func TestSVPSSpotShortAboveMinNotionalStillRetriesClose(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.book = venue.BookTicker{BidPrice: 99, AskPrice: 101}
	v.borrowedBase["BTC"] = 0.5
	v.spotFailNext = true
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusClosing, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svps-btc-naked",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenuePerpSpotLegState{Market: "perp", Side: "long", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}

	if err := e.closePosition(ctx, pos, true, "test_close"); err == nil {
		t.Fatal("above-minNotional spot short must not dust-finalize")
	}
	if len(e.ListActivePositions()) != 1 {
		t.Fatalf("position should remain for retry, got %d active", len(e.ListActivePositions()))
	}
	if atomic.LoadInt32(v.placeCount) != 1 {
		t.Fatalf("expected one close attempt, got %d", atomic.LoadInt32(v.placeCount))
	}
	if atomic.LoadInt32(v.borrowRepayCount) != 0 {
		t.Fatal("above-minNotional must not direct-repay without close fill")
	}
}

func TestSVPSCloseRetryUsesEscalatedLimitPrice(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.borrowedBase["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	v.clearOnClose = false
	dir := t.TempDir()
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.TakerSlippageBudgetBps = 40
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusClosing, CloseReason: "escalate", CloseAttempts: 2,
		SpotLeg: SingleVenuePerpSpotLegState{Qty: 0.5}, PerpLeg: SingleVenuePerpSpotLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "esc",
	}
	_ = e.closePosition(context.Background(), pos, true, "escalate")
	var lastPerpSell float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp && req.Side == venue.SideSell && req.ReduceOnly {
			lastPerpSell = req.Price
		}
	}
	want := 99 * (1 - 160.0/10000)
	if math.Abs(lastPerpSell-want) > 1e-6 {
		t.Fatalf("attempt 3 perp sell limit=%v want %v", lastPerpSell, want)
	}
}

func TestSVPSManageRebalanceOnImbalance(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	v.borrowedBase["BTC"] = 0.6
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	cfg := testSVPSExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenuePerpSpotExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }
	pos := SingleVenuePerpSpotPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now.Add(-time.Hour), IdemPrefix: "rb",
		SpotLeg: SingleVenuePerpSpotLegState{Qty: 0.6}, PerpLeg: SingleVenuePerpSpotLegState{Qty: 0.5},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	var spotBuyQty float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy {
			spotBuyQty = req.Qty
		}
	}
	if math.Abs(spotBuyQty-0.1) > 1e-9 {
		t.Fatalf("expected spot rebalance buy 0.1, got %.6f", spotBuyQty)
	}
}

func testSVPSOpportunityWithInstruments(venueName string, qty, stepSize, minNotional float64) UnifiedOpportunity {
	opp := testSVPSOpportunity(venueName, qty)
	for i := range opp.Legs {
		opp.Legs[i].StepSize = stepSize
		opp.Legs[i].MinNotional = minNotional
	}
	return opp
}

func TestSVPSOpenAlignsQtyToJointStep(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	opp := testSVPSOpportunityWithInstruments(binance.VenueName, 0.1234, 0.01, 5)
	_, err := e.executeOpen(context.Background(), opp, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(v.placeRequests) == 0 || v.placeRequests[0].Qty != 0.12 {
		t.Fatalf("spot borrow qty = %.4f want 0.12", v.placeRequests[0].Qty)
	}
}

func TestSVPSOpenSkipsWhenBelowMinNotional(t *testing.T) {
	now := time.Now().UTC()
	v := newSVPSTestVenue(binance.VenueName, true)
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	opp := testSVPSOpportunityWithInstruments(binance.VenueName, 0.0001, 0.0001, 5)
	_, err := e.executeOpen(context.Background(), opp, true)
	if err == nil {
		t.Fatal("expected skip below min notional")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("must not place when below min notional")
	}
}

func TestSVPSDustOrphanDeferredBeforeFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	v := newSVPSTestVenue(binance.VenueName, true)
	v.book = venue.BookTicker{BidPrice: 0.047, AskPrice: 0.049, BidQty: 1000, AskQty: 1000}
	v.spotMinNotional = 1
	dir := t.TempDir()
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		Symbol: "FLUX-USDT", Venue: binance.VenueName,
		Status: SVPSStatusOpen, OpenedAt: now.Add(-time.Hour), UpdatedAt: now,
		NextFundingTime: nextFunding, IdemPrefix: "defer-dust",
		SpotLeg: SingleVenuePerpSpotLegState{Market: "spot", Side: "short", Qty: 0.10459, EntryPrice: 0.05},
	}
	v.borrowedBase["FLUX"] = 0.10459
	if err := e.persistPosition(pos); err != nil {
		t.Fatal(err)
	}

	e.manageOpenPositions(context.Background(), true)
	if len(e.ListActivePositions()) != 1 {
		t.Fatalf("dust orphan should defer before funding, active=%d", len(e.ListActivePositions()))
	}
	if atomic.LoadInt32(v.borrowRepayCount) != 0 {
		t.Fatal("must not repay dust before funding settlement")
	}
}

func TestSVPSShouldCloseEVWeakBlockedUntilFundingSynced(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	nextFunding := now.Add(-time.Hour)
	e := NewSingleVenuePerpSpotExecutor(testSVPSExecutorConfig(), nil, t.TempDir(), testSVPSLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenuePerpSpotPosition{
		OpenedAt:            now.Add(-9 * time.Hour),
		OpenExpectedNetEV:   2.0,
		ExpectedHoldHours:   8,
		NextFundingTime:     nextFunding,
		RealizedFundingUSDT: 0,
	}
	if reason := e.shouldClosePosition(context.Background(), &pos); reason != "" {
		t.Fatalf("should defer EV weak until post-funding sync, got %q", reason)
	}
	pos.LastFundingSyncAt = now
	if reason := e.shouldClosePosition(context.Background(), &pos); reason == "" {
		t.Fatal("expected EV weak close after funding sync")
	}
}
