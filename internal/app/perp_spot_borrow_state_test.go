package app

import (
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func TestBorrowStateThreeWay(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: true},
		CrossVenueBorrowProbe{Symbol: "ETH-USDT", Venue: binance.VenueName, Available: false},
	)

	if got := perpSpotBorrowState(binance.VenueName, "BTC", reg, nil, now); got != BorrowStateAvailable {
		t.Fatalf("BTC state = %v, want Available", got)
	}
	if got := perpSpotBorrowState(binance.VenueName, "ETH", reg, nil, now); got != BorrowStateUnavailable {
		t.Fatalf("ETH state = %v, want Unavailable", got)
	}
	if got := perpSpotBorrowState(binance.VenueName, "SOL", reg, nil, now); got != BorrowStateUnknown {
		t.Fatalf("SOL state = %v, want Unknown", got)
	}
}

func TestProbeFailureStaysUnknownNotUnavailable(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "ARB-USDT", Venue: "okx", Err: "timeout"},
	)

	if reg.IsKnownUnavailable("okx", "ARB", now) {
		t.Fatal("probe failure must not seed confirmed-unavailable")
	}
	if reg.IsBorrowable("okx", "ARB", now) {
		t.Fatal("probe failure must not seed confirmed-available")
	}
	if got := perpSpotBorrowState("okx", "ARB", reg, nil, now); got != BorrowStateUnknown {
		t.Fatalf("probe failure state = %v, want Unknown", got)
	}
}

func TestConfirmedUnavailableRejects(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	t.Run("svps", func(t *testing.T) {
		in := testSingleVenuePerpSpotScanInput(now)
		in.BorrowKnownAvailable = testBorrowKnownRegistry(now,
			CrossVenueBorrowProbe{Symbol: "ETH-USDT", Venue: bybit.VenueName, Available: true},
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: bybit.VenueName, Available: false},
		)
		opps := ScanSingleVenuePerpSpotOpportunities(in)
		btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
		if btc == nil {
			t.Fatal("expected rejected row for confirmed unavailable")
		}
		if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectNotBorrowable) {
			t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectNotBorrowable)
		}
	})

	t.Run("cvps", func(t *testing.T) {
		in := testCrossVenuePerpSpotScanInput(now)
		in.BorrowKnownAvailable = testBorrowKnownRegistry(now,
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: false},
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "okx", Available: false},
		)
		in.BorrowDailyRateByAsset = map[string]float64{}
		opps := ScanCrossVenuePerpSpotOpportunities(in)
		if len(opps) != 0 {
			t.Fatalf("opportunities = %d, want 0 for confirmed unavailable", len(opps))
		}
		out := captureCrossVenueCarryFunnelLog(t, func() {
			_ = ScanCrossVenuePerpSpotOpportunities(in)
		})
		if !strings.Contains(out, "build_not_borrowable=2") {
			t.Fatalf("expected not-borrowable funnel, got: %s", out)
		}
	})
}

func TestUnknownFailsOpenForCvpsSvps(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	t.Run("svps_empty_registry", func(t *testing.T) {
		in := testSingleVenuePerpSpotScanInput(now)
		in.BorrowKnownAvailable = NewBorrowKnownAvailableRegistry(24 * time.Hour)
		opps := ScanSingleVenuePerpSpotOpportunities(in)
		if len(opps) == 0 {
			t.Fatal("unknown borrow state should fail-open and emit rows")
		}
		for _, opp := range opps {
			if containsPerpSpotReject(opp.RejectReasons, singleVenuePerpSpotRejectNotBorrowable) {
				t.Fatalf("must not hard-reject not_borrowable on unknown, got %#v", opp.RejectReasons)
			}
		}
		btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
		if btc == nil {
			t.Fatal("expected BTC row")
		}
		if !containsPerpSpotReject(btc.RejectReasons, perpSpotBorrowUnknownFailOpen) {
			t.Fatalf("want soft marker %q, got %#v", perpSpotBorrowUnknownFailOpen, btc.RejectReasons)
		}
		if btc.ExpectedBorrowCostUSDT <= 0 {
			t.Fatalf("unknown should use default borrow cost estimate, got %.6f", btc.ExpectedBorrowCostUSDT)
		}
	})

	t.Run("cvps_probe_failure", func(t *testing.T) {
		in := testCrossVenuePerpSpotScanInput(now)
		in.BorrowKnownAvailable = testBorrowKnownRegistry(now,
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Err: "network error"},
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "okx", Err: "timeout"},
		)
		in.BorrowDailyRateByAsset = map[string]float64{}
		in.BorrowDefaultDailyRateWhenUnknown = 0.002
		opps := ScanCrossVenuePerpSpotOpportunities(in)
		if len(opps) == 0 {
			t.Fatal("cvps should fail-open when borrow probe failed")
		}
		for _, opp := range opps {
			if containsPerpSpotReject(opp.RejectReasons, crossVenuePerpSpotRejectNotBorrowable) {
				t.Fatalf("must not reject not_borrowable on unknown, got %#v", opp.RejectReasons)
			}
			if !containsPerpSpotReject(opp.RejectReasons, perpSpotBorrowUnknownFailOpen) {
				t.Fatalf("want soft marker %q, got %#v", perpSpotBorrowUnknownFailOpen, opp.RejectReasons)
			}
		}
		out := captureCrossVenueCarryFunnelLog(t, func() {
			_ = ScanCrossVenuePerpSpotOpportunities(in)
		})
		if strings.Contains(out, "build_not_borrowable=") && !strings.Contains(out, "build_not_borrowable=0") {
			t.Fatalf("unknown must not increment build_not_borrowable, got: %s", out)
		}
	})
}
