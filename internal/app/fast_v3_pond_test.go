package app

// v3 蓝图单测:①池塘质量(滚动中位/热身fail-open/过滤闸)②接型候选(liq_catch/fade_exhaustion/实盘硬闸)。

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"lingxi/internal/core"
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

func TestFastAlt_LiqCatchCandidate(t *testing.T) {
	e := newFastV3TestEngine(t)
	now := time.Now().UTC()
	sym := "LIQ-USDT"
	fastNoteLiq(sym, "long", now.Add(-10*time.Second)) // 多头爆=被迫卖 → 接多
	fastNoteLiq(sym, "long", now.Add(-5*time.Second))
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
	fastNoteLiq(sym2, "long", now)
	fastNoteLiq(sym2, "short", now)
	if got := e.buildFastAltCandidates(&fastExecShadowEvent{Symbol: sym2, Mid: 1}, now); len(got) != 0 {
		t.Fatalf("平票不应出候选,得到 %d", len(got))
	}
	// 超 60s 窗口不出候选
	sym3 := "OLD-USDT"
	fastNoteLiq(sym3, "short", now.Add(-90*time.Second))
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

func TestFastAlt_RecordOnlyHardGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	ev := &fastExecShadowEvent{Symbol: "G-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "liq_catch", FastCandidateSide: "long", FastCandidateScore: 0.99, PondOK: true}
	if r := e.fastLiveOpen(context.Background(), ev); r != "record_only_source" {
		t.Fatalf("接型候选必须被硬闸拒绝,得到 %s", r)
	}
}
