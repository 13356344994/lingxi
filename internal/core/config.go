// Package core 持有被全项目共享的「内核」类型：运行配置及其校验/哈希。
//
// 把这些类型集中到一个不依赖任何上层（exchange/strategy/risk/execution/backtest）的
// 包里，是 Gate 3 解耦的前提——这样 strategy / risk 等纯逻辑包可以只依赖 core，
// 从而脱离网络与上帝对象做单测，而不会和 package main 形成依赖环。
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// capitalRampScheduleValid 校验灰度爬坡计划字符串：逗号分隔、每档为正、严格升序。
// （与 app.parseCapitalRampSchedule 保持同一规则，此处仅做配置期校验，避免 core→app 反向依赖。）
func capitalRampScheduleValid(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	prev := 0.0
	seen := false
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v <= 0 {
			return false
		}
		if seen && v <= prev {
			return false
		}
		prev = v
		seen = true
	}
	return seen
}

// ParseClusterMap 解析 R30 板块/相关性簇映射(PortfolioConfig.ClusterMap)。格式:分号分隔的「簇名=资产,资产,...」组,
// 如 "MAJORS=BTC,ETH;MEMES=DOGE,SHIB"。返回 base 资产(大写)→ 簇名(大写)的映射。空串返回空 map(不报错)。
// 同一资产被映射到多个簇视为非法(防歧义)。簇名或资产为空、缺 '=' 视为非法。
func ParseClusterMap(spec string) (map[string]string, error) {
	out := make(map[string]string)
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return out, nil
	}
	for _, group := range strings.Split(spec, ";") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		parts := strings.SplitN(group, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("ClusterMap 组缺少 '=': %q", group)
		}
		cluster := strings.ToUpper(strings.TrimSpace(parts[0]))
		if cluster == "" {
			return nil, fmt.Errorf("ClusterMap 簇名为空: %q", group)
		}
		assets := strings.Split(parts[1], ",")
		hasAsset := false
		for _, a := range assets {
			asset := strings.ToUpper(strings.TrimSpace(a))
			if asset == "" {
				continue
			}
			if existing, ok := out[asset]; ok && existing != cluster {
				return nil, fmt.Errorf("ClusterMap 资产 %s 被映射到多个簇(%s / %s)", asset, existing, cluster)
			}
			out[asset] = cluster
			hasAsset = true
		}
		if !hasAsset {
			return nil, fmt.Errorf("ClusterMap 簇 %s 无任何资产", cluster)
		}
	}
	return out, nil
}

// EngineProfile 描述系统的资金和运行偏好，主要用于一键切换不同资金规模或场景的参数包。
type EngineProfile string

const (
	// ProfileSmallCapital 小资金档：极低的并发，极低的单笔和日内预算，极高的异常灵敏度
	ProfileSmallCapital EngineProfile = "small_capital"
	// ProfileStandard 标准档：平衡的默认值，适合正常运行
	ProfileStandard EngineProfile = "standard"
	// ProfileResearch 研究档：较高并发，用于回放或历史模拟测试，对异常不敏感
	ProfileResearch EngineProfile = "research"
)

// NetworkConfig 表示网络、流和本地运行相关配置。
type NetworkConfig struct {
	HTTPTimeout              time.Duration `cfg:"http_timeout"`
	MaxPremiumIndexStaleness time.Duration `cfg:"max_premium_index_staleness"`
	StreamWarmupTimeout      time.Duration `cfg:"stream_warmup_timeout"`
	MaxIdleConns             int           `cfg:"max_idle_conns"`
	MaxIdleConnsPerHost      int           `cfg:"max_idle_conns_per_host"`
	IdleConnTimeout          time.Duration `cfg:"idle_conn_timeout"`
	TLSHandshakeTimeout      time.Duration `cfg:"tls_handshake_timeout"`
	ResponseHeaderTimeout    time.Duration `cfg:"response_header_timeout"`
	ExpectContinueTimeout    time.Duration `cfg:"expect_continue_timeout"`
	PublicGETBaseSlot        time.Duration `cfg:"public_get_base_slot"`
	PublicGETCooldown403     time.Duration `cfg:"public_get_cooldown_403"`
	PublicGETCooldown418     time.Duration `cfg:"public_get_cooldown_418"`
	PublicGETUseAPIKey       bool          `cfg:"public_get_use_apikey"`
	PublicGETAPIKeyWhitelist string        `cfg:"public_get_apikey_whitelist"`
	DashboardAddr            string        `cfg:"dashboard_addr"`
	RecordDir                string        `cfg:"record_dir"`
	EmergencyStopFile        string        `cfg:"emergency_stop_file"`
}

// StrategyConfig 表示扫描、监控和容量筛选配置。
type StrategyConfig struct {
	WakeMinute               int           `cfg:"wake_minute"`
	WakeSecond               int           `cfg:"wake_second"`
	ScanWindow               time.Duration `cfg:"scan_window"`
	MonitorInterval          time.Duration `cfg:"monitor_interval"`
	MonitorStopLead          time.Duration `cfg:"monitor_stop_lead"`
	LiveFundingDirectionMode string        `cfg:"live_funding_direction_mode"`
	MinEstimatedFundingRate  float64       `cfg:"min_estimated_funding_rate"`
	MaxEstimatedFundingRate  float64       `cfg:"max_estimated_funding_rate"`
	MaxFundingRateJump       float64       `cfg:"max_funding_rate_jump"`
	MaxScanBasisRate         float64       `cfg:"max_scan_basis_rate"`
	LiveFundingAsset         string        `cfg:"live_funding_asset"`
	FeeReserveRate           float64       `cfg:"fee_reserve_rate"`
	ExitSpreadReserveRate    float64       `cfg:"exit_spread_reserve_rate"`
	// ImpactReserveInGateEnabled 让开仓前净利闸始终扣除往返市场冲击储备(按可见盘口深度估计,薄盘=高冲击),
	// 与 DynamicSizingEnabled 解耦。默认 true(出厂最安全):不扣冲击的入场闸会系统性高估净利、在薄流动性上隐性亏损。
	// 置 false 退回「仅 DynamicSizing 开启时才扣冲击」的旧行为。
	ImpactReserveInGateEnabled bool `cfg:"impact_reserve_in_gate_enabled"`
	PreferredSpotQuoteAssets string        `cfg:"preferred_spot_quote_assets"`
	MaxTargets               int           `cfg:"max_targets"`
	PerSymbolQuoteUSDT       float64       `cfg:"per_symbol_quote_usdt"`
	RecentTradeVolumeWindow  time.Duration `cfg:"recent_trade_volume_window"`
	MaxRecentVolumeShare     float64       `cfg:"max_recent_volume_share"`
	MaxOpenInterestShare     float64       `cfg:"max_open_interest_share"`
	MaxVisibleDepthShare     float64       `cfg:"max_visible_depth_share"`
	DepthLevels              int           `cfg:"depth_levels"`
}

// ExecutionConfig 表示下单确认、恢复和退出执行参数。
type ExecutionConfig struct {
	ExitDelayAfterFunding    time.Duration `cfg:"exit_delay_after_funding"`
	ExitMonitorInterval      time.Duration `cfg:"exit_monitor_interval"`
	AutoBalanceUSDT          bool          `cfg:"auto_balance_usdt"`
	OpenPreparationLead      time.Duration `cfg:"open_preparation_lead"`
	PreOpenNoGoLead          time.Duration `cfg:"pre_open_no_go_lead"`
	PreOpenTickerMaxAge      time.Duration `cfg:"pre_open_ticker_max_age"`
	RecvWindow               int64         `cfg:"recv_window"`
	ExecutionConfirmTimeout  time.Duration `cfg:"execution_confirm_timeout"`
	ExecutionRecoveryTimeout time.Duration `cfg:"execution_recovery_timeout"`
	OpenHedgeMaxConcurrency  int           `cfg:"open_hedge_max_concurrency"`
	ParallelOpenSymbols      string        `cfg:"parallel_open_symbols"`
	ParallelCloseSymbols     string        `cfg:"parallel_close_symbols"`
	UseProtectedIOC          bool          `cfg:"use_protected_ioc"`
	SpotLimitSlippageRate    float64       `cfg:"spot_limit_slippage_rate"`
	FuturesLimitSlippageRate float64       `cfg:"futures_limit_slippage_rate"`
	FuturesPriceProtect      bool          `cfg:"futures_price_protect"`
	SpotSTPMode              string        `cfg:"spot_stp_mode"`
	FuturesSTPMode           string        `cfg:"futures_stp_mode"`
	FuturesMarginType        string        `cfg:"futures_margin_type"`
	FuturesLeverage          int           `cfg:"futures_leverage"`
	// ShadowTrading 为 true 时进入「影子(只读冒烟)」模式：引擎照常对接真实行情与账户、照常完成扫描/评估/
	// 决策，但任何会改变交易所状态或动用资金的签名请求(下单/撤单/改杠杆/划转/借还币等一切非 GET)都会在
	// 唯一咽喉点 signedRequest 被硬拦截、绝不触达交易所，只把下单意图落日志。用于实盘前抓灾难性 bug
	// (下错方向/异常数量/失控循环)，而非校准经济阈值。默认 false。
	ShadowTrading bool `cfg:"shadow_trading"`
	// --- S8-B 执行切片（TWAP/冰山，R13）。默认关闭：单发(现有行为)零变更。启用后,单腿名义超过
	// SliceThresholdNotionalUSDT 的开仓按 TWAP 等分切片(每片 ≤ MaxSliceNotionalUSDT、间隔 SliceIntervalMs、
	// 不超过 MaxSlices 片),平滑大单冲击。规划是纯函数(planExecutionSlices),已单测;实盘双腿 lockstep 执行
	// 接线在 flag 后分阶段灰度,需小额实盘校验(切片间不得产生裸腿)。---
	ExecutionSlicingEnabled    bool    `cfg:"execution_slicing_enabled"`
	SliceThresholdNotionalUSDT float64 `cfg:"slice_threshold_notional_usdt"`
	MaxSliceNotionalUSDT       float64 `cfg:"max_slice_notional_usdt"`
	SliceIntervalMs            int     `cfg:"slice_interval_ms"`
	MaxSlices                  int     `cfg:"max_slices"`
}

// RiskConfig 表示实盘风控阈值和冷却预算。
type RiskConfig struct {
	MinNetProfitRate            float64 `cfg:"min_net_profit_rate"`
	ImmediateInvalidateRate     float64 `cfg:"immediate_invalidate_rate"`
	MonitorMaxConsecutiveBreach int     `cfg:"monitor_max_consecutive_breach"`
	ResidualSpotAlertUSDT       float64 `cfg:"residual_spot_alert_usdt"`
	ResidualPositionAlertQty    float64 `cfg:"residual_position_alert_qty"`
	FundingIncomeDiffAlertUSDT  float64 `cfg:"funding_income_diff_alert_usdt"`
	SettlementDiffAlertUSDT     float64 `cfg:"settlement_diff_alert_usdt"`
	// 对账(reconcile,只读工具)阈值。SettlementDiffRate 为单字段相对偏差上限(0~1):设置后相对差
	// 成为「钱是否按模型比例赚到」的主信号,可抓出小名义大比例泄漏(R6)。SettlementTotalDiffAlertUSDT
	// 为「全部已配对周期净利偏差绝对值之和」上限(<=0 关闭),用于抓系统性慢性泄漏。
	SettlementDiffRate            float64       `cfg:"settlement_diff_rate"`
	SettlementTotalDiffAlertUSDT  float64       `cfg:"settlement_total_diff_alert_usdt"`
	MaxExecutionIncidents         int           `cfg:"max_execution_incidents"`
	MaxCycleLossUSDT              float64       `cfg:"max_cycle_loss_usdt"`
	MaxCycleOpenNotionalUSDT      float64       `cfg:"max_cycle_open_notional_usdt"`
	MaxOrderUncertaintyPerCycle   int           `cfg:"max_order_uncertainty_per_cycle"`
	MaxConsecutivePlatformRejects int           `cfg:"max_consecutive_platform_rejects"`
	MaxConsecutiveRequestErrors   int           `cfg:"max_consecutive_request_errors"`
	ProtectedIOCMinAttempts       int           `cfg:"protected_ioc_min_attempts"`
	ProtectedIOCMaxUnfilledRatio  float64       `cfg:"protected_ioc_max_unfilled_ratio"`
	ProtectedIOCMaxCancelRate     float64       `cfg:"protected_ioc_max_cancel_rate"`
	SymbolAbnormalCooldown        time.Duration `cfg:"symbol_abnormal_cooldown"`
	PlatformMinorCooldown         time.Duration `cfg:"platform_minor_cooldown"`
	PlatformAbnormalCooldown      time.Duration `cfg:"platform_abnormal_cooldown"`
	PlatformCriticalCooldown      time.Duration `cfg:"platform_critical_cooldown"`
	MaxRecoveryActionsPerCycle    int           `cfg:"max_recovery_actions_per_cycle"`
	MaxDailyLossUSDT              float64       `cfg:"max_daily_loss_usdt"`
	MaxDailyOrderCount            int           `cfg:"max_daily_order_count"`
	MaxDailyOpenNotionalUSDT      float64       `cfg:"max_daily_open_notional_usdt"`
	// MaxConsecutiveLossCycles 是实盘连续亏损熔断阈值：当「按 funding 实收结算后的真实净利」连续为负的
	// 周期数达到该值时，触发日级熔断、停止继续开仓，要求人工复核。0 表示关闭该维度。它对应回测里的
	// MaxConsecutiveLosses，把同一条系统性失效保护从回测补齐到实盘，且只会停机（fail-safe），不会下新单。
	MaxConsecutiveLossCycles int `cfg:"max_consecutive_loss_cycles"`
	// MaxAccountDrawdownUSDT 是账户级「高水位回撤」熔断阈值（USDT）：以全部已结算周期净利的累计曲线为准，
	// 当从历史最高累计净利(high-water mark)回撤的绝对额达到该值时停机待人工复核。它补的是 MaxDailyLossUSDT
	// 的盲区——日亏每个 UTC 日清零，抓不住「每天小亏、跨日慢性失血」累计掏空账户的情形。回撤额完全由已落盘
	// 结算记录回放得出，无需权益基线、不依赖现货 mark-to-market，重启后可重算复原。0 表示关闭该维度。
	MaxAccountDrawdownUSDT float64 `cfg:"max_account_drawdown_usdt"`
	// HedgeImbalanceAbortUSDT / HedgeImbalanceAbortRate 是 Gate 4 关键不变量守卫的阈值：
	// 当 |现货腿名义 − 合约腿名义| 的绝对值(USDT)或相对比例超过阈值时立即熔断，防止裸敞口。
	// 默认 0 表示该维度不启用（不改变既有行为，opt-in）。
	HedgeImbalanceAbortUSDT float64 `cfg:"hedge_imbalance_abort_usdt"`
	HedgeImbalanceAbortRate float64 `cfg:"hedge_imbalance_abort_rate"`
	// HoldHedgeGuardEnabled 把对冲偏离守卫从「仅开仓校验」扩展到「持有期周期性校验」(M0.2b)：
	// 启用后,持仓期常驻监控会按 HedgeImbalanceAbort{USDT,Rate} 阈值周期性复核两腿实时名义,
	// 偏离突破即对该 symbol 走方向感知双腿平仓(与强平/ADL 处置一致)。默认 false(opt-in,零行为变更)。
	// 当前仅对正 carry(现货腿在现货钱包,可实时测量)生效;负 carry(全仓杠杆借币空头)的持有期复核
	// 依赖 cross-margin 实时健康数据(S2.4)接入后再覆盖。
	HoldHedgeGuardEnabled bool `cfg:"hold_hedge_guard_enabled"`
	// FeedBlindnessGuardEnabled 启用「持有期断连盲窗守卫」(R32)：常驻监控以行情流(bookTicker 持续推送、
	// 判活可靠)为准驱动状态机——降级(degraded)即暂停本轮开新仓;持续失明(blind 超过宽限)即对托管敞口
	// 保守撤离(panic flatten 双腿平仓 + 平台熔断进入冷却,冷却结束自动恢复)。默认 false(opt-in,零行为变更)。
	FeedBlindnessGuardEnabled bool `cfg:"feed_blindness_guard_enabled"`
	// ConservativeFundingOnSignConflictEnabled 修正资金费估计在「符号冲突」时退回乐观值的隐患(R7):
	// 当前向估计(premium + interestRate)与上期实收 lastFundingRate 严格反号时,意味着资金费正在反转——
	// 旧逻辑此时把估计退回 lastFundingRate(旧方向、可能过度乐观),恰好在最危险时刻按旧方向开仓。
	// 启用后,符号冲突时估计取保守 0(候选不达标 → 本轮不开该 symbol),不在反转时刻押注旧方向。
	// 仅作用于实盘扫描/开仓路径;回测路径不受影响(保持历史可复现)。默认 false(opt-in,零行为变更)。
	ConservativeFundingOnSignConflictEnabled bool `cfg:"conservative_funding_on_sign_conflict_enabled"`
	// MinMarginLevel 是负 carry 保证金腿的最低全仓杠杆「保证金率(marginLevel = 总资产/总负债)」健康阈值(M0.3b-):
	// 启用后,持有负 carry(全仓杠杆借币空头)敞口期间常驻 REST 轮询 cross-margin 账户健康,marginLevel 跌破该
	// 阈值即保守撤离(panic flatten + 熔断冷却)。币安 marginLevel 触发追保≈1.3、强平≈1.1,建议设 1.5 留缓冲。
	// 默认 0 表示该维度不启用(opt-in,零行为变更)。
	MinMarginLevel float64 `cfg:"min_margin_level"`
	// MinLiquidationDistanceRate 是开仓前「强平距离」安全闸：估算合约腿开仓后从入场价到强平价可承受的
	// 逆向价格移动幅度（占入场价比例），低于该阈值则跳过该候选、不开仓（仅冷却该 symbol，非熔断）。
	// 距离用隔离保证金近似导出（可吸收浮亏 = 初始保证金 − 维持保证金，再除以名义），对全仓(CROSSED)模式
	// 是保守下界——全仓有整账户权益兜底，真实距离更大，因此用它作开仓前闸只会更安全。它防的是「杠杆相对
	// 持有期可能的价格波动设得过高、一波行情就把单腿合约打爆、对冲随之失效裸奔」。默认 0.15（15%）：
	// 配默认 2x 杠杆时实际距离约 49%，永不触发；只有当杠杆被调高到安全距离不足时才作为防爆刹车。
	// 0 表示关闭该维度。
	MinLiquidationDistanceRate float64 `cfg:"min_liquidation_distance_rate"`
	// MaxFundingSettlementAttempts / MaxFundingSettlementAge 是 R34「pending funding 处置」阈值(S5-E):
	// 资金费异步结算重试达 MaxFundingSettlementAttempts 次,或自创建挂起超过 MaxFundingSettlementAge 仍未到账,
	// 即升级为 critical 告警 + 停止静默重试(待人工确认),并把该 cycle 从连亏/回撤口径隔离(回退平仓时已记入的
	// 日内净利,避免一笔"funding 还没到账"的不完整周期误触发熔断)。任一为 0 表示该维度不启用。
	MaxFundingSettlementAttempts int           `cfg:"max_funding_settlement_attempts"`
	MaxFundingSettlementAge      time.Duration `cfg:"max_funding_settlement_age"`
	// EdgeWindowCycles / MinRealizedNetEdgeRate / EdgeGuardMinSamples 是 S6「策略健康守卫」(R27)参数:
	// 在最近 EdgeWindowCycles 个已结算周期上滚动统计实测净 edge 率(Σ净利/Σ名义),低于 MinRealizedNetEdgeRate
	// 且样本数 ≥ EdgeGuardMinSamples 时自动降档/停机(S6-B)。EdgeWindowCycles=0 关闭跟踪;MinRealizedNetEdgeRate=0
	// 关闭 edge 阈值闸(只跟踪不强制,默认如此 → 纯观测、对运行零影响,阈值待实盘标定后再开)。
	EdgeWindowCycles       int     `cfg:"edge_window_cycles"`
	MinRealizedNetEdgeRate float64 `cfg:"min_realized_net_edge_rate"`
	EdgeGuardMinSamples    int     `cfg:"edge_guard_min_samples"`
	// DeadMansSwitchEnabled / DeadMansSwitchCountdown 是 S7-B「交易所侧 dead-man 开关」(R14)参数:
	// 启用后引擎按 countdown/3 周期向 Binance 合约 countdownCancelAll 续期一个倒计时;若本进程崩溃/失联未能续期,
	// 交易所会在 countdown 到期后自动撤销该账户的全部挂单(兜底,防裸挂单)。优雅停机时主动撤销(countdownTime=0)。
	// DeadMansSwitchEnabled=false 关闭(默认);Countdown 须 ≥ 10s(过短易误触发)。
	DeadMansSwitchEnabled   bool          `cfg:"dead_mans_switch_enabled"`
	DeadMansSwitchCountdown time.Duration `cfg:"dead_mans_switch_countdown"`

	// BorrowCapacityCheckEnabled / BorrowCapacityMinHeadroomRate / MaxBorrowHourlyInterestRate 是 R29
	// 「借币容量/利率阶梯 pre-open 守卫」参数:负向 carry 开仓需借入 base 资产做空现货,开仓前校验
	// ①交易所最大可借量 ≥ 需借量 ×(1+MinHeadroomRate);②当前每小时借币利率 ≤ MaxBorrowHourlyInterestRate。
	// 任一不满足则该币本轮不开(从候选剔除,不留裸腿)。BorrowCapacityCheckEnabled=false 关闭(默认);
	// MaxBorrowHourlyInterestRate=0 表示不设利率上限(只查容量)。正向 carry 不借币,恒放行。
	BorrowCapacityCheckEnabled    bool    `cfg:"borrow_capacity_check_enabled"`
	BorrowCapacityMinHeadroomRate float64 `cfg:"borrow_capacity_min_headroom_rate"`
	MaxBorrowHourlyInterestRate   float64 `cfg:"max_borrow_hourly_interest_rate"`

	// RequireAPIKeyPermissionCheck / RequireIPWhitelist 是启动期 API Key 权限自检的安全姿态(R26):
	// RequireAPIKeyPermissionCheck=true(默认,出厂最安全)时,读取或解析 apiRestrictions 失败 → **拒绝启动**
	// (fail-close:最需要确认提现权限时不允许带未知状态开跑);设为 false 才退回「失败仅告警」的兼容行为
	// (用于不支持该端点的子账户 Key 类型)。RequireIPWhitelist=true 时未启用 IP 白名单(ipRestrict=false)→
	// 拒绝启动(硬门);默认 false(仅告警),建议实盘开启。提现权限恒为硬闸,与这两项无关。
	RequireAPIKeyPermissionCheck bool `cfg:"require_api_key_permission_check"`
	RequireIPWhitelist           bool `cfg:"require_ip_whitelist"`
}

// HistoricalConfig 表示月度模拟与历史验证配置。
type HistoricalConfig struct {
	DefaultLookbackDays                    int           `cfg:"default_lookback_days"`
	InitialEquityUSDT                      float64       `cfg:"initial_equity_usdt"`
	StopEquityUSDT                         float64       `cfg:"stop_equity_usdt"`
	PerLegNotionalUSDT                     float64       `cfg:"per_leg_notional_usdt"`
	FundingDirectionMode                   string        `cfg:"funding_direction_mode"`
	MaxFundingCyclesHeld                   int           `cfg:"max_funding_cycles_held"`
	BatchExperimentEnabled                 bool          `cfg:"batch_experiment_enabled"`
	BatchSpotTakerFeeRates                 string        `cfg:"batch_spot_taker_fee_rates"`
	BatchPerLegNotionalUSDT                string        `cfg:"batch_per_leg_notional_usdt"`
	BatchMaxFundingCyclesHeld              string        `cfg:"batch_max_funding_cycles_held"`
	MaxPerLegEquityShare                   float64       `cfg:"max_per_leg_equity_share"`
	SpotTakerFeeRate                       float64       `cfg:"spot_taker_fee_rate"`
	FuturesTakerFeeRate                    float64       `cfg:"futures_taker_fee_rate"`
	AssumedHourlyBorrowInterestRate        float64       `cfg:"assumed_hourly_borrow_interest_rate"`
	BorrowInterestMinBillingHours          int           `cfg:"borrow_interest_min_billing_hours"`
	NegativeCarrySpotEntryImpactMultiplier float64       `cfg:"negative_carry_spot_entry_impact_multiplier"`
	NegativeCarrySpotExitImpactMultiplier  float64       `cfg:"negative_carry_spot_exit_impact_multiplier"`
	CrossStableQuoteMaxDeviationRate       float64       `cfg:"cross_stable_quote_max_deviation_rate"`
	ResidualFillHaircutEnabled             bool          `cfg:"residual_fill_haircut_enabled"`
	ExpectedLegFillRatio                   float64       `cfg:"expected_leg_fill_ratio"`
	LegRejectProbability                   float64       `cfg:"leg_reject_probability"`
	EmergencyHedgeSpreadMultiplier         float64       `cfg:"emergency_hedge_spread_multiplier"`
	FundingPhaseLead                       time.Duration `cfg:"funding_phase_lead"`
	FundingFetchConcurrency                int           `cfg:"funding_fetch_concurrency"`
	FundingRetryMax                        int           `cfg:"funding_retry_max"`
	CacheDir                               string        `cfg:"cache_dir"`
	ReferenceKlineLookback                 int           `cfg:"reference_kline_lookback"`
	EstimateKlineLookback                  int           `cfg:"estimate_kline_lookback"`
	SafetyBufferFloorUSDT                  float64       `cfg:"safety_buffer_floor_usdt"`
	SafetyBufferCostMultiplier             float64       `cfg:"safety_buffer_cost_multiplier"`
	MaxDrawdownRate                        float64       `cfg:"max_drawdown_rate"`
	MaxConsecutiveLosses                   int           `cfg:"max_consecutive_losses"`
	MaxFundingRate                         float64       `cfg:"max_funding_rate"`
	MaxFundingRateJump                     float64       `cfg:"max_funding_rate_jump"`
	MaxSingleTradeLossUSDT                 float64       `cfg:"max_single_trade_loss_usdt"`
	FundingFetchSlot                       time.Duration `cfg:"funding_fetch_slot"`
	FundingFetchTimeout                    time.Duration `cfg:"funding_fetch_timeout"`
	GroupEvaluationTimeout                 time.Duration `cfg:"group_evaluation_timeout"`
	CandidateBuildTimeout                  time.Duration `cfg:"candidate_build_timeout"`
	CandidateFinalizeTimeout               time.Duration `cfg:"candidate_finalize_timeout"`
	ScreeningConcurrency                   int           `cfg:"screening_concurrency"`
	DeepCandidateLimit                     int           `cfg:"deep_candidate_limit"`
	CoarseSymbolLimit                      int           `cfg:"coarse_symbol_limit"`
	FundingGroupTopSymbolsPerTime          int           `cfg:"funding_group_top_symbols_per_time"`
	LiquidityShareMin                      float64       `cfg:"liquidity_share_min"`
	LiquidityShareFloor                    float64       `cfg:"liquidity_share_floor"`
	LiquidityShareMax                      float64       `cfg:"liquidity_share_max"`
	SevereLiquidityShareCap                float64       `cfg:"severe_liquidity_share_cap"`
	SevereLiquidityShareMultiplier         float64       `cfg:"severe_liquidity_share_multiplier"`
	SevereLiquidityShareMax                float64       `cfg:"severe_liquidity_share_max"`
}

// PortfolioConfig 表示「跨周期滚动持有的资金费 carry 组合 + 市场驱动的动态仓位管理」配置（见 README 第十六节）。
//
// 架构按 10W 目标一次设计到位，但所有行为开关默认关闭（opt-in）：EnableRollingHold / DynamicSizingEnabled 为
// false 时，引擎严格保持既有「单周期、静态 size」行为，本结构对运行零影响。实际投放档位由 MaxPortfolioNotionalUSDT
// 这一个旋钮随灰度计划（如 10000→30000→100000）调高，与架构进度解耦（见 §16.2.1）。
type PortfolioConfig struct {
	// EnableRollingHold 启用跨周期滚动持有：到期前按各币 funding 时钟做 keep/drop 复核，盈利续持省往返成本。默认 false。
	EnableRollingHold bool `cfg:"enable_rolling_hold"`
	// MaxPortfolioSymbols 组合最大同时持仓币数（L3）。默认 10。
	MaxPortfolioSymbols int `cfg:"max_portfolio_symbols"`
	// MaxPortfolioNotionalUSDT 组合最大总名义（L3，灰度旋钮）。0 表示不启用组合名义上限（默认）；架构上限 100000。
	MaxPortfolioNotionalUSDT float64 `cfg:"max_portfolio_notional_usdt"`
	// RevalidateLead 结算前续持复核提前量（即 :55）。默认 5min。
	RevalidateLead time.Duration `cfg:"revalidate_lead"`
	// HoldRollThresholdRate 续持门槛费率（远低于入场门槛，只需覆盖多持一周期的边际成本）。默认 0（待标定）。
	HoldRollThresholdRate float64 `cfg:"hold_roll_threshold_rate"`
	// RollSafetyBufferRate 续持安全垫：下期保守预期净资金费须明显为正（> buffer）才续持。默认 0（待标定）。
	RollSafetyBufferRate float64 `cfg:"roll_safety_buffer_rate"`
	// RebalanceHysteresisCostMultiplier 换仓滞后带（往返成本倍数，防 churn）。须 ≥ 1.0。默认 1.0。
	RebalanceHysteresisCostMultiplier float64 `cfg:"rebalance_hysteresis_cost_multiplier"`
	// DynamicSizingEnabled 启用市场驱动的容量定 size（depth/volume/OI/强平距离多重天花板取最小）。默认 false。
	DynamicSizingEnabled bool `cfg:"dynamic_sizing_enabled"`
	// PerSymbolHardCapUSDT 单币绝对硬顶（L1 尾部风险封顶）。0 表示按 MaxPortfolioNotionalUSDT/MaxPortfolioSymbols 起步。
	PerSymbolHardCapUSDT float64 `cfg:"per_symbol_hard_cap_usdt"`
	// ClusterMaxNotionalUSDT 单簇上限（L2，按 base 资产/板块聚类防高相关集中）。0 表示不启用（默认，待标定）。
	ClusterMaxNotionalUSDT float64 `cfg:"cluster_max_notional_usdt"`
	// ClusterMap 把 base 资产映射到「板块/相关性簇」(R30 真·相关性簇),格式:分号分隔的「簇名=资产,资产,...」组,
	// 如 "MAJORS=BTC,ETH;MEMES=DOGE,SHIB,PEPE;DEFI=UNI,AAVE"。未映射的资产回退到自身 base 资产(≈每币一簇,即旧行为)。
	// 空字符串=不启用板块映射(默认),此时簇键=base 资产。配合 ClusterMaxNotionalUSDT 才生效。
	ClusterMap string `cfg:"cluster_map"`
	// MaxBorrowNotionalPerAssetUSDT 负向 carry 单资产借币名义上限（撞可借量/利率阶梯前封顶）。0 表示不约束（默认，待标定）。
	MaxBorrowNotionalPerAssetUSDT float64 `cfg:"max_borrow_notional_per_asset_usdt"`
	// CapitalRampSchedule 灰度爬坡计划（逗号分隔，如 "10000,30000,100000"），由验证门逐档放行。默认空。
	CapitalRampSchedule string `cfg:"capital_ramp_schedule"`
	// --- S6-C 灰度门量化门槛（R33）。所有阈值默认 0 表示该项不参与判定（只观测、给建议，不强制）。---
	// RampPromoteMinNetEdgeRate 升档要求的实测净 edge 率下限。默认 0（不约束）。
	RampPromoteMinNetEdgeRate float64 `cfg:"ramp_promote_min_net_edge_rate"`
	// RampMaxSlippageRate 实测滑点率上限（超过→回退/阻止升档）。0=不约束。
	RampMaxSlippageRate float64 `cfg:"ramp_max_slippage_rate"`
	// RampMinFillRate 实测成交率下限（0..1，低于→回退/阻止升档）。0=不约束。
	RampMinFillRate float64 `cfg:"ramp_min_fill_rate"`
	// RampMinHealthyReconciles 升档要求的连续 HEALTHY 对账次数。0=不要求。
	RampMinHealthyReconciles int `cfg:"ramp_min_healthy_reconciles"`
	// AutoCapitalRampEnabled 是否允许引擎按灰度门「自动收紧（回退档位）」MaxPortfolioNotionalUSDT。默认 false：
	// 仅产出建议（日志 + 告警），实际放大/回退由人工确认后改参（资金投放是高后果决策，默认人工把关）。
	AutoCapitalRampEnabled bool `cfg:"auto_capital_ramp_enabled"`
}

// AlertingConfig 表示「关键事件实时告警」配置（S5 / R25）。默认 Enabled=false：禁用时引擎不做任何外部推送,
// 关键事件仍走既有日志/事件落盘路径,对运行零影响。启用后按 MinSeverity 分级、按 DedupWindow 去重,经 webhook /
// Telegram 等出口异步推送(fire-and-forget,不阻塞热路径);未配任何外部出口时退化为带 [ALERT] 前缀的本地日志出口。
type AlertingConfig struct {
	// Enabled 启用实时告警分发。默认 false。
	Enabled bool `cfg:"enabled"`
	// WebhookURL 通用 JSON webhook 出口(钉钉/飞书/自建均可)。空表示不启用该出口。
	WebhookURL string `cfg:"webhook_url"`
	// TelegramBotToken / TelegramChatID Telegram 机器人出口。两者皆非空时启用。
	TelegramBotToken string `cfg:"telegram_bot_token"`
	TelegramChatID   string `cfg:"telegram_chat_id"`
	// MinSeverity 推送的最低级别:info|warning|critical。默认 warning(只推 warning 及以上)。
	MinSeverity string `cfg:"min_severity"`
	// DedupWindow 同一(级别+标题+symbol)告警的去重窗口,窗口内重复告警被抑制。默认 5min;0 表示不去重。
	DedupWindow time.Duration `cfg:"dedup_window"`
	// DispatchTimeout 单次出口推送的超时。默认 5s。
	DispatchTimeout time.Duration `cfg:"dispatch_timeout"`
}

// EngineConfig 是 Funding Rate Sniper Engine 的 MVP 运行配置。
type EngineConfig struct {
	ActiveProfile EngineProfile `cfg:"active_profile"` // 运行偏好档位

	NetworkConfig    `cfg:"network"`
	StrategyConfig   `cfg:"strategy"`
	ExecutionConfig  `cfg:"execution"`
	RiskConfig       `cfg:"risk"`
	HistoricalConfig `cfg:"historical"`
	PortfolioConfig  `cfg:"portfolio"`
	AlertingConfig   `cfg:"alerting"`
}

// ApplyProfile 根据给定的运行档案，覆写当前配置的关键风控和容量参数
func (cfg *EngineConfig) ApplyProfile(profile EngineProfile) {
	cfg.ActiveProfile = profile
	switch profile {
	case ProfileSmallCapital:
		cfg.StrategyConfig.MaxTargets = 2
		cfg.StrategyConfig.PerSymbolQuoteUSDT = 50.0
		cfg.ExecutionConfig.OpenHedgeMaxConcurrency = 1
		cfg.RiskConfig.MaxCycleLossUSDT = 3.0
		cfg.RiskConfig.MaxDailyLossUSDT = 10.0
		cfg.RiskConfig.MaxDailyOpenNotionalUSDT = 500.0
		cfg.RiskConfig.MaxConsecutiveRequestErrors = 3
		cfg.RiskConfig.MaxConsecutivePlatformRejects = 2
		cfg.RiskConfig.MaxOrderUncertaintyPerCycle = 1
		cfg.RiskConfig.MaxConsecutiveLossCycles = 2
		cfg.RiskConfig.MaxAccountDrawdownUSDT = 25.0
	case ProfileResearch:
		cfg.StrategyConfig.MaxTargets = 20
		cfg.StrategyConfig.PerSymbolQuoteUSDT = 200.0
		cfg.ExecutionConfig.OpenHedgeMaxConcurrency = 5
		cfg.RiskConfig.MaxCycleLossUSDT = 50.0
		cfg.RiskConfig.MaxDailyLossUSDT = 200.0
		cfg.RiskConfig.MaxDailyOpenNotionalUSDT = 5000.0
		cfg.RiskConfig.MaxConsecutiveRequestErrors = 20
		cfg.RiskConfig.MaxConsecutivePlatformRejects = 10
		cfg.RiskConfig.MaxOrderUncertaintyPerCycle = 5
		cfg.RiskConfig.MaxConsecutiveLossCycles = 5
		cfg.RiskConfig.MaxAccountDrawdownUSDT = 400.0
	case ProfileStandard:
		// 保持默认值不变
	}
}

// ConfigHash 计算配置的稳定哈希，用于回测/实盘记录的可追溯元数据。
func (cfg EngineConfig) ConfigHash() string {
	payload := struct {
		Network    NetworkConfig    `json:"network"`
		Strategy   StrategyConfig   `json:"strategy"`
		Execution  ExecutionConfig  `json:"execution"`
		Risk       RiskConfig       `json:"risk"`
		Historical HistoricalConfig `json:"historical"`
	}{
		Network:    cfg.NetworkConfig,
		Strategy:   cfg.StrategyConfig,
		Execution:  cfg.ExecutionConfig,
		Risk:       cfg.RiskConfig,
		Historical: cfg.HistoricalConfig,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func isValidCoreSelfTradePreventionMode(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "NONE", "EXPIRE_TAKER", "EXPIRE_MAKER", "EXPIRE_BOTH":
		return true
	default:
		return false
	}
}

func isValidCoreLiveFundingDirectionMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "positive_only", "negative_only", "both":
		return true
	default:
		return false
	}
}

func isValidCoreHistoricalFundingDirectionMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "positive_only", "negative_only", "both":
		return true
	default:
		return false
	}
}

// Validate 校验运行配置是否处于安全可运行区间。
func (cfg EngineConfig) Validate() error {
	issues := make([]string, 0, 16)
	appendIssue := func(ok bool, format string, args ...any) {
		if ok {
			return
		}
		issues = append(issues, fmt.Sprintf(format, args...))
	}

	appendIssue(cfg.NetworkConfig.HTTPTimeout > 0, "HTTPTimeout 必须大于 0")
	appendIssue(strings.TrimSpace(cfg.NetworkConfig.DashboardAddr) != "", "DashboardAddr 不能为空")
	appendIssue(strings.TrimSpace(cfg.NetworkConfig.RecordDir) != "", "RecordDir 不能为空")
	appendIssue(cfg.NetworkConfig.MaxPremiumIndexStaleness > 0, "MaxPremiumIndexStaleness 必须大于 0")
	appendIssue(cfg.NetworkConfig.StreamWarmupTimeout > 0, "StreamWarmupTimeout 必须大于 0")
	appendIssue(cfg.NetworkConfig.MaxIdleConns > 0, "MaxIdleConns 必须大于 0")
	appendIssue(cfg.NetworkConfig.MaxIdleConnsPerHost > 0, "MaxIdleConnsPerHost 必须大于 0")
	appendIssue(cfg.NetworkConfig.MaxIdleConns >= cfg.NetworkConfig.MaxIdleConnsPerHost, "MaxIdleConns 不能小于 MaxIdleConnsPerHost")
	appendIssue(cfg.NetworkConfig.IdleConnTimeout > 0, "IdleConnTimeout 必须大于 0")
	appendIssue(cfg.NetworkConfig.TLSHandshakeTimeout > 0, "TLSHandshakeTimeout 必须大于 0")
	appendIssue(cfg.NetworkConfig.ResponseHeaderTimeout > 0, "ResponseHeaderTimeout 必须大于 0")
	appendIssue(cfg.NetworkConfig.ExpectContinueTimeout >= 0, "ExpectContinueTimeout 不能小于 0")
	appendIssue(cfg.NetworkConfig.PublicGETBaseSlot >= 0, "PublicGETBaseSlot 不能小于 0")
	appendIssue(cfg.NetworkConfig.PublicGETCooldown403 >= 0, "PublicGETCooldown403 不能小于 0")
	appendIssue(cfg.NetworkConfig.PublicGETCooldown418 >= 0, "PublicGETCooldown418 不能小于 0")
	appendIssue(cfg.NetworkConfig.PublicGETCooldown418 >= cfg.NetworkConfig.PublicGETCooldown403, "PublicGETCooldown418 不能小于 PublicGETCooldown403")

	appendIssue(cfg.StrategyConfig.WakeMinute >= 0 && cfg.StrategyConfig.WakeMinute <= 59, "WakeMinute 必须在 0 到 59 之间")
	appendIssue(cfg.StrategyConfig.WakeSecond >= 0 && cfg.StrategyConfig.WakeSecond <= 59, "WakeSecond 必须在 0 到 59 之间")
	appendIssue(cfg.StrategyConfig.ScanWindow > 0, "ScanWindow 必须大于 0")
	appendIssue(cfg.StrategyConfig.MonitorInterval > 0, "MonitorInterval 必须大于 0")
	appendIssue(cfg.StrategyConfig.MonitorStopLead > 0, "MonitorStopLead 必须大于 0")
	appendIssue(cfg.StrategyConfig.MonitorStopLead < cfg.StrategyConfig.ScanWindow, "MonitorStopLead 必须小于 ScanWindow")
	appendIssue(isValidCoreLiveFundingDirectionMode(cfg.StrategyConfig.LiveFundingDirectionMode), "LiveFundingDirectionMode 必须是 positive_only、negative_only 或 both")
	appendIssue(cfg.StrategyConfig.MinEstimatedFundingRate > 0, "MinEstimatedFundingRate 必须大于 0")
	appendIssue(cfg.StrategyConfig.MaxEstimatedFundingRate > cfg.StrategyConfig.MinEstimatedFundingRate, "MaxEstimatedFundingRate 必须大于 MinEstimatedFundingRate")
	appendIssue(cfg.StrategyConfig.MaxFundingRateJump > 0, "MaxFundingRateJump 必须大于 0")
	appendIssue(cfg.StrategyConfig.MaxScanBasisRate > 0, "MaxScanBasisRate 必须大于 0")
	appendIssue(strings.TrimSpace(cfg.StrategyConfig.LiveFundingAsset) != "", "LiveFundingAsset 不能为空")
	appendIssue(cfg.StrategyConfig.FeeReserveRate > 0, "FeeReserveRate 必须大于 0")
	appendIssue(cfg.StrategyConfig.ExitSpreadReserveRate >= 0, "ExitSpreadReserveRate 不能小于 0")
	appendIssue(strings.TrimSpace(cfg.StrategyConfig.PreferredSpotQuoteAssets) != "", "PreferredSpotQuoteAssets 不能为空")
	appendIssue(cfg.StrategyConfig.MaxTargets > 0, "MaxTargets 必须大于 0")
	appendIssue(cfg.StrategyConfig.PerSymbolQuoteUSDT > 0, "PerSymbolQuoteUSDT 必须大于 0")
	appendIssue(cfg.StrategyConfig.RecentTradeVolumeWindow > 0, "RecentTradeVolumeWindow 必须大于 0")
	appendIssue(cfg.StrategyConfig.MaxRecentVolumeShare > 0 && cfg.StrategyConfig.MaxRecentVolumeShare <= 1, "MaxRecentVolumeShare 必须在 (0, 1] 之间")
	appendIssue(cfg.StrategyConfig.MaxOpenInterestShare > 0 && cfg.StrategyConfig.MaxOpenInterestShare <= 1, "MaxOpenInterestShare 必须在 (0, 1] 之间")
	appendIssue(cfg.StrategyConfig.MaxVisibleDepthShare > 0 && cfg.StrategyConfig.MaxVisibleDepthShare <= 1, "MaxVisibleDepthShare 必须在 (0, 1] 之间")
	appendIssue(cfg.StrategyConfig.DepthLevels > 0, "DepthLevels 必须大于 0")

	appendIssue(cfg.ExecutionConfig.ExitDelayAfterFunding >= 0, "ExitDelayAfterFunding 不能小于 0")
	appendIssue(cfg.ExecutionConfig.ExitMonitorInterval > 0, "ExitMonitorInterval 必须大于 0")
	appendIssue(cfg.ExecutionConfig.OpenPreparationLead >= 0, "OpenPreparationLead 不能小于 0")
	appendIssue(cfg.ExecutionConfig.PreOpenNoGoLead >= 0, "PreOpenNoGoLead 不能小于 0")
	appendIssue(cfg.ExecutionConfig.PreOpenTickerMaxAge > 0, "PreOpenTickerMaxAge 必须大于 0")
	appendIssue(cfg.ExecutionConfig.OpenPreparationLead >= cfg.ExecutionConfig.PreOpenNoGoLead, "OpenPreparationLead 不能小于 PreOpenNoGoLead")
	appendIssue(cfg.StrategyConfig.MonitorStopLead > cfg.ExecutionConfig.OpenPreparationLead, "MonitorStopLead 必须大于 OpenPreparationLead")
	appendIssue(cfg.ExecutionConfig.RecvWindow > 0, "RecvWindow 必须大于 0")
	appendIssue(cfg.ExecutionConfig.ExecutionConfirmTimeout > 0, "ExecutionConfirmTimeout 必须大于 0")
	appendIssue(cfg.ExecutionConfig.ExecutionRecoveryTimeout > 0, "ExecutionRecoveryTimeout 必须大于 0")
	appendIssue(cfg.ExecutionConfig.OpenHedgeMaxConcurrency > 0, "OpenHedgeMaxConcurrency 必须大于 0")
	appendIssue(cfg.ExecutionConfig.SpotLimitSlippageRate >= 0 && cfg.ExecutionConfig.SpotLimitSlippageRate < 0.05, "SpotLimitSlippageRate 必须在 [0, 0.05) 之间")
	appendIssue(cfg.ExecutionConfig.FuturesLimitSlippageRate >= 0 && cfg.ExecutionConfig.FuturesLimitSlippageRate < 0.05, "FuturesLimitSlippageRate 必须在 [0, 0.05) 之间")
	appendIssue(isValidCoreSelfTradePreventionMode(cfg.ExecutionConfig.SpotSTPMode), "SpotSTPMode 必须是 EXPIRE_TAKER / EXPIRE_MAKER / EXPIRE_BOTH / NONE")
	appendIssue(isValidCoreSelfTradePreventionMode(cfg.ExecutionConfig.FuturesSTPMode), "FuturesSTPMode 必须是 EXPIRE_TAKER / EXPIRE_MAKER / EXPIRE_BOTH / NONE")
	appendIssue(strings.EqualFold(strings.TrimSpace(cfg.ExecutionConfig.FuturesMarginType), "CROSSED"), "FuturesMarginType 当前必须为 CROSSED")
	appendIssue(cfg.ExecutionConfig.FuturesLeverage >= 1 && cfg.ExecutionConfig.FuturesLeverage <= 20, "FuturesLeverage 必须在 1 到 20 之间")
	appendIssue(cfg.ExecutionConfig.SliceThresholdNotionalUSDT >= 0, "SliceThresholdNotionalUSDT 不能小于 0")
	appendIssue(cfg.ExecutionConfig.MaxSliceNotionalUSDT >= 0, "MaxSliceNotionalUSDT 不能小于 0")
	appendIssue(cfg.ExecutionConfig.SliceIntervalMs >= 0, "SliceIntervalMs 不能小于 0")
	appendIssue(cfg.ExecutionConfig.MaxSlices >= 0, "MaxSlices 不能小于 0")
	if cfg.ExecutionConfig.ExecutionSlicingEnabled {
		appendIssue(cfg.ExecutionConfig.MaxSliceNotionalUSDT > 0, "启用 ExecutionSlicingEnabled 时 MaxSliceNotionalUSDT 必须大于 0")
		appendIssue(cfg.ExecutionConfig.MaxSlices > 0, "启用 ExecutionSlicingEnabled 时 MaxSlices 必须大于 0")
	}
	appendIssue(cfg.RiskConfig.MaxConsecutiveLossCycles >= 0, "MaxConsecutiveLossCycles 不能小于 0（0 表示关闭连续亏损熔断）")
	appendIssue(cfg.RiskConfig.MaxAccountDrawdownUSDT >= 0, "MaxAccountDrawdownUSDT 不能小于 0（0 表示关闭账户级回撤熔断）")

	appendIssue(cfg.RiskConfig.MinNetProfitRate > 0, "MinNetProfitRate 必须大于 0")
	appendIssue(cfg.RiskConfig.ImmediateInvalidateRate > 0, "ImmediateInvalidateRate 必须大于 0")
	appendIssue(cfg.RiskConfig.ImmediateInvalidateRate < cfg.RiskConfig.MinNetProfitRate, "ImmediateInvalidateRate 必须小于 MinNetProfitRate")
	appendIssue(cfg.RiskConfig.MonitorMaxConsecutiveBreach > 0, "MonitorMaxConsecutiveBreach 必须大于 0")
	appendIssue(cfg.RiskConfig.ResidualSpotAlertUSDT >= 0, "ResidualSpotAlertUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.ResidualPositionAlertQty >= 0, "ResidualPositionAlertQty 不能小于 0")
	appendIssue(cfg.RiskConfig.FundingIncomeDiffAlertUSDT >= 0, "FundingIncomeDiffAlertUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.SettlementDiffAlertUSDT >= 0, "SettlementDiffAlertUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.SettlementDiffRate >= 0 && cfg.RiskConfig.SettlementDiffRate <= 1, "SettlementDiffRate 必须在 [0,1] 区间")
	appendIssue(cfg.RiskConfig.SettlementTotalDiffAlertUSDT >= 0, "SettlementTotalDiffAlertUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.MaxExecutionIncidents > 0, "MaxExecutionIncidents 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxCycleLossUSDT > 0, "MaxCycleLossUSDT 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0, "MaxCycleOpenNotionalUSDT 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxCycleOpenNotionalUSDT >= cfg.StrategyConfig.PerSymbolQuoteUSDT, "MaxCycleOpenNotionalUSDT 不能小于 PerSymbolQuoteUSDT")
	appendIssue(cfg.RiskConfig.MaxOrderUncertaintyPerCycle > 0, "MaxOrderUncertaintyPerCycle 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxConsecutivePlatformRejects > 0, "MaxConsecutivePlatformRejects 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxConsecutiveRequestErrors > 0, "MaxConsecutiveRequestErrors 必须大于 0")
	appendIssue(cfg.RiskConfig.ProtectedIOCMinAttempts > 0, "ProtectedIOCMinAttempts 必须大于 0")
	appendIssue(cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio > 0 && cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio <= 1, "ProtectedIOCMaxUnfilledRatio 必须在 (0, 1] 之间")
	appendIssue(cfg.RiskConfig.ProtectedIOCMaxCancelRate > 0 && cfg.RiskConfig.ProtectedIOCMaxCancelRate <= 1, "ProtectedIOCMaxCancelRate 必须在 (0, 1] 之间")
	appendIssue(cfg.RiskConfig.SymbolAbnormalCooldown > 0, "SymbolAbnormalCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.PlatformMinorCooldown > 0, "PlatformMinorCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.PlatformAbnormalCooldown > 0, "PlatformAbnormalCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.PlatformCriticalCooldown > 0, "PlatformCriticalCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.PlatformMinorCooldown <= cfg.RiskConfig.PlatformAbnormalCooldown, "PlatformMinorCooldown 不能大于 PlatformAbnormalCooldown")
	appendIssue(cfg.RiskConfig.PlatformCriticalCooldown >= cfg.RiskConfig.PlatformAbnormalCooldown, "PlatformCriticalCooldown 不能小于 PlatformAbnormalCooldown")
	appendIssue(cfg.RiskConfig.MaxDailyLossUSDT > 0, "MaxDailyLossUSDT 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxDailyOrderCount > 0, "MaxDailyOrderCount 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxDailyOpenNotionalUSDT > 0, "MaxDailyOpenNotionalUSDT 必须大于 0")
	appendIssue(cfg.RiskConfig.MaxDailyOpenNotionalUSDT >= cfg.RiskConfig.MaxCycleOpenNotionalUSDT, "MaxDailyOpenNotionalUSDT 不能小于 MaxCycleOpenNotionalUSDT")
	appendIssue(cfg.RiskConfig.MaxRecoveryActionsPerCycle > 0, "MaxRecoveryActionsPerCycle 必须大于 0")

	appendIssue(cfg.HistoricalConfig.DefaultLookbackDays > 0, "Historical DefaultLookbackDays 必须大于 0")
	appendIssue(cfg.HistoricalConfig.InitialEquityUSDT > 0, "Historical InitialEquityUSDT 必须大于 0")
	appendIssue(cfg.HistoricalConfig.StopEquityUSDT > 0 && cfg.HistoricalConfig.StopEquityUSDT < cfg.HistoricalConfig.InitialEquityUSDT, "Historical StopEquityUSDT 必须在 (0, InitialEquityUSDT) 之间")
	appendIssue(cfg.HistoricalConfig.PerLegNotionalUSDT > 0, "Historical PerLegNotionalUSDT 必须大于 0")
	appendIssue(isValidCoreHistoricalFundingDirectionMode(cfg.HistoricalConfig.FundingDirectionMode), "Historical FundingDirectionMode 必须是 positive_only、negative_only 或 both")
	appendIssue(cfg.HistoricalConfig.MaxFundingCyclesHeld > 0, "Historical MaxFundingCyclesHeld 必须大于 0")
	if cfg.HistoricalConfig.BatchExperimentEnabled {
		appendIssue(strings.TrimSpace(cfg.HistoricalConfig.BatchSpotTakerFeeRates) != "", "Historical BatchSpotTakerFeeRates 不能为空")
		appendIssue(strings.TrimSpace(cfg.HistoricalConfig.BatchPerLegNotionalUSDT) != "", "Historical BatchPerLegNotionalUSDT 不能为空")
		appendIssue(strings.TrimSpace(cfg.HistoricalConfig.BatchMaxFundingCyclesHeld) != "", "Historical BatchMaxFundingCyclesHeld 不能为空")
	}
	appendIssue(cfg.HistoricalConfig.MaxPerLegEquityShare > 0 && cfg.HistoricalConfig.MaxPerLegEquityShare <= 1, "Historical MaxPerLegEquityShare 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.SpotTakerFeeRate >= 0, "Historical SpotTakerFeeRate 不能小于 0")
	appendIssue(cfg.HistoricalConfig.FuturesTakerFeeRate >= 0, "Historical FuturesTakerFeeRate 不能小于 0")
	appendIssue(cfg.HistoricalConfig.AssumedHourlyBorrowInterestRate >= 0, "Historical AssumedHourlyBorrowInterestRate 不能小于 0")
	appendIssue(cfg.HistoricalConfig.BorrowInterestMinBillingHours > 0, "Historical BorrowInterestMinBillingHours 必须大于 0")
	appendIssue(cfg.HistoricalConfig.NegativeCarrySpotEntryImpactMultiplier >= 1, "Historical NegativeCarrySpotEntryImpactMultiplier 不能小于 1")
	appendIssue(cfg.HistoricalConfig.NegativeCarrySpotExitImpactMultiplier >= 1, "Historical NegativeCarrySpotExitImpactMultiplier 不能小于 1")
	appendIssue(cfg.HistoricalConfig.CrossStableQuoteMaxDeviationRate >= 0 && cfg.HistoricalConfig.CrossStableQuoteMaxDeviationRate < 0.2, "Historical CrossStableQuoteMaxDeviationRate 必须在 [0, 0.2) 之间")
	appendIssue(cfg.HistoricalConfig.ExpectedLegFillRatio > 0 && cfg.HistoricalConfig.ExpectedLegFillRatio <= 1, "Historical ExpectedLegFillRatio 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.LegRejectProbability >= 0 && cfg.HistoricalConfig.LegRejectProbability <= 1, "Historical LegRejectProbability 必须在 [0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.EmergencyHedgeSpreadMultiplier >= 1, "Historical EmergencyHedgeSpreadMultiplier 不能小于 1")
	appendIssue(cfg.HistoricalConfig.FundingPhaseLead > 0, "Historical FundingPhaseLead 必须大于 0")
	appendIssue(cfg.HistoricalConfig.FundingFetchConcurrency > 0, "Historical FundingFetchConcurrency 必须大于 0")
	appendIssue(cfg.HistoricalConfig.FundingRetryMax >= 0, "Historical FundingRetryMax 不能小于 0")
	appendIssue(cfg.HistoricalConfig.ReferenceKlineLookback > 0, "Historical ReferenceKlineLookback 必须大于 0")
	appendIssue(cfg.HistoricalConfig.EstimateKlineLookback > 0, "Historical EstimateKlineLookback 必须大于 0")
	appendIssue(cfg.HistoricalConfig.SafetyBufferFloorUSDT >= 0, "Historical SafetyBufferFloorUSDT 不能小于 0")
	appendIssue(cfg.HistoricalConfig.SafetyBufferCostMultiplier > 0, "Historical SafetyBufferCostMultiplier 必须大于 0")
	appendIssue(cfg.HistoricalConfig.MaxDrawdownRate > 0 && cfg.HistoricalConfig.MaxDrawdownRate < 1, "Historical MaxDrawdownRate 必须在 (0, 1) 之间")
	appendIssue(cfg.HistoricalConfig.MaxConsecutiveLosses > 0, "Historical MaxConsecutiveLosses 必须大于 0")
	appendIssue(cfg.HistoricalConfig.MaxFundingRate > cfg.StrategyConfig.MinEstimatedFundingRate, "Historical MaxFundingRate 必须大于策略 MinEstimatedFundingRate")
	appendIssue(cfg.HistoricalConfig.MaxFundingRateJump > 0, "Historical MaxFundingRateJump 必须大于 0")
	appendIssue(cfg.HistoricalConfig.MaxSingleTradeLossUSDT > 0, "Historical MaxSingleTradeLossUSDT 必须大于 0")
	appendIssue(cfg.HistoricalConfig.FundingFetchTimeout > 0, "Historical FundingFetchTimeout 必须大于 0")
	appendIssue(cfg.HistoricalConfig.GroupEvaluationTimeout > 0, "Historical GroupEvaluationTimeout 必须大于 0")
	appendIssue(cfg.HistoricalConfig.CandidateBuildTimeout > 0, "Historical CandidateBuildTimeout 必须大于 0")
	appendIssue(cfg.HistoricalConfig.CandidateFinalizeTimeout > 0, "Historical CandidateFinalizeTimeout 必须大于 0")
	appendIssue(cfg.HistoricalConfig.ScreeningConcurrency > 0, "Historical ScreeningConcurrency 必须大于 0")
	appendIssue(cfg.HistoricalConfig.DeepCandidateLimit > 0, "Historical DeepCandidateLimit 必须大于 0")
	appendIssue(cfg.HistoricalConfig.CoarseSymbolLimit > 0, "Historical CoarseSymbolLimit 必须大于 0")
	appendIssue(cfg.HistoricalConfig.FundingGroupTopSymbolsPerTime > 0, "Historical FundingGroupTopSymbolsPerTime 必须大于 0")
	appendIssue(cfg.HistoricalConfig.LiquidityShareMin > 0 && cfg.HistoricalConfig.LiquidityShareMin <= 1, "Historical LiquidityShareMin 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.LiquidityShareFloor > 0 && cfg.HistoricalConfig.LiquidityShareFloor <= 1, "Historical LiquidityShareFloor 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.LiquidityShareMax > 0 && cfg.HistoricalConfig.LiquidityShareMax <= 1, "Historical LiquidityShareMax 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.LiquidityShareFloor >= cfg.HistoricalConfig.LiquidityShareMin, "Historical LiquidityShareFloor 不能小于 LiquidityShareMin")
	appendIssue(cfg.HistoricalConfig.LiquidityShareMax >= cfg.HistoricalConfig.LiquidityShareFloor, "Historical LiquidityShareMax 不能小于 LiquidityShareFloor")
	appendIssue(cfg.HistoricalConfig.SevereLiquidityShareCap > 0 && cfg.HistoricalConfig.SevereLiquidityShareCap <= 1, "Historical SevereLiquidityShareCap 必须在 (0, 1] 之间")
	appendIssue(cfg.HistoricalConfig.SevereLiquidityShareMultiplier > 1, "Historical SevereLiquidityShareMultiplier 必须大于 1")
	appendIssue(cfg.HistoricalConfig.SevereLiquidityShareMax >= cfg.HistoricalConfig.SevereLiquidityShareCap, "Historical SevereLiquidityShareMax 不能小于 SevereLiquidityShareCap")

	// Portfolio：恒检非负 + 滞后带 ≥ 1.0（防 churn 的下界）；其余约束仅在对应开关启用时生效，关闭时对运行零影响。
	appendIssue(cfg.PortfolioConfig.MaxPortfolioSymbols >= 0, "Portfolio MaxPortfolioSymbols 不能小于 0")
	appendIssue(cfg.PortfolioConfig.MaxPortfolioNotionalUSDT >= 0, "Portfolio MaxPortfolioNotionalUSDT 不能小于 0（0 表示不启用组合名义上限）")
	appendIssue(cfg.PortfolioConfig.RevalidateLead >= 0, "Portfolio RevalidateLead 不能小于 0")
	appendIssue(cfg.PortfolioConfig.HoldRollThresholdRate >= 0, "Portfolio HoldRollThresholdRate 不能小于 0")
	appendIssue(cfg.PortfolioConfig.RollSafetyBufferRate >= 0, "Portfolio RollSafetyBufferRate 不能小于 0")
	appendIssue(cfg.PortfolioConfig.RebalanceHysteresisCostMultiplier >= 1.0, "Portfolio RebalanceHysteresisCostMultiplier 必须 ≥ 1.0（滞后带须至少覆盖完整往返成本以防 churn）")
	appendIssue(cfg.PortfolioConfig.PerSymbolHardCapUSDT >= 0, "Portfolio PerSymbolHardCapUSDT 不能小于 0")
	appendIssue(cfg.PortfolioConfig.ClusterMaxNotionalUSDT >= 0, "Portfolio ClusterMaxNotionalUSDT 不能小于 0")
	if _, err := ParseClusterMap(cfg.PortfolioConfig.ClusterMap); err != nil {
		appendIssue(false, "Portfolio ClusterMap 格式非法：%v", err)
	}
	appendIssue(cfg.PortfolioConfig.MaxBorrowNotionalPerAssetUSDT >= 0, "Portfolio MaxBorrowNotionalPerAssetUSDT 不能小于 0")
	appendIssue(cfg.PortfolioConfig.RampPromoteMinNetEdgeRate >= 0, "Portfolio RampPromoteMinNetEdgeRate 不能小于 0")
	appendIssue(cfg.PortfolioConfig.RampMaxSlippageRate >= 0, "Portfolio RampMaxSlippageRate 不能小于 0")
	appendIssue(cfg.PortfolioConfig.RampMinFillRate >= 0 && cfg.PortfolioConfig.RampMinFillRate <= 1, "Portfolio RampMinFillRate 必须在 [0,1]")
	appendIssue(cfg.PortfolioConfig.RampMinHealthyReconciles >= 0, "Portfolio RampMinHealthyReconciles 不能小于 0")
	if cfg.PortfolioConfig.CapitalRampSchedule != "" {
		appendIssue(capitalRampScheduleValid(cfg.PortfolioConfig.CapitalRampSchedule), "Portfolio CapitalRampSchedule 必须为正、严格升序的逗号分隔数值（如 10000,30000,100000）")
	}
	if cfg.PortfolioConfig.EnableRollingHold {
		appendIssue(cfg.PortfolioConfig.MaxPortfolioSymbols > 0, "启用 EnableRollingHold 时 MaxPortfolioSymbols 必须大于 0")
		appendIssue(cfg.PortfolioConfig.RevalidateLead > 0, "启用 EnableRollingHold 时 RevalidateLead 必须大于 0")
	}
	if cfg.PortfolioConfig.DynamicSizingEnabled {
		appendIssue(cfg.PortfolioConfig.PerSymbolHardCapUSDT > 0 || (cfg.PortfolioConfig.MaxPortfolioNotionalUSDT > 0 && cfg.PortfolioConfig.MaxPortfolioSymbols > 0),
			"启用 DynamicSizingEnabled 时必须能推导单币硬顶（设 PerSymbolHardCapUSDT>0，或同时设 MaxPortfolioNotionalUSDT>0 与 MaxPortfolioSymbols>0）")
	}

	// Alerting：去重窗口/超时非负；MinSeverity 必须是合法级别(空表示用默认 warning)。仅在启用时做更强校验。
	appendIssue(cfg.AlertingConfig.DedupWindow >= 0, "Alerting DedupWindow 不能小于 0")
	appendIssue(cfg.AlertingConfig.DispatchTimeout >= 0, "Alerting DispatchTimeout 不能小于 0")
	switch strings.ToLower(strings.TrimSpace(cfg.AlertingConfig.MinSeverity)) {
	case "", "info", "warning", "critical":
	default:
		appendIssue(false, "Alerting MinSeverity 必须是 info|warning|critical 之一")
	}

	// S5-E / S6 健康守卫:阈值恒非负(关闭维度用 0,不允许负数)。
	appendIssue(cfg.RiskConfig.MaxFundingSettlementAttempts >= 0, "Risk MaxFundingSettlementAttempts 不能小于 0")
	appendIssue(cfg.RiskConfig.MaxFundingSettlementAge >= 0, "Risk MaxFundingSettlementAge 不能小于 0")
	appendIssue(cfg.RiskConfig.EdgeWindowCycles >= 0, "Risk EdgeWindowCycles 不能小于 0")
	appendIssue(cfg.RiskConfig.MinRealizedNetEdgeRate >= 0, "Risk MinRealizedNetEdgeRate 不能小于 0")
	appendIssue(cfg.RiskConfig.EdgeGuardMinSamples >= 0, "Risk EdgeGuardMinSamples 不能小于 0")
	appendIssue(cfg.RiskConfig.DeadMansSwitchCountdown >= 0, "Risk DeadMansSwitchCountdown 不能小于 0")
	if cfg.RiskConfig.DeadMansSwitchEnabled {
		appendIssue(cfg.RiskConfig.DeadMansSwitchCountdown >= 10*time.Second, "启用 DeadMansSwitchEnabled 时 DeadMansSwitchCountdown 必须 ≥ 10s（过短易误触发交易所撤单）")
	}

	// R29 借币容量/利率守卫:余量率与利率上限恒非负(关闭利率上限用 0)。
	appendIssue(cfg.RiskConfig.BorrowCapacityMinHeadroomRate >= 0, "Risk BorrowCapacityMinHeadroomRate 不能小于 0")
	appendIssue(cfg.RiskConfig.MaxBorrowHourlyInterestRate >= 0, "Risk MaxBorrowHourlyInterestRate 不能小于 0")

	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("EngineConfig 校验失败: %s", strings.Join(issues, "; "))
}
