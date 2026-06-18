package app

import (
	"testing"

	"lingxi/internal/venue"
)

func TestCrossVenueEVSlippageThinBookMorePessimisticThanFixed3bps(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		SlippageBudgetBps: 20,
		MaxPositionUSDT:   50,
	}
	thin := crossVenueSpotPerpEVSlippageLegs(
		CrossVenueSpotPerpMarketSnap{
			SpotMid: 100,
			SpotDepth: &venue.Depth{
				Asks: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 105, Qty: 100}},
				Bids: []venue.DepthLevel{{Price: 99.9, Qty: 100}},
			},
		},
		CrossVenueSpotPerpMarketSnap{
			PerpMid: 100,
			PerpDepth: &venue.Depth{
				Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}, {Price: 95, Qty: 100}},
				Asks: []venue.DepthLevel{{Price: 100.01, Qty: 100}},
			},
		},
	)
	depthSlip := crossVenueExpectedSlippageUSDTWithLegs(cfg, 50, thin)
	fixed3 := 4.0 * 50.0 * 3.0 / 10000.0
	if depthSlip <= fixed3 {
		t.Fatalf("thin book depth slip=%v want > fixed3=%v", depthSlip, fixed3)
	}
}

func TestCrossVenueEVSlippageDeepBookSimilarToBaseFallback(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		SlippageBudgetBps: 20,
		MaxPositionUSDT:   50,
	}
	deep := crossVenueSpotPerpEVSlippageLegs(
		CrossVenueSpotPerpMarketSnap{
			SpotMid: 100,
			SpotDepth: &venue.Depth{
				Asks: []venue.DepthLevel{{Price: 100.01, Qty: 1000}},
				Bids: []venue.DepthLevel{{Price: 99.99, Qty: 1000}},
			},
		},
		CrossVenueSpotPerpMarketSnap{
			PerpMid: 100,
			PerpDepth: &venue.Depth{
				Bids: []venue.DepthLevel{{Price: 100, Qty: 1000}},
				Asks: []venue.DepthLevel{{Price: 100.01, Qty: 1000}},
			},
		},
	)
	depthSlip := crossVenueExpectedSlippageUSDTWithLegs(cfg, 50, deep)
	baseFallback := crossVenueExpectedSlippageUSDT(cfg, 50, 4)
	if depthSlip > baseFallback*1.05 {
		t.Fatalf("deep book depth slip=%v want ≈ base fallback=%v", depthSlip, baseFallback)
	}
}

func TestCrossVenueSlippageBudgetUnifiedDerivation(t *testing.T) {
	cfg := CrossVenueExecutorConfig{SlippageBudgetBps: 30}
	if got := crossVenueResolvedSizingMaxSlippageBps(cfg); got != 30 {
		t.Fatalf("sizing=%v want 30", got)
	}
	if got := crossVenueResolvedMaxOpenSlippageBps(cfg); got != 22.5 {
		t.Fatalf("gate=%v want 22.5", got)
	}
	if got := crossVenueResolvedIOCSlippageBps(cfg); got != 30 {
		t.Fatalf("ioc=%v want 30", got)
	}
	if got := crossVenueResolvedTakerSlippageBps(cfg); got != 60 {
		t.Fatalf("taker=%v want 60", got)
	}
	if got := crossVenueResolvedCloseMaxSlippageBps(cfg); got != 375 {
		t.Fatalf("close max=%v want 375", got)
	}
}

func TestCrossVenueEVSlippageNoDepthFallsBackToBaseNot3bps(t *testing.T) {
	cfg := CrossVenueExecutorConfig{SlippageBudgetBps: 20}
	slip := crossVenueExpectedSlippageUSDT(cfg, 50, 4)
	want := 4.0 * 50.0 * 20.0 / 10000.0
	if slip != want {
		t.Fatalf("no-depth slip=%v want base fallback=%v (not 3bps=%v)", slip, want, 4.0*50.0*3.0/10000.0)
	}
}

func TestCrossVenueSlippageLegacyModeWhenBaseZero(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		SlippageBudgetBps:      0,
		SizingMaxSlippageBps:   18,
		MaxOpenSlippageBps:     12,
		TakerSlippageBudgetBps: 36,
		EVExpectedSlippageBps:  3,
	}
	if got := crossVenueResolvedSizingMaxSlippageBps(cfg); got != 18 {
		t.Fatalf("legacy sizing=%v want 18", got)
	}
	if got := crossVenueResolvedMaxOpenSlippageBps(cfg); got != 12 {
		t.Fatalf("legacy gate=%v want 12", got)
	}
	if got := crossVenueResolvedTakerSlippageBps(cfg); got != 36 {
		t.Fatalf("legacy taker=%v want 36", got)
	}
	slip := crossVenueExpectedSlippageUSDT(cfg, 50, 4)
	if slip != 4.0*50.0*3.0/10000.0 {
		t.Fatalf("legacy EV slip=%v want 3bps", slip)
	}
}

func TestCrossVenueTakerSlippageNotMoreAggressiveThanLegacyDefaults(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		SlippageBudgetBps:      20,
		TakerSlippageBudgetBps: 40,
	}
	got := crossVenueResolvedTakerSlippageBps(cfg)
	if got > defaultTakerSlippageBudgetBps+1e-9 {
		t.Fatalf("derived taker=%v want ≤ legacy default %v", got, defaultTakerSlippageBudgetBps)
	}
}
