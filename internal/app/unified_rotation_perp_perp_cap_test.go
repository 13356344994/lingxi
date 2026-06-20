package app

import (
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func testPerpPerpHOpportunity(t *testing.T, notional float64) UnifiedOpportunity {
	t.Helper()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	scannerCfg.PerpWalletBufferRatio = 0.10
	_ = scannerCfg
	now := time.Now().UTC()
	cvOpp := CrossVenueOpportunity{
		Symbol:              "H-USDT",
		HighVenue:           binance.VenueName,
		LowVenue:            bybit.VenueName,
		SpreadAPR:           13.58,
		HighFundingRate:     0.00625,
		LowFundingRate:      0.0000125,
		HighIntervalHours:   4,
		LowIntervalHours:    1,
		HighNextFundingTime: now.Add(2 * time.Hour),
		LowNextFundingTime:  now.Add(time.Hour),
		TradableNotional:    notional,
		SpreadEV:            2.0 * (notional / 79.6),
		OpenCost:            0.3 * (notional / 79.6),
		CloseCost:           0.2 * (notional / 79.6),
		ExpectedSlippage:    0.1 * (notional / 79.6),
		NetEV:               1.5 * (notional / 79.6),
	}
	return UnifiedOpportunityFromCrossVenue(execCfg, cvOpp, nil, notional, 0)
}

func TestPerpPerpCapitalCapSizesDownToFit(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	scannerCfg.PerpWalletBufferRatio = 0.10
	_ = scannerCfg

	rotCfg := DefaultRotationConfig()
	rotCfg.CapitalAwarePerpPerpSizingEnabled = true
	availAfterReserve := 19.75
	available := map[string]float64{
		binance.VenueName: availAfterReserve + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   availAfterReserve + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}

	wantMax := availAfterReserve * 3.0 * rotationCapitalSizingSafetyHaircut(rotCfg)
	if math.Abs(wantMax-58.6575) > 0.05 {
		t.Fatalf("sanity wantMax≈58.66 got %.4f", wantMax)
	}

	opp := testPerpPerpHOpportunity(t, 79.6)
	capped := rotationCapPerpPerpOpportunityByCapital(opp, execCfg, rotCfg, available)

	gotNotional := rotationMaxLegNotional(capped)
	if math.Abs(gotNotional-wantMax) > 0.15 {
		t.Fatalf("capped notional=%.4f want≈%.4f", gotNotional, wantMax)
	}

	margin := crossVenuePerVenueMarginUSDT(execCfg, gotNotional)
	if margin >= availAfterReserve {
		t.Fatalf("per-leg margin=%.4f should be strictly less than avail %.4f", margin, availAfterReserve)
	}
	if margin < availAfterReserve*0.97 {
		t.Fatalf("per-leg margin=%.4f too far below avail %.4f", margin, availAfterReserve)
	}

	ok, reason := rotationCanOpenOpportunity(capped, available, rotCfg, 0, nil, nil)
	if !ok {
		t.Fatalf("capped candidate should open, reason=%q", reason)
	}
}

func TestPerpPerpCapitalCapProportionalEV(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	rotCfg := DefaultRotationConfig()
	available := map[string]float64{
		binance.VenueName: 19.75 + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   19.75 + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}

	opp := testPerpPerpHOpportunity(t, 79.6)
	origFunding := opp.ExpectedFundingUSDT
	origOpen := opp.ExpectedOpenCostUSDT
	origClose := opp.ExpectedCloseCostUSDT
	origSlip := opp.ExpectedSlippageUSDT
	origNet := opp.ExpectedNetEVUSDT

	capped := rotationCapPerpPerpOpportunityByCapital(opp, execCfg, rotCfg, available)
	ratio := rotationMaxLegNotional(capped) / 79.6

	checkRatio := func(name string, got, orig float64) {
		t.Helper()
		if orig == 0 {
			return
		}
		if math.Abs(got/orig-ratio) > 0.02 {
			t.Fatalf("%s ratio=%.4f want≈%.4f (got=%.6f orig=%.6f)", name, got/orig, ratio, got, orig)
		}
	}
	checkRatio("funding", capped.ExpectedFundingUSDT, origFunding)
	checkRatio("open", capped.ExpectedOpenCostUSDT, origOpen)
	checkRatio("close", capped.ExpectedCloseCostUSDT, origClose)
	checkRatio("slippage", capped.ExpectedSlippageUSDT, origSlip)
	checkRatio("netEV", capped.ExpectedNetEVUSDT, origNet)

	if capped.ExpectedNetEVUSDT <= 0 {
		t.Fatalf("scaled netEV=%.6f want >0", capped.ExpectedNetEVUSDT)
	}
	ok, reason := rotationCanOpenOpportunity(capped, available, rotCfg, 0, nil, nil)
	if !ok {
		t.Fatalf("positive netEV capped candidate should open, reason=%q", reason)
	}
}

func TestPerpPerpCapitalCapBelowMinSkips(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	rotCfg := DefaultRotationConfig()
	rotCfg.MinOpenNotionalUSDT = 5
	// 极小可用:反解名义 < MinOpenNotional
	available := map[string]float64{
		binance.VenueName: 1 + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   1 + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}

	opp := testPerpPerpHOpportunity(t, 79.6)
	capped := rotationCapPerpPerpOpportunityByCapital(opp, execCfg, rotCfg, available)
	notional := rotationMaxLegNotional(capped)
	if notional >= rotCfg.MinOpenNotionalUSDT {
		t.Fatalf("capped notional=%.4f should be below min %.4f", notional, rotCfg.MinOpenNotionalUSDT)
	}

	ok, reason := rotationCanOpenOpportunity(capped, available, rotCfg, 0, nil, nil)
	if ok {
		t.Fatal("below-min candidate should not open")
	}
	if reason != "min_open_notional" {
		t.Fatalf("reason=%q want min_open_notional", reason)
	}
}

func TestCapitalAwareSizingDisabledKeepsOld(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	rotCfg := DefaultRotationConfig()
	rotCfg.CapitalAwarePerpPerpSizingEnabled = false
	available := map[string]float64{
		binance.VenueName: 19.75 + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   19.75 + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}

	opp := testPerpPerpHOpportunity(t, 79.6)
	aligned := rotationAlignPerpPerpCapital(opp, execCfg)
	if math.Abs(rotationMaxLegNotional(aligned)-79.6) > 1e-9 {
		t.Fatalf("disabled path should keep notional 79.6, got %.4f", rotationMaxLegNotional(aligned))
	}

	ok, reason := rotationCanOpenOpportunity(aligned, available, rotCfg, 0, nil, nil)
	if ok {
		t.Fatal("old behavior should reject when margin exceeds available")
	}
	if !strings.HasPrefix(reason, "venue_balance_insufficient") {
		t.Fatalf("reason=%q want venue_balance_insufficient prefix", reason)
	}
}

func TestRotationPerpPerpMaxNotionalFromVenueAvailFormula(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 3}
	scannerCfg := DefaultSingleVenueSpotPerpScannerConfig(execCfg)
	scannerCfg.PerpWalletBufferRatio = 0.10
	got := rotationPerpPerpMaxNotionalFromVenueAvail(execCfg, DefaultRotationConfig(), 19.75)
	want := 19.75 * 3.0 * 0.99
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("maxNotional=%.6f want %.6f", got, want)
	}
}

func TestCapitalCapLeavesSafetyHaircut(t *testing.T) {
	t.Parallel()
	execCfg := CrossVenueExecutorConfig{MaxEffectiveLeverage: 2}
	rotCfg := DefaultRotationConfig()
	avail := 50.0
	available := map[string]float64{
		binance.VenueName: avail + rotationReserveUSDT(rotCfg, binance.VenueName),
		bybit.VenueName:   avail + rotationReserveUSDT(rotCfg, bybit.VenueName),
	}
	opp := testPerpPerpHOpportunity(t, 200)
	maxN := rotationPerpPerpMaxNotionalByCapital(execCfg, rotCfg, available, opp)
	margin := crossVenuePerVenueMarginUSDT(execCfg, maxN)
	if margin >= avail {
		t.Fatalf("margin %.4f should be strictly less than avail %.4f (haircut)", margin, avail)
	}
	capped := rotationCapPerpPerpOpportunityByCapital(opp, execCfg, rotCfg, available)
	gotMargin := crossVenuePerVenueMarginUSDT(execCfg, rotationMaxLegNotional(capped))
	if gotMargin >= avail {
		t.Fatalf("capped margin %.4f should be strictly less than avail %.4f", gotMargin, avail)
	}
}

func TestDefaultCapitalAwarePerpPerpSizingEnabled(t *testing.T) {
	t.Parallel()
	if !DefaultRotationConfig().CapitalAwarePerpPerpSizingEnabled {
		t.Fatal("DefaultRotationConfig CapitalAwarePerpPerpSizingEnabled should default true")
	}
	if !core.DefaultEngineConfig().UnifiedRotation.CapitalAwarePerpPerpSizingEnabled {
		t.Fatal("UnifiedRotation CapitalAwarePerpPerpSizingEnabled should default true")
	}
	rotCfg := RotationConfigFromUnified(core.UnifiedRotationConfig{}, CrossVenueExecutorConfig{})
	if !rotCfg.CapitalAwarePerpPerpSizingEnabled {
		t.Fatal("empty UnifiedRotationConfig should preserve default true via RotationConfigFromUnified")
	}
}
