package app

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func testNearTermOpenEVConfig() CrossVenueExecutorConfig {
	cfg := testNetProfitGateMakerHoldConfig()
	cfg.NearTermOpenEVHorizonEnabled = true
	cfg.ExpectedHoldHours = 12
	cfg.MinNetProfitUSDT = 0
	cfg.EVExpectedSlippageBps = 1
	return cfg
}

func testNearTermRollOpportunity(now time.Time) CrossVenueOpportunity {
	highNext := now.Add(30 * time.Minute)
	lowNext := now.Add(2 * time.Hour)
	return CrossVenueOpportunity{
		Symbol:              "ROLL-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		HighFundingRate:     0.008,
		LowFundingRate:      0.0001,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: highNext,
		LowNextFundingTime:  lowNext,
	}
}

func TestCrossVenueOpenEVNearTermHorizonPassesWhenCapturableFundingCoversRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	notional := 150.0
	opp := testNearTermRollOpportunity(now)
	window := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
	if window <= 0 {
		t.Fatalf("expected positive comparable window, got %.4fh", window)
	}
	// 近端 horizon = max(可比窗口, N×最短腿 interval) 再封顶:最短腿=1h、N=3 → 3h(可比窗口 0.5h 更小,被覆盖)。
	wantHold := math.Max(window, 3*crossVenueShortestLegFundingIntervalHours(opp))
	if hold := crossVenueOpenEVHoldHours(cfg, now, opp, window); math.Abs(hold-wantHold) > 1e-9 {
		t.Fatalf("near-term hold=%.4fh want expected-forward-hold %.4fh (NOT residual window %.4fh)", hold, wantHold, window)
	}
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if !res.OK {
		t.Fatalf("expected near-term open EV pass when imminent high-leg settlement covers round-trip: %s", res.Reason)
	}
}

func TestCrossVenueOpenEVNearTermHorizonRejectsWhenCapturableFundingInsufficient(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	cfg.AssumeMakerFills = false
	cfg.DefaultTakerFeeBps = 6
	notional := 150.0
	opp := testNearTermRollOpportunity(now)
	opp.HighFundingRate = 0.00005
	opp.LowFundingRate = 0.00004
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if res.OK {
		t.Fatal("expected reject when near-term capturable funding does not cover round-trip")
	}
}

func TestCrossVenueOpenEVNearTermHorizonDisabledUsesExpectedHoldHours(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	cfg.NearTermOpenEVHorizonEnabled = false
	notional := 150.0
	opp := testNearTermRollOpportunity(now)
	window := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
	if hold := crossVenueOpenEVHoldHours(cfg, now, opp, window); math.Abs(hold-12) > 1e-9 {
		t.Fatalf("flag off hold=%.4fh want fixed 12h", hold)
	}
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if !res.OK {
		t.Fatalf("expected 12h legacy pass for high carry roll fixture: %s", res.Reason)
	}
}

func TestCrossVenueOpenEVNearTermHorizonUsesLegIntervalNotTwelveHoursWhenWindowZero(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig() // NearTermOpenEVHorizonEnabled=true, ExpectedHoldHours=12
	// 两腿结算时间缺失(零值)→ 可比窗口=0;短周期腿=1h。
	opp := CrossVenueOpportunity{
		Symbol:            "ZEROWIN-USDT",
		HighVenue:         binance.VenueName,
		LowVenue:          bybit.VenueName,
		HighFundingRate:   0.008,
		LowFundingRate:    0.0001,
		HighIntervalHours: 4,
		LowIntervalHours:  1,
	}
	if win := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime); win != 0 {
		t.Fatalf("precondition: comparable window must be 0 with missing funding times, got %.4fh", win)
	}
	// window=0 → horizon = N×最短腿 interval = 3×1h = 3h(NOT 残余窗口, NOT 12h)。
	hold := crossVenueOpenEVHoldHours(cfg, now, opp, 0)
	if math.Abs(hold-3) > 1e-9 {
		t.Fatalf("near-term horizon with window=0 should be N×shortest-leg-interval (3×1h=3h), got %.4fh (must NOT be 1h, NOT 12h)", hold)
	}
	// interval 也缺失 → 0(最保守:不凭空给 horizon),仍不退回 12h。
	oppNoInterval := opp
	oppNoInterval.HighIntervalHours = 0
	oppNoInterval.LowIntervalHours = 0
	if hold := crossVenueOpenEVHoldHours(cfg, now, oppNoInterval, 0); hold != 0 {
		t.Fatalf("near-term horizon with no window and no interval should be 0 (not 12h), got %.4fh", hold)
	}
}

// nearTermGateNetProfit 在测试侧按 evaluateCrossVenueOpenEV 同一成本栈复算指定 horizon 的 netProfit,
// 用于对比"残余窗口 horizon(旧 bug)"与"前向持有 horizon(修复)"的符号翻转(midDev=0、evLegs=nil 口径)。
func nearTermGateNetProfit(cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, notional, holdHours float64, now time.Time) float64 {
	expectedFunding := crossVenueOpenGateExpectedFundingUSDT(cfg, now, holdHours, notional, opp)
	openCost := crossVenueOpenEVRoundTripFeeUSDT(cfg, opp, notional)
	closeCost := openCost
	roundTripSlippage := 4 * notional * crossVenueOpenEVEslippageBpsPerLeg(cfg) / 10000
	roundTripCost := openCost + closeCost + roundTripSlippage
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(notional, opp.FreshnessAgeSeconds, holdHours, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT(notional)
	entryBasisCost := crossVenuePerpPerpEntryBasisCostUSDT(cfg, 0, notional, opp)
	return expectedFunding - roundTripCost - observableRiskCost - policyBuffer - dustCost - entryBasisCost
}

func testNearTermEntryWindowOpportunity(now time.Time, highRate, lowRate float64) CrossVenueOpportunity {
	// 进场窗内:最近一腿结算仅剩 ~2.4min(0.04h),最短腿 funding interval=1h。
	return CrossVenueOpportunity{
		Symbol:              "ENTRY-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   1,
		LowIntervalHours:    1,
		HighNextFundingTime: now.Add(144 * time.Second),
		LowNextFundingTime:  now.Add(40 * time.Minute),
	}
}

func TestCrossVenueOpenEVHoldHoursEntryWindowReturnsForwardHoldNotResidual(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig() // N=3(默认), cap=8h(默认)
	opp := testNearTermEntryWindowOpportunity(now, 0.006, 0.0001)
	comparable := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
	if math.Abs(comparable-0.04) > 1e-6 {
		t.Fatalf("precondition: comparable window=%.5fh want ~0.04h (entry window)", comparable)
	}
	hold := crossVenueOpenEVHoldHours(cfg, now, opp, comparable)
	if math.Abs(hold-3) > 1e-9 {
		t.Fatalf("entry-window hold=%.4fh want N×interval=3h (NOT residual 0.04h, NOT 12h)", hold)
	}
}

func TestCrossVenueOpenEVHoldHoursCapsAtMaxHoldHours(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	cfg.NearTermOpenEVExpectedSettlements = 20 // 20×1h=20h 必被封顶
	opp := testNearTermEntryWindowOpportunity(now, 0.006, 0.0001)
	if hold := crossVenueOpenEVHoldHours(cfg, now, opp, 0); math.Abs(hold-8) > 1e-9 {
		t.Fatalf("hold=%.4fh want default cap 8h (20×1h clamped)", hold)
	}
	cfg.NearTermOpenEVMaxHoldHours = 5
	if hold := crossVenueOpenEVHoldHours(cfg, now, opp, 0); math.Abs(hold-5) > 1e-9 {
		t.Fatalf("hold=%.4fh want custom cap 5h", hold)
	}
}

func TestCrossVenueOpenEVHighFundingEntryWindowOpensWithForwardHorizon(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	notional := 150.0
	// 选一个「中等」资费:残窗(~0.04h,单次结算)净值仍为负——成本占优;
	// 前瞻 horizon(N×interval=3h,多次结算)才转正。以此守住「horizon 修复确实解锁开仓」的回归含义。
	// 注:成本侧已改为 maker 感知 + dust 按名义比例,整体成本下降,故此处资费需比旧 0.006 更薄方能让残窗为负。
	opp := testNearTermEntryWindowOpportunity(now, 0.0015, 0.0001)
	residual := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
	forwardHold := crossVenueOpenEVHoldHours(cfg, now, opp, residual)

	residualNet := nearTermGateNetProfit(cfg, opp, notional, residual, now)
	if residualNet > 0 {
		t.Fatalf("regression precondition: residual-window horizon (%.4fh) should be net-negative, got %.4f", residual, residualNet)
	}
	forwardNet := nearTermGateNetProfit(cfg, opp, notional, forwardHold, now)
	if forwardNet <= 0 {
		t.Fatalf("forward horizon (%.4fh) should be net-positive for high-funding opp, got %.4f", forwardHold, forwardNet)
	}
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, residual, 0)
	if !res.OK {
		t.Fatalf("gate should OPEN high-funding entry-window opp with forward horizon: %s", res.Reason)
	}
}

func TestCrossVenueOpenEVThinSpreadEntryWindowStillRejected(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	notional := 150.0
	// 薄价差:即便按前向持有(3 结算)计费,funding 仍覆盖不了往返成本——确认修复未"什么都开"。
	opp := testNearTermEntryWindowOpportunity(now, 0.0005, 0.0003)
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if res.OK {
		t.Fatal("thin-spread entry-window opp must still be rejected (forward horizon must not open everything)")
	}
}

func TestCrossVenueEstimateOpportunityMetricsUsesNearTermHorizonWhenEnabled(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	opp := testNearTermRollOpportunity(now)
	opp.TradableNotional = 150
	// 扫描 netEV 与开仓闸同走 crossVenueOpenEVHoldHours:近端 horizon 为前向持有(max(可比窗口, N×interval) 封顶),非残余窗口。
	window := crossVenueOpenEVHoldHours(cfg, now, opp, crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime))
	metrics := crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	nearFunding := crossVenueOpenGateExpectedFundingUSDT(cfg, now, window, opp.TradableNotional, opp)
	longFunding := crossVenueOpenGateExpectedFundingUSDT(cfg, now, 12, opp.TradableNotional, opp)
	if math.Abs(nearFunding-longFunding) < 1e-9 {
		t.Fatal("near-term and 12h funding should differ for heterogeneous-interval roll")
	}
	rebuilt := opp
	rebuilt.SpreadEV = nearFunding
	openCost := crossVenueOpenEVRoundTripFeeUSDT(cfg, opp, opp.TradableNotional)
	closeCost := openCost
	expectedSlippage := crossVenueExpectedSlippageUSDT(cfg, opp.TradableNotional, 4)
	recoveryQuality := crossVenueRecoveryQualityForPair(opp.HighVenue, opp.LowVenue)
	observableRiskCost := crossVenueObservableRiskCost(opp.TradableNotional, opp.FreshnessAgeSeconds, window, recoveryQuality)
	policyBuffer := crossVenuePolicyBuffer(openCost, closeCost)
	dustCost := carryScannerPerpPerpDustResidualCostUSDT(opp.TradableNotional)
	entryBasisCost := crossVenuePerpPerpEntryBasisCostUSDT(cfg, 0, opp.TradableNotional, opp)
	wantNetEV := nearFunding - openCost - closeCost - expectedSlippage - observableRiskCost - policyBuffer - dustCost - entryBasisCost
	if math.Abs(metrics.NetEV-wantNetEV) > 1e-6 {
		t.Fatalf("scanner netEV=%.6f want near-term horizon %.6f", metrics.NetEV, wantNetEV)
	}
	opps := []CrossVenueOpportunity{metrics}
	crossVenueFinalizeOpportunityRanking(cfg, opps)
	if math.Abs(opps[0].RankScore-opps[0].NetEV) > 1e-9 {
		t.Fatalf("RankScore should equal NetEV after finalize, got rank=%.6f net=%.6f", opps[0].RankScore, opps[0].NetEV)
	}
}
