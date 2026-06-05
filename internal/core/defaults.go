package core

import "time"

// DefaultEngineConfig 返回标准档的默认运行配置。
//
// 默认值属于配置内核，集中在 core，供 package main 与 internal/exchange
// 等下层包共享（避免下层反向依赖 main）。
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
			WakeMinute:      57,
			WakeSecond:      0,
			ScanWindow:      3 * time.Minute,
			MonitorInterval: 500 * time.Millisecond,
			MonitorStopLead: 30 * time.Second,
			// both：正向(现货持有+合约做空，吃正费率)与负向(全仓杠杆借币做空现货+合约做多，吃负费率)
			// 两种 carry 都开。负向 carry 依赖已开通且可借币的全仓杠杆账户，并会产生借币利息。
			LiveFundingDirectionMode: "both",
			// 单周期对冲往返的真实盈亏平衡线：FeeReserveRate(0.0030)+ExitSpreadReserveRate(0.0005)+
			// MinNetProfitRate(0.0020)=0.0055。低于此值的预估资金费率开仓注定亏损或被监控期净利门拒绝，
			// 故地板线下沉到与成本自洽的 0.0055，而非掩盖真实成本的 0.0025。
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
			// 全仓(CROSSED)是 delta 中性对冲的刻意选择：给合约腿更大的强平缓冲，避免单腿被强平后留下裸现货腿。
			// 杠杆从 3x 降到 2x：资金费按名义计提、与杠杆无关，降杠杆不影响收费，却把同名义下的初始保证金
			// 抬高 50%，使强平价更远——对「小散户求稳不爆」是纯安全收益（代价仅是合约钱包占用略增，且
			// 开仓前 assessOpenFuturesLeverage 会在保证金不足时自动拒单兜底）。
			FuturesLeverage: 2,
		},
		RiskConfig: RiskConfig{
			MinNetProfitRate:              0.0020,
			ImmediateInvalidateRate:       0.0005,
			MonitorMaxConsecutiveBreach:   3,
			ResidualSpotAlertUSDT:         3.0,
			ResidualPositionAlertQty:      1e-8,
			FundingIncomeDiffAlertUSDT:    1.0,
			SettlementDiffAlertUSDT:       3.0,
			SettlementDiffRate:            0.20,
			SettlementTotalDiffAlertUSDT:  0,
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
			// HedgeImbalanceAbortRate 默认开启「相对偏离」维度的关键不变量守卫(Gate 4)：两腿名义偏离
			// 超过较大腿的 10% 即熔断。该比例尺度无关，在「数百 U → 100k」全资金区间一致有效，不会像
			// 绝对额阈值那样随资金规模失真。10% 足以容忍正常开仓的步进取整 / 基差，又能抓住「某腿半成交
			// 或未成交」导致的裸敞口。绝对额维度(HedgeImbalanceAbortUSDT)随资金规模难取统一默认，保持 0
			// (按需开启)。
			HedgeImbalanceAbortRate: 0.10,
			// MinLiquidationDistanceRate 默认 0.15：配默认 2x 杠杆时合约腿实际强平距离约 49%，远高于该阈值、
			// 永不触发（零行为变更）；它只在杠杆被调高、安全距离不足时作为开仓前防爆刹车。
			MinLiquidationDistanceRate: 0.15,
			// R34 pending funding 处置默认开启,阈值取宽松值(正常运行永不触发,只兜住真正卡死的周期):
			// 重试 30 次或挂起 12h 仍未到账 → critical 升级 + 停重试 + cycle 隔离。
			MaxFundingSettlementAttempts: 30,
			MaxFundingSettlementAge:      12 * time.Hour,
			// S6 策略健康守卫:默认跟踪最近 50 个已结算周期的实测净 edge,但阈值闸默认关闭(MinRealizedNetEdgeRate=0)——
			// 只观测不强制降档,阈值待实盘标定后再开(opt-in,零行为变更)。
			EdgeWindowCycles:       50,
			MinRealizedNetEdgeRate: 0,
			EdgeGuardMinSamples:    20,
			// S7-B 交易所 dead-man:默认关闭;启用后 30s 倒计时(每 ~10s 续期),进程失联即由交易所兜底撤单。
			DeadMansSwitchEnabled:   false,
			DeadMansSwitchCountdown: 30 * time.Second,
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
			ResidualFillHaircutEnabled:             true,
			ExpectedLegFillRatio:                   0.99,
			LegRejectProbability:                   0.01,
			EmergencyHedgeSpreadMultiplier:         4.0,
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
		PortfolioConfig: PortfolioConfig{
			// 行为开关默认关闭：保持既有「单周期、静态 size」行为，对运行零影响（opt-in）。
			EnableRollingHold:    false,
			DynamicSizingEnabled: false,
			// L3 组合约束：币数上限按架构目标 10；总名义上限默认 0（不启用，由灰度计划调高，见 §16.2.1）。
			MaxPortfolioSymbols:      10,
			MaxPortfolioNotionalUSDT: 0,
			// 续持复核提前量 = :55（结算前 5min 做 keep/drop 复核）。
			RevalidateLead: 5 * time.Minute,
			// 续持门槛 / 安全垫待实盘标定，默认 0（启用滚动持有时须显式配置）。
			HoldRollThresholdRate: 0,
			RollSafetyBufferRate:  0,
			// 换仓滞后带默认 1.0（净 carry 优势须超过完整往返成本才换仓，防 churn）。
			RebalanceHysteresisCostMultiplier: 1.0,
			// 单币硬顶 / 簇上限 / 单资产借币上限默认 0（不约束 / 按组合上限派生），待标定。
			PerSymbolHardCapUSDT:          0,
			ClusterMaxNotionalUSDT:        0,
			MaxBorrowNotionalPerAssetUSDT: 0,
			CapitalRampSchedule:           "",
		},
		AlertingConfig: AlertingConfig{
			// 实时告警默认关闭：禁用时不做任何外部推送,关键事件仍走既有日志/事件落盘,对运行零影响(opt-in)。
			Enabled:         false,
			WebhookURL:      "",
			MinSeverity:     "warning",
			DedupWindow:     5 * time.Minute,
			DispatchTimeout: 5 * time.Second,
		},
	}
	return cfg
}
