package app

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
)

func testUnifiedShadowPipelinePassInput(now time.Time, recordDir string) unifiedShadowPipelinePassInput {
	exec := testCrossVenueExecutorConfig().CrossVenueExecutor
	nextFunding := now.Add(2 * time.Hour)
	return unifiedShadowPipelinePassInput{
		CycleID:    crossVenueDecisionCycleID(now),
		SnapshotID: "unified-shadow-snap-test",
		Now:        now,
		ExecCfg:    exec,
		RecordDir:  recordDir,
		FundingByVenue: map[string]map[string]CrossVenueSpotPerpFundingSnap{
			bybit.VenueName: {
				"BTC-USDT": {
					Rate:            0.0003,
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
		MarketsByVenue: map[string]map[string]CrossVenueSpotPerpMarketSnap{
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
		},
		AvailableUSDTByVenue: map[string]float64{
			binance.VenueName: 200,
			bybit.VenueName:   200,
			"okx":             200,
		},
		PerpPerpOpportunities: []CrossVenueOpportunity{
			{
				Symbol:               "ETH-USDT",
				HighVenue:            bybit.VenueName,
				LowVenue:             "okx",
				SnapshotID:           "unified-shadow-snap-test",
				NetEV:                5.0,
				SpreadEV:             6.0,
				OpenCost:             0.5,
				CloseCost:            0.5,
				ExpectedHoldHours:    8,
				ExecutionScore:       0.8,
				LiquidityScore:       0.8,
				HistoryQualityScore:  0.8,
				StabilityScore:       0.8,
				RecoveryQualityScore: 0.8,
				TradableNotional:     10,
			},
		},
	}
}

func TestRunUnifiedShadowPipelinePassWritesCandidatesAndAllocates(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	recordDir := t.TempDir()
	in := testUnifiedShadowPipelinePassInput(now, recordDir)

	plan := runUnifiedShadowPipelinePass(in)

	candidatesPath := filepath.Join(opportunitiesRecordDir(recordDir), unifiedCandidatesLogFileName)
	lines, err := readJSONLLines(candidatesPath)
	if err != nil {
		t.Fatalf("read candidates: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("unified_candidates.jsonl is empty")
	}

	strategyCounts := make(map[string]int)
	for _, line := range lines {
		var rec UnifiedCandidateLogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode candidate: %v", err)
		}
		if rec.CycleID != in.CycleID {
			t.Fatalf("cycle_id = %q, want %q", rec.CycleID, in.CycleID)
		}
		strategyCounts[rec.StrategyType]++
	}
	if strategyCounts[UnifiedStrategySingleVenueSpotPerp] == 0 {
		t.Fatalf("missing single_venue_spot_perp candidates: %v", strategyCounts)
	}
	if strategyCounts[UnifiedStrategyCrossVenueSpotPerp] == 0 {
		t.Fatalf("missing cross_venue_spot_perp candidates: %v", strategyCounts)
	}

	if plan.CycleID != in.CycleID {
		t.Fatalf("plan cycle_id = %q, want %q", plan.CycleID, in.CycleID)
	}
	if plan.Summary.TotalOpportunities == 0 {
		t.Fatalf("summary total_opportunities = 0")
	}
	if len(plan.RealPlans) == 0 && len(plan.ShadowRebalanceSuggestions) == 0 {
		t.Fatalf("allocator plan has no real plans or shadow suggestions (skipped=%d)", len(plan.Skipped))
	}

	allocatorPath := unifiedAllocatorPlanLogPath(recordDir)
	allocLines, err := readJSONLLines(allocatorPath)
	if err != nil {
		t.Fatalf("read allocator plan: %v", err)
	}
	if len(allocLines) == 0 {
		t.Fatal("allocator_plan.jsonl is empty")
	}
}

func TestRunUnifiedShadowPipelineOnceUsesInjectedFetchers(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	recordDir := t.TempDir()
	marketsCalled := false
	availCalled := false

	e := &CrossVenueExecutor{
		cfg:       testCrossVenueExecutorConfig(),
		recordDir: recordDir,
		nowFn:     func() time.Time { return now },
	}
	snap := CrossVenueScanSnapshot{
		SnapshotID: "inject-snap-1",
		ScannedAt:  now,
		FundingByVenue: map[string]map[string]CrossVenueFundingEntry{
			bybit.VenueName: {
				"BTC-USDT": {
					Rate:            0.0003,
					IntervalHours:   8,
					NextFundingTime: now.Add(2 * time.Hour),
				},
			},
		},
	}

	deps := unifiedShadowPipelineFetchDeps{
		fetchMarkets: func(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
			marketsCalled = true
			return testUnifiedShadowPipelinePassInput(now, recordDir).MarketsByVenue
		},
		fetchAvailableUSDT: func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
			availCalled = true
			return map[string]float64{bybit.VenueName: 200, binance.VenueName: 200, "okx": 200}
		},
	}

	e.runUnifiedShadowPipelineOnce(context.Background(), snap, deps)
	if !marketsCalled {
		t.Fatal("fetchMarkets was not called")
	}
	if !availCalled {
		t.Fatal("fetchAvailableUSDT was not called")
	}

	candidatesPath := filepath.Join(opportunitiesRecordDir(recordDir), unifiedCandidatesLogFileName)
	lines, err := readJSONLLines(candidatesPath)
	if err != nil {
		t.Fatalf("read candidates: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("expected candidates after injected fetch pass")
	}
}

func readJSONLLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
