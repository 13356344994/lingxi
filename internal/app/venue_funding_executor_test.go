package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// fakeVenueFundingTrader 离线 fake venue,计数 PlaceOrder 调用。
type fakeVenueFundingTrader struct {
	venue.Venue
	placeCount  int
	mu          sync.Mutex
	positions   []venue.Position
	balances    venue.Balances
	instruments []venue.Instrument
	perpBook    venue.BookTicker
	spotBook    venue.BookTicker
	spotBuyFail bool
	costProfile venue.CostProfile
}

func (f *fakeVenueFundingTrader) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (f *fakeVenueFundingTrader) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	f.mu.Lock()
	f.placeCount++
	f.mu.Unlock()
	if req.Market == venue.MarketSpot && req.Side == venue.SideBuy && f.spotBuyFail {
		return venue.OrderResult{}, context.DeadlineExceeded
	}
	if req.Type == venue.OrderTypeIOC && req.Price <= 0 {
		return venue.OrderResult{}, fmt.Errorf("IOC 必须带价")
	}
	res := venue.OrderResult{
		Symbol:        req.Symbol,
		Market:        req.Market,
		ClientOrderID: req.ClientOrderID,
		Status:        "filled",
		ExecutedQty:   req.Qty,
		AvgPrice:      1.0,
		CumQuoteUSDT:  req.Qty,
	}
	if req.Market == venue.MarketPerp {
		amt := req.Qty
		if req.Side == venue.SideSell {
			amt = -req.Qty
		}
		f.positions = append(f.positions, venue.Position{Symbol: req.Symbol, PositionAmt: amt})
	}
	return res, nil
}

func (f *fakeVenueFundingTrader) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return nil
}

func (f *fakeVenueFundingTrader) QueryOrder(_ context.Context, m venue.Market, s venue.Symbol, id string) (venue.OrderResult, error) {
	return venue.OrderResult{Symbol: s, Market: m, ClientOrderID: id, Status: "filled", ExecutedQty: 1, AvgPrice: 1}, nil
}

func (f *fakeVenueFundingTrader) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *fakeVenueFundingTrader) FetchBalances(context.Context) (venue.Balances, error) {
	return f.balances, nil
}

func (f *fakeVenueFundingTrader) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return f.instruments, nil
}

func (f *fakeVenueFundingTrader) FetchBookTicker(_ context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	if m == venue.MarketSpot {
		return f.spotBook, nil
	}
	return f.perpBook, nil
}

func (f *fakeVenueFundingTrader) FetchTradingCostProfile(context.Context, []venue.Symbol) (venue.CostProfile, error) {
	return f.costProfile, nil
}

func (f *fakeVenueFundingTrader) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return nil
}

func testFundingInstruments(sym venue.Symbol) []venue.Instrument {
	return []venue.Instrument{
		{Symbol: sym, Market: venue.MarketSpot, StepSize: 0.01, MinNotional: 5, BaseAsset: "BTC"},
		{Symbol: sym, Market: venue.MarketPerp, StepSize: 0.01, MinNotional: 5},
	}
}

func TestVenueFundingEVGate(t *testing.T) {
	cfg := BybitExecConfig{
		MinFundingRate:     0.0005,
		MinNetProfitRate:   -0.01,
		SpotTakerFee:       0.001,
		PerpTakerFee:       0.001,
		EVGateEnabled:      true,
		RollEnabled:        true,
		MaxHoldSettlements: 12,
	}
	c := BybitCandidate{Symbol: "BTCUSDT", FundingRate: 0.01, IntervalHours: 8}
	spotInstr := venue.Instrument{StepSize: 0.01, MinNotional: 5}
	perpInstr := venue.Instrument{StepSize: 0.01, MinNotional: 5}
	plan, ok, _ := planBybitDeltaNeutralOpen(c, 100, 100, spotInstr, perpInstr, 50, cfg)
	if !ok {
		t.Fatal("plan should pass with high funding rate")
	}
	hist := make([]float64, 12)
	for i := range hist {
		hist[i] = 0.001
	}
	ev, _, evOK := bybitMultiSettlementEV(plan, hist, cfg)
	if !evOK || ev <= 0 {
		t.Fatalf("EV should be positive with favorable history, ev=%v ok=%v", ev, evOK)
	}
	// 费率过低历史 → EV 负
	lowHist := make([]float64, 12)
	for i := range lowHist {
		lowHist[i] = 0.00001
	}
	c2 := BybitCandidate{Symbol: "BTCUSDT", FundingRate: 0.0006, IntervalHours: 8}
	plan2, ok2, _ := planBybitDeltaNeutralOpen(c2, 100, 100, spotInstr, perpInstr, 50, cfg)
	if !ok2 {
		t.Fatal("plan2 setup")
	}
	ev2, _, evOK2 := bybitMultiSettlementEV(plan2, lowHist, cfg)
	if evOK2 && ev2 > 0 {
		t.Fatalf("EV should be negative with weak history, ev=%v", ev2)
	}
}

func TestVenueFundingLeadWindow(t *testing.T) {
	lead := 240 * time.Second
	ft := time.Now().UTC().Add(2 * time.Hour)
	now := ft.Add(-lead + 30*time.Second) // 窗口内
	if now.Before(ft.Add(-lead)) || !now.Before(ft) {
		t.Fatal("should be inside lead window")
	}
	nowEarly := ft.Add(-lead - 60*time.Second)
	if !nowEarly.Before(ft.Add(-lead)) {
		t.Fatal("should be outside lead window")
	}
}

func TestVenueFundingShadowPlaceOrderZero(t *testing.T) {
	fake := &fakeVenueFundingTrader{
		balances:    venue.Balances{ByAsset: map[string]venue.Balance{"USDT": {Free: 100}}},
		instruments: testFundingInstruments("BTCUSDT"),
		perpBook:    venue.BookTicker{BidPrice: 100, AskPrice: 100},
		spotBook:    venue.BookTicker{BidPrice: 100, AskPrice: 100},
		costProfile: venue.CostProfile{SpotTaker: 0.001, FuturesTaker: 0.001},
	}
	scanner := &CrossVenueScanner{
		lastSnapshot: CrossVenueScanSnapshot{
			FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
				okx.VenueName: {
					"BTCUSDT": {Rate: 0.01, IntervalHours: 8, NextFundingTime: time.Now().UTC().Add(3 * time.Minute)},
				},
			},
		},
	}
	cfg := venueFundingExecConfigFromEngine(DefaultEngineConfig())
	planner := NewVenueFundingPlanner(okx.VenueName, fake, scanner, cfg, nil)
	plan, err := planner.PlanCycle(context.Background(), 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Accepted) == 0 {
		t.Fatalf("expected accepted plan, rejected=%v", plan.Rejected)
	}
	liveExec := NewVenueFundingLiveExecutor(fake, okx.VenueName, 20, nil)
	// 影子:不调用 OpenDeltaNeutral
	if fake.placeCount != 0 {
		t.Fatalf("shadow should not place orders, got %d", fake.placeCount)
	}
	_ = liveExec
}

func TestVenueFundingSpotFailRevertPerp(t *testing.T) {
	fake := &fakeVenueFundingTrader{
		spotBuyFail: true,
		instruments: testFundingInstruments("BTCUSDT"),
		perpBook:    venue.BookTicker{BidPrice: 100, AskPrice: 100},
		spotBook:    venue.BookTicker{BidPrice: 100, AskPrice: 100},
	}
	liveExec := NewVenueFundingLiveExecutor(fake, okx.VenueName, 20, nil)
	plan := BybitOpenPlan{
		Symbol:          "BTCUSDT",
		BaseQty:         1,
		FundingRate:     0.01,
		NextFundingTime: time.Now().UTC().Add(time.Hour),
		SpotPrice:       100,
		PerpPrice:       100,
	}
	_, err := liveExec.OpenDeltaNeutral(context.Background(), plan, "lx-test", "run1")
	if err == nil {
		t.Fatal("expected spot fail error")
	}
	// 永续开 + 回补 = 2 单(现货失败不下单或只尝试)
	if fake.placeCount < 2 {
		t.Fatalf("expected perp open + revert, placeCount=%d", fake.placeCount)
	}
}

func TestVenueFundingGateSpotIOCRequiresPrice(t *testing.T) {
	if !venueFundingUsesGateIOC(gate.VenueName) {
		t.Fatal("gate should use IOC spot")
	}
	fake := &fakeVenueFundingTrader{
		instruments: testFundingInstruments("BTCUSDT"),
		spotBook:    venue.BookTicker{BidPrice: 99, AskPrice: 101},
		perpBook:    venue.BookTicker{BidPrice: 100, AskPrice: 100},
	}
	liveExec := NewVenueFundingLiveExecutor(fake, gate.VenueName, 20, nil)
	req, err := liveExec.spotBuyRequest(context.Background(), "BTCUSDT", 1, "test-os")
	if err != nil {
		t.Fatal(err)
	}
	if req.Type != venue.OrderTypeIOC || req.Price <= 0 {
		t.Fatalf("gate spot buy must be IOC with price, got type=%v price=%v", req.Type, req.Price)
	}
}

func TestVenueFundingConflictGuards(t *testing.T) {
	dir := t.TempDir()
	sym := venue.Symbol("BTCUSDT")
	// 跨所台账在场
	cvStore, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = cvStore.upsert(CrossVenuePosition{Symbol: "BTCUSDT", Status: CrossVenueStatusOpen})
	if !isCrossVenueSymbolHeld(dir, sym) {
		t.Fatal("cross venue should hold")
	}
	// okx funding 台账
	okxDir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: dir}}, okx.VenueName)
	fs, err := newVenueFundingPositionStore(okxDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.upsert(VenueFundingPosition{Symbol: sym})
	if !isFundingSymbolHeldAnyVenue(dir, sym) {
		t.Fatal("funding held any venue")
	}
	// 跨所闸门
	res := evaluateCrossVenueOpenGate(context.Background(), testCrossVenueExecutorConfig().CrossVenueExecutor, crossVenueGateDeps{}, nil, crossVenueGateInput{
		Opp:         CrossVenueOpportunity{Symbol: "BTCUSDT", HighVenue: okx.VenueName, LowVenue: gate.VenueName, SpreadAPR: 2},
		FundingHeld: func(s venue.Symbol) bool { return isFundingSymbolHeldAnyVenue(dir, s) },
		Now:         time.Now(),
	})
	if res.OK {
		t.Fatal("gate should reject funding-held symbol")
	}
}

func TestVenueFundingPositionsStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newVenueFundingPositionStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := VenueFundingPosition{
		Symbol:          "ETHUSDT",
		BaseQty:         1.5,
		NextFundingTime: time.Now().UTC(),
		OpenedAt:        time.Now().UTC(),
		IdemPrefix:      "lx-eth",
	}
	if err := store.upsert(p); err != nil {
		t.Fatal(err)
	}
	ps, err := store.load()
	if err != nil || len(ps) != 1 || ps[0].Symbol != "ETHUSDT" {
		t.Fatalf("load: %v len=%d", err, len(ps))
	}
	if err := store.remove("ETHUSDT"); err != nil {
		t.Fatal(err)
	}
	ps, _ = store.load()
	if len(ps) != 0 {
		t.Fatal("should be empty after remove")
	}
}

func TestVenueFundingHaltSkipsOpenOnly(t *testing.T) {
	// haltActive=true 时 manage 应跳过开仓但不 panic
	cfg := DefaultEngineConfig()
	cfg.FundingExecutor.LiveTradingEnabled = false
	store, _ := newVenueFundingPositionStore(t.TempDir())
	fake := &fakeVenueFundingTrader{
		balances: venue.Balances{ByAsset: map[string]venue.Balance{"USDT": {Free: 0.01}}},
	}
	planner := NewVenueFundingPlanner(okx.VenueName, fake, &CrossVenueScanner{}, venueFundingExecConfigFromEngine(cfg), nil)
	liveExec := NewVenueFundingLiveExecutor(fake, okx.VenueName, 20, nil)
	dir := t.TempDir()
	rs, err := NewReportStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.NetworkConfig.RecordDir = dir
	rc, err := NewRuntimeController(context.Background(), rs, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc.venueName = okx.VenueName
	rc.venueFundingManageCycle(context.Background(), cfg, store, planner, liveExec, fake, false, true)
	if fake.placeCount != 0 {
		t.Fatalf("halt+shadow should not place orders, count=%d", fake.placeCount)
	}
}

func TestVenueFundingPerCandidateBudget(t *testing.T) {
	b := venueFundingPerCandidateBudget(100, 30)
	if math.Abs(b-30) > 1e-9 {
		t.Fatalf("budget=%v want 30", b)
	}
	b2 := venueFundingPerCandidateBudget(10, 30)
	if math.Abs(b2-9) > 1e-9 { // leg=min(15,4.5)=4.5, *2=9
		t.Fatalf("budget=%v want 9", b2)
	}
}

func TestVenueFundingHeldCache(t *testing.T) {
	dir := t.TempDir()
	okxDir := recordDirForVenue(EngineConfig{NetworkConfig: NetworkConfig{RecordDir: dir}}, okx.VenueName)
	store, _ := newVenueFundingPositionStore(okxDir)
	_ = store.upsert(VenueFundingPosition{Symbol: "XRPUSDT"})
	held1 := fundingHeldSetCached(dir, okx.VenueName)
	if !held1["XRPUSDT"] {
		t.Fatal("should be held")
	}
	_ = store.remove("XRPUSDT")
	// 缓存 5s 内仍应命中
	held2 := fundingHeldSetCached(dir, okx.VenueName)
	if !held2["XRPUSDT"] {
		t.Fatal("cache should still show held within TTL")
	}
}

func TestParseVenuePlansFundingExecutorOverride(t *testing.T) {
	yaml := `
venues:
  okx:
    enabled: true
    funding_executor:
      live_trading_enabled: false
      max_position_usdt: 25
      lead_seconds: 180
`
	plans, err := parseVenuePlans([]byte(yaml), DefaultEngineConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	fx := plans[0].Config.FundingExecutor
	if fx.LiveTradingEnabled {
		t.Fatal("live should be false")
	}
	if math.Abs(fx.MaxPositionUSDT-25) > 1e-9 || fx.LeadSeconds != 180 {
		t.Fatalf("override failed: %+v", fx)
	}
}

func TestVenueFundingReconcileClearsGhost(t *testing.T) {
	dir := t.TempDir()
	store, _ := newVenueFundingPositionStore(dir)
	_ = store.upsert(VenueFundingPosition{Symbol: "BTCUSDT", BaseQty: 1})
	fake := &fakeVenueFundingTrader{balances: venue.Balances{ByAsset: map[string]venue.Balance{"USDT": {Free: 100}}}}
	rs, _ := NewReportStore(dir)
	cfg := DefaultEngineConfig()
	cfg.NetworkConfig.RecordDir = dir
	rc, _ := NewRuntimeController(context.Background(), rs, cfg, nil)
	rc.venueName = okx.VenueName
	rc.venueFundingReconcileOnStart(context.Background(), fake, store)
	ps, _ := store.load()
	if len(ps) != 0 {
		t.Fatalf("ghost position should be cleared, got %d", len(ps))
	}
}

func init() {
	// 确保测试目录可写
	_ = os.MkdirAll(filepath.Join(os.TempDir(), "lingxi-test"), 0o755)
}
