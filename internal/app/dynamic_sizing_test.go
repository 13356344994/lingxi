package app

import (
	"math"
	"testing"
)

// dynamic_sizing_test.go 钉死 M3.1(README §16.3)的容量定 size 纯函数:多重天花板取最小 + 边际择优夹取,
// 以及各 fail-safe 边界(无天花板 / 容量低于最小单 / 缺省维度不收紧)。

func approxEqualSizing(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

// baseSizingInput 给一个「depth 唯一约束、edge=1」的干净基线,各测试在其上改单一维度。
func baseSizingInput() dynamicPerSymbolCapInput {
	return dynamicPerSymbolCapInput{
		Symbol:                     "BTCUSDT",
		VisibleSpotNotionalUSDT:    200000,
		VisibleFuturesNotionalUSDT: 300000,
		MaxVisibleDepthShare:       0.05,
		DepthShareMul:              1,
		DepthCapDiscount:           1, // 关闭踩踏窗打折,便于精确对账
		ExpectedNetFundingRate:     0.0055,
		EntryThresholdRate:         0.0055,
		BaseUnitUSDT:               10000,
		EdgeMin:                    0.5,
		EdgeMax:                    2.0,
		MinNotionalUSDT:            10,
	}
}

func TestResolveDynamicCapDepthBinding(t *testing.T) {
	res := resolveDynamicPerSymbolCap(baseSizingInput())
	if !res.Tradable || res.BindingCeiling != "depth" {
		t.Fatalf("应为 depth 约束且可交易, got %+v", res)
	}
	// 200000(visible min)×0.05×1×1 = 10000;edge=1 → final = min(10000, 10000) = 10000。
	if !approxEqualSizing(res.PerSymbolCapUSDT, 10000) || !approxEqualSizing(res.FinalNotionalUSDT, 10000) {
		t.Fatalf("cap/final 应为 10000, got cap=%.4f final=%.4f", res.PerSymbolCapUSDT, res.FinalNotionalUSDT)
	}
	if !approxEqualSizing(res.EdgeScore, 1.0) {
		t.Fatalf("edgeScore 应为 1.0, got %.4f", res.EdgeScore)
	}
}

func TestResolveDynamicCapOIBindsSmallest(t *testing.T) {
	in := baseSizingInput()
	in.OpenInterestNotionalUSDT = 10000000 // ×0.0005 = 5000,小于 depthCap 10000
	in.MaxOpenInterestShare = 0.0005
	res := resolveDynamicPerSymbolCap(in)
	if res.BindingCeiling != "oi" || !approxEqualSizing(res.PerSymbolCapUSDT, 5000) {
		t.Fatalf("最小天花板应为 oi=5000, got %+v", res)
	}
	// 容量 5000 < 期望下单 10000 → final 被容量封顶为 5000。
	if !approxEqualSizing(res.FinalNotionalUSDT, 5000) {
		t.Fatalf("final 应被容量封顶为 5000, got %.4f", res.FinalNotionalUSDT)
	}
}

func TestResolveDynamicCapHardCapBinds(t *testing.T) {
	in := baseSizingInput()
	in.HardCapUSDT = 3000
	res := resolveDynamicPerSymbolCap(in)
	if res.BindingCeiling != "hardCap" || !approxEqualSizing(res.PerSymbolCapUSDT, 3000) || !approxEqualSizing(res.FinalNotionalUSDT, 3000) {
		t.Fatalf("应被绝对硬顶 3000 约束, got %+v", res)
	}
}

func TestResolveDynamicCapLiqSafeBinds(t *testing.T) {
	in := baseSizingInput()
	in.LiqSafeCapUSDT = 2000
	res := resolveDynamicPerSymbolCap(in)
	if res.BindingCeiling != "liqSafe" || !approxEqualSizing(res.PerSymbolCapUSDT, 2000) {
		t.Fatalf("应被强平安全顶 2000 约束, got %+v", res)
	}
}

func TestResolveDynamicCapEdgeScoreScalesUp(t *testing.T) {
	in := baseSizingInput()
	in.ExpectedNetFundingRate = 0.011 // /0.0055 = 2.0 → 命中 EdgeMax
	in.BaseUnitUSDT = 4000
	res := resolveDynamicPerSymbolCap(in)
	if !approxEqualSizing(res.EdgeScore, 2.0) {
		t.Fatalf("edgeScore 应封顶为 2.0, got %.4f", res.EdgeScore)
	}
	// 4000×2 = 8000 ≤ 容量 10000。
	if !approxEqualSizing(res.FinalNotionalUSDT, 8000) {
		t.Fatalf("final 应为 8000, got %.4f", res.FinalNotionalUSDT)
	}
}

func TestResolveDynamicCapEdgeScoreClampsDown(t *testing.T) {
	in := baseSizingInput()
	in.ExpectedNetFundingRate = 0.001 // /0.0055 ≈ 0.18 → 夹到 EdgeMin 0.5
	res := resolveDynamicPerSymbolCap(in)
	if !approxEqualSizing(res.EdgeScore, 0.5) {
		t.Fatalf("edgeScore 应夹到下限 0.5, got %.4f", res.EdgeScore)
	}
	if !approxEqualSizing(res.FinalNotionalUSDT, 5000) { // 10000×0.5
		t.Fatalf("final 应为 5000, got %.4f", res.FinalNotionalUSDT)
	}
}

func TestResolveDynamicCapEdgeWeightingDisabled(t *testing.T) {
	in := baseSizingInput()
	in.EdgeMax = 0 // 关闭边际加权 → edgeScore=1
	in.ExpectedNetFundingRate = 0.05
	res := resolveDynamicPerSymbolCap(in)
	if !approxEqualSizing(res.EdgeScore, 1.0) {
		t.Fatalf("关闭边际加权时 edgeScore 应为 1.0, got %.4f", res.EdgeScore)
	}
}

func TestResolveDynamicCapDepthDiscountDefault(t *testing.T) {
	in := baseSizingInput()
	in.DepthCapDiscount = 0 // <=0 → 用 defaultDepthCapDiscount(0.5)
	res := resolveDynamicPerSymbolCap(in)
	// 200000×0.05×1×0.5 = 5000。
	if !approxEqualSizing(res.PerSymbolCapUSDT, 5000) {
		t.Fatalf("缺省踩踏窗打折应令 depthCap=5000, got %.4f", res.PerSymbolCapUSDT)
	}
}

func TestResolveDynamicCapNoCeilingNotTradable(t *testing.T) {
	res := resolveDynamicPerSymbolCap(dynamicPerSymbolCapInput{Symbol: "BTCUSDT", MinNotionalUSDT: 10})
	if res.Tradable || res.BindingCeiling != "none" {
		t.Fatalf("无任何天花板应不可交易(fail-safe), got %+v", res)
	}
}

func TestResolveDynamicCapBelowMinNotional(t *testing.T) {
	in := baseSizingInput()
	in.HardCapUSDT = 5 // 容量 5 < 最小单 10
	in.MinNotionalUSDT = 10
	res := resolveDynamicPerSymbolCap(in)
	if res.Tradable || res.FinalNotionalUSDT != 0 {
		t.Fatalf("容量低于最小可下单名义应不可交易, got %+v", res)
	}
}

func TestResolveDynamicCapBaseUnitUnsetUsesCap(t *testing.T) {
	in := baseSizingInput()
	in.BaseUnitUSDT = 0 // 退化:直接用容量上限
	res := resolveDynamicPerSymbolCap(in)
	if !approxEqualSizing(res.FinalNotionalUSDT, res.PerSymbolCapUSDT) {
		t.Fatalf("无基准单位时 final 应等于容量上限, got final=%.4f cap=%.4f", res.FinalNotionalUSDT, res.PerSymbolCapUSDT)
	}
}

func TestResolveHardCapUSDT(t *testing.T) {
	if got := resolveHardCapUSDT(7000, 100000, 10); !approxEqualSizing(got, 7000) {
		t.Fatalf("显式硬顶优先, got %.4f", got)
	}
	if got := resolveHardCapUSDT(0, 100000, 10); !approxEqualSizing(got, 10000) {
		t.Fatalf("应按组合名义/币数均分=10000, got %.4f", got)
	}
	if got := resolveHardCapUSDT(0, 0, 10); got != 0 {
		t.Fatalf("两者皆缺应返回 0(无硬顶), got %.4f", got)
	}
}
