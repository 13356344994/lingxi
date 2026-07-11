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

	// 首战完全体重构期:遗留 carry/rotation 配置允许保留,但任何自动真钱入口必须封火。
	if cfg.UnifiedRotation.LiveTradingEnabled {
		t.Fatal("unified_rotation.live_trading_enabled = true want false(重构期禁止自动真钱)")
	}
	if math.Abs(cfg.UnifiedRotation.TotalDeployableUSDT-435) > 1e-9 {
		t.Fatalf("unified_rotation.total_deployable_usdt = %.2f want 435", cfg.UnifiedRotation.TotalDeployableUSDT)
	}
	if math.Abs(cfg.UnifiedRotation.MaxStrategyCapitalUsagePct) > 1e-12 {
		t.Fatalf("unified_rotation.max_strategy_capital_usage_pct = %.4f want 0", cfg.UnifiedRotation.MaxStrategyCapitalUsagePct)
	}
	if cfg.CrossVenueExecutor.LiveTradingEnabled {
		t.Fatal("cross_venue_executor.live_trading_enabled = true want false(重构期禁止自动真钱)")
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
	// 首战完全体重构期:采集/影子循环保留,真实新开仓总闸必须关闭。
	if !cfg.OKXDirectional.FastScalperV1Enabled {
		t.Fatal("fast_scalper_v1_enabled 应保持 true(采集与影子研究不断流)")
	}
	if cfg.OKXDirectional.LiveTradingEnabled || cfg.OKXDirectional.FastLiveEnabled {
		t.Fatal("okx_directional live_trading_enabled/fast_live_enabled 必须同时为 false")
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
	if cfg.OKXDirectional.FastAltCandidatesEnabled {
		t.Fatal("fast_alt_candidates_enabled = true want false(07-10 降载:无真钱/研究消费者,暂停重复shadow)")
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
	if cfg.OKXDirectional.FastLiqFollowShadowEnabled {
		t.Fatal("fast_liq_follow_shadow_enabled = true want false(07-10 降载:证伪形态原始样本已足)")
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
	// v6.5 E策略「捡便宜货」(2026-07-07 入枪;07-08 探针 9 笔;07-09 入场 K 折判死后探针停火)。
	// 通用探针形态铁律(07-09 定形):任何 fast 线 live=true 时,其 source 必须在探针名单
	// (fast_probe_source csv)或持证名单——单开 live 无身份=配置事故。
	if !cfg.OKXDirectional.FastXgapShadowEnabled {
		t.Fatal("fast_xgap_shadow_enabled = false want true(E策略影子样本继续攒=v3判决数据源)")
	}
	probeHasOrCert := func(src string) bool {
		return fastProbeHas(cfg.OKXDirectional.FastProbeSource, src) ||
			fastProbeHas(cfg.OKXDirectional.FastCertifiedSources, src)
	}
	if cfg.OKXDirectional.FastXgapLiveEnabled && !probeHasOrCert("xgap_rev") {
		t.Fatal("fast_xgap_live_enabled=true 但 xgap_rev 无探针/持证身份(live 只许以探针或持证形态开)")
	}
	if cfg.OKXDirectional.FastFireworkLiveEnabled && !probeHasOrCert("firework_pure") {
		t.Fatal("fast_firework_live_enabled=true 但 firework_pure 无探针/持证身份")
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
	// 全市场磁带探针未过增量视野门且当前无实弹消费者，降载关闭。
	if cfg.OKXDirectional.FastFullTapeEnabled {
		t.Fatal("fast_full_tape_enabled = true want false(07-10 降载)")
	}
	if cfg.OKXDirectional.FastHotWSEnabled || cfg.OKXDirectional.FastXMidEnabled {
		t.Fatal("fast_hot_ws/fast_xmid 应保持关闭(当前方向主线无消费者,避免恢复GB级写盘与跨所WS)")
	}
	// 探针制章程(2026-07-04,HANDOFF §-5):生产配置默认无探针活动;设置 fast_probe_source
	// = 用户明示批准某候选进入探针层(同时须保证预算硬顶为正)。
	if s := strings.TrimSpace(cfg.OKXDirectional.FastProbeSource); s != "" {
		t.Fatalf("fast_probe_source = %q want 空(重构期禁止任何自动真钱探针)", s)
	}
	// 持证闸(2026-07-08 ㊹-补2):当前持证=0;向名单写入任何 source=发证,须前向过门+用户批准。
	if s := strings.TrimSpace(cfg.OKXDirectional.FastCertifiedSources); s != "" {
		t.Fatalf("fast_certified_sources = %q want 空(持证=0;发证须前向过门+用户明示批准)", s)
	}
	// 烟花枪三特征切点锚定(07-09 K 折冻结:惯犯<3/力竭 5%/爆仓=0 写死在 fastFireworkPureSkip,
	// 此处锚定行为:全过样例必须放行、三个否决各自生效——切点漂移即红)。
	if fastFireworkPureSkip(2, 3, 100, 0) != "" || fastFireworkPureSkip(3, 3, 100, 0) == "" ||
		fastFireworkPureSkip(0, 6, 100, 0) == "" || fastFireworkPureSkip(0, 3, 100, 1) == "" {
		t.Fatal("烟花三特征闸切点漂移(冻结口径 3/0.05/0,改=作弊)")
	}
	// 烟花能量刻度出场(07-09 坟场翻案;18:13 用户批准"可以根据你的建议来"后改锚为 true):
	// 回退到 -40 固定线=回到已证死的坏尺子(双向中位-53),关它须新证据+用户明示。
	if !cfg.OKXDirectional.FastFireworkEnergyExitEnabled {
		t.Fatal("fast_firework_energy_exit_enabled = false want true(07-09 用户批准能量刻度;回退=回到被证死的坏尺子)")
	}
	// 能量出场切点锚定(1×unit 灾难/0.8×激活/0.4×回吐/cap600/unit∈[40,200];改=作弊):
	if fastFwEnergyUnit(94) != 94 || fastFwEnergyUnit(10) != 40 || fastFwEnergyUnit(999) != 200 ||
		fastLedgerFireworkExitES(-41, 0, 30, 75) != "" || fastLedgerFireworkExitES(-76, 0, 30, 75) != "disaster" ||
		fastLedgerFireworkExitES(29, 60, 120, 75) != "trail" || fastLedgerFireworkExitES(5, 50, 600, 75) != "max_hold" {
		t.Fatal("烟花能量出场切点漂移(冻结口径 1.0/0.8/0.4/600/[40,200],改=作弊)")
	}

	// cap 否决位(07-10 用户令四已批转实弹):负钉 cap 禁开空,回退=撤销保险栓须用户明示。
	if !cfg.OKXDirectional.FastCapShortVetoEnabled {
		t.Fatal("fast_cap_short_veto_enabled = false want true(07-10 用户批实弹;EDGE 首枪死因保险栓)")
	}
	// 否决位切点锚定:负钉 cap(f≤-0.95%)开空命中,其余不命中(改切点=作弊)。
	if !fastCapShortVeto("short", -0.0095, true) || fastCapShortVeto("short", -0.009, true) ||
		fastCapShortVeto("long", -0.02, true) || fastCapShortVeto("short", -0.02, false) {
		t.Fatal("cap 否决位切点漂移(冻结 short×f≤-0.95%×fundingOK)")
	}

	// 按格营业(07-10 用户令一):默认关(先攒预注册对照);切点锚定同 2 号冻结值。
	if cfg.OKXDirectional.FastKeyGridGateEnabled {
		t.Fatal("fast_key_grid_gate_enabled = true want false(先攒预注册对照,营业收窄须验后开)")
	}
	if cfg.OKXDirectional.FastKeyMaxConcurrentPerSrc <= 0 {
		t.Fatal("fast_key_max_concurrent_per_src 须 >0(防主角币独角戏,2号 LAB 教训)")
	}
	// 天气格切点锚定(2 号 v1 冻结 riot0.1611/breadth-0.01538/volx1.039;改=作弊):
	if g, ok := fastWeatherGrid(0.2, -0.02, 1.1, true, true); !ok || g != 5 {
		t.Fatalf("格5(暴动·跌·放量)判定漂移,得 g=%d ok=%v", g, ok)
	}
	if g, ok := fastWeatherGrid(0.05, -0.02, 0.9, true, true); !ok || g != 0 {
		t.Fatalf("格0(死水·跌·缩量)判定漂移,得 g=%d", g)
	}
	if !fastWxCascFadeInGrid(5, true) || fastWxCascFadeInGrid(7, true) {
		t.Fatal("casc_fade 营业格应=格5")
	}
	if !fastWxSqzFadeInGrid(0, true) || !fastWxSqzFadeInGrid(1, true) || fastWxSqzFadeInGrid(5, true) {
		t.Fatal("sqz_fade 营业格应=死水·跌(格0/1)")
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
