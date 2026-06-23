package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// gateRealFundingVenue 用于闸门单测:可配置 FetchRealizedFunding 行为。
type gateRealFundingVenue struct {
	crossVenueTestVenue
	realFundingErr error
}

func (v *gateRealFundingVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	if v.realFundingErr != nil {
		return nil, v.realFundingErr
	}
	return nil, nil
}

func TestCrossVenueOpenGateAllowsVerifiableRealFunding(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := &gateRealFundingVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding),
	}
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low := &gateRealFundingVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding),
	}
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.019, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("expected gate pass when both legs verifiable: %s", res.Reason)
	}
}

func TestCrossVenueFundingEntryWindowReached(t *testing.T) {
	now := time.Now().UTC()
	lead := 180 * time.Second

	// 窗口内:最近一腿距今 120s ≤180s → 放行。
	if !crossVenueFundingEntryWindowReached(now.Add(120*time.Second), 8, now.Add(300*time.Second), 8, now, lead) {
		t.Fatal("nearest 120s within 180s lead should be reached (pass)")
	}
	// 过早:最近一腿距今 600s >180s → 拒绝(false)。
	if crossVenueFundingEntryWindowReached(now.Add(600*time.Second), 8, now.Add(600*time.Second), 8, now, lead) {
		t.Fatal("nearest 600s beyond 180s lead should NOT be reached (reject)")
	}
	// 两腿结算时间均未知(零值)→ fail-CLOSED 拒绝(FIX-10:开/换仓缺时间不盲开)。
	if crossVenueFundingEntryWindowReached(time.Time{}, 8, time.Time{}, 0, now, lead) {
		t.Fatal("both unknown should fail-CLOSED (reject)")
	}
	// 过期时间(now-30s)按 interval=8h 推进到下一结算点 → 距今≈8h >180s → 拒绝。
	if crossVenueFundingEntryWindowReached(now.Add(-30*time.Second), 8, time.Time{}, 0, now, lead) {
		t.Fatal("expired time advanced by 8h interval should be far out (reject)")
	}
	// 一腿过期(推进到 +8h),另一腿在窗口内(+120s)→ nearest 取较近的 → 放行。
	if !crossVenueFundingEntryWindowReached(now.Add(-30*time.Second), 8, now.Add(120*time.Second), 8, now, lead) {
		t.Fatal("nearest should pick the within-window leg (pass)")
	}
	// 过期且 interval 未知(无法推进)→ 该腿视为不可用;唯一腿不可用 → fail-CLOSED 拒绝(FIX-10)。
	if crossVenueFundingEntryWindowReached(now.Add(-30*time.Second), 0, time.Time{}, 0, now, lead) {
		t.Fatal("expired with unknown interval (unadvanceable) → unknown → fail-CLOSED (reject)")
	}
}

func TestCrossVenueOpenGateFundingEntryWindow(t *testing.T) {
	now := time.Now().UTC()
	runGate := func(nextFunding time.Time, leadSeconds int) crossVenueGateResult {
		high := newCrossVenueTestVenue(binance.VenueName, 0.004, 8, nextFunding)
		high.book = venueBookTight()
		high.depth = venueDepthDeep()
		low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
		low.book = venueBookTight()
		low.depth = venueDepthDeep()
		cfg := testNetProfitGateExecutorConfig()
		cfg.AssumeMakerFills = true
		cfg.ExpectedHoldHours = 12
		cfg.OpenFundingLeadSeconds = leadSeconds
		return evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), map[string]venue.Venue{
			binance.VenueName: high,
			okx.VenueName:     low,
		}, crossVenueGateInput{
			Opp: CrossVenueOpportunity{
				Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
				SpreadAPR: 0.40, HighFundingRate: 0.004, LowFundingRate: 0.0001,
				HighIntervalHours: 8, LowIntervalHours: 8,
				HighAnnualizedAPR: 4.0, LowAnnualizedAPR: 0.1,
			},
			Now: now,
		})
	}

	// 窗口内:距结算 120s ≤180s lead → 过窗口闸,其余闸也通过 → OK。
	if res := runGate(now.Add(120*time.Second), 180); !res.OK {
		t.Fatalf("within entry window (120s) should pass, got reject: %s", res.Reason)
	}
	// 过早:距结算 600s >180s lead → 窗口闸拒,reason=funding_entry_window_not_reached。
	res := runGate(now.Add(600*time.Second), 180)
	if res.OK {
		t.Fatal("too-early (600s before settlement) should be rejected by entry-window gate")
	}
	if res.Reason != carryScannerRejectFundingWindowNotReached {
		t.Fatalf("reject reason = %q, want %q", res.Reason, carryScannerRejectFundingWindowNotReached)
	}
}

func TestCrossVenueFundingSnapshotValid(t *testing.T) {
	now := time.Now().UTC()
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(time.Hour),
	}, now) {
		// ok
	} else {
		t.Fatal("expected valid snapshot")
	}
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 0, NextFundingTime: now.Add(time.Hour),
	}, now) {
		t.Fatal("expected invalid when interval zero")
	}
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(-time.Hour),
	}, now) {
		t.Fatal("expected invalid when next funding in past")
	}
}

func crossVenueGateTestInput(now time.Time) crossVenueGateInput {
	return crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.019, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	}
}

func TestCrossVenueOpenGateLiquiditySlippageRejectsThinBook(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	// 卖 50U@100 需 ~0.5 币:仅 0.05@100 + 0.45@95 → 滑点远超 15bps
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
	}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}, {Price: 100.02, Qty: 100}},
	}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = 50
	cfg.CrossVenueExecutor.MaxOpenSlippageBps = 15
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateTestInput(now))
	if res.OK {
		t.Fatalf("expected gate reject on thin high leg, got pass")
	}
	if !strings.Contains(res.Reason, "高费所预估滑点") || !strings.Contains(res.Reason, "> 15") {
		t.Fatalf("unexpected reason: %s", res.Reason)
	}
}

func TestCrossVenueOpenGateLiquiditySlippageAllowsDeepBook(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 100}, {Price: 99.99, Qty: 100}},
	}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}, {Price: 100.02, Qty: 100}},
	}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = 50
	cfg.CrossVenueExecutor.MaxOpenSlippageBps = 15
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateTestInput(now))
	if !res.OK {
		t.Fatalf("expected gate pass on deep book: %s", res.Reason)
	}
}

func TestSimulateTakerSlippageBps(t *testing.T) {
	// 50U @ ref 100 → 0.5 qty; 0.05@100 + 0.45@95 → avg 95.5, best 100 → 450bps
	slip, ok := simulateTakerSlippageBps(venue.SideSell, []venue.DepthLevel{
		{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100},
	}, 50, 100)
	if !ok {
		t.Fatal("expected ok")
	}
	if slip < 400 || slip > 500 {
		t.Fatalf("slippage bps=%v want ~450", slip)
	}
	slip, ok = simulateTakerSlippageBps(venue.SideBuy, []venue.DepthLevel{
		{Price: 100, Qty: 10},
	}, 50, 100)
	if !ok || slip > 0.01 {
		t.Fatalf("deep book buy slip=%v ok=%v want ~0", slip, ok)
	}
}

func TestOpenGateMarginMatchesSizing(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	notional := 150.0
	lev := 3.0
	wantMargin := crossVenuePerVenueMarginUSDT(CrossVenueExecutorConfig{MaxEffectiveLeverage: lev}, notional)
	if math.Abs(wantMargin-50) > 1e-9 {
		t.Fatalf("sanity sizing margin=%.4f want 50", wantMargin)
	}

	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 5000}, {Price: 99.99, Qty: 5000}},
	}
	high.balanceUSDT = wantMargin
	low := newCrossVenueTestVenue(bybit.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 5000}, {Price: 100.02, Qty: 5000}},
	}
	low.balanceUSDT = wantMargin
	venues := map[string]venue.Venue{binance.VenueName: high, bybit.VenueName: low}

	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = notional
	cfg.CrossVenueExecutor.MaxEffectiveLeverage = lev
	cfg.CrossVenueExecutor.MarginSafetyFactor = 1.5 // 旧口径会要 75;新口径与 sizing 同为 50

	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.019, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
			HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("expected gate pass at sizing-aligned margin 50, got reject: %s", res.Reason)
	}
	// 资金封顶:可用恰为 50 时,执行闸按 可用×lev×0.99 封顶名义到 148.5(留安全垫),
	// 使所需保证金 49.5 严格小于可用 50 —— 正是修掉"49.99<50 几分钱误拒"的关键。
	wantCapped := wantMargin * lev * crossVenueExecCapitalSizingHaircut
	if math.Abs(res.NotionalUSDT-wantCapped) > 1e-6 {
		t.Fatalf("notional=%.4f want capital-capped %.4f (avail×lev×haircut)", res.NotionalUSDT, wantCapped)
	}
	if gotMargin := crossVenuePerVenueMarginUSDT(cfg.CrossVenueExecutor, res.NotionalUSDT); gotMargin >= wantMargin {
		t.Fatalf("required margin %.4f should be strictly < available %.4f", gotMargin, wantMargin)
	}

	// 旧口径要 75:perp=74 会误拒;新口径 50 应通过。
	high.balanceUSDT = 74
	low.balanceUSDT = 74
	resMid := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.019, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
			HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
		},
		Now: now,
	})
	if !resMid.OK {
		t.Fatalf("74 USDT perp should pass new 50 margin gate, old 75 would reject: %s", resMid.Reason)
	}
}

func TestSizedOrderPassesGateWhenCapitalExists(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3, MaxPositionUSDT: 150, ExpectedHoldHours: 12, OpenFundingLeadSeconds: 172800}
	rotCfg := DefaultRotationConfig()
	availAfterReserve := 50.0
	available := map[string]float64{
		binance.VenueName: availAfterReserve + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   availAfterReserve + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}

	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	opp := testPerpPerpHOpportunityForGate(t, 150)
	capped := rotationCapPerpPerpOpportunityByCapital(opp, execCfg, rotCfg, available)
	sizedNotional := rotationMaxLegNotional(capped)
	wantMargin := crossVenuePerVenueMarginUSDT(execCfg, sizedNotional)
	if wantMargin > availAfterReserve+0.01 {
		t.Fatalf("capped margin %.4f should fit avail %.4f", wantMargin, availAfterReserve)
	}

	high := newCrossVenueTestVenue(binance.VenueName, 0.019, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 5000}, {Price: 99.99, Qty: 5000}},
	}
	high.balanceUSDT = wantMargin
	low := newCrossVenueTestVenue(bybit.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 5000}, {Price: 100.02, Qty: 5000}},
	}
	low.balanceUSDT = wantMargin
	venues := map[string]venue.Venue{binance.VenueName: high, bybit.VenueName: low}

	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor = execCfg
	cfg.CrossVenueExecutor.MaxPositionUSDT = sizedNotional
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: bybit.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.019, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
			HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("capital-sized order should pass gate, reason=%s", res.Reason)
	}
}

func TestMidDevRelaxedForExtremeSpread(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	runGate := func(opp CrossVenueOpportunity) crossVenueGateResult {
		high := newCrossVenueTestVenue(okx.VenueName, 0.00625, 4, nextFunding)
		high.book = venue.BookTicker{BidPrice: 103.48, AskPrice: 103.52, BidQty: 1000, AskQty: 1000}
		high.depth = venueDepthDeep()
		low := newCrossVenueTestVenue(bybit.VenueName, 0.0000125, 1, nextFunding)
		low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
		low.depth = venueDepthDeep()
		cfg := testNetProfitGateExecutorConfig()
		cfg.MidPriceDeviationMax = 0.03
		cfg.MidPriceDeviationMaxExtreme = 0.08
		cfg.AssumeMakerFills = true
		cfg.ExpectedHoldHours = 12
		cfg.MaxPositionUSDT = 50
		return evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), map[string]venue.Venue{
			okx.VenueName: high, bybit.VenueName: low,
		}, crossVenueGateInput{Opp: opp, Now: now})
	}
	extremeOpp := CrossVenueOpportunity{
		Symbol: "BTC-USDT", HighVenue: okx.VenueName, LowVenue: bybit.VenueName,
		SpreadAPR: 13.58, ExtremeSpread: true,
		HighFundingRate: 0.00625, LowFundingRate: 0.0000125,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
	}
	if res := runGate(extremeOpp); !res.OK {
		t.Fatalf("extreme ~3.5%% mid dev should pass gate: %s", res.Reason)
	}
	normalOpp := extremeOpp
	normalOpp.ExtremeSpread = false
	normalOpp.SpreadAPR = 1.0
	if res := runGate(normalOpp); res.OK || !strings.Contains(res.Reason, "中间价偏离") {
		t.Fatalf("non-extreme ~3.5%% mid dev should reject, ok=%v reason=%q", res.OK, res.Reason)
	}
}

func testPerpPerpHOpportunityForGate(t *testing.T, notional float64) UnifiedOpportunity {
	t.Helper()
	now := time.Now().UTC()
	cvOpp := CrossVenueOpportunity{
		Symbol:              "H-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		SpreadAPR:           13.58,
		HighFundingRate:     0.00625,
		LowFundingRate:      0.0000125,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: now.Add(2 * time.Hour),
		LowNextFundingTime:  now.Add(time.Hour),
		TradableNotional:    notional,
		SpreadEV:            2.0,
		NetEV:               1.5,
	}
	return UnifiedOpportunityFromCrossVenue(CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}, cvOpp, nil, notional, 0)
}

// TestCrossVenueFundingSnapFromOppLegRollsStaleTime 验证:有效但 NextFundingTime 过期的腿应按 interval 滚动后放行
// (修正旧快照/刚过结算点导致的"幽灵腿"误判);真缺 interval/时间的腿仍判幽灵(行为不变)。
func TestCrossVenueFundingSnapFromOppLegRollsStaleTime(t *testing.T) {
	now := time.Date(2026, 6, 21, 14, 9, 0, 0, time.UTC)
	// 过期但有效(rate+interval 齐全,NextFundingTime=14:00 已过)→ 滚动到 15:00 放行
	opp := CrossVenueOpportunity{HighFundingRate: -0.0004, HighIntervalHours: 1, HighNextFundingTime: time.Date(2026, 6, 21, 14, 0, 0, 0, time.UTC)}
	snap, ok := crossVenueFundingSnapFromOppLeg(opp, true, now)
	if !ok {
		t.Fatal("有效但时间戳过期的高腿被误判为幽灵腿(应滚动后放行)")
	}
	if !snap.NextFundingTime.Equal(time.Date(2026, 6, 21, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextFundingTime 应滚动到 15:00, got %v", snap.NextFundingTime)
	}
	// interval 缺失时回退 venue 默认→ 过期 NextFundingTime 仍可滚动放行
	oppDefaultInt, ok := crossVenueFundingSnapFromOppLeg(CrossVenueOpportunity{
		HighVenue: gate.VenueName, HighFundingRate: -0.0004, HighIntervalHours: 0,
		HighNextFundingTime: time.Date(2026, 6, 21, 14, 0, 0, 0, time.UTC),
	}, true, now)
	if !ok {
		t.Fatal("缺 per-symbol interval 时应回退 venue 默认并滚动过期时间")
	}
	if oppDefaultInt.IntervalHours <= 0 {
		t.Fatalf("venue default interval should be >0, got %d", oppDefaultInt.IntervalHours)
	}
	// 缺 NextFundingTime → 仍幽灵
	if _, ok := crossVenueFundingSnapFromOppLeg(CrossVenueOpportunity{HighFundingRate: -0.0004, HighIntervalHours: 1}, true, now); ok {
		t.Fatal("缺 NextFundingTime 的腿应仍判幽灵")
	}
	// 未来时间 → 原样有效
	snap4, ok := crossVenueFundingSnapFromOppLeg(CrossVenueOpportunity{HighFundingRate: -0.0004, HighIntervalHours: 1, HighNextFundingTime: now.Add(20 * time.Minute)}, true, now)
	if !ok || !snap4.NextFundingTime.Equal(now.Add(20*time.Minute)) {
		t.Fatalf("未来时间应原样有效: ok=%v t=%v", ok, snap4.NextFundingTime)
	}
}
