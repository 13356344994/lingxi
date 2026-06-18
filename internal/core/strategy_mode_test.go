package core

import (
	"math"
	"testing"
)

func TestParseStrategyMode(t *testing.T) {
	cases := []struct {
		in   string
		want StrategyMode
	}{
		{"", StrategyModeCollectData},
		{"collect_data", StrategyModeCollectData},
		{"CANARY_PROFIT", StrategyModeCanaryProfit},
		{"profit_seeking", StrategyModeProfitSeeking},
		{"bogus", StrategyModeCollectData},
	}
	for _, tc := range cases {
		if got := ParseStrategyMode(tc.in); got != tc.want {
			t.Fatalf("ParseStrategyMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestStrategyModeRunModeWhenLiveEnabled(t *testing.T) {
	if got := StrategyModeCollectData.RunModeWhenLiveEnabled(); got != RunModeLIVE {
		t.Fatalf("collect_data live=%q want LIVE", got)
	}
	if got := StrategyModeProfitSeeking.RunModeWhenLiveEnabled(); got != RunModeLIVE {
		t.Fatalf("profit_seeking live=%q want LIVE", got)
	}
	if got := StrategyModeCanaryProfit.RunModeWhenLiveEnabled(); got != RunModeCANARYLive {
		t.Fatalf("canary_profit live=%q want CANARY_LIVE", got)
	}
}

// relaxedCollectDataConfig 模拟现网 config.yaml 数据采集放松姿态(非 defaults 出厂值)。
func relaxedCollectDataConfig() EngineConfig {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.FeeReserveRate = 0.00001
	cfg.StrategyConfig.ExitSpreadReserveRate = 0
	cfg.StrategyConfig.AutoDeriveFeeReserveEnabled = false
	cfg.StrategyConfig.ImpactReserveInGateEnabled = false
	cfg.StrategyConfig.MaxScanBasisRate = 0.10
	cfg.StrategyConfig.FavorableBasisMaxRate = 0.10
	cfg.RiskConfig.MinNetProfitRate = 0.0001
	cfg.CrossVenueExecutor.OpenSpreadAPR = 0.35
	return cfg
}

func breakEvenFromConfig(cfg EngineConfig) float64 {
	fee := cfg.StrategyConfig.FeeReserveRate
	if fee < 0 {
		fee = 0
	}
	exit := cfg.StrategyConfig.ExitSpreadReserveRate
	if exit < 0 {
		exit = 0
	}
	minNet := cfg.RiskConfig.MinNetProfitRate
	if minNet < 0 {
		minNet = 0
	}
	return fee + exit + minNet
}

func TestApplyStrategyModeToEconomicsCollectDataPreservesRelaxed(t *testing.T) {
	cfg := relaxedCollectDataConfig()
	want := cfg
	ApplyStrategyModeToEconomics(&cfg, StrategyModeCollectData)
	if cfg.StrategyConfig.AutoDeriveFeeReserveEnabled != want.StrategyConfig.AutoDeriveFeeReserveEnabled {
		t.Fatalf("collect_data 不应改 auto_derive")
	}
	if cfg.StrategyConfig.ImpactReserveInGateEnabled != want.StrategyConfig.ImpactReserveInGateEnabled {
		t.Fatalf("collect_data 不应改 impact_reserve")
	}
	if math.Abs(cfg.RiskConfig.MinNetProfitRate-want.RiskConfig.MinNetProfitRate) > 1e-12 {
		t.Fatalf("collect_data min_net=%.6f want %.6f", cfg.RiskConfig.MinNetProfitRate, want.RiskConfig.MinNetProfitRate)
	}
	if math.Abs(cfg.StrategyConfig.MaxScanBasisRate-want.StrategyConfig.MaxScanBasisRate) > 1e-12 {
		t.Fatalf("collect_data max_scan_basis=%.6f want %.6f", cfg.StrategyConfig.MaxScanBasisRate, want.StrategyConfig.MaxScanBasisRate)
	}
	if math.Abs(cfg.CrossVenueExecutor.OpenSpreadAPR-want.CrossVenueExecutor.OpenSpreadAPR) > 1e-12 {
		t.Fatalf("collect_data open_spread_apr=%.6f want %.6f", cfg.CrossVenueExecutor.OpenSpreadAPR, want.CrossVenueExecutor.OpenSpreadAPR)
	}
}

func TestApplyStrategyModeToEconomicsCanaryProfitPreservesRelaxed(t *testing.T) {
	cfg := relaxedCollectDataConfig()
	want := cfg
	ApplyStrategyModeToEconomics(&cfg, StrategyModeCanaryProfit)
	if cfg.StrategyConfig.AutoDeriveFeeReserveEnabled != want.StrategyConfig.AutoDeriveFeeReserveEnabled {
		t.Fatalf("canary_profit 经济闸应与 collect_data 同放松")
	}
	if math.Abs(cfg.RiskConfig.MinNetProfitRate-want.RiskConfig.MinNetProfitRate) > 1e-12 {
		t.Fatalf("canary_profit min_net 应保持放松值")
	}
}

func TestApplyStrategyModeToEconomicsProfitSeekingForcesHonestGate(t *testing.T) {
	cfg := relaxedCollectDataConfig()
	collectBE := breakEvenFromConfig(cfg)
	ApplyStrategyModeToEconomics(&cfg, StrategyModeProfitSeeking)
	if !cfg.StrategyConfig.AutoDeriveFeeReserveEnabled {
		t.Fatal("profit_seeking 应强制 auto_derive_fee_reserve_enabled=true")
	}
	if !cfg.StrategyConfig.ImpactReserveInGateEnabled {
		t.Fatal("profit_seeking 应强制 impact_reserve_in_gate_enabled=true")
	}
	if cfg.RiskConfig.MinNetProfitRate < profitSeekingMinNetProfitRate {
		t.Fatalf("min_net=%.6f want >= %.6f", cfg.RiskConfig.MinNetProfitRate, profitSeekingMinNetProfitRate)
	}
	if math.Abs(cfg.StrategyConfig.MaxScanBasisRate-defaultMaxScanBasisRate) > 1e-12 {
		t.Fatalf("max_scan_basis_rate=%.6f want %.6f", cfg.StrategyConfig.MaxScanBasisRate, defaultMaxScanBasisRate)
	}
	if math.Abs(cfg.StrategyConfig.FavorableBasisMaxRate-defaultMaxScanBasisRate) > 1e-12 {
		t.Fatalf("favorable_basis_max_rate=%.6f want %.6f", cfg.StrategyConfig.FavorableBasisMaxRate, defaultMaxScanBasisRate)
	}
	if math.Abs(cfg.CrossVenueExecutor.OpenSpreadAPR-defaultOpenSpreadAPR) > 1e-12 {
		t.Fatalf("open_spread_apr=%.6f want %.6f", cfg.CrossVenueExecutor.OpenSpreadAPR, defaultOpenSpreadAPR)
	}
	profitBE := breakEvenFromConfig(cfg)
	if profitBE <= collectBE {
		t.Fatalf("profit_seeking BreakEven=%.6f 应高于 collect_data %.6f", profitBE, collectBE)
	}
}

func TestApplyStrategyModeToEconomicsProfitSeekingRespectsHigherMinNet(t *testing.T) {
	cfg := relaxedCollectDataConfig()
	cfg.RiskConfig.MinNetProfitRate = 0.005
	ApplyStrategyModeToEconomics(&cfg, StrategyModeProfitSeeking)
	if math.Abs(cfg.RiskConfig.MinNetProfitRate-0.005) > 1e-12 {
		t.Fatalf("用户 min_net 更高时不应下调, got %.6f", cfg.RiskConfig.MinNetProfitRate)
	}
}

func TestDefaultEngineConfigStrategyModeCollectData(t *testing.T) {
	cfg := DefaultEngineConfig()
	if ParseStrategyMode(cfg.StrategyConfig.StrategyMode) != StrategyModeCollectData {
		t.Fatalf("默认 strategy_mode=%q want collect_data", cfg.StrategyConfig.StrategyMode)
	}
}
