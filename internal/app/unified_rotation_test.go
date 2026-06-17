package app

import (
	"math"
	"testing"
)

func testRotationOpp(id, strategy, symbol string, netEV, capital float64, venues ...string) UnifiedOpportunity {
	capByVenue := map[string]float64{}
	legVenue := "binance"
	if len(venues) == 0 {
		capByVenue["binance"] = capital / 2
		capByVenue["bybit"] = capital / 2
	} else if len(venues) == 1 {
		legVenue = venues[0]
		capByVenue[venues[0]] = capital
	} else {
		half := capital / 2
		capByVenue[venues[0]] = half
		capByVenue[venues[1]] = half
		legVenue = venues[0]
	}
	return UnifiedOpportunity{
		ID:                     id,
		StrategyType:           strategy,
		Symbol:                 symbol,
		ExpectedNetEVUSDT:      netEV,
		ExpectedOpenCostUSDT:   1,
		ExpectedCloseCostUSDT:  1,
		CapitalRequiredByVenue: capByVenue,
		Legs: []UnifiedOpportunityLeg{
			{Venue: legVenue, Notional: capital},
		},
	}
}

func TestUnifiedWindowScoreSorting(t *testing.T) {
	low := testRotationOpp("low", UnifiedStrategyPerpPerp, "BTC", 5, 100)
	high := testRotationOpp("high", UnifiedStrategySingleVenueSpotPerp, "ETH", 20, 100)

	if got := unifiedWindowScore(low); math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("low score = %v, want 0.05", got)
	}
	if got := unifiedWindowScore(high); math.Abs(got-0.20) > 1e-9 {
		t.Fatalf("high score = %v, want 0.20", got)
	}

	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{low, high},
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(actions) != 2 {
		t.Fatalf("expected 2 opens, got %d", len(actions))
	}
	if actions[0].OpportunityID != "high" {
		t.Fatalf("expected highest window score first, got %s", actions[0].OpportunityID)
	}
}

func TestPlanUnifiedRotationOpensRespectsBalanceReserveAndConcurrent(t *testing.T) {
	cfg := RotationConfig{
		MaxConcurrent:      1,
		MaxPositionUSDT:    1000,
		DefaultReserveUSDT: 20,
	}
	a := testRotationOpp("a", UnifiedStrategyPerpPerp, "BTC", 10, 80, "binance", "bybit")
	b := testRotationOpp("b", UnifiedStrategyPerpPerp, "ETH", 8, 80, "binance", "bybit")

	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{a, b},
		map[string]float64{"binance": 100, "bybit": 100},
		cfg,
	)
	if len(actions) != 1 {
		t.Fatalf("expected 1 open due to reserve+concurrent, got %d", len(actions))
	}
	if actions[0].OpportunityID != "a" {
		t.Fatalf("expected best score selected, got %s", actions[0].OpportunityID)
	}

	tooLarge := testRotationOpp("big", UnifiedStrategyPerpPerp, "SOL", 50, 600, "binance")
	actions = PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{tooLarge},
		map[string]float64{"binance": 700},
		RotationConfig{MaxConcurrent: 3, MaxPositionUSDT: 500, DefaultReserveUSDT: 0},
	)
	if len(actions) != 0 {
		t.Fatalf("expected max position filter to skip, got %+v", actions)
	}
}

func TestContinuationScoreExcludesSunkOpenCost(t *testing.T) {
	pos := HeldPosition{
		OpportunityID:          "held",
		CapitalRequiredByVenue: map[string]float64{"binance": 100},
	}
	inputs := ContinuationInputs{
		ExpectedFundingUSDT:   15,
		ExpectedCloseCostUSDT: 2,
		HoldingRiskUSDT:       1,
	}
	got := continuationScore(pos, inputs)
	want := (15.0 - 1.0) / 100.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("continuation score = %v, want %v (close excluded from hold baseline)", got, want)
	}
}

func TestDecideRotationHoldWhenContinuationBeatsSwitch(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   20,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 8, 100)

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD, got %+v", plan.Actions)
	}
}

func TestDecideRotationSwitchWhenNewOpportunityWins(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   4,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 30, 100)
	newOpp.ExpectedOpenCostUSDT = 1

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("expected SWITCH, got %+v", plan.Actions)
	}
	if plan.Actions[0].ReplaceOpportunityID != "new-eth" {
		t.Fatalf("expected switch target new-eth, got %s", plan.Actions[0].ReplaceOpportunityID)
	}
}

func TestDecideRotationCloseWhenContinuationNegative(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   1,
			ExpectedCloseCostUSDT: 5,
			HoldingRiskUSDT:       2,
		},
	}

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		nil,
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionClose {
		t.Fatalf("expected CLOSE, got %+v", plan.Actions)
	}
}

func TestDecideRotationOpensRemainingCapital(t *testing.T) {
	newA := testRotationOpp("open-a", UnifiedStrategyPerpPerp, "SOL", 12, 40, "binance", "bybit")
	newB := testRotationOpp("open-b", UnifiedStrategyPerpPerp, "AVAX", 10, 40, "binance", "bybit")

	plan := DecideRotation(
		nil,
		nil,
		[]UnifiedOpportunity{newB, newA},
		map[string]float64{"binance": 100, "bybit": 100},
		RotationConfig{MaxConcurrent: 2, MaxPositionUSDT: 100, DefaultReserveUSDT: 0},
	)
	if len(plan.Actions) != 2 {
		t.Fatalf("expected 2 OPEN actions, got %+v", plan.Actions)
	}
	if plan.Actions[0].Kind != RotationActionOpen || plan.Actions[0].OpportunityID != "open-a" {
		t.Fatalf("expected best open first, got %+v", plan.Actions[0])
	}
}

func TestDecideRotationSwitchDoesNotDoubleChargeOpenCost(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   6,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 10, 100)
	newOpp.ExpectedOpenCostUSDT = 4

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("expected SWITCH (open cost already in netEV), got %+v", plan.Actions)
	}
	if plan.Actions[0].SwitchCostUSDT != 2 {
		t.Fatalf("switch cost = %.4f, want 2 (held close only, no openCost double-charge)", plan.Actions[0].SwitchCostUSDT)
	}
}

func TestDecideRotationSwitchCandidateNotReused(t *testing.T) {
	heldA := HeldPosition{
		OpportunityID:          "held-a",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldB := HeldPosition{
		OpportunityID:          "held-b",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "ETH",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	inputs := map[string]ContinuationInputs{
		"held-a": {ExpectedFundingUSDT: 2, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 1},
		"held-b": {ExpectedFundingUSDT: 2, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 1},
	}
	best := testRotationOpp("best-new", UnifiedStrategySingleVenueSpotPerp, "SOL", 40, 100)

	plan := DecideRotation(
		[]HeldPosition{heldA, heldB},
		inputs,
		[]UnifiedOpportunity{best},
		map[string]float64{"binance": 300, "bybit": 300},
		DefaultRotationConfig(),
	)

	switchCount := 0
	replaceIDs := map[string]int{}
	for _, act := range plan.Actions {
		if act.Kind == RotationActionSwitch {
			switchCount++
			replaceIDs[act.ReplaceOpportunityID]++
		}
	}
	if switchCount != 1 {
		t.Fatalf("expected exactly 1 SWITCH, got %d actions=%+v", switchCount, plan.Actions)
	}
	if replaceIDs["best-new"] != 1 {
		t.Fatalf("best-new chosen %d times, want 1", replaceIDs["best-new"])
	}
}

func TestRotationComparableWindowScoreDeductsHoldingRisk(t *testing.T) {
	opp := testRotationOpp("x", UnifiedStrategySingleVenueSpotPerp, "BTC", 20, 100)
	opp.RiskScore = 1.0
	got := rotationComparableWindowScore(opp)
	want := (20.0 - 1.0) / 100.0
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("comparable score = %v, want %v", got, want)
	}
}
