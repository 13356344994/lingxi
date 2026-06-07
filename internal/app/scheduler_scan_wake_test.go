package app

import (
	"testing"
	"time"
)

// TestNextScanWakeBasics 钉死新开唤醒模型:取最近未来结算 − ScanWindow;冷启动/已入窗 → now。
func TestNextScanWakeBasics(t *testing.T) {
	s := testScheduler() // ScanWindow=3m
	now := time.Date(2026, 6, 7, 3, 0, 0, 0, time.UTC)

	// 无已知结算 → 冷启动立即扫描。
	if got := s.NextScanWake(now, nil); !got.Equal(now) {
		t.Fatalf("冷启动应返回 now, got %v", got)
	}

	// 多个未来结算,取最早者 − 3m。最早 08:00 → 07:57。
	fundings := []time.Time{
		time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC), // 已过(早于 now)→ 忽略
	}
	want := time.Date(2026, 6, 7, 7, 57, 0, 0, time.UTC)
	if got := s.NextScanWake(now, fundings); !got.Equal(want) {
		t.Fatalf("NextScanWake=%v want %v", got, want)
	}

	// 已落在窗口内(结算就在 2m 后 < ScanWindow)→ 立即唤醒。
	inWindow := time.Date(2026, 6, 7, 7, 58, 30, 0, time.UTC)
	if got := s.NextScanWake(inWindow, fundings); !got.Equal(inWindow) {
		t.Fatalf("入窗应返回 now, got %v", got)
	}
}

// TestNextMarketWakeUnifiesScanAndRevalidate 校验统一唤醒点:取「新开扫描」与「逐币复核(:55)」最早者,
// 且完全不依赖 WakeMinute(把 WakeMinute 调成任意值都不影响结果)。
func TestNextMarketWakeUnifiesScanAndRevalidate(t *testing.T) {
	s := testScheduler()
	now := time.Date(2026, 6, 7, 3, 0, 0, 0, time.UTC)

	marketFundings := []time.Time{time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)} // 扫描唤醒 07:57
	// 一个持仓 04:00 结算,RevalidateLead 默认 → 复核早于 07:57,应被选中。
	clocks := []symbolFundingClock{{Symbol: "ETHUSDT", NextFundingTime: time.Date(2026, 6, 7, 4, 0, 0, 0, time.UTC)}}

	got := s.NextMarketWake(now, clocks, marketFundings)
	wantRevalidate := s.RevalidateAt(clocks[0].NextFundingTime)
	if !got.Equal(wantRevalidate) {
		t.Fatalf("应取更早的逐币复核 %v, got %v", wantRevalidate, got)
	}

	// 持仓已落入复核窗口 → 立即唤醒。
	dueNow := s.RevalidateAt(clocks[0].NextFundingTime).Add(time.Second)
	if got := s.NextMarketWake(dueNow, clocks, marketFundings); !got.Equal(dueNow) {
		t.Fatalf("复核窗口内应立即唤醒, got %v", got)
	}
}

// TestNextMarketWakeIndependentOfWakeMinute 验收 ③:调度结果与全局 WakeMinute 取值无关(钉死解绑)。
func TestNextMarketWakeIndependentOfWakeMinute(t *testing.T) {
	now := time.Date(2026, 6, 7, 3, 0, 0, 0, time.UTC)
	marketFundings := []time.Time{time.Date(2026, 6, 7, 8, 0, 0, 0, time.UTC)}

	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.ScanWindow = 3 * time.Minute
	var results []time.Time
	for _, wm := range []int{0, 13, 57, 59} {
		cfg.StrategyConfig.WakeMinute = wm
		s := newCycleScheduler(cfg)
		results = append(results, s.NextMarketWake(now, nil, marketFundings))
	}
	for i := 1; i < len(results); i++ {
		if !results[i].Equal(results[0]) {
			t.Fatalf("NextMarketWake 不应随 WakeMinute 变化: %v vs %v", results[i], results[0])
		}
	}
}

// TestAnyRevalidationDue 校验退避守卫的判别:有持仓落入复核窗口 → true(必须立即唤醒,不退避);
// 否则 false(立即唤醒纯因时钟不可用,可施加重试间隔)。
func TestAnyRevalidationDue(t *testing.T) {
	s := testScheduler()
	now := time.Date(2026, 6, 7, 7, 0, 0, 0, time.UTC)

	// 无持仓 → 不 due。
	if anyRevalidationDue(s, now, nil) {
		t.Fatalf("空持仓不应 due")
	}
	// 持仓结算远在未来(复核窗口未开)→ 不 due。
	far := []symbolFundingClock{{Symbol: "BTCUSDT", NextFundingTime: time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)}}
	if anyRevalidationDue(s, now, far) {
		t.Fatalf("远期持仓不应 due")
	}
	// 持仓已落入复核窗口(结算在 RevalidateLead 之内、未结算)→ due。
	soon := s.RevalidateAt(now.Add(time.Hour)).Add(time.Second)
	due := []symbolFundingClock{{Symbol: "BTCUSDT", NextFundingTime: now.Add(time.Hour)}}
	if !anyRevalidationDue(s, soon, due) {
		t.Fatalf("复核窗口内持仓应 due")
	}
}

// TestScanWakeCaptureEquivalenceOverDay 改造前后「对照测试」:在币安固定 8h 结算(00/08/16 UTC)的 24h 跨度内,
// 新模型(逐结算唤醒)捕获的「可扫描机会」集合与旧模型(每小时 :57 扫一遍)完全一致,但唤醒次数大幅下降。
func TestScanWakeCaptureEquivalenceOverDay(t *testing.T) {
	s := testScheduler() // ScanWindow=3m, WakeMinute=57
	day := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)

	// 币安固定 8h 结算边界(全市场同步)。
	fundingBoundaries := []time.Time{
		day.Add(8 * time.Hour),  // 08:00
		day.Add(16 * time.Hour), // 16:00
		day.Add(24 * time.Hour), // 次日 00:00
	}
	// 旧模型:从 00:00 起每小时 :57 唤醒一次,记录哪些唤醒「能扫到机会」(有结算落在 (now, now+ScanWindow])。
	oldWakes := 0
	oldCaptured := map[time.Time]bool{}
	for h := 0; h < 24; h++ {
		wake := day.Add(time.Duration(h)*time.Hour + 57*time.Minute)
		oldWakes++
		for _, f := range fundingBoundaries {
			rem := f.Sub(wake)
			if rem > 0 && rem <= s.scanWindow {
				oldCaptured[f] = true
			}
		}
	}

	// 新模型:从 day 起,反复 NextScanWake 推进;每次唤醒记录捕获的结算。
	newWakes := 0
	newCaptured := map[time.Time]bool{}
	now := day
	end := day.Add(24 * time.Hour)
	for now.Before(end) {
		wake := s.NextScanWake(now, fundingBoundaries)
		if !wake.After(now) {
			// 已入窗:捕获当前最近结算,然后跨过它继续。
			for _, f := range fundingBoundaries {
				rem := f.Sub(wake)
				if rem > 0 && rem <= s.scanWindow {
					newCaptured[f] = true
				}
			}
			now = wake.Add(s.scanWindow + time.Minute) // 跨过该窗口
			continue
		}
		newWakes++
		for _, f := range fundingBoundaries {
			rem := f.Sub(wake)
			if rem > 0 && rem <= s.scanWindow {
				newCaptured[f] = true
			}
		}
		now = wake.Add(time.Second) // 推进到唤醒点之后,下一轮算下一个结算
	}

	// ① 机会捕获等价:两模型捕获的结算边界集合相同(都应捕获 08:00/16:00/次日 00:00)。
	if len(newCaptured) != len(fundingBoundaries) || len(oldCaptured) != len(fundingBoundaries) {
		t.Fatalf("捕获数不符: old=%d new=%d want=%d", len(oldCaptured), len(newCaptured), len(fundingBoundaries))
	}
	for f := range oldCaptured {
		if !newCaptured[f] {
			t.Fatalf("新模型漏捕获结算 %v", f)
		}
	}
	// ② 唤醒分布:新模型唤醒次数远少于旧模型(旧 24 次/天,新 ≈ 结算次数)。
	if newWakes >= oldWakes {
		t.Fatalf("新模型唤醒次数应显著下降: new=%d old=%d", newWakes, oldWakes)
	}
	if newWakes > len(fundingBoundaries) {
		t.Fatalf("新模型唤醒次数应≈结算次数: new=%d boundaries=%d", newWakes, len(fundingBoundaries))
	}
}
