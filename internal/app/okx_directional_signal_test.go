package app

import (
	"math"
	"testing"
)

func testDirectionalCfg() OKXDirectionalConfig {
	return OKXDirectionalConfig{
		EMAFast: 3, EMASlow: 8,
		TrendEfficiencyThreshold: 0.30,
		ZScoreEntry:              1.2,
		MinFundingAbsPerInterval: 0.0005,
		RequireFundingAlignment:  true,
		StopLossPct:              0.025,
		TakeProfitPct:            0.015,
		MaxHoldMinutes:           180,
	}
}

func TestEfficiencyRatioTrendVsRange(t *testing.T) {
	trend := []float64{100, 101, 102, 103, 104, 105, 106, 107}
	if er := efficiencyRatio(trend); er < 0.99 {
		t.Fatalf("单调上行应高效率比, got %.3f", er)
	}
	zig := []float64{100, 101, 100, 101, 100, 101, 100, 101}
	if er := efficiencyRatio(zig); er > 0.3 {
		t.Fatalf("锯齿应低效率比, got %.3f", er)
	}
}

func TestDirectionalSignalTrendFollow(t *testing.T) {
	cfg := testDirectionalCfg()
	up := []float64{100, 100.5, 101, 101.6, 102.2, 103, 103.8, 104.7, 105.6, 106.5}
	// 趋势向上 + 资费为负(做多还收资费,允许且强对齐)。
	s := computeDirectionalSignal(up, -0.001, cfg)
	if s.Regime != RegimeTrend || s.Side != DirectionLong {
		t.Fatalf("上行趋势+资费负应做多, got regime=%s side=%s reason=%s", s.Regime, s.Side, s.Reason)
	}
	down := []float64{106.5, 105.6, 104.7, 103.8, 103, 102.2, 101.6, 101, 100.5, 100}
	s2 := computeDirectionalSignal(down, 0.001, cfg) // 下行 + 资费正(做空还收资费)
	if s2.Regime != RegimeTrend || s2.Side != DirectionShort {
		t.Fatalf("下行趋势+资费正应做空, got regime=%s side=%s reason=%s", s2.Regime, s2.Side, s2.Reason)
	}
}

func TestDirectionalSignalRangeRevert(t *testing.T) {
	cfg := testDirectionalCfg()
	// 震荡:围绕 100 大幅振荡(低效率比=震荡态),收盘抬高到高 z 但末根温和(非急拉,过防飞刀闸)→ 反转做空。资费正(做空允许)。
	closes := []float64{98, 102, 98, 102, 98, 102, 98, 102, 104, 104.3}
	s := computeDirectionalSignal(closes, 0.0008, cfg)
	if s.Regime != RegimeRange {
		t.Fatalf("应判为震荡态, got %s (reason=%s)", s.Regime, s.Reason)
	}
	if s.Side != DirectionShort {
		t.Fatalf("末根冲高应反转做空, got %s reason=%s", s.Side, s.Reason)
	}
}

func TestDirectionalSignalFundingAlignmentVeto(t *testing.T) {
	cfg := testDirectionalCfg()
	up := []float64{100, 100.5, 101, 101.6, 102.2, 103, 103.8, 104.7, 105.6, 106.5}
	// 上行趋势本应做多,但资费强正(做多要逆扛付资费)→ 对齐闸否决。
	s := computeDirectionalSignal(up, 0.002, cfg)
	if s.Side != DirectionFlat {
		t.Fatalf("资费逆持仓应被对齐闸否决为 flat, got %s reason=%s", s.Side, s.Reason)
	}
	// 关闭对齐闸 → 仍做多。
	cfg.RequireFundingAlignment = false
	if s2 := computeDirectionalSignal(up, 0.002, cfg); s2.Side != DirectionLong {
		t.Fatalf("关闭对齐闸后上行应做多, got %s", s2.Side)
	}
}

func TestDirectionalShouldClose(t *testing.T) {
	cfg := testDirectionalCfg()
	flat := DirectionalSignal{Side: DirectionLong}
	if ok, _ := directionalShouldClose(-0.03, 10, DirectionLong, flat, cfg); !ok {
		t.Fatal("亏损超止损应平")
	}
	if ok, _ := directionalShouldClose(0.02, 10, DirectionLong, flat, cfg); !ok {
		t.Fatal("盈利达止盈应平")
	}
	if ok, _ := directionalShouldClose(0.0, 200, DirectionLong, flat, cfg); !ok {
		t.Fatal("超时应平")
	}
	rev := DirectionalSignal{Side: DirectionShort}
	if ok, reason := directionalShouldClose(0.0, 10, DirectionLong, rev, cfg); !ok || reason != "信号反转" {
		t.Fatalf("信号反向应平, got ok=%v reason=%s", ok, reason)
	}
	if ok, _ := directionalShouldClose(0.005, 10, DirectionLong, flat, cfg); ok {
		t.Fatal("正常持有不应平")
	}
}

func TestFundingPermits(t *testing.T) {
	// 多:费率负=强对齐;费率强正=不允许。
	if permit, strong := fundingPermits(DirectionLong, -0.001, 0.0005); !permit || !strong {
		t.Fatal("做多+负费率应允许且强对齐")
	}
	if permit, _ := fundingPermits(DirectionLong, 0.002, 0.0005); permit {
		t.Fatal("做多+强正费率应不允许")
	}
	if permit, strong := fundingPermits(DirectionShort, 0.001, 0.0005); !permit || !strong {
		t.Fatal("做空+正费率应允许且强对齐")
	}
}

func TestDecideDirectionalPositionAction(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.MaxHoldMinutes = 180
	cfg.ScaleInTriggerPct = 0.15
	cfg.PartialTPTriggerPct = 0.20
	cfg.AddConvictionMin = 0.6
	cfg.TPFadeConviction = 0.4
	cfg.PeakGivebackFraction = 0.4
	cfg.MaxAdds = 2
	stopFloor := -0.5
	// 触止损地板 → 平。
	if a := decideDirectionalPositionAction(-0.55, 0.1, stopFloor, 10, DirectionLong, DirectionalSignal{Side: DirectionLong}, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("触地板应平, got %v", a)
	}
	// 信号反转 → 平。
	if a := decideDirectionalPositionAction(0.1, 0.1, stopFloor, 10, DirectionLong, DirectionalSignal{Side: DirectionShort, Conviction: 0.8}, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("反转应平, got %v", a)
	}
	// 强延续(盈利+高信心+趋势)→ 补仓。
	strong := DirectionalSignal{Side: DirectionLong, Conviction: 0.8, Regime: RegimeTrend}
	if a := decideDirectionalPositionAction(0.18, 0.18, stopFloor, 10, DirectionLong, strong, false, 0, cfg); a.Kind != dirActScaleIn {
		t.Fatalf("强延续应补仓, got %v", a)
	}
	// edge转弱(盈利+低信心)→ 部分止盈。
	weak := DirectionalSignal{Side: DirectionLong, Conviction: 0.3, Regime: RegimeRange}
	if a := decideDirectionalPositionAction(0.25, 0.25, stopFloor, 10, DirectionLong, weak, false, 0, cfg); a.Kind != dirActPartialTP {
		t.Fatalf("信心转弱应部分止盈, got %v", a)
	}
	// 自高水位明显回撤(峰0.5→现0.25,回撤50%>40%)→ 部分止盈。
	midConv := DirectionalSignal{Side: DirectionLong, Conviction: 0.55, Regime: RegimeTrend}
	if a := decideDirectionalPositionAction(0.25, 0.5, stopFloor, 10, DirectionLong, midConv, false, 0, cfg); a.Kind != dirActPartialTP {
		t.Fatalf("高水位回撤应部分止盈, got %v", a)
	}
	// 盈利但不强不弱不回撤 → 持有。
	if a := decideDirectionalPositionAction(0.12, 0.13, stopFloor, 10, DirectionLong, midConv, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("无明确信号应持有, got %v(%s)", a.Kind, a.Reason)
	}
	// 已部分止盈 + 信心转弱 + 未补够 → 不重复部分止盈,持有(交给移动止损)。
	if a := decideDirectionalPositionAction(0.22, 0.25, stopFloor, 10, DirectionLong, weak, true, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("已部分止盈不应重复, got %v", a)
	}
}

func TestDirectionalEntryGuard(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.SpikeBarPct = 0.03
	cfg.TrendMaxExtensionPct = 0.02
	cfg.VolExpansionMaxRatio = 1.8
	cfg.ReversionKnifePct = 0.015
	// 趋势态:最后一根 +5% 暴拉尖刺 → 做多被否(防追高/暴拉回调)。
	spikeUp := []float64{100, 100.2, 100.4, 100.6, 100.8, 101, 106.05}
	if ok, _ := directionalEntryGuard(spikeUp, DirectionLong, RegimeTrend, emaLast(spikeUp, cfg.EMAFast), cfg); ok {
		t.Fatal("同向暴拉尖刺应否决趋势追多")
	}
	// 震荡态:最后一根急跌 -2% → 反转做多被否(防抄底接飞刀)。
	knifeDown := []float64{100, 100.5, 99.8, 100.3, 99.9, 100.2, 98.2}
	if ok, _ := directionalEntryGuard(knifeDown, DirectionLong, RegimeRange, emaLast(knifeDown, cfg.EMAFast), cfg); ok {
		t.Fatal("仍急跌应否决抄底做多")
	}
	// 正常震荡反转:温和回踩,放行。
	calm := []float64{100, 100.4, 99.8, 100.3, 99.9, 100.1, 100.05}
	if ok, _ := directionalEntryGuard(calm, DirectionLong, RegimeRange, emaLast(calm, cfg.EMAFast), cfg); !ok {
		t.Fatal("温和回踩应放行反转做多")
	}
}

func TestCloseZScore(t *testing.T) {
	z, _, _ := closeZScore([]float64{1, 1, 1, 1, 10})
	if z <= 1.5 {
		t.Fatalf("末根远离均值应高 z, got %.3f", z)
	}
	if !math.IsInf(z, 0) && z < 0 {
		t.Fatal("正偏离应为正 z")
	}
}
