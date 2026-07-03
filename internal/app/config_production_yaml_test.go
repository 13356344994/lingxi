package app

import (
	"math"
	"os"
	"path/filepath"
	"strings"
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
	if !cfg.UnifiedRotation.LiveTradingEnabled {
		t.Fatal("unified_rotation.live_trading_enabled = false want true(生产配置应与 rotation_control active=true 意图一致)")
	}
	if math.Abs(cfg.UnifiedRotation.TotalDeployableUSDT-435) > 1e-9 {
		t.Fatalf("unified_rotation.total_deployable_usdt = %.2f want 435", cfg.UnifiedRotation.TotalDeployableUSDT)
	}
	if math.Abs(cfg.UnifiedRotation.MaxStrategyCapitalUsagePct) > 1e-12 {
		t.Fatalf("unified_rotation.max_strategy_capital_usage_pct = %.4f want 0", cfg.UnifiedRotation.MaxStrategyCapitalUsagePct)
	}

	// risk 账户级硬停
	// 2026-07-02 重校准:阈值按 88U 微仓探索档口径(旧值是 6/19 给 450U carry 定的)。
	if math.Abs(cfg.RiskConfig.MaxDailyLossUSDT-35) > 1e-9 {
		t.Fatalf("risk.max_daily_loss_usdt = %.2f want 35(2026-07-03 用户决策:须>fast 30%%本金学费预算)", cfg.RiskConfig.MaxDailyLossUSDT)
	}
	if math.Abs(cfg.RiskConfig.MaxAccountDrawdownUSDT-115) > 1e-9 {
		t.Fatalf("risk.max_account_drawdown_usdt = %.2f want 115", cfg.RiskConfig.MaxAccountDrawdownUSDT)
	}
	if math.Abs(cfg.RiskConfig.MaxStrategyCumulativeLossUSDT-80) > 1e-9 {
		t.Fatalf("risk.max_strategy_cumulative_loss_usdt = %.2f want 80", cfg.RiskConfig.MaxStrategyCumulativeLossUSDT)
	}
	if cfg.RiskConfig.DailyMaxLosingTrades != 0 {
		t.Fatalf("risk.daily_max_losing_trades = %d want 0(微仓 scalper 笔数维度无意义,金额维度才是刹车)", cfg.RiskConfig.DailyMaxLosingTrades)
	}
	if math.Abs(cfg.RiskConfig.MaxAccountOpenNotionalUSDT-2200) > 1e-9 {
		t.Fatalf("risk.max_account_open_notional_usdt = %.2f want 2200", cfg.RiskConfig.MaxAccountOpenNotionalUSDT)
	}
	// 2026-06-21 止血:二级 auto-flatten 暂时关闭。其触发依据(RealEquityPeak/Drawdown)目前坐在不可信的
	// 权益快照上(单样本毛刺设峰、残缺 venue 读把总额算成≈0),会把毛刺/提现误判成回撤而误平健康仓。
	// 待 equity_snapshots 质量闸 + fund-flow 调整后判据验证稳定后再恢复 true。
	if cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled {
		t.Fatal("risk.auto_flatten_on_hard_drawdown_enabled = true want false(2026-06-21 止血,权益质量闸验证前禁用)")
	}

	// okx_directional 方向引擎生产参数(2026-07-02 重建)
	if cfg.OKXDirectional.MaxConcurrent != 5 {
		t.Fatalf("okx_directional.max_concurrent = %d want 5(用户指令 2026-07-02 22:17)", cfg.OKXDirectional.MaxConcurrent)
	}
	if !cfg.OKXDirectional.MAEDisasterCutEnabled {
		t.Fatal("okx_directional.mae_disaster_cut_enabled = false want true")
	}
	if math.Abs(cfg.OKXDirectional.MAEDisasterCutPct-0.03) > 1e-9 {
		t.Fatalf("okx_directional.mae_disaster_cut_pct = %.4f want 0.03", cfg.OKXDirectional.MAEDisasterCutPct)
	}
	if !cfg.OKXDirectional.RunupConfirmFilterEnabled {
		t.Fatal("okx_directional.runup_confirm_filter_enabled = false want true")
	}
	// fast v2 重启(2026-07-03 用户决策):必须带全部 v2 护栏,缺一即视为配置回退事故。
	if !cfg.OKXDirectional.FastScalperV1Enabled || !cfg.OKXDirectional.FastLiveEnabled {
		t.Fatal("fast v2 应启用(fast_scalper_v1_enabled + fast_live_enabled 均须 true)")
	}
	if math.Abs(cfg.OKXDirectional.FastLiveNotionalUSDT-5) > 1e-9 {
		t.Fatalf("fast_live_notional_usdt = %.2f want 5(v2 灰度名义)", cfg.OKXDirectional.FastLiveNotionalUSDT)
	}
	if cfg.OKXDirectional.FastMinHoldSeconds < 15 || cfg.OKXDirectional.FastMinHoldSeconds > 60 {
		t.Fatalf("fast_min_hold_seconds = %d want [15,60](账本:0-30s 全负,30-60s PF≈2;<15 回 v1 绞肉机)", cfg.OKXDirectional.FastMinHoldSeconds)
	}
	if cfg.OKXDirectional.FastDisasterStopBps <= 0 {
		t.Fatal("fast_disaster_stop_bps 须 >0(minHold 期间唯一止损防线)")
	}
	if cfg.OKXDirectional.FastDailyLossPctOfCapital <= 0 && cfg.OKXDirectional.FastDailyLossLimitUSDT <= 0 {
		t.Fatal("fast 日熔断须启用(pct_of_capital 或 usdt 至少一个 >0;v2 学费上限)")
	}
	if v := strings.TrimSpace(cfg.OKXDirectional.FastScalperV1Venue); !strings.EqualFold(v, "okx") {
		t.Fatalf("fast_scalper_v1_venue = %q want okx(v2 仅灰度 OKX)", v)
	}
	// v3 三闸(2026-07-03 当日全开,用户拍板;回退即配置事故)
	if !cfg.OKXDirectional.FastPondFilterEnabled {
		t.Fatal("fast_pond_filter_enabled = false want true(死水池实证纯亏,不跟机构抢镜面盘)")
	}
	if cfg.OKXDirectional.FastPondMinEnergyBps < 6 {
		t.Fatalf("fast_pond_min_energy_bps = %.1f want >=6(好池分水岭≥9.6,死水<6)", cfg.OKXDirectional.FastPondMinEnergyBps)
	}
	if !cfg.OKXDirectional.FastAltCandidatesEnabled || !cfg.OKXDirectional.FastFadeLiveEnabled {
		t.Fatal("fade 接型灰度应启用(fast_alt_candidates_enabled + fast_fade_live_enabled 均须 true)")
	}
	if !cfg.OKXDirectional.FastSpeciesExitEnabled {
		t.Fatal("fast_species_exit_enabled = false want true(A物种minHold是刑具,3s认错止血)")
	}
	if !cfg.OKXDirectional.OpenMicroRecordEnabled {
		t.Fatal("okx_directional.open_micro_record_enabled = false want true(runup 闸数据源)")
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
