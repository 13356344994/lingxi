package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// unknownNext 下一笔 PlaceOrder 返回 unknown 态(模拟查单失败/状态不明)。
	unknownNext bool
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
	if f.unknownNext {
		f.unknownNext = false
		return venue.OrderResult{
			Symbol:        req.Symbol,
			Market:        req.Market,
			ClientOrderID: req.ClientOrderID,
			Status:        "unknown",
			ExecutedQty:   req.Qty,
			AvgPrice:      req.Price,
		}, errors.New("simulated unknown")
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
		// 既有闸门单测的 funding 结算时刻多为 now+(0.5~4h),并非验证"结算前进场窗";给一个极宽的进场窗
		// (48h)使这些用例不被新窗口闸短路,继续考察滑点/保证金/中间价等原逻辑。窗口闸专项见 TestCrossVenueOpenGateFundingEntryWindow*。
		OpenFundingLeadSeconds: 172800,
	}
	defExec := DefaultEngineConfig().CrossVenueExecutor
	cfg.CrossVenueExecutor.CarryFundingEntryBaseFraction = defExec.CarryFundingEntryBaseFraction
	cfg.CrossVenueExecutor.CarryFundingEntryMinVolScale = defExec.CarryFundingEntryMinVolScale
	cfg.CrossVenueExecutor.CarryFundingEntryVolRefPerHour = defExec.CarryFundingEntryVolRefPerHour
	cfg.CrossVenueExecutor.CarryFundingEntryFloorMinutes = defExec.CarryFundingEntryFloorMinutes
	cfg.CrossVenueExecutor.CarryFundingEntryVolLookbackScans = defExec.CarryFundingEntryVolLookbackScans
	cfg.CrossVenueExecutor.PerpPerpOpeningsDefaultEnabled = defExec.PerpPerpOpeningsDefaultEnabled
	cfg.CrossVenueScanner.Enabled = true
	cfg.AllocatedCapitalUSDT = 1000
	return cfg
}

func TestCrossVenueGateDepsUsesScannerFundingCache(t *testing.T) {
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	v := &fakeFundingVenue{name: okx.VenueName}
	e := &CrossVenueExecutor{
		cfg: DefaultEngineConfig(),
		venues: map[string]venue.Venue{
			okx.VenueName: v,
		},
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "snap-1",
		ScannedAt:  now,
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			okx.VenueName: {
				"BTC-USDT": {Rate: 0.0002, IntervalHours: 8, NextFundingTime: now.Add(2 * time.Hour)},
			},
		},
	}
	got, ok := e.gateDeps(snap).fetchFunding(context.Background(), v, "BTC-USDT")
	if !ok {
		t.Fatal("expected funding from scanner cache")
	}
	if got.LastFundingRate != 0.0002 || got.IntervalHours != 8 {
		t.Fatalf("unexpected funding snapshot: %+v", got)
	}
	if v.called != 0 {
		t.Fatalf("venue funding fetch should not be called, got %d", v.called)
	}
}

// testScannerWithFunding 构造一个只带费率缓存的扫描器(执行引擎管理循环从这里查,不触网)。
func testScannerWithFunding(byVenue map[string]map[string]CrossVenueFundingEntry) *CrossVenueScanner {
	return &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{FundingByVenue: byVenue}}
}

func testBaselinePortfolioRecordBySnapshotID(t *testing.T, records []CrossVenueBaselinePortfolioReplayRecord, snapshotID string) CrossVenueBaselinePortfolioReplayRecord {
	t.Helper()
	for _, rec := range records {
		if rec.SnapshotID == snapshotID {
			return rec
		}
	}
	t.Fatalf("baseline portfolio record %q not found in %+v", snapshotID, records)
	return CrossVenueBaselinePortfolioReplayRecord{}
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

func TestCrossVenueOffModeNoPlaceOrder(t *testing.T) {
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
		t.Fatalf("OFF open: %v", err)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("OFF mode must not PlaceOrder: high=%d low=%d", *high.placeCount, *low.placeCount)
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

func newCrossVenueOpenTestExecutor(t *testing.T, high, low *crossVenueTestVenue, sink *recordingAlertSink) *CrossVenueExecutor {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
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
	if sink != nil {
		e.alerts = &AlertDispatcher{
			sinks:    []AlertSink{sink},
			minRank:  severityRank(EventLevelInfo),
			timeout:  time.Second,
			now:      time.Now,
			lastSent: map[string]time.Time{},
		}
	}
	return e
}

func TestCrossVenueSecondLegUnknownBlocksRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.unknownNext = true
	sink := &recordingAlertSink{}
	e := newCrossVenueOpenTestExecutor(t, high, low, sink)
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100},
		Qty:        0.5,
		IdemPrefix: "test-unknown-l2",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected second-leg unknown to block rollback, got pos=%+v err=%v", pos, err)
	}
	if atomic.LoadInt32(high.placeCount) != 1 {
		t.Fatalf("rollback must not run on unknown second leg, high places=%d", atomic.LoadInt32(high.placeCount))
	}
	if pos.Status != CrossVenueStatusOpeningSecondLeg {
		t.Fatalf("expected opening_second_leg for reconcile, got %s", pos.Status)
	}
	if pos.HighLeg.Qty <= executionQtyTolerance {
		t.Fatalf("expected first leg qty retained, got %.6f", pos.HighLeg.Qty)
	}
	titles := sink.waitTitles(t, 1)
	if len(titles) == 0 || !strings.Contains(titles[0], "查单未知") {
		t.Fatalf("expected critical alert, got %v", titles)
	}
	ps, _ := e.store.load()
	if len(ps) != 1 || ps[0].Status != CrossVenueStatusOpeningSecondLeg {
		t.Fatalf("expected persisted opening position, got %+v", ps)
	}
	if e.isSymbolCooling("BTC-USDT") {
		t.Fatal("unknown path must not set cooldown (no rollback attempted)")
	}
}

func TestCrossVenueFirstLegUnknownBlocksRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.unknownNext = true
	sink := &recordingAlertSink{}
	e := newCrossVenueOpenTestExecutor(t, high, low, sink)
	plan := crossVenueOpenPlan{
		Opp:        CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate:       crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100},
		Qty:        0.5,
		IdemPrefix: "test-unknown-l1",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err == nil || !strings.Contains(err.Error(), "禁止自动回滚") {
		t.Fatalf("expected first-leg unknown to block rollback, got pos=%+v err=%v", pos, err)
	}
	if atomic.LoadInt32(high.placeCount) != 1 {
		t.Fatalf("first-leg unknown must not trigger reduce-only rollback, high places=%d", atomic.LoadInt32(high.placeCount))
	}
	if atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("second leg must not be attempted, low places=%d", atomic.LoadInt32(low.placeCount))
	}
	if pos.HighLeg.Qty <= executionQtyTolerance {
		t.Fatalf("expected first leg qty recorded for reconcile, got %.6f", pos.HighLeg.Qty)
	}
	titles := sink.waitTitles(t, 1)
	if len(titles) == 0 || !strings.Contains(titles[0], "查单未知") {
		t.Fatalf("expected critical alert, got %v", titles)
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
	e.reconcileOnStart(context.Background(), true)
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("expected orphan cleared, got %d positions", len(ps))
	}
}

func TestCrossVenueReconcileOnStartImportUntrackedPair(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(2*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(2*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.UntrackedExposureAction = "import"
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected 1 imported position, got %d", len(ps))
	}
	if ps[0].RecoveryAction != crossVenueRecoveryActionImport || ps[0].TransitionState != "import" {
		t.Fatalf("unexpected imported state: %+v", ps[0])
	}
	if ps[0].HighVenue != bybit.VenueName || ps[0].LowVenue != okx.VenueName {
		t.Fatalf("unexpected imported venues: %+v", ps[0])
	}
}

func TestCrossVenueReconcileOnStartFlattenUntrackedPair(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(2*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(2*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	high.clearOnReduceOnly = true
	low.clearOnReduceOnly = true
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.UntrackedExposureAction = "flatten"
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("flatten should remove ledger, got %d", len(ps))
	}
	if atomic.LoadInt32(high.placeCount) == 0 || atomic.LoadInt32(low.placeCount) == 0 {
		t.Fatalf("flatten should place close orders: high=%d low=%d", *high.placeCount, *low.placeCount)
	}
}

func TestCrossVenueReconcileOnStartFlattenRespectsLiveFalse(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(2*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(2*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.UntrackedExposureAction = "flatten"
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.reconcileOnStart(context.Background(), false)
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("live=false must not flatten even when config live_trading_enabled=true: high=%d low=%d",
			atomic.LoadInt32(high.placeCount), atomic.LoadInt32(low.placeCount))
	}
	if e.OpeningsEnabled() {
		t.Fatal("unhandled untracked exposure should latch openings off")
	}
}

func TestCrossVenueReconcileOnStartHaltUntrackedPair(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(2*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(2*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.UntrackedExposureAction = "halt"
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.openingsEnabled.Store(true)
	e.reconcileOnStart(context.Background(), true)
	if e.OpeningsEnabled() {
		t.Fatal("halt branch should disable openings")
	}
}

func TestCrossVenueShouldCloseForBetterSwitchCandidate(t *testing.T) {
	now := time.Now().UTC()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.SwitchBufferUSDT = 0.1
	cfg.CrossVenueExecutor.MinHoldHours = 1
	cfg.CrossVenueExecutor.PostFundingCooldownSeconds = 0
	cfg.CrossVenueExecutor.SlippageBudgetBps = 0
	cfg.CrossVenueExecutor.EVExpectedSlippageBps = 3
	cfg.CrossVenueScanner.MaxSnapshotAgeSeconds = 720
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		SnapshotID: "snap-switch",
		ScannedAt:  now,
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "ETH-USDT", HighVenue: gate.VenueName, LowVenue: okx.VenueName, SpreadAPR: 30,
			HighFundingRate: 0.05, LowFundingRate: 0.0, HighIntervalHours: 8, LowIntervalHours: 8,
			HighNextFundingTime: now.Add(4 * time.Hour), LowNextFundingTime: now.Add(4 * time.Hour),
			ComparableWindowHours: 4, ExpectedHoldHours: 4, TradableNotional: 50,
			SpreadEV: 2.5, OpenCost: 0.05, CloseCost: 0.05, ExpectedSlippage: 0.01,
			NetEV: 2.0, RankScore: 2.0,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.02, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			okx.VenueName: {
				"BTC-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)},
				"ETH-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)},
			},
			gate.VenueName: {"ETH-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
		},
	}}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		scanner:   scanner,
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 50,
		HighLeg: CrossVenueLegState{Qty: 0.5, EntryPrice: 100}, LowLeg: CrossVenueLegState{Qty: 0.5, EntryPrice: 100},
		RecoveryQualityScore: 0.9, ExecutionScore: 0.9,
	}
	reason := e.shouldCloseCrossVenue(context.Background(), &pos)
	if !strings.Contains(reason, "更优候选触发换仓") {
		t.Fatalf("expected switch close reason, got %q", reason)
	}
	if pos.SwitchCooldownUntil.IsZero() || pos.TransitionState != "switch_pending" || pos.ChurnStreak != 1 {
		t.Fatalf("switch state not updated: %+v", pos)
	}
	if pos.PendingSwitchHighVenue != gate.VenueName || pos.PendingSwitchLowVenue != okx.VenueName || pos.PendingSwitchExpectedGainUSDT <= 0 {
		t.Fatalf("pending switch fields not populated: %+v", pos)
	}
	if pos.PendingSwitchSequence != crossVenueSwitchSequenceCloseThenOpen || pos.PartialSwitchPhase != "pending" {
		t.Fatalf("pending switch sequence/phase not populated: %+v", pos)
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 {
		t.Fatalf("expected 1 switch intent, got %d", len(intents))
	}
	if intents[0].TargetHighVenue != gate.VenueName || intents[0].TargetLowVenue != okx.VenueName {
		t.Fatalf("unexpected switch intent: %+v", intents[0])
	}
	if intents[0].SwitchSequence != crossVenueSwitchSequenceCloseThenOpen || intents[0].PartialSwitchPhase != "pending" {
		t.Fatalf("unexpected switch intent sequence/phase: %+v", intents[0])
	}
}

func TestCrossVenueEvaluateNewOpensPrioritizesPendingSwitchIntent(t *testing.T) {
	now := time.Now().UTC()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	cfg.CrossVenueExecutor.MaxConcurrent = 1
	cfg.CrossVenueScanner.MaxSnapshotAgeSeconds = 720
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	bybitV := newCrossVenueTestVenue(bybit.VenueName, 0.05, 8, now.Add(4*time.Hour))
	okxV := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	gateV := newCrossVenueTestVenue(gate.VenueName, 0.04, 8, now.Add(4*time.Hour))
	bybitV.book, okxV.book, gateV.book = tight, tight, tight
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg: cfg,
		venues: map[string]venue.Venue{
			bybit.VenueName: bybitV,
			okx.VenueName:   okxV,
			gate.VenueName:  gateV,
		},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		DecisionCycleID:    "cvcycle-test",
		SnapshotID:         "snap-intent",
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new",
		Reason:             "switch-intent",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	bybitOpp := makeTestOpenOpportunity(cfg.CrossVenueExecutor, now, "BTC-USDT", bybit.VenueName, okx.VenueName, 0.05, 0.0, 20, 0.90, 0.90, 0.40, 0.90)
	gateOpp := makeTestOpenOpportunity(cfg.CrossVenueExecutor, now, "BTC-USDT", gate.VenueName, okx.VenueName, 0.04, 0.0, 20, 0.70, 0.70, 0.40, 0.80)
	e.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		SnapshotID:    "snap-open",
		ScannedAt:     now,
		Opportunities: []CrossVenueOpportunity{bybitOpp, gateOpp},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			gate.VenueName:  {"BTC-USDT": {Rate: 0.04, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
		},
	}}
	e.evaluateNewOpens(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected 1 opened position, got %d", len(ps))
	}
	if ps[0].HighVenue != gate.VenueName || ps[0].LowVenue != okx.VenueName {
		t.Fatalf("switch intent should prioritize gate/okx, got %+v", ps[0])
	}
	if intents := e.activeSwitchIntents(now); len(intents) != 0 {
		t.Fatalf("switch intent should be consumed after open, got %+v", intents)
	}
}

func TestCrossVenueEvaluateNewOpensBlocksFreshOpensWhileSwitchRetryPending(t *testing.T) {
	now := time.Now().UTC()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	cfg.CrossVenueExecutor.MaxConcurrent = 1
	cfg.CrossVenueScanner.MaxSnapshotAgeSeconds = 720
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	bybitV := newCrossVenueTestVenue(bybit.VenueName, 0.05, 8, now.Add(4*time.Hour))
	okxV := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	gateV := newCrossVenueTestVenue(gate.VenueName, 0.04, 8, now.Add(4*time.Hour))
	bybitV.book, okxV.book, gateV.book = tight, tight, tight
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg: cfg,
		venues: map[string]venue.Venue{
			bybit.VenueName: bybitV,
			okx.VenueName:   okxV,
			gate.VenueName:  gateV,
		},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		DecisionCycleID:    "cvcycle-retry",
		SnapshotID:         "snap-retry",
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_retry",
		Reason:             "switch retry pending",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	// 只给一个无关的新机会,不提供 pending switch 目标,执行器应阻止 fresh open。
	ethOpp := makeTestOpenOpportunity(cfg.CrossVenueExecutor, now, "ETH-USDT", bybit.VenueName, okx.VenueName, 0.05, 0.0, 20, 0.95, 0.95, 0.95, 0.95)
	e.scanner = &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		SnapshotID:    "snap-open",
		ScannedAt:     now,
		Opportunities: []CrossVenueOpportunity{ethOpp},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"ETH-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			okx.VenueName:   {"ETH-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
		},
	}}
	e.evaluateNewOpens(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("should not open fresh positions while switch retry pending, got %+v", ps)
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "open_new_retry" {
		t.Fatalf("pending retry intent should remain active, got %+v", intents)
	}
}

func TestCrossVenueClosePositionPromotesSwitchIntentToOpenNew(t *testing.T) {
	now := time.Now().UTC()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	high := newCrossVenueTestVenue(bybit.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.clearOnReduceOnly = true
	low.clearOnReduceOnly = true
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "close_old",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	pos := CrossVenuePosition{
		Symbol:                 "BTC-USDT",
		HighVenue:              bybit.VenueName,
		LowVenue:               okx.VenueName,
		Status:                 CrossVenueStatusClosing,
		OpenedAt:               now.Add(-time.Hour),
		UpdatedAt:              now,
		IdemPrefix:             "close-then-open",
		CloseReason:            "switch close",
		HighLeg:                CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                 CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PendingSwitchHighVenue: gate.VenueName,
		PendingSwitchLowVenue:  okx.VenueName,
		PendingSwitchSequence:  crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:     "close_old",
	}
	if err := e.closeCrossVenuePosition(context.Background(), pos, true, pos.CloseReason); err != nil {
		t.Fatal(err)
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 {
		t.Fatalf("expected one promoted switch intent, got %d", len(intents))
	}
	if intents[0].PartialSwitchPhase != "open_new" {
		t.Fatalf("expected switch intent promoted to open_new, got %+v", intents[0])
	}
}

func TestCrossVenueClosePositionKeepsPartialSwitchState(t *testing.T) {
	now := time.Now().UTC()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	high := newCrossVenueTestVenue(bybit.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	pos := CrossVenuePosition{
		Symbol:                        "BTC-USDT",
		HighVenue:                     bybit.VenueName,
		LowVenue:                      okx.VenueName,
		Status:                        CrossVenueStatusClosing,
		OpenedAt:                      now.Add(-time.Hour),
		UpdatedAt:                     now,
		IdemPrefix:                    "partial-switch",
		CloseReason:                   "switch close",
		HighLeg:                       CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                        CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PendingSwitchHighVenue:        gate.VenueName,
		PendingSwitchLowVenue:         okx.VenueName,
		PendingSwitchReason:           "switch to gate/okx",
		PendingSwitchExpectedGainUSDT: 1.2,
	}
	if err := e.store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	if err := e.closeCrossVenuePosition(context.Background(), pos, true, pos.CloseReason); err == nil {
		t.Fatal("expected partial switch close to remain unfinished")
	}
	ps, err := e.store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected position kept for retry, got %d", len(ps))
	}
	if ps[0].TransitionState != "partial_switch" || ps[0].RecoveryAction != crossVenueRecoveryActionFlatten || ps[0].Status != CrossVenueStatusClosing {
		t.Fatalf("unexpected partial switch recovery state: %+v", ps[0])
	}
	if ps[0].PartialSwitchPhase != "close_old" || ps[0].PendingSwitchSequence != crossVenueSwitchSequenceCloseThenOpen {
		t.Fatalf("unexpected partial switch phase/sequence after unfinished close: %+v", ps[0])
	}
}

func TestCrossVenueReconcileOnStartRecoversPendingSwitch(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                        "BTC-USDT",
		HighVenue:                     bybit.VenueName,
		LowVenue:                      okx.VenueName,
		Status:                        CrossVenueStatusOpen,
		OpenedAt:                      now.Add(-2 * time.Hour),
		UpdatedAt:                     now,
		IdemPrefix:                    "reconcile-switch",
		HighLeg:                       CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                        CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PendingSwitchHighVenue:        gate.VenueName,
		PendingSwitchLowVenue:         okx.VenueName,
		PendingSwitchReason:           "pending switch recovery",
		PendingSwitchExpectedGainUSDT: 0.8,
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	high := newCrossVenueTestVenue(bybit.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected recovered position, got %d", len(ps))
	}
	if ps[0].Status != CrossVenueStatusClosing || ps[0].TransitionState != "partial_switch" || ps[0].RecoveryAction != crossVenueRecoveryActionFlatten {
		t.Fatalf("unexpected recovered pending switch state: %+v", ps[0])
	}
	if ps[0].PartialSwitchPhase != "close_old" || ps[0].PendingSwitchSequence != crossVenueSwitchSequenceCloseThenOpen {
		t.Fatalf("pending switch should recover into close_old phase: %+v", ps[0])
	}
}

func TestCrossVenueReconcileOnStartPromotesUnhedgedSwitchTargetToSecondLegContinuation(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:             "BTC-USDT",
		HighVenue:          gate.VenueName,
		LowVenue:           okx.VenueName,
		Status:             CrossVenueStatusOpeningSecondLeg,
		TransitionState:    "open",
		OpenedAt:           now.Add(-10 * time.Minute),
		UpdatedAt:          now,
		IdemPrefix:         "reconcile-target-open",
		HighLeg:            CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:             CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PartialSwitchPhase: "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		Reason:             "switch target opening",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected recovered opening position, got %d", len(ps))
	}
	if ps[0].Status != CrossVenueStatusOpeningSecondLeg || ps[0].TransitionState != "open" || ps[0].RecoveryAction != crossVenueRecoveryActionObserve {
		t.Fatalf("unexpected recovered switch target state: %+v", ps[0])
	}
	if ps[0].PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("expected open_new_second_leg phase, got %+v", ps[0])
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("intent should move to open_new_second_leg, got %+v", intents)
	}
}

func TestCrossVenueReconcileOnStartConfirmsLiveSwitchTarget(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:             "BTC-USDT",
		HighVenue:          gate.VenueName,
		LowVenue:           okx.VenueName,
		Status:             CrossVenueStatusOpeningSecondLeg,
		TransitionState:    "open",
		OpenedAt:           now.Add(-10 * time.Minute),
		UpdatedAt:          now,
		IdemPrefix:         "reconcile-target-live",
		HighLeg:            CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:             CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PartialSwitchPhase: "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		Reason:             "switch target opening",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected live switch target position, got %d", len(ps))
	}
	if ps[0].Status != CrossVenueStatusOpen || ps[0].TransitionState != "hold" || ps[0].RecoveryAction != crossVenueRecoveryActionObserve {
		t.Fatalf("unexpected confirmed switch target state: %+v", ps[0])
	}
	if ps[0].PartialSwitchPhase != "consumed" {
		t.Fatalf("expected consumed phase, got %+v", ps[0])
	}
	if intents := e.activeSwitchIntents(now); len(intents) != 0 {
		t.Fatalf("switch intent should be consumed after reconcile confirmation, got %+v", intents)
	}
}

func TestCrossVenueReconcileOnStartPromotesFirstLegToSecondLegContinuation(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningFirstLeg,
		TransitionState:       "open",
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "reconcile-promote-second-leg",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy"},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_first_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_first_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	e.reconcileOnStart(context.Background(), true)
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("expected promoted position, got %d", len(ps))
	}
	if ps[0].Status != CrossVenueStatusOpeningSecondLeg || ps[0].PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("expected promotion to second-leg continuation, got %+v", ps[0])
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("intent should be promoted to open_new_second_leg, got %+v", intents)
	}
}

func TestManageCrossVenuePositionConfirmsOpeningSwitchTarget(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningSecondLeg,
		TransitionState:       "open",
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "runtime-target-live",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	got, changed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !changed {
		t.Fatal("expected opening continuation to be handled")
	}
	if got.Status != CrossVenueStatusOpen || got.TransitionState != "hold" || got.PartialSwitchPhase != "consumed" {
		t.Fatalf("unexpected managed target position: %+v", got)
	}
	if intents := e.activeSwitchIntents(now); len(intents) != 0 {
		t.Fatalf("switch intent should be consumed after runtime confirmation, got %+v", intents)
	}
}

func TestManageCrossVenuePositionContinuesOpeningSecondLeg(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningSecondLeg,
		TransitionState:       "open",
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "runtime-target-continue",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy"},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	got, changed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !changed {
		t.Fatal("expected opening second leg continuation to be handled")
	}
	if got.Status != CrossVenueStatusOpen || got.TransitionState != "hold" || got.PartialSwitchPhase != "consumed" {
		t.Fatalf("unexpected continued target position: %+v", got)
	}
	if got.LowLeg.Qty <= 0 || got.LowLeg.EntryPrice <= 0 {
		t.Fatalf("expected second leg to be filled, got %+v", got)
	}
	if intents := e.activeSwitchIntents(now); len(intents) != 0 {
		t.Fatalf("switch intent should be consumed after continuation success, got %+v", intents)
	}
}

func TestManageCrossVenuePositionContinuesOpeningSecondLegUnknownBlocksRollback(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningSecondLeg,
		TransitionState:       "open",
		RecoveryAction:        crossVenueRecoveryActionObserve,
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "runtime-target-unknown-l2",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy"},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	low.unknownNext = true
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	sink := &recordingAlertSink{}
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	e.alerts = &AlertDispatcher{
		sinks:    []AlertSink{sink},
		minRank:  severityRank(EventLevelInfo),
		timeout:  time.Second,
		now:      time.Now,
		lastSent: map[string]time.Time{},
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	got, changed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !changed {
		t.Fatal("expected unknown second-leg continuation to persist awareness")
	}
	if got.Status != CrossVenueStatusOpeningSecondLeg || got.TransitionState != "open" || got.PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("expected recoverable opening_second_leg/open state, got %+v", got)
	}
	if got.RecoveryAction != crossVenueRecoveryActionObserve {
		t.Fatalf("expected observe recovery action, got %q", got.RecoveryAction)
	}
	if atomic.LoadInt32(low.placeCount) != 1 {
		t.Fatalf("expected one second-leg attempt, low places=%d", atomic.LoadInt32(low.placeCount))
	}
	if atomic.LoadInt32(high.placeCount) != 0 {
		t.Fatalf("must not flatten first leg on unknown second leg, high places=%d", atomic.LoadInt32(high.placeCount))
	}
	if len(high.positions) != 1 || high.positions[0].PositionAmt >= 0 {
		t.Fatalf("expected first leg position retained, got %+v", high.positions)
	}
	titles := sink.waitTitles(t, 1)
	if len(titles) == 0 || !strings.Contains(titles[0], "查单未知") {
		t.Fatalf("expected critical unknown-order alert, got %v", titles)
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("switch intent should remain open_new_second_leg, got %+v", intents)
	}
	ps, _ := store.load()
	if len(ps) != 1 || ps[0].Status != CrossVenueStatusOpeningSecondLeg {
		t.Fatalf("expected persisted opening position, got %+v", ps)
	}
}

func TestManageCrossVenuePositionPromotesFirstLegAndContinuesSecondLeg(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningFirstLeg,
		TransitionState:       "open",
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "runtime-promote-second-leg",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy"},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_first_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_first_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	got, changed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !changed {
		t.Fatal("expected first-leg continuation to be handled")
	}
	if got.Status != CrossVenueStatusOpen || got.PartialSwitchPhase != "consumed" || got.LowLeg.Qty <= 0 {
		t.Fatalf("expected promotion then second-leg continuation success, got %+v", got)
	}
	if intents := e.activeSwitchIntents(now); len(intents) != 0 {
		t.Fatalf("switch intent should be consumed after promoted continuation success, got %+v", intents)
	}
}

func TestManageCrossVenuePositionRollsBackUnhedgedOpeningSwitchTarget(t *testing.T) {
	dir := t.TempDir()
	store := mustCrossVenueStore(t, dir)
	now := time.Now().UTC()
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             gate.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningSecondLeg,
		TransitionState:       "open",
		OpenedAt:              now.Add(-10 * time.Minute),
		UpdatedAt:             now,
		IdemPrefix:            "runtime-target-unhedged",
		HighLeg:               CrossVenueLegState{Venue: gate.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5, EntryPrice: 100},
		PendingSwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase:    "open_new_second_leg",
	}
	if err := store.upsert(pos); err != nil {
		t.Fatal(err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	high := newCrossVenueTestVenue(gate.VenueName, 0.02, 8, now.Add(4*time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 8, now.Add(4*time.Hour))
	low.makerPostOnlyNeverFill = true
	low.takerAlwaysFail = true
	high.clearOnReduceOnly = true
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = nil
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{gate.VenueName: high, okx.VenueName: low},
		store:     store,
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       okx.VenueName,
		TargetHighVenue:    gate.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new_second_leg",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	got, changed := e.manageCrossVenuePosition(context.Background(), pos, true)
	if !changed {
		t.Fatal("expected unhedged opening continuation to be handled")
	}
	if got.Status != CrossVenueStatusOrphanSingleLeg || got.TransitionState != "recover" || got.PartialSwitchPhase != "rollback_new" {
		t.Fatalf("unexpected rollback target position: %+v", got)
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "rollback_new" {
		t.Fatalf("switch intent should enter rollback_new, got %+v", intents)
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
			EstimatedSwitchCost:    3.2,
			TransitionState:        "partial_switch",
			PendingSwitchHighVenue: "gate",
			PendingSwitchLowVenue:  "okx",
			PendingSwitchSequence:  crossVenueSwitchSequenceCloseThenOpen,
			PartialSwitchPhase:     "open_new_second_leg",
			CrossVenueAttribution:  CrossVenueAttribution{OpenFeesUSDT: 0.2},
			OpenedAt:               opened,
		},
	}); err != nil {
		t.Fatal(err)
	}
	exec.lastCycle = CrossVenueCycleExplain{
		DecisionCycleID: "cvcycle-test",
		SnapshotID:      "cvsnap-test",
		OpenSummary:     "未开新仓",
		SwitchSummary:   "pending=1 partial=1 orphan=0 transitioning=1",
		FallbackSummary: "无 acceptable fallback",
		NoTradeReasonsTop: []CrossVenueReasonCount{
			{Reason: "execution<0.50", Count: 2},
		},
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
	if resp.PendingSwitches != 1 || resp.PartialSwitches != 1 || resp.TransitioningPositions != 1 {
		t.Fatalf("unexpected transition metrics: %+v", resp)
	}
	if math.Abs(resp.TransitionHeadroomUSDT-3.2) > 1e-9 {
		t.Fatalf("transition_headroom_usdt=%v want 3.2", resp.TransitionHeadroomUSDT)
	}
	if resp.LastCycle.DecisionCycleID != "cvcycle-test" || len(resp.LastCycle.NoTradeReasonsTop) != 1 {
		t.Fatalf("unexpected last_cycle: %+v", resp.LastCycle)
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
	if d.PendingSwitchTo != "gate/okx" || d.PendingSwitchSeq != crossVenueSwitchSequenceCloseThenOpen || d.PartialSwitchPhase != "open_new_second_leg" {
		t.Fatalf("unexpected detail transition info: %+v", d)
	}
}

func TestCrossVenueValidationSnapshot(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e := &CrossVenueExecutor{
		cfg:       cfg,
		store:     mustCrossVenueStore(t, dir),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	if err := os.MkdirAll(crossVenueRecordDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	settlements := []CrossVenueSettlement{
		{
			SettlementID: "s1", Symbol: "BTC-USDT", CloseTime: now.Add(-2 * time.Hour),
			EntrySnapshotID: "snap-btc", HighVenue: "bybit", LowVenue: "okx", NotionalUSDT: 50,
			NetPnL: 5.5, RollbackCost: 0.4, RebalanceCost: 0.1,
			RealizedFunding: 1.5, TotalFees: 0.3, OpenSlippage: 0.1, CloseSlippage: 0.2,
			UsedFallback: true, SwitchExit: true, SwitchSequence: crossVenueSwitchSequenceCloseThenOpen,
		},
		{
			SettlementID: "s2", Symbol: "ETH-USDT", CloseTime: now.Add(-90 * time.Minute),
			EntrySnapshotID: "snap-eth", HighVenue: "binance", LowVenue: "gate", NotionalUSDT: 40,
			NetPnL: 2.0, RollbackCost: 0.2, RebalanceCost: 0.0,
			RealizedFunding: 0.5, TotalFees: 0.1, OpenSlippage: 0.05, CloseSlippage: 0.05,
		},
		{
			SettlementID: "old", Symbol: "OLD-USDT", CloseTime: now.Add(-48 * time.Hour),
			NetPnL: 100,
		},
	}
	var settlementLines []string
	for _, rec := range settlements {
		data, _ := json.Marshal(rec)
		settlementLines = append(settlementLines, string(data))
	}
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName), []byte(strings.Join(settlementLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cycles := []CrossVenueCycleExplain{
		{
			DecisionCycleID: "c1", DecisionTime: now.Add(-30 * time.Minute), SnapshotID: "snap-btc", HaltReason: "snapshot_too_old",
			NoTradeReasonsTop: []CrossVenueReasonCount{{Reason: "execution<0.50", Count: 2}},
		},
		{
			DecisionCycleID: "c2", DecisionTime: now.Add(-20 * time.Minute), HaltReason: "openings_disabled",
			NoTradeReasonsTop: []CrossVenueReasonCount{{Reason: "execution<0.50", Count: 1}, {Reason: "liquidity<0.35", Count: 1}},
		},
	}
	var cycleLines []string
	for _, rec := range cycles {
		data, _ := json.Marshal(rec)
		cycleLines = append(cycleLines, string(data))
	}
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(dir), crossVenueCycleLogFileName), []byte(strings.Join(cycleLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scans := []crossVenueScanLogRecord{
		{
			SnapshotID:      "snap-btc",
			ScannedAt:       now.Add(-3 * time.Hour),
			MaxConcurrent:   2,
			MaxPositionUSDT: 50,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "BTC-USDT", HighVenue: "binance", LowVenue: "okx", NetEV: 4.2, RankScore: 0.95, TradableNotional: 60},
				{Symbol: "BTC-USDT", HighVenue: "bybit", LowVenue: "okx", NetEV: 3.0, RankScore: 0.80, TradableNotional: 55},
			},
		},
		{
			SnapshotID:      "snap-eth",
			ScannedAt:       now.Add(-2 * time.Hour),
			MaxConcurrent:   2,
			MaxPositionUSDT: 50,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "ETH-USDT", HighVenue: "binance", LowVenue: "gate", NetEV: 2.5, RankScore: 0.90, TradableNotional: 45},
				{Symbol: "ETH-USDT", HighVenue: "bybit", LowVenue: "okx", NetEV: 2.2, RankScore: 0.70, TradableNotional: 45},
			},
		},
		{
			SnapshotID:      "snap-sol",
			ScannedAt:       now.Add(-60 * time.Minute),
			MaxConcurrent:   2,
			MaxPositionUSDT: 50,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "SOL-USDT", HighVenue: "gate", LowVenue: "okx", NetEV: 1.8, RankScore: 0.85, TradableNotional: 35},
				{Symbol: "XRP-USDT", HighVenue: "binance", LowVenue: "okx", NetEV: 1.4, RankScore: 0.80, TradableNotional: 30},
			},
		},
	}
	var scanLines []string
	for _, rec := range scans {
		data, _ := json.Marshal(rec)
		scanLines = append(scanLines, string(data))
	}
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(dir), crossVenueScanLogFileName), []byte(strings.Join(scanLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.ValidationSnapshot(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.TradeCount != 2 || math.Abs(got.PerpPerpRealizedNetPnL-7.5) > 1e-9 {
		t.Fatalf("snapshot=%+v", got)
	}
	if got.SwitchTradeCount != 1 || math.Abs(got.SwitchDrag-0.5) > 1e-9 {
		t.Fatalf("switch aggregation mismatch: %+v", got)
	}
	if math.Abs(got.FundingAlpha-2.0) > 1e-9 || math.Abs(got.ExecutionDrag-0.8) > 1e-9 || math.Abs(got.FreshnessDrag-4.2) > 1e-9 {
		t.Fatalf("attribution aggregation mismatch: %+v", got)
	}
	if got.FallbackTradeCount != 1 || math.Abs(got.FallbackNetPnL-5.5) > 1e-9 {
		t.Fatalf("fallback aggregation mismatch: %+v", got)
	}
	if got.ScanSnapshotCount != 3 || got.ReplayMatchedTrades != 2 {
		t.Fatalf("replay coverage mismatch: %+v", got)
	}
	if !got.ReplayReady || got.EntrySnapshotLinkedTrades != 2 || got.MissingEntrySnapshotTrades != 0 || got.ScanSnapshotTotalCount != 3 {
		t.Fatalf("replay readiness mismatch: %+v", got)
	}
	if !got.SameOpportunitySetReady || !got.SameCostModelReady || !got.SameRiskBudgetReady {
		t.Fatalf("validation readiness mismatch: %+v", got)
	}
	if math.Abs(got.SelectionDrag-1.2) > 1e-9 || math.Abs(got.FallbackDilution-1.2) > 1e-9 {
		t.Fatalf("selection drag mismatch: %+v", got)
	}
	if got.ReplayPortfolio.MatchedTrades != 2 || math.Abs(got.ReplayPortfolio.ChosenReplayNetEV-5.5) > 1e-9 || math.Abs(got.ReplayPortfolio.BaselineReplayNetEV-6.7) > 1e-9 {
		t.Fatalf("replay portfolio mismatch: %+v", got.ReplayPortfolio)
	}
	if len(got.ReplayPortfolio.Records) != 2 || got.ReplayPortfolio.Records[0].Symbol != "BTC-USDT" {
		t.Fatalf("replay portfolio records mismatch: %+v", got.ReplayPortfolio.Records)
	}
	if !got.CounterfactualReplay.SameOpportunitySetReady || !got.CounterfactualReplay.SameCostModelReady || !got.CounterfactualReplay.SameRiskBudgetReady {
		t.Fatalf("counterfactual replay readiness mismatch: %+v", got.CounterfactualReplay)
	}
	if got.CounterfactualReplay.MatchedTrades != 2 || got.CounterfactualReplay.BaselineTradeCount != 2 || got.CounterfactualReplay.MissingSnapshotTrades != 0 || got.CounterfactualReplay.MissingChosenTrades != 0 {
		t.Fatalf("counterfactual replay mismatch: %+v", got.CounterfactualReplay)
	}
	if len(got.CounterfactualReplay.Records) != 2 || got.CounterfactualReplay.Records[0].BaselinePair != "binance/okx" {
		t.Fatalf("counterfactual replay records mismatch: %+v", got.CounterfactualReplay.Records)
	}
	if !got.BaselinePortfolioReplay.SameOpportunitySetReady || !got.BaselinePortfolioReplay.SameCostModelReady || !got.BaselinePortfolioReplay.SameRiskBudgetReady {
		t.Fatalf("baseline portfolio replay readiness mismatch: %+v", got.BaselinePortfolioReplay)
	}
	if got.BaselinePortfolioReplay.SnapshotCount != 2 || got.BaselinePortfolioReplay.MatchedSnapshotCount != 2 || got.BaselinePortfolioReplay.MissingSnapshotTradeCount != 0 {
		t.Fatalf("baseline portfolio replay snapshot coverage mismatch: %+v", got.BaselinePortfolioReplay)
	}
	if got.BaselinePortfolioReplay.ChosenTradeCount != 2 || got.BaselinePortfolioReplay.BaselineTradeCount != 2 {
		t.Fatalf("baseline portfolio replay trade counts mismatch: %+v", got.BaselinePortfolioReplay)
	}
	if math.Abs(got.BaselinePortfolioReplay.ChosenReplayNetEV-5.5) > 1e-9 || math.Abs(got.BaselinePortfolioReplay.BaselineReplayNetEV-6.7) > 1e-9 {
		t.Fatalf("baseline portfolio replay ev mismatch: %+v", got.BaselinePortfolioReplay)
	}
	if len(got.BaselinePortfolioReplay.Records) != 2 {
		t.Fatalf("baseline portfolio replay records mismatch: %+v", got.BaselinePortfolioReplay.Records)
	}
	snapBTC := testBaselinePortfolioRecordBySnapshotID(t, got.BaselinePortfolioReplay.Records, "snap-btc")
	if len(snapBTC.BaselinePairs) != 1 || snapBTC.BaselinePairs[0] != "binance/okx" {
		t.Fatalf("baseline portfolio replay baseline pairs mismatch: %+v", snapBTC)
	}
	if !got.RollingBaselineReplay.SameOpportunitySetReady || !got.RollingBaselineReplay.SameCostModelReady || !got.RollingBaselineReplay.SameRiskBudgetReady {
		t.Fatalf("rolling baseline replay readiness mismatch: %+v", got.RollingBaselineReplay)
	}
	if got.RollingBaselineReplay.SnapshotCount != 3 || got.RollingBaselineReplay.ChosenSnapshotCount != 2 || got.RollingBaselineReplay.BaselineSnapshotCount != 3 || got.RollingBaselineReplay.MissedOpenSnapshots != 1 {
		t.Fatalf("rolling baseline replay coverage mismatch: %+v", got.RollingBaselineReplay)
	}
	if math.Abs(got.RollingBaselineReplay.ChosenReplayNetEV-5.5) > 1e-9 || math.Abs(got.RollingBaselineReplay.BaselineReplayNetEV-9.9) > 1e-9 {
		t.Fatalf("rolling baseline replay ev mismatch: %+v", got.RollingBaselineReplay)
	}
	if len(got.RollingBaselineReplay.Records) != 3 || got.RollingBaselineReplay.Records[2].SnapshotID != "snap-sol" {
		t.Fatalf("rolling baseline replay records mismatch: %+v", got.RollingBaselineReplay.Records)
	}
	if got.RollingBaselineReplay.Records[2].ChosenTradeCount != 0 || got.RollingBaselineReplay.Records[2].BaselineTradeCount != 2 {
		t.Fatalf("rolling baseline replay missed-open record mismatch: %+v", got.RollingBaselineReplay.Records[2])
	}
	if math.Abs(got.RollingBaselineReplay.Records[2].CumulativeBaselineReplayNetEV-9.9) > 1e-9 {
		t.Fatalf("rolling baseline replay cumulative mismatch: %+v", got.RollingBaselineReplay.Records[2])
	}
	if got.FreshnessSkipCycles != 1 || got.OpeningsDisabledCycles != 1 {
		t.Fatalf("cycle aggregation mismatch: %+v", got)
	}
	if len(got.NoTradeReasonsTop) == 0 || got.NoTradeReasonsTop[0].Reason != "execution<0.50" || got.NoTradeReasonsTop[0].Count != 3 {
		t.Fatalf("reason aggregation mismatch: %+v", got.NoTradeReasonsTop)
	}
}

func TestCrossVenueValidationSnapshotFallback(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	e := &CrossVenueExecutor{
		cfg:       testCrossVenueExecutorConfig(),
		store:     mustCrossVenueStore(t, dir),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	if err := os.MkdirAll(crossVenueRecordDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	openTime := now.Add(-2 * time.Hour)
	settlements := []CrossVenueSettlement{
		{
			SettlementID: "via-current", Symbol: "BTC-USDT", OpenTime: openTime, CloseTime: now.Add(-time.Hour),
			CurrentSnapshotID: "snap-btc", HighVenue: "bybit", LowVenue: "okx", NotionalUSDT: 50,
			NetPnL: 1, TotalFees: 0.2, OpenSlippage: -0.1, CloseSlippage: 0.05,
		},
		{
			SettlementID: "via-nearest", Symbol: "ETH-USDT", OpenTime: openTime, CloseTime: now.Add(-30 * time.Minute),
			HighVenue: "binance", LowVenue: "gate", NotionalUSDT: 40, NetPnL: 2,
		},
	}
	var settlementLines []string
	for _, rec := range settlements {
		data, _ := json.Marshal(rec)
		settlementLines = append(settlementLines, string(data))
	}
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName), []byte(strings.Join(settlementLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scans := []crossVenueScanLogRecord{
		{
			SnapshotID: "snap-btc", ScannedAt: openTime.Add(-10 * time.Minute), MaxConcurrent: 2,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "BTC-USDT", HighVenue: "bybit", LowVenue: "okx", NetEV: 3.0, RankScore: 0.9},
			},
		},
		{
			SnapshotID: "snap-eth", ScannedAt: openTime.Add(-5 * time.Minute), MaxConcurrent: 2,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "ETH-USDT", HighVenue: "binance", LowVenue: "gate", NetEV: 2.5, RankScore: 0.9},
			},
		},
	}
	var scanLines []string
	for _, rec := range scans {
		data, _ := json.Marshal(rec)
		scanLines = append(scanLines, string(data))
	}
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(dir), crossVenueScanLogFileName), []byte(strings.Join(scanLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.ValidationSnapshot(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.TradeCount != 2 || got.EntrySnapshotLinkedTrades != 2 || got.MissingEntrySnapshotTrades != 0 || !got.ReplayReady {
		t.Fatalf("fallback linkage mismatch: %+v", got)
	}
	if math.Abs(got.ExecutionDrag-0.25) > 1e-9 {
		t.Fatalf("execution drag should ignore negative slippage, got=%v", got.ExecutionDrag)
	}
	if got.ReplayMatchedTrades != 2 {
		t.Fatalf("replay matched mismatch: %+v", got)
	}
}

func TestCrossVenuePositionStoreUsesPositionKey(t *testing.T) {
	store := mustCrossVenueStore(t, t.TempDir())
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	p1 := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "cv-btc-bybit-1", OpenedAt: now, Status: CrossVenueStatusOpen,
	}
	p2 := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: gate.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "cv-btc-gate-2", OpenedAt: now.Add(time.Second), Status: CrossVenueStatusOpen,
	}
	if err := store.upsert(p1); err != nil {
		t.Fatal(err)
	}
	if err := store.upsert(p2); err != nil {
		t.Fatal(err)
	}
	ps, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("expected 2 positions, got %d: %+v", len(ps), ps)
	}
	if crossVenuePositionKey(ps[0]) == crossVenuePositionKey(ps[1]) {
		t.Fatalf("expected distinct position keys: %+v", ps)
	}
}

func TestCrossVenueOpenGateAllowsSwitchTargetWithExistingSymbol(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.book = tight
	low.book = tight
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	deps := testCrossVenueGateDeps(100)
	deps.fetchFunding = func(context.Context, venue.Venue, venue.Symbol) (venue.FundingSnapshot, bool) {
		return venue.FundingSnapshot{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(time.Hour)}, true
	}
	input := crossVenueGateInput{
		Opp: CrossVenueOpportunity{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Positions: []CrossVenuePosition{{
			Symbol: "BTC-USDT", HighVenue: gate.VenueName, LowVenue: okx.VenueName, Status: CrossVenueStatusOpen,
		}},
		Now: now,
	}
	blocked := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, deps, venues, input)
	if blocked.OK {
		t.Fatal("expected existing symbol to block without switch target allowance")
	}
	input.AllowExistingSymbol = true
	allowed := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, deps, venues, input)
	if !allowed.OK {
		t.Fatalf("expected switch target to pass existing symbol gate, got %+v", allowed)
	}
}

func TestCrossVenueSwitchCloseReasonWaitsForOpenThenCloseTarget(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.SwitchSequence = crossVenueSwitchSequenceOpenThenClose
	cfg.CrossVenueExecutor.MaxSwitchesPerCycle = 1
	dir := t.TempDir()
	e := &CrossVenueExecutor{
		cfg:       cfg,
		nowFn:     func() time.Time { return now },
		logger:    log.New(io.Discard, "", 0),
		store:     mustCrossVenueStore(t, dir),
		recordDir: dir,
		scanner: &CrossVenueScanner{
			lastSnapshot: CrossVenueScanSnapshot{
				SnapshotID: "snap-switch",
				ScannedAt:  now,
			},
		},
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		OpenedAt: now.Add(-48 * time.Hour), CurrentFutureEV: 1.0, EstimatedSwitchCost: 0.2,
		RecoveryQualityScore: 0.95, PendingSwitchSequence: crossVenueSwitchSequenceOpenThenClose,
		PendingSwitchHighVenue: gate.VenueName, PendingSwitchLowVenue: okx.VenueName,
		PendingSwitchReason: "wait target live", Status: CrossVenueStatusOpen,
		IdemPrefix: "cv-btc-old", CurrentComparableWindowHours: 1,
	}
	if reason := e.crossVenueSwitchCloseReason(context.Background(), &pos); reason != "" {
		t.Fatalf("expected open_then_close to wait for target, got %q", reason)
	}
	target := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: gate.VenueName, LowVenue: okx.VenueName,
		IdemPrefix: "cv-btc-new", OpenedAt: now, Status: CrossVenueStatusOpen,
	}
	e.positions = []CrossVenuePosition{pos, target}
	if reason := e.crossVenueSwitchCloseReason(context.Background(), &pos); reason == "" {
		t.Fatalf("expected close reason once target is live, got empty reason")
	}
}

func TestExecuteCrossVenueOpenStagesSecondLegUntilOldClosed(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     mustCrossVenueStore(t, dir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		TargetHighVenue:    bybit.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceStagedSwitch,
		PartialSwitchPhase: "pending",
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Hour),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	plan := crossVenueOpenPlan{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, ComparableWindowHours: 1, HighFundingRate: 0.001, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8, HighNextFundingTime: now.Add(time.Hour), LowNextFundingTime: now.Add(time.Hour),
		},
		Gate:            crossVenueGateResult{NotionalUSDT: 50, HighMid: 100, LowMid: 100, HighInstr: high.instruments[0], LowInstr: low.instruments[0]},
		Qty:             0.5,
		RefPrice:        100,
		IdemPrefix:      "stage-open",
		DecisionCycleID: "cycle-stage",
		SnapshotID:      "snap-stage",
		SwitchIntent:    &intent,
		SelectionMode:   crossVenueOpenStrict,
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if pos.PartialSwitchPhase != "staged_close_old" || pos.TransitionState != "staged" || pos.Status != CrossVenueStatusOpeningSecondLeg {
		t.Fatalf("expected staged wait state, got %+v", pos)
	}
	if got := atomic.LoadInt32(high.placeCount); got != 1 {
		t.Fatalf("expected high leg to be opened once, got %d", got)
	}
	if got := atomic.LoadInt32(low.placeCount); got != 0 {
		t.Fatalf("expected low leg to wait, got %d", got)
	}
	intents, err := e.loadSwitchIntents()
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].PartialSwitchPhase != "close_old" {
		t.Fatalf("expected intent to move into close_old, got %+v", intents)
	}
}

func TestManageCrossVenuePositionContinuesStagedSwitchAfterOldClose(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now.Add(time.Hour))
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now.Add(time.Hour))
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, EntryPrice: 100, MarkPrice: 100}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e := &CrossVenueExecutor{
		cfg:       cfg,
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     mustCrossVenueStore(t, dir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: dir,
	}
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		TargetHighVenue:    bybit.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceStagedSwitch,
		PartialSwitchPhase: "close_old",
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Hour),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	pos := CrossVenuePosition{
		Symbol:                "BTC-USDT",
		HighVenue:             bybit.VenueName,
		LowVenue:              okx.VenueName,
		Status:                CrossVenueStatusOpeningSecondLeg,
		HighLeg:               CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5, EntryPrice: 100},
		LowLeg:                CrossVenueLegState{Venue: okx.VenueName, Side: "buy"},
		OpenedAt:              now,
		UpdatedAt:             now,
		IdemPrefix:            "stage-target",
		PendingSwitchSequence: crossVenueSwitchSequenceStagedSwitch,
		PartialSwitchPhase:    "staged_close_old",
		TransitionState:       "staged",
	}
	got, _ := e.manageCrossVenuePosition(context.Background(), pos, true)
	if got.PartialSwitchPhase != "staged_close_old" || got.TransitionState != "staged" {
		t.Fatalf("expected staged wait to remain before old close, got %+v", got)
	}
	if got := atomic.LoadInt32(low.placeCount); got != 0 {
		t.Fatalf("expected no second-leg order before old close, got %d", got)
	}
	if err := e.updateSwitchIntentAttempt(intent.Symbol, intent.TargetHighVenue, intent.TargetLowVenue, "open_new", "old closed"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.manageCrossVenuePosition(context.Background(), got, true)
	if got.Status != CrossVenueStatusOpen || got.PartialSwitchPhase != "consumed" || got.TransitionState != "hold" {
		t.Fatalf("expected staged switch to finish second leg after old close, got %+v", got)
	}
	if got := atomic.LoadInt32(low.placeCount); got != 1 {
		t.Fatalf("expected one second-leg order after old close, got %d", got)
	}
}

func TestHandleCrossVenueValidationAPI(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	exec := &CrossVenueExecutor{
		cfg:       cfg,
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	if err := os.MkdirAll(crossVenueRecordDir(cfg.NetworkConfig.RecordDir), 0o755); err != nil {
		t.Fatal(err)
	}
	settlement := CrossVenueSettlement{
		SettlementID: "api", Symbol: "BTC-USDT", CloseTime: now.Add(-time.Hour),
		NetPnL: 3, RollbackCost: 0.2, RebalanceCost: 0.1, SwitchExit: true,
	}
	data, _ := json.Marshal(settlement)
	if err := os.WriteFile(filepath.Join(crossVenueRecordDir(cfg.NetworkConfig.RecordDir), crossVenueSettlementsFileName), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	dashboard := &DashboardServer{crossVenueExecutor: exec}
	req := httptest.NewRequest(http.MethodGet, "/api/cross-venue/validation?window_hours=24", nil)
	rec := httptest.NewRecorder()
	dashboard.handleCrossVenueValidationAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp CrossVenueValidationSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TradeCount != 1 || math.Abs(resp.BaselineApproxNetPnL-3.3) > 1e-9 {
		t.Fatalf("resp=%+v", resp)
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

// TestCrossVenueDryRunCloseNoLedgerWrite dry-run 平仓只记日志,不删台账、绝不下单。
func TestCrossVenueDryRunCloseNoLedgerWrite(t *testing.T) {
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
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "off",
	}
	_ = store.upsert(pos)
	if err := e.closeCrossVenuePosition(context.Background(), pos, false, "dry-run"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("dry-run 不得调 PlaceOrder")
	}
	ps, _ := store.load()
	if len(ps) != 1 {
		t.Fatalf("dry-run 平仓不得删真实台账, got %d", len(ps))
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

// newSubsetFallbackExecutor 组装一个带日志缓冲的OFF 执行器(bybit/okx 两所 fake,闸门全可过),
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
	cfg.CrossVenueExecutor.SlippageBudgetBps = 0
	cfg.CrossVenueExecutor.EVExpectedSlippageBps = 3
	e := &CrossVenueExecutor{
		cfg:            cfg,
		venues:         map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:          mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:         log.New(buf, "", 0),
		nowFn:          func() time.Time { return time.Now().UTC() },
		symbolCooldown: map[string]time.Time{},
		scanner:        scanner,
		recordDir:      cfg.NetworkConfig.RecordDir,
	}
	return e, high, low
}

func makeTestOpenOpportunity(cfg CrossVenueExecutorConfig, now time.Time, symbol, highVenue, lowVenue string, highRate, lowRate float64, tradableNotional float64, history, liquidity, stability, execution float64) CrossVenueOpportunity {
	opp := CrossVenueOpportunity{
		Symbol:              symbol,
		HighVenue:           highVenue,
		LowVenue:            lowVenue,
		SpreadAPR:           2.0,
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   8,
		LowIntervalHours:    8,
		HighNextFundingTime: now.Add(4 * time.Hour),
		LowNextFundingTime:  now.Add(4 * time.Hour),
		TradableNotional:    tradableNotional,
		HistoryQualityScore: history,
		LiquidityScore:      liquidity,
		StabilityScore:      stability,
		ExecutionScore:      execution,
	}
	opp = crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	opp.HistoryQualityScore = history
	opp.LiquidityScore = liquidity
	opp.StabilityScore = stability
	opp.ExecutionScore = execution
	ranked := []CrossVenueOpportunity{opp}
	crossVenueFinalizeOpportunityRanking(cfg, ranked)
	return ranked[0]
}

// TestCrossVenueSubsetFallbackOpensOff 全局最优含不可交易所、可交易子集内有达标对 → 降级评估,
// OFF 模式走到开仓决策(且绝不下单)。
func TestCrossVenueSubsetFallbackOpensOff(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: nonTradeableVenueName, LowVenue: okx.VenueName, SpreadAPR: 25,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			// 新版按 NetEV 决策，不只看 spreadAPR；这里用足够强的 funding 差确保子集替代对为正 EV。
			bybit.VenueName: {"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: nf}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, high, low := newSubsetFallbackExecutor(t, &buf, scanner)
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if !strings.Contains(logs, "降级用子集对") {
		t.Fatalf("应打降级日志, got:\n%s", logs)
	}
	if !strings.Contains(logs, "[dry-run] 本应开仓 跨所perp-perp BTC-USDT") {
		t.Fatalf("降级后应走到 dry-run 开仓决策, got:\n%s", logs)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("OFF 模式不得调 PlaceOrder")
	}
}

// TestCrossVenueSubsetFallbackBelowThresholdRejected 全局最优含不可交易所、子集对净收益≤0 → 拒绝。
func TestCrossVenueSubsetFallbackBelowThresholdRejected(t *testing.T) {
	nf := time.Now().UTC().Add(time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: nonTradeableVenueName, LowVenue: okx.VenueName, SpreadAPR: 25,
		}},
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			// 两所费率几乎相等 → 子集净收益 ≤0。
			bybit.VenueName: {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.00009, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, high, low := newSubsetFallbackExecutor(t, &buf, scanner)
	cfg := e.cfg
	cfg.CrossVenueExecutor.OpenSpreadAPR = 0
	e.cfg = cfg
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if !strings.Contains(logs, "EV<0") && !strings.Contains(logs, "netProfit") {
		t.Fatalf("拒绝原因应含净收益闸, got:\n%s", logs)
	}
	if strings.Contains(logs, "[dry-run] 本应开仓") {
		t.Fatalf("不达标不应走到开仓决策, got:\n%s", logs)
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("不得下单")
	}
}

// TestCrossVenueTradeablePairNoFallback 全局最优本就可交易 → 不触发降级路径,行为不变。
func TestCrossVenueTradeablePairNoFallback(t *testing.T) {
	now := time.Now().UTC()
	nf := now.Add(2 * time.Hour)
	scanner := &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
		Opportunities: []CrossVenueOpportunity{{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0,
		}},
		// 可交易对必须直接使用原机会,不应触发「含不可交易所才会走」的子集降级路径。
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {"BTC-USDT": {Rate: 0.008, IntervalHours: 1, NextFundingTime: nf}},
			okx.VenueName:   {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
		},
	}}
	var buf bytes.Buffer
	e, _, _ := newSubsetFallbackExecutor(t, &buf, scanner)
	e.nowFn = func() time.Time { return now }
	e.evaluateNewOpens(context.Background(), false)
	logs := buf.String()
	if strings.Contains(logs, "降级用子集对") || strings.Contains(logs, "不可交易所") {
		t.Fatalf("可交易对不应触发降级路径, got:\n%s", logs)
	}
	if !strings.Contains(logs, "[dry-run] 本应开仓 跨所perp-perp BTC-USDT") {
		t.Fatalf("原机会应正常走闸门到 dry-run 开仓, got:\n%s", logs)
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
	if strings.Contains(logs, "[dry-run] 本应开仓") {
		t.Fatal("不应走到开仓决策")
	}
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("不得下单")
	}
	// 纯函数口径再钉一次:bestTradeableSubsetPair 必须返回 not-ok。
	if _, ok := bestTradeableSubsetPair("BTC-USDT", scanner.lastSnapshot.FundingByVenue, e.cfg.CrossVenueExecutor); ok {
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
	now := time.Now().UTC()
	nf := now.Add(2 * time.Hour)
	opp := CrossVenueOpportunity{
		Symbol:                "BTC-USDT",
		HighVenue:             binance.VenueName,
		LowVenue:              okx.VenueName,
		SnapshotID:            "snap-binance-okx",
		SnapshotTime:          now,
		FreshnessAgeSeconds:   0,
		SpreadAPR:             25,
		ComparableWindowHours: 2,
		ExpectedHoldHours:     2,
		NetEV:                 5,
		CapitalEfficiencyEV:   1,
		RiskAdjustedEVPerHour: 1,
		HighFundingRate:       0.002,
		LowFundingRate:        0.0001,
		HighIntervalHours:     1,
		LowIntervalHours:      8,
		HighNextFundingTime:   nf,
		LowNextFundingTime:    nf,
		HistoryQualityScore:   0.80,
		LiquidityScore:        0.80,
		StabilityScore:        0.80,
		ExecutionScore:        0.90,
		RecoveryQualityScore:  0.60,
		RankScore:             95,
		SizeMultiplier:        1,
		TradableNotional:      20,
	}
	cfg := testCrossVenueExecutorConfig()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	got, reject := e.resolveTradeableOpportunity(opp, map[string]map[string]CrossVenueFundingEntry{
		binance.VenueName: {"BTC-USDT": {Rate: 0.002, IntervalHours: 1, NextFundingTime: nf}},
		okx.VenueName:     {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: nf}},
	})
	if reject != "" {
		t.Fatalf("binance/okx 可交易机会不应被降级拒绝: %s", reject)
	}
	if got.HighVenue != binance.VenueName || got.LowVenue != okx.VenueName {
		t.Fatalf("binance/okx 可交易机会不应被替换, got=%+v", got)
	}
}

func TestCrossVenueAcceptableFallbackOpensOff(t *testing.T) {
	now := time.Now().UTC()
	opp := CrossVenueOpportunity{
		Symbol:                "BTC-USDT",
		HighVenue:             bybit.VenueName,
		LowVenue:              okx.VenueName,
		SnapshotID:            "snap-fallback",
		SnapshotTime:          now,
		FreshnessAgeSeconds:   0,
		SpreadAPR:             2.0,
		ComparableWindowHours: 4,
		ExpectedHoldHours:     4,
		NetEV:                 2,
		CapitalEfficiencyEV:   0.5,
		RiskAdjustedEVPerHour: 0.5,
		HighFundingRate:       0.05,
		LowFundingRate:        0.0,
		HighIntervalHours:     8,
		LowIntervalHours:      8,
		HighNextFundingTime:   now.Add(4 * time.Hour),
		LowNextFundingTime:    now.Add(4 * time.Hour),
		HistoryQualityScore:   0.45,
		LiquidityScore:        0.45,
		StabilityScore:        0.45,
		ExecutionScore:        0.70,
		RecoveryQualityScore:  0.65,
		RankScore:             70,
		SizeMultiplier:        1,
		TradableNotional:      20,
	}
	mode, reject := crossVenueSelectOpenMode(opp, false)
	if reject != "" || mode != crossVenueOpenFallback {
		t.Fatalf("expected acceptable fallback, mode=%s reject=%q opp=%+v", mode, reject, opp)
	}
	got := crossVenueApplyAcceptableFallback(opp)
	if got.TradableNotional != 10 {
		t.Fatalf("fallback should cap notional to 10U, got %+v", got)
	}
	if got.NetEV != 1 {
		t.Fatalf("fallback should scale NetEV to 1U, got %.4f", got.NetEV)
	}
	if got.RankScore != got.NetEV {
		t.Fatalf("RankScore=%.4f NetEV=%.4f want equal after fallback shrink", got.RankScore, got.NetEV)
	}
	if got.NoTradeReason != "acceptable_fallback" {
		t.Fatalf("fallback should mark no-trade reason, got %+v", got)
	}
}

func TestCrossVenueStrictCandidateBlocksFallback(t *testing.T) {
	now := time.Now().UTC()
	strict := CrossVenueOpportunity{
		Symbol:                "BTC-USDT",
		HighVenue:             bybit.VenueName,
		LowVenue:              okx.VenueName,
		SnapshotID:            "snap-strict",
		SnapshotTime:          now,
		FreshnessAgeSeconds:   0,
		SpreadAPR:             2.0,
		ComparableWindowHours: 4,
		ExpectedHoldHours:     4,
		NetEV:                 3,
		CapitalEfficiencyEV:   0.7,
		RiskAdjustedEVPerHour: 0.7,
		HighFundingRate:       0.05,
		LowFundingRate:        0.0,
		HighIntervalHours:     8,
		LowIntervalHours:      8,
		HighNextFundingTime:   now.Add(4 * time.Hour),
		LowNextFundingTime:    now.Add(4 * time.Hour),
		HistoryQualityScore:   0.80,
		LiquidityScore:        0.80,
		StabilityScore:        0.80,
		ExecutionScore:        0.90,
		RecoveryQualityScore:  0.65,
		RankScore:             95,
		SizeMultiplier:        1,
		TradableNotional:      20,
	}
	fallback := CrossVenueOpportunity{
		Symbol:                "ETH-USDT",
		HighVenue:             bybit.VenueName,
		LowVenue:              okx.VenueName,
		SnapshotID:            "snap-strict",
		SnapshotTime:          now,
		FreshnessAgeSeconds:   0,
		SpreadAPR:             2.0,
		ComparableWindowHours: 4,
		ExpectedHoldHours:     4,
		NetEV:                 2,
		CapitalEfficiencyEV:   0.5,
		RiskAdjustedEVPerHour: 0.5,
		HighFundingRate:       0.05,
		LowFundingRate:        0.0,
		HighIntervalHours:     8,
		LowIntervalHours:      8,
		HighNextFundingTime:   now.Add(4 * time.Hour),
		LowNextFundingTime:    now.Add(4 * time.Hour),
		HistoryQualityScore:   0.45,
		LiquidityScore:        0.45,
		StabilityScore:        0.45,
		ExecutionScore:        0.70,
		RecoveryQualityScore:  0.65,
		RankScore:             70,
		SizeMultiplier:        1,
		TradableNotional:      20,
	}
	if mode, reject := crossVenueSelectOpenMode(strict, false); mode != crossVenueOpenStrict || reject != "" {
		t.Fatalf("strict candidate should remain strict, mode=%s reject=%q opp=%+v", mode, reject, strict)
	}
	mode, reject := crossVenueSelectOpenMode(fallback, true)
	if mode != crossVenueOpenReject {
		t.Fatalf("fallback should be blocked when strict exists, mode=%s reject=%q opp=%+v", mode, reject, fallback)
	}
	if reject != "存在更高质量 strict 候选,跳过软补位" {
		t.Fatalf("unexpected fallback rejection reason: %q", reject)
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

func TestCrossVenueSwitchIntentOpenRetryAfterRollback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.makerPostOnlyNeverFill = true
	low.takerAlwaysFail = true
	e := newCrossVenueMakerOpenExecutor(t, high, low)
	intent := crossVenueSwitchIntent{
		Symbol:             "BTC-USDT",
		FromHighVenue:      bybit.VenueName,
		FromLowVenue:       gate.VenueName,
		TargetHighVenue:    bybit.VenueName,
		TargetLowVenue:     okx.VenueName,
		SwitchSequence:     crossVenueSwitchSequenceCloseThenOpen,
		PartialSwitchPhase: "open_new",
		Reason:             "switch target open retry",
		CreatedAt:          now,
		ExpiresAt:          now.Add(30 * time.Minute),
	}
	if err := e.upsertSwitchIntent(intent); err != nil {
		t.Fatal(err)
	}
	plan := testCrossVenueOpenPlan()
	plan.SwitchIntent = &intent
	_, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err == nil {
		t.Fatal("expected rollback path")
	}
	intents := e.activeSwitchIntents(now)
	if len(intents) != 1 {
		t.Fatalf("expected switch intent retained for retry, got %+v", intents)
	}
	if intents[0].PartialSwitchPhase != "open_new_retry" {
		t.Fatalf("expected switch intent retry phase, got %+v", intents[0])
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
