package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

// Venue 返回引擎的 venue.Venue 抽象(包住底层币安 client)。P0 接缝:供编排层/未来调用点
// 通过中性接口访问交易所;为兼容直接以结构体字面量构造的测试引擎,nil 时按需懒包装现有 client。
func (e *FundingRateSniperEngine) Venue() venue.Venue {
	if e == nil {
		return nil
	}
	if e.venueAdapter == nil && e.client != nil {
		e.venueAdapter = binance.New(e.client)
	}
	return e.venueAdapter
}

// binanceVenue 返回引擎持有的币安适配器具体类型(*binance.Adapter),供 gateway 中间件下沉
// 签名/发送(P1b)时复用同一实例,而非每次新建。nil-safe;旧测试若未注入适配器则按需懒构造。
func (e *FundingRateSniperEngine) binanceVenue() *binance.Adapter {
	if e == nil {
		return nil
	}
	if adapter, ok := e.venueAdapter.(*binance.Adapter); ok && adapter != nil {
		return adapter
	}
	// 兜底:仅当引擎未注入适配器(部分直建结构体的测试)时,按需构造一个无状态适配器返回。
	// **不**回写 e.venueAdapter —— 本方法处于并发签名请求热路径,回写共享字段会引入数据竞争。
	if e.client != nil {
		return binance.New(e.client)
	}
	return nil
}

func NewFundingRateSniperEngineWithConfig(secretPath string, cfg EngineConfig) (*FundingRateSniperEngine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret, err := LoadSecretConfig(secretPath)
	if err != nil {
		return nil, err
	}

	// Telegram 凭证(放在 secret.yaml)若齐备,则自动启用告警分发的 Telegram 出口。
	// 开/平/收益走 NotifyTrade(info,不受 MinSeverity 过滤),风控告警走 MinSeverity(默认 warning+)。
	if token, chatID := loadAlertingSecretsOrEmpty(secretPath); token != "" && chatID != "" {
		cfg.AlertingConfig.Enabled = true
		cfg.AlertingConfig.TelegramBotToken = token
		cfg.AlertingConfig.TelegramChatID = chatID
		if strings.TrimSpace(cfg.AlertingConfig.MinSeverity) == "" {
			cfg.AlertingConfig.MinSeverity = "warning"
		}
	}

	reportStore, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, err
	}
	client, err := NewAuthenticatedBinanceClient(secret, cfg.NetworkConfig)
	if err != nil {
		return nil, err
	}
	client.ApplyNetworkConfig(cfg.NetworkConfig)

	engine := &FundingRateSniperEngine{
		client:                  client,
		venueAdapter:            binance.New(client),
		cfg:                     cfg,
		econ:                    economicParamsFromConfig(cfg),
		logger:                  log.New(os.Stdout, "[FundingRateSniper] ", log.LstdFlags|log.Lmicroseconds),
		goldenSymbolSet:         make(map[string]struct{}),
		goldenSpotPairByFuture:  make(map[string]string),
		spotMeta:                make(map[string]SpotSymbol),
		spotRules:               make(map[string]SymbolTradeRules),
		futuresMeta:             make(map[string]FuturesSymbol),
		futuresRules:            make(map[string]SymbolTradeRules),
		fundingRateCapBySymbol:  make(map[string]fundingRateCapFloor),
		reportStore:             reportStore,
		domainEventSink:         reportStore,
		costModel:               newResidualCostModel(cfg.HistoricalConfig),
		emergencyStopFile:       effectiveEmergencyStopFile(cfg),
		globalEmergencyStopFile: strings.TrimSpace(cfg.NetworkConfig.GlobalEmergencyStopFile),
		openHedgeStatePath:      effectiveOpenHedgeStatePath(cfg),
		dailyRiskStatePath:      filepath.Join(cfg.NetworkConfig.RecordDir, liveDailyRiskStateName),
		dailyRiskPersistCh:      make(chan persistedDailyRiskState, 1),
		currentStage:            StageReady,
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
		futuresRealtimePositions:   make(map[string]futuresRealtimePosition),
		spotRealtimeBalances:       make(map[string]float64),
		realtimeRiskWakeCh:         make(chan struct{}, 1),
		symbolPanicFlattenInFlight: make(map[string]time.Time),
		fundingSettlementWakeCh:    make(chan struct{}, 1),
		futuresLeverageBrackets:    make(map[string][]futuresLeverageBracket),
		futuresAccountPositions:    make(map[string]float64),
		holdSpotTickerCache:        NewBookTickerCache(),
		holdFuturesTickerCache:     NewBookTickerCache(),
	}
	engine.exposureManager = newExposureManager(engine)
	engine.riskSupervisor = newRiskSupervisor(engine)
	engine.binanceGateway = newBinanceGateway(engine)
	engine.openProtocol = newOpenProtocol(engine)
	engine.closeProtocol = newCloseProtocol(engine)
	engine.recoveryProtocol = newRecoveryProtocol(engine)
	engine.runtimeReporter = newRuntimeReporter(engine)
	engine.costReporter = NewCostReporter(engine.logger, engine.reportStore)
	engine.alertDispatcher = newAlertDispatcher(cfg.AlertingConfig, engine.logger)
	if cfg.RiskConfig.EdgeWindowCycles > 0 {
		engine.edgeWindow = newRealizedEdgeWindow(cfg.RiskConfig.EdgeWindowCycles)
	}
	return engine, nil
}

// Start 启动完整的小时级主循环。
func (e *FundingRateSniperEngine) Start(ctx context.Context) (err error) {
	e.runCtx = ctx
	// S7-C:无论从哪个 return 退出(SIGTERM/Interrupt 取消、致命熔断),都执行一次有界、幂等的优雅停机收尾。
	// 注意 defer 注册顺序:此 defer 先注册 → 后执行(LIFO),因此 panic 恢复 defer(下行)会先把 err 置成崩溃
	// 错误,再由本 defer 据此发「崩溃」通知并安全收尾。
	defer func() { e.finalizeGracefulShutdown(err) }()
	// 把未预期的 panic 转成受控关停:记录堆栈、置为崩溃错误(交给 finalizeGracefulShutdown 发 Telegram 崩溃
	// 通知 + dead-man 兜底),不再裸 panic 让进程在无通知下消失。硬崩溃(OOM/kill -9)走不到这里,由 dead-man
	// (交易所侧)+ 外部看门狗兜底。
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			if e.logger != nil {
				e.logger.Printf("[panic] 主循环发生未捕获 panic: %v\n%s", r, stack)
			}
			err = fatalEngineError{message: fmt.Sprintf("主循环 panic: %v", r)}
		}
	}()
	e.startDailyRiskStateWriter(ctx)
	if err = e.initialize(ctx); err != nil {
		return err
	}
	if e.exposureManager != nil {
		e.exposureManager.Start(ctx)
	}
	e.startFundingSettlementWorker(ctx)
	e.startUserDataStreams(ctx)
	e.startRealtimeDrawdownMonitor(ctx)
	e.startHoldMonitor(ctx)
	e.startResidentMarketFeed(ctx)
	e.startMarginHealthPoller(ctx)
	e.startHeartbeatWriter(ctx)
	e.startDeadMansSwitch(ctx)
	e.backfillRealizedEdge()

	// 启动通知:子系统全部拉起、即将进入主循环 → 通知运营者(含 Telegram)。
	e.notifyLifecycle(EventLevelInfo, "实盘引擎已启动", e.lifecycleStartupDetail())

	for {
		if err := e.checkLiveSafetyGuards("idle"); err != nil {
			return err
		}
		if err := e.syncServerTimeOffset(ctx); err != nil {
			e.logger.Printf("刷新服务器时间偏移失败，将沿用旧偏移继续调度: %v", err)
		}

		// P2.4 统一逐结算唤醒:取「新开扫描(最近未来结算 − ScanWindow)」与「各持仓续持复核(:55)」最早者,
		// 不再绑全局 :wakeMinute。冷启动(无已知结算)退化为立即扫描引导;滚动/单周期路径同一唤醒口径,
		// 仅 cycle 编排仍按 EnableRollingHold 分叉。
		sched := e.scheduler()
		wakeNow := e.serverNow()
		residentClocks := e.residentClocks()
		wakeAt := sched.NextMarketWake(wakeNow, residentClocks, e.knownMarketNextFundings())
		// 防御:若算出「立即唤醒」但**并非**因持仓到了续持复核窗口(即纯因冷启动/上轮扫描失败/结算边界刚过、
		// 结算时钟尚不可用),施加最小重试间隔。否则持续扫描失败会空转打满公共行情接口、反招平台熔断——
		// 这正是旧 :57 模型(失败自然退避到下个整点)隐含的兜底,逐结算模型必须显式补回。
		if !wakeAt.After(wakeNow) && !anyRevalidationDue(sched, wakeNow, residentClocks) {
			wakeAt = wakeNow.Add(scanBootstrapRetryInterval)
		}
		e.logger.Printf("系统静默中，等待下一次觉醒时间: %s", wakeAt.Format("2006-01-02 15:04:05"))
		if err := e.sleepUntilServerTime(ctx, wakeAt); err != nil {
			return err
		}

		if err := e.waitPlatformCooldown(ctx); err != nil {
			return err
		}
		if err := e.checkLiveSafetyGuards("before_cycle"); err != nil {
			return err
		}
		if blocked, reason := e.feedBlindnessBlockingOpens(); blocked {
			e.logger.Printf("断连盲窗守卫: 暂停本轮开仓，等待行情流恢复或保守撤离 (%s)", reason)
			continue
		}
		var cycleErr error
		if e.cfg.PortfolioConfig.EnableRollingHold {
			cycleErr = e.runRollingPortfolioCycle(ctx, wakeAt)
		} else {
			cycleErr = e.runOneCycle(ctx, wakeAt)
		}
		if err := cycleErr; err != nil {
			var fatalErr fatalEngineError
			if errors.As(err, &fatalErr) {
				return err
			}
			var platformErr platformFuseError
			if errors.As(err, &platformErr) {
				e.logger.Printf("本轮触发平台异常熔断: %v", err)
				continue
			}
			e.logger.Printf("本轮狙击失败: %v", err)
		}
	}
}

// initialize 完成黄金币种列表和精度元数据的初始化。
func (e *FundingRateSniperEngine) initialize(ctx context.Context) error {
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return err
	}

	spotSymbols, futuresSymbols, err := e.client.FetchExchangeInfos(ctx)
	if err != nil {
		return err
	}

	e.goldenSymbols, e.goldenSpotPairByFuture = intersectGoldenSymbols(spotSymbols, futuresSymbols, e.livePreferredSpotQuoteAssets())
	e.goldenSymbolSet = make(map[string]struct{}, len(e.goldenSymbols))
	e.spotMeta = make(map[string]SpotSymbol, len(spotSymbols))
	e.spotRules = make(map[string]SymbolTradeRules, len(spotSymbols))
	for _, symbol := range spotSymbols {
		e.spotMeta[symbol.Symbol] = symbol
		e.spotRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}
	for _, symbol := range e.goldenSymbols {
		e.goldenSymbolSet[symbol] = struct{}{}
	}

	e.futuresMeta = make(map[string]FuturesSymbol, len(futuresSymbols))
	e.futuresRules = make(map[string]SymbolTradeRules, len(futuresSymbols))
	for _, symbol := range futuresSymbols {
		e.futuresMeta[symbol.Symbol] = symbol
		e.futuresRules[symbol.Symbol] = extractTradeRules(symbol.Filters)
	}

	// 撞限豁免开启时才需要逐 symbol 的资金费上下限;拉取失败不致命,回退默认 ±2%(仅记日志)。
	if e.cfg.StrategyConfig.CapPinnedFundingOverrideEnabled {
		if fundingInfos, fundingErr := e.client.FetchFundingInfo(ctx); fundingErr != nil {
			e.logger.Printf("拉取 fundingInfo 失败(撞限豁免将回退默认 ±%.2f%% 上下限): %v", defaultFundingRateCapAbs*100, fundingErr)
		} else {
			e.fundingRateCapBySymbol = loadFundingRateCaps(fundingInfos)
			e.logger.Printf("已加载 fundingInfo: %d 个 symbol 设有特殊资金费上下限(撞限豁免已启用)", len(e.fundingRateCapBySymbol))
		}
	}
	spotPairs := make([]string, 0, len(e.goldenSymbols))
	borrowAssets := make([]string, 0, len(e.goldenSymbols))
	for _, futureSymbol := range e.goldenSymbols {
		if spotPair := strings.ToUpper(strings.TrimSpace(e.goldenSpotPairByFuture[futureSymbol])); spotPair != "" {
			spotPairs = append(spotPairs, spotPair)
			if meta, ok := e.spotMeta[spotPair]; ok && strings.TrimSpace(meta.BaseAsset) != "" {
				borrowAssets = append(borrowAssets, meta.BaseAsset)
			}
		}
	}
	if profile, profileErr := e.client.ResolveAccountTradingCostProfile(ctx, spotPairs, e.goldenSymbols, borrowAssets); profileErr != nil {
		if profile.SpotTakerFeeRate > 0 {
			e.cfg.HistoricalConfig.SpotTakerFeeRate = profile.SpotTakerFeeRate
		}
		if profile.FuturesTakerFeeRate > 0 {
			e.cfg.HistoricalConfig.FuturesTakerFeeRate = profile.FuturesTakerFeeRate
		}
		if len(profile.BorrowHourlyRateByAsset) > 0 {
			e.borrowHourlyInterestByAsset = profile.BorrowHourlyRateByAsset
		}
		e.logger.Printf("加载账户真实交易成本存在部分失败，已尽量使用成功结果并对其余项回退: %v", profileErr)
		e.logger.Printf("当前交易成本参数: spot_taker=%.6f(symbol=%s) futures_taker=%.6f(symbol=%s) borrow_assets=%d",
			e.cfg.HistoricalConfig.SpotTakerFeeRate, profile.SpotFeeSymbol, e.cfg.HistoricalConfig.FuturesTakerFeeRate, profile.FuturesFeeSymbol, len(e.borrowHourlyInterestByAsset))
	} else {
		if profile.SpotTakerFeeRate > 0 {
			e.cfg.HistoricalConfig.SpotTakerFeeRate = profile.SpotTakerFeeRate
		}
		if profile.FuturesTakerFeeRate > 0 {
			e.cfg.HistoricalConfig.FuturesTakerFeeRate = profile.FuturesTakerFeeRate
		}
		e.borrowHourlyInterestByAsset = profile.BorrowHourlyRateByAsset
		e.logger.Printf("已加载账户真实交易成本: spot_taker=%.6f(symbol=%s) futures_taker=%.6f(symbol=%s) borrow_assets=%d",
			e.cfg.HistoricalConfig.SpotTakerFeeRate, profile.SpotFeeSymbol, e.cfg.HistoricalConfig.FuturesTakerFeeRate, profile.FuturesFeeSymbol, profile.BorrowInterestResolvedCount)
	}

	// P2.2:用刚实拉的真实 taker 费率派生 FeeReserveRate(4 腿往返),刷新该所经济闸 e.econ。
	// 必须在 ResolveAccountTradingCostProfile 写回 HistoricalConfig taker 费率之后调用。
	e.applyDerivedEconomicParams()

	if err := e.verifyTradingEnvironment(ctx); err != nil {
		return err
	}
	if err := e.verifySpotBNBBurnEnabled(ctx); err != nil {
		return err
	}
	if err := e.loadDailyRiskState(); err != nil {
		return err
	}
	if err := e.guardExposureRescueEnvelopeOnStartup(); err != nil {
		return err
	}
	if err := e.checkLiveSafetyGuards("initialize"); err != nil {
		return err
	}
	e.reportJournalRecoveryNotes()

	e.logger.Printf("模块一已加载完成，黄金币种数量: %d", len(e.goldenSymbols))
	return nil
}

// reportJournalRecoveryNotes 在启动恢复后排空 ReportStore 的事实源重放证据:若物化视图曾与 append-only 事实源
// 不一致并被自动重放修复(典型为崩溃丢失最近一次结算/pending 写),记日志并以必达通知升级告警,提示人工复核。
func (e *FundingRateSniperEngine) reportJournalRecoveryNotes() {
	if e == nil || e.reportStore == nil {
		return
	}
	notes := e.reportStore.DrainRecoveryNotes()
	if len(notes) == 0 {
		return
	}
	for _, note := range notes {
		if e.logger != nil {
			e.logger.Printf("[recovery] 事实源重放: %s", note)
		}
	}
	e.notifyLifecycle(EventLevelWarning, "启动检测到崩溃恢复:已从事实源重放修复落盘视图", strings.Join(notes, "\n"))
}

func (e *FundingRateSniperEngine) livePreferredSpotQuoteAssets() []string {
	asset := strings.ToUpper(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset))
	if asset == "" {
		return preferredSpotQuoteAssets(e.cfg.StrategyConfig.PreferredSpotQuoteAssets)
	}
	return []string{asset}
}

// openCyclePositions 执行「扫描→监控→开仓」段(模块二~四),产出本轮成功建仓的对冲与开仓前账户快照。
// opened=false 表示本轮无可开仓(无满足目标 / 全被风控作废 / 无成功建仓),各分支已完成相应生命周期迁移与上报副作用,
// 调用方直接结束本轮即可。供单周期(runOneCycle,随后进平仓监控)与滚动新开(openResidentPositions,开后保留为常驻)复用。
// openCyclePositions 串起「扫描→监控→开仓」段并返回成功建仓的对冲。selectTargets(可空)是一道开仓前的目标筛选
// 钩子:单周期(runOneCycle)传 nil 表示开全部有效目标;滚动持有补位(openResidentPositions)传 rebalance 选择器,
// 在 L3 币数/名义上限内择优填空槽。钩子返回空集等价「本轮无可开目标」,与监控全废口径一致地优雅收尾。
func (e *FundingRateSniperEngine) openCyclePositions(ctx context.Context, wakeAt time.Time, selectTargets func([]*MonitoredTarget) []*MonitoredTarget) ([]OpenHedge, CycleAccountSnapshot, bool, error) {
	cycleID := newCycleID(e.runID)
	e.setCurrentCycleID(cycleID)
	// 同步日志旁路用的 trace id 副本（在释放 platformRiskMu 之后），后续状态迁移日志才能带上本轮 cycle id。
	e.setTraceLogIDs(e.runID, cycleID)
	e.resetLifecycleTracking(LifecycleStageScanning, "开始扫描候选标的")
	e.resetCyclePlatformRiskCounters()
	e.runtimeReporter.EmitWithReason(StageScan, 0, "开始扫描候选标的")
	e.logger.Printf("模块二启动，觉醒时间: %s", wakeAt.Format("15:04:05"))
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return nil, CycleAccountSnapshot{}, false, err
	}

	// 周期决策漏斗：进度式填充，在本函数所有"决策性"返回路径(已过扫描)通过 defer 汇总成一条可读日志，
	// 回答"本轮为什么没开仓 / 把入场闸调到多少能纳入近失候选"。纯旁路，不影响开仓判定。
	funnel := cycleDecisionFunnel{CycleID: cycleID}
	emitFunnel := false
	defer func() {
		if emitFunnel {
			e.logCycleDecisionFunnel(funnel)
		}
	}()

	targets, scanSummary, err := e.scanTopFundingTargets(ctx)
	if err != nil {
		return nil, CycleAccountSnapshot{}, false, err
	}
	funnel.Scan = scanSummary
	emitFunnel = true
	targets = e.filterTargetsBySymbolCooldown(targets)
	funnel.AfterCooldown = len(targets)
	if len(targets) == 0 {
		e.setCurrentMonitorTargets(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "扫描结束，没有满足条件的目标")
		e.runtimeReporter.EmitWithReason(StageScanEmpty, 0, "扫描结束，没有满足条件的目标")
		e.logger.Println("模块二扫描结束，本轮没有满足条件的目标")
		return nil, CycleAccountSnapshot{}, false, nil
	}

	e.setCurrentMonitorTargets(targets)
	_ = e.advanceLifecycleStage(LifecycleStageMonitoring, fmt.Sprintf("进入监控阶段，目标数=%d", len(targets)))
	e.runtimeReporter.EmitWithReason(StageMonitor, len(targets), fmt.Sprintf("进入监控阶段，目标数=%d", len(targets)))
	monitored := e.monitorTargets(ctx, targets)
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return nil, CycleAccountSnapshot{}, false, fuseErr
	}
	validTargets := filterValidTargets(monitored)
	funnel.MonitorValid = len(validTargets)
	funnel.InvalidReasons = tallyInvalidReasons(monitored)
	if len(validTargets) == 0 {
		e.setCurrentMonitorTargets(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "监控结束，所有目标都已被风控作废")
		e.runtimeReporter.EmitWithReason(StageMonitorInvalidated, 0, "监控结束，所有目标都已被风控作废")
		e.logger.Println("模块三结束，所有目标都已被风控作废")
		return nil, CycleAccountSnapshot{}, false, nil
	}

	// R29 借币容量/利率守卫:剔除借不到 / 利率过高的负向 carry 候选(此刻 OrderQuantity 已定,估算精确)。
	// 守卫关闭(默认)时原样返回。剔除=根本不开,天然无裸腿。
	validTargets = e.filterTargetsByBorrowCapacity(ctx, validTargets)
	funnel.BorrowAdmitted = len(validTargets)
	if len(validTargets) == 0 {
		e.setCurrentMonitorTargets(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "借币容量守卫：所有候选均借不到 / 利率过高")
		e.runtimeReporter.EmitWithReason(StageMonitorInvalidated, 0, "借币容量守卫：所有候选均借不到 / 利率过高")
		e.logger.Println("R29 借币容量守卫：本轮所有负向 carry 候选均被剔除")
		return nil, CycleAccountSnapshot{}, false, nil
	}

	if selectTargets != nil {
		validTargets = selectTargets(validTargets)
		if len(validTargets) == 0 {
			e.setCurrentMonitorTargets(nil)
			_ = e.advanceLifecycleStage(LifecycleStageCompleted, "滚动持有：L3 补位无可纳入空槽的候选")
			e.runtimeReporter.EmitWithReason(StageMonitorInvalidated, 0, "滚动持有：L3 补位无可纳入空槽的候选")
			e.logger.Println("模块四收尾：滚动持有补位无可纳入 L3 空槽的候选")
			return nil, CycleAccountSnapshot{}, false, nil
		}
	}

	funnel.Selected = len(validTargets)
	e.setCurrentMonitorTargets(validTargets)
	e.runtimeReporter.EmitWithReason(StageOpen, len(validTargets), fmt.Sprintf("进入开仓阶段，目标数=%d", len(validTargets)))
	openProtocol := e.ensureOpenProtocol()
	if openProtocol == nil {
		return nil, CycleAccountSnapshot{}, false, fmt.Errorf("open protocol 未初始化")
	}
	preparedResult, err := openProtocol.PrepareSequence(ctx, validTargets)
	if err != nil {
		return nil, CycleAccountSnapshot{}, false, err
	}
	if err := e.applyLifecycleProtocolResult(preparedResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(open.prepare): %v", err)
	} else {
		e.runtimeReporter.Emit(EngineStage(""))
	}
	e.emitDomainEvents(preparedResult.ProtocolResult.Events)
	executeOpenResult, err := openProtocol.ExecuteSequence(ctx, validTargets, preparedResult.Preparation)
	if err != nil {
		return nil, CycleAccountSnapshot{}, false, err
	}
	if err := e.applyLifecycleProtocolResult(executeOpenResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(open.execute): %v", err)
	} else {
		e.runtimeReporter.Emit(EngineStage(""))
	}
	e.emitDomainEvents(executeOpenResult.ProtocolResult.Events)
	openHedges, beforeSnapshot := executeOpenResult.Hedges, executeOpenResult.BeforeSnapshot
	funnel.Opened = len(openHedges)
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return nil, CycleAccountSnapshot{}, false, fuseErr
	}
	if len(openHedges) == 0 {
		e.setCurrentPositions(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "开仓结束，没有成功建仓的目标")
		e.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, "开仓结束，没有成功建仓的目标")
		e.logger.Println("模块四结束，没有成功建仓的目标")
		return nil, CycleAccountSnapshot{}, false, nil
	}
	if symbols, notional := summarizeOpenHedges(openHedges); symbols != "" {
		e.notifyTrade("开仓成功", fmt.Sprintf("建仓 %d 个对冲: %s | 现货名义合计≈%.2f USDT", len(openHedges), symbols, notional))
	}
	return openHedges, beforeSnapshot, true, nil
}

// runOneCycle 串起模块二到模块五(单周期:开 N → 当轮全平 N)。
func (e *FundingRateSniperEngine) runOneCycle(ctx context.Context, wakeAt time.Time) error {
	openHedges, beforeSnapshot, opened, err := e.openCyclePositions(ctx, wakeAt, nil)
	if err != nil {
		return err
	}
	if !opened {
		return nil
	}

	_ = e.advanceLifecycleStage(LifecycleStageCloseMonitoring, fmt.Sprintf("进入平仓监控阶段，持仓数=%d", len(openHedges)))
	e.runtimeReporter.EmitWithReason(StageCloseMonitor, len(openHedges), fmt.Sprintf("进入平仓监控阶段，持仓数=%d", len(openHedges)))
	closeProtocol := e.ensureCloseProtocol()
	if closeProtocol == nil {
		return fmt.Errorf("close protocol 未初始化")
	}
	closeResult, err := closeProtocol.ExecuteSequence(ctx, openHedges, beforeSnapshot, nil, nil)
	if err != nil {
		return err
	}
	if err := e.applyLifecycleProtocolResult(closeResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(close.execute): %v", err)
	} else {
		e.runtimeReporter.Emit(EngineStage(""))
	}
	e.emitDomainEvents(closeResult.ProtocolResult.Events)
	settlement := closeResult.Settlement

	e.logger.Printf(
		"模块五完成，目标数=%d 资金费收入=%.6f 理论资金费=%.6f 资金费偏差=%.6f 现货交易PnL=%.6f 合约交易PnL=%.6f 总手续费=%.6f 精确净利润=%.6f 账户净变动=%.6f 结算时间=%s",
		settlement.Targets,
		settlement.FundingIncome,
		settlement.ExpectedFundingIncome,
		settlement.FundingIncomeDiff,
		settlement.SpotTradingPnL,
		settlement.FuturesTradingPnL,
		settlement.TotalFees,
		settlement.NetPnL,
		settlement.SnapshotNetPnL,
		settlement.CloseExecutedAt.Format("15:04:05"),
	)
	if settlement.HasAlert {
		e.logger.Printf("模块五告警: %s", strings.Join(settlement.Alerts, " | "))
	}
	e.logSettlementTradeDetails(settlement)
	e.notifyCloseSettlement(settlement)
	if e.reportStore != nil {
		if err := e.reportStore.AppendSettlement(settlement); err != nil {
			e.logger.Printf("模块五 JSON 落盘失败: %v", err)
		}
	}
	if err := e.noteDailySettlement(settlement.NetPnL); err != nil {
		return err
	}
	e.scheduleAsyncFundingSettlement(settlement, openHedges)
	if err := e.checkCycleLossProjectionBrake(settlement); err != nil {
		return err
	}
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	e.setCurrentMonitorTargets(nil)
	e.setCurrentPositions(nil)
	_ = e.advanceLifecycleStage(LifecycleStageCompleted, "本轮狙击周期已完成")
	e.runtimeReporter.EmitWithReason(StageCompleted, 0, "本轮狙击周期已完成")

	return nil
}

// notifyCloseSettlement 推送平仓通知;若资金费已实收(非 pending),顺带推送本周期收益(最终净利)。
// 资金费仍 pending 时,真实最终净利由结算 worker 在到账后经 notifyCycleProfit 推送,避免重复/过早通知。
func (e *FundingRateSniperEngine) notifyCloseSettlement(settlement CycleSettlement) {
	e.notifyTrade("平仓完成", fmt.Sprintf(
		"平仓 %d 个目标 | 资金费收入=%.4f 现货PnL=%.4f 合约PnL=%.4f 手续费=%.4f | 即时净利=%.4f USDT%s",
		settlement.Targets, settlement.FundingIncome, settlement.SpotTradingPnL, settlement.FuturesTradingPnL,
		settlement.TotalFees, settlement.NetPnL,
		map[bool]string{true: "（资金费待到账，最终收益到账后另行通知）", false: ""}[settlement.FundingIncomePending],
	))
	if !settlement.FundingIncomePending {
		e.notifyCycleProfit(settlement.NetPnL, settlement.CycleNotionalUSDT)
	}
}

// notifyCycleProfit 推送本周期最终收益(资金费实收、真实净利已确定后)。盈亏用前缀区分,便于一眼看出。
func (e *FundingRateSniperEngine) notifyCycleProfit(netPnL, notionalUSDT float64) {
	tag := "盈利"
	if netPnL < 0 {
		tag = "亏损"
	}
	detail := fmt.Sprintf("本周期最终净利=%.4f USDT", netPnL)
	if notionalUSDT > 0 {
		detail += fmt.Sprintf(" | 名义=%.2f USDT | 净edge=%.4f%%", notionalUSDT, netPnL/notionalUSDT*100)
	}
	e.notifyTrade("周期收益结算（"+tag+"）", detail)
}

// notifyTrade 推送一条交易生命周期通知(开仓/平仓/收益)到告警出口(含 Telegram)。
// 走 NotifyTrade:info 级,不受 MinSeverity 过滤、不做去重——这是用户主动要的「每笔都通知」。nil-safe。
func (e *FundingRateSniperEngine) notifyTrade(title, detail string) {
	if e == nil || e.alertDispatcher == nil {
		return
	}
	e.alertDispatcher.NotifyTrade(Alert{
		Level:      EventLevelInfo,
		Title:      title,
		Detail:     detail,
		OccurredAt: e.serverNow(),
	})
}

// notifyLifecycle 推送进程生命周期通知(启动/停止/崩溃)到告警出口(含 Telegram)。
// 走 NotifyTrade:不受 MinSeverity 过滤、不做去重——这是用户主动要的「启停崩都通知我」,确保必达。nil-safe。
func (e *FundingRateSniperEngine) notifyLifecycle(level EventLevel, title, detail string) {
	if e == nil || e.alertDispatcher == nil {
		return
	}
	e.alertDispatcher.NotifyTrade(Alert{
		Level:      level,
		Title:      title,
		Detail:     detail,
		OccurredAt: e.serverNow(),
	})
}

// lifecycleStartupDetail 组装一条启动通知文案(版本/模式/档位/滚动持有/恢复的活跃对冲数)。
func (e *FundingRateSniperEngine) lifecycleStartupDetail() string {
	mode := "实盘"
	if e.cfg.ExecutionConfig.ShadowTrading {
		mode = "影子(只读冒烟)"
	}
	return fmt.Sprintf("模式=%s 档位=%s 滚动持有=%t 恢复活跃对冲=%d 版本=%s",
		mode, e.cfg.ActiveProfile, e.cfg.PortfolioConfig.EnableRollingHold,
		len(e.snapshotActiveOpenHedges()), buildVersionSummary())
}

// summarizeOpenHedges 汇总一组刚建仓对冲的 symbol 列表与现货名义合计,供开仓通知文案使用。
func summarizeOpenHedges(hedges []OpenHedge) (symbols string, notionalUSDT float64) {
	names := make([]string, 0, len(hedges))
	for i := range hedges {
		if s := strings.ToUpper(strings.TrimSpace(hedges[i].Target.Symbol)); s != "" {
			names = append(names, s)
		}
		notionalUSDT += hedges[i].OpenSpotQuoteQty
	}
	return strings.Join(names, ", "), notionalUSDT
}

// projectedCycleNetPnL 返回单周期净亏熔断「早刹车」使用的投影最终净利。
// funding 尚未实收（FundingIncomePending）时，平仓即时的 NetPnL 不含资金费，叠加预期资金费得到对最终净利的
// 保守投影，避免把「资金费还没到账」误判成单周期巨亏。funding 已实收时直接用 NetPnL（即真实最终净利）。
func projectedCycleNetPnL(settlement CycleSettlement) float64 {
	if settlement.FundingIncomePending {
		return money.Sum(settlement.NetPnL, settlement.ExpectedFundingIncome).USDT()
	}
	return settlement.NetPnL
}

// checkCycleLossProjectionBrake 是单周期净亏熔断的「投影早刹车」(R8 第一道):平仓这一刻资金费尚未实收
// (FundingIncomePending),settlement.NetPnL 不含资金费,直接判亏会把「资金费还没到账」误判成单周期巨亏而误停机;
// 改用「投影最终净利 = 已落袋净利 + 预期资金费」做保守早刹车(同步永久停机,挡住开/平腿真实巨亏,如剧烈滑点)。
// funding 实收后的真实最终净利再由 noteCycleFinalSettlement 做权威复核(R8 第二道)。单周期与滚动 drop 平仓共用。
func (e *FundingRateSniperEngine) checkCycleLossProjectionBrake(settlement CycleSettlement) error {
	limit := e.cfg.RiskConfig.MaxCycleLossUSDT
	if limit <= 0 {
		return nil
	}
	projectedNetPnL := projectedCycleNetPnL(settlement)
	if projectedNetPnL > -limit {
		return nil
	}
	detail := fmt.Sprintf("单周期投影净亏损 %.6f USDT（已落袋 %.6f + 预期资金费 %.6f）已超过阈值 %.6f USDT", -projectedNetPnL, settlement.NetPnL, settlement.ExpectedFundingIncome, limit)
	if incidentErr := e.recordExecutionIncident("cycle_loss_limit", "-", detail); incidentErr != nil {
		return incidentErr
	}
	return fatalEngineError{message: fmt.Sprintf("单周期投影净亏损达到熔断阈值: loss=%.6f limit=%.6f", -projectedNetPnL, limit)}
}

// scanTopFundingTargets 在 XX:57:00 后扫描全网费率，并取 Top 5。
func (e *FundingRateSniperEngine) scanTopFundingTargets(ctx context.Context) ([]*MonitoredTarget, scanFunnelSummary, error) {
	// best-effort 刷新可借白名单(长 TTL,fail-open):供下方在 Top-N 截断前主动排除不在白名单的负 carry。
	e.ensureBorrowableAllowlistFresh(ctx)

	items, err := e.fetchPremiumIndex(ctx)
	if err != nil {
		return nil, scanFunnelSummary{}, err
	}

	now := e.serverNow()
	sched := e.scheduler()
	// P2.4:记录本轮黄金币种各自的下次结算时间(无论是否落在扫描窗口),供下一轮逐结算唤醒计算。
	fundingClock := make([]time.Time, 0, len(items))
	candidates := make([]FundingCandidate, 0, len(items))
	observations := make([]ScanObservationRecord, 0, len(items))
	econScan := e.economicParams()
	breakEven := econScan.BreakEven()
	for _, item := range items {
		if _, ok := e.goldenSymbolSet[item.Symbol]; !ok {
			continue
		}

		nextFundingTime := time.UnixMilli(item.NextFundingTime)
		if item.NextFundingTime > 0 {
			fundingClock = append(fundingClock, nextFundingTime)
		}
		if !sched.ScanEligible(now, nextFundingTime) {
			continue
		}

		staleness := premiumIndexStaleness(now, item)
		isStale := e.cfg.NetworkConfig.MaxPremiumIndexStaleness > 0 && staleness > e.cfg.NetworkConfig.MaxPremiumIndexStaleness

		estimatedRate, lastFundingRate, fundingJump, basisRate, _ := estimateScanFundingSignal(item, e.liveFundingSignalConfig())
		var capPinnedOverride bool
		estimatedRate, fundingJump, capPinnedOverride = e.applyCapPinnedFundingOverride(item.Symbol, lastFundingRate, estimatedRate, fundingJump)
		direction := fundingDirectionFromRate(estimatedRate)
		profile := GetSymbolRiskProfile(item.Symbol)

		strategy := NewDefaultFundingStrategy()
		action, reason := strategy.EvaluateScanCandidate(
			FundingCandidate{Symbol: item.Symbol},
			FundingSnapshot{
				Symbol:          item.Symbol,
				NextFundingTime: nextFundingTime,
				EstimatedRate:   estimatedRate,
				LastFundingRate: lastFundingRate,
				BasisRate:       basisRate,
				FundingJump:     fundingJump,
				Direction:       direction,
				IsStale:         isStale,
			},
			profile,
			e.cfg,
		)

		// 逐标的评估观测（通过/被拒都记），用于事后量化机会供给与近失幅度（estimated_rate 与 min_rate_gate 的差距）。
		minRateGate := maxFloat(e.cfg.StrategyConfig.MinEstimatedFundingRate, breakEven) * profile.MinEstimatedRateMultiplier
		observations = append(observations, newScanObservationRecord(
			e.runID, e.cycleIDSnapshot(), now, item.Symbol, nextFundingTime, direction,
			estimatedRate, lastFundingRate, fundingJump, basisRate,
			breakEven, minRateGate, econScan.MaxEstimatedRate,
			isStale, action != StrategyActionReject, reason, capPinnedOverride,
		))

		if action == StrategyActionReject {
			continue
		}

		meta, ok := e.futuresMeta[item.Symbol]
		if !ok {
			continue
		}
		spotPair, ok := e.goldenSpotPairByFuture[item.Symbol]
		if !ok {
			continue
		}
		spotMeta, ok := e.spotMeta[spotPair]
		if !ok {
			continue
		}

		candidates = append(candidates, FundingCandidate{
			Symbol:            item.Symbol,
			Pair:              spotPair,
			RiskTier:          profile.Tier,
			Direction:         direction,
			EstimatedRate:     estimatedRate,
			EstimatedRateText: formatFloat(estimatedRate),
			LastFundingRate:   lastFundingRate,
			FundingRateJump:   fundingJump,
			BasisRate:         basisRate,
			ScanScoreRate:     liveFundingPriorityScore(e.cfg.StrategyConfig.LiveFundingDirectionMode, estimatedRate, basisRate),
			NextFundingTime:   nextFundingTime,
			MarkPrice:         parseFloat(item.MarkPrice),
			SpotBaseAsset:     spotMeta.BaseAsset,
			SpotBasePrecision: spotMeta.BaseAssetPrecision,
			QuantityPrecision: meta.QuantityPrecision,
		})
	}

	e.setMarketNextFundings(fundingClock)

	for idx := range candidates {
		futuresRule := e.futuresRules[candidates[idx].Symbol]
		profile := GetSymbolRiskProfile(candidates[idx].Symbol)
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / candidates[idx].MarkPrice
		candidates[idx].OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, candidates[idx].QuantityPrecision)
	}

	// A 方案:在 Top-N 截断之前剔除「base 资产近期确定性借不到」的负 carry 候选,让可借的负 carry /
	// 免借币的正 carry 顶上空槽,而不是被 HOME 这类长尾币霸占唯一名额、监控完必被 R29 剔除而本轮 0 开。
	if filtered, dropped := e.dropBorrowUnavailableNegativeCarry(candidates); dropped > 0 {
		candidates = filtered
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ScanScoreRate > candidates[j].ScanScoreRate
	})
	if len(candidates) > e.cfg.StrategyConfig.MaxTargets {
		candidates = candidates[:e.cfg.StrategyConfig.MaxTargets]
	}

	targets := make([]*MonitoredTarget, 0, len(candidates))
	for _, candidate := range candidates {
		targets = append(targets, &MonitoredTarget{
			TraceRecordMetadata: e.recordMetadata(),
			RunID:               e.runID,
			CycleID:             e.cycleIDSnapshot(),
			Candidate:           candidate,
			IsValid:             1,
			MonitorState:        "watching",
		})
	}

	e.flushScanObservations(observations)
	scanSummary := summarizeScanNearMiss(observations)
	e.logger.Printf("模块二完成，候选目标数量: %d", len(targets))
	for _, target := range targets {
		e.logger.Printf(
			"Top 目标: %s tier=%s 预估费率=%.6f 上期费率=%.6f 跳变=%.6f 基差=%.6f 扫描得分=%.6f 下次结算=%s",
			target.Candidate.Symbol,
			target.Candidate.RiskTier,
			target.Candidate.EstimatedRate,
			target.Candidate.LastFundingRate,
			target.Candidate.FundingRateJump,
			target.Candidate.BasisRate,
			target.Candidate.ScanScoreRate,
			target.Candidate.NextFundingTime.Format("15:04:05"),
		)
	}

	return targets, scanSummary, nil
}

// monitorTargets 启动模块三的 Top 5 高频死守监控。
func (e *FundingRateSniperEngine) monitorTargets(ctx context.Context, targets []*MonitoredTarget) []*MonitoredTarget {
	stopAt := e.openPreparationStartAt(targets)
	monitorCtx, cancel := context.WithDeadline(ctx, stopAt)
	defer cancel()

	e.StartBookTickerStreams(monitorCtx, targets)
	e.warmupMarketCaches(monitorCtx, targets)

	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(t *MonitoredTarget) {
			defer wg.Done()
			e.monitorWorker(monitorCtx, t)
		}(target)
	}

	wg.Wait()
	e.logMonitorSummary(targets)
	if e.reportStore != nil {
		if err := e.reportStore.AppendMonitorSnapshot(stopAt, targets); err != nil {
			e.logger.Printf("模块三 JSON 落盘失败: %v", err)
		}
	}
	e.logger.Printf("模块三完成，监控截止时间: %s", stopAt.Format("15:04:05"))
	return targets
}

// scheduler 返回由当前配置派生的周期调度器(纯时点计算,无状态)。时点口径全部委托给它(M-S2.2)。
func (e *FundingRateSniperEngine) scheduler() cycleScheduler {
	return newCycleScheduler(e.cfg)
}

func (e *FundingRateSniperEngine) openFireAt(targets []*MonitoredTarget) time.Time {
	if len(targets) == 0 {
		return time.Time{}
	}
	return e.scheduler().OpenFireAt(earliestFundingTime(targets))
}

func (e *FundingRateSniperEngine) openPreparationStartAt(targets []*MonitoredTarget) time.Time {
	if len(targets) == 0 {
		return time.Time{}
	}
	return e.scheduler().OpenPreparationStartAt(earliestFundingTime(targets))
}

func (e *FundingRateSniperEngine) openNoGoCheckAt(targets []*MonitoredTarget) time.Time {
	if len(targets) == 0 {
		return time.Time{}
	}
	return e.scheduler().OpenNoGoCheckAt(earliestFundingTime(targets))
}

func maxDurationValue(a time.Duration, b time.Duration) time.Duration {
	if a >= b {
		return a
	}
	return b
}

func minDurationValue(a time.Duration, b time.Duration) time.Duration {
	if a <= b {
		return a
	}
	return b
}

func (e *FundingRateSniperEngine) setBookTickerCacheOnlyUntil(until time.Time) {
	if until.IsZero() {
		atomic.StoreInt64(&e.cacheOnlyBookTickerUntil, 0)
		return
	}
	atomic.StoreInt64(&e.cacheOnlyBookTickerUntil, until.UnixNano())
}

func (e *FundingRateSniperEngine) bookTickerCacheOnlyModeEnabled() bool {
	until := atomic.LoadInt64(&e.cacheOnlyBookTickerUntil)
	return until > 0 && time.Now().UTC().UnixNano() < until
}

func (e *FundingRateSniperEngine) warmupMarketCaches(ctx context.Context, targets []*MonitoredTarget) {
	if len(targets) == 0 {
		return
	}

	spotSymbols := make([]string, 0, len(targets))
	futuresSymbols := make([]string, 0, len(targets))
	for _, target := range targets {
		spotSymbols = append(spotSymbols, target.Candidate.Pair)
		futuresSymbols = append(futuresSymbols, target.Candidate.Symbol)
	}

	// 预热预算不超过监控窗口剩余时间的一半:监控窗口被压缩时(扫描晚启动、紧贴 :59:45 截止),
	// 若预热吃满整个窗口,随后的补齐取价与首轮成本检查会拿到一个已过期的 ctx,REST 全部瞬间
	// "context deadline exceeded"(07:59:45 五标的同毫秒报错实锤),监控整轮空转。
	warmupBudget := e.cfg.StreamWarmupTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline) / 2; remain > 0 && (warmupBudget <= 0 || remain < warmupBudget) {
			warmupBudget = remain
		}
	}
	waitCtx := ctx
	var cancel context.CancelFunc
	if warmupBudget > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, warmupBudget)
		defer cancel()
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		spotTickerMissing, futuresTickerMissing, spotDepthMissing, futuresDepthMissing := e.marketCacheMissingSymbols(spotSymbols, futuresSymbols)
		if len(spotTickerMissing) == 0 && len(futuresTickerMissing) == 0 && len(spotDepthMissing) == 0 && len(futuresDepthMissing) == 0 {
			e.logger.Printf(
				"模块三预热完成，现货bookTicker=%d 合约bookTicker=%d 现货深度=%d 合约深度=%d",
				len(spotSymbols),
				len(futuresSymbols),
				len(spotSymbols),
				len(futuresSymbols),
			)
			return
		}

		select {
		case <-waitCtx.Done():
			e.logger.Printf(
				"模块三预热超时，开始补齐缺失行情，现货bookTicker缺失=%d 合约bookTicker缺失=%d 现货深度缺失=%d 合约深度缺失=%d",
				len(spotTickerMissing),
				len(futuresTickerMissing),
				len(spotDepthMissing),
				len(futuresDepthMissing),
			)
			e.backfillMissingMarketData(ctx, targets)
			return
		case <-ticker.C:
		}
	}
}

func (e *FundingRateSniperEngine) marketCacheMissingSymbols(spotSymbols []string, futuresSymbols []string) ([]string, []string, []string, []string) {
	var spotTickerMissing []string
	if e.spotTickerCache != nil {
		spotTickerMissing = e.spotTickerCache.MissingSymbols(spotSymbols)
	}

	var futuresTickerMissing []string
	if e.futuresTickerCache != nil {
		futuresTickerMissing = e.futuresTickerCache.MissingSymbols(futuresSymbols)
	}

	var spotDepthMissing []string
	if e.spotDepthCache != nil {
		spotDepthMissing = e.spotDepthCache.MissingSymbols(spotSymbols)
	}

	var futuresDepthMissing []string
	if e.futuresDepthCache != nil {
		futuresDepthMissing = e.futuresDepthCache.MissingSymbols(futuresSymbols)
	}

	return spotTickerMissing, futuresTickerMissing, spotDepthMissing, futuresDepthMissing
}

// backfillConcurrencyLimit 限制补齐取价的并发 REST 数。修 PROJECT_AUDIT C-7:此前每个标的
// 无上限起 goroutine(N 标的 → 2N 并发请求),:58~:59 高峰互相挤兑共享 ctx 的截止时间,
// 3 天日志里 286 次 context deadline exceeded 全部集中在开仓决策最需要新鲜盘口的那一分钟。
const backfillConcurrencyLimit = 8

func (e *FundingRateSniperEngine) backfillMissingMarketData(ctx context.Context, targets []*MonitoredTarget) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, backfillConcurrencyLimit)
	for _, target := range targets {
		needSpotTicker := e.spotTickerCache != nil
		if needSpotTicker {
			_, needSpotTicker = e.spotTickerCache.Get(target.Candidate.Pair)
			needSpotTicker = !needSpotTicker
		}

		needFuturesTicker := e.futuresTickerCache != nil
		if needFuturesTicker {
			_, needFuturesTicker = e.futuresTickerCache.Get(target.Candidate.Symbol)
			needFuturesTicker = !needFuturesTicker
		}

		needSpotDepth := e.spotDepthCache != nil
		if needSpotDepth {
			cached, ok := e.spotDepthCache.Get(target.Candidate.Pair)
			needSpotDepth = !ok || !depthHasBothSides(cached)
		}

		needFuturesDepth := e.futuresDepthCache != nil
		if needFuturesDepth {
			cached, ok := e.futuresDepthCache.Get(target.Candidate.Symbol)
			needFuturesDepth = !ok || !depthHasBothSides(cached)
		}

		if !needSpotTicker && !needFuturesTicker && !needSpotDepth && !needFuturesDepth {
			continue
		}

		wg.Add(1)
		go func(t *MonitoredTarget, needSpotTicker bool, needFuturesTicker bool, needSpotDepth bool, needFuturesDepth bool) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			if needSpotTicker || needFuturesTicker {
				spotTicker, futuresTicker, tickerErr := e.fetchDualBookTicker(ctx, t.Candidate.Pair, t.Candidate.Symbol)
				if tickerErr != nil {
					e.logger.Printf("补齐 bookTicker 失败，symbol=%s err=%v", t.Candidate.Symbol, tickerErr)
				} else {
					if needSpotTicker && e.spotTickerCache != nil {
						e.spotTickerCache.Update(spotTicker)
					}
					if needFuturesTicker && e.futuresTickerCache != nil {
						e.futuresTickerCache.Update(futuresTicker)
					}
				}
			}

			if needSpotDepth || needFuturesDepth {
				spotDepth, futuresDepth, depthErr := e.fetchDualOrderBookDepth(ctx, t.Candidate.Pair, t.Candidate.Symbol)
				if depthErr != nil {
					e.logger.Printf("补齐深度失败，symbol=%s err=%v", t.Candidate.Symbol, depthErr)
					return
				}

				if needSpotDepth && e.spotDepthCache != nil {
					e.spotDepthCache.Update(t.Candidate.Pair, spotDepth)
				}
				if needFuturesDepth && e.futuresDepthCache != nil {
					e.futuresDepthCache.Update(t.Candidate.Symbol, futuresDepth)
				}
			}
		}(target, needSpotTicker, needFuturesTicker, needSpotDepth, needFuturesDepth)
	}
	wg.Wait()
}

// monitorWorker 负责单个目标的盘口监控。
func (e *FundingRateSniperEngine) monitorWorker(ctx context.Context, target *MonitoredTarget) {
	ticker := time.NewTicker(e.cfg.MonitorInterval)
	defer ticker.Stop()

	var spotSignal <-chan struct{}
	var futuresSignal <-chan struct{}
	spotUnsubscribe := func() {}
	futuresUnsubscribe := func() {}
	if e.spotDepthCache != nil {
		spotSignal, spotUnsubscribe = e.spotDepthCache.Subscribe(target.Candidate.Pair)
	}
	if e.futuresDepthCache != nil {
		futuresSignal, futuresUnsubscribe = e.futuresDepthCache.Subscribe(target.Candidate.Symbol)
	}
	defer spotUnsubscribe()
	defer futuresUnsubscribe()

	consecutiveBreaches := 0
	if ctx.Err() != nil {
		// 监控窗口已耗尽(预热/补齐吃满了整个窗口):静默退出,不再用过期 ctx 发 REST 制造错误风暴。
		return
	}
	e.checkTargetCost(ctx, target, &consecutiveBreaches)

	for {
		if err := e.checkPlatformFuse(); err != nil {
			e.logger.Printf("监控 %s 提前结束: %v", target.Candidate.Symbol, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-spotSignal:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		case <-futuresSignal:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		case <-ticker.C:
			e.checkTargetCost(ctx, target, &consecutiveBreaches)
		}
	}
}

// checkTargetCost 执行一次盘口摩擦成本检查。
