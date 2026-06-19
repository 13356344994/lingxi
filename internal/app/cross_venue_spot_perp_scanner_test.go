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

func testCrossVenueSpotPerpScanInput(now time.Time) CrossVenueSpotPerpScanInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg := DefaultCrossVenueSpotPerpScannerConfig(exec)
	nextFunding := scannerTestCarryNextFunding(now)

	return CrossVenueSpotPerpScanInput{
		SnapshotID: "cvsp-snap-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT"},
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": {
					Rate:            scannerTestCarryNearFundingRate,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
					SpikeThreshold:  0.00025,
				},
			},
			"okx": {
				"BTC-USDT": {
					Rate:            0.0001,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
					SpikeThreshold:  0.00008,
				},
			},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]CrossVenueSpotPerpMarketSnap{
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
		Config:                       cfg,
		FundingRateVolPerHourByVenue: scannerTestCarryLowFundingVol(bybit.VenueName, "BTC-USDT"),
	}
}

func TestScanCrossVenueSpotPerpOpportunitiesHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	opps := ScanCrossVenueSpotPerpOpportunities(in)

	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 (binance+bybit, okx+bybit)", len(opps))
	}
	for _, opp := range opps {
		if opp.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
			t.Fatalf("strategy_type = %q", opp.StrategyType)
		}
		if len(opp.Legs) != 2 {
			t.Fatalf("legs = %d", len(opp.Legs))
		}
		if opp.Legs[0].Market != "spot" || opp.Legs[0].Side != "buy" {
			t.Fatalf("spot leg = %+v", opp.Legs[0])
		}
		if opp.Legs[1].Market != "perp" || opp.Legs[1].Side != "short" || opp.Legs[1].Venue != bybit.VenueName {
			t.Fatalf("perp leg = %+v", opp.Legs[1])
		}
		if opp.ExpectedFundingUSDT <= 0 {
			t.Fatalf("expected_funding_usdt = %.6f, want >0", opp.ExpectedFundingUSDT)
		}
		if opp.CapitalRequiredByVenue[opp.Legs[0].Venue] != opp.Legs[0].Notional {
			t.Fatalf("spot capital = %.2f, want %.2f", opp.CapitalRequiredByVenue[opp.Legs[0].Venue], opp.Legs[0].Notional)
		}
		perpMargin := crossVenueSpotPerpPerpMarginRequired(in.Config, opp.Legs[1].Notional)
		if opp.CapitalRequiredByVenue[bybit.VenueName] != perpMargin {
			t.Fatalf("perp margin = %.4f, want %.4f", opp.CapitalRequiredByVenue[bybit.VenueName], perpMargin)
		}
		if opp.RequiresRebalance {
			t.Fatalf("unexpected requires_rebalance for funded venues")
		}
	}
}

func TestScanCrossVenueSpotPerpRequiresRebalanceWhenVenueShort(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[binance.VenueName] = 500
	in.AvailableUSDTByVenue[bybit.VenueName] = 3

	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if len(opp.Legs) < 2 {
			continue
		}
		if opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("expected no binance spot opportunity when perp venue cannot support 5U floor, got %+v", opp)
		}
	}
}

func TestCrossVenueSpotPerpRequiresRebalanceHelper(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	available := map[string]float64{binance.VenueName: 10, bybit.VenueName: 100}
	if !crossVenueSpotPerpRequiresRebalance(binance.VenueName, bybit.VenueName, 50, 10, available, cfg) {
		t.Fatal("expected requires_rebalance when spot cash exceeds available")
	}
	if crossVenueSpotPerpRequiresRebalance(binance.VenueName, bybit.VenueName, 5, 4, available, cfg) {
		t.Fatal("unexpected requires_rebalance when capital covers legs")
	}
}

func TestCrossVenueSpotPerpEffectiveFundingRateNoHistoryFallback(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0, 0.5)
	want := 0.0005
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("no-history effective rate = %.8f, want %.8f", got, want)
	}
}

func TestScanCrossVenueSpotPerpExcludesNegativeFunding(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = CrossVenueSpotPerpFundingSnap{
		Rate:            -0.0002,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.0001,
	}
	in.FundingByVenue["okx"]["BTC-USDT"] = CrossVenueSpotPerpFundingSnap{
		Rate:            -0.0001,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.00008,
	}
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) != 0 {
		t.Fatalf("opportunities = %d, want 0 for negative funding", len(opps))
	}
}

func TestScanCrossVenueSpotPerpExcludesMidDeviationTooLarge(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       105000,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("unexpected opportunity with large mid deviation: %+v", opp)
		}
	}
}

func TestScanCrossVenueSpotPerpExcludesInsufficientDepth(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if opp.Legs[0].Venue == binance.VenueName {
			t.Fatalf("unexpected opportunity with insufficient depth: notional=%.2f", opp.Legs[0].Notional)
		}
	}
}

func TestScanCrossVenueSpotPerpExcludesMarginStressLiquidation(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.Config.Exec.MaxEffectiveLeverage = 20
	in.Config.PerpWalletBufferRatio = 0.01
	in.Config.PerpMaintenanceMarginRate = 0.05
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) != 0 {
		t.Fatalf("opportunities = %d, want 0 when +20%% shock liquidates", len(opps))
	}
}

func TestCrossVenueSpotPerpEffectiveFundingRateSpikeDecay(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0.0004, 0.5)
	want := 0.0004 + 0.5*(0.001-0.0004)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("effective rate = %.8f, want %.8f", got, want)
	}
}

func TestCrossVenueSpotPerpFundingSettlementCount(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	next := now.Add(2 * time.Hour)
	count := crossVenueSpotPerpFundingSettlementCount(now, 6, 8, next)
	if count != 1 {
		t.Fatalf("settlements = %d, want 1", count)
	}
	count = crossVenueSpotPerpFundingSettlementCount(now, 14, 8, next)
	if count != 2 {
		t.Fatalf("settlements = %d, want 2", count)
	}
}

func TestCrossVenueSpotPerpPerpWalletLiquidated(t *testing.T) {
	cfg := DefaultCrossVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	if crossVenueSpotPerpPerpWalletLiquidated(cfg, 50, 0.20) {
		t.Fatal("expected no liquidation at +20% with default conservative params")
	}
	cfg.PerpWalletBufferRatio = 0
	cfg.Exec.MaxEffectiveLeverage = 20
	if !crossVenueSpotPerpPerpWalletLiquidated(cfg, 50, 0.20) {
		t.Fatal("expected liquidation at +20% with tiny buffer/high leverage")
	}
}

func TestAppendCrossVenueSpotPerpCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 5
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected at least one opportunity")
	}

	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	AppendCrossVenueSpotPerpCandidates(dir, "cvsp-cycle-test", execCfg, opps)

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
	if rec.StrategyType != UnifiedStrategyCrossVenueSpotPerp {
		t.Fatalf("strategy_type = %q", rec.StrategyType)
	}
	if rec.CycleID != "cvsp-cycle-test" {
		t.Fatalf("cycle_id = %q", rec.CycleID)
	}
	if rec.WouldExecute {
		t.Fatal("plan candidates must have would_execute=false")
	}
}

func TestScanCrossVenueSpotPerpExampleOutputSample(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[binance.VenueName] = 500
	in.AvailableUSDTByVenue[bybit.VenueName] = 20
	opps := ScanCrossVenueSpotPerpOpportunities(in)

	var sample UnifiedOpportunity
	for _, opp := range opps {
		if opp.Legs[0].Venue == binance.VenueName && !opp.RequiresRebalance {
			sample = opp
			break
		}
	}
	if sample.ID == "" {
		t.Fatal("missing capital-sized sample opportunity")
	}

	t.Logf("sample opportunity id=%s symbol=%s requires_rebalance=%v net_ev=%.4f funding=%.4f basis_rollback=%.4f capital=%+v legs=%+v reject=%v",
		sample.ID, sample.Symbol, sample.RequiresRebalance, sample.ExpectedNetEVUSDT,
		sample.ExpectedFundingUSDT, sample.ExpectedRollbackCostUSDT,
		sample.CapitalRequiredByVenue, sample.Legs, sample.RejectReasons)

	if sample.ID != "cross_venue_spot_perp:BTC-USDT:binance+bybit:cvsp-snap-test" {
		t.Fatalf("id = %q", sample.ID)
	}
	perpMargin := crossVenueSpotPerpPerpMarginRequired(in.Config, sample.Legs[1].Notional)
	if perpMargin > in.AvailableUSDTByVenue[bybit.VenueName]+1e-9 {
		t.Fatalf("perp margin %.4f should fit %.2fU available", perpMargin, in.AvailableUSDTByVenue[bybit.VenueName])
	}
}

func TestScanCrossVenueSpotPerpDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	beforeFunding := in.FundingByVenue[bybit.VenueName]["BTC-USDT"]
	beforeMarket := in.MarketsByVenue[binance.VenueName]["BTC-USDT"]
	_ = ScanCrossVenueSpotPerpOpportunities(in)
	if in.FundingByVenue[bybit.VenueName]["BTC-USDT"] != beforeFunding {
		t.Fatal("scanner mutated funding input")
	}
	if in.MarketsByVenue[binance.VenueName]["BTC-USDT"] != beforeMarket {
		t.Fatal("scanner mutated market input")
	}
}

func TestScanCrossVenueSpotPerpBasisAndNetEV(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	in.MarketsByVenue[binance.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(CrossVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100200,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected opportunities")
	}
	if opps[0].ExpectedFundingUSDT <= 0 {
		t.Fatalf("expected_funding_usdt = %.4f, want >0", opps[0].ExpectedFundingUSDT)
	}
	entryBasis := crossVenueSpotPerpEntryBasisUSDT(100000, 100200, opps[0].Legs[0].Notional)
	if entryBasis <= 0 {
		t.Fatalf("entry basis = %.4f, want >0 for perp premium", entryBasis)
	}
}

func TestScanCrossVenueSpotPerpAppendMultipleLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testCrossVenueSpotPerpScanInput(now)
	opps := ScanCrossVenueSpotPerpOpportunities(in)
	AppendCrossVenueSpotPerpCandidates(dir, "cycle", testCrossVenueExecutorConfig().CrossVenueExecutor, opps)

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
