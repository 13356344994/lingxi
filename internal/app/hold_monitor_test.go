package app

import (
	"io"
	"log"
	"sync"
	"testing"
	"time"
)

func TestClassifyFeedLiveness(t *testing.T) {
	thr := feedLivenessThresholds{DegradedAfter: 15 * time.Second, BlindAfter: 45 * time.Second}
	cases := []struct {
		name string
		age  time.Duration
		thr  feedLivenessThresholds
		want feedLivenessState
	}{
		{"never_seen", -1, thr, feedStateUnknown},
		{"fresh", 1 * time.Second, thr, feedStateLive},
		{"just_below_degraded", 14 * time.Second, thr, feedStateLive},
		{"at_degraded", 15 * time.Second, thr, feedStateDegraded},
		{"between", 30 * time.Second, thr, feedStateDegraded},
		{"at_blind", 45 * time.Second, thr, feedStateBlind},
		{"well_past_blind", 5 * time.Minute, thr, feedStateBlind},
		{"thresholds_disabled_stale", 10 * time.Minute, feedLivenessThresholds{}, feedStateLive},
		{"only_blind_enabled", 20 * time.Second, feedLivenessThresholds{BlindAfter: 45 * time.Second}, feedStateLive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFeedLiveness(tc.age, tc.thr); got != tc.want {
				t.Fatalf("classifyFeedLiveness(%v, %+v) = %s, want %s", tc.age, tc.thr, got, tc.want)
			}
		})
	}
}

func TestWorseFeedState(t *testing.T) {
	cases := []struct {
		a, b, want feedLivenessState
	}{
		{feedStateLive, feedStateLive, feedStateLive},
		{feedStateLive, feedStateUnknown, feedStateUnknown},
		{feedStateUnknown, feedStateDegraded, feedStateDegraded},
		{feedStateDegraded, feedStateBlind, feedStateBlind},
		{feedStateBlind, feedStateLive, feedStateBlind},
		{feedStateDegraded, feedStateUnknown, feedStateDegraded},
	}
	for _, tc := range cases {
		if got := worseFeedState(tc.a, tc.b); got != tc.want {
			t.Fatalf("worseFeedState(%s, %s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNoteFeedFrameAndAgeSnapshot(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	now := time.Now().UTC()

	// 从未收到帧的流应报告 unknown(-1)。
	ages := engine.feedAgeSnapshot(now, []feedKind{feedFuturesUser, feedSpotUser})
	if ages[feedFuturesUser] != -1 || ages[feedSpotUser] != -1 {
		t.Fatalf("unseen feeds should report -1, got %+v", ages)
	}

	engine.noteFeedFrame(feedFuturesUser)
	ages = engine.feedAgeSnapshot(time.Now().UTC(), []feedKind{feedFuturesUser, feedSpotUser})
	if ages[feedFuturesUser] < 0 {
		t.Fatalf("seen feed should report non-negative age, got %v", ages[feedFuturesUser])
	}
	if ages[feedFuturesUser] > time.Minute {
		t.Fatalf("freshly noted feed age unexpectedly large: %v", ages[feedFuturesUser])
	}
	if ages[feedSpotUser] != -1 {
		t.Fatalf("untouched feed should remain unknown, got %v", ages[feedSpotUser])
	}
}

func TestObserveHoldMonitorClearsSummaryWhenFlat(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	// 预置一个非空摘要，模拟上一次持有期的残留。
	engine.holdMonitorSummary = "managed=1 worst=degraded"
	engine.holdMonitorWorstState = feedStateDegraded

	engine.observeHoldMonitor()

	if engine.holdMonitorSummary != "" || engine.holdMonitorWorstState != "" {
		t.Fatalf("flat exposure should clear hold monitor summary, got summary=%q worst=%q", engine.holdMonitorSummary, engine.holdMonitorWorstState)
	}
}

func TestObserveHoldMonitorPopulatesSummaryWhileHolding(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	engine.exposureLocks = map[string]exposureLockState{"BTCUSDT": {}}
	// 两条用户流均刚收到帧 → 应判定 live。
	engine.noteFeedFrame(feedFuturesUser)
	engine.noteFeedFrame(feedSpotUser)

	engine.observeHoldMonitor()

	// live 不是"跃迁后需要记录"的坏分级，但初次从 "" → live 属于跃迁，应落摘要。
	if engine.holdMonitorWorstState != feedStateLive {
		t.Fatalf("holding with fresh feeds should be live, got %q", engine.holdMonitorWorstState)
	}
	if engine.holdMonitorSummary == "" {
		t.Fatal("holding should populate a non-empty hold monitor summary")
	}
}

// TestHoldMonitorConcurrentAccess 在 -race 下并发打点与观测，回归 feedLivenessMu 与 platformRiskMu 的锁纪律。
func TestHoldMonitorConcurrentAccess(t *testing.T) {
	engine := &FundingRateSniperEngine{logger: log.New(io.Discard, "", 0)}
	engine.exposureLocks = map[string]exposureLockState{"BTCUSDT": {}}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for _, kind := range []feedKind{feedFuturesUser, feedSpotUser, feedMarket} {
		wg.Add(1)
		go func(k feedKind) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					engine.noteFeedFrame(k)
				}
			}
		}(kind)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				engine.observeHoldMonitor()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
