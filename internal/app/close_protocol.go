package app

import (
	"context"
	"fmt"
	"lingxi/internal/lifecycle"
	"lingxi/internal/money"
	"math"
	"strings"
	"sync"
	"time"
)

// CloseProtocol 封装平仓生命周期，并作为平仓主流程的协议入口。
type CloseProtocol struct {
	engine *FundingRateSniperEngine
}

func newCloseProtocol(engine *FundingRateSniperEngine) *CloseProtocol {
	if engine == nil {
		return nil
	}
	return &CloseProtocol{engine: engine}
}

func (e *FundingRateSniperEngine) ensureCloseProtocol() *CloseProtocol {
	if e == nil {
		return nil
	}
	e.closeProtocolOnce.Do(func() {
		e.closeProtocol = newCloseProtocol(e)
	})
	return e.closeProtocol
}

func (e *FundingRateSniperEngine) executeCloseSequence(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot) (CycleSettlement, error) {
	protocol := e.ensureCloseProtocol()
	if protocol == nil {
		return CycleSettlement{}, nil
	}
	result, err := protocol.ExecuteSequence(ctx, hedges, beforeSnapshot, nil, nil)
	return result.Settlement, err
}

// reconcileResidentPositionsAfterClose 平仓执行后把内存常驻态(activeOpenHedges + 持仓标签)收敛到 retained。
// retained 为空(单周期全平 / 无续持)→ setCurrentPositions(nil),与既有行为逐字节一致;
// 非空(滚动续持 keep 仓)→ setCurrentPositionsFromHedges 原子收敛到 keep,无「先清空再重建」窗口(方案 A)。
func (e *FundingRateSniperEngine) reconcileResidentPositionsAfterClose(retained []OpenHedge) {
	if len(retained) == 0 {
		e.setCurrentPositions(nil)
		return
	}
	e.setCurrentPositionsFromHedges(retained)
}

// reconcileResidentLedgerAfterClose 平仓后收敛本地未平仓台账到 retained。
// retained 为空 → 沿用既有「确认交易所无残留(canClear)则清空台账」语义(逐字节不变);
// 非空(滚动续持)→ 把台账收敛到 keep 仓,使台账任意时刻与交易所持仓一致(崩溃恢复 SSOT)。
func (e *FundingRateSniperEngine) reconcileResidentLedgerAfterClose(afterSnapshot CycleAccountSnapshot, closedHedges, retained []OpenHedge) error {
	if len(retained) == 0 {
		if e.canClearPersistedOpenHedges(afterSnapshot, closedHedges) {
			return e.clearPersistedOpenHedges()
		}
		return nil
	}
	return e.syncPersistedOpenHedges(retained)
}

// ExecuteSequence 执行平仓。retained 为本次平仓后仍需常驻保留的持仓(滚动续持 keep ∪ 未到期);单周期全平传 nil。
// fundingEligibleResidents 为被平的常驻 symbol(drop):其开仓前持仓≠0 但本周期资金费应正常归因,放行避免误跳过;
// 单周期全平传 nil。
func (p *CloseProtocol) ExecuteSequence(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, retained []OpenHedge, fundingEligibleResidents map[string]struct{}) (CloseProtocolExecutionResult, error) {
	settlement, err := p.executeSequence(ctx, hedges, beforeSnapshot, retained, fundingEligibleResidents)
	return CloseProtocolExecutionResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "close.execute",
			Successful:   err == nil,
			Summary:      "close sequence executed",
			CurrentStage: LifecycleStageClosing,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStageSettling,
				Reason:    "close sequence executed",
			},
			Risk: RiskDecision{
				Action:      RiskActionObserve,
				Category:    "close_execute",
				Severity:    RiskSeverityLevelInfo,
				Reason:      "close sequence executed",
				CanContinue: err == nil,
			},
			Events: newProtocolEvents(p.engine, "close.execute", err == nil,
				protocolEventSpec{
					EventType: EventTypeCloseExecuted,
					Level:     EventLevelInfo,
					Stage:     LifecycleStageClosing,
					Reason:    "close sequence executed",
				},
			),
		},
		Settlement: settlement,
	}, err
}

func (p *CloseProtocol) CloseHedgePosition(ctx context.Context, hedge *OpenHedge, futureResult FuturesBatchOrderResult, preferPassiveSpot bool) ([]string, error) {
	return p.closeHedgePosition(ctx, hedge, futureResult, preferPassiveSpot)
}

func (p *CloseProtocol) CloseHedgePositionParallel(ctx context.Context, plan closeSequencePlan) ([]string, error) {
	return p.closeHedgePositionParallel(ctx, plan)
}

func (p *CloseProtocol) ExecuteSequentialClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	return p.executeSequentialPlans(ctx, plans)
}

func (p *CloseProtocol) ExecuteParallelClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	return p.executeParallelPlans(ctx, plans)
}

func (p *CloseProtocol) escalateCloseUnknown(ctx context.Context, hedge *OpenHedge, alerts []string, task exposureRescueTask, failureMessage string, scheduledMessage string) ([]string, error) {
	result, err := p.engine.scheduleExposureRescueProtocolResult(ctx, task, failureMessage, scheduledMessage)
	if err != nil {
		return alerts, err
	}
	result.applyCloseProtocol(hedge, scheduledMessage)
	return alerts, fmt.Errorf("%s", result.lifecycleReason(scheduledMessage))
}

func (p *CloseProtocol) recoverCloseByReopeningFutures(ctx context.Context, hedge *OpenHedge, alerts []string, quantity float64, direction fundingCarryDirection, action string, detail string, successAlert string, failureFormat string, failureArgs ...any) ([]string, error) {
	recoveryExecution, err := p.engine.executeFuturesRecoveryReopenProtocol(ctx, hedge.Target.Symbol, quantity, direction, "close_recovery", action, detail)
	if err != nil {
		args := append(failureArgs, err)
		return alerts, fmt.Errorf(failureFormat, args...)
	}
	hedge.CloseFuturesFeeUSDT += recoveryExecution.Action.futuresFeeUSDT(p.engine, ctx)
	hedge.RecoveryFrictionUSDT += recoveryExecution.Action.RecoveryFrictionUSDT
	if trimmed := strings.TrimSpace(successAlert); trimmed != "" {
		alerts = append(alerts, trimmed)
	}
	recoveryExecution.applyCloseProtocol(hedge, strings.Join(alerts, " | "))
	return alerts, nil
}

func (p *CloseProtocol) recoverCloseByRevertingSpot(ctx context.Context, hedge *OpenHedge, spotOrder SpotOrderResponse, spotReferencePrice float64, failureFormat string) error {
	if err := p.engine.revertSpotCloseAfterFuturesFailure(ctx, hedge, spotOrder, spotReferencePrice); err != nil {
		return fmt.Errorf(failureFormat, err)
	}
	return nil
}

func (p *CloseProtocol) rebalanceParallelSpotExcess(ctx context.Context, hedge *OpenHedge, soldSpotQty float64, effectiveCloseQty float64, spotReferencePrice float64, direction fundingCarryDirection) error {
	residualQty := normalizeQuantity(soldSpotQty-effectiveCloseQty, p.engine.spotRules[hedge.Target.Pair], hedge.Target.SpotBasePrecision)
	if residualQty <= executionQtyTolerance || !isQuantityTradable(residualQty, spotReferencePrice, p.engine.spotRules[hedge.Target.Pair]) {
		return nil
	}
	recoveryOrder, err := p.engine.executeSpotRecoveryMarketProtocol(
		ctx,
		hedge.Target.Pair,
		direction.openSpotSide(),
		residualQty,
		"sclose_revert",
		direction.spotVenue(),
		"close_recovery",
		hedge.Target.Symbol,
		"rebalance_spot_excess_after_parallel_close",
		fmt.Sprintf("soldSpotQty=%.8f effectiveCloseQty=%.8f residualQty=%.8f", soldSpotQty, effectiveCloseQty, residualQty),
	)
	if err != nil {
		return fmt.Errorf("并发现货超卖后回补失败: %w", err)
	}
	hedge.CloseSpotFeeUSDT += recoveryOrder.Action.spotFeeUSDT(p.engine, ctx)
	hedge.RecoveryFrictionUSDT += recoveryOrder.Action.RecoveryFrictionUSDT
	if soldSpotQty > executionQtyTolerance {
		hedge.CloseSpotQuoteQty *= effectiveCloseQty / soldSpotQty
	}
	return nil
}

func (e *FundingRateSniperEngine) closeHedgePosition(ctx context.Context, hedge *OpenHedge, futureResult FuturesBatchOrderResult, preferPassiveSpot bool) ([]string, error) {
	protocol := e.ensureCloseProtocol()
	if protocol == nil {
		return nil, fmt.Errorf("close protocol 未初始化")
	}
	return protocol.CloseHedgePosition(ctx, hedge, futureResult, preferPassiveSpot)
}

func (e *FundingRateSniperEngine) closeHedgePositionParallel(ctx context.Context, plan closeSequencePlan) ([]string, error) {
	protocol := e.ensureCloseProtocol()
	if protocol == nil {
		return nil, fmt.Errorf("close protocol 未初始化")
	}
	return protocol.CloseHedgePositionParallel(ctx, plan)
}

func (p *CloseProtocol) executeSequence(ctx context.Context, hedges []OpenHedge, beforeSnapshot CycleAccountSnapshot, retained []OpenHedge, fundingEligibleResidents map[string]struct{}) (CycleSettlement, error) {
	if len(hedges) == 0 {
		return CycleSettlement{}, nil
	}
	// 平仓是「必须拿到价」的收敛动作：标记 flatten 意图，使下游取价在 WS 缓存陈旧时回退 REST 现拉，
	// 而非直接报「bookTicker 已失效」放弃（避免薄盘币裸腿平不掉）。覆盖结算平仓 / 滚动持有 drop 平仓。
	ctx = withFlattenIntent(ctx)

	fundingTime := earliestFundingTimeFromHedges(hedges)
	closeAt, monitorAlerts, preferPassiveSpotClose, err := p.engine.waitForCloseExecutionWindow(ctx, hedges, fundingTime)
	if err != nil {
		return CycleSettlement{}, err
	}

	plans := make([]closeSequencePlan, 0, len(hedges))
	for idx := range hedges {
		plans = append(plans, closeSequencePlan{
			hedge:             &hedges[idx],
			futuresOrder:      p.engine.buildProtectedCloseFuturesOrder(ctx, hedges[idx]),
			preferPassiveSpot: preferPassiveSpotClose,
		})
	}
	sequentialPlans, parallelPlans := splitCloseSequencePlans(p.engine.cfg, plans)
	p.engine.runtimeReporter.EmitWithReason(StageClose, len(hedges), fmt.Sprintf("开始平仓执行，顺序池=%d，并发池=%d", len(sequentialPlans), len(parallelPlans)))
	p.engine.logger.Printf("模块五开始，实际平仓时间=%s，顺序池=%d，并发池=%d", closeAt.Format("15:04:05"), len(sequentialPlans), len(parallelPlans))

	var (
		wg               sync.WaitGroup
		sequentialErr    error
		parallelErr      error
		sequentialAlerts []string
		parallelAlerts   []string
	)
	if len(sequentialPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sequentialAlerts, sequentialErr = p.executeSequentialPlans(ctx, sequentialPlans)
		}()
	}
	if len(parallelPlans) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parallelAlerts, parallelErr = p.executeParallelPlans(ctx, parallelPlans)
		}()
	}
	wg.Wait()
	if sequentialErr != nil {
		return CycleSettlement{}, sequentialErr
	}
	if parallelErr != nil {
		return CycleSettlement{}, parallelErr
	}
	p.engine.reconcileResidentPositionsAfterClose(retained)

	afterSnapshot, err := p.engine.captureCycleAccountSnapshotFromHedges(ctx, hedges)
	if err != nil {
		return CycleSettlement{}, err
	}

	// 交易盈亏 / 手续费 / 借币成本以 money.Micros 定点累加，消除多笔 float64 连加的舍入漂移，
	// 并与回测汇总(historical_summary)同口径，保证落袋净利逐位可对账。
	var spotTradingPnLM, futuresTradingPnLM, totalFeesM, totalBorrowCostsM money.Micros
	borrowCostCloseTime := time.Now().UTC()
	expectedFundingIncome, fundingAlerts := p.engine.expectedFundingIncomeSummary(hedges, beforeSnapshot, fundingEligibleResidents)
	attributions := make([]SettlementAttribution, 0, len(hedges))
	for _, hedge := range hedges {
		direction := hedge.carryDirection()
		closedSpotQty := math.Min(hedge.HedgeQty, hedge.CloseSpotExecutedQty)
		spotPnL, futuresPnL := 0.0, 0.0
		if closedSpotQty > 0 {
			openSpotCost := hedge.OpenSpotQuoteQty * (closedSpotQty / hedge.HedgeQty)
			spotEntryPrice := openSpotCost / closedSpotQty
			spotExitPrice := hedge.CloseSpotQuoteQty / closedSpotQty
			spotPnL = direction.spotGrossPnL(closedSpotQty, spotEntryPrice, spotExitPrice)
			spotTradingPnLM = spotTradingPnLM.Add(money.FromUSDT(spotPnL))
		}
		hedgeFees := hedge.OpenSpotFeeUSDT + hedge.CloseSpotFeeUSDT + hedge.OpenFuturesFeeUSDT + hedge.CloseFuturesFeeUSDT
		totalFeesM = totalFeesM.Add(money.FromUSDT(hedgeFees))

		if hedge.CloseFuturesRealizedPnL != 0 {
			futuresPnL = hedge.CloseFuturesRealizedPnL
			futuresTradingPnLM = futuresTradingPnLM.Add(money.FromUSDT(futuresPnL))
		} else if hedge.OpenFuturesAvgPrice > 0 && hedge.CloseFuturesAvgPrice > 0 {
			closedFuturesQty := hedge.CloseFuturesExecutedQty
			if closedFuturesQty <= 0 {
				closedFuturesQty = hedge.HedgeQty
			}
			futuresPnL = direction.futuresGrossPnL(closedFuturesQty, hedge.OpenFuturesAvgPrice, hedge.CloseFuturesAvgPrice)
			futuresTradingPnLM = futuresTradingPnLM.Add(money.FromUSDT(futuresPnL))
		}

		borrowCost := p.engine.settlementBorrowCost(hedge, borrowCostCloseTime)
		totalBorrowCostsM = totalBorrowCostsM.Add(money.FromUSDT(borrowCost))

		attributions = append(attributions, SettlementAttribution{
			Symbol:           hedge.Target.Symbol,
			FundingIncome:    0, // 异步更新
			SpotFees:         hedge.OpenSpotFeeUSDT + hedge.CloseSpotFeeUSDT,
			FuturesFees:      hedge.OpenFuturesFeeUSDT + hedge.CloseFuturesFeeUSDT,
			BorrowCosts:      borrowCost,
			OpenSlippage:     hedge.OpenSlippageUSDT,
			CloseSlippage:    hedge.CloseSlippageUSDT,
			RecoveryFriction: hedge.RecoveryFrictionUSDT,
			NetPnL:           money.Sum(spotPnL, futuresPnL, -hedgeFees, -borrowCost).USDT(),
		})
	}

	spotTradingPnL := spotTradingPnLM.USDT()
	futuresTradingPnL := futuresTradingPnLM.USDT()
	totalFees := totalFeesM.USDT()
	totalBorrowCosts := totalBorrowCostsM.USDT()
	residualSpotValue := calculateResidualSpotValue(afterSnapshot.SpotAssetBalances, hedges)
	spotUSDTDelta := afterSnapshot.SpotUSDTFree - beforeSnapshot.SpotUSDTFree
	futuresUSDTDelta := afterSnapshot.FuturesUSDTWallet - beforeSnapshot.FuturesUSDTWallet
	totalFundingIncome := 0.0
	netPnL := spotTradingPnLM.Add(futuresTradingPnLM).Sub(totalFeesM).Sub(totalBorrowCostsM).USDT()
	fundingIncomeDiff := totalFundingIncome - expectedFundingIncome
	snapshotNetPnL := spotUSDTDelta + futuresUSDTDelta + residualSpotValue
	alerts := p.engine.buildSettlementAlerts(afterSnapshot, hedges, residualSpotValue, netPnL, snapshotNetPnL)
	alerts = append(alerts, fundingAlerts...)
	alerts = append(alerts, fmt.Sprintf("资金费异步结算待处理，预计在 %s 后更新实收流水", p.engine.effectiveFundingSettlementDelay().Round(time.Second)))
	alerts = append(alerts, monitorAlerts...)
	execAlerts := append(sequentialAlerts, parallelAlerts...)
	if len(execAlerts) > 0 {
		p.engine.logger.Printf("模块五对账警告: %s", strings.Join(execAlerts, " | "))
		alerts = append(alerts, execAlerts...)
	}
	if ledgerErr := p.engine.reconcileResidentLedgerAfterClose(afterSnapshot, hedges, retained); ledgerErr != nil {
		return CycleSettlement{}, ledgerErr
	}

	settlement := CycleSettlement{
		TraceRecordMetadata:   p.engine.recordMetadata(),
		RunID:                 p.engine.runID,
		CycleID:               p.engine.cycleIDSnapshot(),
		Targets:               len(hedges),
		FundingIncome:         totalFundingIncome,
		ExpectedFundingIncome: expectedFundingIncome,
		FundingIncomeDiff:     fundingIncomeDiff,
		TradingPnL:            spotTradingPnLM.Add(futuresTradingPnLM).USDT(),
		SpotTradingPnL:        spotTradingPnL,
		FuturesTradingPnL:     futuresTradingPnL,
		TotalFees:             totalFees,
		TotalBorrowCosts:      totalBorrowCosts,
		NetPnL:                netPnL,
		SnapshotNetPnL:        snapshotNetPnL,
		ResidualSpotValue:     residualSpotValue,
		SpotUSDTDelta:         spotUSDTDelta,
		FuturesUSDTDelta:      futuresUSDTDelta,
		CloseExecutedAt:       closeAt,
		FundingTimePoint:      fundingTime,
		BeforeSnapshot:        beforeSnapshot,
		AfterSnapshot:         afterSnapshot,
		FundingIncomePending:  true,
		HasAlert:              len(alerts) > 0,
		Alerts:                alerts,
		Attributions:          attributions,
	}
	settlement.ExpectedTotalFees, settlement.CycleNotionalUSDT = p.engine.expectedHedgeCostObservation(hedges)
	settlement.applyFrictionObservation()
	p.engine.logFrictionObservation(settlement, "close_presettle")
	// 真实扣息观测（旁路、goroutine 零延迟、绝不影响净利/风控）：拉交易所真实扣息与模型借币成本对照，
	// 便于实盘后用真实数据校准 AssumedHourlyBorrowInterestRate。仅负 carry 触发，正 carry 内部直接返回。
	interestParent := context.Background()
	if p.engine.runCtx != nil {
		interestParent = p.engine.runCtx
	}
	hedgesForInterest := append([]OpenHedge(nil), hedges...)
	go p.engine.observeActualBorrowInterest(interestParent, hedgesForInterest, settlement)
	return settlement, nil
}

func (p *CloseProtocol) closeHedgePosition(ctx context.Context, hedge *OpenHedge, futureResult FuturesBatchOrderResult, preferPassiveSpot bool) ([]string, error) {
	ctx = withFlattenIntent(ctx) // 平仓取价允许 WS 陈旧时回退 REST，避免裸腿平不掉（覆盖 abort 平仓等直连入口）
	alerts := make([]string, 0, 2)
	if err := p.engine.guardHedgeOrderIntent(hedge, lifecycle.IntentCloseLeg); err != nil {
		return alerts, err
	}
	hedge.markClosing("close_sequence_started")
	direction := hedge.carryDirection()
	if err := p.engine.ensureSymbolNotLockedByExposure(hedge.Target.Symbol, "close_hedge"); err != nil {
		return alerts, err
	}
	futuresExecution, resolveErr := p.engine.resolveFuturesOrderExecution(ctx, hedge.Target.Symbol, futureResult.ClientOrderID, futureResult)
	if resolveErr != nil {
		if isUnknownOrderStateError(resolveErr) {
			return p.escalateCloseUnknown(ctx, hedge, alerts, exposureRescueTask{
				Mode:                 exposureRescueModeClose,
				Phase:                "close_futures_unknown",
				Direction:            direction,
				Symbol:               hedge.Target.Symbol,
				Pair:                 hedge.Target.Pair,
				SpotBaseAsset:        hedge.Target.SpotBaseAsset,
				SpotBasePrecision:    hedge.Target.SpotBasePrecision,
				SpotReferencePrice:   hedge.LastKnownSpotPrice(),
				InitialHedgeQty:      hedge.HedgeQty,
				FuturesClientOrderID: futureResult.ClientOrderID,
			}, "合约平仓终态未知且 ExposureManager 托管失败", "合约平仓终态未知，已转入 ExposureManager 托管")
		}
		return alerts, fmt.Errorf("合约平仓未确认成交: %w", resolveErr)
	}
	if futuresExecution.ExecutedQty <= executionQtyTolerance {
		return alerts, fmt.Errorf("合约平仓成交数量为 0")
	}

	hedge.CloseFuturesExecutedQty = math.Min(hedge.HedgeQty, futuresExecution.ExecutedQty)
	closeableSpotQty := hedge.CloseFuturesExecutedQty
	if closeableSpotQty <= executionQtyTolerance {
		hedge.completeCloseLifecycle(false, "closeable spot qty is zero")
		return alerts, nil
	}
	if futuresExecution.ExecutedQty+executionQtyTolerance < hedge.HedgeQty {
		alerts = append(alerts, fmt.Sprintf("合约平仓部分成交 %s: requested=%.8f executed=%.8f", hedge.Target.Symbol, hedge.HedgeQty, futuresExecution.ExecutedQty))
	}
	hedge.CloseFuturesAvgPrice = futuresExecution.AvgPrice
	hedge.CloseFuturesFeeUSDT = p.engine.estimateFuturesExecutionFeeUSDT(ctx, futuresExecution)
	hedge.CloseFuturesRealizedPnL = futuresExecution.RealizedPnL

	spotRule := p.engine.spotRules[hedge.Target.Pair]
	spotQty := normalizeQuantity(closeableSpotQty, spotRule, hedge.Target.SpotBasePrecision)
	spotReferencePrice := hedge.LastKnownSpotPrice()
	if !isQuantityTradable(spotQty, spotReferencePrice, spotRule) {
		if spotQty > executionQtyTolerance {
			if err := p.engine.noteDustResidual("close_recovery", hedge.Target.Symbol, hedge.Target.Pair, hedge.Target.SpotBaseAsset, spotQty, spotQty*math.Max(spotReferencePrice, hedge.Target.MarkPrice), "close_spot_below_min_notional"); err != nil {
				p.engine.logger.Printf("记录现货平仓灰尘残差失败: %v", err)
			}
		}
		if closeableSpotQty*math.Max(spotReferencePrice, hedge.Target.MarkPrice) >= p.engine.cfg.ResidualSpotAlertUSDT {
			return p.recoverCloseByReopeningFutures(
				ctx,
				hedge,
				alerts,
				closeableSpotQty,
				direction,
				"reopen_short_after_untradable_spot_close",
				fmt.Sprintf("closeableSpotQty=%.8f spotQty=%.8f", closeableSpotQty, spotQty),
				fmt.Sprintf("现货平仓数量不足 %s: qty=%.8f，已恢复空头对冲", hedge.Target.Pair, spotQty),
				"现货平仓数量不足且恢复空头失败: %w",
			)
		}
		alerts = append(alerts, fmt.Sprintf("现货平仓数量不足 %s: qty=%.8f", hedge.Target.Pair, spotQty))
		hedge.completeCloseLifecycle(false, strings.Join(alerts, " | "))
		return alerts, nil
	}

	clientOrderID := newClientOrderID("sclose", hedge.Target.Pair)
	var (
		spotOrder SpotOrderResponse
		spotErr   error
	)
	spotCloseSide := direction.closeSpotSide()
	if preferPassiveSpot && direction.spotVenue() == spotExecutionVenueSpot && spotCloseSide == "SELL" {
		spotOrder, spotErr = p.engine.placeSpotPassiveMakerClose(ctx, hedge.Target.Pair, spotQty, "sclosemaker")
		spotView := spotOrder.SemanticView()
		if spotErr == nil && spotView.ExecutedQty+executionQtyTolerance < spotQty {
			chaseOrder, chaseErr := p.engine.placeDirectionalSpotChasingOrderWithOptions(ctx, hedge.Target.Pair, spotCloseSide, maxFloat(spotQty-spotView.ExecutedQty, 0), "sclosechase", spotChaseOptions{}, direction.spotVenue())
			if chaseErr != nil {
				spotErr = chaseErr
			} else {
				spotOrder = mergeSpotOrderResponses(spotOrder, chaseOrder)
			}
		}
	} else {
		spotOrder, spotErr = p.engine.placeDirectionalSpotMarketOrderIdempotent(ctx, hedge.Target.Pair, spotCloseSide, spotQty, clientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(spotCloseSide), true)
	}
	if spotErr != nil {
		if isUnknownOrderStateError(spotErr) {
			return p.escalateCloseUnknown(ctx, hedge, alerts, exposureRescueTask{
				Mode:                    exposureRescueModeClose,
				Phase:                   "close_spot_unknown",
				Direction:               direction,
				Symbol:                  hedge.Target.Symbol,
				Pair:                    hedge.Target.Pair,
				SpotBaseAsset:           hedge.Target.SpotBaseAsset,
				SpotBasePrecision:       hedge.Target.SpotBasePrecision,
				SpotReferencePrice:      spotReferencePrice,
				InitialHedgeQty:         hedge.HedgeQty,
				SpotClientOrderID:       clientOrderID,
				KnownFuturesExecutedQty: closeableSpotQty,
			}, "现货平仓终态未知且 ExposureManager 托管失败", "现货平仓终态未知，已转入 ExposureManager 托管")
		}
		return p.recoverCloseByReopeningFutures(
			ctx,
			hedge,
			alerts,
			closeableSpotQty,
			direction,
			"reopen_short_after_spot_close_failure",
			fmt.Sprintf("closeableSpotQty=%.8f spotErr=%v", closeableSpotQty, spotErr),
			fmt.Sprintf("现货平仓失败 %s: %v，已恢复空头对冲", hedge.Target.Pair, spotErr),
			"现货平仓失败且恢复空头失败: spotErr=%v recoverErr=%w",
			spotErr,
		)
	}

	hedge.CloseSpotExecutedQty = parseFloat(spotOrder.ExecutedQty)
	hedge.CloseSpotQuoteQty = parseFloat(spotOrder.CummulativeQuoteQty)
	hedge.CloseSpotFeeUSDT = p.engine.estimateSpotCommissionUSDT(ctx, spotOrder)
	if hedge.CloseSpotExecutedQty+executionQtyTolerance < closeableSpotQty {
		residualQty := closeableSpotQty - hedge.CloseSpotExecutedQty
		return p.recoverCloseByReopeningFutures(
			ctx,
			hedge,
			alerts,
			residualQty,
			direction,
			"reopen_short_after_partial_spot_close",
			fmt.Sprintf("closeableSpotQty=%.8f spotExecutedQty=%.8f residualQty=%.8f", closeableSpotQty, hedge.CloseSpotExecutedQty, residualQty),
			fmt.Sprintf("现货平仓部分成交 %s: requested=%.8f executed=%.8f，已恢复剩余空头", hedge.Target.Pair, closeableSpotQty, hedge.CloseSpotExecutedQty),
			"现货平仓部分成交且恢复空头失败: %w",
		)
	}
	hedge.completeCloseLifecycle(false, strings.Join(alerts, " | "))
	return alerts, nil
}

func (p *CloseProtocol) closeHedgePositionParallel(ctx context.Context, plan closeSequencePlan) ([]string, error) {
	ctx = withFlattenIntent(ctx) // 平仓取价允许 WS 陈旧时回退 REST，避免裸腿平不掉（覆盖 panic-flatten / abort 并发平仓）
	hedge := plan.hedge
	if err := p.engine.guardHedgeOrderIntent(hedge, lifecycle.IntentCloseLeg); err != nil {
		return nil, err
	}
	hedge.markClosing("parallel_close_sequence_started")
	direction := hedge.carryDirection()
	spotQty, spotReferencePrice, spotTradable := closeSpotTarget(hedge, hedge.HedgeQty, p.engine.spotRules[hedge.Target.Pair])
	if !spotTradable {
		futuresExecution, err := p.engine.placeSingleFuturesOrderIdempotent(ctx, plan.futuresOrder, "futures_close_single")
		if err != nil {
			return nil, fmt.Errorf("并发平仓回退到单腿模式时，合约平仓失败: %w", err)
		}
		return p.closeHedgePosition(ctx, hedge, futuresExecutionToResult(futuresExecution, plan.futuresOrder), plan.preferPassiveSpot)
	}

	type spotCloseResult struct {
		order SpotOrderResponse
		err   error
	}
	type futuresCloseResult struct {
		snapshot OrderExecutionSnapshot
		err      error
	}
	spotClientOrderID := newClientOrderID("sclose", hedge.Target.Pair)

	spotResultCh := make(chan spotCloseResult, 1)
	futuresResultCh := make(chan futuresCloseResult, 1)
	go func() {
		var (
			order SpotOrderResponse
			err   error
		)
		spotCloseSide := direction.closeSpotSide()
		if plan.preferPassiveSpot && direction.spotVenue() == spotExecutionVenueSpot && spotCloseSide == "SELL" {
			order, err = p.engine.placeSpotPassiveMakerClose(ctx, hedge.Target.Pair, spotQty, "sclosemaker")
			orderView := order.SemanticView()
			if err == nil && orderView.ExecutedQty+executionQtyTolerance < spotQty {
				var chaseOrder SpotOrderResponse
				chaseOrder, err = p.engine.placeDirectionalSpotChasingOrderWithOptions(ctx, hedge.Target.Pair, spotCloseSide, maxFloat(spotQty-orderView.ExecutedQty, 0), "sclosechase", spotChaseOptions{}, direction.spotVenue())
				if err == nil {
					order = mergeSpotOrderResponses(order, chaseOrder)
				}
			}
		} else {
			order, err = p.engine.placeDirectionalSpotMarketOrderIdempotent(ctx, hedge.Target.Pair, spotCloseSide, spotQty, spotClientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(spotCloseSide), true)
		}
		spotResultCh <- spotCloseResult{order: order, err: err}
	}()
	go func() {
		snapshot, err := p.engine.placeSingleFuturesOrderIdempotent(ctx, plan.futuresOrder, "futures_close_single")
		futuresResultCh <- futuresCloseResult{snapshot: snapshot, err: err}
	}()

	spotResult := <-spotResultCh
	futuresResult := <-futuresResultCh
	filledFuturesQty := math.Min(hedge.HedgeQty, futuresResult.snapshot.ExecutedQty)
	if futuresResult.err != nil {
		if isUnknownOrderStateError(futuresResult.err) {
			return p.escalateCloseUnknown(ctx, hedge, nil, exposureRescueTask{
				Mode:                 exposureRescueModeClose,
				Phase:                "parallel_close_futures_unknown",
				Direction:            direction,
				Symbol:               hedge.Target.Symbol,
				Pair:                 hedge.Target.Pair,
				SpotBaseAsset:        hedge.Target.SpotBaseAsset,
				SpotBasePrecision:    hedge.Target.SpotBasePrecision,
				SpotReferencePrice:   spotReferencePrice,
				InitialHedgeQty:      hedge.HedgeQty,
				SpotClientOrderID:    spotClientOrderID,
				FuturesClientOrderID: plan.futuresOrder.NewClientOrderID,
			}, "并发合约平仓终态未知且 ExposureManager 托管失败", "并发合约平仓终态未知，已转入 ExposureManager 托管")
		}
		if spotResult.err == nil {
			if err := p.recoverCloseByRevertingSpot(ctx, hedge, spotResult.order, spotReferencePrice, "并发合约平仓失败且现货回补失败: %w"); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("并发合约平仓未确认成交: %w", futuresResult.err)
	}
	if filledFuturesQty <= executionQtyTolerance {
		if spotResult.err == nil {
			if err := p.recoverCloseByRevertingSpot(ctx, hedge, spotResult.order, spotReferencePrice, "并发合约零成交且现货回补失败: %w"); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("并发合约平仓成交数量为 0")
	}

	alerts := make([]string, 0, 2)
	if futuresResult.snapshot.ExecutedQty+executionQtyTolerance < hedge.HedgeQty {
		alerts = append(alerts, fmt.Sprintf("合约平仓部分成交 %s: requested=%.8f executed=%.8f", hedge.Target.Symbol, hedge.HedgeQty, futuresResult.snapshot.ExecutedQty))
	}
	hedge.CloseFuturesAvgPrice = futuresResult.snapshot.AvgPrice
	hedge.CloseFuturesFeeUSDT = p.engine.estimateFuturesExecutionFeeUSDT(ctx, futuresResult.snapshot)
	hedge.CloseFuturesRealizedPnL = futuresResult.snapshot.RealizedPnL

	// Track Close Slippage
	spotTarget := hedge.Target.MarkPrice
	futuresTarget := hedge.Target.MarkPrice
	spotAvgPrice := 0.0
	spotExecutedQty := 0.0
	if spotResult.order.CummulativeQuoteQty != "" {
		spotExecutedQty = resolveSpotNetBaseQuantity(spotResult.order, hedge.Target.SpotBaseAsset)
		quoteQty := parseFloat(spotResult.order.CummulativeQuoteQty)
		if spotExecutedQty > 0 {
			spotAvgPrice = quoteQty / spotExecutedQty
		}
	}
	spotSlippage := CalculateSlippage(spotTarget, spotAvgPrice, spotExecutedQty, direction.closeSpotSide() == "BUY")
	futuresSlippage := CalculateSlippage(futuresTarget, futuresResult.snapshot.AvgPrice, filledFuturesQty, direction.closeFuturesSide() == "BUY")
	hedge.CloseSlippageUSDT = spotSlippage + futuresSlippage

	if spotResult.err != nil {
		if isUnknownOrderStateError(spotResult.err) {
			return p.escalateCloseUnknown(ctx, hedge, alerts, exposureRescueTask{
				Mode:                    exposureRescueModeClose,
				Phase:                   "parallel_close_spot_unknown",
				Direction:               direction,
				Symbol:                  hedge.Target.Symbol,
				Pair:                    hedge.Target.Pair,
				SpotBaseAsset:           hedge.Target.SpotBaseAsset,
				SpotBasePrecision:       hedge.Target.SpotBasePrecision,
				SpotReferencePrice:      spotReferencePrice,
				InitialHedgeQty:         hedge.HedgeQty,
				FuturesClientOrderID:    plan.futuresOrder.NewClientOrderID,
				KnownFuturesExecutedQty: filledFuturesQty,
			}, "并发现货平仓终态未知且 ExposureManager 托管失败", "并发现货平仓终态未知，已转入 ExposureManager 托管")
		}
		return p.recoverCloseByReopeningFutures(
			ctx,
			hedge,
			alerts,
			filledFuturesQty,
			direction,
			"reopen_short_after_parallel_spot_close_failure",
			fmt.Sprintf("closeableSpotQty=%.8f spotErr=%v", filledFuturesQty, spotResult.err),
			fmt.Sprintf("现货平仓失败 %s: %v，已恢复空头对冲", hedge.Target.Pair, spotResult.err),
			"并发现货平仓失败且恢复空头失败: spotErr=%v recoverErr=%w",
			spotResult.err,
		)
	}

	soldSpotQty := spotResult.order.SemanticView().ExecutedQty
	if soldSpotQty <= executionQtyTolerance {
		return p.recoverCloseByReopeningFutures(
			ctx,
			hedge,
			alerts,
			filledFuturesQty,
			direction,
			"reopen_short_after_parallel_zero_spot_close",
			fmt.Sprintf("closeableSpotQty=%.8f soldSpotQty=%.8f", filledFuturesQty, soldSpotQty),
			fmt.Sprintf("现货平仓无有效成交 %s，已恢复空头对冲", hedge.Target.Pair),
			"并发现货平仓无有效成交且恢复空头失败: %w",
		)
	}

	hedge.CloseSpotExecutedQty = soldSpotQty
	hedge.CloseSpotQuoteQty = parseFloat(spotResult.order.CummulativeQuoteQty)
	hedge.CloseSpotFeeUSDT = p.engine.estimateSpotCommissionUSDT(ctx, spotResult.order)
	effectiveCloseQty := math.Min(filledFuturesQty, soldSpotQty)
	if soldSpotQty > executionQtyTolerance && effectiveCloseQty+executionQtyTolerance < soldSpotQty {
		if err := p.rebalanceParallelSpotExcess(ctx, hedge, soldSpotQty, effectiveCloseQty, spotReferencePrice, direction); err != nil {
			return alerts, err
		}
	}
	if filledFuturesQty > effectiveCloseQty+executionQtyTolerance {
		residualQty := filledFuturesQty - effectiveCloseQty
		return p.recoverCloseByReopeningFutures(
			ctx,
			hedge,
			alerts,
			residualQty,
			direction,
			"reopen_short_after_parallel_partial_spot_close",
			fmt.Sprintf("filledFuturesQty=%.8f effectiveCloseQty=%.8f residualQty=%.8f", filledFuturesQty, effectiveCloseQty, residualQty),
			fmt.Sprintf("现货平仓部分成交 %s: requested=%.8f executed=%.8f，已恢复剩余空头", hedge.Target.Pair, filledFuturesQty, effectiveCloseQty),
			"并发平仓后恢复剩余空头失败: %w",
		)
	}
	hedge.CloseFuturesExecutedQty = effectiveCloseQty
	hedge.CloseSpotExecutedQty = effectiveCloseQty
	if effectiveCloseQty <= executionQtyTolerance {
		return alerts, fmt.Errorf("并发平仓后有效成交数量为 0")
	}
	hedge.completeCloseLifecycle(false, strings.Join(alerts, " | "))
	return alerts, nil
}

func (e *FundingRateSniperEngine) executeSequentialClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	protocol := e.ensureCloseProtocol()
	if protocol == nil {
		return nil, fmt.Errorf("close protocol 未初始化")
	}
	return protocol.ExecuteSequentialClosePlans(ctx, plans)
}

func (e *FundingRateSniperEngine) executeParallelClosePlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	protocol := e.ensureCloseProtocol()
	if protocol == nil {
		return nil, fmt.Errorf("close protocol 未初始化")
	}
	return protocol.ExecuteParallelClosePlans(ctx, plans)
}

func (p *CloseProtocol) executeSequentialPlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	futuresOrders := make([]FuturesBatchOrderRequest, 0, len(plans))
	for _, plan := range plans {
		futuresOrders = append(futuresOrders, plan.futuresOrder)
	}
	if err := p.engine.noteOrderPlaced(ctx, "batch_close_futures", fmt.Sprintf("targets=%d", len(futuresOrders)), len(futuresOrders), false); err != nil {
		return nil, err
	}
	futuresCloseResults, err := p.engine.placeFuturesBatchOrdersIdempotent(ctx, futuresOrders)
	if err != nil {
		return nil, err
	}
	closeResultBySymbol := make(map[string]FuturesBatchOrderResult, len(futuresCloseResults))
	for _, result := range futuresCloseResults {
		closeResultBySymbol[result.Symbol] = result
	}

	alerts := make([]string, 0, len(plans))
	var alertsMu sync.Mutex
	runErr := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(p.engine.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		plan := plans[idx]
		futureResult, ok := closeResultBySymbol[plan.hedge.Target.Symbol]
		if !ok {
			alertsMu.Lock()
			alerts = append(alerts, fmt.Sprintf("合约平仓回执缺失 %s", plan.hedge.Target.Symbol))
			alertsMu.Unlock()
			return p.engine.recordExecutionIncident("close_futures_missing", plan.hedge.Target.Symbol, "合约平仓回执缺失")
		}
		closeAlerts, closeErr := p.CloseHedgePosition(workerCtx, plan.hedge, futureResult, plan.preferPassiveSpot)
		alertsMu.Lock()
		alerts = append(alerts, closeAlerts...)
		if closeErr != nil {
			alerts = append(alerts, closeErr.Error())
		}
		alertsMu.Unlock()
		if closeErr == nil {
			return nil
		}
		return p.engine.recordExecutionIncident("close_recovery", plan.hedge.Target.Symbol, closeErr.Error())
	})
	if runErr != nil {
		return nil, runErr
	}
	return alerts, nil
}

func (p *CloseProtocol) executeParallelPlans(ctx context.Context, plans []closeSequencePlan) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	alerts := make([]string, 0, len(plans))
	var alertsMu sync.Mutex
	runErr := runBoundedConcurrentTasks(ctx, effectiveOpenHedgeMaxConcurrency(p.engine.cfg), len(plans), func(workerCtx context.Context, idx int) error {
		closeAlerts, closeErr := p.CloseHedgePositionParallel(workerCtx, plans[idx])
		alertsMu.Lock()
		alerts = append(alerts, closeAlerts...)
		if closeErr != nil {
			alerts = append(alerts, closeErr.Error())
		}
		alertsMu.Unlock()
		if closeErr == nil {
			return nil
		}
		return p.engine.recordExecutionIncident("close_recovery", plans[idx].hedge.Target.Symbol, closeErr.Error())
	})
	if runErr != nil {
		return nil, runErr
	}
	return alerts, nil
}
