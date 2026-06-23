package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

func TestCrossVenueResolveFundingIntervalHoursVenueDefault(t *testing.T) {
	if got := crossVenueResolveFundingIntervalHours(gate.VenueName, 0, nil); got != 1 {
		t.Fatalf("gate default interval = %d, want 1", got)
	}
	if got := crossVenueResolveFundingIntervalHours(binance.VenueName, 0, nil); got != 4 {
		t.Fatalf("binance default interval = %d, want 4", got)
	}
	if got := crossVenueResolveFundingIntervalHours(gate.VenueName, 4, nil); got != 4 {
		t.Fatalf("explicit interval = %d, want 4", got)
	}
}

func TestUnifiedOpportunityPropagatesLegIntervalsToCandidateRecord(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	highNext := now.Add(1 * time.Hour)
	lowNext := now.Add(3 * time.Hour)
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	cvOpp := CrossVenueOpportunity{
		Symbol:              "WAVES-USDT",
		HighVenue:           gate.VenueName,
		LowVenue:            binance.VenueName,
		HighFundingRate:     -0.0001,
		LowFundingRate:      -0.0005,
		HighIntervalHours:   1,
		LowIntervalHours:    4,
		HighNextFundingTime: highNext,
		LowNextFundingTime:  lowNext,
		SpreadAPR:           0.5,
		NetEV:               0.06,
		TradableNotional:    52,
		CapitalEfficiencyEV: 0.06 / 26,
	}
	unified := UnifiedOpportunityFromCrossVenue(cfg, cvOpp, nil, 52, 0)
	if unified.HighIntervalHours != 1 || unified.LowIntervalHours != 4 {
		t.Fatalf("unified intervals high=%d low=%d want 1/4", unified.HighIntervalHours, unified.LowIntervalHours)
	}
	rec := unifiedCandidateLogFromOpportunity(cfg, "cycle-test", unified, true, 52)
	if rec.HighIntervalHours != 1 || rec.LowIntervalHours != 4 {
		t.Fatalf("record intervals high=%d low=%d want 1/4", rec.HighIntervalHours, rec.LowIntervalHours)
	}
	if rec.HighNextFundingTime == "" || rec.LowNextFundingTime == "" {
		t.Fatalf("record next funding times missing: high=%q low=%q", rec.HighNextFundingTime, rec.LowNextFundingTime)
	}
}

func TestCrossVenueFundingSnapFromOppLegStaleNextFundingNotGhostLeg(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 30, 0, 0, time.UTC)
	stale := now.Add(-30 * time.Minute)
	opp := CrossVenueOpportunity{
		HighFundingRate:     -0.0004,
		HighIntervalHours:   1,
		HighNextFundingTime: stale,
	}
	snap, ok := crossVenueFundingSnapFromOppLeg(opp, true, now)
	if !ok {
		t.Fatal("stale next funding with known 1h interval should not be ghost-leg rejected")
	}
	if snap.IntervalHours != 1 {
		t.Fatalf("interval = %d, want 1", snap.IntervalHours)
	}
	if !snap.NextFundingTime.After(now) {
		t.Fatalf("advanced next funding should be after now, got %v", snap.NextFundingTime)
	}
}

func TestCrossVenueOpenGateNoGhostLegWhenIntervalKnownStaleNextFunding(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 30, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 1, nextFunding)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 4, nextFunding)
	high.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	low.book = venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}
	venues := map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low}
	cfg := testCrossVenueExecutorConfig()
	stale := now.Add(-20 * time.Minute)
	res := evaluateCrossVenueOpenGate(context.Background(), cfg.CrossVenueExecutor, testCrossVenueGateDeps(1000), venues, crossVenueGateInput{
		Opp: CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
			SpreadAPR: 2.0, HighFundingRate: 0.001, LowFundingRate: 0.0001,
			HighIntervalHours: 1, LowIntervalHours: 4,
			HighNextFundingTime: stale, LowNextFundingTime: stale,
		},
		Now: now,
	})
	if strings.Contains(res.Reason, "幽灵腿") {
		t.Fatalf("unexpected ghost-leg rejection: %q", res.Reason)
	}
	// 非幽灵腿拒(如 EV 闸)可接受;本测只钉 stale NextFundingTime + 已知 interval 不应触发幽灵腿。
}

func TestEnrichOpportunityFromSnapshotFillsMissingIntervalFromFundingIndex(t *testing.T) {
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	next := now.Add(45 * time.Minute)
	snap := CrossVenueScanSnapshot{
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			gate.VenueName: {
				"H-USDT": {Rate: -0.0004, IntervalHours: 1, NextFundingTime: next},
			},
			binance.VenueName: {
				"H-USDT": {Rate: -0.0025, IntervalHours: 4, NextFundingTime: next},
			},
		},
	}
	e := &CrossVenueExecutor{
		venues: map[string]venue.Venue{},
	}
	opp := CrossVenueOpportunity{
		Symbol: "H-USDT", HighVenue: gate.VenueName, LowVenue: binance.VenueName,
	}
	got := e.enrichOpportunityFromSnapshot(opp, snap, now)
	if got.HighIntervalHours != 1 || got.LowIntervalHours != 4 {
		t.Fatalf("enriched intervals high=%d low=%d want 1/4", got.HighIntervalHours, got.LowIntervalHours)
	}
	if got.HighFundingRate != -0.0004 || got.LowFundingRate != -0.0025 {
		t.Fatalf("enriched rates high=%v low=%v", got.HighFundingRate, got.LowFundingRate)
	}
}
