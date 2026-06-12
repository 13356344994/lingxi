package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// crossVenueTestVenue 是可配置的 fake venue.Venue,用于跨所执行器单测。
type crossVenueTestVenue struct {
	fakeFundingVenue
	instruments   []venue.Instrument
	book          venue.BookTicker
	depth         venue.Depth
	balanceUSDT   float64
	positions     []venue.Position
	fundingSnaps  []venue.FundingSnapshot
	contractBased bool
	placeCount    *int32
	placeFailNext bool
	// clearOnReduceOnly=true 时 reduce-only 单成交后清空持仓(模拟平仓真实成交);
	// false 时持仓保持不变(模拟 IOC 没吃干净——平仓核验必须能发现)。
	clearOnReduceOnly bool
}

func newCrossVenueTestVenue(name string, rate float64, interval int, nextFunding time.Time) *crossVenueTestVenue {
	pc := int32(0)
	return &crossVenueTestVenue{
		fakeFundingVenue: fakeFundingVenue{name: name},
		book: venue.BookTicker{
			BidPrice: 99,
			AskPrice: 101,
			BidQty:   1000,
			AskQty:   1000,
		},
		depth: venue.Depth{
			Bids: []venue.DepthLevel{{Price: 99, Qty: 100}, {Price: 98.5, Qty: 100}, {Price: 98, Qty: 100}},
			Asks: []venue.DepthLevel{{Price: 101, Qty: 100}, {Price: 101.5, Qty: 100}, {Price: 102, Qty: 100}},
		},
		balanceUSDT: 10000,
		fundingSnaps: []venue.FundingSnapshot{{
			Symbol:          "BTC-USDT",
			LastFundingRate: rate,
			IntervalHours:   interval,
			NextFundingTime: nextFunding,
		}},
		instruments: []venue.Instrument{{
			Symbol:       "BTC-USDT",
			Market:       venue.MarketPerp,
			StepSize:     0.001,
			MinNotional:  5,
			ContractSize: 0.01,
			QtyUnit:      "coin",
		}},
		placeCount: &pc,
	}
}

func (f *crossVenueTestVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{ContractBasedPerp: f.contractBased, HasNativeDeadMan: f.name == okx.VenueName || f.name == gate.VenueName}
}

func (f *crossVenueTestVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return f.instruments, nil
}

func (f *crossVenueTestVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return f.book, nil
}

func (f *crossVenueTestVenue) FetchDepth(context.Context, venue.Market, venue.Symbol, int) (venue.Depth, error) {
	return f.depth, nil
}

func (f *crossVenueTestVenue) FetchFundingSnapshot(context.Context) ([]venue.FundingSnapshot, error) {
	return f.fundingSnaps, nil
}

func (f *crossVenueTestVenue) FetchBalances(context.Context) (venue.Balances, error) {
	return venue.Balances{ByAsset: map[string]venue.Balance{"USDT": {Asset: "USDT", Free: f.balanceUSDT}}}, nil
}

func (f *crossVenueTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *crossVenueTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	if f.placeFailNext {
		f.placeFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated fail")
	}
	if req.ReduceOnly && f.clearOnReduceOnly {
		f.positions = nil
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

func (f *crossVenueTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	return venue.OrderResult{Status: "filled", ExecutedQty: 0.5, ClientOrderID: clientOrderID}, nil
}

func (f *crossVenueTestVenue) ArmDeadMansSwitch(context.Context, time.Duration) error { return nil }

var _ venue.Venue = (*crossVenueTestVenue)(nil)

func testCrossVenueExecutorConfig() EngineConfig {
	cfg := DefaultEngineConfig()
	cfg.CrossVenueExecutor = CrossVenueExecutorConfig{
		Enabled:                      true,
		LiveTradingEnabled:           false,
		LoopIntervalSeconds:          60,
		OpenSpreadAPR:                1.5,
		CloseSpreadAPR:               0.3,
		CloseSpreadConsecutiveRounds: 3,
		MaxConcurrent:                2,
		MaxPositionUSDT:              50,
		MaxBookSpreadBps:             30,
		SlippageBudgetBps:            20,
		MaxNakedSeconds:              30,
		MinLiqDistance:               0.15,
		MaxHoldHours:                 72,
		MaxEffectiveLeverage:         2,
		MarginSafetyFactor:           1.5,
		MidPriceDeviationMax:         0.03,
		DepthUsageRatio:              0.20,
		SymbolCooldownHours:          1,
	}
	cfg.CrossVenueScanner.Enabled = true
	cfg.AllocatedCapitalUSDT = 1000
	return cfg
}

// testScannerWithFunding 构造一个只带费率缓存的扫描器(执行引擎管理循环从这里查,不触网)。
func testScannerWithFunding(byVenue map[string]map[string]CrossVenueFundingEntry) *CrossVenueScanner {
	return &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{FundingByVenue: byVenue}}
}

func testCrossVenueGateDeps(allocated float64) crossVenueGateDeps {
	return crossVenueGateDeps{
		lookupInstrument: lookupVenueInstrument,
		fetchBookTicker: func(ctx context.Context, v venue.Venue, sym venue.Symbol) (venue.BookTicker, error) {
			return v.FetchBookTicker(ctx, venue.MarketPerp, sym)
		},
		fetchDepth: func(ctx context.Context, v venue.Venue, sym venue.Symbol, limit int) (venue.Depth, error) {
			return v.FetchDepth(ctx, venue.MarketPerp, sym, limit)
		},
		fetchFunding:     fetchVenueFundingForSymbol,
		fetchBalanceUSDT: fetchVenueUSDTBalance,
		venueCaps:        func(v venue.Venue) venue.Capabilities { return v.Capabilities() },
		allocatedUSDT:    func(string) float64 { return allocated },
	}
}

func TestCrossVenueOpenGateGhostLeg(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, time.Time{})
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now: now,
	})
	if res.OK {
		t.Fatal("expected ghost leg rejection")
	}
	if !strings.Contains(res.Reason, "幽灵腿") {
		t.Fatalf("reason=%q", res.Reason)
	}
}

func TestCrossVenueOpenGateBybitConflict(t *testing.T) {
	dir := t.TempDir()
	bybitDir := filepath.Join(dir, "bybit")
	store, _ := newBybitPositionStore(bybitDir)
	_ = store.upsert(BybitLivePosition{Symbol: "BTC-USDT", BaseQty: 1})
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp:       CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now:       now,
		BybitHeld: func(sym venue.Symbol) bool { return isBybitSymbolHeld(dir, sym) },
	})
	if res.OK {
		t.Fatal("expected bybit conflict rejection")
	}
}

func TestCrossVenueOpenGateLiquiditySpread(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	high.book = venue.BookTicker{BidPrice: 90, AskPrice: 110, BidQty: 100, AskQty: 100}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now: now,
	})
	if res.OK {
		t.Fatal("expected spread or deviation rejection")
	}
}

func TestAlignCrossVenueQtyContractBased(t *testing.T) {
	highInstr := venue.Instrument{StepSize: 0.001, ContractSize: 0.01}
	lowInstr := venue.Instrument{StepSize: 0.001, ContractSize: 0.01}
	qty := alignCrossVenueQty(50, 100, highInstr, lowInstr, venue.Capabilities{ContractBasedPerp: true}, venue.Capabilities{ContractBasedPerp: true})
	if qty <= 0 {
		t.Fatalf("qty=%v", qty)
	}
	if rem := qty / 0.01; rem != float64(int(rem)) {
		t.Fatalf("qty not aligned to contracts: %v", qty)
	}
}

func TestCrossVenueShadowModeNoPlaceOrder(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100},
		Qty:        0.5,
		IdemPrefix: "test",
	}
	_, err := e.executeCrossVenueOpen(context.Background(), plan, false)
	if err != nil {
		t.Fatalf("shadow open: %v", err)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("shadow mode must not PlaceOrder: high=%d low=%d", *high.placeCount, *low.placeCount)
	}
}

func TestCrossVenueSecondLegFailRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.placeFailNext = true
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         testCrossVenueLogger(),
		nowFn:          func() time.Time { return now },
		symbolCooldown: map[string]time.Time{},
	}
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100},
		Qty:        0.5,
		IdemPrefix: "test-rb",
	}
	_, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err == nil {
		t.Fatal("expected second leg failure")
	}
	if n := atomic.LoadInt32(high.placeCount); n < 2 {
		t.Fatalf("expected rollback place on high venue, got %d", n)
	}
	if !e.isSymbolCooling("BTC-USDT") {
		t.Fatal("expected symbol cooldown after rollback")
	}
}

func TestCrossVenueOrphanLegDetection(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "orph",
	}
	_, closed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !closed {
		t.Fatal("expected orphan handling to complete")
	}
	if atomic.LoadInt32(high.placeCount) < 1 {
		t.Fatal("expected close order on orphan leg")
	}
}

func TestCrossVenueLiqDistanceReduceRisk(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 10}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 10}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "liq",
	}
	e.manageCrossVenuePosition(context.Background(), pos, true)
	if atomic.LoadInt32(high.placeCount) == 0 || atomic.LoadInt32(low.placeCount) == 0 {
		t.Fatal("expected sync reduce on both legs")
	}
}

func TestCrossVenueCloseSpreadConsecutive(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.00001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.00001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 3
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
		// 价差从扫描器缓存读:两所费率几乎相等 → 年化价差≈0 < 平仓阈值。
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.00001, IntervalHours: 8, NextFundingTime: now}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.00001, IntervalHours: 8, NextFundingTime: now}},
		}),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "close",
	}
	for i := 0; i < 3; i++ {
		p, _ := e.manageCrossVenuePosition(context.Background(), pos, true)
		pos = p
	}
	if atomic.LoadInt32(high.placeCount) == 0 {
		t.Fatal("expected close after 3 consecutive low spread rounds")
	}
}

func TestCrossVenuePositionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := CrossVenuePosition{Symbol: "ETH-USDT", Status: CrossVenueStatusOpen, OpenedAt: time.Now().UTC()}
	if err := store.upsert(p); err != nil {
		t.Fatal(err)
	}
	ps, err := store.load()
	if err != nil || len(ps) != 1 || ps[0].Symbol != "ETH-USDT" {
		t.Fatalf("load=%v err=%v", ps, err)
	}
}

func TestCrossVenueReconcileOnStart(t *testing.T) {
	dir := t.TempDir()
	store, _ := newCrossVenuePositionStore(dir)
	_ = store.upsert(CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
	})
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	e.reconcileOnStart(context.Background())
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("expected orphan cleared, got %d positions", len(ps))
	}
}

func TestCrossVenueHaltStopsOpenOnly(t *testing.T) {
	dir := t.TempDir()
	haltPath := filepath.Join(dir, ".halt-live")
	if err := os.WriteFile(haltPath, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.NetworkConfig.EmergencyStopFile = ".halt-live"
	e := &CrossVenueExecutor{
		cfg:       cfg,
		haltPaths: emergencyStopPaths(cfg),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return time.Now().UTC() },
	}
	if !e.isHaltActive() {
		t.Fatal("halt should be active")
	}
	e.manageOpenPositions(context.Background(), false)
}

func TestHandleCrossVenuePositionsAPI(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	exec := &CrossVenueExecutor{cfg: cfg, store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir), nowFn: time.Now}
	dashboard := &DashboardServer{crossVenueExecutor: exec}
	req := httptest.NewRequest(http.MethodGet, "/api/cross-venue/positions", nil)
	rec := httptest.NewRecorder()
	dashboard.handleCrossVenuePositionsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCrossVenueOpenGateLeverage(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = 500
	cfg.CrossVenueExecutor.MaxEffectiveLeverage = 2
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(100), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now: now,
	})
	if res.OK {
		t.Fatal("expected leverage rejection")
	}
}

func TestCrossVenueOpenGateHalt(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}, crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now: now, HaltActive: true,
	})
	if res.OK {
		t.Fatal("expected halt rejection")
	}
}

// TestCrossVenueFundingAccrualRealElapsed 钉死记账口径:60s 间隔两轮累计 ≈ 2/60 小时的量,
// 而不是「每轮=1小时」虚高 60 倍。
func TestCrossVenueFundingAccrualRealElapsed(t *testing.T) {
	t0 := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	current := t0
	e := &CrossVenueExecutor{
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return current },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.06, IntervalHours: 1}}, // 0.06/h
			okx.VenueName:   {"BTC-USDT": {Rate: 0, IntervalHours: 1}},
		}),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		NotionalUSDT: 100, OpenedAt: t0,
	}
	total := 0.0
	for i := 0; i < 2; i++ {
		current = current.Add(60 * time.Second)
		total += e.estimateFundingAccrual(&pos)
	}
	// 0.06/h × 100U × 2 分钟 = 0.2;按「每轮=1h」的旧 bug 会算出 12。
	want := 0.06 * 100 * (2.0 / 60.0)
	if total < want*0.99 || total > want*1.01 {
		t.Fatalf("accrual=%v want≈%v(旧 bug 会是 12)", total, want)
	}
	if pos.LastAccrualAt != current {
		t.Fatalf("LastAccrualAt 未推进: %v", pos.LastAccrualAt)
	}
}

// TestCrossVenueFundingAccrualMissingCacheNoAdvance 缓存缺失时返回 0 且不推进 LastAccrualAt(窗口下轮补上)。
func TestCrossVenueFundingAccrualMissingCacheNoAdvance(t *testing.T) {
	t0 := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	e := &CrossVenueExecutor{
		logger:  testCrossVenueLogger(),
		nowFn:   func() time.Time { return t0.Add(time.Minute) },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{}),
	}
	pos := CrossVenuePosition{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, NotionalUSDT: 100, OpenedAt: t0}
	if got := e.estimateFundingAccrual(&pos); got != 0 {
		t.Fatalf("accrual=%v want 0", got)
	}
	if !pos.LastAccrualAt.IsZero() {
		t.Fatal("缓存缺失不应推进 LastAccrualAt")
	}
}

// TestCrossVenueCloseRetryKeepsLedger 平仓 IOC 没吃干净(所内仍有持仓)→ 保持 closing、不删台账、
// 跨轮重试,连续 3 轮平不掉后 CloseAttempts≥3(critical 告警路径)。
func TestCrossVenueCloseRetryKeepsLedger(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	// clearOnReduceOnly=false:平仓单「成交」但所内持仓不消失 → FetchPositions 核验必须发现没平干净。
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "retry",
	}
	for round := 1; round <= 3; round++ {
		if err := e.closeCrossVenuePosition(context.Background(), pos, true, "测试平仓"); err == nil {
			t.Fatalf("round %d: 平仓未完成应返回错误", round)
		}
		ps, lerr := store.load()
		if lerr != nil || len(ps) != 1 {
			t.Fatalf("round %d: 台账必须保留, ps=%v err=%v", round, ps, lerr)
		}
		if ps[0].Status != CrossVenueStatusClosing {
			t.Fatalf("round %d: status=%s want closing", round, ps[0].Status)
		}
		if ps[0].CloseAttempts != round {
			t.Fatalf("round %d: CloseAttempts=%d", round, ps[0].CloseAttempts)
		}
		pos = ps[0]
	}
}

// TestCrossVenueCloseVerifiedRemovesLedger 两腿真平干净(FetchPositions 核验为零)才删台账。
func TestCrossVenueCloseVerifiedRemovesLedger(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5}}
	high.clearOnReduceOnly = true
	low.clearOnReduceOnly = true
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "done",
	}
	if err := e.closeCrossVenuePosition(context.Background(), pos, true, "测试平仓"); err != nil {
		t.Fatalf("平仓应完成: %v", err)
	}
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("平干净后台账应删除, got %d", len(ps))
	}
}

// TestCrossVenueManageContinuesClosing closing 状态的持仓进管理循环走「继续平仓」分支(不走孤腿/同步性误判)。
func TestCrossVenueManageContinuesClosing(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	// 半平状态:高费腿已平、低费腿还剩——若走常规流程会被孤腿分支误判。
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5}}
	low.clearOnReduceOnly = true
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "上轮未平完",
		HighLeg: CrossVenueLegState{Qty: 0}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "cont",
	}
	_ = store.upsert(pos)
	_, done := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !done {
		t.Fatal("续平应完成")
	}
	if atomic.LoadInt32(low.placeCount) == 0 {
		t.Fatal("续平应对剩余腿下平仓单")
	}
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("续平完成后台账应删除, got %d", len(ps))
	}
}

// TestCrossVenueShadowCloseRemovesLedgerNoOrder 影子模式平仓行为不变:直接删台账、绝不下单。
func TestCrossVenueShadowCloseRemovesLedgerNoOrder(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "shadow",
	}
	_ = store.upsert(pos)
	if err := e.closeCrossVenuePosition(context.Background(), pos, false, "影子平仓"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("影子模式不得调 PlaceOrder")
	}
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("影子平仓后台账应删除, got %d", len(ps))
	}
}

// TestBuildCrossVenueFundingIndex 扫描器侧 per-venue per-symbol 费率索引构建正确。
func TestBuildCrossVenueFundingIndex(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	byVenue := map[string][]venue.FundingSnapshot{
		bybit.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 1, NextFundingTime: nf},
			{Symbol: "", LastFundingRate: 0.5}, // 空 symbol 跳过
		},
		okx.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 0, NextFundingTime: nf}, // interval 0 → 8
		},
	}
	idx := buildCrossVenueFundingIndex(byVenue)
	if e := idx[bybit.VenueName]["BTC-USDT"]; e.Rate != 0.001 || e.IntervalHours != 1 || !e.NextFundingTime.Equal(nf) {
		t.Fatalf("bybit entry=%+v", e)
	}
	if e := idx[okx.VenueName]["BTC-USDT"]; e.IntervalHours != 8 {
		t.Fatalf("okx interval 应回退 8, got %d", e.IntervalHours)
	}
	if len(idx[bybit.VenueName]) != 1 {
		t.Fatalf("空 symbol 应跳过, len=%d", len(idx[bybit.VenueName]))
	}
}

func mustCrossVenueStore(t *testing.T, dir string) *crossVenuePositionStore {
	t.Helper()
	s, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testCrossVenueLogger() *log.Logger {
	return log.New(log.Writer(), "[test] ", 0)
}
