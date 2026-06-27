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
	cfg.QuickTPPct = 0.12
	// 达小止盈 + 强延续(高信心+趋势)→ 骑乘加仓。
	strong := DirectionalSignal{Side: DirectionLong, Conviction: 0.8, Regime: RegimeTrend}
	if a := decideDirectionalPositionAction(0.18, 0.18, stopFloor, 10, DirectionLong, strong, false, 0, cfg); a.Kind != dirActScaleIn {
		t.Fatalf("强延续应骑乘加仓, got %v", a)
	}
	// 达小止盈 + 非强(低信心)→ 快进快出全平(吃到就跑)。
	weak := DirectionalSignal{Side: DirectionLong, Conviction: 0.3, Regime: RegimeRange}
	if a := decideDirectionalPositionAction(0.15, 0.15, stopFloor, 10, DirectionLong, weak, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("达小止盈且非强延续应快进快出全平, got %v(%s)", a.Kind, a.Reason)
	}
	// 达小止盈 + 强但已到补仓上限 → 全平落袋(不能再加就拿走)。
	if a := decideDirectionalPositionAction(0.18, 0.18, stopFloor, 10, DirectionLong, strong, false, cfg.MaxAdds, cfg); a.Kind != dirActClose {
		t.Fatalf("到补仓上限应全平, got %v", a)
	}
	// 未到小止盈 → 持有(等到点或撞止损/反转/超时)。
	if a := decideDirectionalPositionAction(0.05, 0.05, stopFloor, 10, DirectionLong, weak, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("未到小止盈应持有, got %v(%s)", a.Kind, a.Reason)
	}
}

func TestStaleCutTimeout(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.MaxHoldMinutes = 60
	cfg.StaleCutMinutes = 40
	cfg.StaleCutMinProfitPct = 0
	cfg.QuickTPPct = 0.12
	cfg.BreakevenArmPct = 0.05
	stopFloor := -0.5
	hold := DirectionalSignal{Side: DirectionLong, Conviction: 0.3, Regime: RegimeRange}
	// 持 45m 未盈利(浮亏)→ 停滞离场提前砍(不等 60m)。
	if a := decideDirectionalPositionAction(-0.02, 0.0, stopFloor, 45, DirectionLong, hold, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("久持未盈应停滞离场, got %v(%s)", a.Kind, a.Reason)
	}
	// 持 45m 但已盈利(+6%,未到快进快出12%)→ 不砍,持有(赢单不受影响)。
	if a := decideDirectionalPositionAction(0.06, 0.06, stopFloor, 45, DirectionLong, hold, false, 0, cfg); a.Kind == dirActClose {
		t.Fatalf("久持但盈利不应停滞离场, got %v(%s)", a.Kind, a.Reason)
	}
	// 持 30m 未盈利(< StaleCut 40m)→ 还没到停滞点,持有。
	if a := decideDirectionalPositionAction(-0.02, 0.0, stopFloor, 30, DirectionLong, hold, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("未到停滞点应持有, got %v(%s)", a.Kind, a.Reason)
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

func TestSignalWindowRegime(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.SignalWindowBars = 5
	// 整段看是震荡(长程大幅来回),但最近窗口是干净上行趋势 → 窗口化后应判趋势做多。
	closes := []float64{100, 90, 110, 95, 105, 100, 101, 102, 103, 104}
	s := computeDirectionalSignal(closes, -0.001, cfg)
	if s.Regime != RegimeTrend {
		t.Fatalf("窗口内为趋势应判趋势态, got %s reason=%s", s.Regime, s.Reason)
	}
}

func TestBreakevenGiveback(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.BreakevenArmPct = 0.05
	cfg.TrailGapPct = 0.15
	cfg.QuickTPPct = 0.12
	cfg.MaxHoldMinutes = 180
	stopFloor := -0.5
	hold := DirectionalSignal{Side: DirectionLong, Conviction: 0.3, Regime: RegimeRange}
	// 峰值 +20%(已过臂点),回吐到 +3%(< 峰值−间距=5%)→ 移动保盈平。
	if a := decideDirectionalPositionAction(0.03, 0.20, stopFloor, 10, DirectionLong, hold, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("回吐过保盈线应平, got %v(%s)", a.Kind, a.Reason)
	}
	// 峰值 +20%,仍在 +10%(> 峰值−间距=5%)→ 不触发保盈,持有。
	if a := decideDirectionalPositionAction(0.10, 0.20, stopFloor, 10, DirectionLong, hold, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("回吐未过保盈线应持有, got %v(%s)", a.Kind, a.Reason)
	}
	// 峰值未过臂点(+4%),回撤到 +1%,不触发保盈,持有。
	if a := decideDirectionalPositionAction(0.01, 0.04, stopFloor, 10, DirectionLong, hold, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("峰值未过臂点不应触发保盈, got %v(%s)", a.Kind, a.Reason)
	}
}

func TestSmartTimeout(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.MaxHoldMinutes = 60
	cfg.AbsMaxHoldMinutes = 180
	cfg.QuickTPPct = 0.12
	cfg.BreakevenArmPct = 0.05
	stopFloor := -0.5
	aligned := DirectionalSignal{Side: DirectionLong, Conviction: 0.3, Regime: RegimeRange}
	flat := DirectionalSignal{Side: DirectionFlat}
	// 超时但盈利且信号同向 + 未到绝对上限 → 续持。
	if a := decideDirectionalPositionAction(0.03, 0.03, stopFloor, 65, DirectionLong, aligned, false, 0, cfg); a.Kind != dirActHold {
		t.Fatalf("超时但盈利同向应续持, got %v(%s)", a.Kind, a.Reason)
	}
	// 超时且亏损 → 离场。
	if a := decideDirectionalPositionAction(-0.02, 0.0, stopFloor, 65, DirectionLong, aligned, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("超时且亏损应离场, got %v(%s)", a.Kind, a.Reason)
	}
	// 超时盈利但信号转 flat(不同向)→ 离场。
	if a := decideDirectionalPositionAction(0.03, 0.03, stopFloor, 65, DirectionLong, flat, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("超时盈利但信号不同向应离场, got %v(%s)", a.Kind, a.Reason)
	}
	// 超时盈利同向但已达绝对上限 → 离场。
	if a := decideDirectionalPositionAction(0.03, 0.03, stopFloor, 185, DirectionLong, aligned, false, 0, cfg); a.Kind != dirActClose {
		t.Fatalf("达绝对上限应离场, got %v(%s)", a.Kind, a.Reason)
	}
}

func TestRecentDirection(t *testing.T) {
	up := []float64{100, 101, 102, 103, 104}
	if recentDirection(up, 5, 0.003) != DirectionLong {
		t.Fatal("上行应判 Long")
	}
	down := []float64{104, 103, 102, 101, 100}
	if recentDirection(down, 5, 0.003) != DirectionShort {
		t.Fatal("下行应判 Short")
	}
	flat := []float64{100, 100.05, 100.02, 100.01, 100.0}
	if recentDirection(flat, 5, 0.003) != DirectionFlat {
		t.Fatal("窄幅应判 Flat")
	}
}

func TestCrossVenueCorroboration(t *testing.T) {
	cfg := testDirectionalCfg()
	cfg.CrossVenueCorroborationEnabled = true
	cfg.CrossVenueConvictionStep = 0.1
	cfg.CrossVenueVetoMinPeers = 2
	// 无数据 → 不调整不否决。
	if d, v, _ := crossVenueCorroboration(DirectionLong, nil, cfg); v || d != 0 {
		t.Fatalf("无数据应中性, got d=%v v=%v", d, v)
	}
	// 两所同向 → 加信心。
	agree := []peerDirState{{"b", DirectionLong}, {"c", DirectionLong}}
	if d, v, _ := crossVenueCorroboration(DirectionLong, agree, cfg); v || d <= 0 {
		t.Fatalf("同向应加信心, got d=%v v=%v", d, v)
	}
	// 两所全反向 → 否决。
	dis := []peerDirState{{"b", DirectionShort}, {"c", DirectionShort}}
	if _, v, _ := crossVenueCorroboration(DirectionLong, dis, cfg); !v {
		t.Fatal("两所全反向应否决")
	}
	// 仅一所反向(<vetoMin)→ 不否决,但减信心。
	one := []peerDirState{{"b", DirectionShort}}
	if d, v, _ := crossVenueCorroboration(DirectionLong, one, cfg); v || d >= 0 {
		t.Fatalf("单所反向应减信心不否决, got d=%v v=%v", d, v)
	}
	// 一同向一反向 → 不否决,净 0。
	mix := []peerDirState{{"b", DirectionLong}, {"c", DirectionShort}}
	if d, v, _ := crossVenueCorroboration(DirectionLong, mix, cfg); v || d != 0 {
		t.Fatalf("一同一反应净0不否决, got d=%v v=%v", d, v)
	}
}

func TestSanitizeFillPrice(t *testing.T) {
	const eps = 1e-9

	// 正常成交价(偏离 5%,在 10% 以内)→ 返回 avg,trusted=true。
	price, trusted := sanitizeFillPrice(0.105, 0.100, 0.10)
	if !trusted || math.Abs(price-0.105) > eps {
		t.Fatalf("正常成交价应返回 avg trusted=true, got price=%.6f trusted=%v", price, trusted)
	}

	// avg=0 → 返回 mid,trusted=false。
	price, trusted = sanitizeFillPrice(0, 0.199, 0.10)
	if trusted || math.Abs(price-0.199) > eps {
		t.Fatalf("avg=0 应回退 mid trusted=false, got price=%.6f trusted=%v", price, trusted)
	}

	// avg 偏离 mid 超过 10%(16%) → 返回 mid,trusted=false。
	price, trusted = sanitizeFillPrice(0.1671, 0.199, 0.10)
	if trusted || math.Abs(price-0.199) > eps {
		t.Fatalf("avg 偏离>10%% 应回退 mid trusted=false, got price=%.6f trusted=%v", price, trusted)
	}

	// mid<=0,avg>0 → 返回 avg,trusted=true(无法校验,信任 avg)。
	price, trusted = sanitizeFillPrice(0.5, 0, 0.10)
	if !trusted || math.Abs(price-0.5) > eps {
		t.Fatalf("mid<=0 且 avg>0 应返回 avg trusted=true, got price=%.6f trusted=%v", price, trusted)
	}

	// mid<=0,avg<=0 → 返回 0,trusted=false。
	price, trusted = sanitizeFillPrice(0, 0, 0.10)
	if trusted || price != 0 {
		t.Fatalf("mid<=0 且 avg<=0 应返回 0 trusted=false, got price=%.6f trusted=%v", price, trusted)
	}

	// maxDevFrac<=0 时回退默认 0.10;偏离 5% 应 trusted。
	price, trusted = sanitizeFillPrice(0.105, 0.100, 0)
	if !trusted || math.Abs(price-0.105) > eps {
		t.Fatalf("maxDevFrac=0 时应用默认 10%% 校验,偏离5%%应 trusted, got price=%.6f trusted=%v", price, trusted)
	}
}
