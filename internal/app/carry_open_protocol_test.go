package app

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

func testCarryFundingEntryGate() CarryFundingEntryGateConfig {
	return normalizeCarryFundingEntryGateConfig(CarryFundingEntryGateConfig{})
}

func TestCarryMatchTwoLegOpensPartialLeg2(t *testing.T) {
	match := carryMatchTwoLegOpens(4.8, 4.0, 0.01)
	if !match.OK || !match.Leg2Partial {
		t.Fatalf("expected partial leg2 match, got %+v", match)
	}
	if math.Abs(match.MatchedQty-4.0) > 1e-9 || math.Abs(match.ExcessLeg1-0.8) > 1e-9 {
		t.Fatalf("matched=%.4f excess=%.4f, want 4.0/0.8", match.MatchedQty, match.ExcessLeg1)
	}
}

func TestCarryCappedSpotSellQtyNeverExceedsHeld(t *testing.T) {
	bals := venue.Balances{ByAsset: map[string]venue.Balance{
		"BEL": {Asset: "BEL", Free: 4.795248},
	}}
	got := carryCappedSpotSellQty(4.8, bals, "BEL", 0.001)
	if got > 4.795248+1e-9 {
		t.Fatalf("sell qty %.8f exceeds held", got)
	}
	if got <= 0 {
		t.Fatal("expected positive sellable qty")
	}
}

func TestSVSPPreflightAbortsWithoutOrders(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.balanceUSDT = 1
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }

	_, err := e.executeOpen(context.Background(), testSVSPOpportunity(binance.VenueName, 0.5), true)
	if err == nil {
		t.Fatal("expected preflight abort")
	}
	if atomic.LoadInt32(v.placeCount) != 0 {
		t.Fatalf("expected no orders, got %d", atomic.LoadInt32(v.placeCount))
	}
}

func TestCarryFundingEntryWindowScalesWithInterval(t *testing.T) {
	gate := testCarryFundingEntryGate()
	w8 := carryFundingEntryWindowMinutes(gate, 8, 0, true)
	w1 := carryFundingEntryWindowMinutes(gate, 1, 0, true)
	if math.Abs(w8-60) > 1e-6 {
		t.Fatalf("8h base window = %.2f, want 60min", w8)
	}
	if math.Abs(w1-7.5) > 1e-6 {
		t.Fatalf("1h base window = %.2f, want 7.5min", w1)
	}
	if w8 <= w1 {
		t.Fatalf("8h window should exceed 1h window: %.2f vs %.2f", w8, w1)
	}
}

func TestCarryFundingEntryHighVolTighterThanLowVol(t *testing.T) {
	gate := testCarryFundingEntryGate()
	low := carryFundingEntryWindowMinutes(gate, 8, 0.00001, true)
	high := carryFundingEntryWindowMinutes(gate, 8, 0.0002, true)
	if high >= low {
		t.Fatalf("high vol window %.2f should be tighter than low vol %.2f", high, low)
	}
	wantHigh := 8.0 * 60 * gate.BaseFraction * gate.MinVolScale
	if math.Abs(high-wantHigh) > 1e-6 {
		t.Fatalf("high vol window %.2f want ≈%.2f (base×minScale)", high, wantHigh)
	}
}

func TestCarryFundingEntryWindowRespectsFloorAndCap(t *testing.T) {
	gate := testCarryFundingEntryGate()
	gate.FloorMinutes = 5
	gate.BaseFraction = 0.125
	// extreme vol → minScale
	tight := carryFundingEntryWindowMinutes(gate, 8, 1.0, true)
	if tight < gate.FloorMinutes-1e-9 {
		t.Fatalf("window %.2f below floor %.2f", tight, gate.FloorMinutes)
	}
	// zero vol → base cap
	wide := carryFundingEntryWindowMinutes(gate, 8, 0, true)
	base := 8.0 * 60 * gate.BaseFraction
	if wide > base+1e-9 {
		t.Fatalf("window %.2f exceeds base cap %.2f", wide, base)
	}
}

func TestCarryFundingEntryMissingVolUsesTightestWindow(t *testing.T) {
	gate := testCarryFundingEntryGate()
	withData := carryFundingEntryWindowMinutes(gate, 8, 0, true)
	noData := carryFundingEntryWindowMinutes(gate, 8, 0, false)
	if noData >= withData {
		t.Fatalf("missing vol should be tighter: noData=%.2f withData=%.2f", noData, withData)
	}
}

func TestCarryScannerFundingEntryRejectsOutsideWindow(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	gate := testCarryFundingEntryGate()
	fund := CrossVenueSpotPerpFundingSnap{
		Rate: 0.0003, IntervalHours: 8, NextFundingTime: now.Add(2 * time.Hour),
	}
	ok, reason := carryScannerFundingEntryEligible(now, fund, 2, 5000, 0.0003, 1, 0.1, 0.05, gate, 0, true)
	if ok || reason != carryScannerRejectFundingWindowNotReached {
		t.Fatalf("got ok=%v reason=%q", ok, reason)
	}
}

func TestCarryScannerFundingEntryKeepsInsideWindow(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	gate := testCarryFundingEntryGate()
	fund := CrossVenueSpotPerpFundingSnap{
		Rate: 0.0003, IntervalHours: 8, NextFundingTime: now.Add(30 * time.Minute),
	}
	ok, reason := carryScannerFundingEntryEligible(now, fund, 0.5, 5000, 0.0003, 0.5, 0.1, 0.05, gate, 0.00001, true)
	if !ok || reason != "" {
		t.Fatalf("got ok=%v reason=%q, want pass inside window", ok, reason)
	}
}

func TestFundingRateStdDevPerHour(t *testing.T) {
	rates := []float64{0.0008, 0.0012, 0.0010}
	vol, ok := fundingRateStdDevPerHour(rates, 8)
	if !ok || vol <= 0 {
		t.Fatalf("expected positive vol, got %.8f ok=%v", vol, ok)
	}
	_, okOne := fundingRateStdDevPerHour([]float64{0.001}, 8)
	if okOne {
		t.Fatal("single sample should not produce volatility")
	}
}

func TestCrossVenueScannerFundingRateVolatilityPerHour(t *testing.T) {
	s := &CrossVenueScanner{fundingHistory: make(map[string]map[string][]float64)}
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: {
			{Symbol: "AI-USDT", LastFundingRate: 0.0008, IntervalHours: 8},
			{Symbol: "AI-USDT", LastFundingRate: 0.0012, IntervalHours: 8},
			{Symbol: "AI-USDT", LastFundingRate: 0.0010, IntervalHours: 8},
		},
	}
	s.recordFundingHistory(byVenue)
	vol, ok := s.fundingRateVolatilityPerHour(binance.VenueName, "AI-USDT", 8, 10)
	if !ok || vol <= 0 {
		t.Fatalf("expected vol from scan history, got %.8f ok=%v", vol, ok)
	}
}

func TestSVSPRollbackSpotUsesSellableBalance(t *testing.T) {
	now := time.Now().UTC()
	v := newSVSPTestVenue(binance.VenueName)
	v.spotBalances["BTC"] = 4.795248
	dir := t.TempDir()
	e := NewSingleVenueSpotPerpExecutor(testSVSPExecutorConfig(), map[string]venue.Venue{binance.VenueName: v}, dir, testSVSPLogger())
	e.nowFn = func() time.Time { return now }
	tr := v

	e.rollbackSpotLeg(context.Background(), v, tr, "BTC-USDT", nil, 4.8, true, "test", venue.Instrument{StepSize: 0.001})
	var sellQty float64
	for _, req := range v.placeRequests {
		if req.Market == venue.MarketSpot && req.Side == venue.SideSell {
			sellQty = req.Qty
		}
	}
	if sellQty > 4.795248+executionQtyTolerance {
		t.Fatalf("rollback sell %.8f exceeds held 4.795248", sellQty)
	}
}
