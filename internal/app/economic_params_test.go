package app

import (
	"math"
	"testing"
)

// TestDeriveFeeReserveRateEquivalence 锚定 P2.2 等价性:用币安默认/兜底 taker 费率(spot 0.0010 /
// futures 0.0005)派生的 FeeReserveRate 与改造前手填 0.0030 diff < ε(默认费率下进场闸零漂移)。
func TestDeriveFeeReserveRateEquivalence(t *testing.T) {
	cfg := DefaultEngineConfig()
	derived := deriveFeeReserveRate(cfg.HistoricalConfig.SpotTakerFeeRate, cfg.HistoricalConfig.FuturesTakerFeeRate)
	if math.Abs(derived-cfg.StrategyConfig.FeeReserveRate) > 1e-9 {
		t.Fatalf("默认费率派生 FeeReserveRate=%.6f 应≈手填 %.6f", derived, cfg.StrategyConfig.FeeReserveRate)
	}
	// BNB 抵扣后真实费率更低 → 派生值更小(经济闸据实放宽,捕获更多有效机会)。
	if got := deriveFeeReserveRate(0.00075, 0.00045); !(got < cfg.StrategyConfig.FeeReserveRate) {
		t.Fatalf("BNB 抵扣费率派生值 %.6f 应低于手填 %.6f", got, cfg.StrategyConfig.FeeReserveRate)
	}
}

// TestApplyDerivedEconomicParams 校验派生应用:开关开启 + 实拉费率合理时改写 FeeReserveRate 并刷新 econ;
// 关闭 或 费率不可用(≤0)时 fail-safe 保持手填值,闸不塌。
func TestApplyDerivedEconomicParams(t *testing.T) {
	// 开启 + 合理实拉费率:写回派生值,econ 同步。
	on := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	on.cfg.HistoricalConfig.SpotTakerFeeRate = 0.00075
	on.cfg.HistoricalConfig.FuturesTakerFeeRate = 0.00045
	on.applyDerivedEconomicParams()
	wantDerived := deriveFeeReserveRate(0.00075, 0.00045)
	if math.Abs(on.cfg.StrategyConfig.FeeReserveRate-wantDerived) > 1e-9 {
		t.Fatalf("应写回派生 FeeReserveRate %.6f, got %.6f", wantDerived, on.cfg.StrategyConfig.FeeReserveRate)
	}
	if math.Abs(on.economicParams().FeeReserveRate-wantDerived) > 1e-9 {
		t.Fatalf("econ 应同步派生值 %.6f, got %.6f", wantDerived, on.economicParams().FeeReserveRate)
	}
	// 策略层读 cfg 与引擎读 econ 口径一致(不漂移)。
	if math.Abs(coherentMinFundingRate(on.cfg)-on.economicParams().BreakEven()) > 1e-9 {
		t.Fatalf("策略层(cfg)与引擎(econ)BreakEven 口径漂移")
	}

	// 开关关闭:保持手填值。
	off := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	off.cfg.StrategyConfig.AutoDeriveFeeReserveEnabled = false
	off.cfg.HistoricalConfig.SpotTakerFeeRate = 0.00075
	off.cfg.HistoricalConfig.FuturesTakerFeeRate = 0.00045
	wantManual := off.cfg.StrategyConfig.FeeReserveRate
	off.applyDerivedEconomicParams()
	if math.Abs(off.cfg.StrategyConfig.FeeReserveRate-wantManual) > 1e-9 {
		t.Fatalf("关闭时应保持手填 %.6f, got %.6f", wantManual, off.cfg.StrategyConfig.FeeReserveRate)
	}

	// fail-safe:实拉费率不可用(0)→ 保持手填值,闸不塌成 0。
	bad := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	bad.cfg.HistoricalConfig.SpotTakerFeeRate = 0
	bad.cfg.HistoricalConfig.FuturesTakerFeeRate = 0
	wantManual2 := bad.cfg.StrategyConfig.FeeReserveRate
	bad.applyDerivedEconomicParams()
	if math.Abs(bad.cfg.StrategyConfig.FeeReserveRate-wantManual2) > 1e-9 {
		t.Fatalf("费率不可用时应保持手填 %.6f, got %.6f", wantManual2, bad.cfg.StrategyConfig.FeeReserveRate)
	}
}

// TestEconomicParamsFromConfigMirrorsConfig 锚定 P2.1:由 cfg 派生的 EconomicParams 各字段与全局配置
// 逐位一致(零行为变更的前提)。
func TestEconomicParamsFromConfigMirrorsConfig(t *testing.T) {
	cfg := DefaultEngineConfig()
	econ := economicParamsFromConfig(cfg)

	if diffTooLarge(econ.FeeReserveRate, cfg.StrategyConfig.FeeReserveRate) {
		t.Fatalf("FeeReserveRate=%.6f want=%.6f", econ.FeeReserveRate, cfg.StrategyConfig.FeeReserveRate)
	}
	if diffTooLarge(econ.ExitSpreadReserveRate, cfg.StrategyConfig.ExitSpreadReserveRate) {
		t.Fatalf("ExitSpreadReserveRate=%.6f want=%.6f", econ.ExitSpreadReserveRate, cfg.StrategyConfig.ExitSpreadReserveRate)
	}
	if diffTooLarge(econ.MinNetProfitRate, cfg.RiskConfig.MinNetProfitRate) {
		t.Fatalf("MinNetProfitRate=%.6f want=%.6f", econ.MinNetProfitRate, cfg.RiskConfig.MinNetProfitRate)
	}
	if diffTooLarge(econ.MaxEstimatedRate, cfg.StrategyConfig.MaxEstimatedFundingRate) {
		t.Fatalf("MaxEstimatedRate=%.6f want=%.6f", econ.MaxEstimatedRate, cfg.StrategyConfig.MaxEstimatedFundingRate)
	}
}

// TestBreakEvenEqualsLegacyFormula 锚定 P2.1 等价性:EconomicParams.BreakEven() 与旧 coherentMinFundingRate
// 的手写公式(FeeReserve + ExitSpreadReserve + MinNetProfit)逐位等价,且 coherentMinFundingRate 现委托给它。
func TestBreakEvenEqualsLegacyFormula(t *testing.T) {
	cfg := DefaultEngineConfig()
	legacy := maxFloat(cfg.StrategyConfig.FeeReserveRate, 0) +
		maxFloat(cfg.StrategyConfig.ExitSpreadReserveRate, 0) +
		maxFloat(cfg.RiskConfig.MinNetProfitRate, 0)

	if got := economicParamsFromConfig(cfg).BreakEven(); diffTooLarge(got, legacy) {
		t.Fatalf("BreakEven=%.6f want(legacy)=%.6f", got, legacy)
	}
	if got := coherentMinFundingRate(cfg); diffTooLarge(got, legacy) {
		t.Fatalf("coherentMinFundingRate=%.6f want(legacy)=%.6f", got, legacy)
	}
}

// TestEconomicParamsAccessorFallback 校验访问器:已实例化(FeeReserveRate>0)时原样返回引擎 econ;
// 未走构造器、econ 为零值的直建结构体测试则按 cfg 兜底派生,取值仍自洽。
func TestEconomicParamsAccessorFallback(t *testing.T) {
	cfg := DefaultEngineConfig()

	// 已实例化:返回引擎持有的 econ(此处构造一个可分辨的取值)。
	set := &FundingRateSniperEngine{cfg: cfg, econ: EconomicParams{FeeReserveRate: 0.0099, ExitSpreadReserveRate: 0.0001, MinNetProfitRate: 0.0002, MaxEstimatedRate: 0.05}}
	if got := set.economicParams(); diffTooLarge(got.FeeReserveRate, 0.0099) {
		t.Fatalf("已实例化应原样返回 econ, FeeReserveRate=%.6f want=0.0099", got.FeeReserveRate)
	}

	// 未实例化(econ 零值):按 cfg 兜底。
	unset := &FundingRateSniperEngine{cfg: cfg}
	if got := unset.economicParams(); diffTooLarge(got.FeeReserveRate, cfg.StrategyConfig.FeeReserveRate) {
		t.Fatalf("未实例化应按 cfg 兜底, FeeReserveRate=%.6f want=%.6f", got.FeeReserveRate, cfg.StrategyConfig.FeeReserveRate)
	}
	if diffTooLarge(unset.economicParams().BreakEven(), coherentMinFundingRate(cfg)) {
		t.Fatalf("兜底 BreakEven 应等于 coherentMinFundingRate")
	}
}
