package app

import (
	"testing"
	"time"
)

// scannerTestInstrumentMeta 为扫描器单测填充默认 tick/step(仅测试用)。
func scannerTestInstrumentMeta(m CrossVenueSpotPerpMarketSnap) CrossVenueSpotPerpMarketSnap {
	if m.HasSpot && (m.SpotTickSize <= 0 || m.SpotStepSize <= 0) {
		m.SpotTickSize = 0.01
		m.SpotStepSize = 0.00001
	}
	if m.HasPerp && (m.PerpTickSize <= 0 || m.PerpStepSize <= 0) {
		m.PerpTickSize = 0.01
		m.PerpStepSize = 0.001
	}
	return m
}

func scannerTestInstrumentMetaMarkets(in map[string]map[string]CrossVenueSpotPerpMarketSnap) map[string]map[string]CrossVenueSpotPerpMarketSnap {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]map[string]CrossVenueSpotPerpMarketSnap, len(in))
	for venue, bySym := range in {
		dst := make(map[string]CrossVenueSpotPerpMarketSnap, len(bySym))
		for sym, m := range bySym {
			dst[sym] = scannerTestInstrumentMeta(m)
		}
		out[venue] = dst
	}
	return out
}

func TestScannerMarketLegInstrumentValid(t *testing.T) {
	if !scannerMarketLegInstrumentValid(0.0001, 0.01) {
		t.Fatal("expected valid tick/step")
	}
	if scannerMarketLegInstrumentValid(0, 0.01) || scannerMarketLegInstrumentValid(0.0001, 0) {
		t.Fatal("expected invalid when tick or step zero")
	}
}

func TestScanCrossVenueSpotPerpExcludesInvalidSpotInstrument(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	nextFunding := scannerTestCarryNextFunding(now)
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	cfg.PeakFundingHaircut = 1.0

	baseFunding := map[string]map[string]CrossVenueSpotPerpFundingSnap{
		"gate": {
			"ESPORTS-USDT": {Rate: scannerTestCarryNearFundingRate, IntervalHours: 8, NextFundingTime: nextFunding},
			"BTC-USDT":     {Rate: scannerTestCarryNearFundingRate, IntervalHours: 8, NextFundingTime: nextFunding},
		},
	}
	validSpot := scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid: 100, PerpMid: 100.1, HasSpot: true, HasPerp: true,
		SpotDepthUSDT: 5000, PerpDepthUSDT: 5000,
	})
	invalidBybitSpot := CrossVenueSpotPerpMarketSnap{
		SpotMid: 0.04, PerpMid: 0.041, HasSpot: true, HasPerp: true,
		SpotDepthUSDT: 5000, PerpDepthUSDT: 5000,
		SpotTickSize: 0, SpotStepSize: 0,
		PerpTickSize: 0.00001, PerpStepSize: 1,
	}
	gatePerp := scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid: 0.04, PerpMid: 0.041, HasSpot: true, HasPerp: true,
		SpotDepthUSDT: 5000, PerpDepthUSDT: 5000,
	})

	excluded := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID: "gate-test",
		Now:        now,
		Symbols:    []string{"ESPORTS-USDT"},
		FundingByVenue: baseFunding,
		MarketsByVenue: map[string]map[string]CrossVenueSpotPerpMarketSnap{
			"bybit": { "ESPORTS-USDT": invalidBybitSpot },
			"gate":  { "ESPORTS-USDT": gatePerp },
		},
		AvailableUSDTByVenue: map[string]float64{"bybit": 500, "gate": 500},
		Config:               cfg,
	})
	if len(excluded) != 0 {
		t.Fatalf("expected ESPORTS excluded (bybit spot no instrument), got %d opps", len(excluded))
	}

	kept := ScanCrossVenueSpotPerpOpportunities(CrossVenueSpotPerpScanInput{
		SnapshotID: "btc-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			"gate": {"BTC-USDT": {Rate: scannerTestCarryNearFundingRate, IntervalHours: 8, NextFundingTime: nextFunding}},
		},
		MarketsByVenue: map[string]map[string]CrossVenueSpotPerpMarketSnap{
			"binance": {"BTC-USDT": validSpot},
			"gate":    {"BTC-USDT": validSpot},
		},
		AvailableUSDTByVenue:         map[string]float64{"binance": 500, "gate": 500},
		Config:                       cfg,
		FundingRateVolPerHourByVenue: scannerTestCarryLowFundingVol("gate", "BTC-USDT"),
	})
	if len(kept) == 0 {
		t.Fatal("expected BTC candidate kept when both legs have valid tick/step")
	}
	for _, opp := range kept {
		if opp.Legs[0].TickSize <= 0 || opp.Legs[0].StepSize <= 0 || opp.Legs[1].TickSize <= 0 || opp.Legs[1].StepSize <= 0 {
			t.Fatalf("kept opp must carry instrument meta: %+v", opp.Legs)
		}
	}
}
