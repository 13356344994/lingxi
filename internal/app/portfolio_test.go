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
