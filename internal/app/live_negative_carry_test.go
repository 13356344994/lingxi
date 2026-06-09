package app

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

// TestSettlementBorrowCostIncrementalAnchor 钉死 FIX_PLAN #2:滚动持有下借币利息按「上次结算锚点→平仓」增量计,
// 不再每轮从开仓时点全额重记。单周期(锚点=0)口径与开仓→平仓全额一致。
func TestSettlementBorrowCostIncrementalAnchor(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	engine.cfg.HistoricalConfig.AssumedHourlyBorrowInterestRate = 0.001
	engine.cfg.HistoricalConfig.BorrowInterestMinBillingHours = 1

	closeTime := time.UnixMilli(1700000000000).UTC()
	openTime := closeTime.Add(-24 * time.Hour) // 持有 24h
	anchor := closeTime.Add(-8 * time.Hour)    // 上次结算在平仓前 8h

	base := OpenHedge{
		Direction:        fundingCarryDirectionNegative,
		Target:           FundingCandidate{SpotBaseAsset: "ABC"},
		OpenSpotQuoteQty: 100,
		SpotOrder:        SpotOrderResponse{TransactTime: openTime.UnixMilli()},
	}

	// 无锚点(单周期/首结算):100 × 0.001 × ceil(24h)=24 → 2.4(行为不变)。
	if got := engine.settlementBorrowCost(base, closeTime); math.Abs(got-2.4) > 1e-9 {
		t.Fatalf("无锚点期望全额 2.4,实际 %.8f", got)
	}

	// 有锚点(滚动 keep 后):只计 anchor→close 的 8h → 100 × 0.001 × 8 = 0.8,而非从开仓的 2.4。
	withAnchor := base
	withAnchor.BorrowSettledThroughMs = anchor.UnixMilli()
	if got := engine.settlementBorrowCost(withAnchor, closeTime); math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("有锚点应只计增量 8h=0.8,实际 %.8f(过去的 bug 会得到 2.4)", got)
	}

	// 锚点早于开仓时点(异常/旧数据):退回从开仓时点起算,不放大。
	stale := base
	stale.BorrowSettledThroughMs = openTime.Add(-5 * time.Hour).UnixMilli()
	if got := engine.settlementBorrowCost(stale, closeTime); math.Abs(got-2.4) > 1e-9 {
		t.Fatalf("锚点早于开仓应退回全额 2.4,实际 %.8f", got)
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
