package app

import "testing"

// portfolioManager 现可脱离 God object 独立构造与单测(README-S2「组合独立可测」)。

func TestPortfolioManagedExposureSymbolsUnionAndNormalize(t *testing.T) {
	pm := &portfolioManager{
		activeOpenHedges: []OpenHedge{
			{Target: FundingCandidate{Symbol: "btcusdt"}, HedgeQty: 1},
			{Target: FundingCandidate{Symbol: "  ETHUSDT "}, HedgeQty: 1},
			{Target: FundingCandidate{Symbol: ""}, HedgeQty: 1}, // 空 symbol 应被跳过
		},
		exposureLocks: map[string]exposureLockState{
			"ETHUSDT": {}, // 与持仓重叠,集合去重
			"solusdt": {},
			"   ":     {}, // 空白应被跳过
		},
	}
	got := pm.managedExposureSymbols()
	want := map[string]struct{}{"BTCUSDT": {}, "ETHUSDT": {}, "SOLUSDT": {}}
	if len(got) != len(want) {
		t.Fatalf("symbols=%v want keys %v", got, want)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("缺少受管 symbol %s: %v", k, got)
		}
	}
}

func TestPortfolioManagedExposureSymbolsEmpty(t *testing.T) {
	pm := &portfolioManager{}
	if got := pm.managedExposureSymbols(); len(got) != 0 {
		t.Fatalf("空组合应返回空集合, got %v", got)
	}
}

func TestPortfolioHasNegativeCarryHedge(t *testing.T) {
	none := &portfolioManager{activeOpenHedges: []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: 0.001}},
	}}
	if none.hasNegativeCarryHedge() {
		t.Fatal("正 carry 持仓不应报告负 carry")
	}
	neg := &portfolioManager{activeOpenHedges: []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: -0.001}},
	}}
	if !neg.hasNegativeCarryHedge() {
		t.Fatal("负 carry 持仓应被识别")
	}
}

// twoHeldPortfolio 构造一个含 2 币、各 5000 USDT 名义(HedgeQty×OpenFuturesAvgPrice)的常驻组合,用于 L3 测试。
func twoHeldPortfolio() *portfolioManager {
	return &portfolioManager{activeOpenHedges: []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT"}, HedgeQty: 0.05, OpenFuturesAvgPrice: 100000}, // 5000
		{Target: FundingCandidate{Symbol: "ETHUSDT"}, HedgeQty: 2, OpenFuturesAvgPrice: 2500},      // 5000
	}}
}

func TestPortfolioHeldNotionalAndCount(t *testing.T) {
	pm := twoHeldPortfolio()
	if got := pm.portfolioHeldSymbolCount(); got != 2 {
		t.Fatalf("持仓币数应为 2, got %d", got)
	}
	if got := pm.portfolioHeldNotionalUSDT(); got != 10000 {
		t.Fatalf("组合总名义应为 10000, got %.6f", got)
	}
}

func TestPortfolioFreeSlots(t *testing.T) {
	pm := twoHeldPortfolio()
	if got := pm.portfolioFreeSlots(5); got != 3 {
		t.Fatalf("空槽应为 3, got %d", got)
	}
	if got := pm.portfolioFreeSlots(2); got != 0 {
		t.Fatalf("满仓空槽应为 0, got %d", got)
	}
	if got := pm.portfolioFreeSlots(0); got != -1 {
		t.Fatalf("无币数上限应返回 -1, got %d", got)
	}
}

func TestPortfolioAdmitCandidateL3(t *testing.T) {
	pm := twoHeldPortfolio() // 2 币、10000 名义

	// 币数维度:上限 2 已满,新币被拒。
	if ok, reason := pm.admitCandidateToPortfolio("SOLUSDT", 1000, portfolioL3Limits{MaxSymbols: 2}); ok {
		t.Fatalf("币数已满应拒新币, reason=%q", reason)
	}
	// 已持仓的币(续持/加仓)不占新币槽,放行。
	if ok, _ := pm.admitCandidateToPortfolio("btcusdt", 0, portfolioL3Limits{MaxSymbols: 2}); !ok {
		t.Fatal("已持仓币不应被币数上限拒绝")
	}
	// 名义维度:上限 12000,新增 3000 → 13000 超顶,被拒。
	if ok, reason := pm.admitCandidateToPortfolio("SOLUSDT", 3000, portfolioL3Limits{MaxSymbols: 10, MaxNotionalUSDT: 12000}); ok {
		t.Fatalf("名义超顶应拒, reason=%q", reason)
	}
	// 名义维度:新增 2000 → 12000 恰好触顶(含容差),放行。
	if ok, reason := pm.admitCandidateToPortfolio("SOLUSDT", 2000, portfolioL3Limits{MaxSymbols: 10, MaxNotionalUSDT: 12000}); !ok {
		t.Fatalf("恰好触顶应放行, reason=%q", reason)
	}
	// 两维度都不限(0)→ 始终放行。
	if ok, _ := pm.admitCandidateToPortfolio("SOLUSDT", 1e9, portfolioL3Limits{}); !ok {
		t.Fatal("不设上限应始终放行")
	}
}
