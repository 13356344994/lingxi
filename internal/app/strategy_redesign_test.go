package app

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestIndexSnapshotsBySymbolIncludesLowHistoryCoin(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	next := now.Add(30 * time.Minute)
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			SpreadPersistenceScans: 2,
			FundingAPRSanityCap:    5.0,
		},
		fundingHistory: make(map[string]map[string][]float64),
	}
	byVenue := map[string][]venue.FundingSnapshot{
		bybit.VenueName: {{Symbol: "NEW-USDT", LastFundingRate: 0.002, IntervalHours: 8, NextFundingTime: next}},
		okx.VenueName:   {{Symbol: "NEW-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: next}},
	}
	idx := s.indexSnapshotsBySymbol(byVenue)
	entries := idx["NEW-USDT"]
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for first-scan coin, got %d", len(entries))
	}
	for _, e := range entries {
		if e.FundingRate <= 0 {
			t.Fatalf("expected live forward rate, got %+v", e)
		}
	}
}

func TestConfidenceSizeMultiplierRisesWithHistoryQuality(t *testing.T) {
	floor := 0.25
	low := crossVenueConfidenceSizeMultiplier(floor, 0, 0)
	high := crossVenueConfidenceSizeMultiplier(floor, 1.0, 1.0)
	if math.Abs(low-floor) > 1e-9 {
		t.Fatalf("zero history should hit floor: got %.4f want %.4f", low, floor)
	}
	if high < 0.99 {
		t.Fatalf("full history should approach 1.0, got %.4f", high)
	}
	if low >= high {
		t.Fatalf("size multiplier should rise with history: low=%.4f high=%.4f", low, high)
	}
}

func TestCrossVenueLowHistoryOpportunityNotRejectedByHistoryGate(t *testing.T) {
	opp := CrossVenueOpportunity{
		Symbol:                "NEW-USDT",
		HighVenue:             bybit.VenueName,
		LowVenue:              okx.VenueName,
		NetEV:                 2,
		CapitalEfficiencyEV:   0.5,
		RiskAdjustedEVPerHour: 0.5,
		HistoryQualityScore:   0.0,
		StabilityScore:        0.0,
		LiquidityScore:        0.45,
		ExecutionScore:        0.70,
		RecoveryQualityScore:  0.65,
		TradableNotional:      20,
	}
	mode, reject := crossVenueSelectOpenMode(opp, false)
	if reject != "" || mode != crossVenueOpenFallback {
		t.Fatalf("low history should not hard-reject, mode=%s reject=%q", mode, reject)
	}
}

func TestCrossVenueSpotPerpLegacyDepthSizesDownToCap(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(CrossVenueExecutorConfig{
		MaxPositionUSDT: 50,
		DepthUsageRatio: 0.20,
	})
	cfg.Exec.SizingMaxSlippageBps = 0
	mkt := CrossVenueSpotPerpMarketSnap{
		SpotMid: 100, PerpMid: 100,
		SpotDepthUSDT: 40, PerpDepthUSDT: 5000,
		HasSpot: true, HasPerp: true,
	}
	got := crossVenueSpotPerpTradableNotional(cfg, mkt, mkt)
	want := 8.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %.2f want %.2f (size down to depth cap)", got, want)
	}
	thin := mkt
	thin.SpotDepthUSDT = 20
	if crossVenueSpotPerpTradableNotional(cfg, thin, thin) != 0 {
		t.Fatal("cap below min notional should return 0")
	}
}

func TestCarryLowHistoryProducesCandidateWithSmallNotional(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	sym := "NEWCOIN-USDT"
	in.Symbols = []string{sym}
	if in.FundingByVenue[bybit.VenueName] == nil {
		in.FundingByVenue[bybit.VenueName] = map[string]CrossVenueSpotPerpFundingSnap{}
	}
	in.FundingByVenue[bybit.VenueName][sym] = CrossVenueSpotPerpFundingSnap{
		Rate: 0.01, IntervalHours: 8, NextFundingTime: scannerTestCarryNextFunding(now),
	}
	if in.MarketsByVenue[binance.VenueName] == nil {
		in.MarketsByVenue[binance.VenueName] = map[string]CrossVenueSpotPerpMarketSnap{}
	}
	if in.MarketsByVenue[bybit.VenueName] == nil {
		in.MarketsByVenue[bybit.VenueName] = map[string]CrossVenueSpotPerpMarketSnap{}
	}
	in.MarketsByVenue[binance.VenueName][sym] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid: 1, PerpMid: 1.001, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true,
	})
	in.MarketsByVenue[bybit.VenueName][sym] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid: 1, PerpMid: 1.001, SpotDepthUSDT: 5000, PerpDepthUSDT: 5000, HasSpot: true, HasPerp: true,
	})
	in.Config.Exec.LowConfidenceSizeFloorFraction = 0.25
	in.Config.Exec.CarryNoHistoryHaircut = 1.0
	in.Config.PeakFundingHaircut = 1.0
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected low-history coin to produce candidate")
	}
	notional := crossVenueSpotPerpLegNotional(opps[0])
	maxNotional := in.Config.Exec.MaxPositionUSDT
	if maxNotional <= 0 {
		maxNotional = 50
	}
	if notional >= maxNotional*0.5 {
		t.Fatalf("expected small confidence-sized notional, got %.2f vs max %.2f", notional, maxNotional)
	}
	if opps[0].ExpectedFundingUSDT <= 0 {
		t.Fatal("expected positive funding EV on live rate")
	}
}

func TestPerpPerpFinalizeAppliesLowHistorySizeFloor(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:                100,
		LowConfidenceSizeFloorFraction: 0.25,
		MaxEffectiveLeverage:           2,
		MarginSafetyFactor:             1.5,
		EVExpectedSlippageBps:          3,
	}
	now := time.Now().UTC()
	opp := CrossVenueOpportunity{
		Symbol: "NEW-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		HighFundingRate: 0.002, LowFundingRate: 0.0001,
		HighIntervalHours: 8, LowIntervalHours: 8,
		HighNextFundingTime: now.Add(time.Hour), LowNextFundingTime: now.Add(time.Hour),
		TradableNotional: 100, HistoryQualityScore: 0, StabilityScore: 0,
	}
	opp = crossVenueEstimateOpportunityMetrics(cfg, opp, now)
	opp.HistoryQualityScore = 0
	opp.StabilityScore = 0
	ranked := []CrossVenueOpportunity{opp}
	crossVenueFinalizeOpportunityRanking(cfg, ranked)
	if ranked[0].SizeMultiplier > 0.26 {
		t.Fatalf("expected size multiplier near floor 0.25, got %.4f", ranked[0].SizeMultiplier)
	}
}
