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

// TestSchedulerRevalidateWindow 钉死逐币续持复核窗口(R31,§16.4):[结算−RevalidateLead, 结算) 内为 due。
func TestSchedulerRevalidateWindow(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.PortfolioConfig.RevalidateLead = 5 * time.Minute
	s := newCycleScheduler(cfg)
	funding := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)

	if at := s.RevalidateAt(funding); !at.Equal(funding.Add(-5 * time.Minute)) {
		t.Fatalf("RevalidateAt=%v want %v", at, funding.Add(-5*time.Minute))
	}
	cases := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"窗口前", funding.Add(-6 * time.Minute), false},
		{"窗口开启边界", funding.Add(-5 * time.Minute), true},
		{"窗口内", funding.Add(-1 * time.Minute), true},
		{"结算时刻关闭", funding, false},
		{"结算后", funding.Add(time.Second), false},
	}
	for _, tc := range cases {
		if got := s.RevalidateDue(tc.now, funding); got != tc.want {
			t.Fatalf("%s: RevalidateDue=%v want %v", tc.name, got, tc.want)
		}
	}
	// 零 funding time 视为不 due(无时钟信息)。
	if s.RevalidateDue(funding, time.Time{}) {
		t.Fatal("零 nextFundingTime 不应为 due")
	}
}

// TestSchedulerNextPortfolioWakeMixedIntervals 钉死逐币时钟聚合:混合 8h/4h 间隔的持仓各按自身结算时钟驱动,
// 取「各持仓续持复核时刻」与「全局新开唤醒」的最早者;已落入复核窗口的持仓使主循环立即唤醒。
func TestSchedulerNextPortfolioWakeMixedIntervals(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.WakeMinute = 57
	cfg.StrategyConfig.WakeSecond = 0
	cfg.PortfolioConfig.RevalidateLead = 5 * time.Minute
	s := newCycleScheduler(cfg)

	now := time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC)
	globalWake := s.NextWake(now) // 10:57

	// 无持仓 → 退化为全局新开唤醒。
	if got := s.NextPortfolioWake(now, nil); !got.Equal(globalWake) {
		t.Fatalf("无持仓应退化为全局唤醒 %v, got %v", globalWake, got)
	}

	// 持仓 A 结算 12:00(8h 周期),复核 11:55;持仓 B 结算 10:50(4h 周期),复核 10:45。
	// 最早事件是 B 的复核 10:45(早于全局 10:57 与 A 的 11:55)。
	clocks := []symbolFundingClock{
		{Symbol: "AAAUSDT", NextFundingTime: time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)},
		{Symbol: "BBBUSDT", NextFundingTime: time.Date(2026, 6, 5, 10, 50, 0, 0, time.UTC)},
	}
	want := time.Date(2026, 6, 5, 10, 45, 0, 0, time.UTC)
	if got := s.NextPortfolioWake(now, clocks); !got.Equal(want) {
		t.Fatalf("NextPortfolioWake=%v want %v(B 的逐币复核最早)", got, want)
	}

	// 某持仓已落入复核窗口(now 在 [复核, 结算))→ 立即唤醒(返回 now)。
	dueNow := time.Date(2026, 6, 5, 10, 46, 0, 0, time.UTC)
	if got := s.NextPortfolioWake(dueNow, clocks); !got.Equal(dueNow) {
		t.Fatalf("已到复核窗口应立即唤醒 %v, got %v", dueNow, got)
	}
}
