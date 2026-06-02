package main

import (
	"math"
	"testing"
	"time"
)

func TestAdverseCloseSpreadRateRespectsDirection(t *testing.T) {
	spotTicker := BookTicker{
		BidPrice: "99",
		AskPrice: "101",
	}
	futuresTicker := BookTicker{
		BidPrice: "100",
		AskPrice: "102",
	}

	positiveSpread, ok := adverseCloseSpreadRate(fundingCarryDirectionPositive, spotTicker, futuresTicker)
	if !ok {
		t.Fatal("expected positive carry spread to be valid")
	}
	if math.Abs(positiveSpread-(3.0/99.0)) > 1e-9 {
		t.Fatalf("unexpected positive carry spread: got %.12f", positiveSpread)
	}

	negativeSpread, ok := adverseCloseSpreadRate(fundingCarryDirectionNegative, spotTicker, futuresTicker)
	if !ok {
		t.Fatal("expected negative carry spread to be valid")
	}
	if math.Abs(negativeSpread-0.01) > 1e-9 {
		t.Fatalf("unexpected negative carry spread: got %.12f", negativeSpread)
	}
}

func TestEstimateExpectedFundingIncomeRespectsNegativeCarry(t *testing.T) {
	hedge := OpenHedge{
		Target: FundingCandidate{
			Direction:     fundingCarryDirectionNegative,
			EstimatedRate: -0.01,
			MarkPrice:     100,
		},
		Direction:           fundingCarryDirectionNegative,
		HedgeQty:            2,
		OpenFuturesAvgPrice: 100,
	}

	income := estimateExpectedFundingIncome(hedge)
	if math.Abs(income-2) > 1e-9 {
		t.Fatalf("expected negative carry funding income to be positive 2, got %.12f", income)
	}
}

// TestSettlementBorrowCostChargesNegativeCarryOnly 钉死 H3：结算时负 carry 必须按实际持仓期计入真实借币利息，
// 正 carry 不借币计 0；且优先使用真实 per-asset 小时利率。
func TestSettlementBorrowCostChargesNegativeCarryOnly(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	engine.cfg.HistoricalConfig.AssumedHourlyBorrowInterestRate = 0.001
	engine.cfg.HistoricalConfig.BorrowInterestMinBillingHours = 1

	// 使用毫秒对齐的固定时间，避免 UnixMilli 截断导致时长略超 3h 而被 ceil 成 4 小时。
	closeTime := time.UnixMilli(1700000000000).UTC()
	openTime := closeTime.Add(-3 * time.Hour)

	negative := OpenHedge{
		Direction:        fundingCarryDirectionNegative,
		Target:           FundingCandidate{SpotBaseAsset: "ABC"},
		OpenSpotQuoteQty: 100,
		SpotOrder:        SpotOrderResponse{TransactTime: openTime.UnixMilli()},
	}
	// 默认假设利率：100 名义 × 0.001/h × ceil(3h)=3 → 0.3
	if got := engine.settlementBorrowCost(negative, closeTime); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("负 carry 借币利息(默认利率)期望 0.3，实际 %.8f", got)
	}

	// 真实 per-asset 利率应覆盖默认值：100 × 0.002 × 3 → 0.6
	engine.borrowHourlyInterestByAsset = map[string]float64{"ABC": 0.002}
	if got := engine.settlementBorrowCost(negative, closeTime); math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("负 carry 借币利息(真实利率)期望 0.6，实际 %.8f", got)
	}

	positive := OpenHedge{
		Direction:        fundingCarryDirectionPositive,
		Target:           FundingCandidate{SpotBaseAsset: "ABC"},
		OpenSpotQuoteQty: 100,
		SpotOrder:        SpotOrderResponse{TransactTime: openTime.UnixMilli()},
	}
	if got := engine.settlementBorrowCost(positive, closeTime); got != 0 {
		t.Fatalf("正 carry 不借币，期望 0，实际 %.8f", got)
	}
}

func TestNegativeCarryLiveSmokeRoute(t *testing.T) {
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:        "BTCUSDT",
			Pair:          "BTCUSDT",
			Direction:     fundingCarryDirectionNegative,
			EstimatedRate: -0.01,
			OrderQuantity: 0.5,
		},
	}
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = false

	order := engine.buildProtectedOpenFuturesOrder(target)
	if order.Side != "BUY" {
		t.Fatalf("expected negative carry futures open side BUY, got %s", order.Side)
	}
	if got := target.Candidate.Direction.openSpotSide(); got != "SELL" {
		t.Fatalf("expected negative carry spot open side SELL, got %s", got)
	}
	if got := target.Candidate.Direction.spotVenue(); got != spotExecutionVenueCrossMargin {
		t.Fatalf("expected negative carry spot venue cross margin, got %s", got)
	}
}
