package core

import (
	"testing"
	"time"
)

func TestParseClusterMap(t *testing.T) {
	// 空串 → 空 map,不报错。
	if m, err := ParseClusterMap(""); err != nil || len(m) != 0 {
		t.Fatalf("空串应返回空 map 且无错, got m=%v err=%v", m, err)
	}
	// 正常多组 + 大小写归一 + 空白容忍。
	m, err := ParseClusterMap(" majors = BTC, eth ;MEMES=doge,SHIB ")
	if err != nil {
		t.Fatalf("合法映射不应报错: %v", err)
	}
	for asset, want := range map[string]string{"BTC": "MAJORS", "ETH": "MAJORS", "DOGE": "MEMES", "SHIB": "MEMES"} {
		if m[asset] != want {
			t.Fatalf("资产 %s 应映射到 %s, got %s", asset, want, m[asset])
		}
	}
	// 缺 '='、空簇名、空资产组、资产映射到多簇 → 报错。
	for _, bad := range []string{"BTC,ETH", "=BTC", "MAJORS=", "MAJORS=BTC;MEMES=BTC"} {
		if _, err := ParseClusterMap(bad); err == nil {
			t.Fatalf("非法映射 %q 应报错", bad)
		}
	}
}

func sampleValidConfig() EngineConfig {
	return EngineConfig{
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
			DashboardAddr:            "127.0.0.1:8080",
			RecordDir:                "records",
			EmergencyStopFile:        ".halt-live",
		},
		StrategyConfig: StrategyConfig{
			WakeMinute:               57,
			ScanWindow:               3 * time.Minute,
			MonitorInterval:          500 * time.Millisecond,
			MonitorStopLead:          30 * time.Second,
			LiveFundingDirectionMode: "both",
			MinEstimatedFundingRate:  0.0055,
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
			ExitMonitorInterval:      250 * time.Millisecond,
			OpenPreparationLead:      5 * time.Second,
			PreOpenNoGoLead:          1 * time.Second,
			PreOpenTickerMaxAge:      1 * time.Second,
			RecvWindow:               5000,
			ExecutionConfirmTimeout:  1500 * time.Millisecond,
			ExecutionRecoveryTimeout: 4 * time.Second,
			OpenHedgeMaxConcurrency:  2,
			SpotLimitSlippageRate:    0.0015,
			FuturesLimitSlippageRate: 0.0010,
			SpotSTPMode:              "EXPIRE_TAKER",
			FuturesSTPMode:           "EXPIRE_TAKER",
			FuturesMarginType:        "CROSSED",
			FuturesLeverage:          2,
		},
		RiskConfig: RiskConfig{
			MinNetProfitRate:              0.0020,
			ImmediateInvalidateRate:       0.0005,
			MonitorMaxConsecutiveBreach:   3,
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
			MaxConsecutiveLossCycles:      3,
			MaxAccountDrawdownUSDT:        60.0,
		},
		HistoricalConfig: HistoricalConfig{
			DefaultLookbackDays:                    30,
			InitialEquityUSDT:                      20.0,
			StopEquityUSDT:                         10.0,
			PerLegNotionalUSDT:                     10.0,
			FundingDirectionMode:                   "both",
			MaxFundingCyclesHeld:                   1,
			MaxPerLegEquityShare:                   0.50,
			SpotTakerFeeRate:                       0.0010,
			FuturesTakerFeeRate:                    0.0005,
			AssumedHourlyBorrowInterestRate:        0.00005,
			BorrowInterestMinBillingHours:          1,
			NegativeCarrySpotEntryImpactMultiplier: 1.5,
			NegativeCarrySpotExitImpactMultiplier:  1.5,
			CrossStableQuoteMaxDeviationRate:       0.0050,
			ResidualFillHaircutEnabled:             true,
			ExpectedLegFillRatio:                   0.99,
			LegRejectProbability:                   0.01,
			EmergencyHedgeSpreadMultiplier:         4.0,
			FundingPhaseLead:                       30 * time.Second,
			FundingFetchConcurrency:                8,
			FundingRetryMax:                        2,
			ReferenceKlineLookback:                 60,
			EstimateKlineLookback:                  180,
			SafetyBufferFloorUSDT:                  0.001,
			SafetyBufferCostMultiplier:             0.35,
			MaxDrawdownRate:                        0.25,
			MaxConsecutiveLosses:                   2,
			MaxFundingRate:                         0.0300,
			MaxFundingRateJump:                     0.0150,
			MaxSingleTradeLossUSDT:                 1.5,
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
		PortfolioConfig: PortfolioConfig{
			EnableRollingHold:                 false,
			DynamicSizingEnabled:              false,
			MaxPortfolioSymbols:               10,
			MaxPortfolioNotionalUSDT:          0,
			RevalidateLead:                    5 * time.Minute,
			HoldRollThresholdRate:             0,
			RollSafetyBufferRate:              0,
			RebalanceHysteresisCostMultiplier: 1.0,
			PerSymbolHardCapUSDT:              0,
			ClusterMaxNotionalUSDT:            0,
			MaxBorrowNotionalPerAssetUSDT:     0,
			CapitalRampSchedule:               "",
		},
	}
}

func TestEngineConfigValidatePasses(t *testing.T) {
	if err := sampleValidConfig().Validate(); err != nil {
		t.Fatalf("expected valid config to pass, got %v", err)
	}
}

func TestEngineConfigValidateRejectsBadLeverage(t *testing.T) {
	cfg := sampleValidConfig()
	cfg.ExecutionConfig.FuturesLeverage = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid leverage to fail validation")
	}
}

// TestPortfolioConfigDefaultsValid 钉死 §16.7 滚动持有/动态仓位配置默认值（全关闭、滞后带 1.0）通过校验，
// 对运行零影响。
func TestPortfolioConfigDefaultsValid(t *testing.T) {
	if err := DefaultEngineConfig().Validate(); err != nil {
		t.Fatalf("默认 PortfolioConfig 应通过校验: %v", err)
	}
}

// TestPortfolioConfigValidationGates 钉死 Portfolio 校验的「恒检非负 + 启用时门控」语义。
func TestPortfolioConfigValidationGates(t *testing.T) {
	// 滞后带 < 1.0 恒拒（防 churn 下界）。
	cfg := sampleValidConfig()
	cfg.PortfolioConfig.RebalanceHysteresisCostMultiplier = 0.5
	if err := cfg.Validate(); err == nil {
		t.Fatal("RebalanceHysteresisCostMultiplier<1.0 应被拒")
	}

	// EnableRollingHold 但 RevalidateLead<=0 应拒。
	cfg = sampleValidConfig()
	cfg.PortfolioConfig.EnableRollingHold = true
	cfg.PortfolioConfig.RevalidateLead = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("EnableRollingHold 且 RevalidateLead<=0 应被拒")
	}

	// EnableRollingHold + 合法参数应通过。
	cfg = sampleValidConfig()
	cfg.PortfolioConfig.EnableRollingHold = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("EnableRollingHold + 合法参数应通过: %v", err)
	}

	// DynamicSizingEnabled 但无法推导单币硬顶应拒。
	cfg = sampleValidConfig()
	cfg.PortfolioConfig.DynamicSizingEnabled = true
	cfg.PortfolioConfig.PerSymbolHardCapUSDT = 0
	cfg.PortfolioConfig.MaxPortfolioNotionalUSDT = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("DynamicSizingEnabled 且无法推导单币硬顶应被拒")
	}

	// DynamicSizingEnabled + 显式硬顶应通过。
	cfg = sampleValidConfig()
	cfg.PortfolioConfig.DynamicSizingEnabled = true
	cfg.PortfolioConfig.PerSymbolHardCapUSDT = 1000
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DynamicSizingEnabled + 显式硬顶应通过: %v", err)
	}
}

func TestConfigHashStableAndSensitive(t *testing.T) {
	cfg := sampleValidConfig()
	h1 := cfg.ConfigHash()
	if h1 == "" || h1 != cfg.ConfigHash() {
		t.Fatalf("expected stable non-empty hash, got %q", h1)
	}
	cfg.RiskConfig.MaxCycleLossUSDT++
	if cfg.ConfigHash() == h1 {
		t.Fatal("expected hash to change when config changes")
	}
}

func TestApplyProfileSmallCapitalTightensBudgets(t *testing.T) {
	cfg := sampleValidConfig()
	cfg.ApplyProfile(ProfileSmallCapital)
	if cfg.ActiveProfile != ProfileSmallCapital {
		t.Fatalf("expected active profile small_capital, got %s", cfg.ActiveProfile)
	}
	if cfg.StrategyConfig.MaxTargets != 2 || cfg.RiskConfig.MaxDailyOpenNotionalUSDT != 500.0 {
		t.Fatalf("expected small-capital budgets applied, got targets=%d dailyNotional=%.1f", cfg.StrategyConfig.MaxTargets, cfg.RiskConfig.MaxDailyOpenNotionalUSDT)
	}
}
