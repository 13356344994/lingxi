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

// ParseVenueList 解析逗号分隔 venue 名列表(小写归一、去空白)。空串返回 nil。
func ParseVenueList(spec string) map[string]struct{} {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	out := make(map[string]struct{})
	for _, part := range strings.Split(spec, ",") {
		v := strings.ToLower(strings.TrimSpace(part))
		if v == "" {
			continue
		}
		out[v] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BorrowDisabledSet 返回 BorrowDisabledVenues 解析后的集合。
func (c CrossVenueScannerConfig) BorrowDisabledSet() map[string]struct{} {
	return ParseVenueList(c.BorrowDisabledVenues)
}

// DirectionalVenueSet 返回 DirectionalVenues 解析后的集合(从 carry 中剔除的方向交易专用所)。
func (c CrossVenueScannerConfig) DirectionalVenueSet() map[string]struct{} {
	return ParseVenueList(c.DirectionalVenues)
}

// IsDirectionalVenue 报告 venue 是否被划为方向交易专用(不参与 carry)。
func (c CrossVenueScannerConfig) IsDirectionalVenue(venue string) bool {
	if c.DirectionalVenues == "" {
		return false
	}
	_, ok := c.DirectionalVenueSet()[strings.ToLower(strings.TrimSpace(venue))]
	return ok
}

// VenueBorrowCapable 报告 venue 是否允许借币探测与 perp-spot short-spot(不在 disabled 列表中)。
func (c CrossVenueScannerConfig) VenueBorrowCapable(venue string) bool {
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "" {
		return false
	}
	_, disabled := c.BorrowDisabledSet()[venue]
	return !disabled
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
	// GlobalEmergencyStopFile 是「全局急停总闸」路径(多所部署的双级急停之全局级,§7.4)。运行期由
	// buildVenueRuntime 注入为基记录目录下的 .halt-live(= 改造前的 records/.halt-live);引擎/启动检查
	// 既看本所 EmergencyStopFile(records/<venue>/.halt-live)又看本字段——任一存在即停。单所(币安)
	// 两者解析为同一路径,去重后行为与改造前逐字节一致。空 → 回退仅看 EmergencyStopFile(后向兼容)。
	GlobalEmergencyStopFile string `cfg:"global_emergency_stop_file"`
}

// StrategyConfig 表示扫描、监控和容量筛选配置。
type StrategyConfig struct {
	WakeMinute               int           `cfg:"wake_minute"`
	WakeSecond               int           `cfg:"wake_second"`
	ScanWindow               time.Duration `cfg:"scan_window"`
	MonitorInterval          time.Duration `cfg:"monitor_interval"`
	MonitorStopLead          time.Duration `cfg:"monitor_stop_lead"`
	LiveFundingDirectionMode string        `cfg:"live_funding_direction_mode"`
	// StrategyMode 统一策略运行模式(见 core.StrategyMode):collect_data(默认)/canary_profit/profit_seeking。
	// 驱动 RunMode 真钱规模、归因 run_mode 标签与成本闸严格度(ApplyStrategyModeToEconomics)。
	StrategyMode            string  `cfg:"strategy_mode"`
	MinEstimatedFundingRate float64 `cfg:"min_estimated_funding_rate"`
	// PositiveMinEstimatedFundingRate 是「正 carry(短永续+买现货,免借币)」专用的海选地板线,与负 carry
	// 解耦:正 carry 无借币利息、无可借性风险,理应用更低门槛。>0 时正 carry 走此值,负 carry 仍走
	// MinEstimatedFundingRate;0=关闭(正负共用 MinEstimatedFundingRate,行为不变)。无论取何值,都仍被
	// 真实盈亏平衡线(coherentMinFundingRate=往返费用+退出价差+健康净利门)硬兜底,绝不会放进结构性亏损单。
	PositiveMinEstimatedFundingRate float64 `cfg:"positive_min_estimated_funding_rate"`
	MaxEstimatedFundingRate         float64 `cfg:"max_estimated_funding_rate"`
	MaxFundingRateJump              float64 `cfg:"max_funding_rate_jump"`
	MaxScanBasisRate                float64 `cfg:"max_scan_basis_rate"`
	// FavorableBasisMaxRate 是「有利基差」专用的更高基差上限。基差与 carry 方向同向(负 carry=长永续+负基差,
	// 正 carry=短永续+正基差)时,该基差是 carry 仓位的方向性顺风(收敛即额外盈利),用此上限放宽;否则仍用
	// MaxScanBasisRate。0=关闭(回退到处处用 MaxScanBasisRate,行为不变)。仅在 ≥ MaxScanBasisRate 时生效。
	// 注意:这只放宽「扫描/监控的基差闸」,真实可成交性仍由下游深度/价差/成本守卫在执行期把关。
	FavorableBasisMaxRate float64 `cfg:"favorable_basis_max_rate"`
	LiveFundingAsset      string  `cfg:"live_funding_asset"`
	FeeReserveRate        float64 `cfg:"fee_reserve_rate"`
	ExitSpreadReserveRate float64 `cfg:"exit_spread_reserve_rate"`
	// AutoDeriveFeeReserveEnabled 开启后,引擎在启动实拉账户真实 taker 费率时,把 FeeReserveRate 重算为
	// 2*SpotTaker + 2*FuturesTaker(开+平 × 现货+合约 四腿往返;平台币抵扣已含在实拉费率里),取代手填常量,
	// 使进场/净利经济闸按「该账户真实成本」收紧或放宽(§6)。默认 true。fail-safe:费率拉取失败/异常(≤0)时
	// 保持手填值,绝不把闸塌成 0。置 false 可回退到纯手填 FeeReserveRate。
	AutoDeriveFeeReserveEnabled bool `cfg:"auto_derive_fee_reserve_enabled"`
	// ImpactReserveInGateEnabled 让开仓前净利闸始终扣除往返市场冲击储备(按可见盘口深度估计,薄盘=高冲击),
	// 与 DynamicSizingEnabled 解耦。默认 true(出厂最安全):不扣冲击的入场闸会系统性高估净利、在薄流动性上隐性亏损。
	// 置 false 退回「仅 DynamicSizing 开启时才扣冲击」的旧行为。
	ImpactReserveInGateEnabled bool          `cfg:"impact_reserve_in_gate_enabled"`
	PreferredSpotQuoteAssets   string        `cfg:"preferred_spot_quote_assets"`
	MaxTargets                 int           `cfg:"max_targets"`
	PerSymbolQuoteUSDT         float64       `cfg:"per_symbol_quote_usdt"`
	RecentTradeVolumeWindow    time.Duration `cfg:"recent_trade_volume_window"`
	MaxRecentVolumeShare       float64       `cfg:"max_recent_volume_share"`
	MaxOpenInterestShare       float64       `cfg:"max_open_interest_share"`
	MaxVisibleDepthShare       float64       `cfg:"max_visible_depth_share"`
	DepthLevels                int           `cfg:"depth_levels"`

	// CapPinnedFundingOverride* 处理「资金费撞交易所上下限(cap/floor)」这一极端但真实的行情:
	// 当某币本期实收资金费已贴住交易所封顶(如 -2% floor),前向溢价信号常因均值回归/插值而与之反号,
	// 触发 R7 保守化把预估费率归零 → 跳变 = |0 - last| 撞穿 MaxFundingRateJump → 该极端肥单被误拒。
	// 开启后:若 |lastFundingRate| 已达 symbol cap/floor 的 ProximityRate 倍且方向一致,则不再让 R7 归零,
	// 而是按 lastFundingRate × EstimateFactor 给出保守但非零的预估(撞限多为持续而非反转)。
	// CapPinnedFundingOverrideEnabled=false(默认,出厂最安全):完全保持既有 R7 行为,本豁免不生效。
	// ProximityRate∈(0,1]:贴限判定的接近度(0.9=达到上下限 90% 即视为撞限);EstimateFactor∈(0,1]:
	// 撞限时预估取 last 的倍率(1.0=全额信其持续,最激进;<1 则按比例打折更保守)。仅 Enabled=true 时校验后两者。
	CapPinnedFundingOverrideEnabled bool    `cfg:"cap_pinned_funding_override_enabled"`
	CapPinnedFundingProximityRate   float64 `cfg:"cap_pinned_funding_proximity_rate"`
	CapPinnedFundingEstimateFactor  float64 `cfg:"cap_pinned_funding_estimate_factor"`
}

// ExecutionConfig 表示下单确认、恢复和退出执行参数。
type ExecutionConfig struct {
	ExitDelayAfterFunding time.Duration `cfg:"exit_delay_after_funding"`
	ExitMonitorInterval   time.Duration `cfg:"exit_monitor_interval"`
	AutoBalanceUSDT       bool          `cfg:"auto_balance_usdt"`
	OpenPreparationLead   time.Duration `cfg:"open_preparation_lead"`
	PreOpenNoGoLead       time.Duration `cfg:"pre_open_no_go_lead"`
	PreOpenTickerMaxAge   time.Duration `cfg:"pre_open_ticker_max_age"`
	// PreOpenTickerRESTRefreshEnabled:战前 Go/No-Go 发现某腿 bookTicker 陈旧/缺失时,是否先用 REST 现拉一次
	// 真实最优买卖价补鲜后再复检(默认 true)。背景:Binance @bookTicker 是「最优买/卖价变化才推」的事件流,
	// 薄盘冷门币顶档长时间不动 → 该 symbol 久无 WS 推送,缓存只剩预热时 REST 补的旧价(实盘曾见 age≈2min 撞穿
	// PreOpenTickerMaxAge 误拒开仓)。补鲜只在本轮确有腿陈旧时触发,流动性好的标的零额外请求;REST 失败/仍陈旧
	// 则保留原拒因放弃开仓(fail-safe,绝不拿旧价裸开)。置 false 完全回退改造前行为(纯按缓存新鲜度判)。
	PreOpenTickerRESTRefreshEnabled bool          `cfg:"pre_open_ticker_rest_refresh_enabled"`
	RecvWindow                      int64         `cfg:"recv_window"`
	ExecutionConfirmTimeout         time.Duration `cfg:"execution_confirm_timeout"`
	ExecutionRecoveryTimeout        time.Duration `cfg:"execution_recovery_timeout"`
	OpenHedgeMaxConcurrency         int           `cfg:"open_hedge_max_concurrency"`
	ParallelOpenSymbols             string        `cfg:"parallel_open_symbols"`
	ParallelCloseSymbols            string        `cfg:"parallel_close_symbols"`
	UseProtectedIOC                 bool          `cfg:"use_protected_ioc"`
	SpotLimitSlippageRate           float64       `cfg:"spot_limit_slippage_rate"`
	FuturesLimitSlippageRate        float64       `cfg:"futures_limit_slippage_rate"`
	FuturesPriceProtect             bool          `cfg:"futures_price_protect"`
	SpotSTPMode                     string        `cfg:"spot_stp_mode"`
	FuturesSTPMode                  string        `cfg:"futures_stp_mode"`
	FuturesMarginType               string        `cfg:"futures_margin_type"`
	FuturesLeverage                 int           `cfg:"futures_leverage"`
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
	// AutoResumeOpenHedges 开启后:带仓重启时,若交易所残留与本地台账核对为「**完整且平衡**的 delta-neutral 对冲」
	// (两腿都在、各腿数量 ≈ 台账 HedgeQty,失衡在 HedgeImbalanceAbortRate 内),则自动重建持仓继续监控/平仓,
	// 而非拒启等人工 flatten。任一腿缺失/失衡/数量对不上 → 仍拒启交人工处理(失败方向偏安全)。默认 true。
	AutoResumeOpenHedges       bool    `cfg:"auto_resume_open_hedges"`
	FundingIncomeDiffAlertUSDT float64 `cfg:"funding_income_diff_alert_usdt"`
	SettlementDiffAlertUSDT    float64 `cfg:"settlement_diff_alert_usdt"`
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
	SymbolMinorCooldown           time.Duration `cfg:"symbol_minor_cooldown"`
	SymbolCriticalCooldown        time.Duration `cfg:"symbol_critical_cooldown"`
	PlatformMinorCooldown         time.Duration `cfg:"platform_minor_cooldown"`
	PlatformAbnormalCooldown      time.Duration `cfg:"platform_abnormal_cooldown"`
	PlatformCriticalCooldown      time.Duration `cfg:"platform_critical_cooldown"`
	MaxRecoveryActionsPerCycle    int           `cfg:"max_recovery_actions_per_cycle"`
	MaxDailyLossUSDT              float64       `cfg:"max_daily_loss_usdt"`
	// MaxDailyLossPct 日亏硬停占真实现金权益比例;>0 时有效阈值=max(MaxDailyLossUSDT, pct×RealEquityTotalUSDT);0=仅 USDT。
	MaxDailyLossPct          float64 `cfg:"max_daily_loss_pct"`
	MaxDailyOrderCount       int     `cfg:"max_daily_order_count"`
	MaxDailyOpenNotionalUSDT float64 `cfg:"max_daily_open_notional_usdt"`
	// DailyMaxLosingTrades 当日错次预算:单个 UTC+8 自然日内"已结算且净亏(NetPnL<0)"的笔数达到此值,
	// 即触发账户级一级硬停(停新开仓、在场仓继续 manage),需人工复核解除。补 MaxDailyLossUSDT 金额维度的盲区:
	// 哪怕每笔亏得不多,连续多笔亏说明当前行情/参数不利,应主动停手而非继续试错。0=关闭该维度。
	DailyMaxLosingTrades int `cfg:"daily_max_losing_trades"`
	// MaxConsecutiveLossCycles 是实盘连续亏损熔断阈值：当「按 funding 实收结算后的真实净利」连续为负的
	// 周期数达到该值时，触发日级熔断、停止继续开仓，要求人工复核。0 表示关闭该维度。它对应回测里的
	// MaxConsecutiveLosses，把同一条系统性失效保护从回测补齐到实盘，且只会停机（fail-safe），不会下新单。
	MaxConsecutiveLossCycles int `cfg:"max_consecutive_loss_cycles"`
	// MaxAccountDrawdownUSDT 是账户级「高水位回撤」熔断阈值（USDT）：以全部已结算周期净利的累计曲线为准，
	// 当从历史最高累计净利(high-water mark)回撤的绝对额达到该值时停机待人工复核。它补的是 MaxDailyLossUSDT
	// 的盲区——日亏每个 UTC 日清零，抓不住「每天小亏、跨日慢性失血」累计掏空账户的情形。回撤额完全由已落盘
	// 结算记录回放得出，无需权益基线、不依赖现货 mark-to-market，重启后可重算复原。0 表示关闭该维度。
	MaxAccountDrawdownUSDT float64 `cfg:"max_account_drawdown_usdt"`
	// MaxAccountDrawdownPct settlements 累计回撤占峰值权益比例;>0 时有效阈值=max(USDT, pct×RealEquityPeakUSDT);0=仅 USDT。
	MaxAccountDrawdownPct float64 `cfg:"max_account_drawdown_pct"`
	// MaxStrategyCumulativeLossUSDT 单策略累计已实现净亏硬停阈值(USDT):任一策略 settlements 累计净利
	// ≤ -该值时触发一级硬停(停新开仓,在场仓继续 manage)。0 表示关闭。默认 35(见 defaults.go)。
	MaxStrategyCumulativeLossUSDT float64 `cfg:"max_strategy_cumulative_loss_usdt"`
	// MaxStrategyCumulativeLossPct 单策略累计净亏占当前权益比例;>0 时有效阈值=max(USDT, pct×RealEquityTotalUSDT);0=仅 USDT。
	MaxStrategyCumulativeLossPct float64 `cfg:"max_strategy_cumulative_loss_pct"`
	// MaxAccountOpenNotionalUSDT 账户级在场总名义硬停上限(USDT):五策略 open notional 之和 ≥ 该值时
	// 触发一级硬停(非破坏性)。0 表示关闭。默认 1200(见 defaults.go)。
	MaxAccountOpenNotionalUSDT float64 `cfg:"max_account_open_notional_usdt"`
	// MaxGrossLeverage 在场总名义相对真实现金权益的总杠杆倍数;>0 时有效上限=max(USDT, leverage×RealEquityTotalUSDT)。
	MaxGrossLeverage float64 `cfg:"max_gross_leverage"`
	// AutoFlattenOnHardDrawdownEnabled 二级自动全平(opt-in):仅当 equity_snapshots 真实现金权益自高水位
	// 回撤 ≥ AutoFlattenAccountDrawdownUSDT 时 halt+flatten。默认 false(零行为变更)。
	AutoFlattenOnHardDrawdownEnabled bool `cfg:"auto_flatten_on_hard_drawdown_enabled"`
	// AutoFlattenAccountDrawdownUSDT 二级自动全平阈值(USDT):须明显高于 MaxAccountDrawdownUSDT;
	// 仅基于 equity_snapshots 真实现金回撤,不用 settlements(避免 HEI 类假亏误触发)。0 表示关闭。默认 150。
	AutoFlattenAccountDrawdownUSDT float64 `cfg:"auto_flatten_account_drawdown_usdt"`
	// AutoFlattenAccountDrawdownPct 二级全平占真实现金峰值权益比例;>0 时有效阈值=max(USDT, pct×RealEquityPeakUSDT);0=仅 USDT。
	AutoFlattenAccountDrawdownPct float64 `cfg:"auto_flatten_account_drawdown_pct"`
	// AccountRiskMinFundFlowStepUSDT 账户风控:权益单 tick 突变 ≥ 该值且无对应交易/持仓变化时,视为充提并平移日基准(不计亏损)。
	AccountRiskMinFundFlowStepUSDT float64 `cfg:"account_risk_min_fund_flow_step_usdt"`
	// AccountRiskMaxEquityTickDropUSDT 账户风控:相对上次可信权益,单 tick 最大允许降幅(USDT);超过且无持仓减少则视为数据不可靠、跳过评估。
	AccountRiskMaxEquityTickDropUSDT float64 `cfg:"account_risk_max_equity_tick_drop_usdt"`
	// AccountRiskMaxEquityTickDropPct 同上,占上次可信权益比例;>0 时阈值=max(USDT, pct×lastTrusted);0=仅 USDT。
	AccountRiskMaxEquityTickDropPct float64 `cfg:"account_risk_max_equity_tick_drop_pct"`
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
	// PanicFlattenHardStopEnabled 启用后:实时浮亏 panic-flatten 触发并完成双腿收敛后,引擎**永久硬停机**
	// (主循环 checkLiveSafetyGuards 返回 fatalEngineError),要求人工核对账户/原因后重启——而非默认的「平台熔断
	// 冷却结束自动复活继续交易」。出过一次实时浮亏急平这种严重事件后,停下等人确认更稳妥。
	// 默认 false(opt-in,零行为变更:保持熔断冷却自动复活)。⚠️ 数据采集姿态下若希望无人值守持续运行,保持 false。
	PanicFlattenHardStopEnabled bool `cfg:"panic_flatten_hard_stop_enabled"`
	// ConservativeFundingOnSignConflictEnabled 修正资金费估计在「符号冲突」时退回乐观值的隐患(R7):
	// 当前向估计(premium + interestRate)与上期实收 lastFundingRate 严格反号时,意味着资金费正在反转——
	// 旧逻辑此时把估计退回 lastFundingRate(旧方向、可能过度乐观),恰好在最危险时刻按旧方向开仓。
	// 启用后,符号冲突时估计取保守 0(候选不达标 → 本轮不开该 symbol),不在反转时刻押注旧方向。
	// 仅作用于实盘扫描/开仓路径;回测路径不受影响(保持历史可复现)。默认 false(opt-in,零行为变更)。
	ConservativeFundingOnSignConflictEnabled bool `cfg:"conservative_funding_on_sign_conflict_enabled"`
	// FundingReversalConfirmRatio 给 R7「符号冲突保守归零」加可信度门槛(R7b),修正 EDENUSDT 类误杀:
	// 仅当反向前瞻信号幅度 |premium+interestRate| ≥ |lastFundingRate| × ratio 时才认定为「真反转」并保守归零;
	// 否则视为预估结算价/盘口的瞬时毛刺,不否决既有强势费率(退回 lastFundingRate)。一个微弱反向瞬时信号
	// 不应否决一笔已强势成立的资金费(如上次实收 -0.585% 却被一个 +0.1% 毛刺归零)。
	// 仅在 ConservativeFundingOnSignConflictEnabled 启用时生效;≤0 表示不设门槛(任何反向冲突都归零,旧 R7 行为)。
	FundingReversalConfirmRatio float64 `cfg:"funding_reversal_confirm_ratio"`
	// ConflictingPremiumSignGuardEnabled 修正前瞻费率估计里「预估结算价基差仅凭绝对值更大就翻转前瞻符号」的
	// 缺陷(R7c,EDENUSDT 误杀根因之一):当预估结算价基差(settleBasis)与盘口基差(markBasis)反号时,
	// 启用后禁止仅因 settleBasis 绝对值更大就用它(从而翻转前瞻符号),改保留更实时直接的盘口基差。
	// 仅作用于实盘扫描/开仓路径;回测路径不受影响。默认 false(opt-in,零行为变更)。
	ConflictingPremiumSignGuardEnabled bool `cfg:"conflicting_premium_sign_guard_enabled"`
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
	// MinLiqDistanceVolScaleEnabled 启用后 minDist = base × volMultiplier(来自 SymbolVolatilityMultiplier hook)。
	// 默认 false(opt-in);未接入 vol 管道时行为与 base 相同。
	MinLiqDistanceVolScaleEnabled bool `cfg:"min_liq_distance_vol_scale_enabled"`
	// MinLiqDistanceVolScaleFactor vol 倍数附加系数;启用 vol 缩放时对 hook 返回值再乘此系数。默认 1.0。
	MinLiqDistanceVolScaleFactor float64 `cfg:"min_liq_distance_vol_scale_factor"`
	// RiskBudgetScaleEnabled 开启后 MaxExecutionIncidents 等固定次数预算随托管持仓数线性放大:
	// budget = base + RiskBudgetScalePerOpenPosition × len(activeOpenHedges)。默认 false(≈原固定值)。
	RiskBudgetScaleEnabled bool `cfg:"risk_budget_scale_enabled"`
	// RiskBudgetScalePerOpenPosition 每多一个托管持仓追加的预算次数;仅 RiskBudgetScaleEnabled 时生效。
	RiskBudgetScalePerOpenPosition int `cfg:"risk_budget_scale_per_open_position"`
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

	// BorrowUnavailableNonFatalEnabled:开仓时现货借币腿因「交易所借不到」(币安 -3045 系统无可借资产 /
	// -3006 超最大可借 / -3008 该资产禁止借入)失败、且合约腿已成功回补(账户已回平、无裸腿)时,
	// 把这类**已知良性失败**降级为「该币本轮跳过 + 置 symbol 冷却」,而**不**计入执行事故熔断计数。
	// 背景:HOME 等长尾负费率币恒被选成 Top 候选,但全仓杠杆池无该币可借 → 每周期连试 3 次同样借不到 →
	//   撞 MaxExecutionIncidents(3) 把整个引擎熔断停机。开启后这类币被冷却跳过,不再拖垮引擎。
	// 仅在合约腿已确认回补(reverted、账户平)时降级;若回补失败(裸腿)仍按致命事故处理。默认开启。
	BorrowUnavailableNonFatalEnabled bool `cfg:"borrow_unavailable_non_fatal_enabled"`

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
	SpotMakerFeeRate                       float64       `cfg:"spot_maker_fee_rate"`
	FuturesMakerFeeRate                    float64       `cfg:"futures_maker_fee_rate"`
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

// CrossVenueScannerConfig 是 P1 跨所资金费价差扫描器(纯只读)的运行参数。
type CrossVenueScannerConfig struct {
	// Enabled 启用周期性跨所资金费扫描。默认 true;false 时不启动 goroutine。
	Enabled bool `cfg:"enabled"`
	// ScanIntervalMinutes 扫描周期(分钟)。默认 10。
	ScanIntervalMinutes int `cfg:"scan_interval_minutes"`
	// MaxSnapshotAgeSeconds 执行层允许消费的最大 scanner 快照年龄(秒);超过则只管理在场仓,不再开新仓。默认 720。
	MaxSnapshotAgeSeconds int `cfg:"max_snapshot_age_seconds"`
	// MinSpreadAPR 进入结果列表的最小年化价差(小数,0.05=5%);仅作 listing 噪声过滤,开仓决策靠净收益闸。默认 0.05。
	MinSpreadAPR float64 `cfg:"min_spread_apr"`
	// AlertSpreadAPR 触发 Telegram 告警的年化价差阈值(小数,1.0=100%)。默认 1.0。
	AlertSpreadAPR float64 `cfg:"alert_spread_apr"`
	// TopN 每轮保留的年化价差 Top N 机会。默认 30。
	TopN int `cfg:"top_n"`
	// PanelDisplayTopN 面板/HTTP 展示 perp-perp 机会条数;快照仍保留全量供轮动消费。默认 30。
	PanelDisplayTopN int `cfg:"panel_display_top_n"`
	// BorrowProbeEnabled 每轮扫描后对头部机会做跨所借币可得性探测(纯只读)。默认 true。
	BorrowProbeEnabled bool `cfg:"borrow_probe_enabled"`
	// BorrowProbeTopN 借币探测覆盖的头部机会数(按 spread 排序)。默认 10。
	BorrowProbeTopN int `cfg:"borrow_probe_top_n"`
	// BorrowProbeFundingTopK 额外探测负资金费 symbol 数(按最负排序),供 perp-spot 轮动扫描。默认 60。
	BorrowProbeFundingTopK int `cfg:"borrow_probe_funding_top_k"`
	// BorrowProbeMaxLivePerScan 每轮扫描最大 live FetchBorrowQuote 次数(缓存命中不计)。默认 30。
	BorrowProbeMaxLivePerScan int `cfg:"borrow_probe_max_live_per_scan"`
	// BorrowKnownFreshnessHours 借币探测 confirmed-available 记忆窗口(小时);供 perp-spot not_borrowable 闸。默认 24。
	BorrowKnownFreshnessHours float64 `cfg:"borrow_known_freshness_hours"`
	// BorrowDefaultDailyRateWhenUnknown 借币可得但缺利率样本时的保守日利率(仅 EV 成本口径)。默认 0.001。
	BorrowDefaultDailyRateWhenUnknown float64 `cfg:"borrow_default_daily_rate_when_unknown"`
	// BorrowDisabledVenues 逗号分隔、禁止借币/short-spot 的 venue 列表(借币探测与 perp-spot 空现货均跳过)。
	// 默认 "gate"(单币种保证金账户无统一账户借币);留空表示全部 venue 可借。
	BorrowDisabledVenues string `cfg:"borrow_disabled_venues"`
	// DirectionalVenues 逗号分隔:从资金费 carry(扫描+跨所配对+单所轮动+统一资金池)中完全剔除、
	// 改作方向交易专用的 venue 列表。这些 venue 仍由 orchestrator 注册(供方向引擎使用)、资金独立核算,
	// 但绝不参与任何 carry 策略,避免 carry 与方向引擎同时占用同一所的资金/持仓。默认空。
	DirectionalVenues string `cfg:"directional_venues"`
	// FundingAPRSanityCap 单腿年化费率绝对值上限(小数,5.0=500%);超过视为尖峰/坏数据,截断后再参与价差/排序。
	FundingAPRSanityCap float64 `cfg:"funding_apr_sanity_cap"`
	// SpreadPersistenceScans 费率历史样本窗口(用于 HistoryQualityScore 软信号;不再作为存在性硬闸)。
	SpreadPersistenceScans int `cfg:"spread_persistence_scans"`
	// MinTradableNotionalUSDT 进入结果列表的最小可交易名义(USDT);两腿盘口深度共同决定的 TradableNotional 低于此值则丢弃。
	// 用于剔除盘口枯竭(如代币化美股)的伪机会,避免它们挤占 TopN。默认 20;≤0 表示不过滤。
	MinTradableNotionalUSDT float64 `cfg:"min_tradable_notional_usdt"`
	// DropOneSidedZeroFunding 丢弃任一腿资金费恰为 0 的"伪单边价差"(典型为 OKX 等所对代币化美股永续返回 0 费率)。
	// 真加密永续几乎总有非零资金费;为 0 多为非资费类工具或坏数据。默认 true。
	DropOneSidedZeroFunding bool `cfg:"drop_one_sided_zero_funding"`
}

// EngineConfig 是 Funding Rate Sniper Engine 的 MVP 运行配置。
type EngineConfig struct {
	ActiveProfile EngineProfile `cfg:"active_profile"` // 运行偏好档位

	// AllocatedCapitalUSDT 是「主观认定投入该所策略的资金」,**仅作绩效指标(夏普/索提诺/回撤等)的收益率分母**
	// (MULTI_VENUE_REFACTOR §9.1)。它**不**限制下单规模——实际下单由 strategy.per_symbol_quote_usdt 决定;
	// 两者口径须自行对齐(配小实放大→收益率高估,反之低估)。多所部署下由 venues.<v>.allocated_capital_usdt
	// 逐所覆盖;0 或未配 → 绩效接口判 Insufficient 分母(不静默猜)。
	AllocatedCapitalUSDT float64 `cfg:"allocated_capital_usdt"`

	NetworkConfig    `cfg:"network"`
	StrategyConfig   `cfg:"strategy"`
	ExecutionConfig  `cfg:"execution"`
	RiskConfig       `cfg:"risk"`
	HistoricalConfig `cfg:"historical"`
	PortfolioConfig  `cfg:"portfolio"`
	AlertingConfig   `cfg:"alerting"`

	// BybitExecutor 是 Bybit 精简执行器(Approach B:正费率 50/50 delta-neutral)的参数,仅 bybit 所读取,
	// 币安路径完全忽略。**2026-06-15 起单所引擎已退役**,策略总控默认不再自动启动;配置保留供回滚/对照。
	BybitExecutor BybitExecutorConfig `cfg:"bybit_executor"`

	// FundingExecutor 是 OKX/Gate 单所正费率执行器参数(venue 中立,仅 okx/gate 读取)。
	// **2026-06-15 起单所引擎已退役**,策略总控默认不再自动启动;配置保留供回滚/对照。
	FundingExecutor VenueFundingExecutorConfig `cfg:"funding_executor"`

	// CrossVenueScanner 是 P1 跨所资金费价差扫描器(纯只读,绝不下单)的参数。
	CrossVenueScanner CrossVenueScannerConfig `cfg:"cross_venue_scanner"`

	// CrossVenueExecutor 是 P1 跨所 perp-perp 对冲执行引擎参数(默认关闭+OFF 模式,绝不下单)。
	CrossVenueExecutor CrossVenueExecutorConfig `cfg:"cross_venue_executor"`

	// CapitalAllocator 是跨策略资本分配器参数(默认关闭+OFF 模式)。
	CapitalAllocator CapitalAllocatorConfig `cfg:"capital_allocator"`

	// DataCollection 是数据收集策略参数(默认关闭+OFF 模式)。
	DataCollection DataCollectionConfig `cfg:"data_collection"`

	// UnifiedRotation 是五策略统一轮动 runner 开关(默认关闭;面板 START+confirm=LIVE 后 active 即实盘)。
	UnifiedRotation UnifiedRotationConfig `cfg:"unified_rotation"`

	// OKXDirectional 是 OKX 方向交易引擎(独立于 carry):用资费筛币、价格 regime 定方向、资费做对齐闸,实盘双向。
	OKXDirectional OKXDirectionalConfig `cfg:"okx_directional"`
}

// OKXDirectionalConfig 是 OKX 方向交易引擎参数。OKX 已从 carry 剔除(directional_venues),资金独占。
// 选币:OKX 资费快照按 |费率| 排序取 TopN(被资费标记的币)。方向:K线 regime(趋势顺势/震荡反转,双向)。
// 资费对齐闸:只在资费不逆持仓时开(多要费率≤阈值、空要费率≥−阈值),令持仓顺带收资费、绝不逆扛。实盘 + 5X + 小额。
type OKXDirectionalConfig struct {
	// Enabled 启用引擎主循环。默认 false。
	Enabled bool `cfg:"enabled"`
	// LiveTradingEnabled 主安全闸:false=只算信号不下单;true=真钱。默认 false。
	LiveTradingEnabled bool `cfg:"live_trading_enabled"`
	// LoopIntervalSeconds 主循环周期(秒)。默认 90。
	LoopIntervalSeconds int `cfg:"loop_interval_seconds"`
	// NotionalUSDT 单仓名义(USDT)。默认 30。
	NotionalUSDT float64 `cfg:"notional_usdt"`
	// Leverage 永续杠杆。默认 5。
	Leverage int `cfg:"leverage"`
	// MaxConcurrent 最大并发持仓数(多币)。每仓本金 = 交易仓本金 / MaxConcurrent,总名义满仓=本金×杠杆。默认 3。
	MaxConcurrent int `cfg:"max_concurrent"`
	// UniverseScanIntervalMinutes 全场扫描周期(分钟):每隔此时长重算 shortlist(综合分)。默认 30。
	UniverseScanIntervalMinutes int `cfg:"universe_scan_interval_minutes"`
	// CandidatePoolN 全场扫描时按 |费率| 取前 N 进入深度打分(拉K线/量/OI)。默认 40。
	CandidatePoolN int `cfg:"candidate_pool_n"`
	// ShortlistN 综合分排序后保留的可交易 shortlist 大小(供 90s 循环出信号)。默认 20。
	ShortlistN int `cfg:"shortlist_n"`
	// UniverseTopN 资费筛币:按 |单次费率| 降序取前 N 作候选(仅在无 shortlist 时回退)。默认 12。
	UniverseTopN int `cfg:"universe_top_n"`
	// MinFundingAbsPerInterval 进入候选的最小 |单次费率|(小数,0.0005=0.05%)。默认 0.0005。
	MinFundingAbsPerInterval float64 `cfg:"min_funding_abs_per_interval"`
	// CandleBar K线粒度(OKX bar:1m/5m/15m/1H)。默认 5m。
	CandleBar string `cfg:"candle_bar"`
	// CandleLimit 拉取K线根数(算信号窗口)。默认 120。
	CandleLimit int `cfg:"candle_limit"`
	// EMAFast/EMASlow 趋势态 EMA 快慢周期。默认 9 / 30。
	EMAFast int `cfg:"ema_fast"`
	EMASlow int `cfg:"ema_slow"`
	// TrendEfficiencyThreshold 趋势/震荡判别:efficiency ratio ≥ 此值=趋势态,否则震荡态。默认 0.30。
	TrendEfficiencyThreshold float64 `cfg:"trend_efficiency_threshold"`
	// ZScoreEntry 震荡态反转入场 z 分阈值(|收盘 z| ≥ 此值才开)。默认 1.5。
	ZScoreEntry float64 `cfg:"zscore_entry"`
	// SignalWindowBars 信号窗口:efficiency ratio 与 z 分仅在最近此根 K 线上计算(匹配持仓周期,避免长窗钝化导致 regime 永远判震荡)。≤0 回退用全部 closes。默认 24(5m×24=2h)。
	SignalWindowBars int `cfg:"signal_window_bars"`
	// RequireFundingAlignment 资费对齐闸:true 时仅当资费"明显逆持仓"(≥FundingVetoAbsPerInterval)才否决该方向。默认 true。
	RequireFundingAlignment bool `cfg:"require_funding_alignment"`
	// FundingVetoAbsPerInterval 资费强逆否决阈值(单次费率绝对值);与选币阈值解耦,避免刚过选币线就把对齐方向误杀。默认 0.0015。
	FundingVetoAbsPerInterval float64 `cfg:"funding_veto_abs_per_interval"`
	// TradingCapitalUSDT 交易仓(合约账户)投入本金,由页面设置;名义 = 本金 × Leverage。默认 30。运行时被页面控制覆盖。
	TradingCapitalUSDT float64 `cfg:"trading_capital_usdt"`
	// StopLossPct 硬止损,以"交易仓本金"为基准(0.5=亏到本金的 50% 即平)。5X 下 50% 本金≈10% 价格移动。默认 0.5。运行时页面可覆盖。
	StopLossPct float64 `cfg:"stop_loss_pct"`
	// TakeProfitPct 止盈,同样以交易仓本金为基准(0.30=赚到本金 30% 止盈)。默认 0.30。
	TakeProfitPct float64 `cfg:"take_profit_pct"`
	// MaxHoldMinutes 最大持有(分钟),超时平。默认 180。
	MaxHoldMinutes int `cfg:"max_hold_minutes"`
	// AbsMaxHoldMinutes 绝对最大持有(分钟):达 MaxHoldMinutes 后若仍盈利且信号同向可续持,但不得超过此绝对上限。≤0 回退 MaxHoldMinutes×3。默认 180。
	AbsMaxHoldMinutes int `cfg:"abs_max_hold_minutes"`
	// DailyLossLimitUSDT 当日累计已实现亏损熔断线(USDT);≤0=不启用日熔断。默认 0(按用户要求不设日熔断)。
	DailyLossLimitUSDT float64 `cfg:"daily_loss_limit_usdt"`
	// CostFloorBps 成本地板:预期边(bps)须 ≥ 此值才开,挡薄盘穿价。默认 60。
	CostFloorBps float64 `cfg:"cost_floor_bps"`
	// SymbolCooldownMinutes 平仓后该币冷却分钟数。默认 30。
	SymbolCooldownMinutes int `cfg:"symbol_cooldown_minutes"`
	// --- 防陷阱入场质量闸(用户要求:不陷波动/单边/暴拉回调/抄底/追高/滚仓陷阱)---
	// SpikeBarPct 最后一根K线同向涨跌幅 ≥ 此值视为"暴拉尖刺",趋势态不追(防暴拉回调/追高)。默认 0.03(3%)。
	SpikeBarPct float64 `cfg:"spike_bar_pct"`
	// TrendMaxExtensionPct 趋势态入场时价格相对快EMA的偏离上限;超过=过度延展,不追(防单边/追高)。默认 0.02(2%)。
	TrendMaxExtensionPct float64 `cfg:"trend_max_extension_pct"`
	// VolExpansionMaxRatio 震荡态:近窗波动/基线波动 > 此比值视为"区间在破/波动骤升",不做反转(防波动陷阱)。默认 1.8。
	VolExpansionMaxRatio float64 `cfg:"vol_expansion_max_ratio"`
	// ReversionKnifePct 震荡反转时,极值仍以 ≥ 此速率延续(急跌/急拉)则不接(防抄底接飞刀/追空顶)。默认 0.015(1.5%)。
	ReversionKnifePct float64 `cfg:"reversion_knife_pct"`
	// LossCooldownMultiplier 止损平仓后该币冷却时间 = SymbolCooldownMinutes × 此倍数(防滚仓/亏损币快速复入)。默认 3。
	LossCooldownMultiplier float64 `cfg:"loss_cooldown_multiplier"`
	// --- 高阶仓位管理:部分止盈/移动止损/补仓(均本金口径) ---
	// PartialTPTriggerPct 浮盈首次达本金的此比例时分批止盈一次(落袋 PartialTPFraction),并把止损上移到保本;
	// 须 < QuickTPPct(否则被快进快出全平抢先)。默认 0.06(+6%本金):复盘里大量单冲到 +6~10% 没够到 +12% 全平就回吐,
	// 分批后至少这一部分已落袋(治"很少赚")。0 关闭分批止盈。
	PartialTPTriggerPct float64 `cfg:"partial_tp_trigger_pct"`
	// PartialTPFraction 部分止盈平掉的仓位比例。默认 0.5(平一半)。
	PartialTPFraction float64 `cfg:"partial_tp_fraction"`
	// TrailGapPct 移动保盈的绝对回撤间距(本金口径):高峰值时用 峰值−间距 作为锁利线(比例锁利的二次收紧)。默认 0.06。
	TrailGapPct float64 `cfg:"trail_gap_pct"`
	// TrailLockFraction 移动保盈的峰值比例锁利系数:浮盈高水位过臂点后,锁利线=max(峰值×该系数, 峰值−TrailGapPct)。
	// 把原先"峰值<TrailGapPct 时只保本(锁利线=0)"的死区补上——让冲到 +6~10% 又回头的单至少落袋一半峰值利润,
	// 不再全额回吐成亏(治"很少赚":赢着的单被拖回亏损)。默认 0.5(锁住峰值的一半)。0 回退 0.5。
	TrailLockFraction float64 `cfg:"trail_lock_fraction"`
	// ScaleInTriggerPct 浮盈达本金此比例且信号仍确认时补仓(仅加盈利仓,绝不摊亏)。默认 0.15。
	ScaleInTriggerPct float64 `cfg:"scale_in_trigger_pct"`
	// MaxAdds 单仓最多补仓次数。默认 2。
	MaxAdds int `cfg:"max_adds"`
	// ScaleInFraction 每次补仓名义 = 首仓名义 × 此比例。默认 0.5。
	ScaleInFraction float64 `cfg:"scale_in_fraction"`
	// --- 数据驱动的仓位动作判定(决定补仓/部分止盈/持有/离场,而非固定阈值机械触发) ---
	// AddConvictionMin 仅当最新信号信心 ≥ 此值且仍趋势态同向(强延续)才考虑补仓(press winner)。默认 0.6。
	AddConvictionMin float64 `cfg:"add_conviction_min"`
	// TPFadeConviction 最新信号信心 ≤ 此值视为"edge 转弱",在盈利时部分止盈锁利。默认 0.4。
	TPFadeConviction float64 `cfg:"tp_fade_conviction"`
	// PeakGivebackFraction 自浮盈高水位回撤达 此比例×高水位 视为动能转弱,盈利中触发部分止盈。默认 0.4。
	PeakGivebackFraction float64 `cfg:"peak_giveback_fraction"`
	// QuickTPPct 快进快出小止盈(本金口径):浮盈达此值即全平落袋(吃到就赚到),除非数据判定强延续则改为骑乘加仓。
	// 小资金高频核心:赢快跑。需 > 往返成本对应的本金%(maker进场压成本)。默认 0.12(+12%本金;5X下≈2.4%价)。
	QuickTPPct float64 `cfg:"quick_tp_pct"`
	// BreakevenArmPct 盈利保护臂点(本金口径):浮盈高水位达此值后启用移动保盈(回吐到 max(0, 峰值−TrailGapPct) 即平),不让赢单回吐成亏单。默认 0.05。
	BreakevenArmPct float64 `cfg:"breakeven_arm_pct"`
	// StaleCutMinutes 治超时:持仓达此分钟数仍未盈利(浮盈 ≤ StaleCutMinProfitPct)即提前停滞离场,不拖到 MaxHold 放大亏损。默认 40(=2/3×60)。
	StaleCutMinutes int `cfg:"stale_cut_minutes"`
	// StaleCutMinProfitPct 停滞离场的盈利门槛(本金口径):达 StaleCutMinutes 时浮盈 ≤ 此值才砍;默认 0(未盈利即砍,赢单不受影响)。
	StaleCutMinProfitPct float64 `cfg:"stale_cut_min_profit_pct"`
	// TrendEntryPullbackMaxBarPct 趋势回踩入场闸:趋势态只在「最后一根顺势方向涨跌 ≤ 此比例」(即回踩/横盘)时入场,
	// 不在顺势暴动的当根追入——买回踩不买尖顶,根治趋势单追高(复盘 trend 胜率仅 25%)。默认 0.005(0.5%/根)。≤0 关闭。
	TrendEntryPullbackMaxBarPct float64 `cfg:"trend_entry_pullback_max_bar_pct"`
	// TrendSizeFraction 趋势单仓位系数:趋势态每仓本金/名义 ×此系数(趋势单胜率低,少冒险,资金偏向 range 反转单)。
	// 默认 0.5(趋势单减半仓)。≤0 或 >1 回退 1(不缩放)。
	TrendSizeFraction float64 `cfg:"trend_size_fraction"`
	// ReversalMinHoldMinutes 信号反转最小持有(分钟):开仓后此时长内不因信号反转立即平仓(防 5m 噪声急翻 churn);
	// 窗口内仍由硬止损兜底。默认 0(立即反转平)。
	ReversalMinHoldMinutes int `cfg:"reversal_min_hold_minutes"`
	// MinOpenConviction 最低开仓信心闸(跨所印证后的最终 Conviction 口径):低于此值的信号不开仓。
	// 离线回测实证:仅高信心档(≥0.7 命中 59% / 扣 10bps 往返后净 +0.13%/笔)有正 edge,低信心档(<0.5)确定性亏损。
	// 只做高信心档把薄 edge 做厚——频次降约 3/4 换取「净亏→净正」。默认 0.65。≤0 关闭(不筛)。
	MinOpenConviction float64 `cfg:"min_open_conviction"`
	// --- 跨所交叉印证(开仓端):同币在其他所的近窗方向用于加/减信心、极端背离则否决 ---
	// CrossVenueCorroborationEnabled 开启跨所交叉印证。默认 true。
	CrossVenueCorroborationEnabled bool `cfg:"cross_venue_corroboration_enabled"`
	// CrossVenueConvictionStep 每个同向 peer 加、每个反向 peer 减的信心步长。默认 0.1。
	CrossVenueConvictionStep float64 `cfg:"cross_venue_conviction_step"`
	// CrossVenueVetoMinPeers 否决所需的最少「有数据 peer」数:仅当 ≥此数的 peer 都反向(无一同向)才否决该开仓。默认 2。
	CrossVenueVetoMinPeers int `cfg:"cross_venue_veto_min_peers"`
	// CrossVenueDeadbandPct 判定 peer 方向的净位移死区(信号窗口内);|净位移|≤此值视为无方向。默认 0.003(0.3%)。
	CrossVenueDeadbandPct float64 `cfg:"cross_venue_deadband_pct"`
	// FillPriceMaxDevPct 成交价合理性校验:avg 偏离 mid 超过此比例则判为不可信,记账回退 mid。默认 0.10(10%)。
	FillPriceMaxDevPct float64 `cfg:"fill_price_max_dev_pct"`
	// MaxOpenSpreadBps 开仓流动性闸:盘口价差(bps)超过此值则跳过开仓(挡薄盘)。≤0 不启用。默认 50(0.5%)。
	MaxOpenSpreadBps float64 `cfg:"max_open_spread_bps"`
}

// CrossVenueExecutorConfig 是跨所资金费对冲执行引擎的可调参数。
type CrossVenueExecutorConfig struct {
	// Enabled 启用执行引擎 goroutine。默认 false;false 时不启动。
	Enabled bool `cfg:"enabled"`
	// LiveTradingEnabled 主安全闸:false=OFF(完整闸门+协议但不下单);true=真钱(LIVE/CANARY)。默认 false。
	LiveTradingEnabled bool `cfg:"live_trading_enabled"`
	// LoopIntervalSeconds 主循环周期(秒)。默认 60。
	LoopIntervalSeconds int `cfg:"loop_interval_seconds"`
	// OpenSpreadAPR 可选年化价差 listing 下限(小数);≤0 时 bypass,开仓由 MinNetProfitUSDT 净收益闸决定。默认 0。
	OpenSpreadAPR float64 `cfg:"open_spread_apr"`
	// CloseSpreadAPR 平仓年化价差阈值(小数,0.3=30%)。默认 0.3。
	CloseSpreadAPR float64 `cfg:"close_spread_apr"`
	// CloseSpreadConsecutiveRounds 价差低于平仓阈值须连续满足的轮数。默认 3。
	CloseSpreadConsecutiveRounds int `cfg:"close_spread_consecutive_rounds"`
	// CloseHardNegativeSpreadAPR 价差深度转负硬止损阈值(小数,−0.5=−50%年化);连续 N 轮低于此值立即平仓,不受最小持有保护。
	CloseHardNegativeSpreadAPR float64 `cfg:"close_hard_negative_spread_apr"`
	// CloseBeforeAdverseFundingEnabled 结算前净不利资费时立即平仓(绕过 N 连轮/MinHold)。默认 true。
	CloseBeforeAdverseFundingEnabled bool `cfg:"close_before_adverse_funding_enabled"`
	// PreFundingCloseLeadSeconds 距某腿下次 funding 结算 ≤ 此秒数且该结算净不利时触发即平。默认 120。
	PreFundingCloseLeadSeconds int `cfg:"pre_funding_close_lead_seconds"`
	// MaxConcurrent 同时在场最大跨所持仓数。默认 2。
	MaxConcurrent int `cfg:"max_concurrent"`
	// MaxPositionUSDT 单腿最大名义(USDT)。默认 50。
	MaxPositionUSDT float64 `cfg:"max_position_usdt"`
	// PerTradeMaxLossPct 单笔下注单元:把基差止损预算(=单笔最坏可控亏损)钉死在此比例 × 该腿名义之内。
	// 暴力币基差可瞬间剧烈摆动,本闸给每笔下注一个硬亏损上限,避免单笔失控吃掉多笔资费利润。
	// 默认 0.08(名义的 8%);≤0 时不封顶(回退纯 BasisStopBudgetFraction 口径)。
	PerTradeMaxLossPct float64 `cfg:"per_trade_max_loss_pct"`
	// MaxBookSpreadBps 两腿盘口价差上限(bps)。默认 30。
	MaxBookSpreadBps float64 `cfg:"max_book_spread_bps"`
	// MakerLegMaxBookSpreadBps 统一模型 maker-first 下 maker(首/高费)腿盘口价差上限(bps)。
	// maker 腿我们挂单不穿价,可放宽;taker(对冲/低费)腿仍用 MaxBookSpreadBps 严控。
	// 默认 80;≤0 时回退 MaxBookSpreadBps(即不放宽)。仅统一模型 + maker-first 生效。
	MakerLegMaxBookSpreadBps float64 `cfg:"maker_leg_max_book_spread_bps"`
	// MaxOpenSlippageBps 开仓前按 max_position_usdt 模拟吃单,任一腿预估滑点超此阈值则拒开。默认 15。
	MaxOpenSlippageBps float64 `cfg:"max_open_slippage_bps"`
	// SlippageBudgetBps 滑点统一基准(bps):sizing/gate/EV 回退/IOC 第一腿均由此派生。默认 20;0=legacy(退回各独立常量)。
	SlippageBudgetBps float64 `cfg:"slippage_budget_bps"`
	// SlippageGateFactor 开仓 gate 拒开阈值 = SlippageBudgetBps×此系数。默认 0.75(20→15bps)。
	SlippageGateFactor float64 `cfg:"slippage_gate_factor"`
	// SlippageExecutionSafetyFactor 执行吃单 IOC = SlippageBudgetBps×此系数(须≥1,硬顶 100bps)。默认 2(20→40bps)。
	SlippageExecutionSafetyFactor float64 `cfg:"slippage_execution_safety_factor"`
	// SlippageCloseMaxMultiplier 平仓阶梯穿价上限 = SlippageBudgetBps×此系数。默认 12.5(20→250bps)。
	SlippageCloseMaxMultiplier float64 `cfg:"slippage_close_max_multiplier"`
	// TakerSlippageBudgetBps 吃单腿(第二腿/回滚/平仓/降风险)IOC 限价滑点预算(bps),须宽于 SlippageBudgetBps
	// 以穿价成交;第一腿仍用 SlippageBudgetBps。0=回退与 SlippageBudgetBps 相同。默认 40。
	TakerSlippageBudgetBps float64 `cfg:"taker_slippage_budget_bps"`
	// MaxNakedSeconds 第二腿失败裸腿暴露上限(秒),超时须完成回滚或升级告警。默认 30。
	MaxNakedSeconds int `cfg:"max_naked_seconds"`
	// MinLiqDistance 强平距离下限(|mark−liq|/mark)。已废弃:请用 risk.min_liquidation_distance_rate;
	// 仅当后者为 0 时作 YAML 兼容回退。默认 0.15。
	MinLiqDistance float64 `cfg:"min_liq_distance"`
	// MaxHoldHours 最大持仓时长(小时)。默认 72。
	MaxHoldHours int `cfg:"max_hold_hours"`
	// MaxEffectiveLeverage 可部署杠杆:单腿保证金 = 名义/此值(如 3 → margin=notional/3)。默认 3。
	MaxEffectiveLeverage float64 `cfg:"max_effective_leverage"`
	// MarginSafetyFactor 强平安全垫(如 SetLeverage/强平距离校验),不参与 sizing 缩小保证金。默认 1.0(不缩)。
	MarginSafetyFactor float64 `cfg:"margin_safety_factor"`
	// MidPriceDeviationMax 两所中间价最大偏离(小数,0.03=3%)。默认 0.03。
	MidPriceDeviationMax float64 `cfg:"mid_price_deviation_max"`
	// MidPriceDeviationMaxExtreme 极端价差机会(ExtremeSpread 或 SpreadAPR>阈值)允许的更大中间价偏离上限。默认 0.08。
	MidPriceDeviationMaxExtreme float64 `cfg:"mid_price_deviation_max_extreme"`
	// EntryBasisConvergenceFraction 入场基差成本 EV 折现:预期持有期内收敛比例(0.5=锁入基差扣一半)。默认 0.5。
	EntryBasisConvergenceFraction float64 `cfg:"entry_basis_convergence_fraction"`
	// DepthUsageRatio 名义额占头部 3 档深度比例上限。默认 0.20。
	DepthUsageRatio float64 `cfg:"depth_usage_ratio"`
	// SizingMaxSlippageBps 流动性自适应单仓:沿盘口吃单滑点预算(bps),与 depth 项共同约束名义上限。默认 20;0=关闭(退化为旧 depth_usage_ratio)。
	SizingMaxSlippageBps float64 `cfg:"sizing_max_slippage_bps"`
	// SizingMaxParticipationPct24h 流动性自适应单仓:≤ participation × 24h 成交量(USDT)。默认 0.01(1%);0=关闭。
	SizingMaxParticipationPct24h float64 `cfg:"sizing_max_participation_pct_24h"`
	// SizingConservativeNoDepthFactor 无深度/24h 量等流动性数据时单仓名义 = max_position × 此系数(宁可少开也不盲满额);1.0=legacy 满额;默认 0.25。
	SizingConservativeNoDepthFactor float64 `cfg:"sizing_conservative_no_depth_factor"`
	// SymbolCooldownHours 回滚失败后 symbol 冷却(小时)。默认 1。
	SymbolCooldownHours int `cfg:"symbol_cooldown_hours"`
	// PerpMarginTopUpBuffer 开仓前自动划转永续保证金时的安全系数(只补差额×buffer)。默认 1.05。
	PerpMarginTopUpBuffer float64 `cfg:"perp_margin_top_up_buffer"`
	// BybitAgreementCooldownHours Bybit 110126 协议未签时 symbol 冷却(小时)。默认 6。
	BybitAgreementCooldownHours int `cfg:"bybit_agreement_cooldown_hours"`
	// DeadManCountdownSeconds okx/gate 腿 dead-man 续期间隔(秒)。默认 60。
	DeadManCountdownSeconds int `cfg:"dead_man_countdown_seconds"`
	// MinHoldHours 最小预期持有时长(小时),EV 闸用作覆盖往返手续费的时间窗;持仓管理也会参考。
	MinHoldHours float64 `cfg:"min_hold_hours"`
	// MinHoldToFirstSettlementEnabled 开仓后须持有至首个 intended funding 结算(+缓冲)才允许软平仓/换仓;硬风控不受限。默认 true。
	MinHoldToFirstSettlementEnabled bool `cfg:"min_hold_to_first_settlement_enabled"`
	// MinHoldToFirstSettlementBufferSeconds 首个 funding 结算后额外缓冲秒数(等待资费 sync)。默认 60。
	MinHoldToFirstSettlementBufferSeconds int `cfg:"min_hold_to_first_settlement_buffer_seconds"`
	// DefaultTakerFeeBps 各所永续吃单费率(bps)未命中内置默认表时的兜底值。默认 6。
	DefaultTakerFeeBps float64 `cfg:"default_taker_fee_bps"`
	// DefaultMakerFeeBps 各所永续挂单费率(bps)未命中内置默认表时的兜底值。默认 2。
	DefaultMakerFeeBps float64 `cfg:"default_maker_fee_bps"`
	// AssumeMakerFills EV/开仓闸往返成本按 maker 费率估算(与 maker-chase 执行一致)。默认 true。
	AssumeMakerFills bool `cfg:"assume_maker_fills"`
	// ExpectedHoldHours 开仓 EV 闸预期持有 horizon(小时):一次往返成本摊销至该窗口内 funding 收入。默认 12。
	// NearTermOpenEVHorizonEnabled=true 时优先用至下次结算的可比窗口;仅在无可结算窗口时回退至此固定 horizon。
	ExpectedHoldHours float64 `cfg:"expected_hold_hours"`
	// NearTermOpenEVHorizonEnabled 开仓 EV/扫描 netEV 使用至下次结算的可比窗口(与 continuation future-EV 一致)。默认 true。
	NearTermOpenEVHorizonEnabled bool `cfg:"near_term_open_ev_horizon_enabled"`
	// NearTermOpenEVExpectedSettlements 近端 horizon 下预期前向持有的结算次数 N:horizon 至少 = N × 最短腿 funding interval,
	// 使进场窗内(可比窗口≈0.04h)的 EV 按"多结算前向持有"而非"距下次结算的残余时间"计费。默认 3;≤0 时回退 3。
	NearTermOpenEVExpectedSettlements int `cfg:"near_term_open_ev_expected_settlements"`
	// NearTermOpenEVMaxHoldHours 近端 horizon 上限(小时):防止 N×interval 反向膨胀回 12h 式高估。默认 8;≤0 时回退 8。
	NearTermOpenEVMaxHoldHours float64 `cfg:"near_term_open_ev_max_hold_hours"`
	// EVExpectedSlippageBps EV 成本模型中每腿预期滑点(bps,非 SlippageBudget 容忍上限)。默认 3。
	EVExpectedSlippageBps float64 `cfg:"ev_expected_slippage_bps"`
	// EVMaxBreakevenHours 盈亏平衡持有时长上限(小时);超过则不开仓。默认 24。
	EVMaxBreakevenHours float64 `cfg:"ev_max_breakeven_hours"`
	// EVSafetyFactor 已废弃(旧 24h-hold EV 闸);保留配置兼容,开仓 EV 改用 MinNetProfitUSDT。默认 1.3。
	EVSafetyFactor float64 `cfg:"ev_safety_factor"`
	// MinNetProfitUSDT 预期持有 horizon 内净收益(USDT)下限;≤0 表示任意净正即可。默认 0。
	MinNetProfitUSDT float64 `cfg:"min_net_profit_usdt"`
	// RealizedFundingPollMinutes 拉取真实结算资费的节流间隔(分钟);两次拉取之间用估算做临时展示。默认 5。
	RealizedFundingPollMinutes int `cfg:"realized_funding_poll_minutes"`
	// MakerFirst 永续腿 post-only 追价优先;false=回退纯 IOC(零行为变更开关)。默认 true。
	MakerFirst bool `cfg:"maker_first"`
	// MakerChaseRounds post-only 追价最多轮数(每轮撤旧挂新贴最优被动价)。默认 6。
	MakerChaseRounds int `cfg:"maker_chase_rounds"`
	// MakerChaseIntervalMs 每轮 maker 挂单等待成交时长(毫秒)。默认 1500。
	MakerChaseIntervalMs int `cfg:"maker_chase_interval_ms"`
	// MakerDeadlineSeconds 单腿 maker 追价总预算(秒);第一腿超时放弃开仓,第二腿/平仓超时 taker 兜底。默认 12。
	MakerDeadlineSeconds int `cfg:"maker_deadline_seconds"`
	// MinComparableWindowHours old/new EV 比较的最短共同可比窗口(小时);低于此阈值时禁止软换仓/软平仓比较。默认 0.25(15m)。
	MinComparableWindowHours float64 `cfg:"min_comparable_window_hours"`
	// PostFundingCooldownSeconds funding 边界后冷静期(秒);冷静期内不做基于短窗口噪声的软比较。默认 180。
	PostFundingCooldownSeconds int `cfg:"post_funding_cooldown_seconds"`
	// MaxCompareAgeSeconds 当前持仓 future EV/soft-close 比较可接受的最大快照年龄(秒)。默认 900。
	MaxCompareAgeSeconds int `cfg:"max_compare_age_seconds"`
	// SwitchBufferUSDT old/new compare 时要求新机会多出来的最小净收益缓冲(USDT);低于该值不触发换仓。默认 0.5。
	SwitchBufferUSDT float64 `cfg:"switch_buffer_usdt"`
	// SwitchCooldownSeconds 同一持仓触发换仓比较后的冷静期(秒);冷静期内不重复发起软换仓。默认 300。
	SwitchCooldownSeconds int `cfg:"switch_cooldown_seconds"`
	// SwitchSequence 默认换仓顺序:close_then_open/open_then_close/staged_switch。默认 close_then_open。
	SwitchSequence string `cfg:"switch_sequence"`
	// MaxSwitchesPerCycle 每轮最多允许触发的换仓数;超过则保留原仓。默认 1。
	MaxSwitchesPerCycle int `cfg:"max_switches_per_cycle"`
	// PerpPerpOpeningsDefaultEnabled 路线 B live-stack 启动时跨所 perp-perp 运行时开仓开关默认值;
	// reconcile 发现未登记真实持仓时仍会强制关(安全优先)。默认 true。
	PerpPerpOpeningsDefaultEnabled bool `cfg:"perp_perp_openings_default_enabled"`
	// UntrackedExposureAction 冷启动发现未登记真实仓位时的显式分支:halt/import/flatten。默认 halt。
	UntrackedExposureAction string `cfg:"untracked_exposure_action"`
	// DustCloseThresholdUSDT 风险腿已平时,残余非风险腿名义低于此值(且低于交易所 minNotional 若可得)视为 dust,
	// 标记已平并释放并发槽,不再无限重试。默认 1.0 USDT。
	DustCloseThresholdUSDT float64 `cfg:"dust_close_threshold_usdt"`
	// CarryFundingEntryBaseFraction 基础开仓窗口 = fundingInterval × 此系数。默认 0.125(1/8)→8h 约 60min。
	CarryFundingEntryBaseFraction float64 `cfg:"carry_funding_entry_base_fraction"`
	// CarryFundingEntryMinVolScale 高波动缩窗下限(volScale 最小值)。默认 0.2。
	CarryFundingEntryMinVolScale float64 `cfg:"carry_funding_entry_min_vol_scale"`
	// CarryFundingEntryVolRefPerHour 费率波动参考(每小时归一化 stddev);vol≥此值时缩至 minVolScale。默认 0.0001。
	CarryFundingEntryVolRefPerHour float64 `cfg:"carry_funding_entry_vol_ref_per_hour"`
	// CarryFundingEntryFloorMinutes 窗口下限(分钟),保证两腿能在结算前建立。默认 5。
	CarryFundingEntryFloorMinutes float64 `cfg:"carry_funding_entry_floor_minutes"`
	// CarryFundingEntryVolLookbackScans 计算费率波动时使用的最近扫描样本数。默认 10。
	CarryFundingEntryVolLookbackScans int `cfg:"carry_funding_entry_vol_lookback_scans"`
	// CarryFundingEntryMissingVolScale 无费率波动历史时开仓窗口 volScale(中等,非最紧)。默认 0.6。
	CarryFundingEntryMissingVolScale float64 `cfg:"carry_funding_entry_missing_vol_scale"`
	// CarryNoHistoryHaircut 无 p90 历史时对 live rate 的乘数;1.0=不裁剪。默认 1.0。
	CarryNoHistoryHaircut float64 `cfg:"carry_no_history_haircut"`
	// LowConfidenceSizeFloorFraction 非极端币低置信度(历史/波动)时的 confMult 下限;极端 spread 机会豁免此砍仓。默认 0.6。
	LowConfidenceSizeFloorFraction float64 `cfg:"low_confidence_size_floor_fraction"`
	// SingleVenueReverseFundingEnabled 单所策略启用反向资金费 carry(负费率 spot-perp / 正费率 perp-spot)。
	// 默认 false;关闭时仍走 v1_not_supported 拒绝,行为与改动前一致。
	SingleVenueReverseFundingEnabled bool `cfg:"single_venue_reverse_funding_enabled"`
	// OpenFundingLeadSeconds perp-perp 结算前进场窗(秒):只在最近一腿资金费结算前 ≤ 此秒数才允许开仓,
	// 太早(距结算 > 此值)拒开,避免提前进场、结算前价差收敛白付手续费。默认 180(3 分钟);≤0 时回退 180。
	OpenFundingLeadSeconds int `cfg:"open_funding_lead_seconds"`
	// TimingAwarePairSelectionEnabled 扫描期 per-symbol 腿对选择:在可比窗口 funding EV 上按进场窗等待折现
	// (per-hour capturable EV);false 时仅取 max 可比窗口 EV。默认 true。
	TimingAwarePairSelectionEnabled bool `cfg:"timing_aware_pair_selection_enabled"`
	// TimingAwarePairSelectionMinHours timing-aware 分母下限(小时),防除零。默认 0.01。
	TimingAwarePairSelectionMinHours float64 `cfg:"timing_aware_pair_selection_min_hours"`
	// UnifiedCaptureModelEnabled 统一资费捕获模型总开关:开启后进场对齐"付钱腿"结算、EV/离场同口径、
	// 用基差(而非资费价差)做实时止损。默认 false(关闭时行为与改造前完全一致,可一键回滚)。
	UnifiedCaptureModelEnabled bool `cfg:"unified_capture_model_enabled"`
	// UnifiedCaptureModelSymbolAllowlist 灰度白名单:非空时统一捕获模型仅对名单内 symbol 生效(其余走旧逻辑);
	// 空=对全部 perp-perp symbol 生效(在 flag ON 前提下)。用于 canary 小范围放量后再扩大。
	UnifiedCaptureModelSymbolAllowlist []string `cfg:"unified_capture_model_symbol_allowlist"`
	// CaptureTargetSettlements 统一捕获模型下每笔默认目标结算次数 N:进场/EV/持有都围绕"吃满 N 次目标结算"。
	// 默认 1(单次目标结算捕获,基差暴露最小);仅当基差稳定分≥阈值时才允许 N>1。≤0 回退 1。
	CaptureTargetSettlements int `cfg:"capture_target_settlements"`
	// BasisStopBudgetFraction 基差止损预算 = 此系数 × 预期目标资费;累计不利基差 PnL 超过预算即平。默认 1.0。
	BasisStopBudgetFraction float64 `cfg:"basis_stop_budget_fraction"`
	// BasisStopConfirmChecks 基差止损迟滞:不利基差需连续此轮数(manage 每轮≈60s)都超预算才真正止损,
	// 而非单 tick 瞬时触发。结算点附近两所永续会瞬时插针/错位,单次采样到尖刺就止损会在最差点把均值回归型
	// 噪声锁成实亏(perp-perp 两腿同一标的,基差本应回归)。要求连续确认可挡掉 <1 轮的瞬时插针。
	// 默认 2;≤1 时退化为单次触发(旧行为)。资费捕获让位窗与硬边界(MaxHold/计划离场)仍兜底。
	BasisStopConfirmChecks int `cfg:"basis_stop_confirm_checks"`
	// BasisStabilityMinScoreForMultiSettlement 允许 N>1 次结算持有所需的最小基差稳定分([0,1])。默认 0.7。
	BasisStabilityMinScoreForMultiSettlement float64 `cfg:"basis_stability_min_score_for_multi_settlement"`
	// CaptureFundingToCostMinRatio 统一模型入场硬条件:预期目标资费 / 往返成本 ≥ 此比值才允许开仓。
	// 用于把"滑点远大于资费"的薄盘暴力小币挡在外面。默认 1.0;≤0 时不启用该闸。
	CaptureFundingToCostMinRatio float64 `cfg:"capture_funding_to_cost_min_ratio"`
	// OpenFundingRefreshEnabled 下单前实时资费复核:在进场窗内真正下第一腿之前,重拉两腿最新资费
	// (离结算越近,交易所预测资费越收敛于真实结算值),用最新值重算"目标腿资费/往返成本";若资费已翻向或
	// 资费÷往返成本跌破 CaptureFundingToCostMinRatio,则放弃本单——根治"用十几分钟前的旧快照赌 3 分钟后结算
	// 的 1h 腿资费翻盘"(LAB 即此:bybit 1h 腿在 12 分钟旧快照期内翻负,做空反付资费)。拉取失败则 fail-open
	// (沿用快照,不阻塞)。默认 true;仅统一捕获模型生效。
	OpenFundingRefreshEnabled bool `cfg:"open_funding_refresh_enabled"`
	// PlannedExitBufferSeconds 统一模型下持有到目标结算后的离场缓冲(秒):结算 + 此缓冲后离场,确保资费已入账。默认 90。
	PlannedExitBufferSeconds int `cfg:"planned_exit_buffer_seconds"`
	// CaptureRollEnabled 计划离场时滚动续持:到计划离场点,若同一对下个目标结算仍净收资费且基差安全(远未触止损),
	// 则把捕获计划延到下个结算(roll),而非平仓重开。根治"稳定复现的资费源被每周期 churn(平了重开)、白付一趟趟往返"
	// (LAB 即此:连续多小时被反复开平,3 趟往返吃掉 3 次资费)。续持是零边际开仓成本,仅多担一个周期基差风险,
	// 且每次续持都重新校验"下个结算仍收钱 + 基差安全",不稳就照常平。默认 true。
	CaptureRollEnabled bool `cfg:"capture_roll_enabled"`
	// CaptureMaxRolls 单笔最多滚动续持次数(上限总持有周期,兜底基差暴露;MaxHold 仍为硬顶)。默认 6。
	CaptureMaxRolls int `cfg:"capture_max_rolls"`
	// CarryHoldToPlannedExitEnabled 对冲 carry 持有到计划离场:开后不再做"持有期主动止损平仓"。
	// 因 perp-perp 两腿同一标的(delta 中性、基差均值回归),持有期的主动平仓(基差止损 / 续持边际转负)
	// 多在薄盘上把"为省几分钱预期成本"变成"实盘穿价亏几 U"(B3 即此:续持净 -0.109 想省 0.1,gate 出场实滑 -3.5)。
	// 开后只保留:计划离场(目标结算+缓冲,流动性最好的窗口)+ 到点 roll 续持/平仓的再评估、结算前净付保护、
	// 最大持有硬顶、以及裸腿/对账/下架等安全平腿;关闭"基差止损"和轮动层"续持边际转负软平"。
	// 默认 true(去掉持有期强平);设 false 可恢复旧的主动止损行为。
	CarryHoldToPlannedExitEnabled bool `cfg:"carry_hold_to_planned_exit_enabled"`
	// ViolenceSizeBoostWeight 暴力度软调仓权重:统一模型下 sizeMult ×= (1 + 权重 × ViolenceScore),clamp01 封顶。
	// 真暴力币(高费且持续,violence 高)更靠近满仓;瞬时尖刺(violence≈0)不受影响。只向满仓靠拢、绝不超额。
	// 默认 0.3;0=关闭(回退纯置信度调仓)。仅统一模型生效。
	ViolenceSizeBoostWeight float64 `cfg:"violence_size_boost_weight"`
	// ViolenceRankBoostWeight 暴力度排序权重:统一模型下正 EV 机会 rank ×= (1 + 权重 × ViolenceScore)。
	// 同等净EV下,真暴力币排得更靠前(用户主战场)。默认 0.3;0=关闭。仅作用于正 EV,不抬升亏损机会。
	ViolenceRankBoostWeight float64 `cfg:"violence_rank_boost_weight"`
	// FundingCaptureProbability 运行时 perp-perp funding 捕获折现(结算回测贝叶斯;scan/decision 周期刷新,非 YAML)。
	FundingCaptureProbability float64
}

// CarryFundingEntryGateConfig carry 策略开仓前资金费结算窗口闸门参数视图(由 CrossVenueExecutorConfig 字段组装)。
type CarryFundingEntryGateConfig struct {
	BaseFraction     float64
	MinVolScale      float64
	VolRefPerHour    float64
	FloorMinutes     float64
	VolLookbackScans int
	MissingVolScale  float64
}

// FundingEntryGate 返回 carry 开仓窗口闸门配置视图。
func (c CrossVenueExecutorConfig) FundingEntryGate() CarryFundingEntryGateConfig {
	return CarryFundingEntryGateConfig{
		BaseFraction:     c.CarryFundingEntryBaseFraction,
		MinVolScale:      c.CarryFundingEntryMinVolScale,
		VolRefPerHour:    c.CarryFundingEntryVolRefPerHour,
		FloorMinutes:     c.CarryFundingEntryFloorMinutes,
		VolLookbackScans: c.CarryFundingEntryVolLookbackScans,
		MissingVolScale:  c.CarryFundingEntryMissingVolScale,
	}
}

// CapitalAllocatorConfig 是跨策略资本分配器的可调参数(默认关闭+OFF 模式)。
type CapitalAllocatorConfig struct {
	// Enabled 启用资本分配器 goroutine。默认 false;false 时不启动。
	Enabled bool `cfg:"enabled"`
	// Mode 运行模式:plan|profit|data_collection。默认 plan。
	Mode string `cfg:"mode"`
	// MaxTotalCapitalUsageRatio 全策略合计资本占用上限(相对分配本金,小数)。默认 0.2。
	MaxTotalCapitalUsageRatio float64 `cfg:"max_total_capital_usage_ratio"`
	// MaxDailyLossUSDT 日亏损熔断上限(USDT)。默认 0.3。
	MaxDailyLossUSDT float64 `cfg:"max_daily_loss_usdt"`
}

// DataCollectionConfig 是数据收集策略的可调参数(默认关闭+OFF 模式)。
type DataCollectionConfig struct {
	// Enabled 启用数据收集策略 goroutine。默认 false;false 时不启动。
	Enabled bool `cfg:"enabled"`
	// Mode 运行模式:plan|data_collection|profit。默认 plan。
	Mode string `cfg:"mode"`
	// StrategyType 数据收集策略类型标识。默认空。
	StrategyType string `cfg:"strategy_type"`
	// MaxPositions 同时在场最大持仓数。默认 1。
	MaxPositions int `cfg:"max_positions"`
	// MaxNotionalUSDT 单策略最大名义(USDT)。默认 10。
	MaxNotionalUSDT float64 `cfg:"max_notional_usdt"`
	// MaxExpectedLossUSDTPerTrade 单笔预期亏损上限(USDT)。默认 0.03。
	MaxExpectedLossUSDTPerTrade float64 `cfg:"max_expected_loss_usdt_per_trade"`
}

// UnifiedRotationConfig 是五策略统一轮动 runner 的开关(默认关闭;active 即实盘,面板 START 须 confirm=LIVE)。
type UnifiedRotationConfig struct {
	// Enabled 启用统一轮动 runner goroutine。默认 false;false 时不启动。
	Enabled bool `cfg:"enabled"`
	// LiveTradingEnabled 部署期安全闸:与 enabled 同时为 true 时,无 rotation_control.json 才默认 active=true。
	// 运行时启停由面板 START/STOP 与 rotation_control.json 的 active 控制;不再单独 OFF/LIVE 切换。
	LiveTradingEnabled bool `cfg:"live_trading_enabled"`
	// DelistGuardEnabled 启用下市/非交易状态守卫:拦截新开仓并对在场持仓主动平仓。默认 true。
	DelistGuardEnabled bool `cfg:"delist_guard_enabled"`
	// DelistStatusCacheTTL 是各 venue instrument 交易状态缓存 TTL。默认 1h。
	DelistStatusCacheTTL time.Duration `cfg:"delist_status_cache_ttl"`
	// ContinuationCloseThreshold 续持分(净/占用资金)须低于 −此阈值才允许软 CLOSE;过滤边际负分 churn。默认 0.001。
	ContinuationCloseThreshold float64 `cfg:"continuation_close_threshold"`
	// ContinuationMinHoldSeconds 开仓后至少持有此秒数才允许续持分触发的软 CLOSE;风险类平仓不受限。默认 3600。
	ContinuationMinHoldSeconds int `cfg:"continuation_min_hold_seconds"`
	// SwitchMinAdvantageUSDT 换仓优势安全垫(USDT):仅当新候选净优势(adjusted_open_net − continuation_net)
	// 超过此值才换仓,而非略优即换;挡掉换仓真实往返 + "平旧失败/新仓开不出→两头落空" 的执行风险 + 单结算乐观 EV。默认 0.10。
	SwitchMinAdvantageUSDT float64 `cfg:"switch_min_advantage_usdt"`
	// MinHoldToFirstSettlementEnabled 开仓后须持有至首个 intended funding 结算(+缓冲)才允许软 CLOSE/SWITCH;硬风控不受限。默认 true。
	MinHoldToFirstSettlementEnabled bool `cfg:"min_hold_to_first_settlement_enabled"`
	// MinHoldToFirstSettlementBufferSeconds 首个 funding 结算后额外缓冲秒数。默认 60。
	MinHoldToFirstSettlementBufferSeconds int `cfg:"min_hold_to_first_settlement_buffer_seconds"`
	// TotalDeployableUSDT 全局可部署资金上限(USDT);0=不单独设限,仅用账户可用合计−储备作为预算基数。
	TotalDeployableUSDT float64 `cfg:"total_deployable_usdt"`
	// TotalDeployablePct 全局可部署预算占真实现金权益比例;>0 且 equity>0 时有效上限=pct×RealEquityTotalUSDT;否则退回 USDT。
	TotalDeployablePct float64 `cfg:"total_deployable_pct"`
	// MaxStrategyCapitalUsagePct 单策略占用占全局可部署预算的比例上限;0=不限。
	MaxStrategyCapitalUsagePct float64 `cfg:"max_strategy_capital_usage_pct"`
	// CapitalAwarePerpPerpSizingEnabled perp-perp 候选按各所 UNIFIED 可用×杠杆封顶名义;资金不足时缩仓而非拒单。默认 true。
	CapitalAwarePerpPerpSizingEnabled bool `cfg:"capital_aware_perp_perp_sizing_enabled"`
	// CapitalSizingSafetyHaircut perp-perp 资金封顶安全垫(0,1];反解名义时按 haircut×可用封顶,使所需保证金严格小于实时可用。默认 0.99。
	CapitalSizingSafetyHaircut float64 `cfg:"capital_sizing_safety_haircut"`
}

// VenueFundingExecutorConfig 是 OKX/Gate 单所正费率执行器参数(正 carry:空永续+买现货,跨结算持有)。
type VenueFundingExecutorConfig struct {
	// LiveTradingEnabled 主安全闸:false=OFF(完整决策+日志,绝不下单);true=真实下单。默认 false。
	LiveTradingEnabled bool `cfg:"live_trading_enabled"`
	// TopN 每周期最多并发开仓标的数(正费率降序取前 N)。默认 1。
	TopN int `cfg:"top_n"`
	// MinFundingRate 仅做单次结算正费率 ≥ 此阈值的标的。
	MinFundingRate float64 `cfg:"min_funding_rate"`
	// MinNetProfitRate 成本后净利率阈值(相对部署资金),低于则放弃。
	MinNetProfitRate float64 `cfg:"min_net_profit_rate"`
	// LeadSeconds 结算前多少秒进入开仓窗口。
	LeadSeconds int `cfg:"lead_seconds"`
	// HoldAfterSeconds 结算后持有多少秒再平仓(确保资金费入账)。
	HoldAfterSeconds int `cfg:"hold_after_seconds"`
	// MaxConcurrentPositions 同时在场最大持仓数。默认 1。
	MaxConcurrentPositions int `cfg:"max_concurrent_positions"`
	// MaxPositionUSDT 单标的两腿名义上限(USDT)。默认 30。
	MaxPositionUSDT float64 `cfg:"max_position_usdt"`
	// RollEnabled 跨结算滚动持有(费率衰减/EV 为负才平)。默认 true。
	RollEnabled bool `cfg:"roll_enabled"`
	// MaxHoldSettlements 滚动持有最多跨越结算次数(含首次)。默认 12。
	MaxHoldSettlements int `cfg:"max_hold_settlements"`
	// EVGateEnabled 多结算期望值门(历史样本不足回退单次门)。默认 true。
	EVGateEnabled bool `cfg:"ev_gate_enabled"`
	// SlippageBudgetBps Gate 现货 IOC 限价相对 ask 的滑点预算(bps)。默认 20。
	SlippageBudgetBps float64 `cfg:"slippage_budget_bps"`
}

// BybitExecutorConfig 是 Bybit 精简执行器的可调参数。所有阈值默认保守;LiveTradingEnabled 是真实下单的总开关。
type BybitExecutorConfig struct {
	// LiveTradingEnabled 主安全闸:false=OFF(只规划+日志,绝不下单);true=真钱。默认 false。
	LiveTradingEnabled bool `cfg:"live_trading_enabled"`
	// TopN 每周期最多并发开仓的标的数(正费率降序取前 N)。默认 1(最保守,先验证单标的闭环)。
	TopN int `cfg:"top_n"`
	// MinFundingRate 仅做单次结算正费率 ≥ 此阈值的标的(如 0.0005=0.05%)。
	MinFundingRate float64 `cfg:"min_funding_rate"`
	// MinNetProfitRate 成本后净利率阈值(相对部署资金),低于则放弃。
	MinNetProfitRate float64 `cfg:"min_net_profit_rate"`
	// LeadSeconds 结算前多少秒进入开仓窗口(在此窗口内扫描→定量→开)。
	LeadSeconds int `cfg:"lead_seconds"`
	// HoldAfterSeconds 结算后持有多少秒再平仓(确保资金费已结算入账)。
	HoldAfterSeconds int `cfg:"hold_after_seconds"`
	// MaxConcurrentPositions 同时在场的最大持仓数(总闸,跨周期)。默认 1。
	MaxConcurrentPositions int `cfg:"max_concurrent_positions"`
	// MaxPositionNotionalUSDT 单标的两腿合计名义上限(0=不限,仅受分配资金约束)。实验期建议设小额硬顶。
	MaxPositionNotionalUSDT float64 `cfg:"max_position_notional_usdt"`
	// RollEnabled 开启「滚动持有」(与币安同口径):到期不直接平,复核下期费率——只要仍为正(>0)就续持到下一个结算、
	// 继续收资金费(把一次性往返手续费摊薄到多次结算);费率转负/不达标才平。默认 false(单次结算收割)。
	RollEnabled bool `cfg:"roll_enabled"`
	// MaxHoldSettlements 滚动持有最多跨越的结算次数(含首次)。RollEnabled 时生效;≤1 等价于不滚动。默认 1。
	MaxHoldSettlements int `cfg:"max_hold_settlements"`
	// EnableNegativeCarry 开启后,除正费率外也吃【负费率】标的:多永续(收负费率)+ 借币做空现货(UTA spot margin)。
	// 默认 false=仅正 carry(零行为变更)。⚠️ 依赖统一账户已开通现货杠杆借币;借不到的现货卖单由执行器回补永续兜底
	// (绝不裸腿)。首次启用务必先 testnet / 极小额验证借币空现货能成交并能买回还币。
	EnableNegativeCarry bool `cfg:"enable_negative_carry"`
	// PerpLeverage 永续腿杠杆(1~3,默认 1=全额保证金,零行为变更)。delta 中性下现货多腿天然对冲价格风险,
	// 永续腿 >1x 只是降低保证金占用、放大同等资金可开的两腿名义(总资金 = 现货名义 + 永续名义/杠杆)。
	// 统一账户全仓下现货本身是保证金抵押物,2x 强平距离仍很远;代码硬顶 3x(再高保证金收益递减、尾部风险陡增)。
	PerpLeverage int `cfg:"perp_leverage"`
	// EVGateEnabled 开仓门槛切换为「多结算期望值 > 0」:用历史费率估计费率维持有利的期望结算次数 N,
	// EV = N×费率×名义 − 四腿费用(−负 carry 借币利息)。单次结算门在 taker 费率下注定为负(只能开负门交学费);
	// EV 门让该开的开、纯送钱的不开。历史样本不足自动回退单次门。默认 false(零行为变更)。
	EVGateEnabled bool `cfg:"ev_gate_enabled"`
	// PerpMakerFirst 永续腿 post-only 追价优先,不成交 taker 兜底(Bybit 永续 maker 0.02% vs taker 0.055%,
	// 每腿省 0.035%;现货 maker/taker 同费率不做)。默认 false(零行为变更)。
	PerpMakerFirst bool `cfg:"perp_maker_first"`
	// BorrowCostHourly 负 carry 借币的每小时利率估计(0.00003=0.003%/h≈26% APR,保守),计入净利/EV 成本侧。
	BorrowCostHourly float64 `cfg:"borrow_cost_hourly"`
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
	appendIssue(IsValidStrategyMode(cfg.StrategyConfig.StrategyMode), "StrategyMode 必须是 collect_data、canary_profit 或 profit_seeking")
	appendIssue(cfg.StrategyConfig.MinEstimatedFundingRate > 0, "MinEstimatedFundingRate 必须大于 0")
	appendIssue(cfg.StrategyConfig.MaxEstimatedFundingRate > cfg.StrategyConfig.MinEstimatedFundingRate, "MaxEstimatedFundingRate 必须大于 MinEstimatedFundingRate")
	appendIssue(cfg.StrategyConfig.MaxFundingRateJump > 0, "MaxFundingRateJump 必须大于 0")
	appendIssue(cfg.StrategyConfig.MaxScanBasisRate > 0, "MaxScanBasisRate 必须大于 0")
	if cfg.StrategyConfig.CapPinnedFundingOverrideEnabled {
		appendIssue(cfg.StrategyConfig.CapPinnedFundingProximityRate > 0 && cfg.StrategyConfig.CapPinnedFundingProximityRate <= 1, "CapPinnedFundingProximityRate 必须在 (0, 1] 之间")
		appendIssue(cfg.StrategyConfig.CapPinnedFundingEstimateFactor > 0 && cfg.StrategyConfig.CapPinnedFundingEstimateFactor <= 1, "CapPinnedFundingEstimateFactor 必须在 (0, 1] 之间")
	}
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
	appendIssue(cfg.RiskConfig.MaxDailyLossPct >= 0, "MaxDailyLossPct 不能小于 0（0 表示仅使用 MaxDailyLossUSDT）")
	appendIssue(cfg.RiskConfig.MaxAccountDrawdownPct >= 0, "MaxAccountDrawdownPct 不能小于 0")
	appendIssue(cfg.RiskConfig.MaxStrategyCumulativeLossPct >= 0, "MaxStrategyCumulativeLossPct 不能小于 0")
	appendIssue(cfg.RiskConfig.MaxGrossLeverage >= 0, "MaxGrossLeverage 不能小于 0（0 表示仅使用 MaxAccountOpenNotionalUSDT）")
	appendIssue(cfg.RiskConfig.AutoFlattenAccountDrawdownPct >= 0, "AutoFlattenAccountDrawdownPct 不能小于 0")
	appendIssue(cfg.RiskConfig.AccountRiskMinFundFlowStepUSDT >= 0, "AccountRiskMinFundFlowStepUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.AccountRiskMaxEquityTickDropUSDT >= 0, "AccountRiskMaxEquityTickDropUSDT 不能小于 0")
	appendIssue(cfg.RiskConfig.AccountRiskMaxEquityTickDropPct >= 0, "AccountRiskMaxEquityTickDropPct 不能小于 0")

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
	appendIssue(cfg.RiskConfig.SymbolMinorCooldown > 0, "SymbolMinorCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.SymbolCriticalCooldown > 0, "SymbolCriticalCooldown 必须大于 0")
	appendIssue(cfg.RiskConfig.SymbolMinorCooldown <= cfg.RiskConfig.SymbolAbnormalCooldown, "SymbolMinorCooldown 不能大于 SymbolAbnormalCooldown")
	appendIssue(cfg.RiskConfig.SymbolCriticalCooldown >= cfg.RiskConfig.SymbolAbnormalCooldown, "SymbolCriticalCooldown 不能小于 SymbolAbnormalCooldown")
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
	appendIssue(cfg.HistoricalConfig.SpotMakerFeeRate >= 0, "Historical SpotMakerFeeRate 不能小于 0")
	appendIssue(cfg.HistoricalConfig.FuturesMakerFeeRate >= 0, "Historical FuturesMakerFeeRate 不能小于 0")
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

	if cfg.CrossVenueScanner.Enabled {
		appendIssue(cfg.CrossVenueScanner.ScanIntervalMinutes > 0, "CrossVenueScanner ScanIntervalMinutes 必须大于 0")
		appendIssue(cfg.CrossVenueScanner.MinSpreadAPR >= 0, "CrossVenueScanner MinSpreadAPR 不能小于 0")
		appendIssue(cfg.CrossVenueScanner.AlertSpreadAPR >= 0, "CrossVenueScanner AlertSpreadAPR 不能小于 0")
		appendIssue(cfg.CrossVenueScanner.TopN > 0, "CrossVenueScanner TopN 必须大于 0")
		appendIssue(cfg.CrossVenueScanner.FundingAPRSanityCap > 0, "CrossVenueScanner FundingAPRSanityCap 必须大于 0")
		appendIssue(cfg.CrossVenueScanner.SpreadPersistenceScans > 0, "CrossVenueScanner SpreadPersistenceScans 必须大于 0")
	}
	if cfg.CrossVenueExecutor.Enabled {
		appendIssue(cfg.CrossVenueExecutor.LoopIntervalSeconds > 0, "CrossVenueExecutor LoopIntervalSeconds 必须大于 0")
		appendIssue(cfg.CrossVenueExecutor.OpenSpreadAPR >= 0, "CrossVenueExecutor OpenSpreadAPR 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.CloseSpreadAPR >= 0, "CrossVenueExecutor CloseSpreadAPR 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.MaxConcurrent > 0, "CrossVenueExecutor MaxConcurrent 必须大于 0")
		appendIssue(cfg.CrossVenueExecutor.MaxPositionUSDT > 0, "CrossVenueExecutor MaxPositionUSDT 必须大于 0")
		appendIssue(cfg.CrossVenueExecutor.MaxEffectiveLeverage > 0, "CrossVenueExecutor MaxEffectiveLeverage 必须大于 0")
		appendIssue(cfg.CrossVenueExecutor.MinHoldHours > 0, "CrossVenueExecutor MinHoldHours 必须大于 0")
		appendIssue(cfg.CrossVenueExecutor.DefaultTakerFeeBps >= 0, "CrossVenueExecutor DefaultTakerFeeBps 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.DefaultMakerFeeBps >= 0, "CrossVenueExecutor DefaultMakerFeeBps 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.ExpectedHoldHours >= 0, "CrossVenueExecutor ExpectedHoldHours 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.EVExpectedSlippageBps >= 0, "CrossVenueExecutor EVExpectedSlippageBps 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.MinNetProfitUSDT >= 0, "CrossVenueExecutor MinNetProfitUSDT 不能小于 0")
		appendIssue(cfg.CrossVenueExecutor.RealizedFundingPollMinutes > 0, "CrossVenueExecutor RealizedFundingPollMinutes 必须大于 0")
	}
	if cfg.CapitalAllocator.Enabled {
		appendIssue(cfg.CapitalAllocator.MaxTotalCapitalUsageRatio > 0 && cfg.CapitalAllocator.MaxTotalCapitalUsageRatio <= 1,
			"CapitalAllocator MaxTotalCapitalUsageRatio 必须在 (0, 1] 之间")
		appendIssue(cfg.CapitalAllocator.MaxDailyLossUSDT >= 0, "CapitalAllocator MaxDailyLossUSDT 不能小于 0")
		switch strings.TrimSpace(cfg.CapitalAllocator.Mode) {
		case "plan", "profit", "data_collection":
		default:
			appendIssue(false, "CapitalAllocator Mode 必须是 plan|profit|data_collection 之一")
		}
	}
	if cfg.DataCollection.Enabled {
		appendIssue(cfg.DataCollection.MaxPositions >= 1, "DataCollection MaxPositions 必须 ≥ 1")
		appendIssue(cfg.DataCollection.MaxNotionalUSDT > 0, "DataCollection MaxNotionalUSDT 必须大于 0")
		appendIssue(cfg.DataCollection.MaxExpectedLossUSDTPerTrade >= 0, "DataCollection MaxExpectedLossUSDTPerTrade 不能小于 0")
		switch strings.TrimSpace(cfg.DataCollection.Mode) {
		case "plan", "data_collection", "profit":
		default:
			appendIssue(false, "DataCollection Mode 必须是 plan|data_collection|profit 之一")
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("EngineConfig 校验失败: %s", strings.Join(issues, "; "))
}
