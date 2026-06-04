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
	"strings"
	"time"
)

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
	HTTPTimeout              time.Duration
	MaxPremiumIndexStaleness time.Duration
	StreamWarmupTimeout      time.Duration
	MaxIdleConns             int
	MaxIdleConnsPerHost      int
	IdleConnTimeout          time.Duration
	TLSHandshakeTimeout      time.Duration
	ResponseHeaderTimeout    time.Duration
	ExpectContinueTimeout    time.Duration
	PublicGETBaseSlot        time.Duration
	PublicGETCooldown403     time.Duration
	PublicGETCooldown418     time.Duration
	PublicGETUseAPIKey       bool
	PublicGETAPIKeyWhitelist string
	DashboardAddr            string
	RecordDir                string
	EmergencyStopFile        string
}

// StrategyConfig 表示扫描、监控和容量筛选配置。
type StrategyConfig struct {
	WakeMinute               int
	WakeSecond               int
	ScanWindow               time.Duration
	MonitorInterval          time.Duration
	MonitorStopLead          time.Duration
	LiveFundingDirectionMode string
	MinEstimatedFundingRate  float64
	MaxEstimatedFundingRate  float64
	MaxFundingRateJump       float64
	MaxScanBasisRate         float64
	LiveFundingAsset         string
	FeeReserveRate           float64
	ExitSpreadReserveRate    float64
	PreferredSpotQuoteAssets string
	MaxTargets               int
	PerSymbolQuoteUSDT       float64
	RecentTradeVolumeWindow  time.Duration
	MaxRecentVolumeShare     float64
	MaxOpenInterestShare     float64
	MaxVisibleDepthShare     float64
	DepthLevels              int
}

// ExecutionConfig 表示下单确认、恢复和退出执行参数。
type ExecutionConfig struct {
	ExitDelayAfterFunding    time.Duration
	ExitMonitorInterval      time.Duration
	AutoBalanceUSDT          bool
	OpenPreparationLead      time.Duration
	PreOpenNoGoLead          time.Duration
	PreOpenTickerMaxAge      time.Duration
	RecvWindow               int64
	ExecutionConfirmTimeout  time.Duration
	ExecutionRecoveryTimeout time.Duration
	OpenHedgeMaxConcurrency  int
	ParallelOpenSymbols      string
	ParallelCloseSymbols     string
	UseProtectedIOC          bool
	SpotLimitSlippageRate    float64
	FuturesLimitSlippageRate float64
	FuturesPriceProtect      bool
	SpotSTPMode              string
	FuturesSTPMode           string
	FuturesMarginType        string
	FuturesLeverage          int
	// ShadowTrading 为 true 时进入「影子(只读冒烟)」模式：引擎照常对接真实行情与账户、照常完成扫描/评估/
	// 决策，但任何会改变交易所状态或动用资金的签名请求(下单/撤单/改杠杆/划转/借还币等一切非 GET)都会在
	// 唯一咽喉点 signedRequest 被硬拦截、绝不触达交易所，只把下单意图落日志。用于实盘前抓灾难性 bug
	// (下错方向/异常数量/失控循环)，而非校准经济阈值。默认 false。
	ShadowTrading bool
}

// RiskConfig 表示实盘风控阈值和冷却预算。
type RiskConfig struct {
	MinNetProfitRate              float64
	ImmediateInvalidateRate       float64
	MonitorMaxConsecutiveBreach   int
	ResidualSpotAlertUSDT         float64
	ResidualPositionAlertQty      float64
	FundingIncomeDiffAlertUSDT    float64
	SettlementDiffAlertUSDT       float64
	MaxExecutionIncidents         int
	MaxCycleLossUSDT              float64
	MaxCycleOpenNotionalUSDT      float64
	MaxOrderUncertaintyPerCycle   int
	MaxConsecutivePlatformRejects int
	MaxConsecutiveRequestErrors   int
	ProtectedIOCMinAttempts       int
	ProtectedIOCMaxUnfilledRatio  float64
	ProtectedIOCMaxCancelRate     float64
	SymbolAbnormalCooldown        time.Duration
	PlatformMinorCooldown         time.Duration
	PlatformAbnormalCooldown      time.Duration
	PlatformCriticalCooldown      time.Duration
	MaxRecoveryActionsPerCycle    int
	MaxDailyLossUSDT              float64
	MaxDailyOrderCount            int
	MaxDailyOpenNotionalUSDT      float64
	// MaxConsecutiveLossCycles 是实盘连续亏损熔断阈值：当「按 funding 实收结算后的真实净利」连续为负的
	// 周期数达到该值时，触发日级熔断、停止继续开仓，要求人工复核。0 表示关闭该维度。它对应回测里的
	// MaxConsecutiveLosses，把同一条系统性失效保护从回测补齐到实盘，且只会停机（fail-safe），不会下新单。
	MaxConsecutiveLossCycles int
	// MaxAccountDrawdownUSDT 是账户级「高水位回撤」熔断阈值（USDT）：以全部已结算周期净利的累计曲线为准，
	// 当从历史最高累计净利(high-water mark)回撤的绝对额达到该值时停机待人工复核。它补的是 MaxDailyLossUSDT
	// 的盲区——日亏每个 UTC 日清零，抓不住「每天小亏、跨日慢性失血」累计掏空账户的情形。回撤额完全由已落盘
	// 结算记录回放得出，无需权益基线、不依赖现货 mark-to-market，重启后可重算复原。0 表示关闭该维度。
	MaxAccountDrawdownUSDT float64
	// HedgeImbalanceAbortUSDT / HedgeImbalanceAbortRate 是 Gate 4 关键不变量守卫的阈值：
	// 当 |现货腿名义 − 合约腿名义| 的绝对值(USDT)或相对比例超过阈值时立即熔断，防止裸敞口。
	// 默认 0 表示该维度不启用（不改变既有行为，opt-in）。
	HedgeImbalanceAbortUSDT float64
	HedgeImbalanceAbortRate float64
	// HoldHedgeGuardEnabled 把对冲偏离守卫从「仅开仓校验」扩展到「持有期周期性校验」(M0.2b)：
	// 启用后,持仓期常驻监控会按 HedgeImbalanceAbort{USDT,Rate} 阈值周期性复核两腿实时名义,
	// 偏离突破即对该 symbol 走方向感知双腿平仓(与强平/ADL 处置一致)。默认 false(opt-in,零行为变更)。
	// 当前仅对正 carry(现货腿在现货钱包,可实时测量)生效;负 carry(全仓杠杆借币空头)的持有期复核
	// 依赖 cross-margin 实时健康数据(S2.4)接入后再覆盖。
	HoldHedgeGuardEnabled bool
	// FeedBlindnessGuardEnabled 启用「持有期断连盲窗守卫」(R32)：常驻监控以行情流(bookTicker 持续推送、
	// 判活可靠)为准驱动状态机——降级(degraded)即暂停本轮开新仓;持续失明(blind 超过宽限)即对托管敞口
	// 保守撤离(panic flatten 双腿平仓 + 平台熔断进入冷却,冷却结束自动恢复)。默认 false(opt-in,零行为变更)。
	FeedBlindnessGuardEnabled bool
	// MinLiquidationDistanceRate 是开仓前「强平距离」安全闸：估算合约腿开仓后从入场价到强平价可承受的
	// 逆向价格移动幅度（占入场价比例），低于该阈值则跳过该候选、不开仓（仅冷却该 symbol，非熔断）。
	// 距离用隔离保证金近似导出（可吸收浮亏 = 初始保证金 − 维持保证金，再除以名义），对全仓(CROSSED)模式
	// 是保守下界——全仓有整账户权益兜底，真实距离更大，因此用它作开仓前闸只会更安全。它防的是「杠杆相对
	// 持有期可能的价格波动设得过高、一波行情就把单腿合约打爆、对冲随之失效裸奔」。默认 0.15（15%）：
	// 配默认 2x 杠杆时实际距离约 49%，永不触发；只有当杠杆被调高到安全距离不足时才作为防爆刹车。
	// 0 表示关闭该维度。
	MinLiquidationDistanceRate float64
}

// HistoricalConfig 表示月度模拟与历史验证配置。
type HistoricalConfig struct {
	DefaultLookbackDays                    int
	InitialEquityUSDT                      float64
	StopEquityUSDT                         float64
	PerLegNotionalUSDT                     float64
	FundingDirectionMode                   string
	MaxFundingCyclesHeld                   int
	BatchExperimentEnabled                 bool
	BatchSpotTakerFeeRates                 string
	BatchPerLegNotionalUSDT                string
	BatchMaxFundingCyclesHeld              string
	MaxPerLegEquityShare                   float64
	SpotTakerFeeRate                       float64
	FuturesTakerFeeRate                    float64
	AssumedHourlyBorrowInterestRate        float64
	BorrowInterestMinBillingHours          int
	NegativeCarrySpotEntryImpactMultiplier float64
	NegativeCarrySpotExitImpactMultiplier  float64
	CrossStableQuoteMaxDeviationRate       float64
	ResidualFillHaircutEnabled             bool
	ExpectedLegFillRatio                   float64
	LegRejectProbability                   float64
	EmergencyHedgeSpreadMultiplier         float64
	FundingPhaseLead                       time.Duration
	FundingFetchConcurrency                int
	FundingRetryMax                        int
	CacheDir                               string
	ReferenceKlineLookback                 int
	EstimateKlineLookback                  int
	SafetyBufferFloorUSDT                  float64
	SafetyBufferCostMultiplier             float64
	MaxDrawdownRate                        float64
	MaxConsecutiveLosses                   int
	MaxFundingRate                         float64
	MaxFundingRateJump                     float64
	MaxSingleTradeLossUSDT                 float64
	FundingFetchSlot                       time.Duration
	FundingFetchTimeout                    time.Duration
	GroupEvaluationTimeout                 time.Duration
	CandidateBuildTimeout                  time.Duration
	CandidateFinalizeTimeout               time.Duration
	ScreeningConcurrency                   int
	DeepCandidateLimit                     int
	CoarseSymbolLimit                      int
	FundingGroupTopSymbolsPerTime          int
	LiquidityShareMin                      float64
	LiquidityShareFloor                    float64
	LiquidityShareMax                      float64
	SevereLiquidityShareCap                float64
	SevereLiquidityShareMultiplier         float64
	SevereLiquidityShareMax                float64
}

// EngineConfig 是 Funding Rate Sniper Engine 的 MVP 运行配置。
type EngineConfig struct {
	ActiveProfile EngineProfile // 运行偏好档位

	NetworkConfig
	StrategyConfig
	ExecutionConfig
	RiskConfig
	HistoricalConfig
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

	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("EngineConfig 校验失败: %s", strings.Join(issues, "; "))
}
