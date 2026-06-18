package app

import (
	"context"
	"errors"
	"testing"

	"lingxi/internal/venue"
)

func TestEffectiveDustThresholdUsesExchangeMinNotional(t *testing.T) {
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	if got := effectiveDustThresholdUSDT(cfg, 3.0); got != 3.0 {
		t.Fatalf("threshold = %v, want 3 (exchange min)", got)
	}
	if got := effectiveDustThresholdUSDT(cfg, 0.5); got != 1.0 {
		t.Fatalf("threshold = %v, want 1 (config floor)", got)
	}
}

func TestEvaluatePositiveCarryOrphanDustVsNaked(t *testing.T) {
	ctx := context.Background()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	venues := map[string]venue.Venue{
		"gate": &cvspTestVenue{
			name:     "gate",
			bookSpot: venue.BookTicker{BidPrice: 0.039, AskPrice: 0.041},
		},
		"binance": newCVSPTestVenue("binance"),
	}

	dust := evaluatePositiveCarryOrphan(ctx, cfg, venues, "PHA-USDT", "gate", "binance", true, false, 1.01, 0, 0.04)
	if !dust.IsOrphan || !dust.IsDustOrphan || dust.NakedRisk {
		t.Fatalf("expected dust orphan, got %+v", dust)
	}
	if dust.RemainingNotionalUSDT >= 1.0 {
		t.Fatalf("dust notional = %v, want <1", dust.RemainingNotionalUSDT)
	}

	naked := evaluatePositiveCarryOrphan(ctx, cfg, venues, "PHA-USDT", "gate", "binance", false, true, 0, 10, 0)
	if !naked.IsOrphan || !naked.NakedRisk || naked.IsDustOrphan {
		t.Fatalf("expected naked perp orphan, got %+v", naked)
	}
}

func TestEvaluateNegativeCarryOrphanSpotShortDustVsNaked(t *testing.T) {
	ctx := context.Background()
	cfg := CrossVenueExecutorConfig{DustCloseThresholdUSDT: 1.0}
	venues := map[string]venue.Venue{
		"binance": &svpsTestVenue{
			fakeFundingVenue: fakeFundingVenue{name: "binance"},
			book:             venue.BookTicker{BidPrice: 0.047, AskPrice: 0.049},
		},
	}

	dust := evaluateNegativeCarryOrphan(ctx, cfg, venues, "FLUX-USDT", "binance", "binance", true, false, 0.10459, 0, 0.05, 0)
	if !dust.IsOrphan || !dust.IsDustOrphan || dust.NakedRisk {
		t.Fatalf("expected spot-short dust orphan, got %+v", dust)
	}
	if dust.RemainingNotionalUSDT >= 1.0 {
		t.Fatalf("dust notional = %v, want <1", dust.RemainingNotionalUSDT)
	}

	nakedVenue := &svpsTestVenue{
		fakeFundingVenue: fakeFundingVenue{name: "binance"},
		book:             venue.BookTicker{BidPrice: 99, AskPrice: 101},
	}
	naked := evaluateNegativeCarryOrphan(ctx, cfg, map[string]venue.Venue{"binance": nakedVenue}, "BTC-USDT", "binance", "binance", true, false, 0.5, 0, 100, 0)
	if !naked.IsOrphan || !naked.NakedRisk || naked.IsDustOrphan {
		t.Fatalf("expected naked spot-short orphan, got %+v", naked)
	}

	perpDust := evaluateNegativeCarryOrphan(ctx, cfg, venues, "BTC-USDT", "binance", "binance", false, true, 0, 0.00008, 0, 100)
	if !perpDust.IsOrphan || !perpDust.IsDustOrphan || perpDust.NakedRisk {
		t.Fatalf("expected perp dust orphan, got %+v", perpDust)
	}
}

func TestIsNotionalFilterError(t *testing.T) {
	if isNotionalFilterError(nil) {
		t.Fatal("nil should not match")
	}
	if !isNotionalFilterError(errors.New("HTTP 400 code=-1013 msg=Filter failure: NOTIONAL")) {
		t.Fatal("expected binance NOTIONAL reject to match")
	}
	if isNotionalFilterError(errors.New("HTTP 400 code=-1013 msg=Filter failure: PERCENT_PRICE")) {
		t.Fatal("percent price reject should not match NOTIONAL")
	}
}
