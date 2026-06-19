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

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func testCrossVenuePerpSpotScanInput(now time.Time) CrossVenuePerpSpotScanInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg := DefaultCrossVenuePerpSpotScannerConfig(exec)
	cfg.PeakFundingHaircut = 1.0
	nextFunding := scannerTestCarryNextFunding(now)

	return CrossVenuePerpSpotScanInput{
		SnapshotID: "cvps-snap-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenuePerpSpotFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": func() CrossVenuePerpSpotFundingSnap {
					s := scannerTestEstablishedFundingSnap(-scannerTestCarryNearNegativeFundingRate, nextFunding)
					s.Rate = -scannerTestCarryNearNegativeFundingRate
					return s
				}(),
			},
			"okx": {
				"BTC-USDT": scannerTestEstablishedFundingSnap(-0.0001, nextFunding),
			},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenuePerpSpotMarketSnap{
			binance.VenueName: {
				"BTC-USDT": {
					SpotMid:       100000,
					PerpMid:       100050,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
			bybit.VenueName: {
				"BTC-USDT": {
					SpotMid:       100010,
					PerpMid:       100080,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
			"okx": {
				"BTC-USDT": {
					SpotMid:       100005,
					PerpMid:       100070,
					SpotDepthUSDT: 5000,
					PerpDepthUSDT: 5000,
					HasSpot:       true,
					HasPerp:       true,
				},
			},
		}),
		AvailableUSDTByVenue: map[string]float64{
			binance.VenueName: 200,
			bybit.VenueName:   200,
			"okx":             200,
		},
		BorrowDailyRateByAsset: map[string]float64{
			"BTC": 0.0001,
		},
		BorrowLimitUSDTByAsset: map[string]float64{
			"BTC": 100000,
		},
		BorrowKnownAvailable: testBorrowKnownRegistry(now,
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: true},
			CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: "okx", Available: true},
		),
		Config:                       cfg,
		FundingRateVolPerHourByVenue: scannerTestCarryLowFundingVol(bybit.VenueName, "BTC-USDT"),
	}
}

func TestScanCrossVenuePerpSpotOpportunitiesHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	opps := ScanCrossVenuePerpSpotOpportunities(in)

	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 (bybit perp + binance/okx spot)", len(opps))
	}
	for _, opp := range opps {
		if opp.StrategyType != UnifiedStrategyCrossVenuePerpSpot {
			t.Fatalf("strategy_type = %q", opp.StrategyType)
		}
		if len(opp.Legs) != 2 {
			t.Fatalf("legs = %d", len(opp.Legs))
		}
		if opp.Legs[0].Market != "perp" || opp.Legs[0].Side != "long" || opp.Legs[0].Venue != bybit.VenueName {
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
		if opp.CapitalRequiredByVenue[opp.Legs[1].Venue] != opp.Legs[1].Notional {
			t.Fatalf("spot capital = %.2f, want %.2f", opp.CapitalRequiredByVenue[opp.Legs[1].Venue], opp.Legs[1].Notional)
		}
		perpMargin := crossVenueSpotPerpPerpMarginRequired(crossVenuePerpSpotAsCrossVenueConfig(in.Config), opp.Legs[0].Notional)
		if opp.CapitalRequiredByVenue[bybit.VenueName] != perpMargin {
			t.Fatalf("perp margin = %.4f, want %.4f", opp.CapitalRequiredByVenue[bybit.VenueName], perpMargin)
		}
		if opp.RequiresRebalance {
			t.Fatalf("unexpected requires_rebalance for funded venues")
		}
	}
}

func TestScanCrossVenuePerpSpotRequiresRebalanceWhenVenueShort(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.AvailableUSDTByVenue[binance.VenueName] = 3
	in.AvailableUSDTByVenue[bybit.VenueName] = 500

	opps := ScanCrossVenuePerpSpotOpportunities(in)
	for _, opp := range opps {
		if !opportunityHasTradableLegs(opp) {
			continue
		}
		if opp.Legs[1].Venue == binance.VenueName {
			t.Fatalf("expected no binance spot opportunity when spot venue cannot support 5U floor, got %+v", opp)
		}
	}
}

func TestCrossVenuePerpSpotRequiresRebalanceHelper(t *testing.T) {
	cfg := DefaultCrossVenuePerpSpotScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	available := map[string]float64{binance.VenueName: 10, bybit.VenueName: 100}
	if !crossVenuePerpSpotRequiresRebalance(binance.VenueName, bybit.VenueName, 50, 10, available, cfg) {
		t.Fatal("expected requires_rebalance when spot margin exceeds available")
	}
	if crossVenuePerpSpotRequiresRebalance(binance.VenueName, bybit.VenueName, 5, 4, available, cfg) {
		t.Fatal("unexpected requires_rebalance when capital covers legs")
	}
}

func TestScanCrossVenuePerpSpotExcludesPositiveFunding(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = CrossVenuePerpSpotFundingSnap{
		Rate:            0.0002,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.0001,
	}
	in.FundingByVenue["okx"]["BTC-USDT"] = CrossVenuePerpSpotFundingSnap{
		Rate:            0.0001,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.00008,
	}
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	if len(opps) != 0 {
		t.Fatalf("opportunities = %d, want 0 for positive funding", len(opps))
	}
}

func TestScanCrossVenuePerpSpotExcludesNotBorrowable(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.BorrowKnownAvailable = NewBorrowKnownAvailableRegistry(24 * time.Hour)
	in.BorrowDailyRateByAsset = map[string]float64{}
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	// P1④: 全部不可借 → 筛选阶段排除,候选集为空。
	if len(opps) != 0 {
		t.Fatalf("opportunities = %d, want 0 (not-borrowable excluded at filter)", len(opps))
	}
}

func TestScanCrossVenuePerpSpotExcludesMidDeviationTooLarge(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       105000,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	for _, opp := range opps {
		if opp.Legs[1].Venue == binance.VenueName {
			t.Fatalf("unexpected opportunity with large mid deviation: %+v", opp)
		}
	}
}

func TestScanCrossVenuePerpSpotExcludesInsufficientDepth(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	for _, opp := range opps {
		if opportunityHasTradableLegs(opp) && len(opp.Legs) >= 2 && opp.Legs[1].Venue == binance.VenueName {
			t.Fatalf("unexpected tradable opportunity with insufficient depth: notional=%.2f", opp.Legs[1].Notional)
		}
	}
	binanceOpp := findCrossVenuePerpSpotOpp(opps, "BTC-USDT", binance.VenueName)
	if binanceOpp == nil {
		t.Fatal("expected rejected row for insufficient depth")
	}
	if !containsPerpSpotReject(binanceOpp.RejectReasons, crossVenuePerpSpotRejectInsufficientDepth) {
		t.Fatalf("reject_reasons = %#v, want %q", binanceOpp.RejectReasons, crossVenuePerpSpotRejectInsufficientDepth)
	}
}

func TestScanCrossVenuePerpSpotExcludesMarginStressLiquidation(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.Config.Exec.MaxEffectiveLeverage = 20
	in.Config.PerpWalletBufferRatio = 0.01
	in.Config.PerpMaintenanceMarginRate = 0.05
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 rejected rows", len(opps))
	}
	for _, opp := range opps {
		if !containsPerpSpotReject(opp.RejectReasons, crossVenuePerpSpotRejectMarginStress) {
			t.Fatalf("reject_reasons = %#v, want %q", opp.RejectReasons, crossVenuePerpSpotRejectMarginStress)
		}
	}
}

func TestScanCrossVenuePerpSpotBasisAndNetEV(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.FundingRateVolPerHourByVenue = scannerTestCarryLowFundingVol(bybit.VenueName, "BTC-USDT")
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenuePerpSpotMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenuePerpSpotMarketSnap{
		SpotMid:       100010,
		PerpMid:       100200,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected opportunities")
	}
	opp := opps[0]
	if opp.ExpectedFundingUSDT <= 0 {
		t.Fatalf("expected_funding_usdt = %.4f, want >0", opp.ExpectedFundingUSDT)
	}
	cvCfg := crossVenuePerpSpotAsCrossVenueConfig(in.Config)
	spotVenue := opp.Legs[1].Venue
	perpVenue := opp.Legs[0].Venue
	spotMid := in.MarketsByVenue[spotVenue][opp.Symbol].SpotMid
	perpMid := in.MarketsByVenue[perpVenue][opp.Symbol].PerpMid
	entryBasis := crossVenueSpotPerpEntryBasisUSDT(spotMid, perpMid, opp.Legs[0].Notional)
	expectedBasis := crossVenuePerpSpotExpectedBasisPnL(entryBasis, opp.Legs[0].Notional, cvCfg)
	reconstructed := opp.ExpectedFundingUSDT + expectedBasis - opp.ExpectedOpenCostUSDT - opp.ExpectedCloseCostUSDT - opp.ExpectedSlippageUSDT - opp.ExpectedBorrowCostUSDT - opp.ExpectedRollbackCostUSDT
	if math.Abs(opp.ExpectedNetEVUSDT-reconstructed) > 1e-6 {
		t.Fatalf("net_ev = %.6f, reconstructed = %.6f", opp.ExpectedNetEVUSDT, reconstructed)
	}
}

func TestAppendCrossVenuePerpSpotCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 5
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected at least one opportunity")
	}

	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	AppendCrossVenuePerpSpotCandidates(dir, "cvps-cycle-test", execCfg, opps)

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
	if rec.StrategyType != UnifiedStrategyCrossVenuePerpSpot {
		t.Fatalf("strategy_type = %q", rec.StrategyType)
	}
	wantExec := len(opps[0].RejectReasons) == 0
	if rec.WouldExecute != wantExec {
		t.Fatalf("would_execute = %v, want %v (reject_reasons=%v)", rec.WouldExecute, wantExec, opps[0].RejectReasons)
	}
}

func TestScanCrossVenuePerpSpotDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	beforeFunding := in.FundingByVenue[bybit.VenueName]["BTC-USDT"]
	beforeMarket := in.MarketsByVenue[binance.VenueName]["BTC-USDT"]
	_ = ScanCrossVenuePerpSpotOpportunities(in)
	if in.FundingByVenue[bybit.VenueName]["BTC-USDT"] != beforeFunding {
		t.Fatal("scanner mutated funding input")
	}
	if in.MarketsByVenue[binance.VenueName]["BTC-USDT"] != beforeMarket {
		t.Fatal("scanner mutated market input")
	}
}

func TestScanCrossVenuePerpSpotAppendMultipleLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	opps := ScanCrossVenuePerpSpotOpportunities(in)
	AppendCrossVenuePerpSpotCandidates(dir, "cycle", testCrossVenueExecutorConfig().CrossVenueExecutor, opps)

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

func findCrossVenuePerpSpotOpp(opps []UnifiedOpportunity, symbol, spotVenue string) *UnifiedOpportunity {
	for i := range opps {
		if opps[i].Symbol != symbol {
			continue
		}
		if strings.Contains(opps[i].ID, spotVenue) {
			return &opps[i]
		}
	}
	return nil
}

func TestScanCrossVenuePerpSpotFunnelRollupNotBorrowable(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenuePerpSpotScanInput(now)
	in.BorrowKnownAvailable = NewBorrowKnownAvailableRegistry(24 * time.Hour)
	in.BorrowDailyRateByAsset = map[string]float64{}
	out := captureCrossVenueCarryFunnelLog(t, func() {
		_ = ScanCrossVenuePerpSpotOpportunities(in)
	})
	if !strings.Contains(out, "[carry-funnel] cvps") {
		t.Fatalf("missing funnel rollup log: %s", out)
	}
	if !strings.Contains(out, "build_not_borrowable=2") || !strings.Contains(out, "emitted=0") {
		t.Fatalf("expected not-borrowable funnel collapse, got: %s", out)
	}
}

func TestScanCrossVenuePerpSpotFunnelRollupHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	out := captureCrossVenueCarryFunnelLog(t, func() {
		_ = ScanCrossVenuePerpSpotOpportunities(testCrossVenuePerpSpotScanInput(now))
	})
	if !strings.Contains(out, "pair_attempts=2") || !strings.Contains(out, "emitted=2") {
		t.Fatalf("expected happy-path funnel counts, got: %s", out)
	}
}
