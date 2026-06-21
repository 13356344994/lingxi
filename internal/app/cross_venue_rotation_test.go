package app

import (
	"math"
	"testing"
	"time"
)

func TestCrossVenueCompareAllowedHeterogeneousIntervals(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		MinComparableWindowHours:   0.25,
		PostFundingCooldownSeconds: 180,
	}
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	highNext := now.Add(2 * time.Hour)   // 4h leg,距上次结算 >3min
	lowNext := now.Add(45 * time.Minute) // 1h leg,距上次结算 15min(>180s 冷却)
	window := crossVenueComparableWindowHours(now, highNext, lowNext)
	if window < 0.25 {
		t.Fatalf("comparable window=%.2fh want >=0.25 (min of 45m/2h legs)", window)
	}
	pos := &CrossVenuePosition{
		HighIntervalHours:            4,
		LowIntervalHours:             1,
		HighNextFundingTime:          highNext,
		LowNextFundingTime:           lowNext,
		CurrentComparableWindowHours: window,
	}
	if !crossVenueCompareAllowed(cfg, pos, now) {
		t.Fatal("expected compare allowed when window >= min and not in post-funding cooldown")
	}

	narrowWindow := 0.1
	pos.CurrentComparableWindowHours = narrowWindow
	if crossVenueCompareAllowed(cfg, pos, now) {
		t.Fatal("expected blocked when comparable window below min")
	}

	pos.CurrentComparableWindowHours = window
	// 1h 腿刚结算后 60s:仅该腿 interval 触发冷却,4h 腿不受影响。
	justAfter1hSettle := lowNext.Add(-1*time.Hour + 60*time.Second)
	pos.LowNextFundingTime = lowNext
	if crossVenueCompareAllowed(cfg, pos, justAfter1hSettle) {
		t.Fatal("expected blocked during 1h leg post-funding cooldown")
	}
	// 4h 腿结算后 60s:按 4h interval 独立判断,1h 腿不在冷却。
	fourHourNext := now.Add(3 * time.Hour)
	pos.HighNextFundingTime = fourHourNext
	pos.LowNextFundingTime = lowNext
	justAfter4hSettle := fourHourNext.Add(-4*time.Hour + 60*time.Second)
	if crossVenueCompareAllowed(cfg, pos, justAfter4hSettle) {
		t.Fatal("expected blocked during 4h leg post-funding cooldown")
	}
}

func TestEntryBasisCostInNetEV(t *testing.T) {
	cfg := testNetProfitGateExecutorConfig()
	cfg.AssumeMakerFills = true
	cfg.ExpectedHoldHours = 12
	cfg.EntryBasisConvergenceFraction = 0.5
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	notional := 150.0
	opp := testCarryOpenEVOpportunity(now, nextFunding)

	if res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0); !res.OK {
		t.Fatalf("aligned mids should pass open EV: %s", res.Reason)
	}
	if res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0.10); res.OK {
		t.Fatal("10% entry basis should reject when funding margin is thin")
	}
	basisCost := crossVenuePerpPerpEntryBasisCostUSDT(cfg, 0.10, notional, CrossVenueOpportunity{})
	if math.Abs(basisCost-7.5) > 1e-9 {
		t.Fatalf("basis cost=%.4f want 7.5", basisCost)
	}
	slipLegs := []crossVenueEVSlippageLeg{{RefMid: 110}, {RefMid: 100}}
	wide := crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, opp, now, opp.HighFundingRate, opp.LowFundingRate, slipLegs)
	aligned := crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	if wide.NetEV >= aligned.NetEV {
		t.Fatalf("wide mid should lower netEV: wide=%.4f aligned=%.4f", wide.NetEV, aligned.NetEV)
	}
	if wide.NetEV > crossVenueMinNetProfitUSDT(cfg) {
		t.Fatalf("large basis should push netEV negative, got %.4f", wide.NetEV)
	}
}

func TestForwardEVDiscretePerLeg(t *testing.T) {
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	highNext := now.Add(2 * time.Hour)
	lowNext := now.Add(45 * time.Minute)
	window := crossVenueComparableWindowHours(now, highNext, lowNext)
	pos := &CrossVenuePosition{
		HighVenue: "okx", LowVenue: "bybit",
		NotionalUSDT:    100,
		HighFundingRate: 0.004, LowFundingRate: 0.0001,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: highNext, LowNextFundingTime: lowNext,
		CurrentComparableWindowHours: window,
		ExecutionScore:               0.8, RecoveryQualityScore: 0.7,
	}
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	opp := CrossVenueOpportunity{
		HighFundingRate: pos.HighFundingRate, LowFundingRate: pos.LowFundingRate,
		HighIntervalHours: pos.HighIntervalHours, LowIntervalHours: pos.LowIntervalHours,
		HighNextFundingTime: highNext, LowNextFundingTime: lowNext,
	}
	discreteFunding := crossVenueExpectedFundingOverHold(now, window, 100, opp)
	linearFunding := crossVenueExpectedFundingPerHour(100, pos.HighFundingRate, 4, pos.LowFundingRate, 1) * window
	if math.Abs(discreteFunding-(-0.01)) > 1e-9 {
		t.Fatalf("discrete funding want only low-leg settlement -0.01, got %.6f", discreteFunding)
	}
	if linearFunding <= discreteFunding {
		t.Fatalf("linear %.6f should differ from discrete %.6f", linearFunding, discreteFunding)
	}
	futureEV, _, _, exitCost, _, _, _ := crossVenueComputeCurrentFutureEV(cfg, pos, now)
	futureRisk := crossVenueObservableRiskCost(100, 0, window, pos.RecoveryQualityScore)
	capturedFunding := discreteFunding * crossVenueExecutorFundingCaptureProbability(cfg)
	linearBasedEV := linearFunding - exitCost - futureRisk
	if math.Abs(futureEV-linearBasedEV) < 1e-6 {
		t.Fatalf("forward EV %.6f should differ from linear extrapolation %.6f", futureEV, linearBasedEV)
	}
	wantEV := capturedFunding - exitCost - futureRisk
	if math.Abs(futureEV-wantEV) > 1e-6 {
		t.Fatalf("forward EV %.6f want discrete-based %.6f", futureEV, wantEV)
	}
}
