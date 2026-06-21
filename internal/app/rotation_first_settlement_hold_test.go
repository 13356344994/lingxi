package app

import (
	"strings"
	"testing"
	"time"
)

func TestRotationFirstSettlementHoldBlocksVoluntaryCloseBeforeFunding(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	firstFunding := now.Add(30 * time.Minute)
	pos := HeldPosition{
		OpportunityID:          "held-ev",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "H-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-5 * time.Minute),
		TargetFirstFundingTime: firstFunding,
	}
	heldInputs := map[string]ContinuationInputs{
		"held-ev": {
			ExpectedFundingUSDT:   0,
			ExpectedCloseCostUSDT: 0.5,
			HoldingRiskUSDT:       5,
		},
	}
	cfg := DefaultRotationConfig()
	cfg.ContinuationMinHoldSeconds = 0
	cfg.MinHoldToFirstSettlementEnabled = true
	cfg.MinHoldToFirstSettlementBufferSeconds = 0

	plan := decideRotationAt([]HeldPosition{pos}, heldInputs, nil, map[string]float64{"binance": 200, "bybit": 200}, cfg, now, 0, nil)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected HOLD before first settlement, got %+v", plan.Actions)
	}
}

func TestCrossVenueHardRiskCloseBypassesFirstSettlementHold(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	pos := &CrossVenuePosition{
		Symbol:                 "H-USDT",
		HighVenue:              "binance",
		LowVenue:               "bybit",
		OpenedAt:               now.Add(-5 * time.Minute),
		TargetFirstFundingTime: now.Add(30 * time.Minute),
		Status:                 CrossVenueStatusOpen,
	}
	e := &CrossVenueExecutor{
		cfg: EngineConfig{CrossVenueExecutor: CrossVenueExecutorConfig{
			MinHoldToFirstSettlementEnabled:       true,
			MinHoldToFirstSettlementBufferSeconds: 60,
			CloseSpreadConsecutiveRounds:          1,
			CloseHardNegativeSpreadAPR:            -0.5,
			MaxHoldHours:                          72,
		}},
		nowFn: func() time.Time { return now },
	}
	reason := e.crossVenueFilterVoluntaryCloseReason(pos, "价差深度转负 -60.0% 连续 1 轮低于 -50.0%(硬止损)")
	if reason == "" {
		t.Fatal("expected hard-risk close to bypass first-settlement hold")
	}
}

func TestCrossVenueVoluntaryEVCloseBlockedBeforeFirstSettlement(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	pos := &CrossVenuePosition{
		OpenedAt:               now.Add(-5 * time.Minute),
		TargetFirstFundingTime: now.Add(30 * time.Minute),
	}
	e := &CrossVenueExecutor{
		cfg: EngineConfig{CrossVenueExecutor: CrossVenueExecutorConfig{
			MinHoldToFirstSettlementEnabled:       true,
			MinHoldToFirstSettlementBufferSeconds: 0,
		}},
		nowFn: func() time.Time { return now },
	}
	reason := e.crossVenueFilterVoluntaryCloseReason(pos, "未来净值<=0(-0.5000)")
	if reason != "" {
		t.Fatalf("expected voluntary EV close blocked before first settlement, got %q", reason)
	}
}

func TestCrossVenueVoluntaryCloseAllowedAfterFirstSettlement(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	firstFunding := now.Add(-2 * time.Minute)
	pos := &CrossVenuePosition{
		OpenedAt:               firstFunding.Add(-30 * time.Minute),
		TargetFirstFundingTime: firstFunding,
	}
	e := &CrossVenueExecutor{
		cfg: EngineConfig{CrossVenueExecutor: CrossVenueExecutorConfig{
			MinHoldToFirstSettlementEnabled:       true,
			MinHoldToFirstSettlementBufferSeconds: 0,
		}},
		nowFn: func() time.Time { return now },
	}
	reason := e.crossVenueFilterVoluntaryCloseReason(pos, "未来净值<=0(-0.5000)")
	if reason == "" || !strings.Contains(reason, "未来净值<=0") {
		t.Fatalf("expected voluntary EV close after first settlement, got %q", reason)
	}
}

func TestRotationVoluntarySwitchBlockedBeforeFirstSettlement(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	firstFunding := now.Add(20 * time.Minute)
	held := HeldPosition{
		OpportunityID:          "held-switch",
		StrategyType:           UnifiedStrategyPerpPerp,
		Symbol:                 "H-USDT",
		CapitalRequiredByVenue: map[string]float64{"binance": 50, "bybit": 50},
		OpenedAt:               now.Add(-10 * time.Minute),
		TargetFirstFundingTime: firstFunding,
	}
	heldInputs := map[string]ContinuationInputs{
		"held-switch": {ExpectedFundingUSDT: 1, ExpectedCloseCostUSDT: 1, HoldingRiskUSDT: 0},
	}
	newOpp := testRotationOpp("new-eth", UnifiedStrategySingleVenueSpotPerp, "ETH", 50, 100)
	cfg := DefaultRotationConfig()
	cfg.ContinuationMinHoldSeconds = 0
	cfg.MinHoldToFirstSettlementEnabled = true

	plan := decideRotationAt([]HeldPosition{held}, heldInputs, []UnifiedOpportunity{newOpp},
		map[string]float64{"binance": 500, "bybit": 500}, cfg, now, 0, nil)
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != RotationActionHold {
		t.Fatalf("expected switch blocked before first settlement, got %+v", plan.Actions)
	}
	if !strings.Contains(plan.Actions[0].Reason, "hold_switch_min_hold") {
		t.Fatalf("expected hold_switch_min_hold reason, got %q", plan.Actions[0].Reason)
	}
}
