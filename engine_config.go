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
	ActiveProfile EngineProfile // 新增：运行偏好档位

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
	case ProfileStandard:
		// 保持默认值不变
	}
}

// DefaultEngineConfig 返回适合 MVP 的默认配置。
func DefaultEngineConfig() EngineConfig {
	cfg := EngineConfig{
		ActiveProfile: ProfileStandard,
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
			LiveFundingDirectionMode: "positive_only",
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
			MaxVisibleDepthShare:     0.05,
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
			BatchExperimentEnabled:                 false,
			BatchSpotTakerFeeRates:                 "0.0010,0.0000",
			BatchPerLegNotionalUSDT:                "5,10,20",
			BatchMaxFundingCyclesHeld:              "1",
			MaxPerLegEquityShare:                   0.50,
			SpotTakerFeeRate:                       0.0010,
			FuturesTakerFeeRate:                    0.0005,
			AssumedHourlyBorrowInterestRate:        0.00005,
			BorrowInterestMinBillingHours:          1,
			NegativeCarrySpotEntryImpactMultiplier: 1.5,
			NegativeCarrySpotExitImpactMultiplier:  1.5,
			CrossStableQuoteMaxDeviationRate:       0.0050,
			FundingPhaseLead:                       30 * time.Second,
			FundingFetchConcurrency:                8,
			FundingRetryMax:                        2,
			CacheDir:                               "records/historical_cache",
			ReferenceKlineLookback:                 60,
			EstimateKlineLookback:                  180,
			SafetyBufferFloorUSDT:                  0.001,
			SafetyBufferCostMultiplier:             0.35,
			MaxDrawdownRate:                        0.25,
			MaxConsecutiveLosses:                   2,
			MaxFundingRate:                         0.0300,
			MaxFundingRateJump:                     0.0150,
			MaxSingleTradeLossUSDT:                 1.5,
			FundingFetchSlot:                       0,
			FundingFetchTimeout:                    60 * time.Second,
			GroupEvaluationTimeout:                 45 * time.Second,
			CandidateBuildTimeout:                  12 * time.Second,
			CandidateFinalizeTimeout:               15 * time.Second,
			ScreeningConcurrency:                   10,
			DeepCandidateLimit:                     24,
			CoarseSymbolLimit:                      96,
			FundingGroupTopSymbolsPerTime:          48,
			LiquidityShareMin:                      0.01,
			LiquidityShareFloor:                    0.08,
			LiquidityShareMax:                      0.25,
			SevereLiquidityShareCap:                0.20,
			SevereLiquidityShareMultiplier:         2.5,
			SevereLiquidityShareMax:                0.50,
		},
	}
	return cfg
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
