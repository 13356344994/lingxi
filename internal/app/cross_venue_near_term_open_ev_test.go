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
	if hold := crossVenueOpenEVHoldHours(cfg, now, opp, window); math.Abs(hold-window) > 1e-9 {
		t.Fatalf("near-term hold=%.4fh want comparable window %.4fh", hold, window)
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

func TestCrossVenueEstimateOpportunityMetricsUsesNearTermHorizonWhenEnabled(t *testing.T) {
	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	cfg := testNearTermOpenEVConfig()
	opp := testNearTermRollOpportunity(now)
	opp.TradableNotional = 150
	window := crossVenueComparableWindowHours(now, opp.HighNextFundingTime, opp.LowNextFundingTime)
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
	dustCost := carryScannerPerpPerpDustResidualCostUSDT()
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
