package main

import (
	"math"
	"testing"
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
