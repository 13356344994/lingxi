package app

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestExtremeSpreadBypassesConfidenceDownsize(t *testing.T) {
	t.Parallel()
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:                150,
		LowConfidenceSizeFloorFraction: 0.25,
		MaxEffectiveLeverage:           3,
		EVExpectedSlippageBps:          3,
	}
	now := time.Now().UTC()
	opp := CrossVenueOpportunity{
		Symbol:               "H-USDT",
		HighVenue:            bybit.VenueName,
		LowVenue:             okx.VenueName,
		HighFundingRate:      0.05,
		LowFundingRate:       -0.01,
		HighIntervalHours:    8,
		LowIntervalHours:     8,
		HighNextFundingTime:  now.Add(time.Hour),
		LowNextFundingTime:   now.Add(time.Hour),
		TradableNotional:     147.65,
		ExtremeSpread:        true,
		SpreadAPR:            13.58,
		HistoryQualityScore:  0,
		StabilityScore:       0,
		ExecutionScore:       0.70,
		LiquidityScore:       0.80,
		RecoveryQualityScore: 0.65,
		CapitalEfficiencyEV:  0.50,
	}
	opp = crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	opp.HistoryQualityScore = 0
	opp.StabilityScore = 0
	opp.ExtremeSpread = true
	opp.TradableNotional = 147.65
	opp.NetEV = 5.0 // 保证 finalize 不因 net_ev<=0 清零

	ranked := []CrossVenueOpportunity{opp}
	crossVenueFinalizeOpportunityRanking(cfg, ranked)

	baseMult := clamp01((clamp01(ranked[0].CapitalEfficiencyEV) + ranked[0].ExecutionScore + ranked[0].LiquidityScore + ranked[0].RecoveryQualityScore) / 4)
	flooredMult := clamp01(baseMult * crossVenueConfidenceSizeMultiplier(0.25, 0, 0))
	if ranked[0].SizeMultiplier < baseMult*0.95 {
		t.Fatalf("extreme spread should bypass confMult: SizeMultiplier=%.4f baseMult=%.4f", ranked[0].SizeMultiplier, baseMult)
	}
	if ranked[0].SizeMultiplier <= flooredMult*1.5 {
		t.Fatalf("extreme spread SizeMultiplier=%.4f should exceed floored=%.4f (old bug≈0.28)", ranked[0].SizeMultiplier, flooredMult)
	}
	if ranked[0].TradableNotional < 70 {
		t.Fatalf("extreme spread notional=%.2f want >=70 (old confMult bug≈41)", ranked[0].TradableNotional)
	}
}

func TestNonExtremeStillConfidenceSized(t *testing.T) {
	t.Parallel()
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:                100,
		LowConfidenceSizeFloorFraction: 0.25,
		MaxEffectiveLeverage:           3,
		EVExpectedSlippageBps:          3,
	}
	now := time.Now().UTC()
	opp := CrossVenueOpportunity{
		Symbol:               "NEW-USDT",
		HighVenue:            bybit.VenueName,
		LowVenue:             okx.VenueName,
		HighFundingRate:      0.002,
		LowFundingRate:       0.0001,
		HighIntervalHours:    8,
		LowIntervalHours:     8,
		HighNextFundingTime:  now.Add(time.Hour),
		LowNextFundingTime:   now.Add(time.Hour),
		TradableNotional:     100,
		ExtremeSpread:        false,
		SpreadAPR:            0.08,
		HistoryQualityScore:  0,
		StabilityScore:       0,
		ExecutionScore:       0.70,
		LiquidityScore:       0.45,
		RecoveryQualityScore: 0.65,
	}
	opp = crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	opp.HistoryQualityScore = 0
	opp.StabilityScore = 0
	opp.ExtremeSpread = false
	opp.SpreadAPR = 0.08
	opp.TradableNotional = 100
	opp.NetEV = 2.0

	ranked := []CrossVenueOpportunity{opp}
	crossVenueFinalizeOpportunityRanking(cfg, ranked)

	confMult := crossVenueConfidenceSizeMultiplier(0.25, 0, 0)
	baseMult := clamp01((clamp01(ranked[0].CapitalEfficiencyEV) + ranked[0].ExecutionScore + ranked[0].LiquidityScore + ranked[0].RecoveryQualityScore) / 4)
	wantMult := clamp01(baseMult * confMult)
	if math.Abs(ranked[0].SizeMultiplier-wantMult) > 0.05 {
		t.Fatalf("non-extreme SizeMultiplier=%.4f want≈%.4f (baseMult*confMult)", ranked[0].SizeMultiplier, wantMult)
	}
	if ranked[0].SizeMultiplier > 0.30 {
		t.Fatalf("non-extreme low-history should stay cautious: SizeMultiplier=%.4f", ranked[0].SizeMultiplier)
	}
}

func TestSpreadAPRThresholdBypassesConfidenceDownsize(t *testing.T) {
	t.Parallel()
	opp := CrossVenueOpportunity{ExtremeSpread: false, SpreadAPR: 3.0}
	if !crossVenueBypassesConfidenceDownsize(opp) {
		t.Fatal("SpreadAPR above threshold should bypass confidence downsize")
	}
	opp.SpreadAPR = 1.0
	if crossVenueBypassesConfidenceDownsize(opp) {
		t.Fatal("moderate spread without ExtremeSpread flag should not bypass")
	}
}
