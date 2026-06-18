package app

import (
	"context"
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
