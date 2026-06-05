package app

import (
	"math"
	"testing"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestComputeRealizedEdgeStats 钉死聚合:净 edge 率=Σ净利/Σ名义,胜率=正净利占比。
func TestComputeRealizedEdgeStats(t *testing.T) {
	if s := computeRealizedEdgeStats(nil); s.Samples != 0 || s.NetEdgeRate != 0 {
		t.Fatalf("空样本应零值, got %+v", s)
	}
	samples := []edgeSample{
		{NetPnL: 2, NotionalUSDT: 100},
		{NetPnL: -1, NotionalUSDT: 100},
		{NetPnL: 3, NotionalUSDT: 200},
	}
	s := computeRealizedEdgeStats(samples)
	if s.Samples != 3 {
		t.Fatalf("样本数应 3, got %d", s.Samples)
	}
	if !approxEq(s.SumNetPnL, 4) || !approxEq(s.SumNotional, 400) {
		t.Fatalf("Σ不正确: %+v", s)
	}
	if !approxEq(s.NetEdgeRate, 4.0/400.0) {
		t.Fatalf("净 edge 率应 0.01, got %v", s.NetEdgeRate)
	}
	if !approxEq(s.WinRate, 2.0/3.0) {
		t.Fatalf("胜率应 2/3, got %v", s.WinRate)
	}
}

// TestRealizedEdgeWindowFIFO 钉死滚动窗只保留最近 N 个样本;maxSamples<=0 不跟踪。
func TestRealizedEdgeWindowFIFO(t *testing.T) {
	w := newRealizedEdgeWindow(3)
	for i := 0; i < 5; i++ {
		w.observe(float64(i), 100) // netPnL=0,1,2,3,4
	}
	s := w.stats()
	if s.Samples != 3 {
		t.Fatalf("应只保留最近 3 个, got %d", s.Samples)
	}
	// 最近 3 个净利 2+3+4=9。
	if !approxEq(s.SumNetPnL, 9) {
		t.Fatalf("应保留最近 3 个(Σ=9), got %v", s.SumNetPnL)
	}

	disabled := newRealizedEdgeWindow(0)
	disabled.observe(1, 100)
	if disabled.stats().Samples != 0 {
		t.Fatal("maxSamples<=0 应不跟踪")
	}
}

// TestDecideEdgeHealth 钉死健康判定:阈值=0 关闭;样本不足放行;低于阈值降档。
func TestDecideEdgeHealth(t *testing.T) {
	// 阈值=0 → 恒健康。
	if h, _ := decideEdgeHealth(realizedEdgeStats{Samples: 100, NetEdgeRate: -1}, 20, 0); !h {
		t.Fatal("阈值=0 应恒健康")
	}
	// 样本不足 → 放行(不因数据少误降档)。
	if h, _ := decideEdgeHealth(realizedEdgeStats{Samples: 5, NetEdgeRate: -1}, 20, 0.001); !h {
		t.Fatal("样本不足应放行")
	}
	// 样本足 + 低于阈值 → 不健康。
	if h, reason := decideEdgeHealth(realizedEdgeStats{Samples: 30, NetEdgeRate: 0.0001}, 20, 0.001); h || reason == "" {
		t.Fatalf("低于阈值应不健康, h=%v reason=%q", h, reason)
	}
	// 样本足 + 达标 → 健康。
	if h, _ := decideEdgeHealth(realizedEdgeStats{Samples: 30, NetEdgeRate: 0.002}, 20, 0.001); !h {
		t.Fatal("达标应健康")
	}
}

// TestEdgeDecayDowngradesGear 钉死 S6-B:edge 衰减到阈值下,档位降到 Observe 且禁止开新仓;
// edge 达标则恢复到可开仓档位。
func TestEdgeDecayDowngradesGear(t *testing.T) {
	cfg := EngineConfig{}
	cfg.RiskConfig.EdgeGuardMinSamples = 20
	cfg.RiskConfig.MinRealizedNetEdgeRate = 0.001
	engine := &FundingRateSniperEngine{cfg: cfg}
	engine.riskSupervisor = newRiskSupervisor(engine)
	engine.edgeWindow = newRealizedEdgeWindow(50)

	// 灌入 30 个净亏损样本(净 edge 率为负)→ 低于阈值。
	for i := 0; i < 30; i++ {
		engine.edgeWindow.observe(-1, 100)
	}
	if gear := engine.riskSupervisor.CurrentGear(); gear != GearObserve {
		t.Fatalf("edge 衰减应降到 Observe, got %s", gear)
	}
	if GearAllowsNewOrders(engine.riskSupervisor.CurrentGear()) {
		t.Fatal("Observe 档不应允许开新仓")
	}
	if err := engine.checkGearAllowsOpen(); err == nil {
		t.Fatal("edge 衰减时 checkGearAllowsOpen 应拦截开仓")
	}

	// 换成健康样本(净 edge 率达标)→ 恢复到可开仓档位。
	engine.edgeWindow = newRealizedEdgeWindow(50)
	for i := 0; i < 30; i++ {
		engine.edgeWindow.observe(1, 100) // 净 edge 率=0.01 > 0.001
	}
	if gear := engine.riskSupervisor.CurrentGear(); !GearAllowsNewOrders(gear) {
		t.Fatalf("edge 恢复后应允许开仓, got %s", gear)
	}
	if err := engine.checkGearAllowsOpen(); err != nil {
		t.Fatalf("edge 健康时不应拦截, got %v", err)
	}
}

// TestEdgeGuardDisabledWhenThresholdZero 钉死阈值=0 时即便净 edge 为负也不降档(只观测)。
func TestEdgeGuardDisabledWhenThresholdZero(t *testing.T) {
	cfg := EngineConfig{}
	cfg.RiskConfig.EdgeGuardMinSamples = 20
	cfg.RiskConfig.MinRealizedNetEdgeRate = 0 // 关闭强制
	engine := &FundingRateSniperEngine{cfg: cfg}
	engine.riskSupervisor = newRiskSupervisor(engine)
	engine.edgeWindow = newRealizedEdgeWindow(50)
	for i := 0; i < 30; i++ {
		engine.edgeWindow.observe(-1, 100)
	}
	if gear := engine.riskSupervisor.CurrentGear(); !GearAllowsNewOrders(gear) {
		t.Fatalf("阈值=0 应只观测不降档, got %s", gear)
	}
}
