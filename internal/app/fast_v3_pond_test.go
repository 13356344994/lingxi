package app

// v3 蓝图单测:①池塘质量(滚动中位/热身fail-open/过滤闸)②接型候选(liq_catch/fade_exhaustion/实盘硬闸)。

import (
	"context"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

func newFastV3TestEngine(t *testing.T) *OKXDirectionalEngine {
	t.Helper()
	cfg := core.DefaultEngineConfig().OKXDirectional
	cfg.FastAltCandidatesEnabled = true
	return &OKXDirectionalEngine{
		cfg:       cfg,
		venueName: "test_v3_" + t.Name(),
		recordDir: t.TempDir(),
		logger:    log.New(io.Discard, "", 0),
		nowFn:     time.Now,
	}
}

func TestFastPond_MedianAndEval(t *testing.T) {
	key := "test_v3_pond|X-USDT"
	now := time.Now().UTC()
	// 奇数个样本:能量中位=5,深度中位=3000
	for i, en := range []float64{1, 5, 20} {
		fastPondRecord(key, now.Add(time.Duration(i)*time.Second), en, []float64{1000, 3000, 9000}[i])
	}
	en, dp, n := fastPondEval(key)
	if n != 3 || en != 5 || dp != 3000 {
		t.Fatalf("中位数错: en=%v dp=%v n=%d", en, dp, n)
	}
	// 超过 2h 窗口的旧样本被剔除
	fastPondRecord(key, now.Add(fastPondWindowSec*time.Second+10*time.Second), 7, 7000)
	_, _, n2 := fastPondEval(key)
	if n2 != 1 {
		t.Fatalf("窗口剔除失败: n=%d", n2)
	}
}

func TestFastPond_WarmupFailOpenAndFilterGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastPondFilterEnabled = true
	e.cfg.FastPondMinEnergyBps = 8
	e.cfg.FastScorerLiveEnabled = true // 本测试测池塘闸,需先过 v4 scorer 来源闸
	// 热身期(样本不足):pond_ok 应为 true,不误杀
	ev := &fastExecShadowEvent{Symbol: "Y-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		FastCandidateSide: "long", FastCandidateScore: 0.99, PondSamples: 10, PondOK: true}
	if r := e.fastLiveOpen(context.Background(), ev); r == "pond_reject" {
		t.Fatalf("热身期不应 pond_reject,得到 %s", r)
	}
	// 成熟样本 + 不合格池:应拒
	ev2 := *ev
	ev2.PondSamples = fastPondWarmupSamples + 1
	ev2.PondOK = false
	if r := e.fastLiveOpen(context.Background(), &ev2); r != "pond_reject" {
		t.Fatalf("死水池应 pond_reject,得到 %s", r)
	}
}

func TestFastPond_RestoreFromShadowFile(t *testing.T) {
	e := newFastV3TestEngine(t)
	now := time.Now().UTC()
	path := filepath.Join(e.recordDir, fastExecShadowFile)
	var lines []string
	// 200 条有效样本(冲量非零)+ 1 条过期 + 1 条接型候选 + 1 条冲量全零(应全被跳过)
	for i := 0; i < 200; i++ {
		lines = append(lines, `{"recorded_at":"`+now.Add(-time.Duration(i)*10*time.Second).Format(time.RFC3339Nano)+`","symbol":"R-USDT","mid_impulse_30s_bps":9,"mid_impulse_15s_bps":4,"depth_notional_10bps":8000}`)
	}
	lines = append(lines,
		`{"recorded_at":"`+now.Add(-3*time.Hour).Format(time.RFC3339Nano)+`","symbol":"R-USDT","mid_impulse_30s_bps":99,"mid_impulse_15s_bps":9,"depth_notional_10bps":1}`,
		`{"recorded_at":"`+now.Format(time.RFC3339Nano)+`","symbol":"R-USDT","candidate_source":"fade_exhaustion","mid_impulse_30s_bps":50,"mid_impulse_15s_bps":1,"depth_notional_10bps":1}`,
		`{"recorded_at":"`+now.Format(time.RFC3339Nano)+`","symbol":"R-USDT","mid_impulse_30s_bps":0,"mid_impulse_15s_bps":0,"mid_impulse_60s_bps":0,"depth_notional_10bps":1}`)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.fastPondRestore()
	en, dp, n := fastPondEval(e.venueName + "|R-USDT")
	if n != 200 || en != 9 || dp != 8000 {
		t.Fatalf("回放错: en=%v dp=%v n=%d(want 200条 en=9 dp=8000;过期/候选/全零应被跳过)", en, dp, n)
	}
}

func TestFastAlt_LiqCatchCandidate(t *testing.T) {
	e := newFastV3TestEngine(t)
	now := time.Now().UTC()
	sym := "LIQ-USDT"
	fastNoteLiq(sym, "long", 3, now.Add(-10*time.Second)) // 多头爆=被迫卖 → 接多
	fastNoteLiq(sym, "long", 7, now.Add(-5*time.Second))
	base := &fastExecShadowEvent{Symbol: sym, Mid: 1, SnapshotID: "s1"}
	alts := e.buildFastAltCandidates(base, now)
	if len(alts) != 1 {
		t.Fatalf("期望 1 条 liq_catch,得到 %d", len(alts))
	}
	a := alts[0]
	if a.CandidateSource != "liq_catch" || a.FastCandidateSide != "long" || a.SnapshotID != "s1|liq_catch" {
		t.Fatalf("liq_catch 字段错: %+v", a)
	}
	// 两向平票不出候选
	sym2 := "TIE-USDT"
	fastNoteLiq(sym2, "long", 1, now)
	fastNoteLiq(sym2, "short", 1, now)
	if got := e.buildFastAltCandidates(&fastExecShadowEvent{Symbol: sym2, Mid: 1}, now); len(got) != 0 {
		t.Fatalf("平票不应出候选,得到 %d", len(got))
	}
	// 超 60s 窗口不出候选
	sym3 := "OLD-USDT"
	fastNoteLiq(sym3, "short", 1, now.Add(-90*time.Second))
	if got := e.buildFastAltCandidates(&fastExecShadowEvent{Symbol: sym3, Mid: 1}, now); len(got) != 0 {
		t.Fatalf("过期强平不应出候选,得到 %d", len(got))
	}
}

func TestFastAlt_FadeExhaustionCandidate(t *testing.T) {
	e := newFastV3TestEngine(t)
	now := time.Now().UTC()
	// 上冲 10bps 且 15s 已减速(2 < 0.4*10)→ 接空
	base := &fastExecShadowEvent{Symbol: "F-USDT", Mid: 1, SnapshotID: "s2",
		MidImpulse30sBps: 10, MidImpulse15sBps: 2}
	alts := e.buildFastAltCandidates(base, now)
	if len(alts) != 1 || alts[0].CandidateSource != "fade_exhaustion" || alts[0].FastCandidateSide != "short" {
		t.Fatalf("fade 候选错: %+v", alts)
	}
	// 15s 仍在加速(8 > 0.4*10)→ 不接(还没力竭)
	base2 := &fastExecShadowEvent{Symbol: "F2-USDT", Mid: 1, MidImpulse30sBps: 10, MidImpulse15sBps: 8}
	if got := e.buildFastAltCandidates(base2, now); len(got) != 0 {
		t.Fatalf("加速中不应出 fade 候选")
	}
	// 下冲力竭 → 接多
	base3 := &fastExecShadowEvent{Symbol: "F3-USDT", Mid: 1, MidImpulse30sBps: -12, MidImpulse15sBps: 1}
	got := e.buildFastAltCandidates(base3, now)
	if len(got) != 1 || got[0].FastCandidateSide != "long" {
		t.Fatalf("下冲 fade 应接多: %+v", got)
	}
}

func TestMinVenueTradableQty(t *testing.T) {
	// 张本位:LAB 1张=10币 minSz=0.1张 → 最小币量=1;5U名义@8.01 量化到0张,但最小票=1币≈8U ≤ 2×5U 可开
	instr := venue.Instrument{ContractSize: 10, StepSize: 0.1}
	if q := minVenueTradableQty(instr, true, 8.01); q != 1 {
		t.Fatalf("LAB 最小票 want 1 币, got %v", q)
	}
	// 币本位:step=0.01
	if q := minVenueTradableQty(venue.Instrument{StepSize: 0.01}, false, 100); q != 0.01 {
		t.Fatalf("币本位最小票 want 0.01, got %v", q)
	}
	// MinNotional 抬升:step=0.01,minNotional=5,价格100 → 需 0.05
	if q := minVenueTradableQty(venue.Instrument{StepSize: 0.01, MinNotional: 5}, false, 100); math.Abs(q-0.05) > 1e-12 {
		t.Fatalf("MinNotional 抬升 want 0.05, got %v", q)
	}
	// 规格未知 → 0
	if q := minVenueTradableQty(venue.Instrument{}, false, 100); q != 0 {
		t.Fatalf("未知规格 want 0, got %v", q)
	}
}

func TestFastAlt_RecordOnlyHardGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	e.cfg.FastFadeLiveEnabled = true
	e.cfg.FastLiqFollowLiveEnabled = true
	// liq_catch:即使 fade/liq_follow 灰度全开、池塘 OK,也永远硬闸(已证伪)
	ev := &fastExecShadowEvent{Symbol: "G-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "liq_catch", FastCandidateSide: "long", FastCandidateScore: 0.99,
		PondOK: true, PondSamples: fastPondWarmupSamples + 1}
	if r := e.fastLiveOpen(context.Background(), ev); r != "record_only_source" {
		t.Fatalf("liq_catch 必须被硬闸拒绝,得到 %s", r)
	}
}

func TestFastLiqFollow_GateAndHelpers(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	// 方向:多头被强平(被迫卖)→ 跟空;空头被强平 → 跟多
	if fastLiqFollowSide("long") != "short" || fastLiqFollowSide("short") != "long" || fastLiqFollowSide("") != "" {
		t.Fatal("liq_follow 方向映射错")
	}
	// liq_follow 强制 taker;其余来源按配置
	if fastEntryMakerMode("maker", "liq_follow") || !fastEntryMakerMode("maker", "") ||
		!fastEntryMakerMode("maker", "fade_exhaustion") || fastEntryMakerMode("taker", "") {
		t.Fatal("入场模式选择错")
	}
	// 开关关 → 硬闸拒
	ev := &fastExecShadowEvent{Symbol: "LF-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "liq_follow", FastCandidateSide: "short", PondOK: false, PondSamples: 9999}
	e.cfg.FastLiqFollowLiveEnabled = false
	if r := e.fastLiveOpen(context.Background(), ev); r != "record_only_source" {
		t.Fatalf("开关关时 liq_follow 应拒,得到 %s", r)
	}
	// 开关开 → 过来源闸,且豁免静态池塘判定(pond REJ 不拦事件驱动线)
	e.cfg.FastLiqFollowLiveEnabled = true
	e.cfg.FastPondFilterEnabled = true
	r := e.fastLiveOpen(context.Background(), ev)
	if r == "record_only_source" || r == "pond_reject" || r == "low_score" || r == "low_score_adaptive" {
		t.Fatalf("liq_follow 不应被来源/池塘/分数闸拦,得到 %s", r)
	}
}

func TestFastLiqHotQueue(t *testing.T) {
	now := time.Now().UTC()
	sym := "HOT-" + t.Name() + "-USDT"
	// 同币多笔入队(入队不去重),drain 批内去重 → 只出 1 个
	fastNoteLiqHot(sym, now)
	fastNoteLiqHot(sym, now.Add(5*time.Second))
	got := fastDrainLiqHot(now.Add(6*time.Second), 8*time.Second, 10)
	cnt := 0
	for _, s := range got {
		if s == sym {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("批内去重失败: %d", cnt)
	}
	// 关键回归(修过的 bug):第一笔评估后(n=1 跳过),第二笔爆仓再入队,簇长成时必须还能被消费到
	fastNoteLiqHot(sym, now.Add(10*time.Second))
	got2 := fastDrainLiqHot(now.Add(11*time.Second), 8*time.Second, 10)
	found := false
	for _, s := range got2 {
		if s == sym {
			found = true
		}
	}
	if !found {
		t.Fatal("簇第二笔不应被入队去重挡住(曾导致合格簇永远错过)")
	}
	// 过期丢弃
	sym2 := "HOT2-" + t.Name() + "-USDT"
	fastNoteLiqHot(sym2, now)
	for _, s := range fastDrainLiqHot(now.Add(30*time.Second), 8*time.Second, 10) {
		if s == sym2 {
			t.Fatal("过期热币应被丢弃")
		}
	}
}

func TestFastLiqEvalCooldown(t *testing.T) {
	now := time.Now().UTC()
	sym := "EVAL-" + t.Name() + "-USDT"
	if !fastLiqEvalAllowed(sym, now) {
		t.Fatal("首次评估应放行")
	}
	if fastLiqEvalAllowed(sym, now.Add(5*time.Second)) {
		t.Fatal("15s 内应冷却")
	}
	if !fastLiqEvalAllowed(sym, now.Add(16*time.Second)) {
		t.Fatal("15s 后应放行")
	}
}

func TestFastAlt_FadeLiveGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	mk := func(pondOK bool, samples int) *fastExecShadowEvent {
		return &fastExecShadowEvent{Symbol: "FG-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
			CandidateSource: "fade_exhaustion", FastCandidateSide: "short",
			PondOK: pondOK, PondSamples: samples}
	}
	// 灰度开关关 → 拒
	e.cfg.FastFadeLiveEnabled = false
	if r := e.fastLiveOpen(context.Background(), mk(true, fastPondWarmupSamples+1)); r != "record_only_source" {
		t.Fatalf("灰度关时 fade 应拒,得到 %s", r)
	}
	e.cfg.FastFadeLiveEnabled = true
	// 池塘热身期(样本不足)→ 拒(fade 实盘要求成熟 OK 池,不吃 fail-open)
	if r := e.fastLiveOpen(context.Background(), mk(true, 10)); r != "record_only_source" {
		t.Fatalf("热身池 fade 应拒,得到 %s", r)
	}
	// 池塘 REJ → 拒
	if r := e.fastLiveOpen(context.Background(), mk(false, fastPondWarmupSamples+1)); r != "record_only_source" {
		t.Fatalf("REJ 池 fade 应拒,得到 %s", r)
	}
	// 成熟 OK 池 → 过硬闸与分数闸(score=0 不应挡 fade),走到后续闸(测试环境无行情,不会真下单)
	r := e.fastLiveOpen(context.Background(), mk(true, fastPondWarmupSamples+1))
	if r == "record_only_source" || r == "low_score" || r == "low_score_adaptive" {
		t.Fatalf("成熟OK池的 fade 不应被来源/分数闸拦,得到 %s", r)
	}
}

func TestFastScaledExitBps(t *testing.T) {
	// unit=0(能量未知/未启用)→ 原绝对值
	if got := fastScaledExitBps(25, 2.0, 0); got != 25 {
		t.Fatalf("unit=0 应回退绝对值 25,得到 %.1f", got)
	}
	// 死水池(能量6bps):2×6=12 < 25 → 仍用绝对下限,不缩小
	if got := fastScaledExitBps(25, 2.0, 6); got != 25 {
		t.Fatalf("低能池不应低于绝对下限,得到 %.1f", got)
	}
	// LAB 级高能池(30.7bps):灾难=61.4、止损=36.8、A斩=18.4、TP=24.6
	if got := fastScaledExitBps(25, 2.0, 30.7); got < 61.3 || got > 61.5 {
		t.Fatalf("高能池灾难线应≈61.4,得到 %.1f", got)
	}
	if got := fastScaledExitBps(15, 1.2, 30.7); got < 36.8 || got > 36.9 {
		t.Fatalf("高能池止损应≈36.84,得到 %.1f", got)
	}
	if got := fastScaledExitBps(12, 0.8, 30.7); got < 24.5 || got > 24.6 {
		t.Fatalf("高能池TP应≈24.56,得到 %.1f", got)
	}
}

func TestFastExitUnitBps(t *testing.T) {
	e := newFastV3TestEngine(t)
	sym := "UNITTEST-USDT"
	key := e.venueName + "|" + sym
	fastPondMu.Lock()
	delete(fastPondHist, key)
	fastPondMu.Unlock()
	// 未启用 → 0
	e.cfg.FastExitEnergyScaleEnabled = false
	if u := e.fastExitUnitBps(sym); u != 0 {
		t.Fatalf("未启用应为 0,得到 %.1f", u)
	}
	e.cfg.FastExitEnergyScaleEnabled = true
	// 样本不足 → 0(退回绝对阈值,不误放大)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		fastPondRecord(key, now.Add(time.Duration(i)*time.Second), 20, 10000)
	}
	if u := e.fastExitUnitBps(sym); u != 0 {
		t.Fatalf("样本不足应为 0,得到 %.1f", u)
	}
	// 样本成熟 → 能量中位
	for i := 10; i < fastPondWarmupSamples+10; i++ {
		fastPondRecord(key, now.Add(time.Duration(i)*time.Second), 20, 10000)
	}
	if u := e.fastExitUnitBps(sym); u < 19.9 || u > 20.1 {
		t.Fatalf("成熟池能量单位应≈20,得到 %.1f", u)
	}
	fastPondMu.Lock()
	delete(fastPondHist, key)
	fastPondMu.Unlock()
}

func TestFastLiqFollowBlindGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	sym := "BLINDGATE-USDT"
	key := e.venueName + "|" + sym
	fastMidMu.Lock()
	delete(fastMidHist, key)
	fastMidMu.Unlock()
	now := time.Now().UTC()
	// 零历史 → 跨度 0 <15 → 盲区(首见币只记观测不下单)
	if span := fastMidSpanSecs(key, now); span >= 15 {
		t.Fatalf("零历史跨度应<15,得到 %.1f", span)
	}
	// 首次评估(15s前)写入首个观测点:此刻评估时跨度 5s → 仍盲
	e.updateFastMidHist(sym, now.Add(-5*time.Second), 1.0)
	if span := fastMidSpanSecs(key, now); span >= 15 {
		t.Fatalf("5s历史应仍<15,得到 %.1f", span)
	}
	// 首个观测点已存在 20s(冷却后新爆仓再评):跨度≥15 → 冲量真实可算,盲区闸放行
	later := now.Add(15 * time.Second)
	e.updateFastMidHist(sym, later, 1.0)
	if span := fastMidSpanSecs(key, later); span < 15 {
		t.Fatalf("20s历史应≥15放行,得到 %.1f", span)
	}
	fastMidMu.Lock()
	delete(fastMidHist, key)
	fastMidMu.Unlock()
}

func TestFastScorerRecordOnlyGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	ev := &fastExecShadowEvent{Symbol: "SC-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		FastCandidateSide: "long", FastCandidateScore: 0.99}
	// v4 换枪:scorer 关 → record-only
	e.cfg.FastScorerLiveEnabled = false
	if r := e.fastLiveOpen(context.Background(), ev); r != "scorer_record_only" {
		t.Fatalf("scorer 关时应 record-only,得到 %s", r)
	}
	// 显式开 → 走后续闸(不被来源闸拦)
	e.cfg.FastScorerLiveEnabled = true
	if r := e.fastLiveOpen(context.Background(), ev); r == "scorer_record_only" {
		t.Fatalf("scorer 开时不应被来源闸拦,得到 %s", r)
	}
}

// TestFastLiqFollowExitUnit v4.2:liq_follow 出场能量单位 = max(池塘, 入场|imp15| 封顶120)。
// 锚定的裁决依据:39笔逐笔审计——26笔在未成熟池上回退死水刻度(TP12/灾难25),入场|imp15|中位37,
// 持有中位8s vs edge地平线60-180s;同子集反事实持满60s净+0.11U vs 实际-0.17U。
func TestFastLiqFollowExitUnit(t *testing.T) {
	// 池塘未成熟(0)→ 用入场冲量(瀑布自己报能量,不等2h统计)
	if u := fastLiqFollowExitUnit(0, -37); u != 37 {
		t.Fatalf("未成熟池应用|imp15|=37,得到 %.1f", u)
	}
	// 冲量取绝对值(跟空单 imp15 为负)
	if u := fastLiqFollowExitUnit(0, 55); u != 55 {
		t.Fatalf("正冲量应=55,得到 %.1f", u)
	}
	// 池塘成熟且大于冲量 → 用池塘(不缩小既有刻度)
	if u := fastLiqFollowExitUnit(30, -12); u != 30 {
		t.Fatalf("池塘更大应用池塘30,得到 %.1f", u)
	}
	// 冲量大于池塘 → 用冲量(LAB 池能量26但瀑布imp15=-190:瀑布比池塘热)
	if u := fastLiqFollowExitUnit(26, -90); u != 90 {
		t.Fatalf("瀑布更热应用冲量90,得到 %.1f", u)
	}
	// 闪崩级冲量封顶120:防灾难线被推到永不触发(2×120=240bps=5U单笔学费上限0.12U)
	if u := fastLiqFollowExitUnit(26, -500); u != fastLiqFollowImpUnitCapBps {
		t.Fatalf("闪崩冲量应封顶%.0f,得到 %.1f", fastLiqFollowImpUnitCapBps, u)
	}
	// 双零 → 0(回退绝对刻度,行为与 v3⑤ 一致)
	if u := fastLiqFollowExitUnit(0, 0); u != 0 {
		t.Fatalf("双零应为0,得到 %.1f", u)
	}
}

// TestFastExitTriggersCooldown v4.2:灾难止损加入冷却名单(最贵的教训必须有学费记忆——
// LAB 灾难 -0.053U 后 44s 重进再灾难 -0.060U);盈利/到时出场不冷却。
func TestFastExitTriggersCooldown(t *testing.T) {
	for _, r := range []string{"fast_stop_reversal", "fast_no_feedback", "fast_early_cut", "fast_species_a_cut", "fast_disaster_stop"} {
		if !fastExitTriggersCooldown(r) {
			t.Fatalf("%s 应触发冷却", r)
		}
	}
	for _, r := range []string{"fast_tp", "fast_trail_tp", "fast_max_hold", "fast_species_c_exit", "fast_horizon_exit"} {
		if fastExitTriggersCooldown(r) {
			t.Fatalf("%s 不应触发冷却", r)
		}
	}
}

// TestFastClusterStats v5.2:簇统计(净方向/笔数/张数和/簇龄与跨度原料)。
func TestFastClusterStats(t *testing.T) {
	now := time.Now().UTC()
	sym := "CLST-" + t.Name() + "-USDT"
	fastNoteLiq(sym, "long", 10, now.Add(-20*time.Second))
	fastNoteLiq(sym, "long", 5, now.Add(-8*time.Second))
	fastNoteLiq(sym, "short", 2, now.Add(-3*time.Second))
	side, n, qty, newest, oldest := fastClusterStats(sym, now, 30)
	if side != "long" || n != 3 || qty != 17 {
		t.Fatalf("簇统计错: side=%s n=%d qty=%v", side, n, qty)
	}
	if got := now.Sub(newest).Seconds(); got < 2.9 || got > 3.1 {
		t.Fatalf("newest(簇龄原料)错: %.2fs", got)
	}
	if got := newest.Sub(oldest).Seconds(); got < 16.9 || got > 17.1 {
		t.Fatalf("span 错: %.2fs", got)
	}
	// 窗口外不计
	side2, n2, _, _, _ := fastClusterStats(sym, now, 5)
	if side2 != "short" || n2 != 1 {
		t.Fatalf("5s窗口应只剩最后一笔 short: side=%s n=%d", side2, n2)
	}
	// Bybit 跨所缓冲计数
	fastNoteXliq(sym, now.Add(-10*time.Second))
	fastNoteXliq(sym, now.Add(-2*time.Second))
	if got := fastXliqCount(sym, now, 30); got != 2 {
		t.Fatalf("xliq 30s 计数错: %d", got)
	}
	if got := fastXliqCount(sym, now, 5); got != 1 {
		t.Fatalf("xliq 5s 计数错: %d", got)
	}
}

// TestFastProbeGovernor 探针治理器(章程 §-5):预算判定/开仓闸/记账/自动缴械/换source重置/持久化恢复。
func TestFastProbeGovernor(t *testing.T) {
	// 纯判定
	if fastProbeExhausted(fastProbeState{Trades: 19, Realized: -0.4}, 20, 0.5) {
		t.Fatal("预算内不应缴械")
	}
	if !fastProbeExhausted(fastProbeState{Trades: 20, Realized: 0}, 20, 0.5) {
		t.Fatal("笔数越线应缴械")
	}
	if !fastProbeExhausted(fastProbeState{Trades: 1, Realized: -0.5}, 20, 0.5) {
		t.Fatal("累亏越线应缴械")
	}
	// 引擎级:开仓闸+记账+缴械+换source重置
	e := newFastV3TestEngine(t)
	e.cfg.FastProbeSource = "probe_x"
	e.cfg.FastProbeMaxTrades = 2
	e.cfg.FastProbeMaxLossUSDT = 0.5
	if r := e.fastProbeGate("probe_x"); r != "" {
		t.Fatalf("新探针应放行,得到 %q", r)
	}
	if r := e.fastProbeGate("other_src"); r != "" {
		t.Fatalf("非探针 source 不受影响,得到 %q", r)
	}
	e.fastProbeRecord("probe_x", -0.1)
	e.fastProbeRecord("other_src", -99) // 非探针 source 不记账
	if r := e.fastProbeGate("probe_x"); r != "" {
		t.Fatalf("1笔/-0.1U 应仍放行,得到 %q", r)
	}
	e.fastProbeRecord("probe_x", -0.05)
	if r := e.fastProbeGate("probe_x"); r != "probe_exhausted" {
		t.Fatalf("2笔应缴械,得到 %q", r)
	}
	// 持久化恢复:清内存,重新加载应仍缴械
	fastProbeMu.Lock()
	delete(fastProbe, e.venueName)
	fastProbeMu.Unlock()
	if r := e.fastProbeGate("probe_x"); r != "probe_exhausted" {
		t.Fatalf("重启恢复后应仍缴械,得到 %q", r)
	}
	// 换 source=新预算
	e.cfg.FastProbeSource = "probe_y"
	if r := e.fastProbeGate("probe_y"); r != "" {
		t.Fatalf("新 source 应重置预算,得到 %q", r)
	}
}

// TestFastSentinel v6 净值哨兵:样本闸/中位判定/入窗滚动/粘性停火/非事件线豁免。
func TestFastSentinel(t *testing.T) {
	if fastSentinelMedianHalt([]float64{-5, -5, 3}, 20) {
		t.Fatal("样本<minTrades 不应停")
	}
	neg := make([]float64, 25)
	for i := range neg {
		neg[i] = -2
	}
	if !fastSentinelMedianHalt(neg, 20) {
		t.Fatal("中位<0 应停")
	}
	pos := append(append([]float64{}, neg[:12]...), 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13)
	if fastSentinelMedianHalt(pos, 20) {
		t.Fatal("中位>0 不应停")
	}
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	e.cfg.FastTapeLiveEnabled = true
	e.cfg.FastSentinelMinTrades = 3
	e.cfg.FastSentinelWindow = 5
	// 空 source(scorer)豁免哨兵
	if r := e.fastSentinelGate(""); r != "" {
		t.Fatalf("空 source 应豁免,得到 %q", r)
	}
	// 连亏3笔 → 停;赢单挤走亏单 → 恢复(窗口滚动语义)
	src := "sent_test"
	for i := 0; i < 3; i++ {
		e.fastSentinelRecord(src, -0.01, 5) // -20bps
	}
	if r := e.fastSentinelGate(src); r != "sentinel_halt" {
		t.Fatalf("3连亏应 sentinel_halt,得到 %q", r)
	}
	for i := 0; i < 4; i++ {
		e.fastSentinelRecord(src, +0.02, 5) // +40bps ×4,窗口5 → 中位转正
	}
	if r := e.fastSentinelGate(src); r != "" {
		t.Fatalf("窗口滚动转正后应放行,得到 %q", r)
	}
}

// TestFastTapeLine v6:挂起队列确认/过期/保留 + 来源闸(默认关死=record_only)。
func TestFastTapeLine(t *testing.T) {
	base := time.Now().UTC()
	fastTapeMu.Lock()
	fastTapeQueue = nil
	fastTapeMu.Unlock()
	fastTapeEnqueue("TA-USDT", "short", base, 800, 8, 0)              // 将被确认(方向对号)
	fastTapeEnqueue("TB-USDT", "long", base.Add(-10*time.Second), 600, 6, 0) // 过期(>3s)
	fastTapeEnqueue("TC-USDT", "short", base, 700, 7, 0)              // 未确认,应留队
	fastTapeEnqueue("TD-USDT", "long", base, 900, 9, 0)               // 将被确认但方向错号
	liqSides := func(sym string, since time.Time) []string {
		switch sym {
		case "TA-USDT":
			return []string{"long"} // 卖方burst(short)← 多头爆 = 对号
		case "TD-USDT":
			return []string{"long", "long", "short"} // 买方burst(long)应对 short 爆,多数票 long = 错号
		}
		return nil
	}
	out := fastTapeDrainConfirmed(base.Add(1*time.Second), 3, liqSides)
	if len(out) != 2 || out[0].sym != "TA-USDT" || out[1].sym != "TD-USDT" {
		t.Fatalf("应确认 TA+TD: %+v", out)
	}
	if !out[0].sideMatch {
		t.Fatalf("TA 方向应对号: %+v", out[0])
	}
	if out[1].sideMatch {
		t.Fatalf("TD 方向应错号(多数票): %+v", out[1])
	}
	fastTapeMu.Lock()
	rest := len(fastTapeQueue)
	fastTapeMu.Unlock()
	if rest != 1 { // TB 过期丢弃,TC 留队
		t.Fatalf("留队应剩 1(TC), got %d", rest)
	}
	// 来源闸:默认关死 → record_only_source;开 → 过闸
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	ev := &fastExecShadowEvent{Symbol: "TP-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "tape_burst", FastCandidateSide: "short", PondOK: true, PondSamples: 9999}
	if r := e.fastLiveOpen(context.Background(), ev); r != "record_only_source" {
		t.Fatalf("默认关死应 record_only,得到 %s", r)
	}
	e.cfg.FastTapeLiveEnabled = true
	if r := e.fastLiveOpen(context.Background(), ev); r == "record_only_source" {
		t.Fatalf("开火后不应被来源闸拦,得到 %s", r)
	}
}

// TestFastBurstUpdate v5.9 亚秒探测器:基线热身/秒桶推进/5×触发/额度下限/死盘地板/方向。
func TestFastBurstUpdate(t *testing.T) {
	st := &symBurstState{}
	base := time.Unix(1783200000, 0)
	// 热身:60 秒每秒 100U 底噪
	for i := 0; i < 60; i++ {
		fastBurstUpdate(st, base.Add(time.Duration(i)*time.Second), 100, true)
	}
	// 底噪不触发
	hit, _, _, _ := fastBurstUpdate(st, base.Add(61*time.Second), 100, true)
	if hit {
		t.Fatal("底噪不应触发")
	}
	// 1s 内 600U(6×中位100)且≥500U → 触发,方向=卖
	t2 := base.Add(62 * time.Second)
	fastBurstUpdate(st, t2, 300, false)
	hit, tot, med, buySide := fastBurstUpdate(st, t2.Add(200*time.Millisecond), 300, false)
	if !hit || tot < 600 || med <= 0 || buySide {
		t.Fatalf("应触发卖向burst: hit=%v tot=%v med=%v buy=%v", hit, tot, med, buySide)
	}
	// 额度下限:5×但名义<500U 不触发
	st2 := &symBurstState{}
	for i := 0; i < 60; i++ {
		fastBurstUpdate(st2, base.Add(time.Duration(i)*time.Second), 10, true)
	}
	if hit, _, _, _ := fastBurstUpdate(st2, base.Add(61*time.Second), 60, true); hit {
		t.Fatal("名义<500U 不应触发")
	}
	// 热身期(<30s)不判定
	st3 := &symBurstState{}
	fastBurstUpdate(st3, base, 100, true)
	if hit, _, _, _ := fastBurstUpdate(st3, base.Add(time.Second), 99999, true); hit {
		t.Fatal("热身期不应触发")
	}
}

func TestFastLiqCascadeAlive(t *testing.T) {
	// 阈值<=0 = 闸关闭
	if !fastLiqCascadeAlive("short", 0, 0) {
		t.Fatal("阈值0应放行")
	}
	// 多头爆仓→跟空:价格须仍在跌(imp15<=-5)
	if !fastLiqCascadeAlive("short", -8, 5) {
		t.Fatal("跟空且仍在跌应放行")
	}
	if fastLiqCascadeAlive("short", -2, 5) {
		t.Fatal("跌幅不足=瀑布将死,应拦")
	}
	if fastLiqCascadeAlive("short", +6, 5) {
		t.Fatal("已翻涨=瀑布死了,应拦")
	}
	// 空头爆仓→跟多:价格须仍在涨
	if !fastLiqCascadeAlive("long", +7, 5) {
		t.Fatal("跟多且仍在涨应放行")
	}
	if fastLiqCascadeAlive("long", -6, 5) {
		t.Fatal("已翻跌应拦")
	}
}
