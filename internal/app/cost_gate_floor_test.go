package app

import "testing"

// TestCoherentMinFundingRateEqualsBreakEven 断言「值得监控」的最低资金费率等于单周期对冲的真实盈亏
// 平衡线：往返费用预留 + 退出价差预留 + 健康净利门。
func TestCoherentMinFundingRateEqualsBreakEven(t *testing.T) {
	cfg := DefaultEngineConfig()
	want := cfg.StrategyConfig.FeeReserveRate + cfg.StrategyConfig.ExitSpreadReserveRate + cfg.RiskConfig.MinNetProfitRate
	if got := coherentMinFundingRate(cfg); diffTooLarge(got, want) {
		t.Fatalf("coherentMinFundingRate=%.6f want=%.6f", got, want)
	}
}

// TestDefaultFundingFloorIsCoherentWithCost 断言默认配置的海选地板线不再低于真实盈亏平衡线，
// 即默认值已与成本自洽，不会掩盖真实成本。
func TestDefaultFundingFloorIsCoherentWithCost(t *testing.T) {
	cfg := DefaultEngineConfig()
	if cfg.StrategyConfig.MinEstimatedFundingRate+1e-12 < coherentMinFundingRate(cfg) {
		t.Fatalf("默认地板线 %.6f 低于盈亏平衡线 %.6f，会放进结构性亏损单",
			cfg.StrategyConfig.MinEstimatedFundingRate, coherentMinFundingRate(cfg))
	}
}

// TestScanGateRejectsBelowBreakEvenFloor 断言即便配置把地板线设得过低（低于盈亏平衡线），
// 海选阶段也会用 coherentMinFundingRate 兜底拒掉结构性亏损单；只有越过平衡线的候选才放行。
func TestScanGateRejectsBelowBreakEvenFloor(t *testing.T) {
	cfg := DefaultEngineConfig()
	// 故意把粗筛地板线压到平衡线以下，模拟错误/过松配置。
	cfg.StrategyConfig.MinEstimatedFundingRate = 0.0025
	breakEven := coherentMinFundingRate(cfg)
	if breakEven <= 0.0025 {
		t.Fatalf("测试前提不成立：盈亏平衡线 %.6f 未高于被压低的地板线", breakEven)
	}

	strat := NewDefaultFundingStrategy()
	profile := symbolRiskProfile{MinEstimatedRateMultiplier: 1, MinNetProfitMultiplier: 1}
	candidate := FundingCandidate{Symbol: "BTCUSDT", Direction: fundingCarryDirectionPositive}

	// 高于被压低的地板线、但低于盈亏平衡线：必须被拒。
	below := FundingSnapshot{EstimatedRate: 0.0040, Direction: fundingCarryDirectionPositive}
	if action, detail := strat.EvaluateScanCandidate(candidate, below, profile, cfg); action != StrategyActionReject {
		t.Fatalf("低于盈亏平衡线应被拒绝, got action=%v detail=%s", action, detail)
	}

	// 越过盈亏平衡线：放行。
	above := FundingSnapshot{EstimatedRate: breakEven + 0.0005, Direction: fundingCarryDirectionPositive}
	if action, detail := strat.EvaluateScanCandidate(candidate, above, profile, cfg); action != StrategyActionOpen {
		t.Fatalf("越过盈亏平衡线应放行, got action=%v detail=%s", action, detail)
	}
}

func diffTooLarge(a, b float64) bool {
	d := a - b
	return d < -1e-12 || d > 1e-12
}
