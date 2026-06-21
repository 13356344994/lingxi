package app

import (
	"context"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func testPerpPerpUnifiedOpportunity(id, symbol, highVenue, lowVenue string, notional float64) UnifiedOpportunity {
	return UnifiedOpportunity{
		ID:           id,
		StrategyType: UnifiedStrategyPerpPerp,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: highVenue, Market: "perp", Side: "short", Notional: notional, Qty: notional / 100},
			{Venue: lowVenue, Market: "perp", Side: "long", Notional: notional, Qty: notional / 100},
		},
		ExpectedFundingUSDT:   10,
		ExpectedOpenCostUSDT:  1,
		ExpectedCloseCostUSDT: 1,
		ExpectedNetEVUSDT:     8,
		ExpectedHoldHours:     8,
	}
}

func newCrossVenueRotationTestExecutor(t *testing.T, high, low *crossVenueTestVenue) *CrossVenueExecutor {
	t.Helper()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.OpenSpreadAPR = 0.5
	cfg.CrossVenueExecutor.MaxBookSpreadBps = 250
	tightBook := venue.BookTicker{BidPrice: 99.9, AskPrice: 100.1, BidQty: 1000, AskQty: 1000}
	high.book = tightBook
	low.book = tightBook
	cvOpp := makeTestOpenOpportunity(cfg.CrossVenueExecutor, now, "BTC-USDT", high.name, low.name, 0.05, 0.0, 50, 0.9, 0.9, 0.9, 0.9)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{high.name: high, low.name: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			high.name: {
				"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: now.Add(2 * time.Hour)},
			},
			low.name: {
				"BTC-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: now.Add(2 * time.Hour)},
			},
		}),
		symbolCooldown: map[string]time.Time{},
	}
	e.scanner.lastSnapshot.ScannedAt = now
	e.scanner.lastSnapshot.SnapshotID = "snap-rot"
	e.scanner.lastSnapshot.Opportunities = []CrossVenueOpportunity{cvOpp}
	return e
}

func TestBindCrossVenuePerpPerpRotationExecutorSmoke(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	e.SetRotationManaged(true)
	b := bindCrossVenuePerpPerpRotationExecutor(e)
	if b == nil {
		t.Fatal("binding nil")
	}
	if b.strategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("strategy=%s", b.strategyType)
	}
	for _, fn := range []struct {
		name string
		ok   bool
	}{
		{"openFn", b.openFn != nil},
		{"closeVerifiedFn", b.closeVerifiedFn != nil},
		{"realHeldCountFn", b.realHeldCountFn != nil},
		{"heldFn", b.heldFn != nil},
		{"reconcileFn", b.reconcileFn != nil},
		{"manageFn", b.manageFn != nil},
	} {
		if !fn.ok {
			t.Fatalf("missing %s", fn.name)
		}
	}
}

func TestCollectCrossVenueHeldRecordsMapping(t *testing.T) {
	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	oppID := unifiedOpportunityID(UnifiedStrategyPerpPerp, "BTC-USDT", "snap-old", bybit.VenueName, okx.VenueName)
	ps := []CrossVenuePosition{{
		Symbol:          "BTC-USDT",
		HighVenue:       bybit.VenueName,
		LowVenue:        okx.VenueName,
		EntrySnapshotID: "snap-old",
		NotionalUSDT:    100,
		EstFundingUSDT:  5,
		Status:          CrossVenueStatusOpen,
		OpenedAt:        time.Now().UTC(),
	}}
	recs := collectCrossVenueHeldRecords(ps, execCfg)
	if len(recs) != 1 {
		t.Fatalf("records=%d", len(recs))
	}
	rec := recs[0]
	if rec.OpportunityID != oppID {
		t.Fatalf("oppID=%q want %q", rec.OpportunityID, oppID)
	}
	if rec.StrategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("strategy=%s", rec.StrategyType)
	}
	wantMargin := rotationPerpMarginWithBuffer(execCfg, 100)
	if rec.CapitalRequiredByVenue[bybit.VenueName] != wantMargin || rec.CapitalRequiredByVenue[okx.VenueName] != wantMargin {
		t.Fatalf("capital=%v want %v each", rec.CapitalRequiredByVenue, wantMargin)
	}
	if rec.ExpectedBorrowCostUSDT != 0 {
		t.Fatalf("borrow=%v want 0", rec.ExpectedBorrowCostUSDT)
	}
	if len(rec.delistLegs) != 2 || rec.delistLegs[0].Market != venue.MarketPerp || rec.delistLegs[1].Market != venue.MarketPerp {
		t.Fatalf("delistLegs=%+v", rec.delistLegs)
	}
}

func TestCrossVenuePositionOpportunityIDFallback(t *testing.T) {
	pos := CrossVenuePosition{
		Symbol:          "ETH-USDT",
		HighVenue:       binance.VenueName,
		LowVenue:        bybit.VenueName,
		EntrySnapshotID: "legacy-snap",
	}
	want := unifiedOpportunityID(UnifiedStrategyPerpPerp, "ETH-USDT", "legacy-snap", binance.VenueName, bybit.VenueName)
	if got := crossVenuePositionOpportunityID(pos); got != want {
		t.Fatalf("fallback=%q want %q", got, want)
	}
	pos.OpportunityID = "explicit-id"
	if got := crossVenuePositionOpportunityID(pos); got != "explicit-id" {
		t.Fatalf("explicit=%q", got)
	}
}

func TestCrossVenueExecuteOpenFromUnifiedOpportunityOff(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	e.SetRotationManaged(true)
	opp := UnifiedOpportunityFromCrossVenue(e.cfg.CrossVenueExecutor, e.scanner.Snapshot().Opportunities[0], nil, 50, 100)
	pos, err := e.executeOpen(context.Background(), opp, false)
	if err != nil {
		t.Fatalf("dry-run executeOpen: %v", err)
	}
	if pos.Symbol != "" {
		t.Fatalf("dry-run must not persist synthetic position, got %+v", pos)
	}
	if atomic.LoadInt32(high.placeCount) != 0 {
		t.Fatal("dry-run must not place orders")
	}
}

func TestCrossVenueExecuteOpenFromUnifiedOpportunityLiveWritesOpportunityID(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	e.SetRotationManaged(true)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	opp := UnifiedOpportunityFromCrossVenue(e.cfg.CrossVenueExecutor, e.scanner.Snapshot().Opportunities[0], nil, 50, 100)
	pos, err := e.executeOpen(context.Background(), opp, true)
	if err != nil {
		t.Fatalf("live executeOpen: %v", err)
	}
	if pos.OpportunityID != opp.ID {
		t.Fatalf("OpportunityID=%q want %q", pos.OpportunityID, opp.ID)
	}
	stored, ok := findCrossVenueHeldByOpportunityID(e.snapshotPositions(), opp.ID, "BTC-USDT")
	if !ok || stored.OpportunityID != opp.ID {
		t.Fatalf("stored position missing oppID: ok=%v stored=%+v", ok, stored)
	}
}

func TestCrossVenueCloseOnePositionVerifiedNoLegsIdempotent(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	oppID := unifiedOpportunityID(UnifiedStrategyPerpPerp, "BTC-USDT", "snap-rot", bybit.VenueName, okx.VenueName)
	pos := CrossVenuePosition{
		Symbol:          "BTC-USDT",
		HighVenue:       bybit.VenueName,
		LowVenue:        okx.VenueName,
		OpportunityID:   oppID,
		EntrySnapshotID: "snap-rot",
		Status:          CrossVenueStatusOpen,
		OpenedAt:        now,
		UpdatedAt:       now,
		IdemPrefix:      "test-close",
		HighLeg:         CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5},
		LowLeg:          CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5},
	}
	_ = e.persistPosition(pos)
	if err := e.CloseOnePositionVerified(context.Background(), oppID, "BTC-USDT", "rotation close"); err != nil {
		t.Fatalf("CloseOnePositionVerified: %v", err)
	}
	if _, ok := findCrossVenueHeldByOpportunityID(e.snapshotPositions(), oppID, ""); ok {
		t.Fatal("position should be removed from active")
	}
}

func TestCrossVenueCountRealHeldPositions(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	e := newCrossVenueRotationTestExecutor(t, high, low)
	oppID := "pp-held"
	pos := CrossVenuePosition{
		Symbol:        "BTC-USDT",
		HighVenue:     bybit.VenueName,
		LowVenue:      okx.VenueName,
		OpportunityID: oppID,
		Status:        CrossVenueStatusOpen,
		OpenedAt:      now,
		UpdatedAt:     now,
		IdemPrefix:    "cnt",
	}
	_ = e.persistPosition(pos)
	if n := e.CountRealHeldPositions(context.Background()); n != 1 {
		t.Fatalf("real held=%d want 1", n)
	}
}

func TestNoDoubleOpenWhenExecutorOpeningsDisabled(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	e.SetRotationManaged(true)
	_ = e.SetOpeningsEnabled(false)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	before := atomic.LoadInt32(high.placeCount)
	e.tick(context.Background(), true)
	after := atomic.LoadInt32(high.placeCount)
	if after != before {
		t.Fatalf("tick placed orders: before=%d after=%d", before, after)
	}
	st := e.PositionsSnapshot()
	if st.LastCycle.HaltReason != "rotation_managed" {
		t.Fatalf("halt=%q", st.LastCycle.HaltReason)
	}
}

func TestNoDoubleManageWhenRotationManaged(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, EntryPrice: 100}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, EntryPrice: 100}}
	e := newCrossVenueRotationTestExecutor(t, high, low)
	pos := CrossVenuePosition{
		Symbol:     "BTC-USDT",
		HighVenue:  bybit.VenueName,
		LowVenue:   okx.VenueName,
		Status:     CrossVenueStatusOpen,
		OpenedAt:   now.Add(-48 * time.Hour),
		UpdatedAt:  now,
		IdemPrefix: "mgr",
		HighLeg:    CrossVenueLegState{Venue: bybit.VenueName, Side: "sell", Qty: 0.5},
		LowLeg:     CrossVenueLegState{Venue: okx.VenueName, Side: "buy", Qty: 0.5},
	}
	_ = e.persistPosition(pos)
	e.SetRotationManaged(true)
	before := atomic.LoadInt32(high.placeCount) + atomic.LoadInt32(low.placeCount)
	e.tick(context.Background(), true)
	after := atomic.LoadInt32(high.placeCount) + atomic.LoadInt32(low.placeCount)
	if after != before {
		t.Fatalf("tick must not manage: places before=%d after=%d", before, after)
	}
}

func TestPerpPerpHeldCountsTowardMaxConcurrent(t *testing.T) {
	oppID := unifiedOpportunityID(UnifiedStrategyPerpPerp, "BTC-USDT", "snap", bybit.VenueName, okx.VenueName)
	held := []HeldPosition{{
		OpportunityID: oppID,
		StrategyType:  UnifiedStrategyPerpPerp,
		Symbol:        "BTC-USDT",
		CapitalRequiredByVenue: map[string]float64{
			bybit.VenueName: 50,
			okx.VenueName:   50,
		},
	}}
	cfg := DefaultRotationConfig()
	cfg.MaxConcurrent = 1
	candidate := testPerpPerpUnifiedOpportunity("pp-new", "ETH-USDT", binance.VenueName, bybit.VenueName, 50)
	plan := DecideRotation(held, nil, []UnifiedOpportunity{candidate}, map[string]float64{
		binance.VenueName: 500,
		bybit.VenueName:   500,
		okx.VenueName:     500,
	}, cfg)
	openN := 0
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			openN++
		}
	}
	if openN != 0 {
		t.Fatalf("expected no OPEN at max_concurrent=1 with 1 held, got actions=%+v", plan.Actions)
	}
}

func TestUnifiedRotationRunnerPerpPerpOpenWithBinding(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	e.SetRotationManaged(true)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	r := &UnifiedRotationRunner{
		bindings:    map[string]*rotationExecutorBinding{},
		logger:      log.New(io.Discard, "", 0),
		nowFn:       func() time.Time { return now },
		rotationCfg: DefaultRotationConfig(),
		execCfg:     e.cfg.CrossVenueExecutor,
		recordDir:   e.cfg.NetworkConfig.RecordDir,
	}
	if b := bindCrossVenuePerpPerpRotationExecutor(e); b != nil {
		r.bindings[b.strategyType] = b
	}
	r.active.Store(true)
	opp := UnifiedOpportunityFromCrossVenue(e.cfg.CrossVenueExecutor, e.scanner.Snapshot().Opportunities[0], nil, 50, 100)
	plan := RotationPlan{Actions: []RotationAction{
		{Kind: RotationActionOpen, OpportunityID: opp.ID, StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT"},
	}}
	r.decideRotationFn = func([]HeldPosition, map[string]ContinuationInputs, []UnifiedOpportunity, map[string]float64) RotationPlan {
		return plan
	}
	r.runOnceDecisionAndDispatch(context.Background(), []UnifiedOpportunity{opp}, map[string]float64{
		bybit.VenueName: 5000,
		okx.VenueName:   5000,
	}, nil, nil)
	if _, ok := findCrossVenueHeldByOpportunityID(e.snapshotPositions(), opp.ID, "BTC-USDT"); !ok {
		t.Fatal("expected perp-perp OPEN via binding")
	}
}

func TestUnifiedRotationRunnerPerpPerpCandidateExecutableWithBinding(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	e := newCrossVenueRotationTestExecutor(t, high, low)
	r := &UnifiedRotationRunner{
		bindings:                  map[string]*rotationExecutorBinding{},
		logger:                    log.New(io.Discard, "", 0),
		nowFn:                     func() time.Time { return now },
		rotationCfg:               DefaultRotationConfig(),
		execCfg:                   e.cfg.CrossVenueExecutor,
		includePerpPerpCandidates: true,
		fetchDeps: unifiedMarketPipelineFetchDeps{
			fetchMarkets:       defaultUnifiedMarketFetchMarketsByVenue,
			fetchAvailableUSDT: defaultUnifiedMarketFetchAvailableUSDTByVenue,
		},
	}
	if b := bindCrossVenuePerpPerpRotationExecutor(e); b != nil {
		r.bindings[b.strategyType] = b
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "pp-snap",
		ScannedAt:  now,
		Opportunities: []CrossVenueOpportunity{{
			Symbol:     "BTC-USDT",
			HighVenue:  bybit.VenueName,
			LowVenue:   okx.VenueName,
			SnapshotID: "pp-snap",
			SpreadAPR:  2.0,
			NetEV:      5,
		}},
		FundingByVenue: e.scanner.lastSnapshot.FundingByVenue,
	}
	candidates, _, err := r.defaultScanRotationCandidates(context.Background(), snap)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, c := range candidates {
		if c.StrategyType == UnifiedStrategyPerpPerp {
			if c.CandidateOnly {
				t.Fatal("perp-perp candidate should be executable when binding present")
			}
			return
		}
	}
	t.Fatal("expected perp-perp candidate")
}

type rotationSideEffectTrackingVenue struct {
	*crossVenueTestVenue
	topUpCalls *int32
	signCalls  *int32
}

func (v *rotationSideEffectTrackingVenue) TopUpPerpMarginUSDT(_ context.Context, requiredUSDT float64) (float64, error) {
	if v.topUpCalls != nil {
		atomic.AddInt32(v.topUpCalls, 1)
	}
	if v.crossVenueTestVenue == nil {
		return 0, nil
	}
	before := v.crossVenueTestVenue.balanceUSDT
	if before+1e-6 >= requiredUSDT {
		return 0, nil
	}
	transferred := requiredUSDT - before
	v.crossVenueTestVenue.balanceUSDT = requiredUSDT + 100
	return transferred, nil
}

func (v *rotationSideEffectTrackingVenue) SignTradingAgreements(context.Context) error {
	if v.signCalls != nil {
		atomic.AddInt32(v.signCalls, 1)
	}
	return nil
}

func newRotationSideEffectTrackingExecutor(t *testing.T, high, low *rotationSideEffectTrackingVenue, nextFunding time.Time) *CrossVenueExecutor {
	t.Helper()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.OpenSpreadAPR = 0.5
	cfg.CrossVenueExecutor.MaxBookSpreadBps = 250
	cfg.CrossVenueExecutor.OpenFundingLeadSeconds = 180
	tightBook := venue.BookTicker{BidPrice: 99.9, AskPrice: 100.1, BidQty: 1000, AskQty: 1000}
	high.book = tightBook
	low.book = tightBook
	cvOpp := makeTestOpenOpportunity(cfg.CrossVenueExecutor, now, "BTC-USDT", high.name, low.name, 0.05, 0.0, 50, 0.9, 0.9, 0.9, 0.9)
	cvOpp.HighNextFundingTime = nextFunding
	cvOpp.LowNextFundingTime = nextFunding
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{high.name: high, low.name: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			high.name: {
				"BTC-USDT": {Rate: 0.05, IntervalHours: 8, NextFundingTime: nextFunding},
			},
			low.name: {
				"BTC-USDT": {Rate: 0.0, IntervalHours: 8, NextFundingTime: nextFunding},
			},
		}),
		symbolCooldown: map[string]time.Time{},
	}
	e.scanner.lastSnapshot.ScannedAt = now
	e.scanner.lastSnapshot.SnapshotID = "snap-rot"
	e.scanner.lastSnapshot.Opportunities = []CrossVenueOpportunity{cvOpp}
	e.SetRotationManaged(true)
	return e
}

// TestRotationExecuteOpenFundingEntryWindowSkipsSideEffects 进场窗未到时须先过无副作用闸再返回,
// 不得触发 TopUpPerpMarginUSDT / bybit 协议签署(审计:每分钟 OPEN 失败仍划保证金)。
func TestRotationExecuteOpenFundingEntryWindowSkipsSideEffects(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	nextFunding := now.Add(600 * time.Second) // 600s > 180s lead → funding_entry_window_not_reached
	var highTopUp, lowTopUp, signCalls int32
	highBase := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, nextFunding)
	lowBase := newCrossVenueTestVenue(binance.VenueName, 0.0001, 8, nextFunding)
	high := &rotationSideEffectTrackingVenue{crossVenueTestVenue: highBase, topUpCalls: &highTopUp, signCalls: &signCalls}
	low := &rotationSideEffectTrackingVenue{crossVenueTestVenue: lowBase, topUpCalls: &lowTopUp}
	e := newRotationSideEffectTrackingExecutor(t, high, low, nextFunding)
	opp := UnifiedOpportunityFromCrossVenue(e.cfg.CrossVenueExecutor, e.scanner.Snapshot().Opportunities[0], nil, 50, 100)
	_, err := e.executeOpen(context.Background(), opp, false)
	if err == nil {
		t.Fatal("expected funding entry window reject")
	}
	if !strings.Contains(err.Error(), carryScannerRejectFundingWindowNotReached) {
		t.Fatalf("unexpected err: %v", err)
	}
	if atomic.LoadInt32(&highTopUp) != 0 || atomic.LoadInt32(&lowTopUp) != 0 {
		t.Fatalf("TopUpPerpMarginUSDT must not run before entry window: high=%d low=%d", highTopUp, lowTopUp)
	}
	if atomic.LoadInt32(&signCalls) != 0 {
		t.Fatalf("SignTradingAgreements must not run before entry window: calls=%d", signCalls)
	}
}

// TestRotationExecuteOpenGatePassStillTopUpPerpMargin 闸通过后仍须在下单前补足永续保证金(含 gate 初拒、划转后复闸)。
func TestRotationExecuteOpenGatePassStillTopUpPerpMargin(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	nextFunding := now.Add(120 * time.Second) // within 180s lead
	var highTopUp, lowTopUp, signCalls int32
	highBase := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, nextFunding)
	lowBase := newCrossVenueTestVenue(binance.VenueName, 0.0001, 8, nextFunding)
	// 低于 gate 所需保证金,TopUpPerpMarginUSDT 须被调用后复闸才放行。
	highBase.balanceUSDT = 5
	lowBase.balanceUSDT = 5
	high := &rotationSideEffectTrackingVenue{crossVenueTestVenue: highBase, topUpCalls: &highTopUp, signCalls: &signCalls}
	low := &rotationSideEffectTrackingVenue{crossVenueTestVenue: lowBase, topUpCalls: &lowTopUp}
	e := newRotationSideEffectTrackingExecutor(t, high, low, nextFunding)
	e.cfg.CrossVenueExecutor.LiveTradingEnabled = true
	opp := UnifiedOpportunityFromCrossVenue(e.cfg.CrossVenueExecutor, e.scanner.Snapshot().Opportunities[0], nil, 50, 100)
	pos, err := e.executeOpen(context.Background(), opp, true)
	if err != nil {
		t.Fatalf("executeOpen after margin top-up: %v", err)
	}
	if pos.OpportunityID != opp.ID {
		t.Fatalf("OpportunityID=%q want %q", pos.OpportunityID, opp.ID)
	}
	if atomic.LoadInt32(&highTopUp) == 0 || atomic.LoadInt32(&lowTopUp) == 0 {
		t.Fatalf("expected TopUpPerpMarginUSDT on both legs: high=%d low=%d", highTopUp, lowTopUp)
	}
	if atomic.LoadInt32(&signCalls) != 1 {
		t.Fatalf("expected bybit agreement sign once, got %d", signCalls)
	}
}
