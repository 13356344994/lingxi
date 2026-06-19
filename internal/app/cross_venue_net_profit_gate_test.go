package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/okx"
)

func testNetProfitGateExecutorConfig() CrossVenueExecutorConfig {
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg.OpenSpreadAPR = 0
	cfg.MinNetProfitUSDT = 0
	cfg.EVExpectedSlippageBps = 1
	cfg.SlippageBudgetBps = 0
	cfg.MaxPositionUSDT = 150
	return cfg
}

func TestEvaluateCrossVenueOpenEVModestSpreadPositiveNetProfitPasses(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	notional := 150.0
	opp := CrossVenueOpportunity{
		Symbol:              "BTC-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		SpreadAPR:           0.40,
		HighFundingRate:     0.004,
		LowFundingRate:      0.0001,
		HighIntervalHours:   8,
		LowIntervalHours:    8,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
	}
	holdHours := crossVenueComparableWindowHours(now, nextFunding, nextFunding)
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, holdHours)
	if !res.OK {
		t.Fatalf("expected pass for modest spread with positive 1-settlement net profit: %s", res.Reason)
	}
}

func TestEvaluateCrossVenueOpenEVNegativeNetProfitRejected(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	notional := 150.0
	opp := CrossVenueOpportunity{
		Symbol:              "BTC-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		SpreadAPR:           0.40,
		HighFundingRate:     0.00005,
		LowFundingRate:      0.00004,
		HighIntervalHours:   8,
		LowIntervalHours:    8,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
	}
	holdHours := crossVenueComparableWindowHours(now, nextFunding, nextFunding)
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, holdHours)
	if res.OK {
		t.Fatal("expected reject when funding over hold does not cover round-trip cost")
	}
	if res.Reason == "" || res.Reason[:4] != "EV<0" {
		t.Fatalf("unexpected reason: %s", res.Reason)
	}
}

func TestCrossVenueOpenGateModestSpreadPassesWhenNetProfitPositive(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.004, 8, nextFunding)
	high.book = venueBookTight()
	high.depth = venueDepthDeep()
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venueBookTight()
	low.depth = venueDepthDeep()
	cfg := testNetProfitGateExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), map[string]venue.Venue{
		binance.VenueName: high,
		okx.VenueName:     low,
	}, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 0.40, HighFundingRate: 0.004, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
			HighAnnualizedAPR: 4.0, LowAnnualizedAPR: 0.1,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("expected gate pass (would_execute): %s", res.Reason)
	}
}

func TestCrossVenueOpenGateBlocksWhenOpenSpreadAPRConfigured(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.004, 8, nextFunding)
	high.book = venueBookTight()
	high.depth = venueDepthDeep()
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venueBookTight()
	low.depth = venueDepthDeep()
	cfg := testNetProfitGateExecutorConfig()
	cfg.OpenSpreadAPR = 1.5
	res := evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), map[string]venue.Venue{
		binance.VenueName: high,
		okx.VenueName:     low,
	}, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 0.40, HighFundingRate: 0.004, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	})
	if res.OK || !strings.Contains(res.Reason, "开仓阈值") {
		t.Fatalf("expected optional spread floor reject, got ok=%v reason=%q", res.OK, res.Reason)
	}
}

func TestCrossVenueFinalizeRankingOrdersByNetEV(t *testing.T) {
	cfg := testNetProfitGateExecutorConfig()
	now := time.Now().UTC().Add(time.Hour)
	next := now.Add(2 * time.Hour)
	makeOpp := func(symbol string, highRate float64) CrossVenueOpportunity {
		opp := CrossVenueOpportunity{
			Symbol: symbol, HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			HighFundingRate: highRate, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
			HighNextFundingTime: next, LowNextFundingTime: next,
			TradableNotional: 150,
		}
		return crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	}
	opps := []CrossVenueOpportunity{makeOpp("LOW", 0.003), makeOpp("HIGH", 0.006)}
	crossVenueFinalizeOpportunityRanking(cfg, opps)
	if opps[0].NetEV >= opps[1].NetEV {
		t.Fatalf("expected HIGH net ev > LOW before sort, got %.4f vs %.4f", opps[0].NetEV, opps[1].NetEV)
	}
	sortCrossVenueOpportunitiesByRank(opps)
	if opps[0].Symbol != "HIGH" {
		t.Fatalf("expected highest net ev first, got %s (scores %.4f, %.4f)", opps[0].Symbol, opps[0].RankScore, opps[1].RankScore)
	}
	if math.Abs(opps[0].RankScore-opps[0].NetEV) > 1e-9 {
		t.Fatalf("RankScore should equal NetEV, got rank=%.6f net=%.6f", opps[0].RankScore, opps[0].NetEV)
	}
}

func TestPlanUnifiedRotationOpensRanksByExpectedNetEV(t *testing.T) {
	cfg := DefaultRotationConfig()
	cfg.MaxConcurrent = 2
	cfg.MinNetProfitUSDT = 0
	candidates := []UnifiedOpportunity{
		{ID: "low", Symbol: "AAA", StrategyType: UnifiedStrategyPerpPerp, ExpectedNetEVUSDT: 0.05, CapitalRequiredByVenue: map[string]float64{"okx": 20}},
		{ID: "high", Symbol: "BBB", StrategyType: UnifiedStrategyPerpPerp, ExpectedNetEVUSDT: 0.20, CapitalRequiredByVenue: map[string]float64{"okx": 20}},
	}
	available := map[string]float64{"okx": 100}
	actions := PlanUnifiedRotationOpens(candidates, available, cfg)
	if len(actions) != 2 {
		t.Fatalf("expected 2 opens, got %d", len(actions))
	}
	if actions[0].OpportunityID != "high" {
		t.Fatalf("expected highest net ev first, got %s score=%.4f", actions[0].OpportunityID, actions[0].Score)
	}
}

func venueBookTight() venue.BookTicker {
	return venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
}

func venueDepthDeep() venue.Depth {
	return venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 100}, {Price: 99.99, Qty: 100}},
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}, {Price: 100.02, Qty: 100}},
	}
}

func sortCrossVenueOpportunitiesByRank(opps []CrossVenueOpportunity) {
	for i := 0; i < len(opps); i++ {
		for j := i + 1; j < len(opps); j++ {
			if opps[j].RankScore > opps[i].RankScore {
				opps[i], opps[j] = opps[j], opps[i]
			}
		}
	}
}
