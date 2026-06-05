package app

import (
	"reflect"
	"sort"
	"testing"
)

// TestEvaluateRollDecision 钉死续持判定(§16.4)的纯函数语义:门槛闸 + 安全垫闸,fail-safe 偏向平仓。
func TestEvaluateRollDecision(t *testing.T) {
	cases := []struct {
		name     string
		in       rollDecisionInput
		wantKeep bool
	}{
		{
			name: "正向carry明显盈利_续持",
			// 下期预期净资金费 0.004，边际持有成本≈0(正向 carry)，门槛/安全垫 0.0005。
			in:       rollDecisionInput{Symbol: "BTCUSDT", ExpectedNextNetFundingRate: 0.004, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0005},
			wantKeep: true,
		},
		{
			name: "贴近零边际_安全垫拦截_平仓",
			// 边际净 carry = 0.0006，门槛 0.0005 过，但未明显超过安全垫 0.0008 → 平仓。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.0006, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0008},
			wantKeep: false,
		},
		{
			name:     "低于门槛_平仓",
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.0003, MarginalHoldCostRate: 0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
		{
			name: "负carry借币利息吃掉资金费_平仓",
			// 下期预期净资金费 0.002，但多持一周期借币利息 0.0025 > 资金费 → 边际净 carry 为负 → 平仓。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.002, MarginalHoldCostRate: 0.0025, HoldRollThresholdRate: 0, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
		{
			name:     "负carry借币利息后仍明显盈利_续持",
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0.005, MarginalHoldCostRate: 0.0025, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0005},
			wantKeep: true,
		},
		{
			name: "R7保守估计退回0_举证失败_平仓",
			// 符号冲突时保守估计为 0：净 carry = -成本 ≤ 0，不得续持(续持举证责任在证明下期仍盈利)。
			in:       rollDecisionInput{ExpectedNextNetFundingRate: 0, MarginalHoldCostRate: 0.0001, HoldRollThresholdRate: 0, RollSafetyBufferRate: 0},
			wantKeep: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateRollDecision(tc.in)
			if got.Keep != tc.wantKeep {
				t.Fatalf("Keep=%v, want %v (netHoldCarry=%.6f, reason=%s)", got.Keep, tc.wantKeep, got.NetHoldCarryRate, got.Reason)
			}
			if got.Reason == "" {
				t.Fatal("Reason 不应为空(可观测性)")
			}
		})
	}
}

// TestEvaluateRollDecisionClampsNegativeHoldCost 钉死边际成本为负被防御性夹为 0(成本不应为负，避免被错误数据放大续持)。
func TestEvaluateRollDecisionClampsNegativeHoldCost(t *testing.T) {
	got := evaluateRollDecision(rollDecisionInput{ExpectedNextNetFundingRate: 0.0003, MarginalHoldCostRate: -1.0, HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0})
	if got.Keep {
		t.Fatalf("负边际成本应夹为 0、不得把 0.0003 抬过门槛 0.0005: keep=%v netHoldCarry=%.6f", got.Keep, got.NetHoldCarryRate)
	}
	if got.NetHoldCarryRate != 0.0003 {
		t.Fatalf("夹 0 后净 carry 应等于 ExpectedNextNetFundingRate=0.0003, got %.6f", got.NetHoldCarryRate)
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func assertSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(sortedStrings(got), sortedStrings(want)) {
		t.Fatalf("%s = %v, want %v", label, sortedStrings(got), sortedStrings(want))
	}
}

// TestRebalancePortfolioFillsFreeSlots 空槽充足时,达标候选直接补入,不触发任何替换。
func TestRebalancePortfolioFillsFreeSlots(t *testing.T) {
	held := []portfolioPosition{{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 1000}}
	cands := []portfolioPosition{
		{Symbol: "BBB", NetCarryRate: 0.004, NotionalUSDT: 1000},
		{Symbol: "CCC", NetCarryRate: 0.0035, NotionalUSDT: 1000},
	}
	plan := rebalancePortfolio(held, cands, rebalanceLimits{MaxSymbols: 5, MaxNotionalUSDT: 0, HysteresisBandRate: 0.001})
	assertSet(t, "Keep", plan.Keep, []string{"AAA"})
	assertSet(t, "Add", plan.Add, []string{"BBB", "CCC"})
	assertSet(t, "Drop", plan.Drop, []string{})
}

// TestRebalancePortfolioHysteresisPreventsChurn 槽位满 + 新候选仅略优于最弱持仓(优势 ≤ 滞后带)→ 不换仓(防 churn)。
func TestRebalancePortfolioHysteresisPreventsChurn(t *testing.T) {
	held := []portfolioPosition{
		{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 1000},
		{Symbol: "BBB", NetCarryRate: 0.0035, NotionalUSDT: 1000},
	}
	// 候选 0.0035 比最弱持仓 AAA(0.003)优 0.0005，但滞后带 0.001 → 优势不足，不换。
	cands := []portfolioPosition{{Symbol: "CCC", NetCarryRate: 0.0035, NotionalUSDT: 1000}}
	plan := rebalancePortfolio(held, cands, rebalanceLimits{MaxSymbols: 2, MaxNotionalUSDT: 0, HysteresisBandRate: 0.001})
	assertSet(t, "Keep", plan.Keep, []string{"AAA", "BBB"})
	assertSet(t, "Add", plan.Add, []string{})
	assertSet(t, "Drop", plan.Drop, []string{})
}

// TestRebalancePortfolioReplacesWhenBeyondBand 槽位满 + 候选净 carry 超过最弱持仓「> 滞后带」→ 替换最弱持仓。
func TestRebalancePortfolioReplacesWhenBeyondBand(t *testing.T) {
	held := []portfolioPosition{
		{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 1000},
		{Symbol: "BBB", NetCarryRate: 0.0035, NotionalUSDT: 1000},
	}
	// 候选 0.006 比最弱 AAA(0.003)优 0.003 > 滞后带 0.001 → 替换 AAA。
	cands := []portfolioPosition{{Symbol: "CCC", NetCarryRate: 0.006, NotionalUSDT: 1000}}
	plan := rebalancePortfolio(held, cands, rebalanceLimits{MaxSymbols: 2, MaxNotionalUSDT: 0, HysteresisBandRate: 0.001})
	assertSet(t, "Keep", plan.Keep, []string{"BBB"})
	assertSet(t, "Add", plan.Add, []string{"CCC"})
	assertSet(t, "Drop", plan.Drop, []string{"AAA"})
}

// TestRebalancePortfolioL3NotionalCapRejectsNewWithoutBreakingHeld 名义触顶时,即便有空币槽,新仓也被拒,
// 且不破坏已持仓(§16.10 L3 上限测试)。
func TestRebalancePortfolioL3NotionalCapRejectsNewWithoutBreakingHeld(t *testing.T) {
	held := []portfolioPosition{
		{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 6000},
		{Symbol: "BBB", NetCarryRate: 0.0035, NotionalUSDT: 3500},
	}
	// 币数上限 5 仍有空槽,但名义上限 10000：已用 9500，新候选 1000 会超顶 → 被拒。
	// 且候选净 carry 不足以超过最弱持仓+滞后带 → 也不触发替换。
	cands := []portfolioPosition{{Symbol: "CCC", NetCarryRate: 0.0036, NotionalUSDT: 1000}}
	plan := rebalancePortfolio(held, cands, rebalanceLimits{MaxSymbols: 5, MaxNotionalUSDT: 10000, HysteresisBandRate: 0.001})
	assertSet(t, "Keep", plan.Keep, []string{"AAA", "BBB"})
	assertSet(t, "Add", plan.Add, []string{})
	assertSet(t, "Drop", plan.Drop, []string{})
}

// TestRebalancePortfolioSymbolCapFull 币数触顶 + 候选优势超滞后带 → 替换最弱;名义随替换保持合规。
func TestRebalancePortfolioSymbolCapFull(t *testing.T) {
	held := []portfolioPosition{
		{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 1000},
	}
	cands := []portfolioPosition{{Symbol: "BBB", NetCarryRate: 0.01, NotionalUSDT: 1000}}
	plan := rebalancePortfolio(held, cands, rebalanceLimits{MaxSymbols: 1, MaxNotionalUSDT: 0, HysteresisBandRate: 0.001})
	assertSet(t, "Keep", plan.Keep, []string{})
	assertSet(t, "Add", plan.Add, []string{"BBB"})
	assertSet(t, "Drop", plan.Drop, []string{"AAA"})
}

// TestRebalancePortfolioDeterministic 同输入多次执行结果稳定(确定性,无 map 迭代序泄漏)。
func TestRebalancePortfolioDeterministic(t *testing.T) {
	held := []portfolioPosition{
		{Symbol: "AAA", NetCarryRate: 0.003, NotionalUSDT: 1000},
		{Symbol: "DDD", NetCarryRate: 0.003, NotionalUSDT: 1000},
	}
	cands := []portfolioPosition{
		{Symbol: "BBB", NetCarryRate: 0.009, NotionalUSDT: 1000},
		{Symbol: "CCC", NetCarryRate: 0.008, NotionalUSDT: 1000},
	}
	limits := rebalanceLimits{MaxSymbols: 2, MaxNotionalUSDT: 0, HysteresisBandRate: 0.001}
	first := rebalancePortfolio(held, cands, limits)
	for i := 0; i < 20; i++ {
		got := rebalancePortfolio(held, cands, limits)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("再平衡非确定性: 第%d次 %+v != 首次 %+v", i, got, first)
		}
	}
}
