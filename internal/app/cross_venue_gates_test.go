package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/okx"
)

// gateRealFundingVenue 用于闸门单测:可配置 FetchRealizedFunding 行为。
type gateRealFundingVenue struct {
	crossVenueTestVenue
	realFundingErr error
}

func (v *gateRealFundingVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	if v.realFundingErr != nil {
		return nil, v.realFundingErr
	}
	return nil, nil
}

func TestCrossVenueOpenGateAllowsVerifiableRealFunding(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := &gateRealFundingVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding),
	}
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low := &gateRealFundingVenue{
		crossVenueTestVenue: *newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding),
	}
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.001, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	})
	if !res.OK {
		t.Fatalf("expected gate pass when both legs verifiable: %s", res.Reason)
	}
}

func TestCrossVenueFundingSnapshotValid(t *testing.T) {
	now := time.Now().UTC()
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(time.Hour),
	}, now) {
		// ok
	} else {
		t.Fatal("expected valid snapshot")
	}
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 0, NextFundingTime: now.Add(time.Hour),
	}, now) {
		t.Fatal("expected invalid when interval zero")
	}
	if crossVenueFundingSnapshotValid(venue.FundingSnapshot{
		LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(-time.Hour),
	}, now) {
		t.Fatal("expected invalid when next funding in past")
	}
}

func crossVenueGateTestInput(now time.Time) crossVenueGateInput {
	return crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.001, LowFundingRate: 0.0001,
			HighIntervalHours: 8, LowIntervalHours: 8,
		},
		Now: now,
	}
}

func TestCrossVenueOpenGateLiquiditySlippageRejectsThinBook(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	// 卖 50U@100 需 ~0.5 币:仅 0.05@100 + 0.45@95 → 滑点远超 15bps
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
	}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}, {Price: 100.02, Qty: 100}},
	}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = 50
	cfg.CrossVenueExecutor.MaxOpenSlippageBps = 15
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateTestInput(now))
	if res.OK {
		t.Fatalf("expected gate reject on thin high leg, got pass")
	}
	if !strings.Contains(res.Reason, "高费所预估滑点") || !strings.Contains(res.Reason, "> 15") {
		t.Fatalf("unexpected reason: %s", res.Reason)
	}
}

func TestCrossVenueOpenGateLiquiditySlippageAllowsDeepBook(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	high.depth = venue.Depth{
		Bids: []venue.DepthLevel{{Price: 100, Qty: 100}, {Price: 99.99, Qty: 100}},
	}
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, nextFunding)
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.depth = venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}, {Price: 100.02, Qty: 100}},
	}
	venues := map[string]venue.Venue{binance.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.MaxPositionUSDT = 50
	cfg.CrossVenueExecutor.MaxOpenSlippageBps = 15
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateTestInput(now))
	if !res.OK {
		t.Fatalf("expected gate pass on deep book: %s", res.Reason)
	}
}

func TestSimulateTakerSlippageBps(t *testing.T) {
	// 50U @ ref 100 → 0.5 qty; 0.05@100 + 0.45@95 → avg 95.5, best 100 → 450bps
	slip, ok := simulateTakerSlippageBps(venue.SideSell, []venue.DepthLevel{
		{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100},
	}, 50, 100)
	if !ok {
		t.Fatal("expected ok")
	}
	if slip < 400 || slip > 500 {
		t.Fatalf("slippage bps=%v want ~450", slip)
	}
	slip, ok = simulateTakerSlippageBps(venue.SideBuy, []venue.DepthLevel{
		{Price: 100, Qty: 10},
	}, 50, 100)
	if !ok || slip > 0.01 {
		t.Fatalf("deep book buy slip=%v ok=%v want ~0", slip, ok)
	}
}
