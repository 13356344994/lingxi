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
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3, MaxPositionUSDT: 150, ExpectedHoldHours: 12}
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
