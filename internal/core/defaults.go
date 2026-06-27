package core

import "time"

// ReferenceAccountEquityUSDT 反推 % 风控默认的基准权益(≈当前生产账户规模);equity=435 时有效阈值 ≈ 固定 USDT 默认。
const ReferenceAccountEquityUSDT = 435.0

const (
	// DefaultMaxDailyLossPct 日亏硬停占当前权益比例(25U@435≈5.7%;生产 40U@435≈9.2% 由 YAML floor 覆盖)。
	DefaultMaxDailyLossPct = 25.0 / ReferenceAccountEquityUSDT
	// DefaultMaxAccountDrawdownPct settlements 回撤占峰值权益(60U@435≈13.8%;生产 90U 由 floor 覆盖)。
	DefaultMaxAccountDrawdownPct = 60.0 / ReferenceAccountEquityUSDT
	// DefaultMaxStrategyCumulativeLossPct 单策略累计净亏占当前权益(30U@435≈7%;dev floor 35 仍生效)。
	DefaultMaxStrategyCumulativeLossPct = 30.0 / ReferenceAccountEquityUSDT
	// DefaultAccountRiskMinFundFlowStepUSDT 充提检测最小步长(USDT);低于该值的 tick 变化视为噪声。
	DefaultAccountRiskMinFundFlowStepUSDT = 5.0
	// DefaultAccountRiskMaxEquityTickDropUSDT 单 tick 权益骤降熔断(USDT);断网读 0 等异常应被跳过。
	DefaultAccountRiskMaxEquityTickDropUSDT = 50.0
	// DefaultAccountRiskMaxEquityTickDropPct 单 tick 权益骤降占上次可信权益比例(35%@435≈152U)。
	DefaultAccountRiskMaxEquityTickDropPct = 0.35

	// DefaultMaxGrossLeverage 在场总名义总杠杆倍数(5×@435≈2175U,对齐生产 open_notional≈2200)。
	DefaultMaxGrossLeverage = 5.0
	// DefaultAutoFlattenAccountDrawdownPct 二级全平占峰值权益(150U@435≈34.5%)。
	DefaultAutoFlattenAccountDrawdownPct = 150.0 / ReferenceAccountEquityUSDT
	// DefaultTotalDeployablePct 全局可部署预算=100% 真实现金权益。
	DefaultTotalDeployablePct = 1.0
)

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
			// WS(bookTicker/depth)预热等待上限：给行情流更充裕的时间填满缓存，再回退到 REST 兜底，
			// 让监控/开仓关键路径尽量读 WS 缓存而非在结算窗口现拉 REST（网络抖动时 REST 易超时）。
			StreamWarmupTimeout:      3 * time.Second,
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
			// 开火点 = 结算 − MonitorStopLead。提前到结算前 60s 开火（原 30s），给开仓序列预留更大的
			// 失败重试缓冲：单次取数/下单遇网络抖动还有时间重试，而不是一次超时就撞上结算死线。
			// 仍满足 MonitorStopLead < ScanWindow(3m) 且 > OpenPreparationLead(5s)。
			MonitorStopLead: 60 * time.Second,
			// both：正向(现货持有+合约做空，吃正费率)与负向(全仓杠杆借币做空现货+合约做多，吃负费率)
			// 两种 carry 都开。负向 carry 依赖已开通且可借币的全仓杠杆账户，并会产生借币利息。
			LiveFundingDirectionMode: "both",
			// 默认 collect_data:放松经济闸采真实成交数据;切 profit_seeking 须显式改 strategy_mode。
			StrategyMode: string(StrategyModeCollectData),
			// 单周期对冲往返的真实盈亏平衡线：FeeReserveRate(0.0030)+ExitSpreadReserveRate(0.0005)+
			// MinNetProfitRate(0.0020)=0.0055。低于此值的预估资金费率开仓注定亏损或被监控期净利门拒绝，
			// 故地板线下沉到与成本自洽的 0.0055，而非掩盖真实成本的 0.0025。
			MinEstimatedFundingRate: 0.0055,
			// 0=正负共用 MinEstimatedFundingRate(行为不变)。正 carry 免借币、无可借性风险,可单独下调;
			// 但仍被真实盈亏平衡线硬兜底,设得再低也不会放进结构性亏损单。
			PositiveMinEstimatedFundingRate: 0,
			MaxEstimatedFundingRate:         0.0300,
			MaxFundingRateJump:              0.0150,
			MaxScanBasisRate:                0.0015,
			LiveFundingAsset:                "USDT",
			FeeReserveRate:                  0.0030,
			ExitSpreadReserveRate:           0.0005,
			// 默认按账户实拉真实 taker 费率派生 FeeReserveRate(2*spot+2*futures),使经济闸据实成本判定;
			// fail-safe 回退手填 0.0030(见 StrategyConfig.AutoDeriveFeeReserveEnabled 说明)。
			AutoDeriveFeeReserveEnabled: true,
			// 出厂最安全:入场净利闸始终扣往返市场冲击(与 DynamicSizing 解耦),避免薄盘上系统性高估净利。
			ImpactReserveInGateEnabled: true,
			PreferredSpotQuoteAssets:   "FDUSD,USDC,USDT",
			MaxTargets:                 5,
			PerSymbolQuoteUSDT:         100.0,
			RecentTradeVolumeWindow:    5 * time.Minute,
			MaxRecentVolumeShare:       0.0020,
			MaxOpenInterestShare:       0.0005,
			MaxVisibleDepthShare:       0.05,
			DepthLevels:                20,
			// 撞限豁免默认关闭(出厂最安全):需人工在配置里显式开启后才生效。
			// 接近度 0.9 = 实收费率达到 symbol cap/floor 的 90% 即视为撞限;倍率 1.0 = 撞限时全额信其持续。
			CapPinnedFundingOverrideEnabled: false,
			CapPinnedFundingProximityRate:   0.9,
			CapPinnedFundingEstimateFactor:  1.0,
		},
		ExecutionConfig: ExecutionConfig{
			ExitDelayAfterFunding: 2 * time.Second,
			ExitMonitorInterval:   250 * time.Millisecond,
			AutoBalanceUSDT:       true,
			OpenPreparationLead:   5 * time.Second,
			PreOpenNoGoLead:       1 * time.Second,
			PreOpenTickerMaxAge:   1 * time.Second,
			// 默认开启战前盘口 REST 补鲜:薄盘冷门币(大负费率机会高发区)的 @bookTicker 事件流常长时间不推,
			// 仅靠缓存新鲜度会把这类「价其实没变」的标的误判成行情失效而错过开仓;补鲜给一个真·当前价的兜底。
			PreOpenTickerRESTRefreshEnabled: true,
			RecvWindow:                      5000,
			ExecutionConfirmTimeout:         1500 * time.Millisecond,
			ExecutionRecoveryTimeout:        4 * time.Second,
			OpenHedgeMaxConcurrency:         2,
			ParallelOpenSymbols:             "",
			ParallelCloseSymbols:            "",
			UseProtectedIOC:                 true,
			SpotLimitSlippageRate:           0.0015,
			FuturesLimitSlippageRate:        0.0010,
			FuturesPriceProtect:             true,
			SpotSTPMode:                     "EXPIRE_TAKER",
			FuturesSTPMode:                  "EXPIRE_TAKER",
			FuturesMarginType:               "CROSSED",
			// 全仓(CROSSED)是 delta 中性对冲的刻意选择：给合约腿更大的强平缓冲，避免单腿被强平后留下裸现货腿。
			// 杠杆从 3x 降到 2x：资金费按名义计提、与杠杆无关，降杠杆不影响收费，却把同名义下的初始保证金
			// 抬高 50%，使强平价更远——对「小散户求稳不爆」是纯安全收益（代价仅是合约钱包占用略增，且
			// 开仓前 assessOpenFuturesLeverage 会在保证金不足时自动拒单兜底）。
			FuturesLeverage: 2,
			// S8-B 执行切片:默认关闭(单发,零行为变更)。启用时建议起点:单腿名义 > 2000U 才切片,
			// 每片 ≤ 1000U,间隔 800ms,最多 8 片(具体阈值待实盘 depth/冲击校准)。
			ExecutionSlicingEnabled:    false,
			SliceThresholdNotionalUSDT: 2000,
			MaxSliceNotionalUSDT:       1000,
			SliceIntervalMs:            800,
			MaxSlices:                  8,
		},
		RiskConfig: RiskConfig{
			MinNetProfitRate:              0.0020,
			ImmediateInvalidateRate:       0.0005,
			MonitorMaxConsecutiveBreach:   3,
			ResidualSpotAlertUSDT:         3.0,
			ResidualPositionAlertQty:      1e-8,
			AutoResumeOpenHedges:          true,
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
			SymbolMinorCooldown:           5 * time.Minute,
			SymbolCriticalCooldown:        30 * time.Minute,
			PlatformMinorCooldown:         5 * time.Minute,
			PlatformAbnormalCooldown:      15 * time.Minute,
			PlatformCriticalCooldown:      30 * time.Minute,
			MaxRecoveryActionsPerCycle:    3,
			MaxDailyLossUSDT:              25.0,
			MaxDailyLossPct:               DefaultMaxDailyLossPct,
			MaxDailyOrderCount:            40,
			MaxDailyOpenNotionalUSDT:      1500.0,
			DailyMaxLosingTrades:          8,
			MaxConsecutiveLossCycles:      3,
			MaxAccountDrawdownUSDT:        60.0,
			MaxAccountDrawdownPct:         DefaultMaxAccountDrawdownPct,
			// 账户级硬风控 Stage B 一级/二级默认(数百 U 账户尺度;可在 YAML 覆盖):
			MaxStrategyCumulativeLossUSDT:    35.0,
			MaxStrategyCumulativeLossPct:     DefaultMaxStrategyCumulativeLossPct,
			MaxAccountOpenNotionalUSDT:       1200.0,
			MaxGrossLeverage:                 DefaultMaxGrossLeverage,
			AutoFlattenOnHardDrawdownEnabled: false,
			AutoFlattenAccountDrawdownUSDT:   150.0,
			AutoFlattenAccountDrawdownPct:    DefaultAutoFlattenAccountDrawdownPct,
			AccountRiskMinFundFlowStepUSDT:   DefaultAccountRiskMinFundFlowStepUSDT,
			AccountRiskMaxEquityTickDropUSDT: DefaultAccountRiskMaxEquityTickDropUSDT,
			AccountRiskMaxEquityTickDropPct:  DefaultAccountRiskMaxEquityTickDropPct,
			// HedgeImbalanceAbortRate 默认开启「相对偏离」维度的关键不变量守卫(Gate 4)：两腿名义偏离
			// 超过较大腿的 10% 即熔断。该比例尺度无关，在「数百 U → 100k」全资金区间一致有效，不会像
			// 绝对额阈值那样随资金规模失真。10% 足以容忍正常开仓的步进取整 / 基差，又能抓住「某腿半成交
			// 或未成交」导致的裸敞口。绝对额维度(HedgeImbalanceAbortUSDT)随资金规模难取统一默认，保持 0
			// (按需开启)。
			HedgeImbalanceAbortRate: 0.10,
			// 出厂最安全姿态(实盘 profile 默认武装,P3):以下关键守卫默认开启——它们只在真实异常时动作、
			// 正常运行恒不触发,且只会停机/收敛(fail-safe),不会主动放大风险:
			//   - HoldHedgeGuardEnabled:持有期逐腿对冲偏离守卫(正/负 carry 均覆盖),配 HedgeImbalanceAbortRate=0.10;
			//   - FeedBlindnessGuardEnabled:行情断连盲窗守卫(R32),失明即暂停开仓/保守撤离;
			//   - MinMarginLevel=1.5:负 carry 全仓杠杆保证金腿健康下限(无负 carry 敞口时为 no-op);
			//   - DeadMansSwitchEnabled:交易所侧 countdownCancelAll(无持仓时不发请求,进程失联兜底撤单)。
			// 任一项可在 YAML/env 显式置 false / 0 退回兼容姿态(逐字段逃生口)。
			HoldHedgeGuardEnabled:     true,
			FeedBlindnessGuardEnabled: true,
			MinMarginLevel:            1.5,
			// R7:资金费符号冲突(前向估计与上期实收严格反号=费率正在反转)时,取保守 0 而非退回旧方向乐观值,
			// 不在最危险的反转时刻按旧方向开仓。默认开启(出厂最安全),仅作用于实盘扫描/开仓,回测不受影响。
			ConservativeFundingOnSignConflictEnabled: true,
			// R7b 可信反转门槛:反向前瞻信号需达到既有费率绝对值的一半(0.5)才认定真反转并保守归零,
			// 否则视为瞬时毛刺、不否决既有强势费率(修正 EDENUSDT 类误杀)。默认 0.5(开启 R7 时生效)。
			FundingReversalConfirmRatio: 0.5,
			// R7c 前瞻基差同号守卫:预估结算价基差与盘口基差反号时,禁止仅凭绝对值更大就翻转前瞻符号。
			// 默认开启(出厂最安全),仅作用于实盘,回测不受影响。
			ConflictingPremiumSignGuardEnabled: true,
			// MinLiquidationDistanceRate 默认 0.15：配默认 2x 杠杆时合约腿实际强平距离约 49%，远高于该阈值、
			// 永不触发（零行为变更）；它只在杠杆被调高、安全距离不足时作为开仓前防爆刹车。
			MinLiquidationDistanceRate:     0.15,
			MinLiqDistanceVolScaleEnabled:  false,
			MinLiqDistanceVolScaleFactor:   1.0,
			RiskBudgetScaleEnabled:         false,
			RiskBudgetScalePerOpenPosition: 1,
			// R34 pending funding 处置默认开启,阈值取宽松值(正常运行永不触发,只兜住真正卡死的周期):
			// 重试 30 次或挂起 12h 仍未到账 → critical 升级 + 停重试 + cycle 隔离。
			MaxFundingSettlementAttempts: 30,
			MaxFundingSettlementAge:      12 * time.Hour,
			// S6 策略健康守卫:默认跟踪最近 50 个已结算周期的实测净 edge,但阈值闸默认关闭(MinRealizedNetEdgeRate=0)——
			// 只观测不强制降档,阈值待实盘标定后再开(opt-in,零行为变更)。
			EdgeWindowCycles:       50,
			MinRealizedNetEdgeRate: 0,
			EdgeGuardMinSamples:    20,
			// S7-B 交易所 dead-man:默认开启(出厂最安全);30s 倒计时(每 ~10s 续期),进程失联即由交易所
			// 兜底撤单。无活跃合约持仓时 renew 不发任何请求(deadManArmSymbols 为空),故对空仓/回测为 no-op。
			DeadMansSwitchEnabled:   true,
			DeadMansSwitchCountdown: 30 * time.Second,
			// R29 借币容量守卫:默认关闭(advisory 由人工开启);开启后要求最大可借量留 10% 余量,
			// 利率上限默认 0=不限(只查容量,避免误标定阈值挡掉正常机会)。
			BorrowCapacityCheckEnabled:    false,
			BorrowCapacityMinHeadroomRate: 0.10,
			MaxBorrowHourlyInterestRate:   0,
			// 借币不可用(-3045/-3006/-3008)且合约腿已回补时降级为「跳过+冷却」,不计熔断。默认开启(防长尾币拖垮引擎)。
			BorrowUnavailableNonFatalEnabled: true,
			// R26 API Key 权限自检姿态:默认 fail-close(读取/解析失败即拒启,出厂最安全);
			// IP 白名单默认仅告警(避免直接 brick 现有未配白名单的 Key),建议实盘置 true 转硬门。
			RequireAPIKeyPermissionCheck: true,
			RequireIPWhitelist:           false,
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
			SpotMakerFeeRate:                       0.0008,
			FuturesMakerFeeRate:                    0.0002,
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
		CrossVenueScanner: CrossVenueScannerConfig{
			Enabled:                           true,
			ScanIntervalMinutes:               10,
			MaxSnapshotAgeSeconds:             720,
			MinSpreadAPR:                      0.05,
			AlertSpreadAPR:                    1.0,
			TopN:                              30,
			PanelDisplayTopN:                  30,
			BorrowProbeEnabled:                true,
			BorrowProbeTopN:                   10,
			BorrowProbeFundingTopK:            60,
			BorrowProbeMaxLivePerScan:         30,
			BorrowKnownFreshnessHours:         24,
			BorrowDefaultDailyRateWhenUnknown: 0.001,
			BorrowDisabledVenues:              "gate",
			FundingAPRSanityCap:               50.0, // 5000%:与 crossVenueSpreadSanityCap 对齐。旧值 5.0(500%)会把荒谬上限+EV费率上限一起夹到500%,导致 H 这类双腿全爆表的极端高费币价差被压平/EV被压成负而漏开;5000% 仍能挡真正的脏数据。
			SpreadPersistenceScans:            2,
			MinTradableNotionalUSDT:           20,
			DropOneSidedZeroFunding:           true,
		},
		CrossVenueExecutor: CrossVenueExecutorConfig{
			Enabled:                                  false,
			LiveTradingEnabled:                       false,
			LoopIntervalSeconds:                      60,
			OpenSpreadAPR:                            0,
			CloseSpreadAPR:                           0.3,
			CloseSpreadConsecutiveRounds:             3,
			MaxConcurrent:                            2,
			MaxPositionUSDT:                          75,
			PerTradeMaxLossPct:                       0.08,
			MaxBookSpreadBps:                         30,
			MakerLegMaxBookSpreadBps:                 80,
			MaxOpenSlippageBps:                       15,
			SlippageBudgetBps:                        30,
			TakerSlippageBudgetBps:                   40,
			MaxNakedSeconds:                          30,
			MinLiqDistance:                           0.15,
			MaxHoldHours:                             72,
			MaxEffectiveLeverage:                     3,
			MarginSafetyFactor:                       1.0,
			MidPriceDeviationMax:                     0.03,
			MidPriceDeviationMaxExtreme:              0.08,
			EntryBasisConvergenceFraction:            0.50,
			CloseBeforeAdverseFundingEnabled:         true,
			PreFundingCloseLeadSeconds:               120,
			DepthUsageRatio:                          0.20,
			SizingMaxSlippageBps:                     30,
			SizingMaxParticipationPct24h:             0.01,
			SizingConservativeNoDepthFactor:          0.25,
			SymbolCooldownHours:                      1,
			PerpMarginTopUpBuffer:                    1.05,
			BybitAgreementCooldownHours:              6,
			DeadManCountdownSeconds:                  60,
			MinHoldHours:                             24,
			MinHoldToFirstSettlementEnabled:          true,
			MinHoldToFirstSettlementBufferSeconds:    60,
			DefaultTakerFeeBps:                       6,
			DefaultMakerFeeBps:                       2,
			AssumeMakerFills:                         true,
			ExpectedHoldHours:                        12,
			NearTermOpenEVHorizonEnabled:             true,
			NearTermOpenEVExpectedSettlements:        5,
			NearTermOpenEVMaxHoldHours:               8,
			EVExpectedSlippageBps:                    3,
			EVMaxBreakevenHours:                      24,
			EVSafetyFactor:                           1.3,
			MinNetProfitUSDT:                         0,
			CloseHardNegativeSpreadAPR:               -0.5,
			RealizedFundingPollMinutes:               5,
			MakerFirst:                               true,
			MakerChaseRounds:                         6,
			MakerChaseIntervalMs:                     1500,
			MakerDeadlineSeconds:                     12,
			MinComparableWindowHours:                 0.25,
			PostFundingCooldownSeconds:               180,
			MaxCompareAgeSeconds:                     900,
			SwitchBufferUSDT:                         0.5,
			SwitchCooldownSeconds:                    300,
			SwitchSequence:                           "close_then_open",
			MaxSwitchesPerCycle:                      1,
			PerpPerpOpeningsDefaultEnabled:           true,
			UntrackedExposureAction:                  "halt",
			DustCloseThresholdUSDT:                   1.0,
			CarryFundingEntryBaseFraction:            0.125,
			CarryFundingEntryMinVolScale:             0.2,
			CarryFundingEntryVolRefPerHour:           0.0001,
			CarryFundingEntryFloorMinutes:            5,
			CarryFundingEntryVolLookbackScans:        10,
			CarryFundingEntryMissingVolScale:         0.6,
			CarryNoHistoryHaircut:                    1.0,
			LowConfidenceSizeFloorFraction:           0.6,
			SingleVenueReverseFundingEnabled:         false,
			OpenFundingLeadSeconds:                   180,
			TimingAwarePairSelectionEnabled:          true,
			TimingAwarePairSelectionMinHours:         0.01,
			UnifiedCaptureModelEnabled:               false,
			CaptureTargetSettlements:                 1,
			BasisStopBudgetFraction:                  1.5,
			BasisStopConfirmChecks:                   2,
			BasisStabilityMinScoreForMultiSettlement: 0.7,
			CaptureFundingToCostMinRatio:             1.0,
			OpenFundingRefreshEnabled:                true,
			PlannedExitBufferSeconds:                 90,
			CaptureRollEnabled:                       true,
			CaptureMaxRolls:                          6,
			CarryHoldToPlannedExitEnabled:            true,
			ViolenceSizeBoostWeight:                  0.3,
			ViolenceRankBoostWeight:                  0.3,
		},
		CapitalAllocator: CapitalAllocatorConfig{
			Enabled:                   false,
			Mode:                      "plan",
			MaxTotalCapitalUsageRatio: 0.2,
			MaxDailyLossUSDT:          0.3,
		},
		DataCollection: DataCollectionConfig{
			Enabled:                     false,
			Mode:                        "plan",
			StrategyType:                "",
			MaxPositions:                1,
			MaxNotionalUSDT:             10,
			MaxExpectedLossUSDTPerTrade: 0.03,
		},
		UnifiedRotation: UnifiedRotationConfig{
			Enabled:                               false,
			LiveTradingEnabled:                    false,
			DelistGuardEnabled:                    true,
			DelistStatusCacheTTL:                  time.Hour,
			ContinuationCloseThreshold:            0.001,
			ContinuationMinHoldSeconds:            3600,
			SwitchMinAdvantageUSDT:                0.10,
			MinHoldToFirstSettlementEnabled:       true,
			MinHoldToFirstSettlementBufferSeconds: 60,
			TotalDeployablePct:                    DefaultTotalDeployablePct,
			CapitalAwarePerpPerpSizingEnabled:     true,
			CapitalSizingSafetyHaircut:            0.99,
		},
		OKXDirectional: OKXDirectionalConfig{
			Enabled:                     false,
			LiveTradingEnabled:          false,
			LoopIntervalSeconds:         90,
			NotionalUSDT:                30,
			Leverage:                    5,
			MaxConcurrent:               3,
			UniverseScanIntervalMinutes: 30,
			CandidatePoolN:              40,
			ShortlistN:                  20,
			UniverseTopN:                12,
			MinFundingAbsPerInterval:    0.0005,
			CandleBar:                   "5m",
			CandleLimit:                 120,
			EMAFast:                     9,
			EMASlow:                     30,
			TrendEfficiencyThreshold:    0.30,
			ZScoreEntry:                 1.2,
			RequireFundingAlignment:     true,
			FundingVetoAbsPerInterval:   0.0015,
			TradingCapitalUSDT:          30,
			StopLossPct:                 0.20, // 交易仓本金基准:20%(5X 下≈4% 价格)——收紧,让止损先于超时生效
			TakeProfitPct:               0.30, // 交易仓本金基准:30%(5X 下≈6% 价格)
			MaxHoldMinutes:              60,   // 快进快出:不久持,停滞即让位
			DailyLossLimitUSDT:          0,    // 不设日熔断(用户要求)
			CostFloorBps:                60,
			SymbolCooldownMinutes:       30,
			SpikeBarPct:                 0.03,
			TrendMaxExtensionPct:        0.02,
			VolExpansionMaxRatio:        1.8,
			ReversionKnifePct:           0.015,
			LossCooldownMultiplier:      3,
			PartialTPTriggerPct:         0.06,
			PartialTPFraction:           0.5,
			TrailGapPct:                 0.06,
			TrailLockFraction:           0.5,
			ScaleInTriggerPct:           0.15,
			MaxAdds:                     2,
			ScaleInFraction:             0.5,
			AddConvictionMin:            0.6,
			TPFadeConviction:            0.4,
			PeakGivebackFraction:        0.4,
			QuickTPPct:                  0.12,
			SignalWindowBars:            24,
			AbsMaxHoldMinutes:           180,
			BreakevenArmPct:                0.05,
			StaleCutMinutes:                40,
			StaleCutMinProfitPct:           0,
			TrendEntryPullbackMaxBarPct:    0.005,
			TrendSizeFraction:              0.5,
			MinOpenConviction:              0.65,
			CrossVenueCorroborationEnabled: true,
			CrossVenueConvictionStep:       0.1,
			CrossVenueVetoMinPeers:         2,
			CrossVenueDeadbandPct:          0.003,
			FillPriceMaxDevPct:             0.10,
			MaxOpenSpreadBps:               50,
		},
		FundingExecutor: VenueFundingExecutorConfig{
			LiveTradingEnabled:     false,
			TopN:                   1,
			MinFundingRate:         0.0005,
			MinNetProfitRate:       0.0005,
			LeadSeconds:            240,
			HoldAfterSeconds:       20,
			MaxConcurrentPositions: 1,
			MaxPositionUSDT:        30,
			RollEnabled:            true,
			MaxHoldSettlements:     12,
			EVGateEnabled:          true,
			SlippageBudgetBps:      20,
		},
		BybitExecutor: BybitExecutorConfig{
			// 主安全闸默认关闭:启动 bybit 任务也只 OFF(规划+日志,绝不下单),置 true 才真实下单。
			LiveTradingEnabled: false,
			// 最保守起步:单标的、正费率 ≥0.05%、成本后净利率 ≥0.05%、结算前 60s 开、结算后 20s 平、最多 1 个并发持仓。
			TopN:                    1,
			MinFundingRate:          0.0005,
			MinNetProfitRate:        0.0005,
			LeadSeconds:             60,
			HoldAfterSeconds:        20,
			MaxConcurrentPositions:  1,
			MaxPositionNotionalUSDT: 0,
			RollEnabled:             false,
			MaxHoldSettlements:      1,
			PerpLeverage:            1,
			EVGateEnabled:           false,
			PerpMakerFirst:          false,
			BorrowCostHourly:        0.00003,
		},
	}
	return cfg
}
