package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SecretConfig 表示从 secret.yaml 读取的最小认证配置。
type SecretConfig struct {
	APIKey   string
	Secret   string
	ProxyURL string
}

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
	LiquidityShareMin                      float64
	LiquidityShareFloor                    float64
	LiquidityShareMax                      float64
	SevereLiquidityShareCap                float64
	SevereLiquidityShareMultiplier         float64
	SevereLiquidityShareMax                float64
}

// EngineConfig 是 Funding Rate Sniper Engine 的 MVP 运行配置。
type EngineConfig struct {
	NetworkConfig
	StrategyConfig
	ExecutionConfig
	RiskConfig
	HistoricalConfig
}

// DefaultEngineConfig 返回适合 MVP 的默认配置。
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		NetworkConfig: NetworkConfig{
			HTTPTimeout:              10 * time.Second,
			MaxPremiumIndexStaleness: 15 * time.Second,
			StreamWarmupTimeout:      1500 * time.Millisecond,
			MaxIdleConns:             256,
			MaxIdleConnsPerHost:      128,
			IdleConnTimeout:          90 * time.Second,
			TLSHandshakeTimeout:      3 * time.Second,
			ResponseHeaderTimeout:    5 * time.Second,
			ExpectContinueTimeout:    1 * time.Second,
			PublicGETBaseSlot:        120 * time.Millisecond,
			PublicGETCooldown403:     5 * time.Second,
			PublicGETCooldown418:     30 * time.Second,
			PublicGETUseAPIKey:       false,
			PublicGETAPIKeyWhitelist: "exchangeInfo,premiumIndex,openInterest,klines",
			DashboardAddr:            "127.0.0.1:8080",
			RecordDir:                "records",
			EmergencyStopFile:        ".halt-live",
		},
		StrategyConfig: StrategyConfig{
			WakeMinute:               57,
			WakeSecond:               0,
			ScanWindow:               3 * time.Minute,
			MonitorInterval:          500 * time.Millisecond,
			MonitorStopLead:          30 * time.Second,
			MinEstimatedFundingRate:  0.0025,
			MaxEstimatedFundingRate:  0.0300,
			MaxFundingRateJump:       0.0150,
			MaxScanBasisRate:         0.0015,
			LiveFundingAsset:         "USDT",
			FeeReserveRate:           0.0030,
			ExitSpreadReserveRate:    0.0005,
			PreferredSpotQuoteAssets: "FDUSD,USDC,USDT",
			MaxTargets:               5,
			PerSymbolQuoteUSDT:       100.0,
			RecentTradeVolumeWindow:  5 * time.Minute,
			MaxRecentVolumeShare:     0.0020,
			MaxOpenInterestShare:     0.0005,
			MaxVisibleDepthShare:     0.08,
			DepthLevels:              20,
		},
		ExecutionConfig: ExecutionConfig{
			ExitDelayAfterFunding:    2 * time.Second,
			ExitMonitorInterval:      250 * time.Millisecond,
			AutoBalanceUSDT:          true,
			OpenPreparationLead:      5 * time.Second,
			PreOpenNoGoLead:          1 * time.Second,
			PreOpenTickerMaxAge:      1 * time.Second,
			RecvWindow:               5000,
			ExecutionConfirmTimeout:  1500 * time.Millisecond,
			ExecutionRecoveryTimeout: 4 * time.Second,
			OpenHedgeMaxConcurrency:  2,
			ParallelOpenSymbols:      "",
			ParallelCloseSymbols:     "",
			UseProtectedIOC:          true,
			SpotLimitSlippageRate:    0.0015,
			FuturesLimitSlippageRate: 0.0010,
			FuturesPriceProtect:      true,
			SpotSTPMode:              "EXPIRE_TAKER",
			FuturesSTPMode:           "EXPIRE_TAKER",
			FuturesMarginType:        "CROSSED",
			FuturesLeverage:          3,
		},
		RiskConfig: RiskConfig{
			MinNetProfitRate:              0.0020,
			ImmediateInvalidateRate:       0.0005,
			MonitorMaxConsecutiveBreach:   3,
			ResidualSpotAlertUSDT:         3.0,
			ResidualPositionAlertQty:      1e-8,
			FundingIncomeDiffAlertUSDT:    1.0,
			SettlementDiffAlertUSDT:       3.0,
			MaxExecutionIncidents:         3,
			MaxCycleLossUSDT:              8.0,
			MaxCycleOpenNotionalUSDT:      300.0,
			MaxOrderUncertaintyPerCycle:   2,
			MaxConsecutivePlatformRejects: 3,
			MaxConsecutiveRequestErrors:   6,
			ProtectedIOCMinAttempts:       5,
			ProtectedIOCMaxUnfilledRatio:  0.40,
			ProtectedIOCMaxCancelRate:     0.30,
			SymbolAbnormalCooldown:        10 * time.Minute,
			PlatformMinorCooldown:         5 * time.Minute,
			PlatformAbnormalCooldown:      15 * time.Minute,
			PlatformCriticalCooldown:      30 * time.Minute,
			MaxRecoveryActionsPerCycle:    3,
			MaxDailyLossUSDT:              25.0,
			MaxDailyOrderCount:            40,
			MaxDailyOpenNotionalUSDT:      1500.0,
		},
		HistoricalConfig: HistoricalConfig{
			DefaultLookbackDays:                    30,
			InitialEquityUSDT:                      20.0,
			StopEquityUSDT:                         10.0,
			PerLegNotionalUSDT:                     10.0,
			FundingDirectionMode:                   "both",
			MaxFundingCyclesHeld:                   1,
			BatchExperimentEnabled:                 true,
			BatchSpotTakerFeeRates:                 "0.0010,0.0000",
			BatchPerLegNotionalUSDT:                "5,10,20",
			BatchMaxFundingCyclesHeld:              "1,2",
			MaxPerLegEquityShare:                   0.50,
			SpotTakerFeeRate:                       0.0010,
			FuturesTakerFeeRate:                    0.0005,
			AssumedHourlyBorrowInterestRate:        0.0010,
			BorrowInterestMinBillingHours:          1,
			NegativeCarrySpotEntryImpactMultiplier: 2.5,
			NegativeCarrySpotExitImpactMultiplier:  2.5,
			CrossStableQuoteMaxDeviationRate:       0.0010,
			FundingPhaseLead:                       30 * time.Second,
			FundingFetchConcurrency:                20,
			FundingRetryMax:                        6,
			CacheDir:                               "records/historical_cache",
			ReferenceKlineLookback:                 60,
			EstimateKlineLookback:                  180,
			SafetyBufferFloorUSDT:                  0.02,
			SafetyBufferCostMultiplier:             0.50,
			MaxDrawdownRate:                        0.25,
			MaxConsecutiveLosses:                   2,
			MaxFundingRate:                         0.0300,
			MaxFundingRateJump:                     0.0150,
			MaxSingleTradeLossUSDT:                 1.5,
			FundingFetchSlot:                       0,
			FundingFetchTimeout:                    35 * time.Second,
			GroupEvaluationTimeout:                 45 * time.Second,
			CandidateBuildTimeout:                  12 * time.Second,
			CandidateFinalizeTimeout:               15 * time.Second,
			ScreeningConcurrency:                   10,
			DeepCandidateLimit:                     24,
			LiquidityShareMin:                      0.01,
			LiquidityShareFloor:                    0.08,
			LiquidityShareMax:                      0.25,
			SevereLiquidityShareCap:                0.20,
			SevereLiquidityShareMultiplier:         2.5,
			SevereLiquidityShareMax:                0.50,
		},
	}
}

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
	appendIssue(isValidSelfTradePreventionMode(cfg.ExecutionConfig.SpotSTPMode), "SpotSTPMode 必须是 EXPIRE_TAKER / EXPIRE_MAKER / EXPIRE_BOTH / NONE")
	appendIssue(isValidSelfTradePreventionMode(cfg.ExecutionConfig.FuturesSTPMode), "FuturesSTPMode 必须是 EXPIRE_TAKER / EXPIRE_MAKER / EXPIRE_BOTH / NONE")
	appendIssue(strings.EqualFold(strings.TrimSpace(cfg.ExecutionConfig.FuturesMarginType), "CROSSED"), "FuturesMarginType 当前必须为 CROSSED")
	appendIssue(cfg.ExecutionConfig.FuturesLeverage >= 1 && cfg.ExecutionConfig.FuturesLeverage <= 20, "FuturesLeverage 必须在 1 到 20 之间")

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
	appendIssue(isValidHistoricalFundingDirectionMode(cfg.HistoricalConfig.FundingDirectionMode), "Historical FundingDirectionMode 必须是 positive_only、negative_only 或 both")
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

func isValidSelfTradePreventionMode(mode string) bool {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "NONE", "EXPIRE_TAKER", "EXPIRE_MAKER", "EXPIRE_BOTH":
		return true
	default:
		return false
	}
}

// LoadSecretConfig 读取当前项目中的 secret.yaml。
func LoadSecretConfig(path string) (SecretConfig, error) {
	values, err := readSimpleYAML(path)
	if err != nil {
		return SecretConfig{}, err
	}

	cfg := SecretConfig{
		APIKey:   values["api"],
		Secret:   values["secret"],
		ProxyURL: values["proxy_url"],
	}
	if cfg.APIKey == "" || cfg.Secret == "" {
		return SecretConfig{}, fmt.Errorf("secret.yaml 缺少 api 或 secret")
	}

	return cfg, nil
}

// NewHTTPClient 创建可选代理的 HTTP 客户端。
func NewHTTPClient(networkCfg NetworkConfig, proxyAddress string) (*http.Client, error) {
	timeout := networkCfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          networkCfg.MaxIdleConns,
		MaxIdleConnsPerHost:   networkCfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       networkCfg.IdleConnTimeout,
		TLSHandshakeTimeout:   networkCfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: networkCfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: networkCfg.ExpectContinueTimeout,
	}

	if strings.TrimSpace(proxyAddress) != "" {
		parsed, err := url.Parse(proxyAddress)
		if err != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}

// readSimpleYAML 读取当前这种 key: value 形式的简单配置文件。
func readSimpleYAML(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开配置文件失败: %w", err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("非法配置行: %s", line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		values[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	return values, nil
}
