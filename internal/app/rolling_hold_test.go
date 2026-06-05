package app

import (
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
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

// TestBuildFundingOnlySettlement 钉死 keep 周期 funding-only 结算骨架(M-S4.4b):零交易腿/零手续费、
// 只扣本周期借币、FundingIncomePending=true、预结算 NetPnL=−借币;归因逐币正确。
func TestBuildFundingOnlySettlement(t *testing.T) {
	fundingTime := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	s := buildFundingOnlySettlement(fundingOnlySettlementInput{
		RunID:   "run-1",
		CycleID: "cycle-keep-1",
		Symbols: []string{"btcusdt", "ETHUSDT", "BTCUSDT"}, // 含小写与重复,验证归一去重
		ExpectedFundingBySymbol: map[string]float64{
			"BTCUSDT": 0.40,
			"ETHUSDT": 0.25,
		},
		BorrowCostThisCycleBySymbol: map[string]float64{
			"ETHUSDT": 0.10, // 负向 carry,本周期借币利息
		},
		FundingTimePoint: fundingTime,
	})

	if s.Targets != 2 {
		t.Fatalf("去重后应 2 个 symbol, got %d", s.Targets)
	}
	if s.SpotTradingPnL != 0 || s.FuturesTradingPnL != 0 || s.TradingPnL != 0 {
		t.Fatalf("keep 周期不应有交易盈亏: spot=%.6f fut=%.6f", s.SpotTradingPnL, s.FuturesTradingPnL)
	}
	if s.TotalFees != 0 {
		t.Fatalf("keep 周期不应重计手续费, got %.6f", s.TotalFees)
	}
	if math.Abs(s.TotalBorrowCosts-0.10) > 1e-9 {
		t.Fatalf("本周期借币应为 0.10, got %.6f", s.TotalBorrowCosts)
	}
	if math.Abs(s.ExpectedFundingIncome-0.65) > 1e-9 {
		t.Fatalf("预期资金费应为 0.65, got %.6f", s.ExpectedFundingIncome)
	}
	if !s.FundingIncomePending {
		t.Fatal("FundingIncomePending 应为 true")
	}
	if math.Abs(s.NetPnL-(-0.10)) > 1e-9 {
		t.Fatalf("预结算 NetPnL 应为 −本周期借币=−0.10, got %.6f", s.NetPnL)
	}
	// 投影最终净利(R8 早刹车口径)= NetPnL + ExpectedFundingIncome = −0.10 + 0.65 = 0.55 > 0。
	if proj := projectedCycleNetPnL(s); math.Abs(proj-0.55) > 1e-9 {
		t.Fatalf("投影最终净利应为 0.55, got %.6f", proj)
	}
	// 归因:逐币、确定性升序。
	if len(s.Attributions) != 2 || s.Attributions[0].Symbol != "BTCUSDT" || s.Attributions[1].Symbol != "ETHUSDT" {
		t.Fatalf("归因 symbol 序不符: %+v", s.Attributions)
	}
	if math.Abs(s.Attributions[1].BorrowCosts-0.10) > 1e-9 || math.Abs(s.Attributions[1].NetPnL-(-0.10)) > 1e-9 {
		t.Fatalf("ETHUSDT 归因借币/净利不符: %+v", s.Attributions[1])
	}
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

// TestPartitionHedgesByRoll 钉死 keep/drop 拆分:命中 keep 集合的留任、其余(含空 symbol)平仓,各组保持原序。
func TestPartitionHedgesByRoll(t *testing.T) {
	hedges := []OpenHedge{
		{Target: FundingCandidate{Symbol: "btcusdt"}},
		{Target: FundingCandidate{Symbol: "ETHUSDT"}},
		{Target: FundingCandidate{Symbol: " SOLUSDT "}},
		{Target: FundingCandidate{Symbol: ""}}, // 空 symbol 一律 drop
	}
	keepSet := map[string]struct{}{"BTCUSDT": {}, "SOLUSDT": {}}
	keep, drop := partitionHedgesByRoll(hedges, keepSet)

	if len(keep) != 2 || keep[0].Target.Symbol != "btcusdt" || keep[1].Target.Symbol != " SOLUSDT " {
		t.Fatalf("keep 组应为 [btcusdt, SOLUSDT] 原序, got %+v", keep)
	}
	if len(drop) != 2 || drop[0].Target.Symbol != "ETHUSDT" || drop[1].Target.Symbol != "" {
		t.Fatalf("drop 组应为 [ETHUSDT, 空], got %+v", drop)
	}

	// 空 keep 集合 → 全 drop(单周期口径)。
	allDropKeep, allDrop := partitionHedgesByRoll(hedges, nil)
	if len(allDropKeep) != 0 || len(allDrop) != len(hedges) {
		t.Fatalf("空 keep 集合应全平仓, keep=%+v drop=%+v", allDropKeep, allDrop)
	}
}

// TestBuildRollDecisionInputs 钉死续持判定输入构建(方案 3):按 carry 方向归一「对我有利为正」、跳过 0 qty、
// 门槛/安全垫/边际成本透传;费率反转(预测费率与开仓方向相悖)→ 净 carry 变负 → fail-safe 平仓。
func TestBuildRollDecisionInputs(t *testing.T) {
	cfg := PortfolioConfig{HoldRollThresholdRate: 0.0005, RollSafetyBufferRate: 0.0003}
	held := []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: 0.001}, HedgeQty: 1, OpenFuturesAvgPrice: 100}, // 正 carry
		{Target: FundingCandidate{Symbol: "ETHUSDT", EstimatedRate: 0.001}, HedgeQty: 0, OpenFuturesAvgPrice: 50},  // 0 qty 跳过
	}
	rates := map[string]float64{"BTCUSDT": 0.0008}
	cost := map[string]float64{"BTCUSDT": 0.0001}
	inputs := buildRollDecisionInputs(held, rates, cost, cfg)
	if len(inputs) != 1 || inputs[0].Symbol != "BTCUSDT" {
		t.Fatalf("应只构造 BTCUSDT(跳过 0 qty), got %+v", inputs)
	}
	if inputs[0].ExpectedNextNetFundingRate <= 0 {
		t.Fatalf("正 carry + 正预测费率应为正净 carry, got %.6f", inputs[0].ExpectedNextNetFundingRate)
	}
	if inputs[0].HoldRollThresholdRate != 0.0005 || inputs[0].RollSafetyBufferRate != 0.0003 || inputs[0].MarginalHoldCostRate != 0.0001 {
		t.Fatalf("门槛/安全垫/边际成本透传错误: %+v", inputs[0])
	}

	reversed := buildRollDecisionInputs(
		[]OpenHedge{{Target: FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: 0.001}, HedgeQty: 1, OpenFuturesAvgPrice: 100}},
		map[string]float64{"BTCUSDT": -0.0008}, nil, cfg)
	if reversed[0].ExpectedNextNetFundingRate >= 0 {
		t.Fatalf("费率反转应使净 carry 为负(fail-safe 平仓), got %.6f", reversed[0].ExpectedNextNetFundingRate)
	}
}
