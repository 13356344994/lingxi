package app

import (
	"math"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
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
		OpenEligibleNow:        true,
		CapitalRequiredByVenue: capByVenue,
		Legs: []UnifiedOpportunityLeg{
			{Venue: legVenue, Notional: capital},
		},
	}
}

func TestRotationConfigFromUnifiedInheritsExecMaxPosition(t *testing.T) {
	exec := CrossVenueExecutorConfig{MaxPositionUSDT: 150}
	cfg := RotationConfigFromUnified(core.UnifiedRotationConfig{}, exec)
	if math.Abs(cfg.MaxPositionUSDT-150) > 1e-9 {
		t.Fatalf("rotation max_position=%.2f want 150 from exec", cfg.MaxPositionUSDT)
	}
	if def := DefaultRotationConfig(); def.MaxPositionUSDT != 0 {
		t.Fatalf("DefaultRotationConfig max_position=%.2f want 0 (no independent default)", def.MaxPositionUSDT)
	}
}

func TestRotationConfigFromUnifiedInheritsExecMaxConcurrent(t *testing.T) {
	exec := CrossVenueExecutorConfig{MaxConcurrent: 8}
	cfg := RotationConfigFromUnified(core.UnifiedRotationConfig{}, exec)
	if cfg.MaxConcurrent != 8 {
		t.Fatalf("rotation max_concurrent=%d want 8 from exec", cfg.MaxConcurrent)
	}
	if def := DefaultRotationConfig(); def.MaxConcurrent != 3 {
		t.Fatalf("DefaultRotationConfig max_concurrent=%d want 3", def.MaxConcurrent)
	}
}

func TestRotationCanOpenRejectsAboveExecMaxPosition(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{MaxPositionUSDT: 150}
	rotCfg := RotationConfigFromUnified(core.UnifiedRotationConfig{}, execCfg)
	rotCfg.MaxConcurrent = 3
	rotCfg.DefaultReserveUSDT = 0

	// 构造 scanner 会放行(TradableNotional=150)但 leg 名义 160 超过 exec/rotation 闸的场景。
	opp := testRotationOpp("big", UnifiedStrategyPerpPerp, "BTC", 10, 320, "binance", "bybit")
	opp.Legs = []UnifiedOpportunityLeg{
		{Venue: "binance", Notional: 160},
		{Venue: "bybit", Notional: 160},
	}
	available := map[string]float64{"binance": 500, "bybit": 500}

	ok, reason := rotationCanOpenOpportunity(opp, available, rotCfg, 0, nil, nil)
	if ok || reason != "max_position_usdt_exceeded" {
		t.Fatalf("exec-aligned rotation should reject notional>150: ok=%v reason=%q", ok, reason)
	}

	legacyCfg := RotationConfig{MaxConcurrent: 3, MaxPositionUSDT: 500, DefaultReserveUSDT: 0}
	okLegacy, _ := rotationCanOpenOpportunity(opp, available, legacyCfg, 0, nil, nil)
	if !okLegacy {
		t.Fatal("legacy rotation max_position=500 would incorrectly allow scanner-sized opp")
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

// TestDecideRotationSwitchAdvantageMargin 验证换仓优势安全垫:同一显著更优的候选,
// margin 极大时 HOLD(优势不足以覆盖安全垫),margin=0 时 SWITCH——确认安全垫是决定因素,挡掉换仓 churn。
func TestDecideRotationSwitchAdvantageMargin(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {ExpectedFundingUSDT: 4, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 1},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 30, 100)
	newOpp.ExpectedOpenCostUSDT = 1
	funds := map[string]float64{"binance": 200, "bybit": 200}

	// 安全垫极大:任何优势都不够 → HOLD。
	cfg := DefaultRotationConfig()
	cfg.SwitchMinAdvantageUSDT = 1000
	plan := DecideRotation([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{newOpp}, funds, cfg)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("安全垫极大应 HOLD,实得 %+v", plan.Actions)
	}

	// 安全垫=0:同样优势 → SWITCH(证明安全垫是决定因素)。
	cfg.SwitchMinAdvantageUSDT = 0
	plan2 := DecideRotation([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{newOpp}, funds, cfg)
	if len(plan2.Actions) != 1 || plan2.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("安全垫=0 同样优势应 SWITCH,实得 %+v", plan2.Actions)
	}
}

func TestDecideRotationCloseWhenContinuationNegative(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-2 * time.Hour),
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   0,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       10,
		},
	}

	plan := decideRotationAt(
		[]HeldPosition{held},
		heldInputs,
		nil,
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionClose {
		t.Fatalf("expected CLOSE, got %+v", plan.Actions)
	}
}

// ContinuationCloseDisabled(对冲 carry 持有到计划离场)时:即便续持边际转负也不主动平,交由计划离场/roll 在结算窗口再评估。
func TestDecideRotationContinuationCloseDisabledHolds(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-2 * time.Hour),
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {ExpectedFundingUSDT: 0, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 10},
	}
	cfg := DefaultRotationConfig()
	cfg.ContinuationCloseDisabled = true
	plan := decideRotationAt(
		[]HeldPosition{held}, heldInputs, nil,
		map[string]float64{"binance": 200, "bybit": 200},
		cfg, now, 0, nil,
	)
	for _, a := range plan.Actions {
		if a.Kind == RotationActionClose {
			t.Fatalf("ContinuationCloseDisabled 时不应主动平仓,却产生 CLOSE: %+v", a)
		}
	}
}

// RotationConfigFromUnified 应把 exec.CarryHoldToPlannedExitEnabled 注入 ContinuationCloseDisabled。
func TestRotationConfigFromUnifiedWiresHoldToPlannedExit(t *testing.T) {
	exec := CrossVenueExecutorConfig{CarryHoldToPlannedExitEnabled: true}
	cfg := RotationConfigFromUnified(core.UnifiedRotationConfig{}, exec)
	if !cfg.ContinuationCloseDisabled {
		t.Fatal("CarryHoldToPlannedExitEnabled=true 应令 ContinuationCloseDisabled=true")
	}
	exec.CarryHoldToPlannedExitEnabled = false
	if RotationConfigFromUnified(core.UnifiedRotationConfig{}, exec).ContinuationCloseDisabled {
		t.Fatal("CarryHoldToPlannedExitEnabled=false 应令 ContinuationCloseDisabled=false")
	}
}

func TestDecideRotationMarginalNegativeHoldsFreshPosition(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID:          "held-pha",
		StrategyType:           UnifiedStrategyCrossVenueSpotPerp,
		Symbol:                 "PHA-USDT",
		CapitalRequiredByVenue: map[string]float64{"gate": 40, "binance": 40},
		OpenedAt:               now.Add(-30 * time.Second),
	}
	heldInputs := map[string]ContinuationInputs{
		"held-pha": {
			ExpectedFundingUSDT:   -0.043,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       0,
		},
	}

	plan := decideRotationAt(
		[]HeldPosition{held},
		heldInputs,
		nil,
		map[string]float64{"gate": 200, "binance": 200},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD for marginal negative fresh position, got %+v", plan.Actions)
	}
}

func TestDecideRotationStrongNegativeClosesAfterMinHold(t *testing.T) {
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID:          "held-pha",
		StrategyType:           UnifiedStrategyCrossVenueSpotPerp,
		Symbol:                 "PHA-USDT",
		CapitalRequiredByVenue: map[string]float64{"gate": 40, "binance": 40},
		OpenedAt:               now.Add(-2 * time.Hour),
	}
	heldInputs := map[string]ContinuationInputs{
		"held-pha": {
			ExpectedFundingUSDT:   0,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       10,
		},
	}

	plan := decideRotationAt(
		[]HeldPosition{held},
		heldInputs,
		nil,
		map[string]float64{"gate": 200, "binance": 200},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionClose {
		t.Fatalf("expected CLOSE for strong negative after min hold, got %+v", plan.Actions)
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

func TestRotationSwitchMarginalCostExcludesNewSlippage(t *testing.T) {
	opp := testRotationOpp("new", UnifiedStrategyPerpPerp, "ETH", 10, 100, "binance", "bybit")
	opp.ExpectedSlippageUSDT = 0.5
	opp.ExpectedRollbackCostUSDT = 0.3
	heldClose := 2.0
	switchCost := rotationSwitchMarginalCostUSDT(heldClose, opp)
	if switchCost != heldClose {
		t.Fatalf("switchCost=%.4f want held close only %.4f", switchCost, heldClose)
	}
	openScore := rotationComparableAbsoluteScore(opp)
	adjusted := openScore - switchCost
	wantAdjusted := opp.ExpectedNetEVUSDT - rotationHoldingRiskUSDT(opp, unifiedOpportunityTotalCapital(opp)) - heldClose
	if math.Abs(adjusted-wantAdjusted) > 1e-9 {
		t.Fatalf("adjustedOpenScore=%.4f want netEV-holdingRisk-heldClose=%.4f", adjusted, wantAdjusted)
	}
}

func TestDecideRotationHoldWhenReplaceNotOpenEligibleNow(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   1,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategyPerpPerp, "ETH", 50, 100, "binance", "bybit")
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	farFunding := now.Add(600 * time.Second).UTC().Format(time.RFC3339)
	newOpp.HighNextFundingTime = farFunding
	newOpp.LowNextFundingTime = farFunding
	newOpp.FundingIntervalHours = 8
	newOpp.OpenEligibleNow = false

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 500, "bybit": 500},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD when replace not open-eligible, got %+v", plan.Actions)
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

func TestPlanUnifiedRotationOpensSkipsCandidateOnly(t *testing.T) {
	perpPerp := testRotationOpp("pp-best", UnifiedStrategyPerpPerp, "BTC", 50, 100)
	perpPerp.CandidateOnly = true
	carry := testRotationOpp("carry-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 10, 100)

	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{perpPerp, carry},
		map[string]float64{"binance": 200, "bybit": 200},
		DefaultRotationConfig(),
	)
	if len(actions) != 1 {
		t.Fatalf("expected 1 OPEN (carry only), got %d: %+v", len(actions), actions)
	}
	if actions[0].OpportunityID != "carry-eth" {
		t.Fatalf("expected carry open, got %s", actions[0].OpportunityID)
	}
	for _, act := range actions {
		if act.StrategyType == UnifiedStrategyPerpPerp {
			t.Fatalf("perp-perp candidate_only must not OPEN: %+v", act)
		}
	}
}

func TestDecideRotationHoldWhenCandidateOnlyBeatsSwitch(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-svsp",
		StrategyType:           UnifiedStrategySingleVenueSpotPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-svsp": {
			ExpectedFundingUSDT:   4,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	perpPerp := testRotationOpp("pp-best", UnifiedStrategyPerpPerp, "ETH", 50, 100)
	perpPerp.CandidateOnly = true

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{perpPerp},
		map[string]float64{"binance": 200},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD (no SWITCH to candidate_only), got %+v", plan.Actions)
	}
	for _, act := range plan.Actions {
		if act.Kind == RotationActionSwitch && act.ReplaceStrategyType == UnifiedStrategyPerpPerp {
			t.Fatalf("must not SWITCH to perp-perp candidate_only: %+v", act)
		}
	}
}

func TestPlanUnifiedRotationOpensCandidateOnlyDoesNotConsumeConcurrentSlot(t *testing.T) {
	cfg := RotationConfig{
		MaxConcurrent:      1,
		MaxPositionUSDT:    1000,
		DefaultReserveUSDT: 0,
	}
	perpPerp := testRotationOpp("pp-blocker", UnifiedStrategyPerpPerp, "BTC", 100, 80, "binance", "bybit")
	perpPerp.CandidateOnly = true
	carry := testRotationOpp("carry-sol", UnifiedStrategyCrossVenueSpotPerp, "SOL", 8, 80, "binance", "bybit")

	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{perpPerp, carry},
		map[string]float64{"binance": 100, "bybit": 100},
		cfg,
	)
	if len(actions) != 1 {
		t.Fatalf("expected carry to fill sole slot, got %d: %+v", len(actions), actions)
	}
	if actions[0].OpportunityID != "carry-sol" {
		t.Fatalf("expected carry-sol open, got %s", actions[0].OpportunityID)
	}
}

func TestRotationCanOpenBlocksHardRejectReason(t *testing.T) {
	opp := testRotationOpp("bad", UnifiedStrategySingleVenuePerpSpot, "BTC", 20, 100, "bybit")
	opp.RejectReasons = []string{singleVenuePerpSpotRejectBorrowOverLimit}

	ok, reason := rotationCanOpenOpportunity(
		opp,
		map[string]float64{"bybit": 200},
		DefaultRotationConfig(),
		0,
		nil,
		nil,
	)
	if ok {
		t.Fatal("expected hard reject to block open")
	}
	if reason != "reject:"+singleVenuePerpSpotRejectBorrowOverLimit {
		t.Fatalf("reason = %q, want reject:%s", reason, singleVenuePerpSpotRejectBorrowOverLimit)
	}
}

func TestRotationCanOpenBlocksOpenBlockedSymbol(t *testing.T) {
	opp := testRotationOpp("bel", UnifiedStrategyCrossVenueSpotPerp, "BEL", 20, 100, "bybit", "gate")
	blocked := RotationOpenBlocked(func(strategyType, symbol string) bool {
		return strategyType == UnifiedStrategyCrossVenueSpotPerp && symbol == "BEL"
	})

	ok, reason := rotationCanOpenOpportunity(opp, map[string]float64{"bybit": 200, "gate": 200}, DefaultRotationConfig(), 0, nil, blocked)
	if ok {
		t.Fatal("expected open_blocked to prevent open")
	}
	if reason != "open_blocked" {
		t.Fatalf("reason = %q, want open_blocked", reason)
	}
}

func TestDecideRotationExcludesOpenBlockedSymbol(t *testing.T) {
	blocked := testRotationOpp("bel-blocked", UnifiedStrategyCrossVenueSpotPerp, "BEL", 30, 100, "bybit", "gate")
	alt := testRotationOpp("eth-open", UnifiedStrategyCrossVenueSpotPerp, "ETH", 15, 100, "bybit", "gate")
	available := map[string]float64{"bybit": 500, "gate": 500}
	cfg := DefaultRotationConfig()

	blockedFn := RotationOpenBlocked(func(strategyType, symbol string) bool {
		return strategyType == UnifiedStrategyCrossVenueSpotPerp && symbol == "BEL"
	})

	plan := DecideRotationWithBlocked(nil, nil, []UnifiedOpportunity{blocked, alt}, available, cfg, blockedFn)
	var opens []RotationAction
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			opens = append(opens, act)
		}
	}
	if len(opens) != 1 {
		t.Fatalf("expected 1 OPEN (ETH only), got %d: %+v", len(opens), plan.Actions)
	}
	if opens[0].Symbol != "ETH" {
		t.Fatalf("OPEN symbol = %q, want ETH", opens[0].Symbol)
	}
}

func TestPlanUnifiedRotationOpensSkipsHardRejectEvenWithPositiveNetEV(t *testing.T) {
	bad := testRotationOpp("bad-borrow", UnifiedStrategySingleVenuePerpSpot, "BTC", 25, 100, "bybit")
	bad.RejectReasons = []string{singleVenuePerpSpotRejectBorrowOverLimit}
	good := testRotationOpp("good-carry", UnifiedStrategySingleVenueSpotPerp, "ETH", 10, 100, "binance")

	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{bad, good},
		map[string]float64{"bybit": 200, "binance": 200},
		DefaultRotationConfig(),
	)
	if len(actions) != 1 {
		t.Fatalf("expected 1 open (good only), got %d: %+v", len(actions), actions)
	}
	if actions[0].OpportunityID != "good-carry" {
		t.Fatalf("expected good-carry open, got %s", actions[0].OpportunityID)
	}
}

func TestDecideRotationSwitchSkipsHardRejectCandidate(t *testing.T) {
	held := HeldPosition{
		OpportunityID:          "held-btc",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-btc": {
			ExpectedFundingUSDT:   2,
			ExpectedCloseCostUSDT: 2,
			HoldingRiskUSDT:       1,
		},
	}
	badReplace := testRotationOpp("bad-replace", UnifiedStrategyCrossVenuePerpSpot, "ETH", 40, 100, "binance", "bybit")
	badReplace.RejectReasons = []string{crossVenuePerpSpotRejectMarginStress}

	plan := DecideRotation(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{badReplace},
		map[string]float64{"binance": 300, "bybit": 300},
		DefaultRotationConfig(),
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD (no SWITCH to hard-reject), got %+v", plan.Actions)
	}
}

func TestRotationCanOpenBlocksRequiresRebalanceWhenUnfundable(t *testing.T) {
	opp := testRotationOpp("rebal-short", UnifiedStrategyCrossVenueSpotPerp, "SOL", 15, 100, "binance", "bybit")
	opp.RequiresRebalance = true
	opp.RejectReasons = []string{"requires_rebalance"}

	ok, reason := rotationCanOpenOpportunity(
		opp,
		map[string]float64{"binance": 10, "bybit": 200},
		RotationConfig{MaxConcurrent: 3, MaxPositionUSDT: 500, DefaultReserveUSDT: 0},
		0,
		nil,
		nil,
	)
	if ok {
		t.Fatal("expected requires_rebalance unfundable to block open")
	}
	if reason != "requires_rebalance_unfundable" {
		t.Fatalf("reason = %q, want requires_rebalance_unfundable", reason)
	}
}

func TestRotationCanOpenAllowsRequiresRebalanceWhenFunded(t *testing.T) {
	opp := testRotationOpp("rebal-ok", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 80, "binance")
	opp.RequiresRebalance = true
	opp.RejectReasons = []string{"requires_rebalance"}

	ok, reason := rotationCanOpenOpportunity(
		opp,
		map[string]float64{"binance": 200},
		DefaultRotationConfig(),
		0,
		nil,
		nil,
	)
	if !ok {
		t.Fatalf("expected funded requires_rebalance candidate to open, reason=%q", reason)
	}
}

func TestDecideRotationCarryStrategiesRegression(t *testing.T) {
	available := map[string]float64{"binance": 500, "bybit": 500}
	cfg := RotationConfig{MaxConcurrent: 4, MaxPositionUSDT: 500, DefaultReserveUSDT: 0}

	svsp := testRotationOpp("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 50, "binance")
	cvsp := testRotationOpp("open-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH", 11, 50, "binance", "bybit")
	svps := testRotationOpp("open-svps", UnifiedStrategySingleVenuePerpSpot, "SOL", 10, 50, "bybit")
	cvps := testRotationOpp("open-cvps", UnifiedStrategyCrossVenuePerpSpot, "AVAX", 9, 50, "binance", "bybit")

	plan := DecideRotation(nil, nil, []UnifiedOpportunity{cvps, svps, cvsp, svsp}, available, cfg)
	openIDs := map[string]struct{}{}
	for _, act := range plan.Actions {
		if act.Kind != RotationActionOpen {
			t.Fatalf("expected only OPEN actions, got %+v", act)
		}
		openIDs[act.OpportunityID] = struct{}{}
	}
	if len(openIDs) != 4 {
		t.Fatalf("expected 4 carry opens, got %d: %+v", len(openIDs), plan.Actions)
	}

	held := HeldPosition{
		OpportunityID:          "held-svsp",
		StrategyType:           UnifiedStrategySingleVenueSpotPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 50},
	}
	heldInputs := map[string]ContinuationInputs{
		"held-svsp": {ExpectedFundingUSDT: 2, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 1},
	}
	replace := testRotationOpp("replace-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH", 30, 50, "binance", "bybit")
	switchPlan := DecideRotation([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{replace}, available, cfg)
	if len(switchPlan.Actions) != 1 || switchPlan.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("expected carry SWITCH regression, got %+v", switchPlan.Actions)
	}
	if switchPlan.Actions[0].ReplaceOpportunityID != "replace-cvsp" {
		t.Fatalf("expected switch to replace-cvsp, got %s", switchPlan.Actions[0].ReplaceOpportunityID)
	}
}

func rotationBudgetTestCfg(totalDeployable float64, strategyPct float64) RotationConfig {
	return RotationConfig{
		MaxConcurrent:              10,
		MaxPositionUSDT:            500,
		DefaultReserveUSDT:         0,
		TotalDeployableUSDT:        totalDeployable,
		MaxStrategyCapitalUsagePct: strategyPct,
	}
}

func TestRotationGlobalBudgetAllowsMultipleStrategies(t *testing.T) {
	cfg := rotationBudgetTestCfg(500, 0)
	available := map[string]float64{"binance": 300, "bybit": 300}
	svsp := testRotationOpp("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 50, "binance")
	cvsp := testRotationOpp("open-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH", 11, 50, "binance", "bybit")

	plan := DecideRotation(nil, nil, []UnifiedOpportunity{cvsp, svsp}, available, cfg)
	if len(plan.Actions) != 2 {
		t.Fatalf("expected 2 OPEN under sufficient global budget, got %+v", plan.Actions)
	}
}

func TestRotationGlobalBudgetExceededBlocksLaterCandidates(t *testing.T) {
	cfg := rotationBudgetTestCfg(120, 0)
	available := map[string]float64{"binance": 200, "bybit": 200}
	a := testRotationOpp("open-a", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 50, "binance")
	b := testRotationOpp("open-b", UnifiedStrategyCrossVenueSpotPerp, "ETH", 11, 50, "binance", "bybit")
	c := testRotationOpp("open-c", UnifiedStrategySingleVenuePerpSpot, "SOL", 10, 50, "bybit")

	plan := DecideRotation(nil, nil, []UnifiedOpportunity{c, b, a}, available, cfg)
	openCount := 0
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			openCount++
		}
	}
	if openCount != 2 {
		t.Fatalf("expected 2 OPEN under global budget 120, got %d: %+v", openCount, plan.Actions)
	}

	budget := newRotationBudgetState(available, nil, cfg, 0)
	budget.addOpportunity(a)
	budget.addOpportunity(b)
	ok, reason := rotationCanOpenOpportunity(c, available, cfg, 2, &budget, nil)
	if ok {
		t.Fatal("expected third candidate blocked by global budget")
	}
	if reason != "global_budget_exceeded" {
		t.Fatalf("reason = %q, want global_budget_exceeded", reason)
	}
}

func TestRotationStrategyCapExceededBlocksSameStrategy(t *testing.T) {
	cfg := rotationBudgetTestCfg(200, 0.40) // 单策略上限 80 USDT
	available := map[string]float64{"binance": 300, "bybit": 300}
	svsp1 := testRotationOpp("svsp1", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 50, "binance")
	svsp2 := testRotationOpp("svsp2", UnifiedStrategySingleVenueSpotPerp, "ETH", 11, 50, "binance")
	cvsp1 := testRotationOpp("cvsp1", UnifiedStrategyCrossVenueSpotPerp, "SOL", 10, 50, "binance", "bybit")

	plan := DecideRotation(nil, nil, []UnifiedOpportunity{svsp2, cvsp1, svsp1}, available, cfg)
	openByStrategy := map[string]int{}
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			openByStrategy[act.StrategyType]++
		}
	}
	if openByStrategy[UnifiedStrategySingleVenueSpotPerp] != 1 {
		t.Fatalf("expected 1 SVSP open under strategy cap, got %+v", openByStrategy)
	}
	if openByStrategy[UnifiedStrategyCrossVenueSpotPerp] != 1 {
		t.Fatalf("expected CVSP still opens with remaining budget, got %+v", openByStrategy)
	}

	budget := newRotationBudgetState(available, nil, cfg, 0)
	budget.addOpportunity(svsp1)
	ok, reason := rotationCanOpenOpportunity(svsp2, available, cfg, 1, &budget, nil)
	if ok {
		t.Fatal("expected second SVSP blocked by strategy cap")
	}
	if reason != "strategy_cap_exceeded" {
		t.Fatalf("reason = %q, want strategy_cap_exceeded", reason)
	}
}

func TestRotationGlobalBudgetIncludesPerpPerpHeld(t *testing.T) {
	cfg := rotationBudgetTestCfg(100, 0)
	held := HeldPosition{
		OpportunityID:          "pp-held",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "BTC",
		CapitalRequiredByVenue: map[string]float64{"binance": 40, "bybit": 40},
	}
	heldInputs := map[string]ContinuationInputs{
		"pp-held": {ExpectedFundingUSDT: 5, ExpectedCloseCostUSDT: 2, HoldingRiskUSDT: 1},
	}
	freeAvailable := map[string]float64{"binance": 60, "bybit": 60}
	blockOpp := testRotationOpp("open-block", UnifiedStrategySingleVenueSpotPerp, "ETH", 12, 30, "binance")

	plan := DecideRotation([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{blockOpp}, freeAvailable, cfg)
	for _, act := range plan.Actions {
		if act.Kind == RotationActionOpen {
			t.Fatalf("expected no OPEN: perp-perp held consumes global budget, got %+v", act)
		}
	}

	budget := newRotationBudgetState(freeAvailable, []HeldPosition{held}, cfg, 0)
	ok, reason := rotationCanOpenOpportunity(blockOpp, freeAvailable, cfg, 1, &budget, nil)
	if ok {
		t.Fatal("expected OPEN blocked when held+new exceeds global budget")
	}
	if reason != "global_budget_exceeded" {
		t.Fatalf("reason = %q, want global_budget_exceeded", reason)
	}

	allowOpp := testRotationOpp("open-ok", UnifiedStrategySingleVenueSpotPerp, "SOL", 10, 15, "binance")
	ok, reason = rotationCanOpenOpportunity(allowOpp, freeAvailable, cfg, 1, &budget, nil)
	if !ok {
		t.Fatalf("expected smaller open within remaining budget, reason=%q", reason)
	}
}

func TestRotationBudgetZeroDegeneratesToLegacyBehavior(t *testing.T) {
	cfg := RotationConfig{MaxConcurrent: 4, MaxPositionUSDT: 500, DefaultReserveUSDT: 0}
	available := map[string]float64{"binance": 500, "bybit": 500}
	svsp := testRotationOpp("open-svsp", UnifiedStrategySingleVenueSpotPerp, "BTC", 12, 50, "binance")
	cvsp := testRotationOpp("open-cvsp", UnifiedStrategyCrossVenueSpotPerp, "ETH", 11, 50, "binance", "bybit")
	svps := testRotationOpp("open-svps", UnifiedStrategySingleVenuePerpSpot, "SOL", 10, 50, "bybit")
	cvps := testRotationOpp("open-cvps", UnifiedStrategyCrossVenuePerpSpot, "AVAX", 9, 50, "binance", "bybit")

	plan := DecideRotation(nil, nil, []UnifiedOpportunity{cvps, svps, cvsp, svsp}, available, cfg)
	if len(plan.Actions) != 4 {
		t.Fatalf("expected 4 OPEN with budget disabled, got %+v", plan.Actions)
	}
}

func TestRotationContinuationUsesWindowFundingNotCumulative(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	held := HeldPosition{
		OpportunityID:          "held-long",
		StrategyType:           UnifiedStrategyCrossVenueSpotPerp,
		Symbol:                 "OLD-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-72 * time.Hour),
		// 已过首次结算,使 SWITCH min-hold(延至结算)不阻断;续持 window funding 仍用 future nextFunding 计算。
		NextFundingTime: now.Add(-30 * time.Minute),
	}
	// 累计 funding 很大(旧口径会虚高续持分),下一窗口仅 2U。
	windowFunding := rotationSpotPerpWindowFundingUSDT(1000, 0.001, 8, nextFunding, now, false)
	if windowFunding <= 0 || windowFunding >= 100 {
		t.Fatalf("window funding=%v want small positive window EV", windowFunding)
	}
	heldInputs := map[string]ContinuationInputs{
		"held-long": rotationContinuationFromPosition(windowFunding, 1000, CrossVenueExecutorConfig{}, false, rotationContinuationOpts{}),
	}
	newOpp := testRotationOpp("new-hot", UnifiedStrategyCrossVenueSpotPerp, "NEW-USDT", 25, 100, "binance", "bybit")
	newOpp.ExpectedFundingUSDT = 25
	newOpp.ExpectedOpenCostUSDT = 1

	plan := decideRotationAt(
		[]HeldPosition{held},
		heldInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 500, "bybit": 500},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionSwitch {
		t.Fatalf("expected SWITCH (window口径续持分低于候选), got %+v", plan.Actions)
	}

	// 若误用累计值 500U,会 HOLD。
	staleInputs := map[string]ContinuationInputs{
		"held-long": rotationContinuationFromPosition(500, 1000, CrossVenueExecutorConfig{}, false, rotationContinuationOpts{}),
	}
	stalePlan := decideRotationAt(
		[]HeldPosition{held},
		staleInputs,
		[]UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 500, "bybit": 500},
		DefaultRotationConfig(),
		now,
		0,
		nil,
	)
	if len(stalePlan.Actions) != 1 || stalePlan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("cumulative funding should incorrectly HOLD, got %+v", stalePlan.Actions)
	}
}

func TestRotationSpotPerpWindowFundingMatchesCandidateSettlements(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	nextFunding := now.Add(3 * time.Hour)
	notional := 2000.0
	rate := 0.0004
	got := rotationSpotPerpWindowFundingUSDT(notional, rate, 4, nextFunding, now, false)
	settlements := crossVenueSpotPerpFundingSettlementCount(now, 3, 4, nextFunding)
	want := notional * rate * float64(settlements)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("window funding=%v want %v (settlements=%d)", got, want, settlements)
	}
}

func TestRotationPerpPerpWindowFundingMatchesCandidateDiscreteCaptureBasis(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	notional := 3000.0
	highRate, lowRate := 0.0009, 0.0001
	highInt, lowInt := 1, 8 // 异周期;high(空)在窗内结算→正 spread funding
	highNext := now.Add(15 * time.Minute)
	lowNext := now.Add(50 * time.Minute)

	got := rotationPerpPerpWindowFundingUSDT(notional, highRate, highInt, lowRate, lowInt, highNext, lowNext, now)

	// FIX-9:续持口径必须等于候选口径——离散结算次数(crossVenueExpectedFundingOverHold)× 捕获概率折现。
	windowHours := crossVenueComparableWindowHours(now, highNext, lowNext)
	candidateOpp := CrossVenueOpportunity{
		HighFundingRate:     highRate,
		LowFundingRate:      lowRate,
		HighIntervalHours:   highInt,
		LowIntervalHours:    lowInt,
		HighNextFundingTime: highNext,
		LowNextFundingTime:  lowNext,
	}
	wantFunding := crossVenueExpectedFundingOverHold(now, windowHours, notional, candidateOpp)
	wantFunding *= crossVenueExecutorFundingCaptureProbability(CrossVenueExecutorConfig{})
	if math.Abs(got-wantFunding) > 1e-9 {
		t.Fatalf("continuation funding=%v want candidate discrete×capture basis %v", got, wantFunding)
	}

	// 必须与旧线性口径(fundingPerHour×windowHours,无捕获折现)不同,否则未真正改口径。
	oldLinear := crossVenueExpectedFundingPerHour(notional, highRate, highInt, lowRate, lowInt) * windowHours
	if math.Abs(got-oldLinear) < 1e-9 {
		t.Fatalf("continuation funding still equals old linear basis %v; capture/discrete fix not applied", oldLinear)
	}
	// 捕获概率 ∈ (0,1) → 续持口径应严格小于未折现的离散值。
	undiscounted := crossVenueExpectedFundingOverHold(now, windowHours, notional, candidateOpp)
	if !(got < undiscounted) {
		t.Fatalf("continuation funding %v should be discounted below undiscounted discrete %v", got, undiscounted)
	}
}

func TestRotationPerpPerpEntryWindowPerLegDistinguishesMixedIntervals(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	highNext := now.Add(6 * time.Hour)      // 8h 腿,远离结算
	lowNext := now.Add(-3540 * time.Second) // 1h 腿,59min 前结算 → 下次结算在 +60s
	opp := UnifiedOpportunity{
		StrategyType:         UnifiedStrategyPerpPerp,
		HighNextFundingTime:  highNext.Format(time.RFC3339),
		LowNextFundingTime:   lowNext.Format(time.RFC3339),
		HighIntervalHours:    8,
		LowIntervalHours:     1,
		FundingIntervalHours: 8, // 旧单 interval 口径会误用 8h 评估两腿
	}
	execCfg := CrossVenueExecutorConfig{} // lead=180s 默认
	// FIX-10(a):per-leg——1h 腿在 ~60s 内结算(≤180s lead)→ 进场窗已到 → eligible。
	if !rotationOpportunityFundingEntryWindowEligible(opp, execCfg, now) {
		t.Fatal("per-leg: 1h-interval low leg settles in ~60s (within 180s lead) → must be entry-window eligible")
	}
	// 对照:旧单 8h interval 会把过期 low 推进到 +8h,nearest≈6h → 不在窗口 → 误判 not eligible。
	lead := 180 * time.Second
	if crossVenueFundingEntryWindowReached(highNext, 8, lowNext, 8, now, lead) {
		t.Fatal("control: single-8h-interval interpretation should NOT reach window (demonstrates per-leg fix matters)")
	}
}

func TestRotationOpenEligibilityFailsClosedWhenFundingTimesUnknown(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	execCfg := CrossVenueExecutorConfig{}

	// FIX-10(b) perp-perp:两腿结算时间均缺失 → 进场窗闸 fail-CLOSED → 不 eligible。
	ppOpp := UnifiedOpportunity{
		StrategyType:      UnifiedStrategyPerpPerp,
		HighIntervalHours: 1,
		LowIntervalHours:  8,
	}
	if rotationOpportunityFundingEntryWindowEligible(ppOpp, execCfg, now) {
		t.Fatal("perp-perp with unknown funding times must be NOT eligible (fail-closed)")
	}

	// FIX-10(b) spot-perp(default 分支):缺 NextFundingTime → fail-CLOSED。
	spOpp := UnifiedOpportunity{
		StrategyType:         UnifiedStrategyCrossVenueSpotPerp,
		FundingIntervalHours: 8,
	}
	if rotationOpportunityFundingEntryWindowEligible(spOpp, execCfg, now) {
		t.Fatal("spot-perp with unknown funding time must be NOT eligible (fail-closed)")
	}

	// FIX-10(b) 决策层:OpenEligibleNow=false 且无 funding 时间数据 → NOT eligible(不再 fail-open)。
	decisionOpp := UnifiedOpportunity{StrategyType: UnifiedStrategyPerpPerp, OpenEligibleNow: false}
	if rotationOpportunityOpenEligibleForDecision(decisionOpp) {
		t.Fatal("decision layer: unknown funding times + not eligible → must be NOT open-eligible (fail-closed)")
	}
	decisionOpp.OpenEligibleNow = true
	if !rotationOpportunityOpenEligibleForDecision(decisionOpp) {
		t.Fatal("decision layer: OpenEligibleNow=true should remain eligible")
	}
}

func TestRotationContinuationMinHoldUsesNextFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	pos := HeldPosition{
		OpenedAt:        now.Add(-30 * time.Minute),
		NextFundingTime: now.Add(4 * time.Hour),
	}
	cfg := DefaultRotationConfig()
	cfg.ContinuationMinHoldSeconds = 3600
	if rotationContinuationMinHoldSatisfied(pos, cfg, now) {
		t.Fatal("should block soft close before first funding when funding wait > min hold")
	}
	pos.OpenedAt = now.Add(-2 * time.Hour)
	pos.NextFundingTime = now.Add(-30 * time.Minute)
	if !rotationContinuationMinHoldSatisfied(pos, cfg, now) {
		t.Fatal("should allow after passing first funding and fixed min hold")
	}
}

func TestCollectCrossVenueSpotPerpHeldRecordsIntervalFallback8(t *testing.T) {
	execCfg := CrossVenueExecutorConfig{DefaultTakerFeeBps: 4}
	ps := []CrossVenueSpotPerpPosition{{
		OpportunityID:   "cvsp-old",
		Symbol:          "BTC-USDT",
		SpotVenue:       "binance",
		PerpVenue:       "bybit",
		NotionalUSDT:    100,
		PerpFundingRate: 0.001,
		Status:          CVSPStatusOpen,
		OpenedAt:        time.Now().UTC().Add(-time.Hour),
	}}
	recs := collectCrossVenueSpotPerpHeldRecords(ps, execCfg)
	if len(recs) != 1 {
		t.Fatalf("records=%d want 1", len(recs))
	}
	if recs[0].ExpectedFundingUSDT < 0 {
		t.Fatalf("continuation funding should not panic with missing interval, got %v", recs[0].ExpectedFundingUSDT)
	}
}

func TestRotationContinuationBorrowCostUsesProbeRate(t *testing.T) {
	notional := 10_000.0
	holdHours := 24.0
	// 日利率 1% → 24h borrow = 100U, 远高于旧固定 3U (notional×0.0003)。
	daily := map[string]float64{"BTC": 0.01}
	got := rotationContinuationBorrowCostUSDT(notional, true, "BTC", holdHours, daily)
	want := perpSpotBorrowCostUSDT("BTC", notional, holdHours, daily, 0)
	if math.Abs(got-want) > 1e-9 || got <= notional*rotationContinuationBorrowFallbackDailyRate {
		t.Fatalf("probe borrow=%v want %v (> fallback %v)", got, want, notional*rotationContinuationBorrowFallbackDailyRate)
	}
}

func TestRotationContinuationBorrowCostFallbackWithoutProbe(t *testing.T) {
	notional := 10_000.0
	got := rotationContinuationBorrowCostUSDT(notional, true, "BTC", 24, nil)
	want := notional * rotationContinuationBorrowFallbackDailyRate
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("fallback borrow=%v want %v", got, want)
	}
	gotEmpty := rotationContinuationBorrowCostUSDT(notional, true, "ETH", 24, map[string]float64{})
	if math.Abs(gotEmpty-want) > 1e-9 {
		t.Fatalf("missing asset fallback=%v want %v", gotEmpty, want)
	}
}

func TestHoldingRiskUSDTUnifiedAcrossPaths(t *testing.T) {
	const riskScore = 0.5
	const capital = 2000.0
	want := riskScore * capital * rotationHoldingRiskPerScoreUnit

	opp := testRotationOpp("x", UnifiedStrategySingleVenueSpotPerp, "BTC", 10, capital)
	opp.RiskScore = riskScore

	if got := holdingRiskUSDT(riskScore, capital); math.Abs(got-want) > 1e-9 {
		t.Fatalf("holdingRiskUSDT=%v want %v", got, want)
	}
	if got := rotationHoldingRiskUSDT(opp, capital); math.Abs(got-want) > 1e-9 {
		t.Fatalf("rotationHoldingRiskUSDT=%v want %v", got, want)
	}
	if got := unifiedOpportunityRiskAdjustedNetEV(opp, capital); math.Abs((opp.ExpectedNetEVUSDT-got)-want) > 1e-9 {
		t.Fatalf("allocator riskAdj deduction=%v want %v", opp.ExpectedNetEVUSDT-got, want)
	}

	cont := rotationContinuationFromPosition(0, capital, CrossVenueExecutorConfig{}, false, rotationContinuationOpts{
		riskScore: riskScore,
	})
	if math.Abs(cont.HoldingRiskUSDT-want) > 1e-9 {
		t.Fatalf("continuation holdingRisk=%v want %v", cont.HoldingRiskUSDT, want)
	}
}

func TestRotationContinuationDefaultHoldingRiskBackwardCompat(t *testing.T) {
	notional := 1000.0
	cont := rotationContinuationFromPosition(0, notional, CrossVenueExecutorConfig{}, false, rotationContinuationOpts{})
	want := notional * 0.001 // 旧口径: notional×0.001 = riskScore0.1 × notional × 1%
	if math.Abs(cont.HoldingRiskUSDT-want) > 1e-9 {
		t.Fatalf("default holdingRisk=%v want %v", cont.HoldingRiskUSDT, want)
	}
}

// P4(2026-06-20): 主排序口径改为绝对净收益(ExpectedNetEVUSDT)。本金长期闲置时机会稀缺、
// 应优先抓绝对美元最大的单;资本效率仅作 tiebreaker。故大 EV 单先开,而非高资本效率单。
func TestRotationOpenSortsByAbsoluteEVNotYield(t *testing.T) {
	bigAbsEV := testRotationOpp("big", UnifiedStrategyPerpPerp, "BTC", 50, 1000, "binance", "bybit")
	smallHighYield := testRotationOpp("small", UnifiedStrategyPerpPerp, "ETH", 30, 100, "binance", "bybit")
	actions := PlanUnifiedRotationOpens(
		[]UnifiedOpportunity{bigAbsEV, smallHighYield},
		map[string]float64{"binance": 2000, "bybit": 2000},
		RotationConfig{MaxConcurrent: 2, MaxPositionUSDT: 2000, DefaultReserveUSDT: 0},
	)
	if len(actions) != 2 || actions[0].OpportunityID != "big" {
		t.Fatalf("expected absolute-EV-first big, got %+v", actions)
	}
}

func TestRotationMinOpenNotionalRejectsThinCandidates(t *testing.T) {
	cfg := DefaultRotationConfig()
	cfg.DefaultReserveUSDT = 0
	thin := testRotationOpp("thin", UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", 5, 6, "binance", "gate")
	thin.Legs = []UnifiedOpportunityLeg{{Venue: "binance", Notional: 3}, {Venue: "gate", Notional: 3}}
	okOpp := testRotationOpp("ok", UnifiedStrategyCrossVenueSpotPerp, "ETH-USDT", 4, 50, "binance", "gate")
	okOpp.Legs = []UnifiedOpportunityLeg{{Venue: "binance", Notional: 50}, {Venue: "gate", Notional: 50}}
	actions := PlanUnifiedRotationOpens([]UnifiedOpportunity{thin, okOpp}, map[string]float64{"binance": 200, "gate": 200}, cfg)
	if len(actions) != 1 || actions[0].OpportunityID != "ok" {
		t.Fatalf("expected only ok opp, got %+v", actions)
	}
}

func TestRotationOpenCooldownBlocksCandidate(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	store := newRotationOpenCooldownStore("", 15*time.Minute)
	store.RecordFailure(UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", "binance+gate", now)
	cfg := DefaultRotationConfig()
	cfg.OpenCooldownBlocked = store.blockedPredicate(func() time.Time { return now })
	cfg.DefaultReserveUSDT = 0
	opp := testRotationOpp("bel", UnifiedStrategyCrossVenueSpotPerp, "BEL-USDT", 10, 80, "binance", "gate")
	ok, reason := rotationCanOpenOpportunity(opp, map[string]float64{"binance": 200, "gate": 200}, cfg, 0, nil, nil)
	if ok || reason != "open_cooldown" {
		t.Fatalf("cooldown: ok=%v reason=%q", ok, reason)
	}
}

func TestDefaultRotationConfigMinOpenNotional(t *testing.T) {
	cfg := DefaultRotationConfig()
	if math.Abs(cfg.MinOpenNotionalUSDT-5) > 1e-9 {
		t.Fatalf("MinOpenNotionalUSDT = %.2f, want 5", cfg.MinOpenNotionalUSDT)
	}
}

func TestRotationSwitchMinHoldExtendsToNextFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	cfg := DefaultRotationConfig()
	cfg.ContinuationMinHoldSeconds = 3600

	pos := HeldPosition{
		OpenedAt:        now.Add(-90 * time.Minute),
		NextFundingTime: now.Add(30 * time.Minute),
	}
	if rotationSwitchMinHoldSatisfied(pos, cfg, now) {
		t.Fatal("SWITCH should stay blocked until first funding when open→funding > fixed min hold")
	}

	pos.OpenedAt = now.Add(-2 * time.Hour)
	pos.NextFundingTime = now.Add(-30 * time.Minute)
	if !rotationSwitchMinHoldSatisfied(pos, cfg, now) {
		t.Fatal("SWITCH should allow after fixed min hold and first funding passed")
	}

	posZeroFunding := HeldPosition{OpenedAt: now.Add(-30 * time.Minute)}
	if rotationSwitchMinHoldSatisfied(posZeroFunding, cfg, now) {
		t.Fatal("zero NextFundingTime should fall back to fixed min hold only")
	}
}

func TestDecideRotationSwitchBlockedByMinHoldNearFunding(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID: "held-btc", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-90 * time.Minute), NextFundingTime: now.Add(30 * time.Minute),
	}
	heldInputs := map[string]ContinuationInputs{"held-btc": {ExpectedFundingUSDT: 1, ExpectedCloseCostUSDT: 2}}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 30, 100)
	plan := decideRotationAt([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 200, "bybit": 200}, DefaultRotationConfig(), now, 0, nil)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD near funding, got %+v", plan.Actions)
	}
	if !strings.Contains(plan.Actions[0].Reason, "hold_switch_min_hold") {
		t.Fatalf("reason=%q", plan.Actions[0].Reason)
	}
}

func TestDecideRotationSwitchBlockedByMinHold(t *testing.T) {
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	held := HeldPosition{
		OpportunityID: "held-btc", StrategyType: UnifiedStrategyPerpPerp, Symbol: "BTC-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-10 * time.Minute), NextFundingTime: now.Add(4 * time.Hour),
	}
	heldInputs := map[string]ContinuationInputs{"held-btc": {ExpectedFundingUSDT: 1, ExpectedCloseCostUSDT: 2}}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH-USDT", 30, 100)
	plan := decideRotationAt([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 200, "bybit": 200}, DefaultRotationConfig(), now, 0, nil)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD, got %+v", plan.Actions)
	}
	if !strings.Contains(plan.Actions[0].Reason, "hold_switch_min_hold") {
		t.Fatalf("reason=%q", plan.Actions[0].Reason)
	}
}
