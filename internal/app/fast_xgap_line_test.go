package app

import (
	"context"
	"testing"
	"time"
)

// TestFastXgapLine E策略「捡便宜货」:纯触发判定 + 来源闸/池塘豁免(v6.5)。
func TestFastXgapLine(t *testing.T) {
	now := time.Now().UTC()
	// 触发:OKX 便宜 ≥40bps 且无冷却
	if !fastXgapShouldFire(-45, 40, time.Time{}, now, 1800) {
		t.Fatal("便宜45bps 无冷却应触发")
	}
	// 不够便宜不触发
	if fastXgapShouldFire(-39, 40, time.Time{}, now, 1800) {
		t.Fatal("便宜39bps 不应触发(冻结切点40)")
	}
	// 贵不做:空头腿已判负(净-14958,贵=真拉升逆动量)
	if fastXgapShouldFire(45, 40, time.Time{}, now, 1800) {
		t.Fatal("OKX 贵45bps 不应触发(只做多头腿)")
	}
	// 冷却中不触发;冷却过了再触发
	if fastXgapShouldFire(-45, 40, now.Add(-10*time.Minute), now, 1800) {
		t.Fatal("冷却1800s 内不应再触发")
	}
	if !fastXgapShouldFire(-45, 40, now.Add(-31*time.Minute), now, 1800) {
		t.Fatal("冷却期满应可再触发")
	}
	// entry_gap 未配置(<=0)=线不工作(fail-closed)
	if fastXgapShouldFire(-45, 0, time.Time{}, now, 1800) {
		t.Fatal("entry_gap<=0 应 fail-closed")
	}

	// 来源闸:实弹开关关 → record_only;开 → 过来源闸且豁免静态池塘(资格=价差本身)
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	ev := &fastExecShadowEvent{Symbol: "XG-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "xgap_rev", FastCandidateSide: "long", PondOK: false, PondSamples: 9999}
	if r := e.fastLiveOpen(context.Background(), ev); r != "record_only_source" {
		t.Fatalf("fast_xgap_live_enabled=false 时应 record_only_source,得到 %s", r)
	}
	e.cfg.FastXgapLiveEnabled = true
	e.cfg.FastPondFilterEnabled = true
	r := e.fastLiveOpen(context.Background(), ev)
	if r == "record_only_source" || r == "pond_reject" || r == "low_score" || r == "low_score_adaptive" {
		t.Fatalf("xgap_rev 不应被来源/池塘/分数闸拦,得到 %s", r)
	}
}

// TestFastGapPath ㊳①路径信息集:生龄(≤-20连续段)/速度(3s/15s)/归因(OKX vs 锚)。
func TestFastGapPath(t *testing.T) {
	fastGapPathMu.Lock()
	fastGapPath = map[string][]fastGapPathPt{}
	fastGapPathMu.Unlock()
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	// 构造:t-30~t-18 价差健康(-5),t-15 起裂开并持续加深(OKX 掉、锚不动)。
	// 锚恒为 1.0000 → okx = 1*(1+gap/1e4)。
	seed := []struct {
		secAgo float64
		gap    float64
	}{{30, -5}, {27, -5}, {24, -5}, {21, -5}, {18, -5}, {15, -22}, {12, -30}, {9, -35}, {6, -40}, {3, -42}, {0, -45}}
	for _, s := range seed {
		ts := base.Add(-time.Duration(s.secAgo*1000) * time.Millisecond)
		fastNoteGapPath("X-USDT", ts, s.gap, 1*(1+s.gap/1e4))
	}
	st := fastGapPathAt("X-USDT", base)
	if !st.OK {
		t.Fatal("15s 历史充足应可算")
	}
	// 生龄:-22 起点在 t-15 → 15s(浮点容忍)
	if st.AgeSec < 14 || st.AgeSec > 16 {
		t.Fatalf("生龄应≈15s,得 %.1f", st.AgeSec)
	}
	// 速度:3s 前 gap=-42 → vel3 = -45-(-42) = -3(还在扩大);15s 前=-22 → vel15=-23
	if st.Vel3Bps > -2.9 || st.Vel3Bps < -3.1 {
		t.Fatalf("vel3 应≈-3,得 %.2f", st.Vel3Bps)
	}
	if st.Vel15Bps > -22.9 || st.Vel15Bps < -23.1 {
		t.Fatalf("vel15 应≈-23,得 %.2f", st.Vel15Bps)
	}
	// 归因:OKX 自身跌了(-45)-(-22)≈-23bps,锚 0 → OKX 驱动
	if st.OkxRet15Bps > -22 || st.OkxRet15Bps < -24 {
		t.Fatalf("okxRet15 应≈-23,得 %.2f", st.OkxRet15Bps)
	}
	if st.AnchorRet15Bps < -0.5 || st.AnchorRet15Bps > 0.5 {
		t.Fatalf("锚未动 anchorRet15 应≈0,得 %.2f", st.AnchorRet15Bps)
	}
	// 历史不足:只有 6s 的新币 → OK=false(fail-closed 标注)
	fastNoteGapPath("Y-USDT", base.Add(-6*time.Second), -50, 0.995)
	fastNoteGapPath("Y-USDT", base.Add(-3*time.Second), -48, 0.9952)
	fastNoteGapPath("Y-USDT", base, -46, 0.9954)
	if st2 := fastGapPathAt("Y-USDT", base); st2.OK {
		t.Fatal("不足 15s 历史应标不可算")
	}
}
