package app

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue/bybit"
)

func testSingleVenuePerpSpotScanInput(now time.Time) SingleVenuePerpSpotScanInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg := DefaultSingleVenuePerpSpotScannerConfig(exec)
	cfg.PeakFundingHaircut = 1.0
	nextFunding := scannerTestCarryNextFunding(now)

	return SingleVenuePerpSpotScanInput{
		SnapshotID: "svps-snap-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT", "ETH-USDT"},
		Venues:     []string{bybit.VenueName},
		FundingByVenue: map[string]map[string]SingleVenuePerpSpotFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": {
					Rate:            -scannerTestCarryNearNegativeFundingRate,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
				},
				"ETH-USDT": {
					Rate:            -scannerTestCarryNearNegativeFundingRate,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
				},
			},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]SingleVenuePerpSpotMarketSnap{
			bybit.VenueName: {
				"BTC-USDT": {
					SpotMid:       100000,
					PerpMid:       100050,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
				"ETH-USDT": {
					SpotMid:       3000,
					PerpMid:       3006,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
		}),
		AvailableUSDTByVenue: map[string]float64{
			bybit.VenueName: 500,
		},
		BorrowDailyRateByAsset: map[string]float64{
			"BTC": 0.0001,
			"ETH": 0.00012,
		},
		BorrowLimitUSDTByAsset: map[string]float64{
			"BTC": 100000,
			"ETH": 100000,
		},
		BorrowKnownAvailable: testBorrowKnownRegistry(now,
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: bybit.VenueName, Available: true},
			CrossVenueBorrowProbe{Symbol: "ETH-USDT", Venue: bybit.VenueName, Available: true},
		),
		Config: cfg,
		FundingRateVolPerHourByVenue: map[string]map[string]float64{
			bybit.VenueName: {
				"BTC-USDT": 0.00001,
				"ETH-USDT": 0.00001,
			},
		},
	}
}

func TestScanSingleVenuePerpSpotOpportunitiesHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	opps := ScanSingleVenuePerpSpotOpportunities(in)

	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2", len(opps))
	}
	for _, opp := range opps {
		if opp.StrategyType != UnifiedStrategySingleVenuePerpSpot {
			t.Fatalf("strategy_type = %q", opp.StrategyType)
		}
		if len(opp.Legs) != 2 {
			t.Fatalf("legs = %d", len(opp.Legs))
		}
		if opp.Legs[0].Venue != bybit.VenueName || opp.Legs[1].Venue != bybit.VenueName {
			t.Fatalf("both legs must be same venue, got %+v", opp.Legs)
		}
		if opp.Legs[0].Market != "perp" || opp.Legs[0].Side != "long" {
			t.Fatalf("perp leg = %+v", opp.Legs[0])
		}
		if opp.Legs[1].Market != "spot" || opp.Legs[1].Side != "short" {
			t.Fatalf("spot leg = %+v", opp.Legs[1])
		}
		if opp.ExpectedFundingUSDT <= 0 {
			t.Fatalf("expected_funding_usdt = %.6f, want >0", opp.ExpectedFundingUSDT)
		}
		if opp.ExpectedBorrowCostUSDT <= 0 {
			t.Fatalf("expected_borrow_cost_usdt = %.6f, want >0", opp.ExpectedBorrowCostUSDT)
		}
		perpMargin := crossVenueSpotPerpPerpMarginRequired(singleVenuePerpSpotAsCrossVenueConfig(in.Config), opp.Legs[0].Notional)
		wantCapital := opp.Legs[1].Notional + perpMargin
		if opp.CapitalRequiredByVenue[bybit.VenueName] != wantCapital {
			t.Fatalf("capital = %.4f, want spot_margin(%.2f)+perp_margin(%.4f)=%.4f",
				opp.CapitalRequiredByVenue[bybit.VenueName], opp.Legs[1].Notional, perpMargin, wantCapital)
		}
		if opp.RequiresRebalance {
			t.Fatalf("unexpected requires_rebalance with sufficient funds")
		}
	}
}

func TestScanSingleVenuePerpSpotRequiresRebalanceWhenFundsShort(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 3

	opps := ScanSingleVenuePerpSpotOpportunities(in)
	for _, opp := range opps {
		if opportunityHasTradableLegs(opp) {
			t.Fatalf("expected no tradable opportunities when capital cannot support 5U floor, got %+v", opp)
		}
	}
}

func TestSingleVenuePerpSpotRequiresRebalanceHelper(t *testing.T) {
	cfg := DefaultSingleVenuePerpSpotScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	available := map[string]float64{bybit.VenueName: 20}
	if !singleVenuePerpSpotRequiresRebalance(bybit.VenueName, 30, 10, available, cfg) {
		t.Fatal("expected requires_rebalance when total required exceeds available")
	}
	if singleVenuePerpSpotRequiresRebalance(bybit.VenueName, 5, 4, available, cfg) {
		t.Fatal("unexpected requires_rebalance when capital covers legs")
	}
}

func TestScanSingleVenuePerpSpotRecordsPositiveFundingReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotFundingSnap{
		Rate:            0.0002,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.0001,
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for positive funding")
	}
	if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectPositiveFunding) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectPositiveFunding)
	}
	if btc.RawFundingRate <= 0 {
		t.Fatalf("positive funding reject should carry raw rate, got %.8f", btc.RawFundingRate)
	}
}

func TestScanSingleVenuePerpSpotRecordsNotBorrowableReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	reg := testBorrowKnownRegistry(now,
		CrossVenueBorrowProbe{Symbol: "ETH-USDT", Venue: bybit.VenueName, Available: true},
	)
	in.BorrowKnownAvailable = reg
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for not borrowable")
	}
	if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectNotBorrowable) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectNotBorrowable)
	}
}

func TestScanSingleVenuePerpSpotReversePositiveFundingWhenEnabled(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.Config.Exec.SingleVenueReverseFundingEnabled = true
	in.Config.PeakFundingHaircut = 1.0
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotFundingSnap{
		Rate:            scannerTestCarryNearNegativeFundingRate,
		IntervalHours:   8,
		NextFundingTime: scannerTestCarryNextFunding(now),
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected reverse positive funding candidate")
	}
	if len(btc.RejectReasons) > 0 || !opportunityHasTradableLegs(*btc) {
		t.Fatalf("expected tradable reverse candidate, reject=%#v legs=%+v", btc.RejectReasons, btc.Legs)
	}
	if btc.Legs[0].Side != "short" || btc.Legs[1].Side != "buy" {
		t.Fatalf("reverse legs = %+v", btc.Legs)
	}
	if btc.ExpectedNetEVUSDT <= 0 {
		t.Fatalf("expected positive EV, got %.6f", btc.ExpectedNetEVUSDT)
	}
	if btc.ExpectedBorrowCostUSDT != 0 {
		t.Fatalf("reverse perp-spot should not borrow, got %.6f", btc.ExpectedBorrowCostUSDT)
	}
}

func TestScanSingleVenuePerpSpotRecordsBorrowOverLimitReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.BorrowLimitUSDTByAsset["BTC"] = 10
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected opportunity row for borrow over limit")
	}
	if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectBorrowOverLimit) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectBorrowOverLimit)
	}
}

func TestScanSingleVenuePerpSpotRecordsNoSpotMarketReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotMarketSnap{
		SpotMid:       0,
		PerpMid:       100050,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       false,
		HasPerp:       true,
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for missing spot market")
	}
	if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectNoSpotMarket) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectNoSpotMarket)
	}
}

func TestScanSingleVenuePerpSpotExcludesMidDeviationTooLarge(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       105000,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	if findSingleVenuePerpSpotOpp(opps, "BTC-USDT") != nil {
		t.Fatal("expected no opportunity for large mid deviation")
	}
}

func TestScanSingleVenuePerpSpotRecordsInsufficientDepthReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for insufficient depth")
	}
	if !containsPerpSpotReject(btc.RejectReasons, singleVenuePerpSpotRejectInsufficientDepth) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenuePerpSpotRejectInsufficientDepth)
	}
}

func TestScanSingleVenuePerpSpotRecordsMarginStressReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.Config.Exec.MaxEffectiveLeverage = 20
	in.Config.PerpWalletBufferRatio = 0.01
	in.Config.PerpMaintenanceMarginRate = 0.05
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 rejected rows", len(opps))
	}
	for _, opp := range opps {
		if !containsPerpSpotReject(opp.RejectReasons, singleVenuePerpSpotRejectMarginStress) {
			t.Fatalf("reject_reasons = %#v, want %q", opp.RejectReasons, singleVenuePerpSpotRejectMarginStress)
		}
	}
}

func TestScanSingleVenuePerpSpotBasisAndNetEV(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       100200,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	}
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	btc := findSingleVenuePerpSpotOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("missing BTC opportunity")
	}
	spotPerpEntryBasis := crossVenueSpotPerpEntryBasisUSDT(100000, 100200, btc.Legs[0].Notional)
	if spotPerpEntryBasis <= 0 {
		t.Fatalf("spot-perp entry basis = %.4f, want >0 for perp premium", spotPerpEntryBasis)
	}
	cvCfg := singleVenuePerpSpotAsCrossVenueConfig(in.Config)
	expectedBasis := crossVenuePerpSpotExpectedBasisPnL(spotPerpEntryBasis, btc.Legs[0].Notional, cvCfg)
	if expectedBasis >= 0 {
		t.Fatalf("perp-spot basis pnl = %.4f, want <0 when perp premium", expectedBasis)
	}
	reconstructed := btc.ExpectedFundingUSDT + expectedBasis - btc.ExpectedOpenCostUSDT - btc.ExpectedCloseCostUSDT - btc.ExpectedSlippageUSDT - btc.ExpectedBorrowCostUSDT - btc.ExpectedRollbackCostUSDT
	if math.Abs(btc.ExpectedNetEVUSDT-reconstructed) > 1e-6 {
		t.Fatalf("net_ev = %.6f, reconstructed = %.6f", btc.ExpectedNetEVUSDT, reconstructed)
	}
}

func TestPerpSpotBorrowCostUSDT(t *testing.T) {
	got := perpSpotBorrowCostUSDT("BTC", 100, 24, map[string]float64{"BTC": 0.001}, 0)
	want := 100 * 0.001 * 1.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("borrow cost = %.6f, want %.6f", got, want)
	}
}

func TestAppendSingleVenuePerpSpotCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 50
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected at least one opportunity")
	}

	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	AppendSingleVenuePerpSpotCandidates(dir, "svps-cycle-test", execCfg, opps)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(opps) {
		t.Fatalf("lines = %d, want %d", len(lines), len(opps))
	}
	var rec UnifiedCandidateLogRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.StrategyType != UnifiedStrategySingleVenuePerpSpot {
		t.Fatalf("strategy_type = %q", rec.StrategyType)
	}
	wantExec := len(opps[0].RejectReasons) == 0
	if rec.WouldExecute != wantExec {
		t.Fatalf("would_execute = %v, want %v (reject_reasons=%v)", rec.WouldExecute, wantExec, opps[0].RejectReasons)
	}
}

func TestScanSingleVenuePerpSpotDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	beforeFunding := in.FundingByVenue[bybit.VenueName]["BTC-USDT"]
	beforeMarket := in.MarketsByVenue[bybit.VenueName]["BTC-USDT"]
	_ = ScanSingleVenuePerpSpotOpportunities(in)
	if in.FundingByVenue[bybit.VenueName]["BTC-USDT"] != beforeFunding {
		t.Fatal("scanner mutated funding input")
	}
	if in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] != beforeMarket {
		t.Fatal("scanner mutated market input")
	}
}

func TestScanSingleVenuePerpSpotAppendMultipleLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenuePerpSpotScanInput(now)
	opps := ScanSingleVenuePerpSpotOpportunities(in)
	AppendSingleVenuePerpSpotCandidates(dir, "cycle", testCrossVenueExecutorConfig().CrossVenueExecutor, opps)

	path := filepath.Join(opportunitiesRecordDir(dir), unifiedCandidatesLogFileName)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		count++
	}
	if count != len(opps) {
		t.Fatalf("lines = %d, want %d", count, len(opps))
	}
}

func findSingleVenuePerpSpotOpp(opps []UnifiedOpportunity, symbol string) *UnifiedOpportunity {
	for i := range opps {
		if opps[i].Symbol == symbol {
			return &opps[i]
		}
	}
	return nil
}

func containsPerpSpotReject(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
