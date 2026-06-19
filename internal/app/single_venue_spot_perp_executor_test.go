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
	"lingxi/internal/venue/okx"
)

// svspTestVenue 是同所 spot-perp 单测用 fake venue(支持 spot+perp 两条腿)。
type svspTestVenue struct {
	fakeFundingVenue
	book                 venue.BookTicker
	balanceUSDT          float64
	spotBalances         map[string]float64
	positions            []venue.Position
	placeCount           *int32
	placeRequests        []venue.OrderRequest
	spotFailNext         bool
	spotUnknownNext      bool
	perpFailNext         bool
	perpPartialFillNext  bool
	perpPartialFillRatio float64
	clearOnClose         bool
	fundingIncome        []venue.FundingIncome
	instruments          []venue.Instrument
	fundingSnaps         []venue.FundingSnapshot
}

func newSVSPTestVenue(name string) *svspTestVenue {
	pc := int32(0)
	return &svspTestVenue{
		fakeFundingVenue: fakeFundingVenue{name: name},
		book: venue.BookTicker{
			BidPrice: 99,
			AskPrice: 101,
			BidQty:   1000,
			AskQty:   1000,
		},
		balanceUSDT:  10000,
		spotBalances: make(map[string]float64),
		placeCount:   &pc,
		clearOnClose: true,
		instruments:  defaultCarryTestInstruments(),
	}
}

func (f *svspTestVenue) FetchBookTicker(_ context.Context, _ venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	return f.book, nil
}

func (f *svspTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	byAsset := map[string]venue.Balance{
		"USDT": {Asset: "USDT", Free: f.balanceUSDT},
	}
	for asset, qty := range f.spotBalances {
		byAsset[asset] = venue.Balance{Asset: asset, Free: qty}
	}
	return venue.Balances{ByAsset: byAsset}, nil
}

func (f *svspTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *svspTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	f.placeRequests = append(f.placeRequests, req)
	if req.Market == venue.MarketSpot && f.spotFailNext {
		f.spotFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated spot fail")
	}
	if req.Market == venue.MarketSpot && f.spotUnknownNext {
		f.spotUnknownNext = false
		base := baseAssetFromCanonicalSymbol(string(req.Symbol))
		if req.Side == venue.SideBuy {
			f.spotBalances[base] += req.Qty
			f.balanceUSDT -= req.Qty * req.Price
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
	base := baseAssetFromCanonicalSymbol(string(req.Symbol))
	switch req.Market {
	case venue.MarketSpot:
		switch req.Side {
		case venue.SideBuy:
			f.spotBalances[base] += req.Qty
			f.balanceUSDT -= req.Qty * req.Price
		case venue.SideSell:
			f.spotBalances[base] = math.Max(0, f.spotBalances[base]-req.Qty)
			f.balanceUSDT += req.Qty * req.Price
		}
	case venue.MarketPerp:
		switch req.Side {
		case venue.SideSell:
			if req.ReduceOnly && f.clearOnClose {
				f.positions = nil
			} else {
				f.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: -execQty, EntryPrice: req.Price, MarkPrice: req.Price, Leverage: 2}}
			}
		case venue.SideBuy:
			if req.ReduceOnly && f.clearOnClose {
				f.positions = nil
			} else {
				f.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: execQty, EntryPrice: req.Price, MarkPrice: req.Price, Leverage: 2}}
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

func (f *svspTestVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}

func (f *svspTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
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

func (f *svspTestVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return f.fundingIncome, nil
}

func (f *svspTestVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	if len(f.instruments) == 0 {
		return defaultCarryTestInstruments(), nil
	}
	return f.instruments, nil
}

func (f *svspTestVenue) FetchFundingSnapshot(context.Context) ([]venue.FundingSnapshot, error) {
	return f.fundingSnaps, nil
}

var _ venue.Venue = (*svspTestVenue)(nil)
var _ venue.PerpMarginProvider = (*svspTestVenue)(nil)

func (f *svspTestVenue) FetchPerpAvailableUSDT(context.Context) (float64, error) {
	return f.balanceUSDT, nil
}

func (f *svspTestVenue) TopUpPerpMarginUSDT(context.Context, float64) (float64, error) {
	return 0, nil
}

func testSVSPExecutorConfig() EngineConfig {
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

func testSVSPLogger() *log.Logger {
	return log.New(os.Stderr, "[svsp-test] ", log.LstdFlags)
}

func mustSVSPStore(t *testing.T, dir string) *singleVenueSpotPerpPositionStore {
	t.Helper()
	store, err := newSingleVenueSpotPerpPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testSVSPOpportunity(venueName string, qty float64) UnifiedOpportunity {
	return UnifiedOpportunity{
		ID:                "svsp-test-1",
		StrategyType:      UnifiedStrategySingleVenueSpotPerp,
		Symbol:            "BTC-USDT",
		ExpectedNetEVUSDT: 1.5,
		ExpectedHoldHours: 8,
		Legs: []UnifiedOpportunityLeg{
			{Venue: venueName, Market: "spot", Side: "buy", Qty: qty, Notional: qty * 100},
			{Venue: venueName, Market: "perp", Side: "short", Qty: qty, Notional: qty * 100},
		},
	}
}

func testSVSPOpportunityWithInstruments(venueName string, qty float64, stepSize, minNotional float64) UnifiedOpportunity {
	opp := testSVSPOpportunity(venueName, qty)
	for i := range opp.Legs {
		opp.Legs[i].StepSize = stepSize
		opp.Legs[i].MinNotional = minNotional
	}
	return opp
}

func withSVSPDefaultInstruments(v *svspTestVenue) *svspTestVenue {
	v.instruments = []venue.Instrument{
		{Symbol: "BTC-USDT", Market: venue.MarketSpot, StepSize: 0.001, MinNotional: 5, TickSize: 0.01},
		{Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5, TickSize: 0.01},
	}
	return v
}

func TestSVSPOOffModeNoPlaceOrder(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), false)
	if err != nil {
		t.Fatalf("dry-run open: %v", err)
	}
	if pos.Symbol != "" {
		t.Fatalf("dry-run must not synthesize position, got %+v", pos)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("dry-run must not PlaceOrder, got %d", *v.placeCount)
	}
	assertRealLedgerUntouched(t, dir, "singlevenuespotperp/positions.json")
}

func TestSVSPLiveOpenBothLegsCorrect(t *testing.T) {
	now := time.Now().UTC()
	v := withSVSPDefaultInstruments(newSVSPTestVenue(binance.VenueName))
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err != nil {
		t.Fatalf("live open: %v", err)
	}
	if pos.Status != SVSPStatusOpen {
		t.Fatalf("status=%s want open", pos.Status)
	}
	if n := atomic.LoadInt32(v.placeCount); n != 2 {
		t.Fatalf("expected 2 orders, got %d", n)
	}
	var spotBuy, perpSell bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideBuy {
			spotBuy = true
		}
		if req.Market == venue.MarketPerp && req.Side == venue.SideSell && !req.ReduceOnly {
			perpSell = true
		}
	}
	if !spotBuy || !perpSell {
		t.Fatalf("leg directions wrong: spotBuy=%v perpSell=%v reqs=%+v", spotBuy, perpSell, v.placeRequests)
	}
	if v.spotBalances["BTC"] <= 0 {
		t.Fatal("expected spot BTC balance after open")
	}
	if len(v.positions) == 0 || v.positions[0].PositionAmt >= 0 {
		t.Fatalf("expected short perp position, got %+v", v.positions)
	}
}

func TestSVSPPerpLegFailRollbackSpot(t *testing.T) {
	now := time.Now().UTC()
	v := withSVSPDefaultInstruments(newSVSPTestVenue(binance.VenueName))
	v.perpFailNext = true
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected perp leg failure")
	}
	if n := atomic.LoadInt32(v.placeCount); n < 3 {
		t.Fatalf("expected spot open + perp fail + spot rollback, got %d orders", n)
	}
	var rollbackSell bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			rollbackSell = true
		}
	}
	if !rollbackSell {
		t.Fatal("expected spot sell rollback")
	}
	if v.spotBalances["BTC"] > executionQtyTolerance {
		t.Fatalf("spot should be rolled back, BTC=%.6f", v.spotBalances["BTC"])
	}
}

func TestSVSPPositionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := mustSVSPStore(t, dir)
	now := time.Now().UTC()
	p := SingleVenueSpotPerpPosition{
		Symbol: "ETH-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "test-eth",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 1},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 1},
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

func TestSVSPReconcileOrphanSpotLeg(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	dir := t.TempDir()
	store := mustSVSPStore(t, dir)
	_ = store.upsert(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now.Add(-time.Hour), IdemPrefix: "orph",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	})
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.store = store
	e.nowFn = func() time.Time { return now }

	e.reconcileOnStart(context.Background(), true)
	if atomic.LoadInt32(v.placeCount) < 1 {
		t.Fatal("expected orphan flatten order")
	}
}

func TestSVSPCloseRecordsSettlement(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	v.fundingIncome = []venue.FundingIncome{{Income: 0.8}}
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now.Add(-10 * time.Hour), IdemPrefix: "close-test",
		SpotLeg:      SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
		PerpLeg:      SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
		NotionalUSDT: 50, OpenExpectedNetEV: 1.0, ExpectedHoldHours: 8,
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatal(err)
	}

	if err := e.closePosition(context.Background(), pos, true, "测试平仓"); err != nil {
		t.Fatalf("close: %v", err)
	}
	path := filepath.Join(singleVenueSpotPerpRecordDir(dir), svspSettlementsLogName)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("settlements file: %v", err)
	}
	defer f.Close()
	var rec SingleVenueSpotPerpSettlement
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
	if len(activeSingleVenueSpotPerpPositions(ps)) != 0 {
		t.Fatalf("expected position removed after close, got %d", len(ps))
	}
}

func TestSVSPExecutorLoadsPersistedPositions(t *testing.T) {
	dir := t.TempDir()
	store := mustSVSPStore(t, dir)
	now := time.Now().UTC()
	_ = store.upsert(SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "load-test",
	})
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{}, dir, testSVSPLogger())
	if len(e.snapshotPositions()) != 1 {
		t.Fatalf("expected 1 loaded position, got %d", len(e.snapshotPositions()))
	}
}

func TestSVSPOOffCloseNoPlaceOrder(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "off-close",
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5}, PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5},
	}
	if err := e.closePosition(context.Background(), pos, false, "off"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("OFF close must not PlaceOrder")
	}
}

func TestSVSPPerpPartialFillMatchesAtFilledQty(t *testing.T) {
	now := time.Now().UTC()
	v := withSVSPDefaultInstruments(newSVSPTestVenue(binance.VenueName))
	v.perpPartialFillNext = true
	v.perpPartialFillRatio = 0.4
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err != nil {
		t.Fatalf("partial perp fill should match at filled qty: %v", err)
	}
	if pos.Status != SVSPStatusOpen {
		t.Fatalf("status = %s, want open", pos.Status)
	}
	wantQty := 0.5 * 0.4
	if math.Abs(pos.PerpLeg.Qty-wantQty) > executionQtyTolerance {
		t.Fatalf("perp qty = %.6f, want ≈%.6f", pos.PerpLeg.Qty, wantQty)
	}
	if math.Abs(pos.SpotLeg.Qty-wantQty) > executionQtyTolerance {
		t.Fatalf("spot qty = %.6f, want ≈%.6f", pos.SpotLeg.Qty, wantQty)
	}
}

func TestSVSPCloseUsesLedgerCappedNotAccountTotal(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 2.0
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "cap-close",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.closePosition(context.Background(), pos, true, "cap"); err == nil {
		t.Fatal("expected close incomplete when account holds extra user spot beyond ledger")
	}
	var spotSellQty float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			spotSellQty = req.Qty
		}
	}
	if spotSellQty > 0.5+executionQtyTolerance || spotSellQty < 0.5-executionQtyTolerance {
		t.Fatalf("close must sell ledger-capped qty≈0.5, got %.6f", spotSellQty)
	}
	if v.spotBalances["BTC"] < 1.5-executionQtyTolerance {
		t.Fatalf("user-owned spot beyond ledger must remain, BTC=%.6f", v.spotBalances["BTC"])
	}
}

func TestSVSPReconcileIgnoresUntrackedPerpPerpPosition(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.004, MarkPrice: 110000, EntryPrice: 110000, Leverage: 2}}
	dir := t.TempDir()
	ppStore, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = ppStore.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "pp-test",
		HighLeg: CrossVenueLegState{Venue: binance.VenueName, Side: "sell", Qty: 0.004},
		LowLeg:  CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.004},
	})
	cfg := testSVSPExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	e.reconcileOnStart(context.Background(), true)
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("reconcile must not flatten untracked perp-perp position, got %d orders", atomic.LoadInt32(v.placeCount))
	}
}

func TestSVSPFirstLegUnknownBlocksRollback(t *testing.T) {
	now := time.Now().UTC()
	v := withSVSPDefaultInstruments(newSVSPTestVenue(binance.VenueName))
	v.spotUnknownNext = true
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected first-leg unknown to block rollback, got pos=%+v err=%v", pos, err)
	}
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp {
			t.Fatal("first-leg unknown must not attempt perp hedge")
		}
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			t.Fatal("first-leg unknown must not rollback spot")
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

func TestSVSPFirstLegExplicitFailRollback(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotFailNext = true
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected first-leg explicit failure")
	}
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp {
			t.Fatal("explicit first-leg failure must not attempt perp hedge")
		}
	}
}

type svspDeadManVenue struct {
	*svspTestVenue
	arms int
}

func (v *svspDeadManVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (v *svspDeadManVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	v.arms++
	return nil
}

func TestSVSPManageOpenArmsDeadManForPerpVenue(t *testing.T) {
	now := time.Now().UTC()
	base := newSVSPTestVenue(binance.VenueName)
	v := &svspDeadManVenue{svspTestVenue: base}
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, EntryPrice: 100, MarkPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svsp-deadman",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.persistPosition(pos); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	_, _ = e.managePosition(context.Background(), pos, true)
	if v.arms != 1 {
		t.Fatalf("expected dead-man arm once for perp venue, got %d", v.arms)
	}
}

func TestSVSPManageOpenSkipsUnsupportedDeadManVenue(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 0.5
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, EntryPrice: 100, MarkPrice: 100, Leverage: 2}}
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svsp-no-deadman",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
	}
	_, _ = e.managePosition(context.Background(), pos, true)
}

func TestSVSPCloseSkipsWhenMeasuredZero(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	// 台账有仓, 交易所无实测腿。
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, IdemPrefix: "zero-measured",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
	}
	if err := e.closePosition(context.Background(), pos, true, "zero"); err == nil {
		t.Fatal("expected close to skip when measured qty is zero")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("measured=0 must not place blind flatten orders, got %d", atomic.LoadInt32(v.placeCount))
	}
}

func TestSingleVenueSpotPerpManageOpenNoResurrectAfterFinalize(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("dust_orphan", func(t *testing.T) {
		dir := t.TempDir()
		v := newSVSPTestVenue(binance.VenueName)
		v.book = venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041, BidQty: 1000, AskQty: 1000}
		cfg := testSVSPExecutorConfig()
		cfg.CrossVenueExecutor.DustCloseThresholdUSDT = 1.0
		e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
		e.nowFn = func() time.Time { return now }

		pos := SingleVenueSpotPerpPosition{
			Symbol: "PHA-USDT", Venue: binance.VenueName,
			Status: SVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svsp-dust-resurrect",
			NextFundingTime: carryTestPastFundingTime(now),
			SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.1875, EntryPrice: 0.04},
			PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.01},
		}
		v.spotBalances["PHA"] = 0.1875
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertSVSPManageOpenFinalizeOnce(t, e, ctx)
	})

	t.Run("both_legs_flat", func(t *testing.T) {
		dir := t.TempDir()
		v := newSVSPTestVenue(binance.VenueName)
		e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
		e.nowFn = func() time.Time { return now }

		pos := SingleVenueSpotPerpPosition{
			Symbol: "BTC-USDT", Venue: binance.VenueName,
			Status: SVSPStatusOpen, OpenedAt: now, UpdatedAt: now, IdemPrefix: "svsp-flat-resurrect",
			SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.5, EntryPrice: 100},
			PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
		}
		if err := e.persistPosition(pos); err != nil {
			t.Fatalf("seed position: %v", err)
		}

		assertSVSPManageOpenFinalizeOnce(t, e, ctx)
	})
}

func TestSVSPOpenAlignsQtyToJointStep(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	opp := testSVSPOpportunityWithInstruments(binance.VenueName, 0.1234, 0.01, 5)
	_, err := e.executeOpen(context.Background(), opp, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(v.placeRequests) == 0 {
		t.Fatal("expected orders")
	}
	if v.placeRequests[0].Qty != 0.12 {
		t.Fatalf("spot qty = %.4f want 0.12", v.placeRequests[0].Qty)
	}
}

func TestSVSPOpenSkipsWhenBelowMinNotional(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	opp := testSVSPOpportunityWithInstruments(binance.VenueName, 0.0001, 0.0001, 5)
	_, err := e.executeOpen(context.Background(), opp, true)
	if err == nil {
		t.Fatal("expected skip below min notional")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("must not place when below min notional")
	}
}

func TestSVSPDustOrphanDeferredBeforeFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	v := newSVSPTestVenue(binance.VenueName)
	v.book = venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041, BidQty: 1000, AskQty: 1000}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "PHA-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now.Add(-time.Hour), UpdatedAt: now,
		NextFundingTime: nextFunding, IdemPrefix: "defer-dust",
		SpotLeg: SingleVenueSpotPerpLegState{Market: "spot", Side: "buy", Qty: 0.1875, EntryPrice: 0.04},
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.01},
	}
	v.spotBalances["PHA"] = 0.1875
	if err := e.persistPosition(pos); err != nil {
		t.Fatal(err)
	}

	e.manageOpenPositions(context.Background(), true)
	if len(e.ListActivePositions()) != 1 {
		t.Fatalf("dust orphan should defer before funding, active=%d", len(e.ListActivePositions()))
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatal("must not flatten dust before funding settlement")
	}
}

func TestSVSPNakedPerpOrphanStillClosesBeforeFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 6, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	v := newSVSPTestVenue(binance.VenueName)
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName,
		Status: SVSPStatusOpen, OpenedAt: now, UpdatedAt: now,
		NextFundingTime: nextFunding, IdemPrefix: "naked-perp",
		PerpLeg: SingleVenueSpotPerpLegState{Market: "perp", Side: "short", Qty: 0.5, EntryPrice: 100},
	}
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	if err := e.persistPosition(pos); err != nil {
		t.Fatal(err)
	}

	e.manageOpenPositions(context.Background(), true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatal("naked perp orphan must still close before funding")
	}
}

func TestSVSPShouldCloseEVWeakBlockedUntilFundingSynced(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	nextFunding := now.Add(-time.Hour)
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), nil, t.TempDir(), testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	pos := SingleVenueSpotPerpPosition{
		OpenedAt:          now.Add(-9 * time.Hour),
		OpenExpectedNetEV: 2.0,
		ExpectedHoldHours: 8,
		NextFundingTime:   nextFunding,
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

func TestSVSPHardStopBypassesFundingDefer(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	v := newSVSPTestVenue(binance.VenueName)
	v.book = venue.BookTicker{BidPrice: 100, AskPrice: 100}
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 20}}
	dir := t.TempDir()
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	v.spotBalances["BTC"] = 0.5
	pos := SingleVenueSpotPerpPosition{
		Symbol: "BTC-USDT", Venue: binance.VenueName, Status: SVSPStatusOpen,
		OpenedAt: now.Add(-time.Hour), UpdatedAt: now, IdemPrefix: "hard-stop",
		NextFundingTime: nextFunding,
		SpotLeg: SingleVenueSpotPerpLegState{Qty: 0.5, EntryPrice: 100},
		PerpLeg: SingleVenueSpotPerpLegState{Qty: 0.5, EntryPrice: 100},
	}
	_ = e.persistPosition(pos)
	e.manageOpenPositions(context.Background(), true)
	if len(e.ListActivePositions()) != 0 {
		t.Fatal("hard stop should close before funding defer window")
	}
}

func TestSVSPReconcileUntrackedPerpFlatten(t *testing.T) {
	dir := t.TempDir()
	v := newSVSPTestVenue(binance.VenueName)
	v.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100, Leverage: 2}}
	cfg := testSVSPExecutorConfig()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	cfg.CrossVenueExecutor.UntrackedExposureAction = "flatten"
	e := NewSingleVenueSpotPerpExecutor(cfg, map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.reconcileOnStart(context.Background(), true)
	var sawClose bool
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketPerp && req.ReduceOnly && req.Side == venue.SideBuy {
			sawClose = true
		}
	}
	if !sawClose {
		t.Fatalf("expected flatten of untracked perp, requests=%+v", v.placeRequests)
	}
}

func assertSVSPManageOpenFinalizeOnce(t *testing.T, e *SingleVenueSpotPerpExecutor, ctx context.Context) {
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
