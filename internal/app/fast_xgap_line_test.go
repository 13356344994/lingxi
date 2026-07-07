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
