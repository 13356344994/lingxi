package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// OpenProtocol 封装开仓生命周期，并作为开仓主流程的协议入口。
type OpenProtocol struct {
	engine *FundingRateSniperEngine
}

func newOpenProtocol(engine *FundingRateSniperEngine) *OpenProtocol {
	if engine == nil {
		return nil
	}
	return &OpenProtocol{engine: engine}
}

func (e *FundingRateSniperEngine) prepareOpenSequence(ctx context.Context, targets []*MonitoredTarget) (openExecutionPreparation, error) {
	protocol := e.ensureOpenProtocol()
	if protocol == nil {
		return openExecutionPreparation{}, nil
	}
	result, err := protocol.PrepareSequence(ctx, targets)
	return result.Preparation, err
}

func (e *FundingRateSniperEngine) executeOpenSequence(ctx context.Context, targets []*MonitoredTarget, prepared openExecutionPreparation) ([]OpenHedge, CycleAccountSnapshot, error) {
	protocol := e.ensureOpenProtocol()
	if protocol == nil {
		return nil, CycleAccountSnapshot{}, nil
	}
	result, err := protocol.ExecuteSequence(ctx, targets, prepared)
	return result.Hedges, result.BeforeSnapshot, err
}

func (e *FundingRateSniperEngine) ensureOpenProtocol() *OpenProtocol {
	if e == nil {
		return nil
	}
	e.openProtocolOnce.Do(func() {
		e.openProtocol = newOpenProtocol(e)
	})
	return e.openProtocol
}

func (p *OpenProtocol) PrepareSequence(ctx context.Context, targets []*MonitoredTarget) (OpenProtocolPreparationResult, error) {
	prepared, err := p.prepareSequence(ctx, targets)
	return OpenProtocolPreparationResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "open.prepare",
			Successful:   err == nil,
			Summary:      "open sequence prepared",
			CurrentStage: LifecycleStageMonitoring,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStagePreOpenCheck,
				Reason:    "open preparation completed",
			},
			Risk: RiskDecision{
				Action:      RiskActionObserve,
				Category:    "open_prepare",
				Severity:    RiskSeverityLevelInfo,
				Reason:      "open sequence prepared",
				CanContinue: err == nil,
			},
			Events: newProtocolEvents(p.engine, "open.prepare", err == nil,
				protocolEventSpec{
					EventType: EventTypeOpenPrepared,
					Level:     EventLevelInfo,
					Stage:     LifecycleStagePreOpenCheck,
					Reason:    "open preparation completed",
				},
			),
		},
		Preparation: prepared,
	}, err
}

func (p *OpenProtocol) ExecuteSequence(ctx context.Context, targets []*MonitoredTarget, prepared openExecutionPreparation) (OpenProtocolExecutionResult, error) {
	hedges, beforeSnapshot, err := p.executeSequence(ctx, targets, prepared)
	return OpenProtocolExecutionResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "open.execute",
			Successful:   err == nil,
			Summary:      "open sequence executed",
			CurrentStage: LifecycleStageOpening,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStageHolding,
				Reason:    "open sequence executed",
			},
			Risk: RiskDecision{
				Action:      RiskActionObserve,
				Category:    "open_execute",
				Severity:    RiskSeverityLevelInfo,
				Reason:      "open sequence executed",
				CanContinue: err == nil,
			},
			Events: newProtocolEvents(p.engine, "open.execute", err == nil,
				protocolEventSpec{
					EventType: EventTypeOpenExecuted,
					Level:     EventLevelInfo,
					Stage:     LifecycleStageOpening,
					Reason:    "open sequence executed",
				},
				protocolEventSpec{
					EventType: EventTypeOpenValidated,
					Level:     EventLevelInfo,
					Stage:     LifecycleStageValidatingOpen,
					Reason:    "fresh open hedges validated",
				},
			),
		},
		Hedges:         hedges,
		BeforeSnapshot: beforeSnapshot,
	}, err
}

func (p *OpenProtocol) OpenHedgeForTarget(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult) (OpenHedge, error) {
	return p.openHedgeForTarget(ctx, target, futureResult)
}

func (p *OpenProtocol) OpenHedgeForTargetParallel(ctx context.Context, plan openSequencePlan) (OpenHedge, error) {
	return p.openHedgeForTargetParallel(ctx, plan)
}

func (p *OpenProtocol) ValidateFreshOpenHedge(ctx context.Context, hedge *OpenHedge) error {
	return p.validateFreshOpenHedge(ctx, hedge)
}

func (p *OpenProtocol) ExecuteSequentialOpenPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	return p.executeSequentialPlans(ctx, plans)
}

func (p *OpenProtocol) ExecuteParallelOpenPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	return p.executeParallelPlans(ctx, plans)
}

func (p *OpenProtocol) prepareSequence(ctx context.Context, targets []*MonitoredTarget) (openExecutionPreparation, error) {
	validTargets := filterValidTargets(targets)
	if len(validTargets) == 0 {
		return openExecutionPreparation{}, nil
	}
	if err := p.engine.ensureFuturesExecutionSettings(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	if err := p.engine.ensureUSDTFundingBalancesForOpen(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	if err := p.engine.ensureSpotBNBFeeCoverageForOpen(ctx, validTargets); err != nil {
		return openExecutionPreparation{}, err
	}
	beforeSnapshot, err := p.engine.captureCycleAccountSnapshot(ctx, validTargets)
	if err != nil {
		return openExecutionPreparation{}, err
	}
	plans, err := p.engine.buildOpenSequencePlans(ctx, validTargets, beforeSnapshot)
	if err != nil {
		return openExecutionPreparation{beforeSnapshot: beforeSnapshot}, err
	}
	prepared := openExecutionPreparation{
		beforeSnapshot: beforeSnapshot,
		plans:          plans,
		fireAt:         p.engine.openFireAt(validTargets),
		goNoGoAt:       p.engine.openNoGoCheckAt(validTargets),
	}
	p.engine.logger.Printf(
		"模块四预备完成，开仓时间=%s，战前检查时间=%s，顺序池候选=%d",
		prepared.fireAt.Format("15:04:05"),
		prepared.goNoGoAt.Format("15:04:05"),
		len(prepared.plans),
	)
	return prepared, nil
}

func (p *OpenProtocol) executeSequence(ctx context.Context, targets []*MonitoredTarget, prepared openExecutionPreparation) ([]OpenHedge, CycleAccountSnapshot, error) {
	validTargets := filterValidTargets(targets)
	if len(validTargets) == 0 {
		return nil, CycleAccountSnapshot{}, nil
	}
	if len(prepared.plans) == 0 {
		return nil, prepared.beforeSnapshot, nil
	}
	if !prepared.goNoGoAt.IsZero() && p.engine.serverNow().Before(prepared.goNoGoAt) {
		if sleepErr := p.engine.sleepUntilServerTime(ctx, prepared.goNoGoAt); sleepErr != nil {
			return nil, CycleAccountSnapshot{}, sleepErr
		}
	}
	if syncErr := p.engine.syncServerTimeOffset(ctx); syncErr != nil {
		p.engine.logger.Printf("模块四战前刷新服务器时间偏移失败，将沿用旧偏移: %v", syncErr)
	}
	if err := p.engine.verifyPreOpenGoNoGo(ctx, validTargets); err != nil {
		p.engine.logger.Printf("模块四放弃本轮开仓，战前检查失败: %v", err)
		p.engine.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, fmt.Sprintf("战前 Go/No-Go 未通过: %v", err))
		return nil, prepared.beforeSnapshot, nil
	}
	stablePlans, stabilityAlerts := p.engine.filterOpenPlansByFundingStability(ctx, prepared.plans, prepared.fireAt)
	for _, alert := range stabilityAlerts {
		p.engine.logger.Printf("模块四战前稳定性过滤: %s", alert)
	}
	if len(stablePlans) == 0 {
		p.engine.runtimeReporter.EmitWithReason(StageOpenEmpty, 0, "战前 funding 稳定性门未通过")
		return nil, prepared.beforeSnapshot, nil
	}
	cacheOnlyUntil := prepared.fireAt.Add(maxDurationValue(p.engine.cfg.ExecutionConfig.ExecutionRecoveryTimeout, 5*time.Second))
	p.engine.setBookTickerCacheOnlyUntil(cacheOnlyUntil)
	defer p.engine.setBookTickerCacheOnlyUntil(time.Time{})
	if prepared.fireAt.After(p.engine.serverNow()) {
		if sleepErr := p.engine.sleepUntilServerTime(ctx, prepared.fireAt); sleepErr != nil {
			return nil, CycleAccountSnapshot{}, sleepErr
		}
	}

	sequentialPlans, parallelPlans := splitOpenSequencePlans(p.engine.cfg, stablePlans)
	p.engine.logger.Printf("模块四开始，触发时间=%s，顺序池=%d，并发池=%d", prepared.fireAt.Format("15:04:05"), len(sequentialPlans), len(parallelPlans))

	var (
		wg            sync.WaitGroup
		sequentialErr error
		parallelErr   error
		sequentialHgs []OpenHedge
		parallelHgs   []OpenHedge
	)
	if len(sequentialPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sequentialHgs, sequentialErr = p.executeSequentialPlans(ctx, sequentialPlans)
		}()
	}
	if len(parallelPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parallelHgs, parallelErr = p.executeParallelPlans(ctx, parallelPlans)
		}()
	}
	wg.Wait()
	if sequentialErr != nil {
		return nil, prepared.beforeSnapshot, sequentialErr
	}
	if parallelErr != nil {
		return nil, prepared.beforeSnapshot, parallelErr
	}
	hedges := append(sequentialHgs, parallelHgs...)
	// 并入既有常驻组合后整体写入(滚动持有下不抹既有 keep 仓;单周期下既有为空=仅新开,行为不变)。
	p.engine.setCurrentPositionsFromHedges(p.engine.mergeResidentBaselineWith(hedges))
	p.engine.logger.Printf("模块四结束，成功锁仓数量: %d", len(hedges))
	return hedges, prepared.beforeSnapshot, nil
}

func (p *OpenProtocol) executeSequentialPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(plans))
	for _, plan := range plans {
		futuresOrders = append(futuresOrders, plan.futuresOrder)
	}
	if err := p.engine.noteOrderPlaced(ctx, "batch_open_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), true); err != nil {
		return nil, err
	}
	futuresResults, err := p.engine.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return nil, err
	}
	return p.engine.executeOpenHedgePlans(ctx, plans, futuresResults)
}

func (p *OpenProtocol) executeParallelPlans(ctx context.Context, plans []openSequencePlan) ([]OpenHedge, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	hedges := make([]OpenHedge, 0, len(plans))
	var hedgesMu sync.Mutex
	err := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(p.engine.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		hedge, openErr := p.OpenHedgeForTargetParallel(workerCtx, plans[idx])
		if openErr != nil {
			var skipped nonFatalOpenAbortError
			if errors.As(openErr, &skipped) {
				p.engine.logger.Printf("模块四跳过 %s: %v", plans[idx].target.Candidate.Symbol, skipped)
				return nil
			}
			return p.engine.recordExecutionIncident("open_recovery", plans[idx].target.Candidate.Symbol, openErr.Error())
		}
		hedgesMu.Lock()
		hedges = append(hedges, hedge)
		// 增量落盘时并入既有常驻组合,避免崩溃在开仓中途时台账只剩本轮新开、丢失既有 keep 仓。
		persistErr := p.engine.syncPersistedOpenHedges(p.engine.mergeResidentBaselineWith(hedges))
		hedgesMu.Unlock()
		return persistErr
	})
	if err != nil {
		return nil, err
	}
	return hedges, nil
}

func (e *FundingRateSniperEngine) openHedgeForTarget(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult) (OpenHedge, error) {
	protocol := e.ensureOpenProtocol()
	if protocol == nil {
		return OpenHedge{}, fmt.Errorf("open protocol 未初始化")
	}
	return protocol.OpenHedgeForTarget(ctx, target, futureResult)
}

func (e *FundingRateSniperEngine) openHedgeForTargetParallel(ctx context.Context, plan openSequencePlan) (OpenHedge, error) {
	protocol := e.ensureOpenProtocol()
	if protocol == nil {
		return OpenHedge{}, fmt.Errorf("open protocol 未初始化")
	}
	return protocol.OpenHedgeForTargetParallel(ctx, plan)
}

func (e *FundingRateSniperEngine) validateFreshOpenHedge(ctx context.Context, hedge *OpenHedge) error {
	protocol := e.ensureOpenProtocol()
	if protocol == nil {
		return fmt.Errorf("open protocol 未初始化")
	}
	return protocol.ValidateFreshOpenHedge(ctx, hedge)
}

func (p *OpenProtocol) openHedgeForTarget(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult) (OpenHedge, error) {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if err := p.engine.ensureSymbolNotLockedByExposure(target.Candidate.Symbol, "open_hedge"); err != nil {
		return OpenHedge{}, err
	}
	futuresExecution, resolveErr := p.engine.resolveFuturesOrderExecution(ctx, futureResult.Symbol, futureResult.ClientOrderID, futureResult)
	filledFuturesQty := futuresExecution.ExecutedQty
	if resolveErr != nil {
		return p.handleOpenFuturesResolutionFailure(ctx, target, futureResult, direction, resolveErr)
	}
	if filledFuturesQty <= executionQtyTolerance {
		return OpenHedge{}, fmt.Errorf("合约空单成交数量为 0")
	}

	spotQty, spotPriceForCheck, spotTradable := p.openSpotTarget(target, filledFuturesQty)
	if !spotTradable {
		return p.handleOpenSpotNotTradable(ctx, target, direction, filledFuturesQty, spotQty)
	}

	spotOrder, spotErr := p.engine.placeDirectionalSpotMarketOrderIdempotent(ctx, target.Candidate.Pair, direction.openSpotSide(), spotQty, newClientOrderID("sopen", target.Candidate.Pair), direction.spotVenue(), "spot_market_"+strings.ToLower(direction.openSpotSide()), true)
	if spotErr != nil {
		return p.handleOpenSpotFailure(ctx, target, futureResult, direction, filledFuturesQty, spotPriceForCheck, spotErr)
	}

	hedge, err := p.finalizeOpenHedge(ctx, target, futureResult, futuresExecution, spotOrder, spotPriceForCheck)
	if err != nil {
		return OpenHedge{}, err
	}
	if err := p.validateFreshOpenHedge(ctx, &hedge); err != nil {
		return OpenHedge{}, err
	}
	return hedge, nil
}

func (p *OpenProtocol) openHedgeForTargetParallel(ctx context.Context, plan openSequencePlan) (OpenHedge, error) {
	target := plan.target
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	if err := p.engine.ensureSymbolNotLockedByExposure(target.Candidate.Symbol, "parallel_open_hedge"); err != nil {
		return OpenHedge{}, err
	}
	resolution, err := p.executeParallelOpenOrders(ctx, plan, direction)
	if err != nil {
		return OpenHedge{}, err
	}
	decision := resolution.decision()
	if err := p.applyParallelOpenDecision(ctx, plan, direction, resolution, decision); err != nil {
		return OpenHedge{}, err
	}

	hedge, err := p.finalizeOpenHedge(ctx, target, futuresExecutionToResult(resolution.futuresResult.snapshot, plan.futuresOrder), resolution.futuresResult.snapshot, resolution.spotResult.order, resolution.spotPriceForCheck)
	if err != nil {
		return OpenHedge{}, err
	}
	if err := p.validateFreshOpenHedge(ctx, &hedge); err != nil {
		return OpenHedge{}, err
	}
	return hedge, nil
}

func (p *OpenProtocol) validateFreshOpenHedge(ctx context.Context, hedge *OpenHedge) error {
	// Gate 4 关键不变量：对冲两腿建立后，名义偏离超阈值立即熔断，杜绝裸敞口。
	spotNotional := hedge.OpenSpotQuoteQty
	futuresNotional := hedge.HedgeQty * hedge.OpenFuturesAvgPrice
	if err := p.engine.assertHedgeInvariant(hedge.Target.Symbol, spotNotional, futuresNotional); err != nil {
		hedge.markClosing("hedge_invariant_breach")
		return err
	}
	spotTicker, futuresTicker, err := p.engine.fetchDualBookTicker(ctx, hedge.Target.Pair, hedge.Target.Symbol)
	if err != nil {
		return p.abortFreshOpenHedge(ctx, hedge, fmt.Sprintf("post_open_ticker_unavailable: %v", err))
	}
	adverseSpread, ok := adverseCloseSpreadRate(hedge.carryDirection(), spotTicker, futuresTicker)
	if !ok {
		return p.abortFreshOpenHedge(ctx, hedge, "post_open_spread_invalid")
	}
	if p.isFreshOpenHedgeSafe(hedge, adverseSpread) {
		hedge.markActive(fmt.Sprintf("validated adverse_spread=%.6f", adverseSpread))
		return nil
	}
	econGate := p.engine.economicParams()
	immediateNetProfit := estimateReservedNetProfitRate(
		hedge.Target.EstimatedRate,
		adverseSpread,
		econGate.FeeReserveRate,
		econGate.ExitSpreadReserveRate,
		p.engine.liveBorrowReserveRate(hedge.Target, hedge.LastKnownSpotPrice(), p.engine.serverNow()),
	)
	return p.abortFreshOpenHedge(
		ctx,
		hedge,
		fmt.Sprintf(
			"adverse_spread=%.6f reserve=%.6f immediate_net=%.6f invalidate=%.6f",
			adverseSpread,
			econGate.ExitSpreadReserveRate,
			immediateNetProfit,
			p.engine.cfg.RiskConfig.ImmediateInvalidateRate,
		),
	)
}

func (p *OpenProtocol) abortFreshOpenHedge(ctx context.Context, hedge *OpenHedge, reason string) error {
	hedge.markClosing("abort_fresh_open: " + strings.TrimSpace(reason))
	closeCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	alerts, err := p.engine.closeHedgePositionParallel(closeCtx, closeSequencePlan{
		hedge:             hedge,
		futuresOrder:      p.engine.buildProtectedCloseFuturesOrder(closeCtx, *hedge),
		preferPassiveSpot: false,
	})
	if err != nil {
		// 即时撤退失败时**不**直接放弃:这是一笔双腿已成交的真实持仓,必须转入 ExposureManager 托管收敛
		// (保持 symbol 锁定 + 有界重试 + 升级),否则会留下无人收敛的活敞口。
		rescueResult, rescueErr := p.engine.scheduleExposureRescueProtocolResult(ctx, exposureRescueTask{
			Mode:               exposureRescueModeClose,
			Phase:              "abort_after_adverse_selection",
			Direction:          hedge.carryDirection(),
			Symbol:             hedge.Target.Symbol,
			Pair:               hedge.Target.Pair,
			SpotBaseAsset:      hedge.Target.SpotBaseAsset,
			SpotBasePrecision:  hedge.Target.SpotBasePrecision,
			SpotReferencePrice: hedge.LastKnownSpotPrice(),
			InitialHedgeQty:    hedge.HedgeQty,
		}, "开仓后逆向选择复核失败且即时撤退失败且 ExposureManager 托管失败", "开仓后即时撤退失败，已转入 ExposureManager 托管收敛")
		if rescueErr != nil {
			return fmt.Errorf("开仓后逆向选择复核失败且即时撤退失败: closeErr=%v rescueErr=%w", err, rescueErr)
		}
		rescueResult.applyOpenProtocol(hedge, "abort_after_adverse_selection_escalated")
		p.engine.recordExecutionAction("open_recovery", hedge.Target.Symbol, "abort_after_adverse_selection_escalated", fmt.Sprintf("closeErr=%v", err))
		return nonFatalOpenAbortError{message: rescueResult.lifecycleReason("开仓后即时撤退失败，已转入 ExposureManager 托管收敛")}
	}
	if len(alerts) > 0 {
		p.engine.logger.Printf("开仓后即时撤退告警 %s: %s", hedge.Target.Symbol, strings.Join(alerts, " | "))
	}
	result := recoveryActionResult{
		ProtocolAction: "abort_after_adverse_selection",
		Summary:        fmt.Sprintf("开仓后逆向选择复核未通过，已即时撤退: %s", strings.TrimSpace(reason)),
		Aborted:        true,
	}
	result.applyOpenProtocol(hedge, reason)
	p.engine.recordExecutionAction("open_recovery", hedge.Target.Symbol, "abort_after_adverse_selection", reason)
	return nonFatalOpenAbortError{message: result.openDecision(reason).Summary}
}

func (p *OpenProtocol) handleOpenFuturesResolutionFailure(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult, direction fundingCarryDirection, resolveErr error) (OpenHedge, error) {
	if isUnknownOrderStateError(resolveErr) {
		err := p.engine.scheduleOpenExposureRescueForTargetOrFail(
			ctx,
			target,
			direction,
			"open_futures_unknown",
			target.LastSpotAsk,
			"",
			futureResult.ClientOrderID,
			0,
			0,
			"合约开仓终态未知且 ExposureManager 托管失败",
			"合约开仓终态未知，已转入 ExposureManager 托管",
		)
		return OpenHedge{}, err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	cleanup := p.engine.flattenUncertainFuturesOpen(cleanupCtx, target.Candidate.Symbol, futureResult.ClientOrderID, futureResult)
	return OpenHedge{}, cleanup.openError("合约空单未确认成交", resolveErr)
}

func (p *OpenProtocol) handleOpenSpotNotTradable(ctx context.Context, target *MonitoredTarget, direction fundingCarryDirection, filledFuturesQty float64, spotQty float64) (OpenHedge, error) {
	result, err := p.engine.executeOpenFuturesRevertProtocol(ctx, target.Candidate.Symbol, filledFuturesQty, direction, "close_futures_after_invalid_spot_qty", fmt.Sprintf("filledFuturesQty=%.8f spotTargetQty=%.8f", filledFuturesQty, spotQty))
	if err != nil {
		return OpenHedge{}, fmt.Errorf("现货对冲数量无效且合约回补失败: %w", err)
	}
	return OpenHedge{}, result.openError("现货对冲数量归零，已回补合约空单", nil)
}

func (p *OpenProtocol) handleOpenSpotFailure(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult, direction fundingCarryDirection, filledFuturesQty float64, spotPriceForCheck float64, spotErr error) (OpenHedge, error) {
	if isUnknownOrderStateError(spotErr) {
		err := p.engine.scheduleOpenExposureRescueForTargetOrFail(
			ctx,
			target,
			direction,
			"open_spot_unknown",
			spotPriceForCheck,
			"",
			futureResult.ClientOrderID,
			0,
			filledFuturesQty,
			"现货开仓终态未知且 ExposureManager 托管失败",
			"现货开仓终态未知，已转入 ExposureManager 托管",
		)
		return OpenHedge{}, err
	}
	result, err := p.engine.executeOpenFuturesRevertProtocol(ctx, target.Candidate.Symbol, filledFuturesQty, direction, "close_futures_after_spot_open_failure", fmt.Sprintf("filledFuturesQty=%.8f spotErr=%v", filledFuturesQty, spotErr))
	if err != nil {
		return OpenHedge{}, fmt.Errorf("现货补仓失败且合约回补失败: spotErr=%v recoverErr=%w", spotErr, err)
	}
	revertErr := result.openError("现货补仓失败，已回补合约空单", spotErr)
	// 借币不可用(-3045/-3006/-3008)且合约腿已回补 → 标记为良性「跳过+冷却」错误,不计熔断(R 借币降级)。
	if isBorrowUnavailableError(spotErr) {
		return OpenHedge{}, borrowUnavailableRevertedError{message: revertErr.Error()}
	}
	return OpenHedge{}, revertErr
}

func (p *OpenProtocol) openSpotTarget(target *MonitoredTarget, hedgeQty float64) (float64, float64, bool) {
	spotRule := p.engine.spotRules[target.Candidate.Pair]
	spotQty := normalizeQuantity(hedgeQty, spotRule, target.Candidate.SpotBasePrecision)
	spotPriceForCheck := target.LastSpotAsk
	if spotPriceForCheck <= 0 {
		spotPriceForCheck = target.Candidate.MarkPrice
	}
	return spotQty, spotPriceForCheck, isQuantityTradable(spotQty, spotPriceForCheck, spotRule)
}

func (p parallelOpenProtocol) revertSpotOpenAfterFuturesFailure(ctx context.Context, target *MonitoredTarget, spotOrder SpotOrderResponse, spotPriceForCheck float64) error {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	spotExecutedQty := resolveSpotNetBaseQuantity(spotOrder, target.Candidate.SpotBaseAsset)
	if spotExecutedQty <= executionQtyTolerance {
		return nil
	}
	spotRule := p.engine.spotRules[target.Candidate.Pair]
	residualQty := normalizeQuantity(spotExecutedQty, spotRule, target.Candidate.SpotBasePrecision)
	if residualQty <= executionQtyTolerance || !isQuantityTradable(residualQty, spotPriceForCheck, spotRule) {
		return fmt.Errorf("现货已成交但无法安全回滚: spotQty=%.8f", spotExecutedQty)
	}
	_, err := p.engine.executeSpotRecoveryMarketProtocol(ctx, target.Candidate.Pair, direction.closeSpotSide(), residualQty, "srevert", direction.spotVenue(), "open_recovery", target.Candidate.Symbol, "revert_spot_after_futures_open_failure", fmt.Sprintf("direction=%s spotQty=%.8f residualQty=%.8f", direction, spotExecutedQty, residualQty))
	if err != nil {
		return fmt.Errorf("现货回滚失败: %w", err)
	}
	return nil
}

func (p *OpenProtocol) finalizeOpenHedge(ctx context.Context, target *MonitoredTarget, futureResult FuturesBatchOrderResult, futuresExecution OrderExecutionSnapshot, spotOrder SpotOrderResponse, spotPriceForCheck float64) (OpenHedge, error) {
	direction := resolvedFundingCarryDirection(target.Candidate.Direction, target.Candidate.EstimatedRate)
	filledFuturesQty := futuresExecution.ExecutedQty
	spotExecutedQty := resolveSpotNetBaseQuantity(spotOrder, target.Candidate.SpotBaseAsset)
	if spotExecutedQty <= executionQtyTolerance {
		return p.handleOpenSpotZeroFill(ctx, target, direction, filledFuturesQty)
	}

	spotRule := p.engine.spotRules[target.Candidate.Pair]
	finalSpotQty, additionalSpotFeeUSDT, spotRebalanceResult, err := p.rebalanceOpenSpotExcess(ctx, target, direction, spotExecutedQty, filledFuturesQty, spotPriceForCheck, spotRule)
	if err != nil {
		return OpenHedge{}, err
	}

	hedgeQty, additionalFuturesFeeUSDT, futuresRebalanceResult, err := p.rebalanceOpenFuturesResidual(ctx, target, direction, finalSpotQty, filledFuturesQty)
	if err != nil {
		return OpenHedge{}, err
	}
	if hedgeQty <= executionQtyTolerance {
		return OpenHedge{}, fmt.Errorf("对冲后有效数量为 0")
	}

	openSpotQuoteQty := parseFloat(spotOrder.CummulativeQuoteQty)
	if spotExecutedQty > executionQtyTolerance && hedgeQty+executionQtyTolerance < spotExecutedQty {
		openSpotQuoteQty *= hedgeQty / spotExecutedQty
	}
	lifecycleReason := "open orders filled; awaiting post-open validation"
	if detail := openLifecycleReason("", spotRebalanceResult, futuresRebalanceResult); detail != "" {
		lifecycleReason += " | " + detail
	}

	recoveryFrictionUSDT := 0.0
	if spotRebalanceResult.Successful {
		recoveryFrictionUSDT += spotRebalanceResult.Action.RecoveryFrictionUSDT
	}
	if futuresRebalanceResult.Successful {
		recoveryFrictionUSDT += futuresRebalanceResult.Action.RecoveryFrictionUSDT
	}

	return OpenHedge{
		Target:               target.Candidate,
		FuturesOrder:         futureResult,
		SpotOrder:            spotOrder,
		LifecycleState:       OpenHedgeLifecyclePendingValidation,
		LifecycleReason:      lifecycleReason,
		LifecycleUpdatedAt:   time.Now().UTC(),
		Direction:            direction,
		SpotBaseQty:          formatFloat(hedgeQty),
		HedgeQty:             hedgeQty,
		HedgeQtyText:         formatFloat(hedgeQty),
		OpenSpotQuoteQty:     openSpotQuoteQty,
		OpenSpotFeeUSDT:      p.engine.estimateSpotCommissionUSDT(ctx, spotOrder) + additionalSpotFeeUSDT,
		OpenFuturesAvgPrice:  futuresExecution.AvgPrice,
		OpenFuturesFeeUSDT:   p.engine.estimateFuturesExecutionFeeUSDT(ctx, futuresExecution) + additionalFuturesFeeUSDT,
		OpenSlippageUSDT:     calculateOpenSlippage(target.Candidate, direction, hedgeQty, futuresExecution.AvgPrice, openSpotQuoteQty/hedgeQty),
		RecoveryFrictionUSDT: recoveryFrictionUSDT,
	}, nil
}

func calculateOpenSlippage(candidate FundingCandidate, direction fundingCarryDirection, hedgeQty, futuresAvgPrice, spotAvgPrice float64) float64 {
	// 期望目标价(即执行前计算的名义资金依据价，比如现货VWAP与合约VWAP)
	// 因为此处缺乏严格快照时的目标价，我们取MarkPrice近似，或者理想状态应该取评估时的VWAP
	// 为了简化并符合 cost_accounting.go, 假设 targetPrice 是从 candidate 里的 MarkPrice
	spotTarget := candidate.MarkPrice
	futuresTarget := candidate.MarkPrice

	spotSlippage := CalculateSlippage(spotTarget, spotAvgPrice, hedgeQty, direction.openSpotSide() == "BUY")
	futuresSlippage := CalculateSlippage(futuresTarget, futuresAvgPrice, hedgeQty, direction.openFuturesSide() == "BUY")
	return spotSlippage + futuresSlippage
}

func (p *OpenProtocol) handleOpenSpotZeroFill(ctx context.Context, target *MonitoredTarget, direction fundingCarryDirection, filledFuturesQty float64) (OpenHedge, error) {
	result, err := p.engine.executeOpenFuturesRevertProtocol(ctx, target.Candidate.Symbol, filledFuturesQty, direction, "close_futures_after_zero_spot_fill", fmt.Sprintf("filledFuturesQty=%.8f", filledFuturesQty))
	if err != nil {
		return OpenHedge{}, fmt.Errorf("现货买单无有效成交且合约回补失败: %w", err)
	}
	return OpenHedge{}, result.openError("现货买单无有效成交，已回补合约空单", nil)
}

func (p *OpenProtocol) rebalanceOpenSpotExcess(ctx context.Context, target *MonitoredTarget, direction fundingCarryDirection, spotExecutedQty float64, filledFuturesQty float64, spotPriceForCheck float64, spotRule SymbolTradeRules) (float64, float64, RecoveryProtocolResult, error) {
	finalSpotQty := spotExecutedQty
	additionalSpotFeeUSDT := 0.0
	if spotResidualQty := finalSpotQty - filledFuturesQty; spotResidualQty > executionQtyTolerance {
		residualQty := normalizeQuantity(spotResidualQty, spotRule, target.Candidate.SpotBasePrecision)
		if residualQty > executionQtyTolerance && isQuantityTradable(residualQty, spotPriceForCheck, spotRule) {
			recoveryResult, err := p.engine.executeSpotRecoveryChasingProtocol(ctx, target.Candidate.Pair, direction.closeSpotSide(), residualQty, "srebalance", direction.spotVenue(), "open_recovery", target.Candidate.Symbol, "rebalance_spot_excess", fmt.Sprintf("direction=%s spotQty=%.8f residualQty=%.8f", direction, spotExecutedQty, residualQty))
			if err != nil {
				return 0, 0, RecoveryProtocolResult{}, fmt.Errorf("现货超配后回滚失败: %w", err)
			}
			revertedQty := resolveSpotNetBaseQuantity(recoveryResult.Action.SpotOrder, target.Candidate.SpotBaseAsset)
			finalSpotQty = math.Max(0, finalSpotQty-revertedQty)
			additionalSpotFeeUSDT += recoveryResult.spotFeeUSDT(p.engine, ctx)
			recoveryResult.ProtocolResult.Summary = fmt.Sprintf("spot excess rebalanced: executed=%.8f reverted=%.8f final=%.8f", spotExecutedQty, revertedQty, finalSpotQty)
			recoveryResult.Action.Summary = recoveryResult.ProtocolResult.Summary
			return finalSpotQty, additionalSpotFeeUSDT, recoveryResult, nil
		} else if residualQty > executionQtyTolerance {
			if err := p.engine.noteDustResidual("open_recovery", target.Candidate.Symbol, target.Candidate.Pair, target.Candidate.SpotBaseAsset, residualQty, residualQty*maxFloat(spotPriceForCheck, target.Candidate.MarkPrice), "spot_excess_below_min_notional"); err != nil {
				p.engine.logger.Printf("记录现货超买灰尘残差失败: %v", err)
			}
			summary := fmt.Sprintf("spot excess recorded as dust: residual=%.8f", residualQty)
			return finalSpotQty, additionalSpotFeeUSDT, RecoveryProtocolResult{
				ProtocolResult: ProtocolResult{Protocol: "recovery.open_spot_excess_dust", Successful: true, Summary: summary},
				Action:         recoveryActionResult{ProtocolAction: "spot_excess_dust_recorded", Summary: summary},
			}, nil
		}
	}
	return finalSpotQty, additionalSpotFeeUSDT, RecoveryProtocolResult{}, nil
}

func (p *OpenProtocol) rebalanceOpenFuturesResidual(ctx context.Context, target *MonitoredTarget, direction fundingCarryDirection, finalSpotQty float64, filledFuturesQty float64) (float64, float64, RecoveryProtocolResult, error) {
	hedgeQty := math.Min(filledFuturesQty, finalSpotQty)
	additionalFuturesFeeUSDT := 0.0
	if futuresResidualQty := filledFuturesQty - hedgeQty; futuresResidualQty > executionQtyTolerance {
		recoveryResult, err := p.engine.executeFuturesRecoveryCloseProtocol(ctx, target.Candidate.Symbol, futuresResidualQty, direction, "open_recovery", "close_futures_residual", fmt.Sprintf("futuresResidualQty=%.8f hedgeQty=%.8f", futuresResidualQty, hedgeQty))
		if err != nil {
			return 0, 0, RecoveryProtocolResult{}, fmt.Errorf("修正合约裸腿失败: %w", err)
		}
		additionalFuturesFeeUSDT += recoveryResult.futuresFeeUSDT(p.engine, ctx)
		recoveryResult.ProtocolResult.Summary = fmt.Sprintf("futures residual closed: residual=%.8f hedge=%.8f", futuresResidualQty, hedgeQty)
		recoveryResult.Action.Summary = recoveryResult.ProtocolResult.Summary
		return hedgeQty, additionalFuturesFeeUSDT, recoveryResult, nil
	}
	return hedgeQty, additionalFuturesFeeUSDT, RecoveryProtocolResult{}, nil
}

func (p *OpenProtocol) isFreshOpenHedgeSafe(hedge *OpenHedge, adverseSpread float64) bool {
	if hedge == nil {
		return false
	}
	econGate := p.engine.economicParams()
	immediateNetProfit := estimateReservedNetProfitRate(
		hedge.Target.EstimatedRate,
		adverseSpread,
		econGate.FeeReserveRate,
		econGate.ExitSpreadReserveRate,
		p.engine.liveBorrowReserveRate(hedge.Target, hedge.LastKnownSpotPrice(), p.engine.serverNow()),
	)
	return adverseSpread <= econGate.ExitSpreadReserveRate && immediateNetProfit > p.engine.cfg.RiskConfig.ImmediateInvalidateRate
}
