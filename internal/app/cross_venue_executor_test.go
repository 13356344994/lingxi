package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/lifecycle"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
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
	// placeAlwaysErr 非空时每笔 PlaceOrder 都返回该错误(模拟 duplicate / 51169 等交易所拒单)。
	placeAlwaysErr error
	// placeClientIDs 记录每笔 PlaceOrder 的 client order ID(跨轮幂等回归测试用)。
	placeClientIDs []string
	// placeRequests 记录每笔 PlaceOrder 请求(平仓 qty/限价回归测试用)。
	placeRequests []venue.OrderRequest
	// clearOnReduceOnly=true 时 reduce-only 单成交后清空持仓(模拟平仓真实成交);
	// false 时持仓保持不变(模拟 IOC 没吃干净——平仓核验必须能发现)。
	clearOnReduceOnly bool
	// makerPostOnlyNeverFill=true 时 post-only 单永不成交(用于 maker 追价测试)。
	makerPostOnlyNeverFill bool
	// makerFillOnRound 指定第几轮 post-only 成交(0=按默认规则)。
	makerFillOnRound int
	// takerAlwaysFail 强制 taker(IOC) 兜底失败。
	takerAlwaysFail bool
	// canceledIDs 记录撤单 client ID。
	canceledIDs []string
	// openOrders 跟踪挂单状态(QueryOrder 用)。
	openOrders map[string]venue.OrderResult
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
		openOrders: make(map[string]venue.OrderResult),
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

func (f *crossVenueTestVenue) FetchPerpAvailableUSDT(context.Context) (float64, error) {
	return f.balanceUSDT, nil
}

func (f *crossVenueTestVenue) TopUpPerpMarginUSDT(context.Context, float64) (float64, error) {
	return 0, nil
}

func (f *crossVenueTestVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *crossVenueTestVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	atomic.AddInt32(f.placeCount, 1)
	if f.placeClientIDs != nil {
		f.placeClientIDs = append(f.placeClientIDs, req.ClientOrderID)
	}
	f.placeRequests = append(f.placeRequests, req)
	if f.placeAlwaysErr != nil {
		return venue.OrderResult{Status: "rejected", ClientOrderID: req.ClientOrderID}, f.placeAlwaysErr
	}
	if f.placeFailNext {
		f.placeFailNext = false
		return venue.OrderResult{Status: "rejected"}, errors.New("simulated fail")
	}
	if req.Type == venue.OrderTypePostOnly {
		round := 0
		if strings.Contains(req.ClientOrderID, "-m") {
			fmt.Sscanf(req.ClientOrderID[strings.LastIndex(req.ClientOrderID, "-m")+2:], "%d", &round)
		}
		if f.makerPostOnlyNeverFill || (f.makerFillOnRound > 0 && round != f.makerFillOnRound) {
			if f.openOrders == nil {
				f.openOrders = make(map[string]venue.OrderResult)
			}
			f.openOrders[req.ClientOrderID] = venue.OrderResult{
				Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
				Status: "new", ExecutedQty: 0, AvgPrice: req.Price,
			}
			return f.openOrders[req.ClientOrderID], nil
		}
	}
	if req.Type == venue.OrderTypeIOC && f.takerAlwaysFail {
		return venue.OrderResult{Status: "canceled", ClientOrderID: req.ClientOrderID}, errors.New("taker fail")
	}
	if req.ReduceOnly && f.clearOnReduceOnly {
		f.positions = nil
	}
	res := venue.OrderResult{
		Symbol:        req.Symbol,
		Market:        req.Market,
		ClientOrderID: req.ClientOrderID,
		Status:        "filled",
		ExecutedQty:   req.Qty,
		AvgPrice:      req.Price,
		CumQuoteUSDT:  req.Qty * req.Price,
	}
	if f.openOrders != nil {
		f.openOrders[req.ClientOrderID] = res
	}
	return res, nil
}

func (f *crossVenueTestVenue) CancelOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) error {
	f.canceledIDs = append(f.canceledIDs, clientOrderID)
	if f.openOrders != nil {
		if o, ok := f.openOrders[clientOrderID]; ok {
			o.Status = "canceled"
			f.openOrders[clientOrderID] = o
		}
	}
	return nil
}

func (f *crossVenueTestVenue) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	if f.openOrders != nil {
		if o, ok := f.openOrders[clientOrderID]; ok {
			return o, nil
		}
	}
	return venue.OrderResult{Status: "unknown", ExecutedQty: 0, ClientOrderID: clientOrderID}, nil
}

func (f *crossVenueTestVenue) ArmDeadMansSwitch(context.Context, time.Duration) error { return nil }

func (f *crossVenueTestVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return nil, nil
}

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
		MakerFirst:                   false, // 既有单测钉死纯 IOC 语义;maker 场景见 TestCrossVenueMaker*
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
		fetchFunding:           fetchVenueFundingForSymbol,
		fetchPerpAvailableUSDT: fetchVenuePerpAvailableUSDT,
		venueCaps:              func(v venue.Venue) venue.Capabilities { return v.Capabilities() },
		allocatedUSDT:          func(string) float64 { return allocated },
	}
}

// nonTradeableVenueName 单测用占位所(不在 crossVenueTradeableVenues),触发子集降级路径。
const nonTradeableVenueName = "kraken"

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
	opened := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	now := opened.Add(2 * time.Hour)
	exec := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
	}
	if err := exec.store.saveAll([]CrossVenuePosition{
		{
			Symbol: "ETH-USDT", Status: CrossVenueStatusOpen,
			HighVenue: "bybit", LowVenue: "okx",
			HighLeg:      CrossVenueLegState{Side: "sell", Qty: 0.5, EntryPrice: 2500},
			LowLeg:       CrossVenueLegState{Side: "buy", Qty: 0.5, EntryPrice: 2498},
			NotionalUSDT: 1250, EstFundingUSDT: 2.5,
			CrossVenueAttribution: CrossVenueAttribution{OpenFeesUSDT: 0.2},
			OpenedAt:              opened,
		},
	}); err != nil {
		t.Fatal(err)
	}
	dashboard := &DashboardServer{crossVenueExecutor: exec}
	req := httptest.NewRequest(http.MethodGet, "/api/cross-venue/positions", nil)
	rec := httptest.NewRecorder()
	dashboard.handleCrossVenuePositionsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var resp CrossVenuePositionsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.ActivePositionDetails) != 1 {
		t.Fatalf("details=%d want 1", len(resp.ActivePositionDetails))
	}
	d := resp.ActivePositionDetails[0]
	if math.Abs(d.OpenFeesUSDT-0.2) > 1e-9 {
		t.Fatalf("open_fees_usdt=%v want 0.2", d.OpenFeesUSDT)
	}
	wantEstClose := crossVenueEstCloseFeeUSDT(cfg.CrossVenueExecutor, &CrossVenuePosition{
		HighVenue: "bybit", LowVenue: "okx", NotionalUSDT: 1250,
	})
	if math.Abs(d.EstCloseFeesUSDT-wantEstClose) > 1e-9 {
		t.Fatalf("est_close_fees_usdt=%v want %v", d.EstCloseFeesUSDT, wantEstClose)
	}
	wantNet := 2.5 - 0.2 - wantEstClose
	if math.Abs(d.NetUSDT-wantNet) > 1e-9 {
		t.Fatalf("net_usdt=%v want %v", d.NetUSDT, wantNet)
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

// assertCrossVenueCloseClientIDsIncrement 断言高费腿平仓 client ID 跨轮递增且不重复(钉死 duplicate 事故)。
func assertCrossVenueCloseClientIDsIncrement(t *testing.T, ids []string, rounds int) {
	t.Helper()
	if len(ids) != rounds {
		t.Fatalf("高费腿 PlaceOrder 次数=%d want %d", len(ids), rounds)
	}
	seen := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		wantSuffix := fmt.Sprintf("ch%d", i+1)
		if !strings.Contains(id, wantSuffix) {
			t.Fatalf("round %d: clientID=%q 应含 %q", i+1, id, wantSuffix)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("round %d: 重复 clientID=%q", i+1, id)
		}
		seen[id] = struct{}{}
	}
}

// seedCrossVenueClosingExecutor 构造带 closing 持仓的执行器(落盘+内存双写,模拟 manageOpenPositions 路径)。
func seedCrossVenueClosingExecutor(t *testing.T, high, low *crossVenueTestVenue, pos CrossVenuePosition) (*CrossVenueExecutor, *crossVenuePositionStore) {
	t.Helper()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Hour)
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		positions: []CrossVenuePosition{pos},
	}
	return e, store
}

// TestCrossVenueCloseRetryClientIDIncrementsViaManageLoop 跨轮经 manageOpenPositions 重试时,
// 高费腿 client order ID 必须随 CloseAttempts 递增(ch1/ch2/ch3…),不能重复撞 duplicate。
func TestCrossVenueCloseRetryClientIDIncrementsViaManageLoop(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	// 高费腿每轮 duplicate 拒单、所内持仓不消失 → 必须跨轮换 client ID 重试。
	high.placeAlwaysErr = errors.New("110072 OrderLinkedID is duplicate")
	high.clearOnReduceOnly = false
	low.clearOnReduceOnly = false
	high.placeClientIDs = make([]string, 0, 3)

	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "测试 duplicate 重试",
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "dup-retry",
	}
	e, store := seedCrossVenueClosingExecutor(t, high, low, pos)

	const rounds = 3
	for round := 1; round <= rounds; round++ {
		e.manageOpenPositions(context.Background(), true)
		ps, lerr := store.load()
		if lerr != nil || len(ps) != 1 {
			t.Fatalf("round %d: 台账必须保留, ps=%v err=%v", round, ps, lerr)
		}
		if ps[0].CloseAttempts != round {
			t.Fatalf("round %d: CloseAttempts=%d want %d", round, ps[0].CloseAttempts, round)
		}
		mem := activeCrossVenuePositions(e.snapshotPositions())
		if len(mem) != 1 || mem[0].CloseAttempts != round {
			t.Fatalf("round %d: 内存台账 CloseAttempts 未同步, mem=%+v", round, mem)
		}
	}

	assertCrossVenueCloseClientIDsIncrement(t, high.placeClientIDs, rounds)
}

// TestCrossVenueCloseLegAlreadyFlatViaFetchPositions 一腿 FetchPositions 已为 0 时视为该腿完成,
// 下单前同步所内持仓,不因台账残留或 51169 类拒单无限重试;两腿真实持仓归零后移出台账。
func TestCrossVenueCloseLegAlreadyFlatViaFetchPositions(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	// 低费腿所内已平,但台账仍记有剩余量(线上 okx 已平、引擎不认的典型场景)。
	low.positions = nil
	low.placeAlwaysErr = errors.New("51169 no position to close")
	high.clearOnReduceOnly = true

	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "低费腿已平",
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "flat-low",
	}
	e, store := seedCrossVenueClosingExecutor(t, high, low, pos)

	e.manageOpenPositions(context.Background(), true)

	if n := atomic.LoadInt32(low.placeCount); n != 0 {
		t.Fatalf("低费腿所内已平时不应再下单(FetchPositions 同步), got %d", n)
	}
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("平干净后落盘台账应删除, got %d", len(ps))
	}
	if active := activeCrossVenuePositions(e.snapshotPositions()); len(active) != 0 {
		t.Fatalf("平干净后内存台账应清空, got %d", len(active))
	}
	if atomic.LoadInt32(high.placeCount) == 0 {
		t.Fatal("高费腿应成功下平仓单")
	}

	// 下轮管理循环不得再对低费腿发单。
	e.manageOpenPositions(context.Background(), true)
	if n := atomic.LoadInt32(low.placeCount); n != 0 {
		t.Fatalf("低费腿不应跨轮无限重试, got %d place calls", n)
	}
}

// TestCrossVenueCloseBothFetchPositionsZeroRemovesLedger 两腿 FetchPositions 均为 0 时一轮内收尾删台账。
func TestCrossVenueCloseBothFetchPositionsZeroRemovesLedger(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	// 所内均已平,台账 leg qty 已同步为 0(authority 复核后的 closing 续平状态)。
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "所内已全平",
		HighLeg: CrossVenueLegState{Qty: 0}, LowLeg: CrossVenueLegState{Qty: 0},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "both-flat",
	}
	e, store := seedCrossVenueClosingExecutor(t, high, low, pos)

	e.manageOpenPositions(context.Background(), true)

	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("FetchPositions 均为 0 时不应再下单: high=%d low=%d", *high.placeCount, *low.placeCount)
	}
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("落盘台账应删除, got %d", len(ps))
	}
	if active := activeCrossVenuePositions(e.snapshotPositions()); len(active) != 0 {
		t.Fatalf("内存台账应清空, got %d", len(active))
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

// newSubsetFallbackExecutor 组装一个带日志缓冲的影子执行器(bybit/okx 两所 fake,闸门全可过),
// 供降级路径集成测试复用。
func newSubsetFallbackExecutor(t *testing.T, buf *bytes.Buffer, scanner *CrossVenueScanner) (*CrossVenueExecutor, *crossVenueTestVenue, *crossVenueTestVenue) {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.002, 1, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	// fake 默认盘口 99/101(200bps)过不了 30bps 流动性闸,这里收紧到 2bps 让闸门全可过。
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.book = tight
	low.book = tight
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         log.New(buf, "", 0),
		nowFn:          func() time.Time { return time.Now().UTC() },
		symbolCooldown: map[string]time.Time{},
		scanner:        scanner,
	}
	return e, high, low
}

// TestCrossVenueSubsetFallbackOpensShadow 全局最优含不可交易所、可交易子集内有达标对 → 降级评估,
// 影子模式走到开仓决策(且绝不下单)。
func TestCrossVenueSubsetFallbackOpensShadow(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: nonTradeableVenueName, LowVenue: okx.VenueName, SpreadAPR: 25,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			// bybit 0.002/1h → 年化 17.5;okx 0.0001/8h → 年化 0.11;子集价差 ≈17.4 ≥ 1.5 达标。
			bybit.VenueName: {"BTC-USDT": {Rate: 0.002, IntervalHours: 1, NextFundingTime: nf}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, high, low := newSubsetFallbackExecutor(t, &buf, scanner)
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if !strings.Contains(logs, "降级用子集对") {
		t.Fatalf("应打降级日志, got:\n%s", logs)
	}
	if !strings.Contains(logs, "[影子] 本应开仓 BTC-USDT") {
		t.Fatalf("降级后应走到影子开仓决策, got:\n%s", logs)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("影子模式不得调 PlaceOrder")
	}
}

// TestCrossVenueSubsetFallbackBelowThresholdRejected 全局最优含不可交易所、子集对价差不达标 → 拒绝,
// 原因含子集最优价差。
func TestCrossVenueSubsetFallbackBelowThresholdRejected(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: nonTradeableVenueName, LowVenue: okx.VenueName, SpreadAPR: 25,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			// 两所费率几乎相等 → 子集价差 ≈0 < 1.5 不达标。
			bybit.VenueName: {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.00009, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, high, low := newSubsetFallbackExecutor(t, &buf, scanner)
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if !strings.Contains(logs, "子集内无达标替代") || !strings.Contains(logs, "子集最优=") {
		t.Fatalf("拒绝原因应含子集最优价差, got:\n%s", logs)
	}
	if strings.Contains(logs, "[影子] 本应开仓") {
		t.Fatalf("不达标不应走到开仓决策, got:\n%s", logs)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("不得下单")
	}
}

// TestCrossVenueTradeablePairNoFallback 全局最优本就可交易 → 不触发降级路径,行为不变。
func TestCrossVenueTradeablePairNoFallback(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0,
		}},
		// 索引故意只给 bybit 一所:若误触发降级会因「子集不足两所」拒绝——以此钉死「可交易对不走降级」。
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.002, IntervalHours: 1, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, _, _ := newSubsetFallbackExecutor(t, &buf, scanner)
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if strings.Contains(logs, "降级用子集对") || strings.Contains(logs, "不可交易所") {
		t.Fatalf("可交易对不应触发降级路径, got:\n%s", logs)
	}
	if !strings.Contains(logs, "[影子] 本应开仓 BTC-USDT") {
		t.Fatalf("原机会应正常走闸门到影子开仓, got:\n%s", logs)
	}
}

// TestCrossVenueSubsetFallbackInsufficientVenues 费率索引中该 symbol 可交易所覆盖不足两所 → 拒绝。
func TestCrossVenueSubsetFallbackInsufficientVenues(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: nonTradeableVenueName, LowVenue: bybit.VenueName, SpreadAPR: 25,
		}},
		// 子集内只有 bybit 一所有该币 → 凑不出对。
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			nonTradeableVenueName: {"BTC-USDT": {Rate: 0.01, IntervalHours: 8, NextFundingTime: nf}},
			bybit.VenueName:       {"BTC-USDT": {Rate: 0.002, IntervalHours: 1, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, high, low := newSubsetFallbackExecutor(t, &buf, scanner)
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if !strings.Contains(logs, "子集不足两所") {
		t.Fatalf("应按「子集不足两所」拒绝, got:\n%s", logs)
	}
	if strings.Contains(logs, "[影子] 本应开仓") {
		t.Fatal("不应走到开仓决策")
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("不得下单")
	}
	// 纯函数口径再钉一次:bestTradeableSubsetPair 必须返回 not-ok。
	if _, ok := bestTradeableSubsetPair("BTC-USDT", scanner.lastSnapshot.FundingByVenue); ok {
		t.Fatal("子集不足两所时 bestTradeableSubsetPair 应返回 not-ok")
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

func TestCrossVenueTradeableVenuesIncludesBinance(t *testing.T) {
	if !crossVenueTradeableVenues[binance.VenueName] {
		t.Fatal("binance 应在 crossVenueTradeableVenues")
	}
	found := false
	for _, name := range crossVenueExecutorVenues {
		if name == binance.VenueName {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("crossVenueExecutorVenues 应含 binance")
	}
}

func TestBinanceAdapterSatisfiesCrossVenueTrader(t *testing.T) {
	var _ crossVenueTrader = binance.New(nil)
}

func TestCrossVenueOpenGateBinanceConflict(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, liveOpenHedgesStateName)
	if err := writePersistedOpenHedgeState(statePath, persistedOpenHedgeState{
		Hedges: []persistedOpenHedgeEntry{{Symbol: "BTCUSDT", LifecycleState: lifecycle.StateActive}},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp:         CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now:         now,
		BinanceHeld: func(sym venue.Symbol) bool { return isBinanceSymbolHeld(dir, sym) },
	})
	if res.OK {
		t.Fatal("expected binance legacy conflict rejection")
	}
	if !strings.Contains(res.Reason, "Binance 遗留引擎") {
		t.Fatalf("reason=%q", res.Reason)
	}
}

func TestCrossVenueOpenGateBinanceOkxPair(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.contractBased = false
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.instruments = []venue.Instrument{{
		Symbol: "BTC-USDT", Market: venue.MarketPerp, StepSize: 0.001, MinNotional: 5, ContractSize: 1,
	}}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("binance/okx 应对过闸门: %s", res.Reason)
	}
}

func TestCrossVenueBinanceOkxDirectNoFallback(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName, SpreadAPR: 25,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			binance.VenueName: {"BTC-USDT": {Rate: 0.002, IntervalHours: 1, NextFundingTime: nf}},
			okx.VenueName:     {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.002, 1, nextFunding)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.book = tight
	low.book = tight
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         log.New(&buf, "", 0),
		nowFn:          func() time.Time { return now },
		symbolCooldown: map[string]time.Time{},
		scanner:        scanner,
	}
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if strings.Contains(logs, "降级用子集对") {
		t.Fatalf("binance 可交易时不应降级, got:\n%s", logs)
	}
	if !strings.Contains(logs, "[影子] 本应开仓 BTC-USDT") {
		t.Fatalf("应直接走 binance/okx 影子开仓, got:\n%s", logs)
	}
}

func testCrossVenueMakerExecutorConfig() EngineConfig {
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MakerFirst = true
	cfg.CrossVenueExecutor.MakerChaseRounds = 3
	cfg.CrossVenueExecutor.MakerChaseIntervalMs = 20
	cfg.CrossVenueExecutor.MakerDeadlineSeconds = 15
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	return cfg
}

func newCrossVenueMakerOpenExecutor(t *testing.T, high, low *crossVenueTestVenue) *CrossVenueExecutor {
	t.Helper()
	cfg := testCrossVenueMakerExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	return &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         testCrossVenueLogger(),
		nowFn:          time.Now,
		symbolCooldown: map[string]time.Time{},
	}
}

func testCrossVenueOpenPlan() crossVenueOpenPlan {
	return crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100},
		Qty:        0.5,
		IdemPrefix: "mk-test",
	}
}

// TestCrossVenueMakerFirstLegAbortNoPosition 第一腿 maker 不成交 → 撤单放弃,低费腿不下单。
func TestCrossVenueMakerFirstLegAbortNoPosition(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	high.makerPostOnlyNeverFill = true
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueMakerOpenExecutor(t, high, low)
	_, err := e.executeCrossVenueOpen(context.Background(), testCrossVenueOpenPlan(), true)
	if err == nil {
		t.Fatal("expected first leg maker abort")
	}
	if atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("低费腿不应下单, got %d", *low.placeCount)
	}
	if len(high.canceledIDs) == 0 {
		t.Fatal("maker 未成交应撤单")
	}
}

// TestCrossVenueMakerSecondLegTakerFallback 第一腿 maker 成交 + 第二腿 maker 不成交 → taker 兜底闭合。
func TestCrossVenueMakerSecondLegTakerFallback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.makerPostOnlyNeverFill = true
	low.placeClientIDs = make([]string, 0, 8)
	e := newCrossVenueMakerOpenExecutor(t, high, low)
	pos, err := e.executeCrossVenueOpen(context.Background(), testCrossVenueOpenPlan(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if pos.Status != CrossVenueStatusOpen {
		t.Fatalf("status=%s", pos.Status)
	}
	var hasMaker, hasTaker bool
	for _, id := range low.placeClientIDs {
		if strings.Contains(id, "-m") {
			hasMaker = true
		}
		if strings.Contains(id, "-tk") {
			hasTaker = true
		}
	}
	if !hasMaker || !hasTaker {
		t.Fatalf("第二腿应 maker 后 taker 兜底, ids=%v", low.placeClientIDs)
	}
}

// TestCrossVenueMakerSecondLegTakerFailRollback 第二腿 maker+taker 均失败 → 回滚第一腿。
func TestCrossVenueMakerSecondLegTakerFailRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.makerPostOnlyNeverFill = true
	low.takerAlwaysFail = true
	e := newCrossVenueMakerOpenExecutor(t, high, low)
	_, err := e.executeCrossVenueOpen(context.Background(), testCrossVenueOpenPlan(), true)
	if err == nil {
		t.Fatal("expected rollback path")
	}
	if n := atomic.LoadInt32(high.placeCount); n < 2 {
		t.Fatalf("应回滚第一腿, high places=%d", n)
	}
	if !e.isSymbolCooling("BTC-USDT") {
		t.Fatal("expected cooldown after rollback")
	}
}

// TestCrossVenueMakerFirstDisabledUsesIOC maker_first=false 时走纯 IOC(每腿一次下单)。
func TestCrossVenueMakerFirstDisabledUsesIOC(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.placeClientIDs = make([]string, 0, 4)
	low.placeClientIDs = make([]string, 0, 4)
	cfg := testCrossVenueMakerExecutorConfig()
	cfg.CrossVenueExecutor.MakerFirst = false
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         testCrossVenueLogger(),
		nowFn:          func() time.Time { return now },
		symbolCooldown: map[string]time.Time{},
	}
	_, err := e.executeCrossVenueOpen(context.Background(), testCrossVenueOpenPlan(), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if atomic.LoadInt32(high.placeCount) != 1 || atomic.LoadInt32(low.placeCount) != 1 {
		t.Fatalf("IOC 模式每腿应只下一单: high=%d low=%d", *high.placeCount, *low.placeCount)
	}
	for _, id := range append(high.placeClientIDs, low.placeClientIDs...) {
		if strings.Contains(id, "-m") {
			t.Fatalf("不应出现 maker 单: %s", id)
		}
	}
}
