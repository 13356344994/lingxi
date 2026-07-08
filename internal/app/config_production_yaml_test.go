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
	if !cfg.OKXDirectional.FastAltCandidatesEnabled {
		t.Fatal("fast_alt_candidates_enabled = false want true(fade/liq shadow 候选=地图数据,必须持续生成)")
	}
	// fade 实盘 2026-07-03 21:10 解剖降级:maker 成交=力竭诊断证伪器(实盘35笔 -10.6bps vs 全体9032 +1.0bps@60s),
	// 群体优势付不起执行成本。回开=撤销解剖裁决,须用户明示。
	if cfg.OKXDirectional.FastFadeLiveEnabled {
		t.Fatal("fast_fade_live_enabled = true want false(成交即证伪,+1.0bps群体优势无可收割形态)")
	}
	if !cfg.OKXDirectional.FastSpeciesExitEnabled {
		t.Fatal("fast_species_exit_enabled = false want true(A物种minHold是刑具,3s认错止血)")
	}
	if !cfg.OKXDirectional.FastExitEnergyScaleEnabled {
		t.Fatal("fast_exit_energy_scale_enabled = false want true(高能池绝对阈值=0.8个噪声步长,止损收割自选能量)")
	}
	// v4 换枪(2026-07-03 用户拍板):scorer 必须 record-only;回开=撤销换枪决策,须用户明示。
	if cfg.OKXDirectional.FastScorerLiveEnabled {
		t.Fatal("fast_scorer_live_enabled = true want false(606笔:91%子弹3s顺向仅12%,60%无肉,逆选择税)")
	}
	// v5 证伪停火(2026-07-04 接手审计):信息集对齐后,实盘可开火时刻的样本 ex-LAB 全部子集为负
	// (适度带 ex-LAB n=16: 60s顺向率38%/-4.7bps);盲区闸+评估冷却=结构性迟到,进场时瀑布已跑完
	// -75~-440bps。三次出场修刀单笔经济性 -13.7→-22.0→-33.3bps/笔恶化,病根在入场时机。
	// 重开须:全市场mid标尺下以"簇诞生即评估"新信息集重做对齐研究 ex-LAB 净正 + 用户明示。
	if cfg.OKXDirectional.FastLiqFollowLiveEnabled {
		t.Fatal("fast_liq_follow_live_enabled = true want false(2026-07-04 证伪停火,重开须用户明示)")
	}
	if !cfg.OKXDirectional.FastLiqFollowShadowEnabled {
		t.Fatal("fast_liq_follow_shadow_enabled = false want true(停火后评估管线必须继续记 shadow=重开研究数据源)")
	}
	if !cfg.OKXDirectional.FastMarketMidsEnabled {
		t.Fatal("fast_market_mids_enabled = false want true(全市场mid标尺=盲区闸根治,重开研究前置)")
	}
	if !cfg.OKXDirectional.FastXvenueLiqEnabled {
		t.Fatal("fast_xvenue_liq_enabled = false want true(跨所强平流采集=频率正解的地图数据)")
	}
	// v6.1(2026-07-05 用户拍板"文档代码一块改了并上线"):研究覆盖债根治,两项均 record-only
	if !cfg.OKXDirectional.FastMarketMidsPersistEnabled {
		t.Fatal("fast_market_mids_persist_enabled = false want true(全市场mid落盘:tape复测路径覆盖仅40%/321暴动币61%无秒级视野的根治)")
	}
	if !cfg.OKXDirectional.FastHotPriorityEnabled {
		t.Fatal("fast_hot_priority_enabled = false want true(热币坑位怪兽优先:H-USDT 139次暴动0秒覆盖教训)")
	}
	// v6.2:tape 方向核对闸(定义级:确认被爆方向须与 burst 方向对号;错号6笔中位-10.7 实证)
	if !cfg.OKXDirectional.FastTapeSideMatchEnabled {
		t.Fatal("fast_tape_side_match_enabled = false want true(确认闸方向核对:错号=不是我们以为的那场强平)")
	}
	// v6.3:选择层代码已装但必须锁死(开=探针日与 live 开关一起,用户明示);切点锚定冻结值防静默调参
	if cfg.OKXDirectional.FastTapeSelEnabled || cfg.OKXDirectional.FastTapeSelBreakoutEnabled {
		t.Fatal("fast_tape_sel_* = true want false(选择层开=探针日用户明示;新样本判决未过前不许开)")
	}
	if cfg.OKXDirectional.FastTapeSelMinBaseUSDT != 50 || cfg.OKXDirectional.FastTapeSelMinNtlOIRatio != 0.0017 {
		t.Fatalf("选择层切点漂移(want 50/0.0017 got %.4g/%.4g)——切点=07-05 组合扫描冻结值,改=作弊",
			cfg.OKXDirectional.FastTapeSelMinBaseUSDT, cfg.OKXDirectional.FastTapeSelMinNtlOIRatio)
	}
	if cfg.OKXDirectional.FastLiqFollowMinImp15Bps < 3 {
		t.Fatalf("fast_liq_follow_min_imp15_bps = %.1f want >=3(瀑布存活确认,死瀑布=接反转)", cfg.OKXDirectional.FastLiqFollowMinImp15Bps)
	}
	// v4.1 方向不对称:跟多=替轧空站岗(-34bps@180s,实盘35笔-0.26U);回开须用户明示。
	if cfg.OKXDirectional.FastLiqFollowLongEnabled {
		t.Fatal("fast_liq_follow_long_enabled = true want false(空头爆=一次性回补,跟多接FOMO阴跌)")
	}
	if !cfg.OKXDirectional.OpenMicroRecordEnabled {
		t.Fatal("okx_directional.open_micro_record_enabled = false want true(runup 闸数据源)")
	}
	// v6 tape_burst 线(2026-07-05 预置):枪已造好但保险必须锁着——开火=用户批探针
	// (届时 fast_tape_live_enabled=true + fast_probe_source=tape_burst 同时设置)。
	if cfg.OKXDirectional.FastTapeLiveEnabled {
		t.Fatal("fast_tape_live_enabled = true want false(开火须用户明示批探针;当前应只记 shadow)")
	}
	// v6.5 E策略「捡便宜货」(2026-07-07 入枪):影子线开(前向判决攒样本),实弹锁死;
	// 参数=冻结口径(40/10/600/60/1800,先验预选),锚定防静默调参。
	if !cfg.OKXDirectional.FastXgapShadowEnabled {
		t.Fatal("fast_xgap_shadow_enabled = false want true(E策略前向判决要影子样本,n≥300 才裁决)")
	}
	if cfg.OKXDirectional.FastXgapLiveEnabled {
		t.Fatal("fast_xgap_live_enabled = true want false(开火=前向判决过+用户明示批探针)")
	}
	if cfg.OKXDirectional.FastXgapEntryGapBps != 40 || cfg.OKXDirectional.FastXgapExitGapBps != 10 ||
		cfg.OKXDirectional.FastXgapMaxHoldSeconds != 600 || cfg.OKXDirectional.FastXgapDisasterBps != 60 ||
		cfg.OKXDirectional.FastXgapCooldownSeconds != 1800 {
		t.Fatalf("E策略参数漂移(want 40/10/600/60/1800 got %.4g/%.4g/%d/%.4g/%d)——冻结口径,改=作弊",
			cfg.OKXDirectional.FastXgapEntryGapBps, cfg.OKXDirectional.FastXgapExitGapBps,
			cfg.OKXDirectional.FastXgapMaxHoldSeconds, cfg.OKXDirectional.FastXgapDisasterBps,
			cfg.OKXDirectional.FastXgapCooldownSeconds)
	}
	// Layer A 粒子账本(2026-07-07 粒子化宪法):record-only 基建,阅卷层的规则单一来源;关=账本断流。
	if !cfg.OKXDirectional.FastParticleLedgerEnabled {
		t.Fatal("fast_particle_ledger_enabled = false want true(Layer A 粒子账本=record-only 基建)")
	}
	// E枪格子三闸(2026-07-08 实盘日硬化):live 锁死期只影响 shadow 标注,探针开火时即真闸;
	// 关闭=探针将按无闸 v6.5 口径打(已被㉝判负的口径)=配置回退事故。
	if !cfg.OKXDirectional.FastXgapCellGatesEnabled {
		t.Fatal("fast_xgap_cell_gates_enabled = false want true(格子三闸:探针必须按格子口径打)")
	}
	// ㊳② 全市场磁带探针(2026-07-08):burst 第一波视野的数据源;关=增量视野断流。
	if !cfg.OKXDirectional.FastFullTapeEnabled {
		t.Fatal("fast_full_tape_enabled = false want true(全市场磁带探针=第一波视野 record-only 基建)")
	}
	// 探针制章程(2026-07-04,HANDOFF §-5):生产配置默认无探针活动;设置 fast_probe_source
	// = 用户明示批准某候选进入探针层(同时须保证预算硬顶为正)。
	if s := strings.TrimSpace(cfg.OKXDirectional.FastProbeSource); s != "" {
		if cfg.OKXDirectional.FastProbeMaxTrades <= 0 || cfg.OKXDirectional.FastProbeMaxLossUSDT <= 0 {
			t.Fatalf("探针 %q 激活但预算未锁死(max_trades=%d max_loss=%.2f 均须>0)", s,
				cfg.OKXDirectional.FastProbeMaxTrades, cfg.OKXDirectional.FastProbeMaxLossUSDT)
		}
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
