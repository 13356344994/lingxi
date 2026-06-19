package app

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/core"
)

// TestLoadProductionConfigYAML 验证仓库根目录 config.yaml 能被 loadEngineConfig 正常加载,
// 且用户 2026-06-19 拍板的关键字段值与 profit_seeking 经济闸收紧生效。
func TestLoadProductionConfigYAML(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Skipf("未找到仓库根目录,跳过生产配置测试: %v", err)
	}
	configPath := filepath.Join(repoRoot, "config.yaml")
	if _, statErr := os.Stat(configPath); statErr != nil {
		t.Skipf("生产 config.yaml 不存在: %v", statErr)
	}

	t.Setenv("LINGXI_CONFIG", configPath)
	t.Setenv(allowUnknownConfigEnv, "") // 严格模式:未知键必须报错

	cfg, loadErr := loadEngineConfig()
	if loadErr != nil {
		t.Fatalf("loadEngineConfig(%s) error = %v", configPath, loadErr)
	}

	// strategy_mode
	if got := core.ParseStrategyMode(cfg.StrategyConfig.StrategyMode); got != core.StrategyModeProfitSeeking {
		t.Fatalf("strategy.strategy_mode = %q want profit_seeking", cfg.StrategyConfig.StrategyMode)
	}

	// unified_rotation 全局预算
	if math.Abs(cfg.UnifiedRotation.TotalDeployableUSDT-435) > 1e-9 {
		t.Fatalf("unified_rotation.total_deployable_usdt = %.2f want 435", cfg.UnifiedRotation.TotalDeployableUSDT)
	}
	if math.Abs(cfg.UnifiedRotation.MaxStrategyCapitalUsagePct) > 1e-12 {
		t.Fatalf("unified_rotation.max_strategy_capital_usage_pct = %.4f want 0", cfg.UnifiedRotation.MaxStrategyCapitalUsagePct)
	}

	// risk 账户级硬停
	if math.Abs(cfg.RiskConfig.MaxDailyLossUSDT-40) > 1e-9 {
		t.Fatalf("risk.max_daily_loss_usdt = %.2f want 40", cfg.RiskConfig.MaxDailyLossUSDT)
	}
	if math.Abs(cfg.RiskConfig.MaxAccountDrawdownUSDT-90) > 1e-9 {
		t.Fatalf("risk.max_account_drawdown_usdt = %.2f want 90", cfg.RiskConfig.MaxAccountDrawdownUSDT)
	}
	if math.Abs(cfg.RiskConfig.MaxStrategyCumulativeLossUSDT-30) > 1e-9 {
		t.Fatalf("risk.max_strategy_cumulative_loss_usdt = %.2f want 30", cfg.RiskConfig.MaxStrategyCumulativeLossUSDT)
	}
	if math.Abs(cfg.RiskConfig.MaxAccountOpenNotionalUSDT-2200) > 1e-9 {
		t.Fatalf("risk.max_account_open_notional_usdt = %.2f want 2200", cfg.RiskConfig.MaxAccountOpenNotionalUSDT)
	}
	// 用户 2026-06-20 拍板:启用击穿硬回撤自动平仓(P1-8)。
	if !cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled {
		t.Fatal("risk.auto_flatten_on_hard_drawdown_enabled = false want true")
	}

	// cross_venue_executor 杠杆/名义
	if math.Abs(cfg.CrossVenueExecutor.MaxPositionUSDT-150) > 1e-9 {
		t.Fatalf("cross_venue_executor.max_position_usdt = %.2f want 150", cfg.CrossVenueExecutor.MaxPositionUSDT)
	}
	if math.Abs(cfg.CrossVenueExecutor.MaxEffectiveLeverage-3) > 1e-9 {
		t.Fatalf("cross_venue_executor.max_effective_leverage = %.2f want 3", cfg.CrossVenueExecutor.MaxEffectiveLeverage)
	}
	if math.Abs(cfg.CrossVenueExecutor.MarginSafetyFactor-1.5) > 1e-9 {
		t.Fatalf("cross_venue_executor.margin_safety_factor = %.2f want 1.5", cfg.CrossVenueExecutor.MarginSafetyFactor)
	}

	// profit_seeking 经济闸收紧(ApplyStrategyModeToEconomics 在 loadEngineConfig 末尾已执行)
	if !cfg.StrategyConfig.AutoDeriveFeeReserveEnabled {
		t.Fatal("profit_seeking 应强制 strategy.auto_derive_fee_reserve_enabled=true")
	}
	if !cfg.StrategyConfig.ImpactReserveInGateEnabled {
		t.Fatal("profit_seeking 应强制 strategy.impact_reserve_in_gate_enabled=true")
	}
	if cfg.RiskConfig.MinNetProfitRate < 0.002 {
		t.Fatalf("profit_seeking risk.min_net_profit_rate = %.6f want >= 0.002", cfg.RiskConfig.MinNetProfitRate)
	}
	if math.Abs(cfg.StrategyConfig.MaxScanBasisRate-0.0015) > 1e-12 {
		t.Fatalf("profit_seeking strategy.max_scan_basis_rate = %.6f want 0.0015", cfg.StrategyConfig.MaxScanBasisRate)
	}
}

// findRepoRoot 从测试包目录向上查找含 go.mod 的仓库根。
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
