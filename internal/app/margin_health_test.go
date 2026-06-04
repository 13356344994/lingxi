package app

import (
	"io"
	"log"
	"math"
	"testing"
	"time"
)

func newMarginHealthEngine(t *testing.T, direction fundingCarryDirection) *FundingRateSniperEngine {
	t.Helper()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.RiskConfig.MinMarginLevel = 1.5
	engine.activeOpenHedges = []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC", Direction: direction}},
	}
	return engine
}

func TestCrossMarginLevelPure(t *testing.T) {
	if got := crossMarginLevel(0.5, 1.0); math.Abs(got-1.5) > 1e-9 {
		t.Fatalf("expected 1.5, got %v", got)
	}
	if got := crossMarginLevel(1.0, 0); !math.IsInf(got, 1) {
		t.Fatalf("no liability should be +Inf, got %v", got)
	}
	if got := crossMarginLevel(1.0, -1); !math.IsInf(got, 1) {
		t.Fatalf("negative liability should be +Inf, got %v", got)
	}
}

func TestMarginHealthWithdrawTriggersBelowThreshold(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.marginHealthLevel = 1.2 // < 1.5
	engine.marginHealthLiabilityBTC = 0.05
	engine.lastMarginHealthAt = time.Now().UTC()

	reason, ok := engine.shouldTriggerMarginHealthWithdraw()
	if !ok {
		t.Fatalf("margin level below threshold should trigger withdraw, reason=%q", reason)
	}
	if !engine.panicFlattenInFlight {
		t.Fatal("trigger should claim the global panic in-flight guard")
	}
}

func TestMarginHealthWithdrawSilentWhenHealthy(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.marginHealthLevel = 2.0 // >= 1.5
	engine.lastMarginHealthAt = time.Now().UTC()

	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("healthy margin should not trigger withdraw")
	}
}

func TestMarginHealthWithdrawDisabledByDefault(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.cfg.RiskConfig.MinMarginLevel = 0 // opt-in off
	engine.marginHealthLevel = 1.01
	engine.lastMarginHealthAt = time.Now().UTC()

	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("disabled guard should never trigger")
	}
}

func TestMarginHealthWithdrawSkipsWithoutNegativeCarry(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionPositive)
	engine.marginHealthLevel = 1.01
	engine.lastMarginHealthAt = time.Now().UTC()

	// 仅持有正 carry（无借币空头）时 margin 健康不相关，不触发。
	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("no negative-carry exposure should skip margin guard")
	}
}

func TestMarginHealthWithdrawSkipsOnStaleData(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.marginHealthLevel = 1.01
	engine.lastMarginHealthAt = time.Now().UTC().Add(-2 * marginHealthStaleAfter)

	// 过期数据不可信，不据此撤离（数据失明另由 R32 机制处置）。
	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("stale margin data should not trigger withdraw")
	}
}

func TestMarginHealthWithdrawSkipsWhenNeverPolled(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.marginHealthLevel = 1.01 // 但 lastMarginHealthAt 为零值

	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("never-polled margin should not trigger withdraw")
	}
}

func TestMarginHealthWithdrawSkipsWhenAlreadyInFlight(t *testing.T) {
	engine := newMarginHealthEngine(t, fundingCarryDirectionNegative)
	engine.marginHealthLevel = 1.01
	engine.lastMarginHealthAt = time.Now().UTC()
	engine.panicFlattenInFlight = true

	if _, ok := engine.shouldTriggerMarginHealthWithdraw(); ok {
		t.Fatal("panic already in-flight should be skipped")
	}
}
