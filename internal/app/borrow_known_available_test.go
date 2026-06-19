package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func testBorrowKnownRegistry(at time.Time, probes ...CrossVenueBorrowProbe) *BorrowKnownAvailableRegistry {
	reg := NewBorrowKnownAvailableRegistry(24 * time.Hour)
	reg.ObserveProbes(probes, at)
	return reg
}

func TestBorrowKnownAvailableRegistrySeedAndFreshness(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, crossVenueBorrowProbesLog)
	lines := "" +
		`{"scanned_at":"2026-06-18T10:00:00Z","symbol":"ARB-USDT","base_asset":"ARB","venue":"okx","available":true}` + "\n" +
		`{"scanned_at":"2026-06-17T10:00:00Z","symbol":"OLD-USDT","base_asset":"OLD","venue":"binance","available":true}` + "\n" +
		`{"symbol":"NOTS-USDT","base_asset":"NOTS","venue":"bybit","available":true}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := NewBorrowKnownAvailableRegistry(24 * time.Hour)
	if err := reg.SeedFromBorrowProbesJSONL(dir, now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !reg.IsBorrowable("okx", "ARB", now) {
		t.Fatal("ARB@okx within 24h should be borrowable")
	}
	if reg.IsBorrowable("binance", "OLD", now) {
		t.Fatal("OLD@binance outside 24h should be rejected")
	}
	if !reg.IsBorrowable(bybit.VenueName, "NOTS", now) {
		t.Fatal("NOTS@bybit without timestamp should seed as fresh")
	}
	if reg.IsBorrowable("okx", "ARB", now.Add(25*time.Hour)) {
		t.Fatal("expired entry should fail closed")
	}
}

func TestPerpSpotAssetBorrowableVenueAware(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: true},
	)
	if !perpSpotAssetBorrowable(binance.VenueName, "BTC", reg, nil, now) {
		t.Fatal("binance BTC should pass")
	}
	if perpSpotAssetBorrowable(bybit.VenueName, "BTC", reg, nil, now) {
		t.Fatal("bybit BTC must not inherit binance borrowability")
	}
	if perpSpotAssetBorrowable(binance.VenueName, "ETH", reg, nil, now) {
		t.Fatal("never-confirmed ETH should fail closed")
	}
}

func TestPerpSpotAssetBorrowableRespectsBorrowDisabled(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: true},
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "gate", Available: true},
	)
	disabled := core.ParseVenueList("gate")
	if perpSpotAssetBorrowable("gate", "BTC", reg, disabled, now) {
		t.Fatal("borrow-disabled gate must fail closed even when known-available")
	}
	if !perpSpotAssetBorrowable(binance.VenueName, "BTC", reg, disabled, now) {
		t.Fatal("binance should remain borrow-capable when only gate is disabled")
	}
}

func TestPerpSpotAssetBorrowableZeroRateDoesNotBlockGate(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "JUP-USDT", Venue: "okx", Available: true},
	)
	if !perpSpotAssetBorrowable("okx", "JUP", reg, nil, now) {
		t.Fatal("available with zero rate should pass borrow gate")
	}
	cost := perpSpotBorrowCostUSDT("JUP", 1000, 24, map[string]float64{"JUP": 0}, 0)
	if cost != 0 {
		t.Fatalf("explicit zero rate should yield zero borrow cost, got %v", cost)
	}
}

func TestPerpSpotBorrowCostUsesConservativeDefaultWhenRateMissing(t *testing.T) {
	got := perpSpotBorrowCostUSDT("ZK", 100, 24, map[string]float64{}, 0.002)
	want := 100 * 0.002 * 1.0
	if got != want {
		t.Fatalf("borrow cost = %v want %v", got, want)
	}
}

func TestRotationBorrowMapsFromProbesAvailableZeroRate(t *testing.T) {
	daily, limits := rotationBorrowMapsFromProbes([]CrossVenueBorrowProbe{
		{Symbol: "W-USDT", Venue: "okx", Available: true, HourlyRateAPR: 0, MaxBorrowable: 500000},
	})
	if _, ok := daily["W"]; !ok {
		t.Fatal("available asset with zero rate should appear in daily map")
	}
	if daily["W"] != 0 {
		t.Fatalf("daily rate = %v want 0", daily["W"])
	}
	if limits["W"] != 500000 {
		t.Fatalf("limit = %v", limits["W"])
	}
}
