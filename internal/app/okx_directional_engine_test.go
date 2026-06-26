package app

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestOKXDirectionalSelectUniverse(t *testing.T) {
	cfg := OKXDirectionalConfig{
		MinFundingAbsPerInterval: 0.0005,
		UniverseTopN:             3,
	}
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	snaps := []venue.FundingSnapshot{
		{Symbol: "AAA-USDT", LastFundingRate: 0.0003},  // below threshold
		{Symbol: "BBB-USDT", LastFundingRate: -0.0010}, // |0.001|
		{Symbol: "CCC-USDT", LastFundingRate: 0.0008},  // |0.0008|
		{Symbol: "DDD-USDT", LastFundingRate: -0.0020}, // |0.002| highest
		{Symbol: "EEE-USDT", LastFundingRate: 0.0015},  // |0.0015|
		{Symbol: "ETH-BTC", LastFundingRate: 0.01},     // not USDT
	}
	cooldown := map[string]time.Time{
		"EEE-USDT": now.Add(10 * time.Minute), // still cooling
	}
	got := selectDirectionalUniverse(snaps, cooldown, now, cfg)
	want := []string{"DDD-USDT", "BBB-USDT", "CCC-USDT"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d got %q want %q full=%v", i, got[i], want[i], got)
		}
	}
}

func TestOKXDirectionalDailyLossHalt(t *testing.T) {
	limit := 6.0
	if directionalDailyLossHalted(-5.99, limit) {
		t.Fatal("-5.99 should not halt")
	}
	if !directionalDailyLossHalted(-6.0, limit) {
		t.Fatal("-6.0 should halt")
	}
	if !directionalDailyLossHalted(-7.0, limit) {
		t.Fatal("-7.0 should halt")
	}
	if directionalDailyLossHalted(-100, 0) {
		t.Fatal("limit=0 disables halt")
	}
}

func TestOKXDirectionalUPnLPct(t *testing.T) {
	entry := 100.0
	midLongWin := 105.0
	midShortWin := 95.0
	longPnL := directionalUPnLPct(DirectionLong, entry, midLongWin)
	if math.Abs(longPnL-0.05) > 1e-9 {
		t.Fatalf("long win pnl=%v want 0.05", longPnL)
	}
	shortPnL := directionalUPnLPct(DirectionShort, entry, midShortWin)
	if math.Abs(shortPnL-0.05) > 1e-9 {
		t.Fatalf("short win pnl=%v want 0.05", shortPnL)
	}
	longLoss := directionalUPnLPct(DirectionLong, entry, 95.0)
	if longLoss >= 0 {
		t.Fatalf("long loss should be negative, got %v", longLoss)
	}
	shortLoss := directionalUPnLPct(DirectionShort, entry, 105.0)
	if shortLoss >= 0 {
		t.Fatalf("short loss should be negative, got %v", shortLoss)
	}
}
