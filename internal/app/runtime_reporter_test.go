package app

import (
	"sync"
	"testing"
	"time"
)

func TestRuntimeReporterEmitWithReasonTransitionsAndPublishesSnapshot(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                       DefaultEngineConfig(),
		currentStage:              StageReady,
		portfolioManager:          portfolioManager{symbolCooldowns: make(map[string]symbolCooldownState)},
		runID:                     "live-20260529T120000Z-11112222",
		currentCycleID:            "live-20260529T120000Z-11112222-cycle-33334444",
		protectedIOCAttempts:      6,
		protectedIOCCanceled:      1,
		protectedIOCExpired:       1,
		protectedIOCPartial:       2,
		protectedIOCUnfilledSum:   1.8,
		lastManagedDrawdownAt:     time.Date(2026, 5, 31, 1, 10, 0, 0, time.UTC),
		lastManagedDrawdownAction: "symbol_cut_completed | BTCUSDT | alerts=flattened",
		managedDrawdownSummary:    "tracked=2 managedLoss=65.000000 trigger=50.000000 worst=BTCUSDT upnl=-60.000000 qty=1.00000000",
	}
	reporter := newRuntimeReporter(engine)

	var snapshot LiveRuntimeSnapshot
	engine.liveStatusHook = func(s LiveRuntimeSnapshot) {
		snapshot = s
	}

	reporter.EmitWithReason(StageScan, 3, "开始扫描")

	if engine.currentStage != StageScan {
		t.Fatalf("expected engine stage scan, got %s", engine.currentStage)
	}
	if snapshot.Stage != StageScan {
		t.Fatalf("expected snapshot stage scan, got %s", snapshot.Stage)
	}
	if snapshot.StageReason != "开始扫描" {
		t.Fatalf("unexpected stage reason: %s", snapshot.StageReason)
	}
}

// TestRuntimeReporterEmitNoRaceWithConcurrentLockedWriters 钉死 P0 数据竞争修复:Emit(自加锁) 必须与持有
// platformRiskMu 的并发写(模拟后台 drawdown/hold/margin 监控对 activeOpenHedges/cooldowns 的修改)安全并发。
// 需以 `go test -race` 运行才能暴露回归——未加锁的旧 EmitLocked 会在此处触发 data race / concurrent map iteration。
func TestRuntimeReporterEmitNoRaceWithConcurrentLockedWriters(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:              DefaultEngineConfig(),
		currentStage:     StageReady,
		portfolioManager: portfolioManager{symbolCooldowns: make(map[string]symbolCooldownState)},
		runID:            "live-race",
		currentCycleID:   "live-race-cycle",
	}
	reporter := newRuntimeReporter(engine)
	engine.liveStatusHook = func(LiveRuntimeSnapshot) {}

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	// 写者:持锁修改受保护状态(含 map 写、切片重建),模拟后台监控的并发 mutate。
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			engine.platformRiskMu.Lock()
			engine.activeOpenHedges = append(engine.activeOpenHedges, OpenHedge{
				Target:   FundingCandidate{Symbol: "BTCUSDT"},
				HedgeQty: float64(i),
			})
			engine.currentPositions = append(engine.currentPositions, "BTCUSDT")
			engine.symbolCooldowns["BTCUSDT"] = symbolCooldownState{}
			engine.platformRiskMu.Unlock()
		}
	}()

	// 读者:未持锁调用 Emit(应自加锁),读取同一批受保护状态。
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			reporter.Emit(EngineStage(""))
		}
	}()

	wg.Wait()
}
