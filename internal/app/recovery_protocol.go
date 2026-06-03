package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RecoveryProtocol 封装恢复、升级和未知状态处理。
type RecoveryProtocol struct {
	engine *FundingRateSniperEngine
}

type uncertainFuturesOpenFallbackOutcome string

const (
	uncertainFuturesOpenFallbackNoExposure   uncertainFuturesOpenFallbackOutcome = "no_exposure"
	uncertainFuturesOpenFallbackTrimRequired uncertainFuturesOpenFallbackOutcome = "trim_required"
)

type uncertainFuturesOpenFallbackDecision struct {
	Outcome       uncertainFuturesOpenFallbackOutcome
	Summary       string
	TrimQty       float64
	TrimSide      string
	Result        FuturesBatchOrderResult
	ClientOrderID string
}

func newRecoveryProtocol(engine *FundingRateSniperEngine) *RecoveryProtocol {
	if engine == nil {
		return nil
	}
	return &RecoveryProtocol{engine: engine}
}

func (e *FundingRateSniperEngine) ensureRecoveryProtocol() *RecoveryProtocol {
	if e == nil {
		return nil
	}
	e.recoveryProtocolOnce.Do(func() {
		e.recoveryProtocol = newRecoveryProtocol(e)
	})
	return e.recoveryProtocol
}

func (e *FundingRateSniperEngine) scheduleExposureRescueProtocolResult(ctx context.Context, task exposureRescueTask, failureMessage string, scheduledMessage string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		if err := e.scheduleExposureRescue(ctx, task); err != nil {
			return RecoveryProtocolResult{}, fmt.Errorf("%s: %w", strings.TrimSpace(failureMessage), err)
		}
		action := recoveryActionResult{
			ProtocolAction: "exposure_rescue_escalated",
			Summary:        strings.TrimSpace(scheduledMessage),
			Escalated:      true,
		}
		return RecoveryProtocolResult{
			ProtocolResult: ProtocolResult{
				Protocol:     "recovery.exposure_rescue",
				Successful:   true,
				Summary:      strings.TrimSpace(scheduledMessage),
				CurrentStage: LifecycleStageRecovering,
				Lifecycle: LifecycleSuggestion{
					NextStage: LifecycleStageRecovering,
					Reason:    strings.TrimSpace(scheduledMessage),
				},
				Risk: RiskDecision{
					Action:      RiskActionEscalateRecovery,
					Category:    "exposure_rescue",
					Severity:    RiskSeverityLevelCritical,
					Reason:      strings.TrimSpace(scheduledMessage),
					CanContinue: false,
				},
				Events: newProtocolEvents(e, "recovery.exposure_rescue", true,
					protocolEventSpec{
						EventType: EventTypeRecoveryStarted,
						Level:     EventLevelWarning,
						Stage:     LifecycleStageRecovering,
						Reason:    strings.TrimSpace(scheduledMessage),
					},
					protocolEventSpec{
						EventType: EventTypeRecoveryEscalated,
						Level:     EventLevelCritical,
						Stage:     LifecycleStageRecovering,
						Reason:    strings.TrimSpace(scheduledMessage),
					},
				),
			},
			Action: action,
		}, nil
	}
	return protocol.ScheduleExposureRescue(ctx, task, failureMessage, scheduledMessage)
}

func (e *FundingRateSniperEngine) handleUnknownOrderState(symbol string, phase string, err error) error {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return fmt.Errorf("recovery protocol 未初始化")
	}
	_, protocolErr := protocol.HandleUnknownOrderState(symbol, phase, err)
	return protocolErr
}

func (e *FundingRateSniperEngine) flattenUncertainFuturesOpen(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) RecoveryProtocolResult {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}
	}
	return protocol.FlattenUncertainFuturesOpen(ctx, symbol, clientOrderID, initial)
}

func (e *FundingRateSniperEngine) executeFuturesRecoveryCloseProtocol(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}, fmt.Errorf("recovery protocol 未初始化")
	}
	return protocol.ExecuteFuturesRecoveryClose(ctx, symbol, quantity, direction, phase, action, detail)
}

func (e *FundingRateSniperEngine) executeOpenFuturesRevertProtocol(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, action string, detail string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}, fmt.Errorf("recovery protocol 未初始化")
	}
	return protocol.ExecuteOpenFuturesRevert(ctx, symbol, quantity, direction, action, detail)
}

func (e *FundingRateSniperEngine) executeFuturesRecoveryReopenProtocol(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}, fmt.Errorf("recovery protocol 未初始化")
	}
	return protocol.ExecuteFuturesRecoveryReopen(ctx, symbol, quantity, direction, phase, action, detail)
}

func (e *FundingRateSniperEngine) executeSpotRecoveryMarketProtocol(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}, fmt.Errorf("recovery protocol 未初始化")
	}
	return protocol.ExecuteSpotRecoveryMarket(ctx, pair, side, quantity, clientOrderPrefix, venue, phase, symbol, action, detail)
}

func (e *FundingRateSniperEngine) executeSpotRecoveryChasingProtocol(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (RecoveryProtocolResult, error) {
	protocol := e.ensureRecoveryProtocol()
	if protocol == nil {
		return RecoveryProtocolResult{}, fmt.Errorf("recovery protocol 未初始化")
	}
	return protocol.ExecuteSpotRecoveryChasing(ctx, pair, side, quantity, clientOrderPrefix, venue, phase, symbol, action, detail)
}

func (p *RecoveryProtocol) ScheduleExposureRescue(ctx context.Context, task exposureRescueTask, failureMessage string, scheduledMessage string) (RecoveryProtocolResult, error) {
	action, err := p.scheduleExposureRescue(ctx, task, failureMessage, scheduledMessage)
	return RecoveryProtocolResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "recovery.exposure_rescue",
			Successful:   err == nil,
			Summary:      scheduledMessage,
			CurrentStage: LifecycleStageRecovering,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStageRecovering,
				Reason:    scheduledMessage,
			},
			Risk: RiskDecision{
				Action:      RiskActionEscalateRecovery,
				Category:    "exposure_rescue",
				Severity:    RiskSeverityLevelCritical,
				Reason:      scheduledMessage,
				CanContinue: false,
			},
			Events: newProtocolEvents(p.engine, "recovery.exposure_rescue", err == nil,
				protocolEventSpec{
					EventType: EventTypeRecoveryStarted,
					Level:     EventLevelWarning,
					Stage:     LifecycleStageRecovering,
					Reason:    scheduledMessage,
				},
				protocolEventSpec{
					EventType: EventTypeRecoveryEscalated,
					Level:     EventLevelCritical,
					Stage:     LifecycleStageRecovering,
					Reason:    scheduledMessage,
				},
			),
		},
		Action: action,
	}, err
}

func (p *RecoveryProtocol) HandleUnknownOrderState(symbol string, phase string, err error) (RecoveryProtocolResult, error) {
	protocolErr := p.handleUnknownOrderState(symbol, phase, err)
	return RecoveryProtocolResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "recovery.unknown_order",
			Successful:   protocolErr == nil,
			Summary:      nonEmpty(errorString(err), "订单终态未知"),
			CurrentStage: LifecycleStageRecovering,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStageFused,
				Reason:    nonEmpty(errorString(err), "订单终态未知"),
			},
			Risk: RiskDecision{
				Action:      RiskActionTripPlatformFuse,
				Category:    "unknown_order",
				Severity:    RiskSeverityLevelCritical,
				Reason:      nonEmpty(errorString(err), "订单终态未知"),
				CanContinue: false,
			},
			Events: newProtocolEvents(p.engine, "recovery.unknown_order", protocolErr == nil,
				protocolEventSpec{
					EventType: EventTypeRecoveryStarted,
					Level:     EventLevelCritical,
					Stage:     LifecycleStageRecovering,
					Reason:    nonEmpty(errorString(err), "订单终态未知"),
				},
				protocolEventSpec{
					EventType: EventTypeOrderUncertain,
					Level:     EventLevelCritical,
					Stage:     LifecycleStageFused,
					Reason:    nonEmpty(errorString(err), "订单终态未知"),
				},
			),
		},
	}, protocolErr
}

func (p *RecoveryProtocol) FlattenUncertainFuturesOpen(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) RecoveryProtocolResult {
	result := p.recoverUncertainFuturesOpenResult(ctx, symbol, clientOrderID, initial)
	result = p.cancelUncertainFuturesOpenIfNeeded(ctx, symbol, clientOrderID, result)
	decision := p.decideUncertainFuturesOpenFallback(symbol, clientOrderID, result)
	action := p.applyUncertainFuturesOpenFallback(ctx, symbol, decision)
	return p.flattenUncertainFuturesOpenProtocolResult(decision, action)
}

func (p *RecoveryProtocol) ExecuteFuturesRecoveryClose(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (RecoveryProtocolResult, error) {
	recoveryAction, err := p.executeFuturesRecoveryClose(ctx, symbol, quantity, direction, phase, action, detail)
	return p.newRecoveryActionProtocolResult("recovery.futures_close", "futures_recovery_close", detail, recoveryAction, err), err
}

func (p *RecoveryProtocol) ExecuteOpenFuturesRevert(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, action string, detail string) (RecoveryProtocolResult, error) {
	recoveryAction, err := p.executeOpenFuturesRevert(ctx, symbol, quantity, direction, action, detail)
	return p.newRecoveryActionProtocolResult("recovery.open_futures_revert", "open_futures_revert", detail, recoveryAction, err), err
}

func (p *RecoveryProtocol) ExecuteFuturesRecoveryReopen(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (RecoveryProtocolResult, error) {
	recoveryAction, err := p.executeFuturesRecoveryReopen(ctx, symbol, quantity, direction, phase, action, detail)
	return p.newRecoveryActionProtocolResult("recovery.futures_reopen", "futures_recovery_reopen", detail, recoveryAction, err), err
}

func (p *RecoveryProtocol) ExecuteSpotRecoveryMarket(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (RecoveryProtocolResult, error) {
	recoveryAction, err := p.executeSpotRecoveryMarket(ctx, pair, side, quantity, clientOrderPrefix, venue, phase, symbol, action, detail)
	return p.newRecoveryActionProtocolResult("recovery.spot_market", "spot_recovery_market", detail, recoveryAction, err), err
}

func (p *RecoveryProtocol) ExecuteSpotRecoveryChasing(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (RecoveryProtocolResult, error) {
	recoveryAction, err := p.executeSpotRecoveryChasing(ctx, pair, side, quantity, clientOrderPrefix, venue, phase, symbol, action, detail)
	return p.newRecoveryActionProtocolResult("recovery.spot_chasing", "spot_recovery_chasing", detail, recoveryAction, err), err
}

func (p *RecoveryProtocol) scheduleExposureRescue(ctx context.Context, task exposureRescueTask, failureMessage string, scheduledMessage string) (recoveryActionResult, error) {
	if err := p.engine.scheduleExposureRescue(ctx, task); err != nil {
		return recoveryActionResult{}, fmt.Errorf("%s: %w", strings.TrimSpace(failureMessage), err)
	}
	return recoveryActionResult{
		ProtocolAction: "exposure_rescue_escalated",
		Summary:        strings.TrimSpace(scheduledMessage),
		Escalated:      true,
	}, nil
}

func (p *RecoveryProtocol) handleUnknownOrderState(symbol string, phase string, err error) error {
	semantic := semanticViewFromExecutionError(err)
	detail := nonEmpty(semantic.Reason, nonEmpty(errorString(err), "订单终态未知"))
	if strings.TrimSpace(symbol) == "" && strings.TrimSpace(semantic.Symbol) != "" {
		symbol = semantic.Symbol
	}
	if strings.TrimSpace(symbol) != "" {
		p.engine.setSymbolCooldown(symbol, "unknown_order", "critical", detail)
	}
	if incidentErr := p.engine.recordExecutionIncident(phase, symbol, detail); incidentErr != nil {
		p.engine.logger.Printf("记录未知订单执行事故失败: %v", incidentErr)
	}
	return p.engine.tripPlatformFuse("unknown_order", "critical", fmt.Sprintf("symbol=%s phase=%s detail=%s", nonEmpty(symbol, "-"), nonEmpty(phase, "unknown_order"), detail))
}

func (p *RecoveryProtocol) flattenUncertainFuturesOpen(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) {
	_ = p.FlattenUncertainFuturesOpen(ctx, symbol, clientOrderID, initial)
}

func (p *RecoveryProtocol) recoverUncertainFuturesOpenResult(ctx context.Context, symbol string, clientOrderID string, initial FuturesBatchOrderResult) FuturesBatchOrderResult {
	result := initial
	if clientOrderID == "" {
		return result
	}
	queried, err := p.engine.queryFuturesOrderByClientID(ctx, symbol, clientOrderID)
	if err != nil {
		p.engine.logger.Printf("合约空单兜底回查失败，symbol=%s clientOrderId=%s err=%v", symbol, clientOrderID, err)
		return result
	}
	return queried
}

func (p *RecoveryProtocol) cancelUncertainFuturesOpenIfNeeded(ctx context.Context, symbol string, clientOrderID string, result FuturesBatchOrderResult) FuturesBatchOrderResult {
	if clientOrderID == "" || result.IsClosed() {
		return result
	}
	canceled, err := p.engine.cancelFuturesOrderByClientID(ctx, symbol, clientOrderID)
	if err != nil {
		p.engine.logger.Printf("合约空单兜底撤单失败，symbol=%s clientOrderId=%s err=%v", symbol, clientOrderID, err)
		return result
	}
	return canceled
}

func (p *RecoveryProtocol) decideUncertainFuturesOpenFallback(symbol string, clientOrderID string, result FuturesBatchOrderResult) uncertainFuturesOpenFallbackDecision {
	decision := uncertainFuturesOpenFallbackDecision{
		Outcome:       uncertainFuturesOpenFallbackNoExposure,
		Summary:       fmt.Sprintf("合约空单兜底完成，未发现已成交数量，symbol=%s clientOrderId=%s status=%s", symbol, clientOrderID, result.Status),
		Result:        result,
		ClientOrderID: clientOrderID,
	}
	closeQty := result.ExecutedQtyValue()
	if closeQty <= 0 {
		return decision
	}
	closeSide := "BUY"
	if strings.EqualFold(result.Side, "BUY") {
		closeSide = "SELL"
	}
	decision.Outcome = uncertainFuturesOpenFallbackTrimRequired
	decision.TrimQty = closeQty
	decision.TrimSide = closeSide
	decision.Summary = fmt.Sprintf("合约空单兜底平仓计划已生成，symbol=%s clientOrderId=%s qty=%.8f side=%s", symbol, clientOrderID, closeQty, closeSide)
	return decision
}

func (p *RecoveryProtocol) flattenUncertainFuturesOpenProtocolResult(decision uncertainFuturesOpenFallbackDecision, action recoveryActionResult) RecoveryProtocolResult {
	summary := action.lifecycleReason(decision.Summary)
	successful := !action.Escalated
	nextStage := LifecycleStageMonitoring
	riskAction := RiskActionObserve
	riskSeverity := RiskSeverityLevelWarning
	canContinue := true
	eventSpecs := []protocolEventSpec{
		{
			EventType: EventTypeRecoveryStarted,
			Level:     EventLevelWarning,
			Stage:     LifecycleStageRecovering,
			Reason:    summary,
		},
	}
	if action.Escalated {
		nextStage = LifecycleStageRecovering
		riskAction = RiskActionEscalateRecovery
		riskSeverity = RiskSeverityLevelCritical
		canContinue = false
		eventSpecs = append(eventSpecs, protocolEventSpec{
			EventType: EventTypeRecoveryEscalated,
			Level:     EventLevelCritical,
			Stage:     LifecycleStageRecovering,
			Reason:    summary,
		})
	}
	return RecoveryProtocolResult{
		ProtocolResult: ProtocolResult{
			Protocol:     "recovery.flatten_uncertain_futures_open",
			Successful:   successful,
			Summary:      summary,
			CurrentStage: LifecycleStageRecovering,
			Lifecycle: LifecycleSuggestion{
				NextStage: nextStage,
				Reason:    summary,
			},
			Risk: RiskDecision{
				Action:      riskAction,
				Category:    "uncertain_futures_open",
				Severity:    riskSeverity,
				Reason:      summary,
				CanContinue: canContinue,
			},
			Events: newProtocolEvents(p.engine, "recovery.flatten_uncertain_futures_open", successful, eventSpecs...),
		},
		Action: action,
	}
}

func (p *RecoveryProtocol) newRecoveryActionProtocolResult(protocol string, category string, summary string, action recoveryActionResult, err error) RecoveryProtocolResult {
	reason := strings.TrimSpace(nonEmpty(action.lifecycleReason(summary), nonEmpty(errorString(err), summary)))
	successful := err == nil
	riskAction := RiskActionObserve
	riskSeverity := RiskSeverityLevelWarning
	canContinue := true
	eventSpecs := []protocolEventSpec{
		{
			EventType: EventTypeRecoveryStarted,
			Level:     EventLevelWarning,
			Stage:     LifecycleStageRecovering,
			Reason:    reason,
		},
	}
	if !successful {
		riskAction = RiskActionEscalateRecovery
		riskSeverity = RiskSeverityLevelCritical
		canContinue = false
		eventSpecs = append(eventSpecs, protocolEventSpec{
			EventType: EventTypeRecoveryEscalated,
			Level:     EventLevelCritical,
			Stage:     LifecycleStageRecovering,
			Reason:    reason,
		})
	}
	return RecoveryProtocolResult{
		ProtocolResult: ProtocolResult{
			Protocol:     strings.TrimSpace(protocol),
			Successful:   successful,
			Summary:      reason,
			CurrentStage: LifecycleStageRecovering,
			Lifecycle: LifecycleSuggestion{
				NextStage: LifecycleStageRecovering,
				Reason:    reason,
			},
			Risk: RiskDecision{
				Action:      riskAction,
				Category:    strings.TrimSpace(category),
				Severity:    riskSeverity,
				Reason:      reason,
				CanContinue: canContinue,
			},
			Events: newProtocolEvents(p.engine, strings.TrimSpace(protocol), successful, eventSpecs...),
		},
		Action: action,
	}
}

func (p *RecoveryProtocol) applyUncertainFuturesOpenFallback(ctx context.Context, symbol string, decision uncertainFuturesOpenFallbackDecision) recoveryActionResult {
	if decision.Outcome == uncertainFuturesOpenFallbackNoExposure {
		p.engine.logger.Printf("%s", decision.Summary)
		return recoveryActionResult{
			ProtocolAction: "flatten_uncertain_futures_open_no_exposure",
			Summary:        decision.Summary,
			Aborted:        true,
		}
	}
	trimmed, err := p.engine.placeSingleFuturesMarketOrderIdempotent(ctx, symbol, decision.TrimSide, decision.TrimQty, true, newClientOrderID("ftrim", symbol))
	if err != nil {
		summary := fmt.Sprintf("合约空单兜底平仓失败，symbol=%s qty=%.8f side=%s err=%v", symbol, decision.TrimQty, decision.TrimSide, err)
		p.engine.logger.Printf("%s", summary)
		return recoveryActionResult{
			ProtocolAction: "flatten_uncertain_futures_open_trim_failed",
			Summary:        summary,
			Escalated:      true,
		}
	}
	summary := fmt.Sprintf("合约空单兜底平仓成功，symbol=%s qty=%.8f side=%s", symbol, decision.TrimQty, decision.TrimSide)
	p.engine.logger.Printf("%s", summary)
	return recoveryActionResult{
		FuturesExecution: trimmed,
		ProtocolAction:   "flatten_uncertain_futures_open_trim",
		Summary:          summary,
		Reverted:         true,
	}
}

func (p *RecoveryProtocol) executeFuturesRecoveryClose(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (recoveryActionResult, error) {
	recoverCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	start := time.Now()
	execution, err := p.engine.placeDirectionalFuturesCloseConfirmed(recoverCtx, symbol, quantity, direction)
	latency := time.Since(start)

	var slippage float64
	if err == nil {
		targetPrice := execution.AvgPrice // Recovery doesn't have a strict target price, use avg
		slippage = CalculateSlippage(targetPrice, execution.AvgPrice, quantity, direction.closeFuturesSide() == "BUY")
		if p.engine.costReporter != nil {
			p.engine.costReporter.RecordExecutionCost(ExecutionCostRecord{
				Symbol:           symbol,
				Action:           "recover_close_futures",
				TargetQuantity:   quantity,
				ExecutedQuantity: execution.ExecutedQty,
				TargetPrice:      targetPrice,
				ExecutedPrice:    execution.AvgPrice,
				SlippageUSDT:     slippage,
				Latency:          latency,
				Attempts:         1,
			})
		}
	}

	if err != nil {
		return recoveryActionResult{}, err
	}
	p.engine.recordExecutionAction(phase, symbol, action, detail)
	return recoveryActionResult{
		FuturesExecution:     execution,
		ProtocolAction:       action,
		Summary:              detail,
		RecoveryFrictionUSDT: slippage,
	}, nil
}

func (p *RecoveryProtocol) executeOpenFuturesRevert(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, action string, detail string) (recoveryActionResult, error) {
	result, err := p.executeFuturesRecoveryClose(ctx, symbol, quantity, direction, "open_recovery", action, detail)
	if err != nil {
		return recoveryActionResult{}, err
	}
	result.Reverted = true
	return result, nil
}

func (p *RecoveryProtocol) executeFuturesRecoveryReopen(ctx context.Context, symbol string, quantity float64, direction fundingCarryDirection, phase string, action string, detail string) (recoveryActionResult, error) {
	recoverCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()

	start := time.Now()
	execution, err := p.engine.placeDirectionalFuturesReopen(recoverCtx, symbol, quantity, direction)
	latency := time.Since(start)

	var slippage float64
	if err == nil {
		targetPrice := execution.AvgPrice // Recovery uses avg as target
		slippage = CalculateSlippage(targetPrice, execution.AvgPrice, quantity, direction.openFuturesSide() == "BUY")
		if p.engine.costReporter != nil {
			p.engine.costReporter.RecordExecutionCost(ExecutionCostRecord{
				Symbol:           symbol,
				Action:           "recover_reopen_futures",
				TargetQuantity:   quantity,
				ExecutedQuantity: execution.ExecutedQty,
				TargetPrice:      targetPrice,
				ExecutedPrice:    execution.AvgPrice,
				SlippageUSDT:     slippage,
				Latency:          latency,
				Attempts:         1,
			})
		}
	}

	if err != nil {
		return recoveryActionResult{}, err
	}
	p.engine.recordExecutionAction(phase, symbol, action, detail)
	return recoveryActionResult{
		FuturesExecution:     execution,
		ProtocolAction:       action,
		Summary:              detail,
		RestoredHedge:        true,
		PartialClose:         strings.Contains(action, "partial"),
		RecoveryFrictionUSDT: slippage,
	}, nil
}

func (p *RecoveryProtocol) executeSpotRecoveryMarket(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (recoveryActionResult, error) {
	recoverCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()

	start := time.Now()
	order, err := p.engine.placeDirectionalSpotMarketOrderIdempotent(recoverCtx, pair, side, quantity, newClientOrderID(clientOrderPrefix, pair), venue, "spot_"+strings.ToLower(side)+"_revert", true)
	latency := time.Since(start)

	var slippage float64
	if err == nil {
		view := order.SemanticView()
		avgPrice := 0.0
		if view.ExecutedQty > 0 {
			avgPrice = view.CumQuoteQty / view.ExecutedQty
		}
		slippage = CalculateSlippage(avgPrice, avgPrice, view.ExecutedQty, side == "BUY") // Use avgPrice as target since it's recovery
		if p.engine.costReporter != nil {
			p.engine.costReporter.RecordExecutionCost(ExecutionCostRecord{
				Symbol:           symbol,
				Action:           "recover_spot_market",
				TargetQuantity:   quantity,
				ExecutedQuantity: view.ExecutedQty,
				TargetPrice:      avgPrice,
				ExecutedPrice:    avgPrice,
				SlippageUSDT:     slippage,
				Latency:          latency,
				Attempts:         1,
			})
		}
	}

	if err != nil {
		return recoveryActionResult{}, err
	}
	p.engine.recordExecutionAction(phase, symbol, action, detail)
	return recoveryActionResult{
		SpotOrder:            order,
		ProtocolAction:       action,
		Summary:              detail,
		RecoveryFrictionUSDT: slippage,
	}, nil
}

func (p *RecoveryProtocol) executeSpotRecoveryChasing(ctx context.Context, pair string, side string, quantity float64, clientOrderPrefix string, venue spotExecutionVenue, phase string, symbol string, action string, detail string) (recoveryActionResult, error) {
	recoverCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()

	start := time.Now()
	order, err := p.engine.placeDirectionalSpotChasingOrderWithOptions(recoverCtx, pair, side, quantity, clientOrderPrefix, spotChaseOptions{}, venue)
	latency := time.Since(start)

	var slippage float64
	if err == nil {
		view := order.SemanticView()
		avgPrice := 0.0
		if view.ExecutedQty > 0 {
			avgPrice = view.CumQuoteQty / view.ExecutedQty
		}
		slippage = CalculateSlippage(avgPrice, avgPrice, view.ExecutedQty, side == "BUY")
		if p.engine.costReporter != nil {
			p.engine.costReporter.RecordExecutionCost(ExecutionCostRecord{
				Symbol:           symbol,
				Action:           "recover_spot_chasing",
				TargetQuantity:   quantity,
				ExecutedQuantity: view.ExecutedQty,
				TargetPrice:      avgPrice,
				ExecutedPrice:    avgPrice,
				SlippageUSDT:     slippage,
				Latency:          latency,
				Attempts:         1,
			})
		}
	}

	if err != nil {
		return recoveryActionResult{}, err
	}
	p.engine.recordExecutionAction(phase, symbol, action, detail)
	return recoveryActionResult{
		SpotOrder:            order,
		ProtocolAction:       action,
		Summary:              detail,
		RecoveryFrictionUSDT: slippage,
	}, nil
}
