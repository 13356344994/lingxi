package app

import (
	"bytes"
	"context"
	"log"
	"testing"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
)

func TestCrossVenueScannerBorrowProbeSkipsBorrowDisabledVenue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	gateVenue := &fakeBorrowQuoteVenue{
		fakeFundingVenue: fakeFundingVenue{name: gate.VenueName},
		quotes: map[string]venue.BorrowQuote{
			"BTC": {Asset: "BTC", Available: true, MaxBorrowable: 1, HourlyRate: 0.0001},
		},
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			BorrowDisabledVenues:   "gate",
			BorrowProbeFundingTopK: 1,
		},
		logger:  log.New(&bytes.Buffer{}, "", 0),
		nowFn:   func() time.Time { return now },
		sleepFn: func(time.Duration) {},
		venues:  map[string]venue.Venue{gate.VenueName: gateVenue},
	}
	funding := map[string]map[string]CrossVenueFundingEntry{
		gate.VenueName: {"BTC-USDT": {Rate: -0.003}},
	}
	probes := s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if gateVenue.calls != 0 {
		t.Fatalf("gate FetchBorrowQuote calls = %d, want 0 when borrow-disabled", gateVenue.calls)
	}
	if len(probes) != 0 {
		t.Fatalf("probes = %d, want 0 when only venue is borrow-disabled", len(probes))
	}

	s.cfg.BorrowDisabledVenues = ""
	probes = s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if gateVenue.calls != 1 {
		t.Fatalf("after re-enable gate calls = %d, want 1", gateVenue.calls)
	}
	if len(probes) != 1 || probes[0].Venue != gate.VenueName {
		t.Fatalf("probes after re-enable = %+v", probes)
	}
}

func TestScanCrossVenuePerpSpotExcludesBorrowDisabledSpotVenue(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.BorrowDisabledVenues = core.ParseVenueList("gate")
	in.MarketsByVenue["gate"] = map[string]CrossVenuePerpSpotMarketSnap{
		"BTC-USDT": scannerTestInstrumentMeta(CrossVenuePerpSpotMarketSnap{
			SpotMid:       100000,
			PerpMid:       100050,
			SpotDepthUSDT: 5000,
			PerpDepthUSDT: 5000,
			HasSpot:       true,
			HasPerp:       true,
		}),
	}
	// gate 借币禁用(应被排除);binance/okx 可借,应产出真候选(P1④:不可借才在筛选阶段剔除)。
	in.BorrowKnownAvailable = testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "gate", Available: true},
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "binance", Available: true},
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "okx", Available: true},
	)

	opps := ScanCrossVenuePerpSpotOpportunities(in)
	for _, opp := range opps {
		if opp.Legs[1].Venue == gate.VenueName {
			t.Fatalf("borrow-disabled gate must not be short-spot leg, got %+v", opp)
		}
	}
	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 (binance/okx spot only)", len(opps))
	}
}

func TestScanSingleVenuePerpSpotSkipsBorrowDisabledVenue(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.Venues = []string{gate.VenueName}
	in.FundingByVenue = map[string]map[string]SingleVenuePerpSpotFundingSnap{
		gate.VenueName: in.FundingByVenue[bybit.VenueName],
	}
	in.MarketsByVenue = map[string]map[string]SingleVenuePerpSpotMarketSnap{
		gate.VenueName: in.MarketsByVenue[bybit.VenueName],
	}
	in.AvailableUSDTByVenue = map[string]float64{gate.VenueName: 500}
	in.BorrowKnownAvailable = testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: gate.VenueName, Available: true},
		CrossVenueBorrowProbe{Symbol: "ETH-USDT", Venue: gate.VenueName, Available: true},
	)
	in.BorrowDisabledVenues = core.ParseVenueList("gate")

	opps := ScanSingleVenuePerpSpotOpportunities(in)
	if len(opps) != 0 {
		t.Fatalf("borrow-disabled gate SVPS should produce 0 rows, got %d", len(opps))
	}
}

func TestScanSingleVenueSpotPerpGateBuySpotStillEligible(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.Venues = []string{gate.VenueName}
	in.FundingByVenue = map[string]map[string]SingleVenueSpotPerpFundingSnap{
		gate.VenueName: in.FundingByVenue[bybit.VenueName],
	}
	in.MarketsByVenue = map[string]map[string]SingleVenueSpotPerpMarketSnap{
		gate.VenueName: in.MarketsByVenue[bybit.VenueName],
	}
	in.AvailableUSDTByVenue = map[string]float64{gate.VenueName: 500}
	in.FundingRateVolPerHourByVenue = map[string]map[string]float64{
		gate.VenueName: in.FundingRateVolPerHourByVenue[bybit.VenueName],
	}

	opps := ScanSingleVenueSpotPerpOpportunities(in)
	if len(opps) != 2 {
		t.Fatalf("gate buy-spot SVSP opportunities = %d, want 2", len(opps))
	}
	for _, opp := range opps {
		if opp.Legs[0].Venue != gate.VenueName || opp.Legs[0].Market != "spot" || opp.Legs[0].Side != "buy" {
			t.Fatalf("expected gate buy-spot leg, got %+v", opp.Legs[0])
		}
		if opp.ExpectedFundingUSDT <= 0 {
			t.Fatalf("expected positive funding on gate buy-spot, got %.6f", opp.ExpectedFundingUSDT)
		}
		for _, reason := range opp.RejectReasons {
			if reason == singleVenuePerpSpotRejectNotBorrowable || reason == crossVenuePerpSpotRejectNotBorrowable {
				t.Fatalf("gate buy-spot must not be borrow-rejected, got %v", opp.RejectReasons)
			}
		}
	}
}

func TestScanCrossVenueSpotPerpGateBuySpotStillEligible(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[gate.VenueName] = in.MarketsByVenue[binance.VenueName]
	delete(in.MarketsByVenue, binance.VenueName)
	in.AvailableUSDTByVenue[gate.VenueName] = in.AvailableUSDTByVenue[binance.VenueName]
	delete(in.AvailableUSDTByVenue, binance.VenueName)

	opps := ScanCrossVenueSpotPerpOpportunities(in)
	var gateSpotBuy *UnifiedOpportunity
	for i := range opps {
		if opps[i].Legs[0].Venue == gate.VenueName && opps[i].Legs[0].Market == "spot" {
			gateSpotBuy = &opps[i]
			break
		}
	}
	if gateSpotBuy == nil {
		t.Fatalf("expected gate buy-spot CVSP candidate among %d opps", len(opps))
	}
	if gateSpotBuy.ExpectedFundingUSDT <= 0 {
		t.Fatalf("expected positive funding on gate buy-spot CVSP, got %.6f", gateSpotBuy.ExpectedFundingUSDT)
	}
	for _, reason := range gateSpotBuy.RejectReasons {
		if reason == singleVenuePerpSpotRejectNotBorrowable || reason == crossVenuePerpSpotRejectNotBorrowable {
			t.Fatalf("gate buy-spot must not be borrow-rejected, got %v", gateSpotBuy.RejectReasons)
		}
	}
}
