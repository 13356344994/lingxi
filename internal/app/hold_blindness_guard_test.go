package app

import (
	"io"
	"log"
	"testing"
	"time"
)

func newBlindnessGuardEngine(t *testing.T) *FundingRateSniperEngine {
	t.Helper()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	engine.cfg.RiskConfig.FeedBlindnessGuardEnabled = true
	engine.exposureLocks = map[string]exposureLockState{"BTCUSDT": {}}
	return engine
}

func setFeedMarketAge(engine *FundingRateSniperEngine, age time.Duration) {
	now := time.Now().UTC()
	engine.feedLastFrameAt = map[feedKind]time.Time{
		feedFuturesUser: now,
		feedSpotUser:    now,
		feedMarket:      now.Add(-age),
	}
}

func TestBlindnessGuardBlocksOpensWhenMarketBlind(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	setFeedMarketAge(engine, holdMonitorFeedBlindAge+10*time.Second)

	engine.observeHoldMonitor()

	if !engine.feedBlindnessBlockOpens {
		t.Fatal("blind market feed during hold should block new opens")
	}
	if engine.feedMarketBlindSince.IsZero() {
		t.Fatal("blind market feed should start the blind-since timer")
	}
	if blocked, _ := engine.feedBlindnessBlockingOpens(); !blocked {
		t.Fatal("feedBlindnessBlockingOpens should report blocked")
	}
}

func TestBlindnessGuardBlocksOpensWhenDegradedButNoWithdrawTimer(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	// 介于 degraded 与 blind 阈值之间。
	setFeedMarketAge(engine, holdMonitorFeedDegradedAge+2*time.Second)

	engine.observeHoldMonitor()

	if !engine.feedBlindnessBlockOpens {
		t.Fatal("degraded market feed should block new opens")
	}
	if !engine.feedMarketBlindSince.IsZero() {
		t.Fatal("degraded (not blind) should not start the withdraw timer")
	}
}

func TestBlindnessGuardClearsWhenMarketLive(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	// 先制造失明态。
	setFeedMarketAge(engine, holdMonitorFeedBlindAge+10*time.Second)
	engine.observeHoldMonitor()
	// 行情恢复。
	setFeedMarketAge(engine, 1*time.Second)
	engine.observeHoldMonitor()

	if engine.feedBlindnessBlockOpens {
		t.Fatal("live market feed should clear the open block")
	}
	if !engine.feedMarketBlindSince.IsZero() {
		t.Fatal("live market feed should clear the blind-since timer")
	}
}

func TestBlindnessGuardDisabledIsNoop(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	engine.cfg.RiskConfig.FeedBlindnessGuardEnabled = false
	setFeedMarketAge(engine, holdMonitorFeedBlindAge+10*time.Second)

	engine.observeHoldMonitor()

	if engine.feedBlindnessBlockOpens || !engine.feedMarketBlindSince.IsZero() {
		t.Fatal("disabled guard must not touch blindness state (zero behavior change)")
	}
}

func TestBlindnessGuardClearsStateWhenFlat(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	setFeedMarketAge(engine, holdMonitorFeedBlindAge+10*time.Second)
	engine.observeHoldMonitor()
	// 敞口清空（撤离/平仓完成）。
	engine.exposureLocks = map[string]exposureLockState{}
	engine.observeHoldMonitor()

	if engine.feedBlindnessBlockOpens || !engine.feedMarketBlindSince.IsZero() {
		t.Fatal("flat exposure should clear blindness guard state")
	}
}

func TestBlindnessWithdrawTriggersAfterGrace(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	engine.feedMarketBlindSince = time.Now().UTC().Add(-(feedBlindnessWithdrawGrace + time.Second))

	reason, ok := engine.shouldTriggerFeedBlindnessWithdraw()
	if !ok {
		t.Fatal("persistent blindness beyond grace should trigger withdraw")
	}
	if reason == "" {
		t.Fatal("withdraw should carry a reason")
	}
	if !engine.panicFlattenInFlight {
		t.Fatal("withdraw should claim the global panic in-flight guard")
	}
}

func TestBlindnessWithdrawWaitsForGrace(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	engine.feedMarketBlindSince = time.Now().UTC().Add(-1 * time.Second)

	if _, ok := engine.shouldTriggerFeedBlindnessWithdraw(); ok {
		t.Fatal("blindness within grace window should not withdraw yet")
	}
}

func TestBlindnessWithdrawSkipsWhenFlatOrDisabled(t *testing.T) {
	// flat：无托管敞口。
	flat := newBlindnessGuardEngine(t)
	flat.exposureLocks = map[string]exposureLockState{}
	flat.feedMarketBlindSince = time.Now().UTC().Add(-time.Hour)
	if _, ok := flat.shouldTriggerFeedBlindnessWithdraw(); ok {
		t.Fatal("flat exposure should never withdraw")
	}

	// disabled：守卫关闭。
	disabled := newBlindnessGuardEngine(t)
	disabled.cfg.RiskConfig.FeedBlindnessGuardEnabled = false
	disabled.feedMarketBlindSince = time.Now().UTC().Add(-time.Hour)
	if _, ok := disabled.shouldTriggerFeedBlindnessWithdraw(); ok {
		t.Fatal("disabled guard should never withdraw")
	}
}

func TestBlindnessWithdrawSkipsWhenPanicInFlight(t *testing.T) {
	engine := newBlindnessGuardEngine(t)
	engine.feedMarketBlindSince = time.Now().UTC().Add(-time.Hour)
	engine.panicFlattenInFlight = true

	if _, ok := engine.shouldTriggerFeedBlindnessWithdraw(); ok {
		t.Fatal("withdraw should not double-dispatch while a panic flatten is in flight")
	}
}
