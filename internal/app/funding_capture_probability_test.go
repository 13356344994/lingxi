package app

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBayesianFundingCaptureProbabilityNoSamples(t *testing.T) {
	got := bayesianFundingCaptureProbability(nil, defaultFundingCaptureEstimateConfig())
	if math.Abs(got-carryScannerDefaultFundingCaptureProbability) > 1e-9 {
		t.Fatalf("no samples should stay at prior mean 0.5, got %.4f", got)
	}
}

func TestBayesianFundingCaptureProbabilityStrongLowCaptureClamped(t *testing.T) {
	ratios := make([]float64, 50)
	for i := range ratios {
		ratios[i] = 0.01
	}
	got := bayesianFundingCaptureProbability(ratios, defaultFundingCaptureEstimateConfig())
	if got < carryScannerMinFundingCaptureProbability {
		t.Fatalf("expected floor %.2f, got %.4f", carryScannerMinFundingCaptureProbability, got)
	}
	if got > carryScannerMinFundingCaptureProbability+1e-6 {
		t.Fatalf("expected clamp at floor %.2f, got %.4f", carryScannerMinFundingCaptureProbability, got)
	}
}

func TestCrossVenueSettlementCaptureSampleExcludesChurnedHold(t *testing.T) {
	open := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	rec := CrossVenueSettlement{
		OpenTime:         open,
		CloseTime:        open.Add(15 * time.Minute),
		EstimatedFunding: 1.0,
		RealizedFunding:  0.01,
	}
	if _, ok := crossVenueSettlementCaptureSample(rec); ok {
		t.Fatal("expected churned short-hold settlement to be excluded")
	}
}

func TestCrossVenueSettlementCaptureSampleIncludesHeldThroughFunding(t *testing.T) {
	open := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	rec := CrossVenueSettlement{
		OpenTime:         open,
		CloseTime:        open.Add(9 * time.Hour),
		EstimatedFunding: 2.0,
		RealizedFunding:  0.8,
	}
	sample, ok := crossVenueSettlementCaptureSample(rec)
	if !ok {
		t.Fatal("expected held-through settlement to qualify")
	}
	if sample.ExpectedFundingUSDT <= 0 {
		t.Fatalf("expected prorated expected funding, got %.4f", sample.ExpectedFundingUSDT)
	}
}

func TestEstimateFundingCaptureProbabilityForStrategyAggregates(t *testing.T) {
	samples := []fundingCaptureSample{
		{StrategyType: UnifiedStrategyPerpPerp, ExpectedFundingUSDT: 1, RealizedFundingUSDT: 0.2},
		{StrategyType: UnifiedStrategyPerpPerp, ExpectedFundingUSDT: 1, RealizedFundingUSDT: 0.2},
	}
	got := estimateFundingCaptureProbabilityForStrategy(samples, UnifiedStrategyPerpPerp)
	if got <= carryScannerMinFundingCaptureProbability {
		t.Fatalf("expected estimate above floor with moderate capture evidence, got %.4f", got)
	}
	if got >= 0.5 {
		t.Fatalf("expected estimate below prior with low capture evidence, got %.4f", got)
	}
}

func TestScannerFundingCaptureProbabilityFallbackWithoutRecords(t *testing.T) {
	got := scannerFundingCaptureProbability("", UnifiedStrategyCrossVenueSpotPerp, 0)
	if math.Abs(got-carryScannerDefaultFundingCaptureProbability) > 1e-9 {
		t.Fatalf("expected fallback 0.5, got %.4f", got)
	}
}

func writeCrossVenueSettlementSamples(t *testing.T, dir string, recs []CrossVenueSettlement) {
	t.Helper()
	path := filepath.Join(crossVenueRecordDir(dir), crossVenueSettlementsFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, rec := range recs {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCrossVenueOpenGateExpectedFundingUsesBackfilledCaptureProbability(t *testing.T) {
	dir := t.TempDir()
	open := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	writeCrossVenueSettlementSamples(t, dir, []CrossVenueSettlement{
		{OpenTime: open, CloseTime: open.Add(9 * time.Hour), EstimatedFunding: 2.0, RealizedFunding: 0.4},
		{OpenTime: open.Add(24 * time.Hour), CloseTime: open.Add(33 * time.Hour), EstimatedFunding: 2.0, RealizedFunding: 0.4},
	})
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	refreshCrossVenueExecutorFundingCaptureProbability(&cfg, dir)
	if cfg.FundingCaptureProbability >= 0.5 {
		t.Fatalf("expected backfill below prior 0.5 with low capture history, got %.4f", cfg.FundingCaptureProbability)
	}
	if cfg.FundingCaptureProbability < carryScannerMinFundingCaptureProbability {
		t.Fatalf("expected floor %.2f, got %.4f", carryScannerMinFundingCaptureProbability, cfg.FundingCaptureProbability)
	}

	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	opp := CrossVenueOpportunity{
		HighFundingRate: 0.004, LowFundingRate: 0.0001,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
	}
	notional := 100.0
	rawFunding := crossVenueExpectedFundingOverHold(now, 12, notional, opp)
	got := crossVenueOpenGateExpectedFundingUSDT(cfg, now, 12, notional, opp)
	wantDefault := rawFunding * carryScannerDefaultFundingCaptureProbability
	wantBackfill := rawFunding * cfg.FundingCaptureProbability
	if math.Abs(got-wantDefault) < 1e-9 {
		t.Fatalf("open-gate funding still matches hardcoded 0.5 default: got %.6f", got)
	}
	if math.Abs(got-wantBackfill) > 1e-6 {
		t.Fatalf("open-gate funding=%.6f want backfilled %.6f", got, wantBackfill)
	}
}

func TestCrossVenueOpenGateExpectedFundingCaptureFallbackWithoutRecords(t *testing.T) {
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	refreshCrossVenueExecutorFundingCaptureProbability(&cfg, t.TempDir())
	if math.Abs(cfg.FundingCaptureProbability-carryScannerDefaultFundingCaptureProbability) > 1e-9 {
		t.Fatalf("expected fallback 0.5, got %.4f", cfg.FundingCaptureProbability)
	}

	now := time.Date(2026, 6, 21, 4, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	opp := CrossVenueOpportunity{
		HighFundingRate: 0.004, LowFundingRate: 0.0001,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
	}
	notional := 100.0
	got := crossVenueOpenGateExpectedFundingUSDT(cfg, now, 12, notional, opp)
	rawFunding := crossVenueExpectedFundingOverHold(now, 12, notional, opp)
	want := rawFunding * carryScannerDefaultFundingCaptureProbability
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("fallback funding=%.6f want %.6f", got, want)
	}
}

func TestCrossVenueComputeCurrentFutureEVUsesBackfilledCaptureProbability(t *testing.T) {
	dir := t.TempDir()
	open := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	writeCrossVenueSettlementSamples(t, dir, []CrossVenueSettlement{
		{OpenTime: open, CloseTime: open.Add(9 * time.Hour), EstimatedFunding: 2.0, RealizedFunding: 0.4},
		{OpenTime: open.Add(24 * time.Hour), CloseTime: open.Add(33 * time.Hour), EstimatedFunding: 2.0, RealizedFunding: 0.4},
	})
	cfg := testCrossVenueExecutorConfig().CrossVenueExecutor
	refreshCrossVenueExecutorFundingCaptureProbability(&cfg, dir)

	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	highNext := now.Add(2 * time.Hour)
	lowNext := now.Add(45 * time.Minute)
	window := crossVenueComparableWindowHours(now, highNext, lowNext)
	pos := &CrossVenuePosition{
		HighVenue: "okx", LowVenue: "bybit",
		NotionalUSDT:    100,
		HighFundingRate: 0.004, LowFundingRate: 0.0001,
		HighIntervalHours: 4, LowIntervalHours: 1,
		HighNextFundingTime: highNext, LowNextFundingTime: lowNext,
		CurrentComparableWindowHours: window,
		ExecutionScore:               0.8, RecoveryQualityScore: 0.7,
	}
	opp := CrossVenueOpportunity{
		HighFundingRate: pos.HighFundingRate, LowFundingRate: pos.LowFundingRate,
		HighIntervalHours: pos.HighIntervalHours, LowIntervalHours: pos.LowIntervalHours,
		HighNextFundingTime: highNext, LowNextFundingTime: lowNext,
	}
	discreteFunding := crossVenueExpectedFundingOverHold(now, window, 100, opp)
	futureEV, _, _, exitCost, _, _, _ := crossVenueComputeCurrentFutureEV(cfg, pos, now)
	futureRisk := crossVenueObservableRiskCost(100, 0, window, pos.RecoveryQualityScore)
	wantEV := discreteFunding*cfg.FundingCaptureProbability - exitCost - futureRisk
	wantDefaultEV := discreteFunding*carryScannerDefaultFundingCaptureProbability - exitCost - futureRisk
	if math.Abs(futureEV-wantDefaultEV) < 1e-6 {
		t.Fatalf("future EV %.6f still matches hardcoded 0.5 capture", futureEV)
	}
	if math.Abs(futureEV-wantEV) > 1e-6 {
		t.Fatalf("future EV %.6f want backfilled %.6f", futureEV, wantEV)
	}
}
