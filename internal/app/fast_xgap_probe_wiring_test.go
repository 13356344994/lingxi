package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFastXgapProbeWiring 实盘日接线演练(2026-07-08):明天的两行配置
// (fast_xgap_live_enabled + fast_probe_source=xgap_rev)必须让预算机器真实接管 E 枪。
// 断言链:①live 关=record_only_source(现状) ②live 开+预算未耗尽=通过来源闸与探针闸
// ③预算耗尽(笔数/累亏任一)=probe_exhausted 缴械 ④哨兵中位<0=sentinel_halt。
func TestFastXgapProbeWiring(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	ev := &fastExecShadowEvent{Symbol: "XG-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "xgap_rev", FastCandidateSide: "long", PondOK: true, PondSamples: 9999}

	// ① 现状:live 锁死 → record-only
	e.cfg.FastXgapLiveEnabled = false
	if r := e.fastLiveOpen(nil, ev); r != "record_only_source" {
		t.Fatalf("live 关应 record_only_source,得 %s", r)
	}

	// ② 明天的开火配置:live 开 + 探针源 = 预算机器接管;预算未耗尽应放行(走到后续闸,
	// 本测试无 venue 适配器,走到 no_instrument 级即证明来源闸/探针闸/哨兵闸全部通过)。
	e.cfg.FastXgapLiveEnabled = true
	e.cfg.FastProbeSource = "xgap_rev"
	e.cfg.FastProbeMaxTrades = 20
	e.cfg.FastProbeMaxLossUSDT = 0.5
	fastProbeMu.Lock()
	delete(fastProbe, e.fastProbeKey("xgap_rev"))
	fastProbeLoaded[e.venueName] = true // 测试隔离:不从盘上恢复
	fastProbeMu.Unlock()
	if r := e.fastLiveOpen(nil, ev); r == "record_only_source" || r == "probe_exhausted" || r == "sentinel_halt" {
		t.Fatalf("live 开+预算充足不应被闸拦,得 %s", r)
	}

	// ③ 自动阶梯(07-09 全自动拍板):20 笔净正打满 → 自动晋升 L1(不再缴械=机器代批加钱)
	for i := 0; i < 20; i++ {
		e.fastProbeRecord("xgap_rev", 0.001)
	}
	fastProbeMu.Lock()
	lvl := e.fastProbeStateLocked("xgap_rev").Level
	fastProbeMu.Unlock()
	if lvl != 1 {
		t.Fatalf("净正打满应自动晋升 L1,得 level=%d", lvl)
	}
	if n := e.fastGradNotional("xgap_rev"); n != 7.5 {
		t.Fatalf("L1 名义应 7.5U,得 %.1f", n)
	}
	// 累亏口径:L1 亏穿 1.5U → 缴械
	e.fastProbeRecord("xgap_rev", -1.51)
	if r := e.fastLiveOpen(nil, ev); r != "probe_exhausted" {
		t.Fatalf("L1 累亏穿线应缴械,得 %s", r)
	}
	// 净负打满=停机等裁决(不晋升不缴械)
	fastProbeMu.Lock()
	delete(fastProbe, e.fastProbeKey("xgap_rev"))
	fastProbeMu.Unlock()
	for i := 0; i < 20; i++ {
		e.fastProbeRecord("xgap_rev", -0.02) // 合计-0.4,未穿0.5缴械线
	}
	if r := e.fastLiveOpen(nil, ev); r != "grad_stalled" {
		t.Fatalf("净负打满应停机等裁决,得 %s", r)
	}

	// ③b 多探针独立预算(2026-07-08):xgap 缴械不牵连 tape 探针的预算
	e.cfg.FastProbeSource = "xgap_rev,tape_burst"
	if r := e.fastProbeGate("tape_burst"); r != "" {
		t.Fatalf("tape 探针预算独立,不应被 xgap 缴械牵连,得 %s", r)
	}
	if r := e.fastProbeGate("xgap_rev"); r == "" {
		t.Fatalf("xgap 自己的停机/缴械应仍生效,得放行")
	}
	e.cfg.FastProbeSource = "xgap_rev"

	// ④ 哨兵:滚动窗净值中位 <0 → 粘性停火(预算重置也拦得住)。
	// 注:③里 -0.51U 已被持久化(重启不失忆的设计),须连磁盘账一起清才算真重置——
	// 这个失败本身验证了缴械状态的持久性 ✓
	fastProbeMu.Lock()
	delete(fastProbe, e.fastProbeKey("xgap_rev"))
	fastProbeMu.Unlock()
	_ = os.Remove(filepath.Join(e.recordDir, fastProbeStateFile))
	e.cfg.FastSentinelMinTrades = 5
	fastSentinelMu.Lock()
	fastSentinelRing[e.fastSentinelKey("xgap_rev")] = []float64{-5, -6, -7, -8, -9, -10}
	fastSentinelInit[e.fastSentinelKey("xgap_rev")] = true
	fastSentinelMu.Unlock()
	if r := e.fastLiveOpen(nil, ev); r != "sentinel_halt" {
		t.Fatalf("哨兵中位<0 应粘性停火,得 %s", r)
	}
}
