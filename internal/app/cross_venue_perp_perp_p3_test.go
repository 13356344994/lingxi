package app

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func TestPerpPerpNoTop30Truncation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	deepDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 1, Qty: 100000}},
		Bids: []venue.DepthLevel{{Price: 1, Qty: 100000}},
	}
	makeSnaps := func(highRate float64) []venue.FundingSnapshot {
		out := make([]venue.FundingSnapshot, 0, 35)
		for i := 0; i < 35; i++ {
			out = append(out, venue.FundingSnapshot{
				Symbol:          venue.Symbol(fmt.Sprintf("SYM%d-USDT", i)),
				LastFundingRate: highRate + float64(i)*1e-6,
				IntervalHours:   8,
				NextFundingTime: nextFunding,
			})
		}
		return out
	}
	lowSnaps := make([]venue.FundingSnapshot, 35)
	for i := 0; i < 35; i++ {
		lowSnaps[i] = venue.FundingSnapshot{
			Symbol:          venue.Symbol(fmt.Sprintf("SYM%d-USDT", i)),
			LastFundingRate: 0.00001,
			IntervalHours:   8,
			NextFundingTime: nextFunding,
		}
	}
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: makeSnaps(0.001),
		bybit.VenueName:   lowSnaps,
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{MinSpreadAPR: 0.01, TopN: 30},
		execCfg: CrossVenueExecutorConfig{
			MaxPositionUSDT:       150,
			MaxEffectiveLeverage:  2,
			MarginSafetyFactor:    1.5,
			EVExpectedSlippageBps: 3,
		},
		venues: map[string]venue.Venue{
			binance.VenueName: &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, depth: deepDepth},
			bybit.VenueName:   &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}, depth: deepDepth},
		},
		nowFn:          func() time.Time { return now },
		fundingHistory: make(map[string]map[string][]float64),
	}
	s.recordFundingHistory(byVenue)
	opps := s.computeCrossVenueOpportunities(context.Background(), byVenue)
	if len(opps) <= 30 {
		t.Fatalf("expected full set >30 opportunities, got %d", len(opps))
	}
}

func TestPerpPerpNotionalSearchPicksMaxNetEV(t *testing.T) {
	t.Parallel()
	// 浅层 $60 流动性:50U 滑点低,150U 穿价后滑点/成本主导 netEV。
	tieredDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 1, Qty: 60}, {Price: 2, Qty: 100000}},
		Bids: []venue.DepthLevel{{Price: 1, Qty: 60}, {Price: 0.5, Qty: 100000}},
	}
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:       150,
		MaxEffectiveLeverage:  2,
		MarginSafetyFactor:    1.5,
		SlippageBudgetBps:     20,
		DefaultTakerFeeBps:    6,
		MinNetProfitUSDT:      0,
		ExpectedHoldHours:     12,
		SizingMaxSlippageBps:  20,
	}
	opp := CrossVenueOpportunity{
		Symbol:              "H-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		HighFundingRate:     0.00625,
		LowFundingRate:      0.0000125,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
		SpreadAPR:           6.0,
	}
	fetchDepth := func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error) {
		return tieredDepth, nil
	}
	highV := &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, depth: tieredDepth}
	lowV := &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}, depth: tieredDepth}
	slipLegs := crossVenuePerpPerpEVSlippageLegs(fetchDepth, context.Background(), highV, lowV, venue.Symbol("H-USDT"), 1, 1)
	clamp := crossVenuePerpPerpNotionalClampCtx(context.Background(), map[string]venue.Venue{
		binance.VenueName: highV,
		bybit.VenueName:   lowV,
	}, opp, slipLegs)
	bestOpp, chosen := crossVenuePerpPerpSearchBestNotional(cfg, opp, now, opp.HighFundingRate, opp.LowFundingRate, 150, slipLegs, clamp)
	if chosen >= 150-1e-6 {
		t.Fatalf("chosen_notional=%.2f want <150 when large size walks the book", chosen)
	}
	if bestOpp.NetEV <= 0 {
		t.Fatalf("best netEV=%.4f want >0 at smaller notional", bestOpp.NetEV)
	}
	at150 := crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, CrossVenueOpportunity{TradableNotional: 150, HighVenue: opp.HighVenue, LowVenue: opp.LowVenue, HighFundingRate: opp.HighFundingRate, LowFundingRate: opp.LowFundingRate, HighIntervalHours: opp.HighIntervalHours, LowIntervalHours: opp.LowIntervalHours, HighNextFundingTime: opp.HighNextFundingTime, LowNextFundingTime: opp.LowNextFundingTime}, now, opp.HighFundingRate, opp.LowFundingRate, slipLegs)
	if at150.NetEV >= bestOpp.NetEV {
		t.Fatalf("search picked netEV=%.4f but fixed150=%.4f", bestOpp.NetEV, at150.NetEV)
	}
}

func TestPerpPerpScanSlippageMatchesGate(t *testing.T) {
	t.Parallel()
	deepDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 5000}, {Price: 100.5, Qty: 5000}},
		Bids: []venue.DepthLevel{{Price: 99.9, Qty: 5000}, {Price: 99.5, Qty: 5000}},
	}
	now := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:       75,
		SlippageBudgetBps:     20,
		DefaultTakerFeeBps:    5,
		MaxEffectiveLeverage:  2,
		MarginSafetyFactor:    1.5,
		ExpectedHoldHours:     12,
	}
	notional := 75.0
	opp := CrossVenueOpportunity{
		Symbol:              "BTC-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		HighFundingRate:     0.0003,
		LowFundingRate:      0.0001,
		HighIntervalHours:   8,
		LowIntervalHours:    8,
		HighNextFundingTime: nextFunding,
		LowNextFundingTime:  nextFunding,
		TradableNotional:    notional,
	}
	fetchDepth := func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error) {
		return deepDepth, nil
	}
	highV := &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, depth: deepDepth}
	lowV := &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}, depth: deepDepth}
	slipLegs := crossVenuePerpPerpEVSlippageLegs(fetchDepth, context.Background(), highV, lowV, venue.Symbol("BTC-USDT"), 100, 100)
	scanOpp := crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, opp, now, opp.HighFundingRate, opp.LowFundingRate, slipLegs)
	gateSlip := 0.0
	for i := range slipLegs {
		gateSlip += notional * crossVenueEVSlippageBpsPerLeg(cfg, notional, slipLegs[i]) / 10000
	}
	if math.Abs(scanOpp.ExpectedSlippage-gateSlip) > 1e-9 {
		t.Fatalf("scan slip=%.6f gate slip=%.6f want match", scanOpp.ExpectedSlippage, gateSlip)
	}
}
