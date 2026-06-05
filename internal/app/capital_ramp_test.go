package app

import "testing"

func TestParseCapitalRampSchedule(t *testing.T) {
	if tiers, err := parseCapitalRampSchedule(""); err != nil || tiers != nil {
		t.Fatalf("空串应 nil,无错: %v %v", tiers, err)
	}
	tiers, err := parseCapitalRampSchedule(" 10000, 30000 ,100000 ")
	if err != nil {
		t.Fatalf("合法计划不应报错: %v", err)
	}
	if len(tiers) != 3 || tiers[0] != 10000 || tiers[2] != 100000 {
		t.Fatalf("解析结果错误: %v", tiers)
	}
	if _, err := parseCapitalRampSchedule("10000,5000"); err == nil {
		t.Fatal("非升序应报错")
	}
	if _, err := parseCapitalRampSchedule("10000,-1"); err == nil {
		t.Fatal("非正应报错")
	}
	if _, err := parseCapitalRampSchedule("abc"); err == nil {
		t.Fatal("非数值应报错")
	}
}

func TestCurrentRampTierIndex(t *testing.T) {
	tiers := []float64{10000, 30000, 100000}
	cases := []struct {
		cur float64
		exp int
	}{
		{0, -1}, {5000, -1}, {10000, 0}, {29999, 0}, {30000, 1}, {100000, 2}, {200000, 2},
	}
	for _, c := range cases {
		if got := currentRampTierIndex(c.cur, tiers); got != c.exp {
			t.Fatalf("cur=%v 期望档下标 %d, got %d", c.cur, c.exp, got)
		}
	}
}

func TestDecideCapitalRampPromote(t *testing.T) {
	tiers := []float64{10000, 30000, 100000}
	gate := capitalRampGate{PromoteMinNetEdgeRate: 0.001, RollbackMaxNetEdgeRate: 0, MinEdgeSamples: 20}
	// 在 10k 档、edge 达标、样本足 → 升到 30k。
	d := decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: 0.002, EdgeSamples: 30}, gate)
	if d.Action != capitalRampPromote || d.TargetNotional != 30000 {
		t.Fatalf("应升档到 30000, got %+v", d)
	}
	// 顶档保持。
	d = decideCapitalRampAction(100000, tiers, capitalRampSignals{NetEdgeRate: 0.002, EdgeSamples: 30}, gate)
	if d.Action != capitalRampHold {
		t.Fatalf("顶档应保持, got %+v", d)
	}
	// 样本不足 → 保持(不升)。
	d = decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: 0.002, EdgeSamples: 5}, gate)
	if d.Action != capitalRampHold {
		t.Fatalf("样本不足应保持, got %+v", d)
	}
	// edge 未达升档线 → 保持。
	d = decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: 0.0005, EdgeSamples: 30}, gate)
	if d.Action != capitalRampHold {
		t.Fatalf("edge 不足应保持, got %+v", d)
	}
}

func TestDecideCapitalRampRollback(t *testing.T) {
	tiers := []float64{10000, 30000, 100000}
	gate := capitalRampGate{PromoteMinNetEdgeRate: 0.001, RollbackMaxNetEdgeRate: 0, MaxSlippageRate: 0.0005, MinFillRate: 0.9, MinEdgeSamples: 20}
	// 在 30k 档、edge 跌破回退线 → 回退到 10k。
	d := decideCapitalRampAction(30000, tiers, capitalRampSignals{NetEdgeRate: -0.001, EdgeSamples: 30}, gate)
	if d.Action != capitalRampRollback || d.TargetNotional != 10000 {
		t.Fatalf("应回退到 10000, got %+v", d)
	}
	// 对账 UNHEALTHY 立即回退(不看样本)。
	d = decideCapitalRampAction(100000, tiers, capitalRampSignals{NetEdgeRate: 0.01, EdgeSamples: 1, ReconcileUnhealthy: true}, gate)
	if d.Action != capitalRampRollback || d.TargetNotional != 30000 {
		t.Fatalf("UNHEALTHY 应回退到 30000, got %+v", d)
	}
	// 滑点超预算 → 回退。
	d = decideCapitalRampAction(30000, tiers, capitalRampSignals{NetEdgeRate: 0.01, EdgeSamples: 30, SlippageRate: 0.001, HasSlippage: true}, gate)
	if d.Action != capitalRampRollback {
		t.Fatalf("滑点超预算应回退, got %+v", d)
	}
	// 成交率不足 → 回退。
	d = decideCapitalRampAction(30000, tiers, capitalRampSignals{NetEdgeRate: 0.01, EdgeSamples: 30, FillRate: 0.5, HasFillRate: true}, gate)
	if d.Action != capitalRampRollback {
		t.Fatalf("成交率不足应回退, got %+v", d)
	}
	// 已在最低档仍触发回退 → 保持 + 建议人工停机。
	d = decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: -0.001, EdgeSamples: 30}, gate)
	if d.Action != capitalRampHold {
		t.Fatalf("最低档应保持(建议人工), got %+v", d)
	}
}

func TestDecideCapitalRampHealthyReconcilesGate(t *testing.T) {
	tiers := []float64{10000, 30000}
	gate := capitalRampGate{PromoteMinNetEdgeRate: 0.001, MinEdgeSamples: 20, MinHealthyReconciles: 3}
	// edge 达标但连续 HEALTHY 不足 → 不升。
	d := decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: 0.002, EdgeSamples: 30, HealthyReconciles: 1}, gate)
	if d.Action != capitalRampHold {
		t.Fatalf("HEALTHY 不足应保持, got %+v", d)
	}
	// 满足后升档。
	d = decideCapitalRampAction(10000, tiers, capitalRampSignals{NetEdgeRate: 0.002, EdgeSamples: 30, HealthyReconciles: 3}, gate)
	if d.Action != capitalRampPromote {
		t.Fatalf("HEALTHY 达标应升档, got %+v", d)
	}
}

// TestRampNotionalOverrideMonotonicTighten 钉死灰度门自动收紧:override 只减不增,且与基线取更严。
func TestRampNotionalOverrideMonotonicTighten(t *testing.T) {
	e := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	e.cfg.PortfolioConfig.MaxPortfolioNotionalUSDT = 1000

	if got := e.effectiveMaxPortfolioNotionalUSDT(); got != 1000 {
		t.Fatalf("无 override 应返回基线 1000, got %.0f", got)
	}
	e.tightenRampNotionalOverride(600)
	if got := e.effectiveMaxPortfolioNotionalUSDT(); got != 600 {
		t.Fatalf("收紧到 600 应生效, got %.0f", got)
	}
	// 更高目标不放松(只减不增)。
	e.tightenRampNotionalOverride(800)
	if got := e.effectiveMaxPortfolioNotionalUSDT(); got != 600 {
		t.Fatalf("更高目标不应放松 override, got %.0f", got)
	}
	// 更低目标继续收紧。
	e.tightenRampNotionalOverride(400)
	if got := e.effectiveMaxPortfolioNotionalUSDT(); got != 400 {
		t.Fatalf("更低目标应继续收紧, got %.0f", got)
	}
	// 基线比 override 更严时取基线。
	e.cfg.PortfolioConfig.MaxPortfolioNotionalUSDT = 300
	if got := e.effectiveMaxPortfolioNotionalUSDT(); got != 300 {
		t.Fatalf("基线更严应取基线 300, got %.0f", got)
	}
}
