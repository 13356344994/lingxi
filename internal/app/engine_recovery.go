package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"lingxi/internal/venue/binance"
)

// 本文件从 funding_sniper_engine.go 拆出：仅承载开/平仓恢复协议结果的语义映射（生命周期理由、
// 决策与协议应用）、敞口救援调度、风险/领域/执行事件发射与批量合约下单入口（同包搬移、
// 行为不变），向单文件 ≤ 800 行门禁靠拢。

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type recoveryActionResult struct {
	FuturesExecution     OrderExecutionSnapshot
	SpotOrder            SpotOrderResponse
	ProtocolAction       string
	Summary              string
	Reverted             bool
	Aborted              bool
	RestoredHedge        bool
	PartialClose         bool
	Escalated            bool
	RecoveryFrictionUSDT float64
}

type RecoveryOutcome string

const (
	RecoveryOutcomeClosed           RecoveryOutcome = "closed"
	RecoveryOutcomeCloseRecovered   RecoveryOutcome = "close_recovered"
	RecoveryOutcomeCloseRecoveredPt RecoveryOutcome = "close_recovered_partial"
	RecoveryOutcomeEscalated        RecoveryOutcome = "escalated"
)

type RecoveryDecision struct {
	Outcome            RecoveryOutcome
	LifecycleState     OpenHedgeLifecycleState
	Summary            string
	RequiresEscalation bool
	RestoredHedge      bool
}

type OpenRecoveryOutcome string

const (
	OpenRecoveryOutcomeOpened    OpenRecoveryOutcome = "opened"
	OpenRecoveryOutcomeReverted  OpenRecoveryOutcome = "reverted"
	OpenRecoveryOutcomeEscalated OpenRecoveryOutcome = "escalated"
	OpenRecoveryOutcomeAborted   OpenRecoveryOutcome = "aborted"
)

type OpenRecoveryDecision struct {
	Outcome            OpenRecoveryOutcome
	Summary            string
	RequiresEscalation bool
	Reverted           bool
	Aborted            bool
}

func (d OpenRecoveryDecision) err(cause error) error {
	if cause != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(d.Summary), cause)
	}
	return fmt.Errorf("%s", strings.TrimSpace(d.Summary))
}

func (r recoveryActionResult) futuresFeeUSDT(e *FundingRateSniperEngine, ctx context.Context) float64 {
	if e == nil || !hasConfirmedOrderExecution(r.FuturesExecution) {
		return 0
	}
	if e.client == nil {
		return e.convertCommissionToUSDT(ctx, r.FuturesExecution.Symbol, r.FuturesExecution.CommissionAsset, r.FuturesExecution.Commission, r.FuturesExecution.AvgPrice)
	}
	return e.estimateFuturesExecutionFeeUSDT(ctx, r.FuturesExecution)
}

func (r recoveryActionResult) spotFeeUSDT(e *FundingRateSniperEngine, ctx context.Context) float64 {
	if e == nil {
		return 0
	}
	return e.estimateSpotCommissionUSDT(ctx, r.SpotOrder)
}

func (r RecoveryProtocolResult) futuresFeeUSDT(e *FundingRateSniperEngine, ctx context.Context) float64 {
	return r.Action.futuresFeeUSDT(e, ctx)
}

func (r RecoveryProtocolResult) spotFeeUSDT(e *FundingRateSniperEngine, ctx context.Context) float64 {
	return r.Action.spotFeeUSDT(e, ctx)
}

func (r recoveryActionResult) lifecycleReason(fallback string) string {
	return strings.TrimSpace(nonEmpty(r.Summary, fallback))
}

func (r RecoveryProtocolResult) lifecycleReason(fallback string) string {
	if trimmed := strings.TrimSpace(r.ProtocolResult.Summary); trimmed != "" {
		return trimmed
	}
	return r.Action.lifecycleReason(fallback)
}

func (r recoveryActionResult) openDecision(fallback string) OpenRecoveryDecision {
	reason := r.lifecycleReason(fallback)
	if r.Escalated {
		return OpenRecoveryDecision{
			Outcome:            OpenRecoveryOutcomeEscalated,
			Summary:            reason,
			RequiresEscalation: true,
		}
	}
	if r.Aborted {
		return OpenRecoveryDecision{
			Outcome: OpenRecoveryOutcomeAborted,
			Summary: reason,
			Aborted: true,
		}
	}
	if r.Reverted {
		return OpenRecoveryDecision{
			Outcome:  OpenRecoveryOutcomeReverted,
			Summary:  reason,
			Reverted: true,
		}
	}
	return OpenRecoveryDecision{
		Outcome: OpenRecoveryOutcomeOpened,
		Summary: reason,
	}
}

func (r RecoveryProtocolResult) openDecision(fallback string) OpenRecoveryDecision {
	return r.Action.openDecision(r.lifecycleReason(fallback))
}

func (r recoveryActionResult) openError(fallback string, cause error) error {
	decision := r.openDecision(fallback)
	return decision.err(cause)
}

func (r RecoveryProtocolResult) openError(fallback string, cause error) error {
	decision := r.openDecision(fallback)
	return decision.err(cause)
}

func (r recoveryActionResult) applyOpenProtocol(hedge *OpenHedge, fallback string) {
	if hedge == nil {
		return
	}
	decision := r.openDecision(fallback)
	switch decision.Outcome {
	case OpenRecoveryOutcomeEscalated:
		hedge.markEscalated(decision.Summary)
	case OpenRecoveryOutcomeAborted:
		hedge.completeCloseLifecycle(true, decision.Summary)
	}
}

func (r RecoveryProtocolResult) applyOpenProtocol(hedge *OpenHedge, fallback string) {
	r.Action.applyOpenProtocol(hedge, r.lifecycleReason(fallback))
}

func openLifecycleReason(base string, results ...RecoveryProtocolResult) string {
	parts := make([]string, 0, len(results)+1)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, result := range results {
		if strings.TrimSpace(result.Action.ProtocolAction) == "" {
			continue
		}
		decision := result.openDecision(result.lifecycleReason(result.ProtocolResult.Summary))
		if decision.Outcome != OpenRecoveryOutcomeOpened {
			continue
		}
		if trimmed := strings.TrimSpace(decision.Summary); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " | ")
}

func (r recoveryActionResult) closeDecision(fallback string) RecoveryDecision {
	reason := r.lifecycleReason(fallback)
	if r.Escalated {
		return RecoveryDecision{
			Outcome:            RecoveryOutcomeEscalated,
			LifecycleState:     OpenHedgeLifecycleEscalated,
			Summary:            reason,
			RequiresEscalation: true,
		}
	}
	if r.RestoredHedge {
		state := OpenHedgeLifecycleCloseRecovered
		outcome := RecoveryOutcomeCloseRecovered
		if r.PartialClose {
			state = OpenHedgeLifecycleCloseRecoveredPart
			outcome = RecoveryOutcomeCloseRecoveredPt
		}
		return RecoveryDecision{
			Outcome:        outcome,
			LifecycleState: state,
			Summary:        reason,
			RestoredHedge:  true,
		}
	}
	return RecoveryDecision{
		Outcome:        RecoveryOutcomeClosed,
		LifecycleState: OpenHedgeLifecycleClosed,
		Summary:        reason,
	}
}

func (r recoveryActionResult) applyCloseProtocol(hedge *OpenHedge, fallback string) {
	if hedge == nil {
		return
	}
	decision := r.closeDecision(fallback)
	switch decision.Outcome {
	case RecoveryOutcomeEscalated:
		hedge.markEscalated(decision.Summary)
	case RecoveryOutcomeCloseRecovered, RecoveryOutcomeCloseRecoveredPt:
		hedge.markCloseRecovered(decision.Outcome == RecoveryOutcomeCloseRecoveredPt, decision.Summary)
	default:
		hedge.completeCloseLifecycle(false, decision.Summary)
	}
}

func (r RecoveryProtocolResult) applyCloseProtocol(hedge *OpenHedge, fallback string) {
	r.Action.applyCloseProtocol(hedge, r.lifecycleReason(fallback))
}

func (e *FundingRateSniperEngine) scheduleOpenExposureRescueOrFail(ctx context.Context, task exposureRescueTask, failureMessage string, scheduledMessage string) error {
	result, err := e.scheduleExposureRescueProtocolResult(ctx, task, failureMessage, scheduledMessage)
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", result.lifecycleReason(scheduledMessage))
}

func buildOpenExposureRescueTask(
	target *MonitoredTarget,
	direction fundingCarryDirection,
	phase string,
	spotReferencePrice float64,
	spotClientOrderID string,
	futuresClientOrderID string,
	knownSpotExecutedQty float64,
	knownFuturesExecutedQty float64,
) exposureRescueTask {
	if target == nil {
		return exposureRescueTask{
			Mode:                    exposureRescueModeOpen,
			Phase:                   phase,
			Direction:               direction,
			SpotReferencePrice:      spotReferencePrice,
			SpotClientOrderID:       strings.TrimSpace(spotClientOrderID),
			FuturesClientOrderID:    strings.TrimSpace(futuresClientOrderID),
			KnownSpotExecutedQty:    knownSpotExecutedQty,
			KnownFuturesExecutedQty: knownFuturesExecutedQty,
		}
	}
	return exposureRescueTask{
		Mode:                    exposureRescueModeOpen,
		Phase:                   phase,
		Direction:               direction,
		Symbol:                  target.Candidate.Symbol,
		Pair:                    target.Candidate.Pair,
		SpotBaseAsset:           target.Candidate.SpotBaseAsset,
		SpotBasePrecision:       target.Candidate.SpotBasePrecision,
		SpotReferencePrice:      spotReferencePrice,
		SpotClientOrderID:       strings.TrimSpace(spotClientOrderID),
		FuturesClientOrderID:    strings.TrimSpace(futuresClientOrderID),
		KnownSpotExecutedQty:    knownSpotExecutedQty,
		KnownFuturesExecutedQty: knownFuturesExecutedQty,
	}
}

func (e *FundingRateSniperEngine) scheduleOpenExposureRescueForTargetOrFail(
	ctx context.Context,
	target *MonitoredTarget,
	direction fundingCarryDirection,
	phase string,
	spotReferencePrice float64,
	spotClientOrderID string,
	futuresClientOrderID string,
	knownSpotExecutedQty float64,
	knownFuturesExecutedQty float64,
	failureMessage string,
	scheduledMessage string,
) error {
	task := buildOpenExposureRescueTask(
		target,
		direction,
		phase,
		spotReferencePrice,
		spotClientOrderID,
		futuresClientOrderID,
		knownSpotExecutedQty,
		knownFuturesExecutedQty,
	)
	return e.scheduleOpenExposureRescueOrFail(ctx, task, failureMessage, scheduledMessage)
}

func (e *FundingRateSniperEngine) scheduleExposureRescueOrFail(ctx context.Context, task exposureRescueTask, failureMessage string, scheduledMessage string) error {
	result, err := e.scheduleExposureRescueProtocolResult(ctx, task, failureMessage, scheduledMessage)
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", result.lifecycleReason(scheduledMessage))
}

func (e *FundingRateSniperEngine) emitRiskEvent(event RiskEvent) {
	runID, cycleID := e.traceIDs()
	enriched := event.WithTrace(runID, cycleID).WithMetadata(e.recordMetadata())
	e.emitExecutionEvent(enriched.ExecutionRecord())
	if domainEvent, ok := enriched.DomainEvent(e.currentLifecycleStage); ok {
		e.emitDomainEvents([]DomainEvent{domainEvent})
	}
}

func (e *FundingRateSniperEngine) emitDomainEvents(events []DomainEvent) {
	e.persistDomainEvents(events)
	// S5-A:把达标的领域事件实时推送到外部告警出口(级别/去重在 dispatcher 内判定,nil-safe、fire-and-forget)。
	if e.alertDispatcher != nil {
		for _, event := range events {
			e.alertDispatcher.Notify(domainEventToAlert(event))
		}
	}
}

// persistDomainEvents 落盘结构化事件 + 触发观测 hook,但**不**推送告警出口。供已自行用 notifyLifecycle/NotifyTrade
// 保证告警必达的调用方使用(如停机/崩溃通知),避免同一事件在 Telegram 上重复推送两条。
func (e *FundingRateSniperEngine) persistDomainEvents(events []DomainEvent) {
	if e == nil || len(events) == 0 {
		return
	}
	if e.domainEventSink != nil {
		if err := e.domainEventSink.AppendDomainEvents(events); err != nil && e.logger != nil {
			e.logger.Printf("写入结构化协议事件失败: %v", err)
		}
	}
	if e.domainEventHook != nil {
		for _, event := range events {
			e.domainEventHook(event)
		}
	}
}

func (e *FundingRateSniperEngine) emitExecutionEvent(record ExecutionIncidentRecord) {
	if e.reportStore != nil {
		if err := e.reportStore.AppendExecutionIncident(record); err != nil {
			e.logger.Printf("写入执行事故记录失败: %v", err)
		}
	}
	if e.executionEventHook != nil {
		e.executionEventHook(record)
	}
}

func (e *FundingRateSniperEngine) logMonitorSummary(targets []*MonitoredTarget) {
	stateCounts := make(map[string]int)
	for _, target := range targets {
		state := nonEmpty(target.MonitorState, "unknown")
		stateCounts[state]++
		e.logger.Printf(
			"模块三摘要，symbol=%s state=%s valid=%t breach=%d net=%.6f spread=%.6f borrowRate=%.6f borrowUSDT=%.6f reason=%s",
			target.Candidate.Symbol,
			state,
			atomic.LoadInt32(&target.IsValid) == 1,
			target.BreachCount,
			target.LastNetProfit,
			target.LastSpread,
			target.LastBorrowReserveRate,
			target.LastBorrowReserveUSDT,
			nonEmpty(target.InvalidReason, "-"),
		)
	}

	e.logger.Printf(
		"模块三状态汇总，healthy=%d observing=%d invalidated=%d watching=%d unknown=%d",
		stateCounts["healthy"],
		stateCounts["observing"],
		stateCounts["invalidated"],
		stateCounts["watching"],
		stateCounts["unknown"],
	)
}

func (e *FundingRateSniperEngine) placeFuturesBatchOrders(ctx context.Context, orders []FuturesBatchOrderRequest) ([]FuturesBatchOrderResult, error) {
	raw, err := json.Marshal(orders)
	if err != nil {
		return nil, fmt.Errorf("序列化合约批量订单失败: %w", err)
	}

	params := url.Values{}
	params.Set("batchOrders", string(raw))

	body, err := e.signedRequest(ctx, http.MethodPost, e.client.FuturesBaseURL()+binance.PathFuturesBatchOrders, params)
	if err != nil {
		return nil, err
	}

	var result []FuturesBatchOrderResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析合约批量下单回执失败: %w", err)
	}

	return result, nil
}

func (e *FundingRateSniperEngine) placeFuturesBatchOrdersIdempotent(ctx context.Context, orders []FuturesBatchOrderRequest) ([]FuturesBatchOrderResult, error) {
	result, err := e.placeFuturesBatchOrders(ctx, orders)
	if err == nil {
		for idx := range result {
			if idx >= len(orders) || !futuresProtectedOpenFallbackNeeded(orders[idx], result[idx]) {
				continue
			}
			snapshot, fallbackErr := e.placeFuturesProtectedOpenDualTrack(ctx, orders[idx], "futures_batch_open_single", e.submitSingleFuturesOrderIdempotent)
			if fallbackErr != nil {
				result[idx] = FuturesBatchOrderResult{
					Symbol:        orders[idx].Symbol,
					ClientOrderID: orders[idx].NewClientOrderID,
					Msg:           fallbackErr.Error(),
				}
				continue
			}
			result[idx] = futuresExecutionToResult(snapshot, orders[idx])
		}
		return result, nil
	}
	if !isUnknownOrderRequestError(err) {
		return nil, err
	}
	e.noteOrderUncertainty("-", "futures_batch_fallback", fmt.Sprintf("orders=%d err=%v", len(orders), err))

	recovered := make([]FuturesBatchOrderResult, 0, len(orders))
	recoveredCount := 0
	for _, order := range orders {
		if order.NewClientOrderID == "" {
			recovered = append(recovered, FuturesBatchOrderResult{
				Symbol: order.Symbol,
				Msg:    "缺少 clientOrderId，无法幂等回查",
			})
			continue
		}

		queried, queryErr := e.queryFuturesOrderByClientID(ctx, order.Symbol, order.NewClientOrderID)
		if queryErr != nil {
			recovered = append(recovered, FuturesBatchOrderResult{
				Symbol:        order.Symbol,
				ClientOrderID: order.NewClientOrderID,
				Msg:           queryErr.Error(),
			})
			continue
		}

		recoveredCount++
		recovered = append(recovered, queried)
	}

	if recoveredCount > 0 {
		e.logger.Printf("合约批量下单出现不确定状态，已通过 clientOrderId 回查恢复 %d/%d 条订单", recoveredCount, len(orders))
		return recovered, nil
	}

	return nil, err
}
