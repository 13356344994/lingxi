package app

import (
	"testing"
	"time"
)

func TestIsTrustedSettlement_Boundary(t *testing.T) {
	boundary := attributionFixBoundaryTime
	before := boundary.Add(-time.Second)
	after := boundary.Add(time.Second)

	if isTrustedSettlement(settlementTrustInput{CloseTime: before}) {
		t.Fatal("close before boundary should be untrusted")
	}
	if !isTrustedSettlement(settlementTrustInput{CloseTime: boundary}) {
		t.Fatal("close at boundary should be trusted")
	}
	if !isTrustedSettlement(settlementTrustInput{CloseTime: after}) {
		t.Fatal("close after boundary should be trusted")
	}
	if isTrustedSettlement(settlementTrustInput{}) {
		t.Fatal("zero close_time should be untrusted")
	}
}

func TestIsTrustedSettlement_MissingFundingTruthStatus(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	in := settlementTrustInput{
		CloseTime:               after,
		CheckFundingTruthStatus: true,
		FundingTruthStatus:      "",
	}
	if isTrustedSettlement(in) {
		t.Fatal("empty funding_truth_status should be untrusted when checked")
	}
	in.FundingTruthStatus = "complete"
	if !isTrustedSettlement(in) {
		t.Fatal("non-empty funding_truth_status should be trusted")
	}
}

func TestIsTrustedSettlement_AttributionIncomplete(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	in := settlementTrustInput{
		CloseTime:                  after,
		CheckAttributionIncomplete: true,
		AttributionIncomplete:      true,
	}
	if isTrustedSettlement(in) {
		t.Fatal("attribution_incomplete should be untrusted when checked")
	}
	in.AttributionIncomplete = false
	if !isTrustedSettlement(in) {
		t.Fatal("attribution complete should be trusted")
	}
}

func TestIsTrustedSettlement_AbnormalTradePnL(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	in := settlementTrustInput{
		CloseTime:     after,
		CheckTradePnL: true,
		TradePnL:      -801.56,
	}
	if isTrustedSettlement(in) {
		t.Fatal("HEI-like |trade_pnl|>50 should be untrusted")
	}
	in.TradePnL = -50
	if !isTrustedSettlement(in) {
		t.Fatal("|trade_pnl|=50 should be trusted (strictly > threshold)")
	}
	in.TradePnL = 50.01
	if isTrustedSettlement(in) {
		t.Fatal("|trade_pnl|=50.01 should be untrusted")
	}
}

func TestIsTrustedSettlement_MissingTradePnLSafe(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	if !isTrustedSettlement(settlementTrustInput{CloseTime: after, CheckTradePnL: false}) {
		t.Fatal("missing trade_pnl field should not fail when CheckTradePnL=false")
	}
}

func TestIsTrustedCrossVenueSettlement_HEIExcluded(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	rec := CrossVenueSettlement{
		CloseTime:          after,
		FundingTruthStatus: "complete",
		TradePnL:           -801.56,
		NetPnL:             -801.56,
	}
	if isTrustedSettlement(crossVenueSettlementTrustInput(rec)) {
		t.Fatal("HEI-like crossvenue settlement should be untrusted")
	}
}

func TestIsTrustedCrossVenueSettlement_PostFixTrusted(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	rec := CrossVenueSettlement{
		CloseTime:          after,
		FundingTruthStatus: "complete",
		TradePnL:           0.3,
		NetPnL:             1.1,
	}
	if !isTrustedSettlement(crossVenueSettlementTrustInput(rec)) {
		t.Fatal("post-fix attribution-complete settlement should be trusted")
	}
}

func TestComputeRealizedPnLWaterMark_ExcludesUntrustedLegacy(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	trusted := strategySettlementRecord{
		SettlementID:       "ok",
		StrategyType:       UnifiedStrategyPerpPerp,
		CloseTime:          after,
		NetPnL:             10,
		FundingTruthStatus: "complete",
	}
	hei := strategySettlementRecord{
		SettlementID:       "hei",
		StrategyType:       UnifiedStrategyPerpPerp,
		CloseTime:          after,
		NetPnL:             -801.56,
		FundingTruthStatus: "complete",
		TradePnL:           -801.56,
	}
	cumulative, peak, drawdown := computeRealizedPnLWaterMark([]strategySettlementRecord{trusted, hei})
	if cumulative != 10 || peak != 10 || drawdown != 0 {
		t.Fatalf("watermark cumulative=%v peak=%v drawdown=%v, want 10/10/0 (HEI excluded)", cumulative, peak, drawdown)
	}
}

func TestFilterAlphaRealizedSamples_ExcludesHEIAndLegacy(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	before := attributionFixBoundaryTime.Add(-time.Hour)
	samples := []AttributionRealizedRecord{
		{
			OpportunityID:       "legacy",
			RunMode:             string(AttributionRunModeProfitSeeking),
			Strategy:            UnifiedStrategyPerpPerp,
			CloseTime:           before,
			FundingTruthStatus:  "complete",
			AttributionComplete: true,
			FinalNetPnLUSDT:     1,
		},
		{
			OpportunityID:        "hei",
			RunMode:              string(AttributionRunModeProfitSeeking),
			Strategy:             UnifiedStrategyPerpPerp,
			CloseTime:            after,
			FundingTruthStatus:   "complete",
			AttributionComplete:  true,
			RealizedTradePnLUSDT: -801.56,
			FinalNetPnLUSDT:      -801.56,
		},
		{
			OpportunityID:        "good",
			RunMode:              string(AttributionRunModeProfitSeeking),
			Strategy:             UnifiedStrategyPerpPerp,
			CloseTime:            after,
			FundingTruthStatus:   "complete",
			AttributionComplete:  true,
			RealizedTradePnLUSDT: 0.2,
			FinalNetPnLUSDT:      0.5,
		},
	}
	alpha := filterAlphaRealizedSamples(samples)
	if len(alpha) != 1 || alpha[0].OpportunityID != "good" {
		t.Fatalf("alpha samples = %+v, want only good", alpha)
	}
}

func TestAggregateStrategySettlements_UntrustedBucket(t *testing.T) {
	after := attributionFixBoundaryTime.Add(time.Hour)
	sum := aggregateStrategySettlementsInRange([]strategySettlementRecord{
		{
			SettlementID: "trusted",
			StrategyType: UnifiedStrategyCrossVenueSpotPerp,
			CloseTime:    after,
			NetPnL:       2,
		},
		{
			SettlementID:       "hei",
			StrategyType:       UnifiedStrategyPerpPerp,
			CloseTime:          after,
			FundingTruthStatus: "complete",
			TradePnL:           -801.56,
			NetPnL:             -801.56,
		},
	}, time.Time{}, after)
	if sum.TotalNetPnL != 2 {
		t.Fatalf("trusted total=%v want 2", sum.TotalNetPnL)
	}
	if sum.UntrustedNetPnL != -801.56 {
		t.Fatalf("untrusted total=%v want -801.56", sum.UntrustedNetPnL)
	}
	if sum.TradeCount != 1 || sum.UntrustedTradeCount != 1 {
		t.Fatalf("trade counts trusted=%d untrusted=%d want 1/1", sum.TradeCount, sum.UntrustedTradeCount)
	}
}
