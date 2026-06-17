package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func testUnifiedAllocatorOpportunity(id, strategy, symbol string, netEV, holdHours, capital, riskScore, exec, liq, data float64, venues map[string]float64) UnifiedOpportunity {
	return UnifiedOpportunity{
		ID:                     id,
		StrategyType:           strategy,
		Symbol:                 symbol,
		ExpectedNetEVUSDT:      netEV,
		ExpectedHoldHours:      holdHours,
		CapitalRequiredByVenue: venues,
		RiskScore:              riskScore,
		ExecutionScore:         exec,
		LiquidityScore:         liq,
		DataQualityScore:       data,
	}
}

func TestUnifiedOpportunityPrimarySortKeyOrdersDescending(t *testing.T) {
	// opp-high: riskAdj=0.8, cap=10, hours=2 => key=0.04
	// opp-mid:  riskAdj=0.3, cap=10, hours=1 => key=0.03
	// opp-low:  riskAdj=0.1, cap=20, hours=4 => key=0.00125
	opps := []UnifiedOpportunity{
		testUnifiedAllocatorOpportunity("low", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 0.5, 4, 20, 0.02, 0.8, 0.8, 0.8, map[string]float64{bybit.VenueName: 20}),
		testUnifiedAllocatorOpportunity("high", UnifiedStrategySingleVenueSpotPerp, "BTC-USDT", 1.0, 2, 10, 0.02, 0.8, 0.8, 0.8, map[string]float64{bybit.VenueName: 10}),
		testUnifiedAllocatorOpportunity("mid", UnifiedStrategySingleVenueSpotPerp, "SOL-USDT", 0.5, 1, 10, 0.02, 0.8, 0.8, 0.8, map[string]float64{bybit.VenueName: 10}),
	}

	dir := t.TempDir()
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID: "test-sort",
		Opportunities: []UnifiedOpportunity{
			opps[0],
			opps[2],
			opps[1],
		},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 1000},
		TotalCapitalUSDT:     1000,
		Config: UnifiedAllocatorConfig{
			MaxGlobalCapitalUsagePct:   1.0,
			MaxStrategyCapitalUsagePct: 1.0,
			MaxSymbolCapitalUsagePct:   1.0,
			MaxConcurrentPlans:         3,
			MaxSingleTradeExpectedLossUSDT: 100,
		},
		RecordDir: dir,
	})

	if len(plan.RealPlans) != 3 {
		t.Fatalf("real plans = %d, want 3", len(plan.RealPlans))
	}
	if plan.RealPlans[0].OpportunityID != "high" {
		t.Fatalf("rank1 = %q, want high", plan.RealPlans[0].OpportunityID)
	}
	if plan.RealPlans[1].OpportunityID != "mid" {
		t.Fatalf("rank2 = %q, want mid", plan.RealPlans[1].OpportunityID)
	}
	if plan.RealPlans[2].OpportunityID != "low" {
		t.Fatalf("rank3 = %q, want low", plan.RealPlans[2].OpportunityID)
	}
	if plan.RealPlans[0].PrimarySortKey <= plan.RealPlans[1].PrimarySortKey {
		t.Fatalf("sort keys not descending: %.6f <= %.6f", plan.RealPlans[0].PrimarySortKey, plan.RealPlans[1].PrimarySortKey)
	}
}

func TestUnifiedShadowAllocatorSkipsInsufficientVenueBalance(t *testing.T) {
	opp := testUnifiedAllocatorOpportunity(
		"spot-perp-1",
		UnifiedStrategySingleVenueSpotPerp,
		"BTC-USDT",
		1.0, 4, 80, 0.005, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 80},
	)
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              "test-balance",
		Opportunities:        []UnifiedOpportunity{opp},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 50},
		TotalCapitalUSDT:     100,
		Config:               DefaultUnifiedAllocatorConfig(),
	})

	if len(plan.RealPlans) != 0 {
		t.Fatalf("real plans = %d, want 0", len(plan.RealPlans))
	}
	if len(plan.ShadowRebalanceSuggestions) != 1 {
		t.Fatalf("shadow suggestions = %d, want 1", len(plan.ShadowRebalanceSuggestions))
	}
	sh := plan.ShadowRebalanceSuggestions[0]
	if sh.ShadowKind != unifiedAllocatorShadowKindRebalance {
		t.Fatalf("shadow kind = %q, want %q", sh.ShadowKind, unifiedAllocatorShadowKindRebalance)
	}
	if sh.DeficitByVenue[bybit.VenueName] != 30 {
		t.Fatalf("deficit = %.2f, want 30", sh.DeficitByVenue[bybit.VenueName])
	}
}

func TestUnifiedShadowAllocatorPerpPerpGoesToShadowNotRealPlan(t *testing.T) {
	opp := testUnifiedAllocatorOpportunity(
		"pp-1",
		UnifiedStrategyPerpPerp,
		"BTC-USDT",
		2.0, 4, 20, 0.05, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 10, okx.VenueName: 10},
	)
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              "test-perp-shadow",
		Opportunities:        []UnifiedOpportunity{opp},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 100, okx.VenueName: 100},
		TotalCapitalUSDT:     200,
		Config:               DefaultUnifiedAllocatorConfig(),
	})

	if len(plan.RealPlans) != 0 {
		t.Fatalf("real plans = %d, want 0", len(plan.RealPlans))
	}
	if len(plan.ShadowRebalanceSuggestions) != 1 {
		t.Fatalf("shadow suggestions = %d, want 1", len(plan.ShadowRebalanceSuggestions))
	}
	if plan.ShadowRebalanceSuggestions[0].ShadowKind != unifiedAllocatorShadowKindStrategy {
		t.Fatalf("shadow kind = %q", plan.ShadowRebalanceSuggestions[0].ShadowKind)
	}
}

func TestUnifiedShadowAllocatorCrossVenueRebalanceShadowWhenOneVenueShort(t *testing.T) {
	opp := testUnifiedAllocatorOpportunity(
		"cross-spot-perp",
		UnifiedStrategyCrossVenueSpotPerp,
		"BTC-USDT",
		1.5, 4, 40, 0.005, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 20, okx.VenueName: 20},
	)
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              "test-cross-rebalance",
		Opportunities:        []UnifiedOpportunity{opp},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 50, okx.VenueName: 5},
		TotalCapitalUSDT:     100,
		Config:               DefaultUnifiedAllocatorConfig(),
	})

	if len(plan.RealPlans) != 0 {
		t.Fatalf("real plans = %d, want 0", len(plan.RealPlans))
	}
	if len(plan.ShadowRebalanceSuggestions) != 1 {
		t.Fatalf("shadow = %d, want 1", len(plan.ShadowRebalanceSuggestions))
	}
	if plan.ShadowRebalanceSuggestions[0].DeficitByVenue[okx.VenueName] != 15 {
		t.Fatalf("okx deficit = %.2f, want 15", plan.ShadowRebalanceSuggestions[0].DeficitByVenue[okx.VenueName])
	}
}

func TestUnifiedShadowAllocatorRiskLimitsEnforced(t *testing.T) {
	cfg := DefaultUnifiedAllocatorConfig()
	cfg.MaxGlobalCapitalUsagePct = 0.20
	cfg.MaxStrategyCapitalUsagePct = 0.20
	cfg.MaxConcurrentPlans = 5

	makeOpp := func(id, symbol string, cap float64) UnifiedOpportunity {
		return testUnifiedAllocatorOpportunity(
			id,
			UnifiedStrategySingleVenueSpotPerp,
			symbol,
			1.0, 2, cap, 0.02, 0.8, 0.8, 0.8,
			map[string]float64{bybit.VenueName: cap},
		)
	}
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID: "test-risk",
		Opportunities: []UnifiedOpportunity{
			makeOpp("a", "BTC-USDT", 15),
			makeOpp("b", "ETH-USDT", 10),
			makeOpp("c", "SOL-USDT", 10),
		},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 200},
		TotalCapitalUSDT:     100,
		Config:               cfg,
	})

	if len(plan.RealPlans) != 2 {
		t.Fatalf("real plans = %d, want 2 (global 20%% = 20 USDT)", len(plan.RealPlans))
	}
	if plan.Summary.AllocatedCapitalUSDT != 20 {
		t.Fatalf("allocated = %.2f, want 20", plan.Summary.AllocatedCapitalUSDT)
	}
	foundGlobalSkip := false
	for _, sk := range plan.Skipped {
		for _, r := range sk.Reasons {
			if r == "global_capital_usage_limit" {
				foundGlobalSkip = true
			}
		}
	}
	if !foundGlobalSkip {
		t.Fatalf("skipped = %#v, want global_capital_usage_limit", plan.Skipped)
	}
}

func TestUnifiedShadowAllocatorHardGateRejectsNonPositiveNetEV(t *testing.T) {
	opp := testUnifiedAllocatorOpportunity(
		"bad-ev",
		UnifiedStrategySingleVenueSpotPerp,
		"BTC-USDT",
		-0.1, 4, 10, 0.05, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 10},
	)
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              "test-hard-gate",
		Opportunities:        []UnifiedOpportunity{opp},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 100},
		TotalCapitalUSDT:     100,
	})

	if len(plan.Skipped) != 1 {
		t.Fatalf("skipped = %d, want 1", len(plan.Skipped))
	}
	if plan.Skipped[0].Reasons[0] != "net_ev<=0" {
		t.Fatalf("reasons = %#v", plan.Skipped[0].Reasons)
	}
}

func TestUnifiedShadowAllocatorWritesPlanJSONL(t *testing.T) {
	dir := t.TempDir()
	opp := testUnifiedAllocatorOpportunity(
		"write-1",
		UnifiedStrategySingleVenueSpotPerp,
		"BTC-USDT",
		1.0, 2, 10, 0.02, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 10},
	)
	_ = RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID:              "test-write",
		Opportunities:        []UnifiedOpportunity{opp},
		AvailableUSDTByVenue: map[string]float64{bybit.VenueName: 100},
		TotalCapitalUSDT:     100,
		RecordDir:            dir,
	})

	path := unifiedAllocatorPlanLogPath(dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan log: %v", err)
	}
	var rec UnifiedAllocatorPlan
	if err := json.Unmarshal(raw[:len(raw)-1], &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.CycleID != "test-write" || len(rec.RealPlans) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	if filepath.Base(path) != unifiedAllocatorPlanLogFileName {
		t.Fatalf("file name = %q", filepath.Base(path))
	}
}

func TestUnifiedOpportunityRiskAdjustedNetEVAndSortKey(t *testing.T) {
	opp := testUnifiedAllocatorOpportunity(
		"x",
		UnifiedStrategySingleVenueSpotPerp,
		"BTC-USDT",
		1.0, 4, 10, 0.10, 0.8, 0.8, 0.8,
		map[string]float64{bybit.VenueName: 10},
	)
	total := unifiedOpportunityTotalCapital(opp)
	riskAdj := unifiedOpportunityRiskAdjustedNetEV(opp, total)
	if riskAdj != 0.0 {
		t.Fatalf("riskAdj = %.4f, want 0 (1.0 - 10*0.10)", riskAdj)
	}
	key := unifiedOpportunityPrimarySortKey(riskAdj, total, opp.ExpectedHoldHours)
	if key != 0 {
		t.Fatalf("sort key = %.6f, want 0 for non-positive riskAdj", key)
	}
}

func TestUnifiedShadowAllocatorDemoOutputShape(t *testing.T) {
	plan := RunUnifiedShadowAllocator(UnifiedAllocatorInput{
		CycleID: "demo-cycle",
		Opportunities: []UnifiedOpportunity{
			testUnifiedAllocatorOpportunity(
				"sv-1",
				UnifiedStrategySingleVenueSpotPerp,
				"BTC-USDT",
				0.50, 4, 20, 0.005, 0.85, 0.70, 0.80,
				map[string]float64{bybit.VenueName: 20},
			),
			testUnifiedAllocatorOpportunity(
				"pp-1",
				UnifiedStrategyPerpPerp,
				"ETH-USDT",
				1.20, 4, 15, 0.005, 0.85, 0.70, 0.80,
				map[string]float64{bybit.VenueName: 7.5, okx.VenueName: 7.5},
			),
		},
		AvailableUSDTByVenue: map[string]float64{
			bybit.VenueName: 100,
			okx.VenueName:   10,
		},
		TotalCapitalUSDT: 200,
		Config:           DefaultUnifiedAllocatorConfig(),
		RecordDir:        "",
	})

	_ = plan
	if len(plan.RealPlans) != 1 {
		t.Fatalf("real plans = %d, want 1", len(plan.RealPlans))
	}
	if len(plan.ShadowRebalanceSuggestions) != 1 {
		t.Fatalf("shadow = %d, want 1", len(plan.ShadowRebalanceSuggestions))
	}
}
