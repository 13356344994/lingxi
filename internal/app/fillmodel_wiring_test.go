package app

import (
	"math"
	"testing"

	"lingxi/internal/fillmodel"
)

// TestSharedCostModelParamsMatchBacktest 断言实盘引擎与回测从「同一份原始配置」装配出的残腿成本
// 模型参数逐字段一致，即 Gate 3 要求的「回测↔实盘共注入同一套成交/费用模型」单一装配点。
func TestSharedCostModelParamsMatchBacktest(t *testing.T) {
	raw := HistoricalConfig{
		ResidualFillHaircutEnabled:     true,
		ExpectedLegFillRatio:           0.97,
		LegRejectProbability:           0.02,
		EmergencyHedgeSpreadMultiplier: 5,
	}
	live, ok := newResidualCostModel(raw).(fillmodel.ResidualCostModel)
	if !ok {
		t.Fatalf("expected ResidualCostModel")
	}
	want := fillmodel.ResidualParams{Enabled: true, ExpectedLegFillRatio: 0.97, LegRejectProbability: 0.02, EmergencyHedgeMultiplier: 5}
	if live.Params != want {
		t.Fatalf("live params=%+v want %+v", live.Params, want)
	}
	// 完全未配置时与回测一致地默认开启 haircut 并套用缺省值。
	def, _ := newResidualCostModel(HistoricalConfig{}).(fillmodel.ResidualCostModel)
	if !def.Params.Enabled || def.Params.ExpectedLegFillRatio != 0.99 || def.Params.LegRejectProbability != 0.01 || def.Params.EmergencyHedgeMultiplier != 4.0 {
		t.Fatalf("default params not normalized like backtest: %+v", def.Params)
	}
}

// TestLiveResidualReserveReducesNetProfit 断言实盘盈利门禁接入共享成本模型后，会预留一笔正的残腿
// 成本（使净收益更保守），且模型关闭时预留归零。
func TestLiveResidualReserveReducesNetProfit(t *testing.T) {
	enabled := &FundingRateSniperEngine{
		costModel: newResidualCostModel(HistoricalConfig{}),
		cfg:       EngineConfig{StrategyConfig: StrategyConfig{FeeReserveRate: 0.0008, ExitSpreadReserveRate: 0.0004}},
	}
	rate, usdt := enabled.liveResidualReserveRate(10000, 0.0006)
	if rate <= 0 || usdt <= 0 {
		t.Fatalf("expected positive residual reserve, got rate=%v usdt=%v", rate, usdt)
	}
	if !math.IsNaN(rate) && math.Abs(rate-usdt/10000) > 1e-12 {
		t.Fatalf("rate should equal usdt/notional: rate=%v usdt=%v", rate, usdt)
	}

	disabled := &FundingRateSniperEngine{
		costModel: newResidualCostModel(HistoricalConfig{ResidualFillHaircutEnabled: false, ExpectedLegFillRatio: 1, EmergencyHedgeSpreadMultiplier: 1}),
		cfg:       EngineConfig{StrategyConfig: StrategyConfig{FeeReserveRate: 0.0008}},
	}
	if rate, usdt := disabled.liveResidualReserveRate(10000, 0.0006); rate != 0 || usdt != 0 {
		t.Fatalf("disabled model should reserve nothing, got rate=%v usdt=%v", rate, usdt)
	}

	// 零名义保护。
	if rate, usdt := enabled.liveResidualReserveRate(0, 0.0006); rate != 0 || usdt != 0 {
		t.Fatalf("zero notional should reserve nothing, got rate=%v usdt=%v", rate, usdt)
	}
}
