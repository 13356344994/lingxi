package app

import (
	"testing"
	"time"
)

// TestFastXgapFunnel 今日漏斗计数器:触发/通过/拦截计数 + 北京日切清零。
func TestFastXgapFunnel(t *testing.T) {
	day1 := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC) // BJ 07-08 18:00
	fastXgapFunnelMu.Lock()
	fastXgapFunnel = fastXgapFunnelStats{Skips: map[string]int{}} // 测试隔离:清空共享状态
	fastXgapFunnelMu.Unlock()

	fastXgapFunnelNote(day1, "trigger", "")
	fastXgapFunnelNote(day1, "trigger", "")
	fastXgapFunnelNote(day1, "verdict", "cell_stale_gap")
	fastXgapFunnelNote(day1, "verdict", "")
	got := fastXgapFunnelSnapshot(day1)
	if got.Trigger != 2 || got.Pass != 1 || got.Skips["cell_stale_gap"] != 1 {
		t.Fatalf("计数错: trigger=%d pass=%d skips=%v", got.Trigger, got.Pass, got.Skips)
	}

	// 北京日切:UTC 16:00 = BJ 次日 00:00,快照应清零
	day2 := time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
	got2 := fastXgapFunnelSnapshot(day2)
	if got2.Trigger != 0 || got2.Pass != 0 || len(got2.Skips) != 0 {
		t.Fatalf("跨日未清零: %+v", got2)
	}
	// 新一天记账从零开始
	fastXgapFunnelNote(day2, "trigger", "")
	if got3 := fastXgapFunnelSnapshot(day2); got3.Trigger != 1 {
		t.Fatalf("跨日后计数错: %+v", got3)
	}
}

// TestMissionStrategyCard 一军流程卡:锁态与今日实数正确反映 config 真值与漏斗。
func TestMissionStrategyCard(t *testing.T) {
	cfg := OKXDirectionalConfig{
		FastXgapEntryGapBps: 40, FastXgapExitGapBps: 10, FastXgapMaxHoldSeconds: 600,
		FastXgapDisasterBps: 60, FastXgapCooldownSeconds: 1800,
		FastXgapCellGatesEnabled: true, FastXgapLiveEnabled: false, FastXgapBurstEjectEnabled: false,
	}
	fn := fastXgapFunnelStats{Trigger: 42, Pass: 3, Skips: map[string]int{"cell_stale_gap": 7}}
	card := missionBuildStrategyCard(cfg, fn)
	if card.Steps[3].State != "locked" { // ④下单必须显示锁死(live=false)
		t.Fatalf("live 关时下单步应 locked,得 %s", card.Steps[3].State)
	}
	if card.Exits[3].State != "locked" { // burst 弹射默认锁死
		t.Fatalf("弹射应 locked,得 %s", card.Exits[3].State)
	}
	if want := "今日触发 42 次"; card.Steps[0].Today != want {
		t.Fatalf("触发实数错: %q", card.Steps[0].Today)
	}
}
