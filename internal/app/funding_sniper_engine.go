package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func NewFundingRateSniperEngineWithConfig(secretPath string, cfg EngineConfig) (*FundingRateSniperEngine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	secret, err := LoadSecretConfig(secretPath)
	if err != nil {
		return nil, err
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
		client:                     client,
		cfg:                        cfg,
		logger:                     log.New(os.Stdout, "[FundingRateSniper] ", log.LstdFlags|log.Lmicroseconds),
		goldenSymbolSet:            make(map[string]struct{}),
		goldenSpotPairByFuture:     make(map[string]string),
		spotMeta:                   make(map[string]SpotSymbol),
		spotRules:                  make(map[string]SymbolTradeRules),
		futuresMeta:                make(map[string]FuturesSymbol),
		futuresRules:               make(map[string]SymbolTradeRules),
		reportStore:                reportStore,
		domainEventSink:            reportStore,
		costModel:                  newResidualCostModel(cfg.HistoricalConfig),
		emergencyStopFile:          effectiveEmergencyStopFile(cfg),
		openHedgeStatePath:         effectiveOpenHedgeStatePath(cfg),
		dailyRiskStatePath:         filepath.Join(cfg.NetworkConfig.RecordDir, liveDailyRiskStateName),
		dailyRiskPersistCh:         make(chan persistedDailyRiskState, 1),
		currentStage:               StageReady,
		symbolCooldowns:            make(map[string]symbolCooldownState),
		exposureLocks:              make(map[string]exposureLockState),
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
	return engine, nil
}

// Start 启动完整的小时级主循环。
func (e *FundingRateSniperEngine) Start(ctx context.Context) error {
	e.runCtx = ctx
	e.startDailyRiskStateWriter(ctx)
	if err := e.initialize(ctx); err != nil {
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

	for {
		if err := e.checkLiveSafetyGuards("idle"); err != nil {
			return err
		}
		if err := e.syncServerTimeOffset(ctx); err != nil {
			e.logger.Printf("刷新服务器时间偏移失败，将沿用旧偏移继续调度: %v", err)
		}

		wakeAt := nextWakeTime(e.serverNow(), e.cfg.StrategyConfig.WakeMinute, e.cfg.StrategyConfig.WakeSecond)
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
		if err := e.runOneCycle(ctx, wakeAt); err != nil {
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

	if err := e.verifyTradingEnvironment(ctx); err != nil {
		return err
	}
	if err := e.verifySpotBNBBurnEnabled(ctx); err != nil {
		return err
	}
	if err := e.loadDailyRiskState(); err != nil {
		return err
	}
	if err := e.checkLiveSafetyGuards("initialize"); err != nil {
		return err
	}

	e.logger.Printf("模块一已加载完成，黄金币种数量: %d", len(e.goldenSymbols))
	return nil
}

func (e *FundingRateSniperEngine) livePreferredSpotQuoteAssets() []string {
	asset := strings.ToUpper(strings.TrimSpace(e.cfg.StrategyConfig.LiveFundingAsset))
	if asset == "" {
		return preferredSpotQuoteAssets(e.cfg.StrategyConfig.PreferredSpotQuoteAssets)
	}
	return []string{asset}
}

// runOneCycle 串起模块二到模块五。
func (e *FundingRateSniperEngine) runOneCycle(ctx context.Context, wakeAt time.Time) error {
	e.platformRiskMu.Lock()
	e.currentCycleID = newCycleID(e.runID)
	cycleID := e.currentCycleID
	e.platformRiskMu.Unlock()
	// 同步日志旁路用的 trace id 副本（在释放 platformRiskMu 之后），后续状态迁移日志才能带上本轮 cycle id。
	e.setTraceLogIDs(e.runID, cycleID)
	e.resetLifecycleTracking(LifecycleStageScanning, "开始扫描候选标的")
	e.resetCyclePlatformRiskCounters()
	e.runtimeReporter.EmitWithReason(StageScan, 0, "开始扫描候选标的")
	e.logger.Printf("模块二启动，觉醒时间: %s", wakeAt.Format("15:04:05"))
	if err := e.syncServerTimeOffset(ctx); err != nil {
		return err
	}

	targets, err := e.scanTopFundingTargets(ctx)
	if err != nil {
		return err
	}
	targets = e.filterTargetsBySymbolCooldown(targets)
	if len(targets) == 0 {
		e.setCurrentMonitorTargets(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "扫描结束，没有满足条件的目标")
		e.runtimeReporter.EmitWithReason(StageScanEmpty, 0, "扫描结束，没有满足条件的目标")
		e.logger.Println("模块二扫描结束，本轮没有满足条件的目标")
		return nil
	}

	e.setCurrentMonitorTargets(targets)
	_ = e.advanceLifecycleStage(LifecycleStageMonitoring, fmt.Sprintf("进入监控阶段，目标数=%d", len(targets)))
	e.runtimeReporter.EmitWithReason(StageMonitor, len(targets), fmt.Sprintf("进入监控阶段，目标数=%d", len(targets)))
	monitored := e.monitorTargets(ctx, targets)
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	validTargets := filterValidTargets(monitored)
	if len(validTargets) == 0 {
		e.setCurrentMonitorTargets(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "监控结束，所有目标都已被风控作废")
		e.runtimeReporter.EmitWithReason(StageMonitorInvalidated, 0, "监控结束，所有目标都已被风控作废")
		e.logger.Println("模块三结束，所有目标都已被风控作废")
		return nil
	}

	e.setCurrentMonitorTargets(validTargets)
	e.runtimeReporter.EmitWithReason(StageOpen, len(validTargets), fmt.Sprintf("进入开仓阶段，目标数=%d", len(validTargets)))
	openProtocol := e.ensureOpenProtocol()
	if openProtocol == nil {
		return fmt.Errorf("open protocol 未初始化")
	}
	preparedResult, err := openProtocol.PrepareSequence(ctx, validTargets)
	if err != nil {
		return err
	}
	if err := e.applyLifecycleProtocolResult(preparedResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(open.prepare): %v", err)
	} else {
		e.runtimeReporter.EmitLocked(EngineStage(""))
	}
	e.emitDomainEvents(preparedResult.ProtocolResult.Events)
	executeOpenResult, err := openProtocol.ExecuteSequence(ctx, validTargets, preparedResult.Preparation)
	if err != nil {
		return err
	}
	if err := e.applyLifecycleProtocolResult(executeOpenResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(open.execute): %v", err)
	} else {
		e.runtimeReporter.EmitLocked(EngineStage(""))
	}
	e.emitDomainEvents(executeOpenResult.ProtocolResult.Events)
	openHedges, beforeSnapshot := executeOpenResult.Hedges, executeOpenResult.BeforeSnapshot
	if fuseErr := e.checkPlatformFuse(); fuseErr != nil {
		return fuseErr
	}
	if len(openHedges) == 0 {
		e.setCurrentPositions(nil)
		_ = e.advanceLifecycleStage(LifecycleStageCompleted, "开仓结束，没有成功建仓的目标")
		e.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, "开仓结束，没有成功建仓的目标")
		e.logger.Println("模块四结束，没有成功建仓的目标")
		return nil
	}

	_ = e.advanceLifecycleStage(LifecycleStageCloseMonitoring, fmt.Sprintf("进入平仓监控阶段，持仓数=%d", len(openHedges)))
	e.runtimeReporter.EmitWithReason(StageCloseMonitor, len(openHedges), fmt.Sprintf("进入平仓监控阶段，持仓数=%d", len(openHedges)))
	closeProtocol := e.ensureCloseProtocol()
	if closeProtocol == nil {
		return fmt.Errorf("close protocol 未初始化")
	}
	closeResult, err := closeProtocol.ExecuteSequence(ctx, openHedges, beforeSnapshot)
	if err != nil {
		return err
	}
	if err := e.applyLifecycleProtocolResult(closeResult.ProtocolResult); err != nil {
		e.logger.Printf("忽略非法生命周期迁移(close.execute): %v", err)
	} else {
		e.runtimeReporter.EmitLocked(EngineStage(""))
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
	if e.reportStore != nil {
		if err := e.reportStore.AppendSettlement(settlement); err != nil {
			e.logger.Printf("模块五 JSON 落盘失败: %v", err)
		}
	}
	if err := e.noteDailySettlement(settlement.NetPnL); err != nil {
		return err
	}
	e.scheduleAsyncFundingSettlement(settlement, openHedges)
	if e.cfg.RiskConfig.MaxCycleLossUSDT > 0 && settlement.NetPnL <= -e.cfg.RiskConfig.MaxCycleLossUSDT {
		if incidentErr := e.recordExecutionIncident("cycle_loss_limit", "-", fmt.Sprintf("单周期净亏损 %.6f USDT 已超过阈值 %.6f USDT", -settlement.NetPnL, e.cfg.RiskConfig.MaxCycleLossUSDT)); incidentErr != nil {
			return incidentErr
		}
		return fatalEngineError{message: fmt.Sprintf("单周期净亏损达到熔断阈值: loss=%.6f limit=%.6f", -settlement.NetPnL, e.cfg.RiskConfig.MaxCycleLossUSDT)}
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

// scanTopFundingTargets 在 XX:57:00 后扫描全网费率，并取 Top 5。
func (e *FundingRateSniperEngine) scanTopFundingTargets(ctx context.Context) ([]*MonitoredTarget, error) {
	items, err := e.fetchPremiumIndex(ctx)
	if err != nil {
		return nil, err
	}

	now := e.serverNow()
	candidates := make([]FundingCandidate, 0, len(items))
	observations := make([]ScanObservationRecord, 0, len(items))
	breakEven := coherentMinFundingRate(e.cfg)
	for _, item := range items {
		if _, ok := e.goldenSymbolSet[item.Symbol]; !ok {
			continue
		}

		nextFundingTime := time.UnixMilli(item.NextFundingTime)
		remaining := nextFundingTime.Sub(now)
		if remaining <= 0 || remaining > e.cfg.StrategyConfig.ScanWindow {
			continue
		}

		staleness := premiumIndexStaleness(now, item)
		isStale := e.cfg.NetworkConfig.MaxPremiumIndexStaleness > 0 && staleness > e.cfg.NetworkConfig.MaxPremiumIndexStaleness

		estimatedRate, lastFundingRate, fundingJump, basisRate, _ := estimateScanFundingSignal(item)
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
			e.runID, e.currentCycleID, now, item.Symbol, nextFundingTime, direction,
			estimatedRate, lastFundingRate, fundingJump, basisRate,
			breakEven, minRateGate, e.cfg.StrategyConfig.MaxEstimatedFundingRate,
			isStale, action != StrategyActionReject, reason,
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

	for idx := range candidates {
		futuresRule := e.futuresRules[candidates[idx].Symbol]
		profile := GetSymbolRiskProfile(candidates[idx].Symbol)
		rawOrderQty := (e.cfg.StrategyConfig.PerSymbolQuoteUSDT * profile.PerSymbolQuoteMultiplier) / candidates[idx].MarkPrice
		candidates[idx].OrderQuantity = normalizeQuantity(rawOrderQty, futuresRule, candidates[idx].QuantityPrecision)
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
			CycleID:             e.currentCycleID,
			Candidate:           candidate,
			IsValid:             1,
			MonitorState:        "watching",
		})
	}

	e.flushScanObservations(observations)
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

	return targets, nil
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

func (e *FundingRateSniperEngine) openFireAt(targets []*MonitoredTarget) time.Time {
	if len(targets) == 0 {
		return time.Time{}
	}
	return earliestFundingTime(targets).Add(-e.cfg.StrategyConfig.MonitorStopLead)
}

func (e *FundingRateSniperEngine) openPreparationStartAt(targets []*MonitoredTarget) time.Time {
	fireAt := e.openFireAt(targets)
	if fireAt.IsZero() {
		return time.Time{}
	}
	lead := e.cfg.ExecutionConfig.OpenPreparationLead
	if lead <= 0 {
		return fireAt
	}
	return fireAt.Add(-lead)
}

func (e *FundingRateSniperEngine) openNoGoCheckAt(targets []*MonitoredTarget) time.Time {
	fireAt := e.openFireAt(targets)
	if fireAt.IsZero() {
		return time.Time{}
	}
	lead := e.cfg.ExecutionConfig.PreOpenNoGoLead
	if lead <= 0 {
		return fireAt
	}
	return fireAt.Add(-lead)
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

	waitCtx := ctx
	var cancel context.CancelFunc
	if e.cfg.StreamWarmupTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, e.cfg.StreamWarmupTimeout)
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

func (e *FundingRateSniperEngine) backfillMissingMarketData(ctx context.Context, targets []*MonitoredTarget) {
	var wg sync.WaitGroup
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
			_, needSpotDepth = e.spotDepthCache.Get(target.Candidate.Pair)
			needSpotDepth = !needSpotDepth
		}

		needFuturesDepth := e.futuresDepthCache != nil
		if needFuturesDepth {
			_, needFuturesDepth = e.futuresDepthCache.Get(target.Candidate.Symbol)
			needFuturesDepth = !needFuturesDepth
		}

		if !needSpotTicker && !needFuturesTicker && !needSpotDepth && !needFuturesDepth {
			continue
		}

		wg.Add(1)
		go func(t *MonitoredTarget, needSpotTicker bool, needFuturesTicker bool, needSpotDepth bool, needFuturesDepth bool) {
			defer wg.Done()

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
