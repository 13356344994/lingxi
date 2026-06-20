package app

import (
	"math"
	"testing"
)

func TestPerVenueMarginHonorsConfiguredLeverage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lev      float64
		notional float64
	}{
		{lev: 3, notional: 41.49},
		{lev: 5, notional: 100},
		{lev: 2, notional: 50},
	}
	for _, tc := range cases {
		cfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: tc.lev}
		got := crossVenuePerVenueMarginUSDT(cfg, tc.notional)
		want := tc.notional / tc.lev
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("lev=%.0f notional=%.2f margin=%.6f want %.6f", tc.lev, tc.notional, got, want)
		}
		// margin_safety_factor 不应再缩小 sizing。
		cfg.MarginSafetyFactor = 1.5
		gotWithFactor := crossVenuePerVenueMarginUSDT(cfg, tc.notional)
		if math.Abs(gotWithFactor-want) > 1e-9 {
			t.Fatalf("margin_safety_factor must not shrink sizing: got %.6f want %.6f", gotWithFactor, want)
		}
	}
}

func TestEffectiveMarginTwoLegs(t *testing.T) {
	t.Parallel()
	cfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	notional := 41.49
	got := crossVenueEffectiveMarginOccupied(cfg, notional)
	want := 2 * notional / 3
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("two-leg margin=%.6f want %.6f", got, want)
	}
}

func TestInferNotionalRoundTrip(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	scannerCfg.PerpWalletBufferRatio = 0.10
	_ = scannerCfg // buffer 来自 DefaultSingleVenueSpotPerpScannerConfig(execCfg)

	for _, notional := range []float64{41.49, 100, 50} {
		margin := rotationPerpMarginWithBuffer(execCfg, notional)
		total := notional + margin
		back := rotationInferNotionalFromTotalCapital(execCfg, total)
		if math.Abs(back-notional) > 1e-6 {
			t.Fatalf("notional=%.2f total=%.6f round-trip=%.6f", notional, total, back)
		}
	}
}

func TestPerpPerpEffectiveLeverageNoLongerCompressedBySafetyFactor(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{
		MaxEffectiveLeverage: 3,
		MarginSafetyFactor:   1.5, // 旧口径会压杠杆; 新口径应忽略。
	}
	notional := 41.49
	bareMargin := crossVenuePerVenueMarginUSDT(execCfg, notional)
	effectiveLev := notional / bareMargin
	if math.Abs(effectiveLev-3) > 1e-6 {
		t.Fatalf("bare effective leverage=%.4f want 3", effectiveLev)
	}
	// 旧 bug: margin=notional*0.5 → 1.67X; 新: margin=notional/3 → 3X(不含 buffer)。
	if math.Abs(bareMargin-notional/3) > 1e-9 {
		t.Fatalf("margin=%.6f want %.6f", bareMargin, notional/3)
	}
}
