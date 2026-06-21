package app

import (
	"math"
	"testing"
)

func TestCrossVenueApplyAcceptableFallbackScalesLinearEconomics(t *testing.T) {
	opp := CrossVenueOpportunity{
		Symbol:                "BTC-USDT",
		HighVenue:             "bybit",
		LowVenue:              "okx",
		NetEV:                 4.0,
		CapitalEfficiencyEV:   0.5,
		RiskAdjustedEVPerHour: 1.0,
		SpreadEV:              6.0,
		OpenCost:              0.5,
		CloseCost:             0.5,
		ExpectedSlippage:      0.3,
		ObservableRiskCost:    0.4,
		PolicyBuffer:          0.3,
		RankScore:             4.0,
		SizeMultiplier:        1,
		TradableNotional:      20,
		TradableQty:           0.002,
		ExecutionScore:        0.70,
		RecoveryQualityScore:  0.65,
		LiquidityScore:        0.45,
	}
	orig := opp

	got := crossVenueApplyAcceptableFallback(opp)

	if got.TradableNotional != 10 {
		t.Fatalf("TradableNotional=%.2f want 10", got.TradableNotional)
	}
	if got.TradableQty != 0.001 {
		t.Fatalf("TradableQty=%.6f want 0.001", got.TradableQty)
	}
	if got.NoTradeReason != "acceptable_fallback" {
		t.Fatalf("NoTradeReason=%q want acceptable_fallback", got.NoTradeReason)
	}

	const ratio = 0.5
	checkRatio := func(name string, gotVal, origVal float64) {
		t.Helper()
		if origVal == 0 {
			return
		}
		if math.Abs(gotVal/origVal-ratio) > 1e-9 {
			t.Fatalf("%s ratio=%.6f want %.6f (got=%.6f orig=%.6f)", name, gotVal/origVal, ratio, gotVal, origVal)
		}
	}
	checkRatio("SpreadEV", got.SpreadEV, orig.SpreadEV)
	checkRatio("OpenCost", got.OpenCost, orig.OpenCost)
	checkRatio("CloseCost", got.CloseCost, orig.CloseCost)
	checkRatio("ExpectedSlippage", got.ExpectedSlippage, orig.ExpectedSlippage)
	checkRatio("ObservableRiskCost", got.ObservableRiskCost, orig.ObservableRiskCost)
	checkRatio("PolicyBuffer", got.PolicyBuffer, orig.PolicyBuffer)
	checkRatio("NetEV", got.NetEV, orig.NetEV)
	checkRatio("RiskAdjustedEVPerHour", got.RiskAdjustedEVPerHour, orig.RiskAdjustedEVPerHour)
	if math.Abs(got.RankScore-got.NetEV) > 1e-12 {
		t.Fatalf("RankScore=%.6f NetEV=%.6f want equal after fallback shrink", got.RankScore, got.NetEV)
	}
	if math.Abs(got.CapitalEfficiencyEV-orig.CapitalEfficiencyEV) > 1e-12 {
		t.Fatalf("CapitalEfficiencyEV=%.6f want ratio-invariant %.6f", got.CapitalEfficiencyEV, orig.CapitalEfficiencyEV)
	}
}
