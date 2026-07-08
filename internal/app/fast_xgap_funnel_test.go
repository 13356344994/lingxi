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

// TestMissionBaselineFromRows 当前策略基准纯函数:窗口过滤/模块过滤/配对杠杆算术。
func TestMissionBaselineFromRows(t *testing.T) {
	seal := time.Date(2026, 7, 8, 4, 34, 0, 0, time.UTC)
	before := seal.Add(-time.Hour)
	after := seal.Add(time.Hour)
	rows := []fastLedgerRow{
		// 窗内 rest:计入基准;peak≥5 计吐回;trail 配对 +7
		{Src: "rest", T0: after, NetBps: 3, GrossBps: 15, PeakBps: 10, TrailFired: true, TrailExitBps: 10},
		// 窗内 rest 亏单:peak≥5 且收负 → giveback_neg
		{Src: "rest", T0: after, NetBps: -8, GrossBps: 4, PeakBps: 6},
		// 窗前 rest:不计基准,但 trail 标注仍配对(标注天然封样)
		{Src: "rest", T0: before, NetBps: 20, GrossBps: 32, TrailFired: true, TrailExitBps: 15},
		// det 行:不计基准,trail 配对进 det 参考
		{Src: "det", T0: after, NetBps: 0, GrossBps: 12, TrailFired: true, TrailExitBps: 6},
		// fire 模块:全部排除
		{Src: "rest", Module: "fire", T0: after, NetBps: 100, TrailFired: true, TrailExitBps: 200},
		// 弹射配对:burst 时刻 +9,最终 gross -11 → 省 20
		{Src: "rest", T0: after, NetBps: -23, GrossBps: -11, BurstDuringHold: true, RetAtBurstBps: 9},
	}
	b := missionBaselineFromRows(rows, seal)
	if b.N != 3 { // 窗内 rest 三行(含弹射那行)
		t.Fatalf("基准 n=%d want 3", b.N)
	}
	if b.GivebackN != 2 || b.GivebackNegN != 1 {
		t.Fatalf("吐回税计数错: %d/%d", b.GivebackN, b.GivebackNegN)
	}
	// 棘轮配对:rest 两笔 delta=+7、-5(窗前行也配对)→ 中位 +1
	if b.RatchetRestN != 2 || b.RatchetRestMedBps != 1 {
		t.Fatalf("棘轮 rest 配对错: n=%d med=%.1f", b.RatchetRestN, b.RatchetRestMedBps)
	}
	if b.RatchetDetN != 1 || b.RatchetDetMedBps != 6 {
		t.Fatalf("棘轮 det 配对错: n=%d med=%.1f", b.RatchetDetN, b.RatchetDetMedBps)
	}
	if b.EjectN != 1 || b.EjectSaveMedBps != 20 {
		t.Fatalf("弹射配对错: n=%d save=%.1f", b.EjectN, b.EjectSaveMedBps)
	}
}

// TestExamKeysECurrent e_current 键:三闸部署后 rest 行才归属"当前版本 E"(混窗根治)。
func TestExamKeysECurrent(t *testing.T) {
	after := missionSealCellGates.Add(time.Hour)
	before := missionSealCellGates.Add(-time.Hour)
	has := func(keys []string, k string) bool {
		for _, x := range keys {
			if x == k {
				return true
			}
		}
		return false
	}
	if !has(examKeysForLedgerRow("rest", 0, after), "e_current") {
		t.Fatal("三闸后 rest 行应归属 e_current")
	}
	if has(examKeysForLedgerRow("rest", 0, before), "e_current") {
		t.Fatal("三闸前 rest 行不许混入 e_current")
	}
	if has(examKeysForLedgerRow("det", 0, after), "e_current") {
		t.Fatal("det 行不属于可执行口径")
	}
	// 考试键不受时间断代影响(封样过滤在门槛表侧)
	bits := pbFresh | pbDutyXgap | pbResidual2040
	if keys := examKeysForLedgerRow("rest", bits, before); !has(keys, "exam_e_ticket") {
		t.Fatalf("bits 齐全应归属 exam_e_ticket,got %v", keys)
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
