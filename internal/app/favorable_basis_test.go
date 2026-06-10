package app

import (
	"strings"
	"testing"

	"lingxi/internal/core"
)

func TestIsBasisFavorableForCarry(t *testing.T) {
	cases := []struct {
		dir   fundingCarryDirection
		basis float64
		want  bool
	}{
		{fundingCarryDirectionNegative, -0.0723, true}, // 负 carry + 负基差(perp 折价)→ 有利
		{fundingCarryDirectionNegative, 0.05, false},   // 负 carry + 正基差 → 反向,不放宽
		{fundingCarryDirectionPositive, 0.0723, true},  // 正 carry + 正基差(perp 溢价)→ 有利
		{fundingCarryDirectionPositive, -0.05, false},  // 正 carry + 负基差 → 反向
		{fundingCarryDirectionNegative, 0, false},      // 基差 0 → 不放宽
		{fundingCarryDirection(""), -0.05, false},      // 方向未定 → 不放宽
	}
	for _, c := range cases {
		if got := isBasisFavorableForCarry(c.dir, c.basis); got != c.want {
			t.Errorf("isBasisFavorableForCarry(%s,%v)=%v want %v", c.dir, c.basis, got, c.want)
		}
	}
}

func cfgWithBasis(maxScan, favorable float64) EngineConfig {
	return EngineConfig{StrategyConfig: core.StrategyConfig{MaxScanBasisRate: maxScan, FavorableBasisMaxRate: favorable}}
}

func TestEffectiveMaxScanBasisRate(t *testing.T) {
	// 配了更高有利上限,且基差有利 → 用 0.08
	if got := effectiveMaxScanBasisRate(cfgWithBasis(0.04, 0.08), fundingCarryDirectionNegative, -0.0723); got != 0.08 {
		t.Errorf("有利基差应放宽到 0.08, got %v", got)
	}
	// 基差反向 → 仍用 0.04
	if got := effectiveMaxScanBasisRate(cfgWithBasis(0.04, 0.08), fundingCarryDirectionNegative, 0.0723); got != 0.04 {
		t.Errorf("反向基差应用常规 0.04, got %v", got)
	}
	// 未配置有利上限(0)→ 回退常规,行为不变
	if got := effectiveMaxScanBasisRate(cfgWithBasis(0.04, 0), fundingCarryDirectionNegative, -0.0723); got != 0.04 {
		t.Errorf("未配置有利上限应回退 0.04, got %v", got)
	}
	// 有利上限 < 常规(误配)→ 不生效,用常规
	if got := effectiveMaxScanBasisRate(cfgWithBasis(0.04, 0.02), fundingCarryDirectionNegative, -0.0723); got != 0.04 {
		t.Errorf("有利上限<常规应忽略, got %v", got)
	}
}

// TestScanGateHomeusdtRepro 复现并验证修复:HOMEUSDT(负 carry,估计 -0.81%,基差 -7.23%)
// 旧 0.04 门会以「基差率过大」误挡;开启 0.08 有利上限后应放行。
func TestScanGateHomeusdtRepro(t *testing.T) {
	strategy := NewDefaultFundingStrategy()
	snap := FundingSnapshot{
		Symbol:          "HOMEUSDT",
		EstimatedRate:   -0.00807786,
		LastFundingRate: -0.00807786,
		BasisRate:       -0.07233871,
		Direction:       fundingCarryDirectionNegative,
		IsStale:         false,
	}
	profile := symbolRiskProfile{MinEstimatedRateMultiplier: 1, MinNetProfitMultiplier: 1}

	// 旧行为(无有利上限):基差闸误挡。
	cfgOld := EngineConfig{StrategyConfig: core.StrategyConfig{
		MaxScanBasisRate: 0.04, MinEstimatedFundingRate: 0.0034, FeeReserveRate: 0.0024,
		ExitSpreadReserveRate: 0.0005,
	}}
	cfgOld.RiskConfig.MinNetProfitRate = 0.0005
	if action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "HOMEUSDT"}, snap, profile, cfgOld); action != StrategyActionReject {
		t.Fatalf("旧配置应被基差闸挡下, got action=%v reason=%q", action, reason)
	}

	// 新行为(有利上限 0.08):基差 -7.23% 同向有利,放行(过基差闸后由方向/费率闸继续判定)。
	cfgNew := cfgOld
	cfgNew.StrategyConfig.FavorableBasisMaxRate = 0.08
	action, reason := strategy.EvaluateScanCandidate(FundingCandidate{Symbol: "HOMEUSDT"}, snap, profile, cfgNew)
	if action == StrategyActionReject && strings.Contains(reason, "基差率过大") {
		t.Fatalf("有利上限下不应再因基差被挡, reason=%q", reason)
	}
}
