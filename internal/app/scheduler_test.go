package app

import (
	"testing"
	"time"
)

func testScheduler() cycleScheduler {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.WakeMinute = 57
	cfg.StrategyConfig.WakeSecond = 0
	cfg.StrategyConfig.ScanWindow = 3 * time.Minute
	cfg.StrategyConfig.MonitorStopLead = 30 * time.Second
	cfg.ExecutionConfig.OpenPreparationLead = 10 * time.Second
	cfg.ExecutionConfig.PreOpenNoGoLead = 5 * time.Second
	cfg.ExecutionConfig.ExitDelayAfterFunding = 2 * time.Minute
	return newCycleScheduler(cfg)
}

func TestSchedulerNextWakeRollsToNextHour(t *testing.T) {
	s := testScheduler()
	loc := time.UTC
	// 当前已过 :57 → 下一次应跳到下个整点的 :57。
	now := time.Date(2026, 6, 5, 10, 58, 0, 0, loc)
	got := s.NextWake(now)
	want := time.Date(2026, 6, 5, 11, 57, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextWake=%v want %v", got, want)
	}
	// 当前未到 :57 → 本小时 :57。
	now2 := time.Date(2026, 6, 5, 10, 30, 0, 0, loc)
	if got2 := s.NextWake(now2); !got2.Equal(time.Date(2026, 6, 5, 10, 57, 0, 0, loc)) {
		t.Fatalf("NextWake(before)=%v", got2)
	}
}

func TestSchedulerScanEligibleWindow(t *testing.T) {
	s := testScheduler()
	now := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		funding time.Time
		want    bool
	}{
		{"已过结算", now.Add(-time.Second), false},
		{"恰好到点", now, false},
		{"窗口内", now.Add(2 * time.Minute), true},
		{"窗口边界", now.Add(3 * time.Minute), true},
		{"超出窗口", now.Add(3*time.Minute + time.Second), false},
	}
	for _, tc := range cases {
		if got := s.ScanEligible(now, tc.funding); got != tc.want {
			t.Fatalf("%s: ScanEligible=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSchedulerOpenTimingChain(t *testing.T) {
	s := testScheduler()
	funding := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	fire := s.OpenFireAt(funding)
	if !fire.Equal(funding.Add(-30 * time.Second)) {
		t.Fatalf("OpenFireAt=%v", fire)
	}
	if prep := s.OpenPreparationStartAt(funding); !prep.Equal(fire.Add(-10 * time.Second)) {
		t.Fatalf("OpenPreparationStartAt=%v", prep)
	}
	if nogo := s.OpenNoGoCheckAt(funding); !nogo.Equal(fire.Add(-5 * time.Second)) {
		t.Fatalf("OpenNoGoCheckAt=%v", nogo)
	}
	if dl := s.CloseDeadline(funding); !dl.Equal(funding.Add(2 * time.Minute)) {
		t.Fatalf("CloseDeadline=%v", dl)
	}
}

func TestSchedulerZeroLeadFallsBackToFire(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.MonitorStopLead = 30 * time.Second
	cfg.ExecutionConfig.OpenPreparationLead = 0
	cfg.ExecutionConfig.PreOpenNoGoLead = 0
	s := newCycleScheduler(cfg)
	funding := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	fire := s.OpenFireAt(funding)
	if prep := s.OpenPreparationStartAt(funding); !prep.Equal(fire) {
		t.Fatalf("lead<=0 时 prep 应等于 fire, got %v want %v", prep, fire)
	}
	if nogo := s.OpenNoGoCheckAt(funding); !nogo.Equal(fire) {
		t.Fatalf("lead<=0 时 nogo 应等于 fire, got %v want %v", nogo, fire)
	}
}

// TestSchedulerMatchesEngineTimingMethods 回归锁:scheduler 的时点必须与引擎薄包装方法逐位一致。
func TestSchedulerMatchesEngineTimingMethods(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: testSchedulerEngineConfig()}
	funding := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	targets := []*MonitoredTarget{{Candidate: FundingCandidate{NextFundingTime: funding}}}
	s := engine.scheduler()
	if got, want := engine.openFireAt(targets), s.OpenFireAt(funding); !got.Equal(want) {
		t.Fatalf("openFireAt 不一致: %v vs %v", got, want)
	}
	if got, want := engine.openPreparationStartAt(targets), s.OpenPreparationStartAt(funding); !got.Equal(want) {
		t.Fatalf("openPreparationStartAt 不一致: %v vs %v", got, want)
	}
	if got, want := engine.openNoGoCheckAt(targets), s.OpenNoGoCheckAt(funding); !got.Equal(want) {
		t.Fatalf("openNoGoCheckAt 不一致: %v vs %v", got, want)
	}
}

func testSchedulerEngineConfig() EngineConfig {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.MonitorStopLead = 30 * time.Second
	cfg.ExecutionConfig.OpenPreparationLead = 10 * time.Second
	cfg.ExecutionConfig.PreOpenNoGoLead = 5 * time.Second
	return cfg
}
