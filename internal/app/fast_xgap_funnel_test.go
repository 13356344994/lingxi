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
		// 窗内 rest3(棘轮版 v3 主口径,07-09 起):计入基准;peak≥5 计吐回
		{Src: "rest3", T0: after, NetBps: 3, GrossBps: 15, PeakBps: 10},
		{Src: "rest3", T0: after, NetBps: -8, GrossBps: 4, PeakBps: 6},
		{Src: "rest3", T0: after, NetBps: -23, GrossBps: -11, PeakBps: 2},
		// 窗前 rest3:不计基准
		{Src: "rest3", T0: before, NetBps: 50},
		// 窗内 rest(原版对照):计入 Orig,trail 配对
		{Src: "rest", T0: after, NetBps: 3, GrossBps: 15, PeakBps: 10, TrailFired: true, TrailExitBps: 10},
		// 窗前 rest:不计对照,但 trail 标注仍配对(标注天然封样)
		{Src: "rest", T0: before, NetBps: 20, GrossBps: 32, TrailFired: true, TrailExitBps: 15},
		// det 行:不计基准,trail 配对进 det 参考
		{Src: "det", T0: after, NetBps: 0, GrossBps: 12, TrailFired: true, TrailExitBps: 6},
		// fire 模块:全部排除
		{Src: "rest", Module: "fire", T0: after, NetBps: 100, TrailFired: true, TrailExitBps: 200},
		// 弹射配对(rest 行标注):burst 时刻 +9,最终 gross -11 → 省 20
		{Src: "rest", T0: after, NetBps: -23, GrossBps: -11, BurstDuringHold: true, RetAtBurstBps: 9},
	}
	b := missionBaselineFromRows(rows, seal)
	if b.N != 3 { // 窗内 rest2 三行
		t.Fatalf("基准 n=%d want 3", b.N)
	}
	if b.OrigN != 2 { // 窗内 rest 两行(含弹射行)
		t.Fatalf("原版对照 n=%d want 2", b.OrigN)
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

// TestFastExamVerdict 自动阅卷纯函数:到线判定/过门/单币主导作废/刮痕中性化。
func TestFastExamVerdict(t *testing.T) {
	ex := fastExamSpec{MinN: 3, MinMed: 0, MinPF: 1.3, MinWin: 0.45, MaxTop: 0.5,
		Seal: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		Rows: func(r fastLedgerRow) bool { return r.Src == "rest2" }}
	after := ex.Seal.Add(time.Hour)
	mk := func(sym string, net float64) fastLedgerRow {
		return fastLedgerRow{Src: "rest2", Symbol: sym, T0: after, NetBps: net}
	}
	// 未到线
	st := fastExamEvaluate([]fastLedgerRow{mk("A", 10), mk("B", 8)}, ex)
	if v := fastExamVerdict(st, ex); v != "" {
		t.Fatalf("n<3 应未到线,得 %q", v)
	}
	// 过门:三笔双正且分散
	st = fastExamEvaluate([]fastLedgerRow{mk("A", 10), mk("B", 8), mk("C", -4)}, ex)
	if v := fastExamVerdict(st, ex); v != "pass" {
		t.Fatalf("应过门,得 %q(st=%+v)", v, st)
	}
	// 单币主导作废(A 占 2/3 > 0.5)
	st = fastExamEvaluate([]fastLedgerRow{mk("A", 10), mk("A", 8), mk("C", -4)}, ex)
	if v := fastExamVerdict(st, ex); v != "fail" {
		t.Fatalf("单币主导应 fail,得 %q", v)
	}
	// 封样前的行不计
	old := mk("D", 100)
	old.T0 = ex.Seal.Add(-time.Hour)
	st = fastExamEvaluate([]fastLedgerRow{old, mk("A", 10), mk("B", 8)}, ex)
	if st.N != 2 {
		t.Fatalf("封样前行不应计入,n=%d", st.N)
	}
	// 刮痕中性化:|net|≤5 不进胜率分母
	st = fastExamEvaluate([]fastLedgerRow{mk("A", 10), mk("B", 3), mk("C", -2), mk("D", 8)}, ex)
	if st.WinNeu != 1.0 { // 有效样本=10/8 两笔全胜
		t.Fatalf("中性化胜率应 1.0,得 %.2f", st.WinNeu)
	}
}

// TestFastProbeMulti 多探针纯函数:csv 名单判定 + 老格式状态文件迁移。
func TestFastProbeMulti(t *testing.T) {
	if !fastProbeHas("xgap_rev,tape_burst", "tape_burst") || !fastProbeHas("xgap_rev", "xgap_rev") {
		t.Fatal("csv 名单应命中")
	}
	if fastProbeHas("xgap_rev,tape_burst", "fade_exhaustion") || fastProbeHas("", "xgap_rev") || fastProbeHas("xgap_rev", "") {
		t.Fatal("名单外/空值不应命中")
	}
	// 持证闸吃 csv 探针名单
	if r := fastCertGate("tape_burst", "xgap_rev,tape_burst", ""); r != "" {
		t.Fatalf("csv 第二探针应放行,得 %q", r)
	}
}

// TestSanitizeRuntimeParams 运行时参数硬顶(防手滑:名义≤50/杠杆≤10/负数归零)。
func TestSanitizeRuntimeParams(t *testing.T) {
	p := sanitizeRuntimeParams(RuntimeParams{PerTradeNotionalUSDT: 5000, Leverage: 100, MaxDailyLossUSDT: -3})
	if p.PerTradeNotionalUSDT != 50 || p.Leverage != 10 || p.MaxDailyLossUSDT != 0 {
		t.Fatalf("硬顶失效: %+v", p)
	}
	q := sanitizeRuntimeParams(RuntimeParams{PerTradeNotionalUSDT: 7.5, Leverage: 3, MaxDailyLossUSDT: 20})
	if q.PerTradeNotionalUSDT != 7.5 || q.Leverage != 3 || q.MaxDailyLossUSDT != 20 {
		t.Fatalf("正常值被误裁: %+v", q)
	}
}

// TestFastAutoProbe 全自动升级管线:过门自动接入探针(去重/并发上限/合并判定)。
func TestFastAutoProbe(t *testing.T) {
	e := newFastV3TestEngine(t)
	fastAutoProbeMu.Lock()
	fastAutoProbeCache = fastAutoProbeState{}
	fastAutoProbeAt = time.Time{}
	fastAutoProbeMu.Unlock()
	if !e.fastAutoProbeAdmit("firework_pure") {
		t.Fatal("首次过门应接入")
	}
	if e.fastAutoProbeAdmit("firework_pure") {
		t.Fatal("重复接入应拒(去重)")
	}
	if !e.fastAutoProbeAdmit("src_b") {
		t.Fatal("第二源应接入(并发上限2)")
	}
	if e.fastAutoProbeAdmit("src_c") {
		t.Fatal("第三源应被并发上限拦下")
	}
	if !e.fastProbeAllowed("firework_pure") || e.fastProbeAllowed("src_c") {
		t.Fatal("合并判定错:自动名单内应放行,名单外应拒")
	}
	// 自动身份走通来源闸+持证闸(live 开关全关时 allowAuto 放行)
	ev := &fastExecShadowEvent{Symbol: "FW-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "firework_pure", FastCandidateSide: "short", PondOK: true, PondSamples: 9999}
	e.cfg.FastLiveEnabled = true
	if r := e.fastLiveOpen(nil, ev); r == "record_only_source" || r == "no_cert" {
		t.Fatalf("自动探针身份应过来源闸与持证闸,得 %s", r)
	}
}

// TestFastFireworkPureSkip 烟花枪三特征闸(冻结切点 3/0.05/0;改=作弊)。
func TestFastFireworkPureSkip(t *testing.T) {
	// 全过:非惯犯+完全力竭+无爆仓
	if s := fastFireworkPureSkip(2, 3, 100, 0); s != "" {
		t.Fatalf("三特征全过应放行,得 %q", s)
	}
	if s := fastFireworkPureSkip(3, 3, 100, 0); s != "fw_habitual" {
		t.Fatalf("惯犯应拒,得 %q", s)
	}
	if s := fastFireworkPureSkip(0, 6, 100, 0); s != "fw_not_exhausted" { // 6 >= 5%×100
		t.Fatalf("未完全力竭应拒,得 %q", s)
	}
	if s := fastFireworkPureSkip(0, 3, 100, 1); s != "fw_squeeze_flow" {
		t.Fatalf("有本币爆仓应拒,得 %q", s)
	}
}

// TestFastCertGate 持证闸纯函数:探针放行/持证放行/无证硬拒。
func TestFastCertGate(t *testing.T) {
	// 无探针无证:一律 no_cert(把"误开 live+未设探针"的无上限实弹封死)
	if r := fastCertGate("xgap_rev", "", ""); r != "no_cert" {
		t.Fatalf("无证应拒,得 %q", r)
	}
	// 探针身份:source==probe_source=用户明示批准
	if r := fastCertGate("xgap_rev", "xgap_rev", ""); r != "" {
		t.Fatalf("探针应放行,得 %q", r)
	}
	// 探针只豁免自己的 source
	if r := fastCertGate("tape_burst", "xgap_rev", ""); r != "no_cert" {
		t.Fatalf("非探针 source 应拒,得 %q", r)
	}
	// 持证名单放行
	if r := fastCertGate("xgap_rev", "", "xgap_rev,tape_burst"); r != "" {
		t.Fatalf("持证应放行,得 %q", r)
	}
	// scorer 空 source 按 scorer 键查证
	if r := fastCertGate("", "", ""); r != "no_cert" {
		t.Fatalf("scorer 无证应拒,得 %q", r)
	}
	if r := fastCertGate("", "", "scorer"); r != "" {
		t.Fatalf("scorer 持证应放行,得 %q", r)
	}
}

// TestFastLedgerExitV2 改良版出场纯函数:弹射>灾难>棘轮>收敛>到点。
func TestFastLedgerExitV2(t *testing.T) {
	// 弹射最优先(burst 出现即离场,哪怕价差已收敛)
	if e := fastLedgerExitV2(5, 12, true, -5, true, 30, 10, 60, 600); e != "burst_eject" {
		t.Fatalf("burst 应弹射,得 %s", e)
	}
	// 灾难
	if e := fastLedgerExitV2(-61, 0, false, -50, true, 30, 10, 60, 600); e != "disaster" {
		t.Fatalf("穿灾难线应 disaster,得 %s", e)
	}
	// 棘轮:峰值 12 回吐到 +3(回吐 9≥8)→ trail
	if e := fastLedgerExitV2(3, 12, false, -30, true, 30, 10, 60, 600); e != "trail" {
		t.Fatalf("峰12回吐9应 trail,得 %s", e)
	}
	// 峰值未到激活线(9<10)不触发棘轮 → 继续持有
	if e := fastLedgerExitV2(0, 9, false, -30, true, 30, 10, 60, 600); e != "" {
		t.Fatalf("峰9不应触发任何出场,得 %s", e)
	}
	// 收敛
	if e := fastLedgerExitV2(4, 9, false, -8, true, 30, 10, 60, 600); e != "converged" {
		t.Fatalf("价差-8≥-10应收敛离场,得 %s", e)
	}
	// 到点
	if e := fastLedgerExitV2(1, 5, false, -30, true, 601, 10, 60, 600); e != "max_hold" {
		t.Fatalf("601s应到点离场,得 %s", e)
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
	if card.Exits[0].State != "on" { // 弹射:影子主口径真跑(实枪仍由 live 闸管)
		t.Fatalf("弹射影子应 on,得 %s", card.Exits[0].State)
	}
	if want := "今日触发 42 次"; card.Steps[0].Today != want {
		t.Fatalf("触发实数错: %q", card.Steps[0].Today)
	}
}
