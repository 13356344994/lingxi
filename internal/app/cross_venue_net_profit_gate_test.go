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
	// 单测显式传 holdHours / 费率档位;不继承生产默认 expected_hold_hours / assume_maker_fills。
	cfg.ExpectedHoldHours = 0
	cfg.AssumeMakerFills = false
	return cfg
}

func testNetProfitGateMakerHoldConfig() CrossVenueExecutorConfig {
	cfg := testNetProfitGateExecutorConfig()
	cfg.AssumeMakerFills = true
	cfg.DefaultMakerFeeBps = 2
	cfg.ExpectedHoldHours = 12
	return cfg
}

func testCarryOpenEVOpportunity(now time.Time, nextFunding time.Time) CrossVenueOpportunity {
	// 典型高 carry 币(经扫描器平滑后 SpreadAPR 常落在 ~0.4–0.6 区间);费率取可覆盖 maker 往返 + 12h hold 的量级。
	const highRate = 0.004
	const lowRate = 0.0001
	const intervalHours = 8
	return CrossVenueOpportunity{
		Symbol:              "ALT-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		SpreadAPR:           fundingAnnualizedAPR(highRate, intervalHours) - fundingAnnualizedAPR(lowRate, intervalHours),
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   intervalHours,
		LowIntervalHours:    intervalHours,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
		HighAnnualizedAPR:   fundingAnnualizedAPR(highRate, intervalHours),
		LowAnnualizedAPR:    fundingAnnualizedAPR(lowRate, intervalHours),
	}
}

func TestEvaluateCrossVenueOpenEVSixtyAPRTakerOneSettlementRejected(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	cfg.AssumeMakerFills = false
	cfg.DefaultTakerFeeBps = 6
	cfg.ExpectedHoldHours = 0
	notional := 150.0
	const spreadAPR = 0.60
	const lowAPR = 0.10
	const highAPR = lowAPR + spreadAPR
	const intervalHours = 8
	highRate := highAPR * float64(intervalHours) / 8760.0
	lowRate := lowAPR * float64(intervalHours) / 8760.0
	opp := CrossVenueOpportunity{
		Symbol:              "ALT-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		SpreadAPR:           spreadAPR,
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   intervalHours,
		LowIntervalHours:    intervalHours,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
	}
	holdHours := crossVenueComparableWindowHours(now, nextFunding, nextFunding)
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, holdHours, 0)
	if res.OK {
		t.Fatal("expected reject for ~60% APR under taker fees + 1-settlement hold window")
	}
}

func TestEvaluateCrossVenueOpenEVUsesDiscreteFundingNotLinearExtrapolation(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	cfg.ExpectedHoldHours = 12
	notional := 150.0
	opp := testCarryOpenEVOpportunity(now, nextFunding)
	discrete := crossVenueOpenGateExpectedFundingUSDT(cfg, now, 12, notional, opp)
	linear := crossVenueExpectedFundingPerHour(notional, opp.HighFundingRate, opp.HighIntervalHours, opp.LowFundingRate, opp.LowIntervalHours) * 12
	if discrete >= linear {
		t.Fatalf("discrete funding %.6f should be below 12h linear %.6f", discrete, linear)
	}
}

func TestEvaluateCrossVenueOpenEVCarryCoinMakerTwelveHourHoldPasses(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateMakerHoldConfig()
	notional := 150.0
	opp := testCarryOpenEVOpportunity(now, nextFunding)
	// SpreadAPR≈4.2 为扫描器年化口径;对应 operator 可见的 ~40–60% 高 carry 档位(经平滑/封顶后 interval rate)。
	if opp.SpreadAPR < 0.40 {
		t.Fatalf("carry fixture spread_apr=%.2f expected ≥0.40", opp.SpreadAPR)
	}
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if !res.OK {
		t.Fatalf("expected pass for high carry coin under discrete funding + maker 12h hold: %s", res.Reason)
	}
}

func TestEvaluateCrossVenueOpenEVCarryCoinTakerShortHoldRejected(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	cfg.AssumeMakerFills = false
	cfg.DefaultTakerFeeBps = 6
	cfg.ExpectedHoldHours = 0
	notional := 150.0
	const spreadAPR = 0.60
	const lowAPR = 0.10
	const highAPR = lowAPR + spreadAPR
	const intervalHours = 8
	highRate := highAPR * float64(intervalHours) / 8760.0
	lowRate := lowAPR * float64(intervalHours) / 8760.0
	opp := CrossVenueOpportunity{
		Symbol:              "ALT-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		SpreadAPR:           spreadAPR,
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   intervalHours,
		LowIntervalHours:    intervalHours,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
	}
	holdHours := crossVenueComparableWindowHours(now, nextFunding, nextFunding)
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, holdHours, 0)
	if res.OK {
		t.Fatal("expected reject for ~60% APR coin under taker fees + short hold window")
	}
}

func TestEvaluateCrossVenueOpenEVWeakFundingMakerHoldStillRejected(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateMakerHoldConfig()
	notional := 150.0
	opp := CrossVenueOpportunity{
		Symbol:              "BTC-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            okx.VenueName,
		HighFundingRate:     0.00005,
		LowFundingRate:      0.00004,
		HighIntervalHours:   8,
		LowIntervalHours:    8,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
	}
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if res.OK {
		t.Fatal("expected reject when funding over expected hold does not cover maker round-trip")
	}
}

func TestCrossVenueOpenEVConfigTogglesExpectedHoldHoursAndMakerFees(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	notional := 150.0
	opp := testCarryOpenEVOpportunity(now, nextFunding)

	shortHold := testNetProfitGateMakerHoldConfig()
	shortHold.NearTermOpenEVHorizonEnabled = false
	shortHold.ExpectedHoldHours = 2
	if res := evaluateCrossVenueOpenEV(shortHold, opp, notional, nil, now, 0, 0); res.OK {
		t.Fatal("expected reject when expected_hold_hours shortened to 2h")
	}

	longHold := testNetProfitGateMakerHoldConfig()
	longHold.NearTermOpenEVHorizonEnabled = false
	longHold.ExpectedHoldHours = 12
	if res := evaluateCrossVenueOpenEV(longHold, opp, notional, nil, now, 0, 0); !res.OK {
		t.Fatalf("expected pass with 12h discrete settlements (not linear extrapolation): %s", res.Reason)
	}

	highMakerFee := testNetProfitGateMakerHoldConfig()
	highMakerFee.NearTermOpenEVHorizonEnabled = false
	highMakerFee.DefaultMakerFeeBps = 150
	highMakerFeeOpp := opp
	highMakerFeeOpp.HighVenue = "unknown-high"
	highMakerFeeOpp.LowVenue = "unknown-low"
	if res := evaluateCrossVenueOpenEV(highMakerFee, highMakerFeeOpp, notional, nil, now, 0, 0); res.OK {
		t.Fatal("expected reject when default_maker_fee_bps raised to 150")
	}

	takerCfg := testNetProfitGateMakerHoldConfig()
	takerCfg.NearTermOpenEVHorizonEnabled = false
	takerCfg.AssumeMakerFills = false
	takerCfg.DefaultTakerFeeBps = 6
	const intervalHours = 8
	const lowAPR = 0.10
	highRate := (lowAPR + 0.60) * float64(intervalHours) / 8760.0
	lowRate := lowAPR * float64(intervalHours) / 8760.0
	takerCfgOpp := opp
	takerCfgOpp.SpreadAPR = 0.60
	takerCfgOpp.HighFundingRate = highRate
	takerCfgOpp.LowFundingRate = lowRate
	if res := evaluateCrossVenueOpenEV(takerCfg, takerCfgOpp, notional, nil, now, 0, 0); res.OK {
		t.Fatal("expected reject when assume_maker_fills=false with taker fee tier on ~60% APR coin")
	}
}

func TestEvaluateCrossVenueOpenEVModestSpreadPositiveNetProfitPasses(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(2 * time.Hour)
	cfg := testNetProfitGateExecutorConfig()
	cfg.AssumeMakerFills = true
	cfg.ExpectedHoldHours = 12
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
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, 0, 0)
	if !res.OK {
		t.Fatalf("expected pass for modest spread when discrete funding covers maker round-trip: %s", res.Reason)
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
	res := evaluateCrossVenueOpenEV(cfg, opp, notional, nil, now, holdHours, 0)
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
	cfg.AssumeMakerFills = true
	cfg.ExpectedHoldHours = 12
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
		t.Fatalf("expected gate pass when net profit positive under discrete EV: %s", res.Reason)
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
	opps := []CrossVenueOpportunity{makeOpp("LOW", 0.01), makeOpp("HIGH", 0.02)}
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
