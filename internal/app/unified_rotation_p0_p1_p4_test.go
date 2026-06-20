package app

import (
	"strings"
	"testing"
	"time"
)

func TestRotationPrimaryScoreAbsoluteEV(t *testing.T) {
	bigEV := testRotationOpp("big", UnifiedStrategyPerpPerp, "BTC", 2, 150, "binance", "bybit")
	smallEV := testRotationOpp("small", UnifiedStrategyPerpPerp, "ETH", 0.5, 30, "binance", "bybit")
	sorted := rotationSortedPositiveCandidates(
		[]UnifiedOpportunity{bigEV, smallEV},
		RotationConfig{MaxConcurrent: 2, DefaultReserveUSDT: 0},
	)
	if len(sorted) != 2 || sorted[0].ID != "big" {
		t.Fatalf("expected absolute EV sort big first, got %+v", sorted)
	}
	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{bigEV, smallEV},
		map[string]float64{"binance": 500, "bybit": 500},
		RotationConfig{MaxConcurrent: 2, MaxPositionUSDT: 500, DefaultReserveUSDT: 0},
	)
	if len(actions) != 2 || actions[0].OpportunityID != "big" {
		t.Fatalf("expected OPEN order big first, got %+v", actions)
	}
	if !strings.Contains(actions[0].Reason, "open_net_ev=2.0000") {
		t.Fatalf("OPEN reason=%q", actions[0].Reason)
	}
}

func TestRotationSwitchScoreConsistency(t *testing.T) {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID:          "held-low",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 1000, "bybit": 1000},
		OpenedAt:               now.Add(-5 * time.Hour),
	}
	// 续持绝对净收益 0.5 USDT; per-capital 续持分仅 0.00025,若误用 per-capital 会与 open 分混淆。
	heldInputs := map[string]ContinuationInputs{
		"held-low": {
			ExpectedFundingUSDT:   1.0,
			ExpectedCloseCostUSDT: 2.0,
			HoldingRiskUSDT:       0.5,
		},
	}
	highEV := testRotationOpp("high-ev", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 50, 200)
	highEV.ExpectedSlippageUSDT = 0.5
	highEV.ExpectedRollbackCostUSDT = 0.5

	plan := decideRotationAt(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{highEV},
		map[string]float64{"binance": 5000, "bybit": 5000},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("expected SWITCH with USDT-dimension compare, got %+v", plan.Actions)
	}
	contNet := continuationNetUSDT(heldInputs["held-low"])
	switchCost := rotationSwitchMarginalCostUSDT(heldInputs["held-low"].ExpectedCloseCostUSDT, highEV)
	adjOpen := rotationComparableAbsoluteScore(highEV) - switchCost
	if contNet >= adjOpen {
		t.Fatalf("precondition: contNet=%.4f should be < adjOpen=%.4f", contNet, adjOpen)
	}
	if !strings.Contains(plan.Actions[0].Reason, "continuation_net=") || !strings.Contains(plan.Actions[0].Reason, "adjusted_open_net=") {
		t.Fatalf("reason should use USDT fields: %q", plan.Actions[0].Reason)
	}
}

func TestRotationCandidatesExcludeHardReject(t *testing.T) {
	cfg := DefaultRotationConfig()
	hard := testRotationOpp("hard", UnifiedStrategySingleVenueSpotPerp, "BEL-USDT", 5, 50)
	hard.RejectReasons = []string{"no_spot_market"}
	soft := testRotationOpp("soft", UnifiedStrategySingleVenuePerpSpot, "ETH-USDT", -1, 50)
	soft.RejectReasons = []string{"net_ev<=0"}
	ok := testRotationOpp("ok", UnifiedStrategyCrossVenueSpotPerp, "SOL-USDT", 3, 50)
	candidateOnly := testRotationOpp("pp", UnifiedStrategyPerpPerp, "BTC-USDT", 4, 50)
	candidateOnly.CandidateOnly = true

	filtered := filterRotationCandidates([]UnifiedOpportunity{hard, soft, ok, candidateOnly}, cfg)
	if len(filtered) != 1 || filtered[0].ID != "ok" {
		t.Fatalf("filtered=%+v want only ok", filtered)
	}
}

func TestRotationDecisionDiagCounts(t *testing.T) {
	cfg := DefaultRotationConfig()
	cfg.DefaultReserveUSDT = 0
	available := map[string]float64{"binance": 200, "bybit": 200}

	hard := testRotationOpp("hard", UnifiedStrategySingleVenueSpotPerp, "A-USDT", 5, 50)
	hard.RejectReasons = []string{"no_spot_market"}
	soft := testRotationOpp("soft", UnifiedStrategySingleVenuePerpSpot, "B-USDT", -2, 50)
	soft.RejectReasons = []string{"net_ev<=0"}
	ok := testRotationOpp("ok", UnifiedStrategyCrossVenueSpotPerp, "C-USDT", 4, 50)
	noBalance := testRotationOpp("nb", UnifiedStrategyPerpPerp, "D-USDT", 6, 5000, "binance", "bybit")

	diag := rotationDiagnoseCandidates([]UnifiedOpportunity{hard, soft, ok, noBalance}, available, cfg)
	if diag.RawTotal != 4 {
		t.Fatalf("raw_total=%d", diag.RawTotal)
	}
	if diag.ScannerWouldExec != 2 || diag.HardRejectCount != 1 || diag.SoftRejectCount != 1 {
		t.Fatalf("scanner counts: exec=%d hard=%d soft=%d", diag.ScannerWouldExec, diag.HardRejectCount, diag.SoftRejectCount)
	}
	if diag.BlockedByHardReject != 1 || diag.BlockedByNetEV != 1 || diag.BlockedByBalance != 1 {
		t.Fatalf("blocked hard=%d net_ev=%d balance=%d", diag.BlockedByHardReject, diag.BlockedByNetEV, diag.BlockedByBalance)
	}
	if diag.RotationWouldExec != 1 {
		t.Fatalf("rotation_would_exec=%d want 1 (ok only)", diag.RotationWouldExec)
	}
	if diag.TopRejectReasons["reject:no_spot_market"] != 1 {
		t.Fatalf("top rejects=%v", diag.TopRejectReasons)
	}
	filtered := filterRotationCandidates([]UnifiedOpportunity{hard, soft, ok, noBalance}, cfg)
	if len(filtered) != 2 {
		t.Fatalf("rotationCandidates=%d want 2 (ok+noBalance)", len(filtered))
	}
	if diag.ScannerWouldExec != 2 {
		t.Fatalf("scanner_would_exec=%d want 2", diag.ScannerWouldExec)
	}
}
