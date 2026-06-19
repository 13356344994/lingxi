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

func testSingleVenueSpotPerpScanInput(now time.Time) SingleVenueSpotPerpScanInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	cfg := DefaultSingleVenueSpotPerpScannerConfig(exec)
	nextFunding := now.Add(2 * time.Hour)

	return SingleVenueSpotPerpScanInput{
		SnapshotID: "svsp-snap-test",
		Now:        now,
		Symbols:    []string{"BTC-USDT", "ETH-USDT"},
		Venues:     []string{bybit.VenueName},
		FundingByVenue: map[string]map[string]SingleVenueSpotPerpFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": {
					Rate:            0.0003,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
					SpikeThreshold:  0.00025,
				},
				"ETH-USDT": {
					Rate:            0.0002,
					IntervalHours:   8,
					NextFundingTime: nextFunding,
					SpikeThreshold:  0.00015,
				},
			},
		},
		MarketsByVenue: scannerTestInstrumentMetaMarkets(map[string]map[string]SingleVenueSpotPerpMarketSnap{
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
		Config: cfg,
	}
}

func TestScanSingleVenueSpotPerpOpportunitiesHappyPath(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	opps := ScanSingleVenueSpotPerpOpportunities(in)

	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2", len(opps))
	}
	for _, opp := range opps {
		if opp.StrategyType != UnifiedStrategySingleVenueSpotPerp {
			t.Fatalf("strategy_type = %q", opp.StrategyType)
		}
		if len(opp.Legs) != 2 {
			t.Fatalf("legs = %d", len(opp.Legs))
		}
		if opp.Legs[0].Venue != bybit.VenueName || opp.Legs[1].Venue != bybit.VenueName {
			t.Fatalf("both legs must be same venue, got %+v", opp.Legs)
		}
		if opp.Legs[0].Market != "spot" || opp.Legs[0].Side != "buy" {
			t.Fatalf("spot leg = %+v", opp.Legs[0])
		}
		if opp.Legs[1].Market != "perp" || opp.Legs[1].Side != "short" {
			t.Fatalf("perp leg = %+v", opp.Legs[1])
		}
		if opp.ExpectedFundingUSDT <= 0 {
			t.Fatalf("expected_funding_usdt = %.6f, want >0", opp.ExpectedFundingUSDT)
		}
		if opp.ExpectedBorrowCostUSDT != 0 {
			t.Fatalf("v1 must not borrow, got %.6f", opp.ExpectedBorrowCostUSDT)
		}
		perpMargin := crossVenueSpotPerpPerpMarginRequired(singleVenueSpotPerpAsCrossVenueConfig(in.Config), opp.Legs[1].Notional)
		wantCapital := opp.Legs[0].Notional + perpMargin
		if opp.CapitalRequiredByVenue[bybit.VenueName] != wantCapital {
			t.Fatalf("capital = %.4f, want spot(%.2f)+perp_margin(%.4f)=%.4f",
				opp.CapitalRequiredByVenue[bybit.VenueName], opp.Legs[0].Notional, perpMargin, wantCapital)
		}
		if opp.RequiresRebalance {
			t.Fatalf("unexpected requires_rebalance with sufficient funds")
		}
	}
}

func TestScanSingleVenueSpotPerpRequiresRebalanceWhenFundsShort(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 3

	opps := ScanSingleVenueSpotPerpOpportunities(in)
	for _, opp := range opps {
		if len(opp.Legs) > 0 {
			t.Fatalf("expected no tradable opportunities when capital cannot support 5U floor, got %+v", opp)
		}
	}
}

func TestSingleVenueSpotPerpRequiresRebalanceHelper(t *testing.T) {
	cfg := DefaultSingleVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	available := map[string]float64{bybit.VenueName: 20}
	if !singleVenueSpotPerpRequiresRebalance(bybit.VenueName, 30, 10, available, cfg) {
		t.Fatal("expected requires_rebalance when total required exceeds available")
	}
	if singleVenueSpotPerpRequiresRebalance(bybit.VenueName, 5, 4, available, cfg) {
		t.Fatal("unexpected requires_rebalance when capital covers legs")
	}
}

func TestSingleVenueSpotPerpEffectiveFundingRateNoHistoryFallback(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0, 0.5)
	want := 0.0005
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("no-history effective rate = %.8f, want %.8f", got, want)
	}
}

func TestScanSingleVenueSpotPerpRecordsNegativeFundingReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.FundingByVenue[bybit.VenueName]["BTC-USDT"] = SingleVenueSpotPerpFundingSnap{
		Rate:            -0.0002,
		IntervalHours:   8,
		NextFundingTime: now.Add(2 * time.Hour),
		SpikeThreshold:  0.0001,
	}
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	btc := findSingleVenueSpotPerpOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for negative funding")
	}
	if !containsReject(btc.RejectReasons, singleVenueSpotPerpRejectNegativeFunding) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenueSpotPerpRejectNegativeFunding)
	}
	if len(btc.Legs) != 0 {
		t.Fatal("negative funding should not produce legs")
	}
}

func TestScanSingleVenueSpotPerpRecordsNoSpotMarketReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(SingleVenueSpotPerpMarketSnap{
		SpotMid:       0,
		PerpMid:       100050,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       false,
		HasPerp:       true,
	})
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	btc := findSingleVenueSpotPerpOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for missing spot market")
	}
	if !containsReject(btc.RejectReasons, singleVenueSpotPerpRejectNoSpotMarket) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenueSpotPerpRejectNoSpotMarket)
	}
}

func TestScanSingleVenueSpotPerpExcludesMidDeviationTooLarge(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(SingleVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       105000,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	if findSingleVenueSpotPerpOpp(opps, "BTC-USDT") != nil {
		t.Fatal("expected no opportunity for large mid deviation")
	}
}

func TestScanSingleVenueSpotPerpRecordsInsufficientDepthReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(SingleVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100050,
		SpotDepthUSDT: 10,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	btc := findSingleVenueSpotPerpOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("expected rejected row for insufficient depth")
	}
	if !containsReject(btc.RejectReasons, singleVenueSpotPerpRejectInsufficientDepth) {
		t.Fatalf("reject_reasons = %#v, want %q", btc.RejectReasons, singleVenueSpotPerpRejectInsufficientDepth)
	}
	if len(btc.Legs) != 0 {
		t.Fatal("insufficient depth should not produce legs")
	}
}

func TestScanSingleVenueSpotPerpRecordsMarginStressReject(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.Config.Exec.MaxEffectiveLeverage = 20
	in.Config.PerpWalletBufferRatio = 0.01
	in.Config.PerpMaintenanceMarginRate = 0.05
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	if len(opps) != 2 {
		t.Fatalf("opportunities = %d, want 2 rejected rows", len(opps))
	}
	for _, opp := range opps {
		if !containsReject(opp.RejectReasons, singleVenueSpotPerpRejectMarginStress) {
			t.Fatalf("reject_reasons = %#v, want %q", opp.RejectReasons, singleVenueSpotPerpRejectMarginStress)
		}
	}
}

func TestSingleVenueSpotPerpUsesDiscreteSettlementCount(t *testing.T) {
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

func TestSingleVenueSpotPerpEffectiveFundingRateSpikeDecay(t *testing.T) {
	got := crossVenueSpotPerpEffectiveFundingRate(0.001, 0.0004, 0.5)
	want := 0.0004 + 0.5*(0.001-0.0004)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("effective rate = %.8f, want %.8f", got, want)
	}
}

func TestSingleVenueSpotPerpPerpWalletLiquidated(t *testing.T) {
	cfg := DefaultSingleVenueSpotPerpScannerConfig(testCrossVenueExecutorConfig().CrossVenueExecutor)
	cvCfg := singleVenueSpotPerpAsCrossVenueConfig(cfg)
	if crossVenueSpotPerpPerpWalletLiquidated(cvCfg, 50, 0.20) {
		t.Fatal("expected no liquidation at +20% with default conservative params")
	}
	cfg.PerpWalletBufferRatio = 0
	cfg.Exec.MaxEffectiveLeverage = 20
	cvCfg = singleVenueSpotPerpAsCrossVenueConfig(cfg)
	if !crossVenueSpotPerpPerpWalletLiquidated(cvCfg, 50, 0.20) {
		t.Fatal("expected liquidation at +20% with tiny buffer/high leverage")
	}
}

func TestScanSingleVenueSpotPerpBasisAndNetEV(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] = scannerTestInstrumentMeta(SingleVenueSpotPerpMarketSnap{
		SpotMid:       100000,
		PerpMid:       100200,
		SpotDepthUSDT: 5000,
		PerpDepthUSDT: 5000,
		HasSpot:       true,
		HasPerp:       true,
	})
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	btc := findSingleVenueSpotPerpOpp(opps, "BTC-USDT")
	if btc == nil {
		t.Fatal("missing BTC opportunity")
	}
	entryBasis := crossVenueSpotPerpEntryBasisUSDT(100000, 100200, btc.Legs[0].Notional)
	if entryBasis <= 0 {
		t.Fatalf("entry basis = %.4f, want >0 for perp premium", entryBasis)
	}
	cvCfg := singleVenueSpotPerpAsCrossVenueConfig(in.Config)
	expectedBasis := crossVenueSpotPerpExpectedBasisPnL(entryBasis, btc.Legs[0].Notional, cvCfg)
	reconstructed := btc.ExpectedFundingUSDT + expectedBasis - btc.ExpectedOpenCostUSDT - btc.ExpectedCloseCostUSDT - btc.ExpectedSlippageUSDT - btc.ExpectedRollbackCostUSDT
	if math.Abs(btc.ExpectedNetEVUSDT-reconstructed) > 1e-6 {
		t.Fatalf("net_ev = %.6f, reconstructed = %.6f", btc.ExpectedNetEVUSDT, reconstructed)
	}
}

func TestAppendSingleVenueSpotPerpCandidatesWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 50
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	if len(opps) == 0 {
		t.Fatal("expected at least one opportunity")
	}

	execCfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	AppendSingleVenueSpotPerpCandidates(dir, "svsp-cycle-test", execCfg, opps)

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
	if rec.StrategyType != UnifiedStrategySingleVenueSpotPerp {
		t.Fatalf("strategy_type = %q", rec.StrategyType)
	}
	if rec.CycleID != "svsp-cycle-test" {
		t.Fatalf("cycle_id = %q", rec.CycleID)
	}
	if rec.WouldExecute {
		t.Fatal("plan candidates must have would_execute=false")
	}
}

func TestScanSingleVenueSpotPerpExampleOutputSample(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	in.AvailableUSDTByVenue[bybit.VenueName] = 20
	opps := ScanSingleVenueSpotPerpOpportunities(in)

	var sample UnifiedOpportunity
	for _, opp := range opps {
		if opp.Symbol == "BTC-USDT" && !opp.RequiresRebalance {
			sample = opp
			break
		}
	}
	if sample.ID == "" {
		t.Fatal("missing capital-sized sample opportunity")
	}

	t.Logf("sample opportunity id=%s symbol=%s requires_rebalance=%v net_ev=%.4f funding=%.4f rollback=%.4f capital=%+v legs=%+v reject=%v",
		sample.ID, sample.Symbol, sample.RequiresRebalance, sample.ExpectedNetEVUSDT,
		sample.ExpectedFundingUSDT, sample.ExpectedRollbackCostUSDT,
		sample.CapitalRequiredByVenue, sample.Legs, sample.RejectReasons)

	wantID := "single_venue_spot_perp:BTC-USDT:bybit+bybit:svsp-snap-test"
	if sample.ID != wantID {
		t.Fatalf("id = %q, want %q", sample.ID, wantID)
	}
}

func TestScanSingleVenueSpotPerpDoesNotMutateInput(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	beforeFunding := in.FundingByVenue[bybit.VenueName]["BTC-USDT"]
	beforeMarket := in.MarketsByVenue[bybit.VenueName]["BTC-USDT"]
	_ = ScanSingleVenueSpotPerpOpportunities(in)
	if in.FundingByVenue[bybit.VenueName]["BTC-USDT"] != beforeFunding {
		t.Fatal("scanner mutated funding input")
	}
	if in.MarketsByVenue[bybit.VenueName]["BTC-USDT"] != beforeMarket {
		t.Fatal("scanner mutated market input")
	}
}

func TestScanSingleVenueSpotPerpAppendMultipleLines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	in := testSingleVenueSpotPerpScanInput(now)
	opps := ScanSingleVenueSpotPerpOpportunities(in)
	AppendSingleVenueSpotPerpCandidates(dir, "cycle", testCrossVenueExecutorConfig().CrossVenueExecutor, opps)

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

func findSingleVenueSpotPerpOpp(opps []UnifiedOpportunity, symbol string) *UnifiedOpportunity {
	for i := range opps {
		if opps[i].Symbol == symbol {
			return &opps[i]
		}
	}
	return nil
}

func containsReject(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
