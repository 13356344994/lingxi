package app

import (
	"testing"
	"time"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestUnifiedOpportunityFromCrossVenueMapsFields(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cvCfg := cfg.CrossVenueExecutor
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	opp := makeTestOpenOpportunity(cvCfg, now, "BTC-USDT", bybit.VenueName, okx.VenueName, 0.001, 0.0002, 100, 0.8, 0.7, 0.75, 0.85)
	opp.SnapshotID = "cvsnap-test"
	opp.OpenCost = 0.11
	opp.CloseCost = 0.11
	opp.ExpectedSlippage = 0.12
	opp.SpreadEV = 0.55
	opp.NetEV = 0.21
	opp.ExpectedHoldHours = 4
	opp.CapitalEfficiencyEV = 0.003
	opp.TradableQty = 0.001

	unified := UnifiedOpportunityFromCrossVenue(cvCfg, opp, []string{"EV<0"}, 100, 100000)

	if unified.StrategyType != UnifiedStrategyPerpPerp {
		t.Fatalf("strategy_type = %q, want %q", unified.StrategyType, UnifiedStrategyPerpPerp)
	}
	if unified.ID != "perp_perp:BTC-USDT:bybit+okx:cvsnap-test" {
		t.Fatalf("id = %q", unified.ID)
	}
	if unified.Symbol != "BTC-USDT" {
		t.Fatalf("symbol = %q", unified.Symbol)
	}
	if len(unified.Legs) != 2 {
		t.Fatalf("legs len = %d, want 2", len(unified.Legs))
	}
	if unified.Legs[0].Venue != bybit.VenueName || unified.Legs[0].Side != "short" || unified.Legs[0].Market != "perp" {
		t.Fatalf("high leg = %+v", unified.Legs[0])
	}
	if unified.Legs[1].Venue != okx.VenueName || unified.Legs[1].Side != "long" {
		t.Fatalf("low leg = %+v", unified.Legs[1])
	}
	if unified.Legs[0].Notional != 100 || unified.Legs[0].Qty != 0.001 {
		t.Fatalf("high leg notional/qty = %.4f/%.6f", unified.Legs[0].Notional, unified.Legs[0].Qty)
	}
	if unified.ExpectedFundingUSDT != 0.55 || unified.ExpectedOpenCostUSDT != 0.11 || unified.ExpectedCloseCostUSDT != 0.11 {
		t.Fatalf("cost fields = funding %.4f open %.4f close %.4f", unified.ExpectedFundingUSDT, unified.ExpectedOpenCostUSDT, unified.ExpectedCloseCostUSDT)
	}
	if unified.ExpectedSlippageUSDT != 0.12 || unified.ExpectedBorrowCostUSDT != 0 || unified.ExpectedRollbackCostUSDT != 0 {
		t.Fatalf("slippage/borrow/rollback = %.4f/%.4f/%.4f", unified.ExpectedSlippageUSDT, unified.ExpectedBorrowCostUSDT, unified.ExpectedRollbackCostUSDT)
	}
	if unified.ExpectedNetEVUSDT != 0.21 || unified.ExpectedHoldHours != 4 {
		t.Fatalf("net_ev/hold = %.4f/%.1f", unified.ExpectedNetEVUSDT, unified.ExpectedHoldHours)
	}
	perVenue := crossVenuePerVenueMarginUSDT(cvCfg, 100)
	if unified.CapitalRequiredByVenue[bybit.VenueName] != perVenue || unified.CapitalRequiredByVenue[okx.VenueName] != perVenue {
		t.Fatalf("capital_required_by_venue = %+v, want %.4f each", unified.CapitalRequiredByVenue, perVenue)
	}
	if unified.ExecutionScore != 0.85 || unified.LiquidityScore != 0.7 || unified.DataQualityScore != 0.8 {
		t.Fatalf("scores = exec %.2f liq %.2f data %.2f", unified.ExecutionScore, unified.LiquidityScore, unified.DataQualityScore)
	}
	if unified.CapitalEfficiencyScore != 0.003 {
		t.Fatalf("capital_efficiency_score = %.4f", unified.CapitalEfficiencyScore)
	}
	if len(unified.RejectReasons) != 1 || unified.RejectReasons[0] != "EV<0" {
		t.Fatalf("reject_reasons = %#v", unified.RejectReasons)
	}
	if unified.RiskScore <= 0 || unified.RiskScore > 1 {
		t.Fatalf("risk_score = %.4f, want (0,1]", unified.RiskScore)
	}
}

func TestUnifiedOpportunityFromCrossVenueDoesNotMutateInput(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cvCfg := cfg.CrossVenueExecutor
	now := time.Now().UTC()
	opp := makeTestOpenOpportunity(cvCfg, now, "ETH-USDT", bybit.VenueName, okx.VenueName, 0.001, 0.0002, 50, 0.5, 0.5, 0.5, 0.5)
	before := opp
	_ = UnifiedOpportunityFromCrossVenue(cvCfg, opp, nil, 50, 3000)
	if opp != before {
		t.Fatal("UnifiedOpportunityFromCrossVenue mutated input CrossVenueOpportunity")
	}
}
