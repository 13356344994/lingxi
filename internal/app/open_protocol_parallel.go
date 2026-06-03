package app

import (
	"context"
	"fmt"
	"strings"
)

// 本文件从 open_protocol.go 拆出：仅承载「并发开仓」(spot/futures 同时下单) 的解析与
// 失败处置协议（同包搬移，行为不变），让 open_protocol.go 满足单文件 ≤ 800 行门禁。

type parallelOpenSpotResult struct {
	order SpotOrderResponse
	err   error
}

type parallelOpenFuturesResult struct {
	snapshot OrderExecutionSnapshot
	err      error
}

type parallelOpenResolution struct {
	spotClientOrderID string
	spotPriceForCheck float64
	spotResult        parallelOpenSpotResult
	futuresResult     parallelOpenFuturesResult
}

func (r parallelOpenResolution) filledFuturesQty() float64 {
	return r.futuresResult.snapshot.ExecutedQty
}

type parallelOpenDecisionOutcome string

const (
	parallelOpenDecisionProceed         parallelOpenDecisionOutcome = "proceed"
	parallelOpenDecisionFuturesFailed   parallelOpenDecisionOutcome = "futures_failed"
	parallelOpenDecisionZeroFuturesFill parallelOpenDecisionOutcome = "zero_futures_fill"
	parallelOpenDecisionSpotFailed      parallelOpenDecisionOutcome = "spot_failed"
)

type parallelOpenDecision struct {
	Outcome          parallelOpenDecisionOutcome
	FilledFuturesQty float64
}

type parallelOpenProtocol struct {
	engine     *FundingRateSniperEngine
	plan       openSequencePlan
	direction  fundingCarryDirection
	resolution parallelOpenResolution
}

func (r parallelOpenResolution) decision() parallelOpenDecision {
	filledFuturesQty := r.filledFuturesQty()
	switch {
	case r.futuresResult.err != nil:
		return parallelOpenDecision{Outcome: parallelOpenDecisionFuturesFailed, FilledFuturesQty: filledFuturesQty}
	case filledFuturesQty <= executionQtyTolerance:
		return parallelOpenDecision{Outcome: parallelOpenDecisionZeroFuturesFill, FilledFuturesQty: filledFuturesQty}
	case r.spotResult.err != nil:
		return parallelOpenDecision{Outcome: parallelOpenDecisionSpotFailed, FilledFuturesQty: filledFuturesQty}
	default:
		return parallelOpenDecision{Outcome: parallelOpenDecisionProceed, FilledFuturesQty: filledFuturesQty}
	}
}

func (p *OpenProtocol) executeParallelOpenOrders(ctx context.Context, plan openSequencePlan, direction fundingCarryDirection) (parallelOpenResolution, error) {
	target := plan.target
	spotQty, spotPriceForCheck, spotTradable := p.openSpotTarget(target, target.Candidate.OrderQuantity)
	if !spotTradable {
		return parallelOpenResolution{}, fmt.Errorf("并发对冲的现货目标数量无效")
	}
	spotClientOrderID := newClientOrderID("sopen", target.Candidate.Pair)

	spotResultCh := make(chan parallelOpenSpotResult, 1)
	futuresResultCh := make(chan parallelOpenFuturesResult, 1)
	go func() {
		order, err := p.engine.placeDirectionalSpotMarketOrderIdempotent(ctx, target.Candidate.Pair, direction.openSpotSide(), spotQty, spotClientOrderID, direction.spotVenue(), "spot_market_"+strings.ToLower(direction.openSpotSide()), true)
		spotResultCh <- parallelOpenSpotResult{order: order, err: err}
	}()
	go func() {
		snapshot, err := p.engine.placeSingleFuturesOpenOrderIdempotent(ctx, plan.futuresOrder)
		futuresResultCh <- parallelOpenFuturesResult{snapshot: snapshot, err: err}
	}()

	return parallelOpenResolution{
		spotClientOrderID: spotClientOrderID,
		spotPriceForCheck: spotPriceForCheck,
		spotResult:        <-spotResultCh,
		futuresResult:     <-futuresResultCh,
	}, nil
}

func (p *OpenProtocol) applyParallelOpenDecision(ctx context.Context, plan openSequencePlan, direction fundingCarryDirection, resolution parallelOpenResolution, decision parallelOpenDecision) error {
	protocol := parallelOpenProtocol{
		engine:     p.engine,
		plan:       plan,
		direction:  direction,
		resolution: resolution,
	}
	return protocol.execute(ctx, decision)
}

func (p parallelOpenProtocol) execute(ctx context.Context, decision parallelOpenDecision) error {
	switch decision.Outcome {
	case parallelOpenDecisionFuturesFailed:
		return p.handleFuturesFailure(ctx)
	case parallelOpenDecisionZeroFuturesFill:
		return p.handleZeroFuturesFill(ctx)
	case parallelOpenDecisionSpotFailed:
		return p.handleSpotFailure(ctx, decision.FilledFuturesQty)
	default:
		return nil
	}
}

func (p parallelOpenProtocol) handleFuturesFailure(ctx context.Context) error {
	target := p.plan.target
	futuresErr := p.resolution.futuresResult.err
	if isUnknownOrderStateError(futuresErr) {
		return p.engine.scheduleOpenExposureRescueForTargetOrFail(
			ctx,
			target,
			p.direction,
			"parallel_open_futures_unknown",
			p.resolution.spotPriceForCheck,
			p.resolution.spotClientOrderID,
			p.plan.futuresOrder.NewClientOrderID,
			0,
			0,
			"并发合约开仓终态未知且 ExposureManager 托管失败",
			"并发合约开仓终态未知，已转入 ExposureManager 托管",
		)
	}
	if p.resolution.spotResult.err == nil {
		if err := p.revertSpotOrRescue(ctx, "parallel_open_revert_spot_failed", "并发合约开仓失败且现货回滚失败", "并发合约开仓失败且现货回滚失败，已转入 ExposureManager"); err != nil {
			return err
		}
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, p.engine.cfg.ExecutionRecoveryTimeout)
	defer cancel()
	cleanup := p.engine.flattenUncertainFuturesOpen(cleanupCtx, target.Candidate.Symbol, p.plan.futuresOrder.NewClientOrderID, FuturesBatchOrderResult{
		Symbol:        p.plan.futuresOrder.Symbol,
		ClientOrderID: p.plan.futuresOrder.NewClientOrderID,
	})
	return cleanup.openError("并发合约空单未确认成交", futuresErr)
}

func (p parallelOpenProtocol) handleZeroFuturesFill(ctx context.Context) error {
	if p.resolution.spotResult.err == nil {
		if err := p.revertSpotOrRescue(ctx, "parallel_open_zero_fill_revert_failed", "并发合约零成交且现货回滚失败", "并发合约零成交且现货回滚失败，已转入 ExposureManager"); err != nil {
			return err
		}
	}
	return fmt.Errorf("并发合约空单成交数量为 0")
}

func (p parallelOpenProtocol) handleSpotFailure(ctx context.Context, filledFuturesQty float64) error {
	target := p.plan.target
	spotErr := p.resolution.spotResult.err
	if isUnknownOrderStateError(spotErr) {
		return p.engine.scheduleOpenExposureRescueForTargetOrFail(
			ctx,
			target,
			p.direction,
			"parallel_open_spot_unknown",
			p.resolution.spotPriceForCheck,
			p.resolution.spotClientOrderID,
			p.plan.futuresOrder.NewClientOrderID,
			0,
			filledFuturesQty,
			"并发现货开仓终态未知且 ExposureManager 托管失败",
			"并发现货开仓终态未知，已转入 ExposureManager 托管",
		)
	}
	result, err := p.engine.executeOpenFuturesRevertProtocol(ctx, target.Candidate.Symbol, filledFuturesQty, p.direction, "close_futures_after_parallel_spot_open_failure", fmt.Sprintf("filledFuturesQty=%.8f spotErr=%v", filledFuturesQty, spotErr))
	if err != nil {
		rescueErr := p.engine.scheduleOpenExposureRescueForTargetOrFail(
			ctx,
			target,
			p.direction,
			"parallel_open_spot_failure_close_futures_failed",
			p.resolution.spotPriceForCheck,
			"",
			p.plan.futuresOrder.NewClientOrderID,
			0,
			filledFuturesQty,
			"并发现货补仓失败且合约回补失败，ExposureManager 也失败",
			"并发现货补仓失败且合约回补失败，已转入 ExposureManager",
		)
		if rescueErr != nil {
			return fmt.Errorf("spotErr=%v recoverErr=%v rescueErr=%w", spotErr, err, rescueErr)
		}
		return fmt.Errorf("并发现货补仓失败且合约回补失败，已转入 ExposureManager")
	}
	return result.openError("并发现货补仓失败，已回补合约空单", spotErr)
}

func (p parallelOpenProtocol) revertSpotOrRescue(ctx context.Context, phase string, failureMessage string, scheduledMessage string) error {
	target := p.plan.target
	spotOrder := p.resolution.spotResult.order
	if err := p.revertSpotOpenAfterFuturesFailure(ctx, target, spotOrder, p.resolution.spotPriceForCheck); err != nil {
		rescueErr := p.engine.scheduleExposureRescue(ctx, buildOpenExposureRescueTask(
			target,
			p.direction,
			phase,
			p.resolution.spotPriceForCheck,
			nonEmpty(spotOrder.ClientOrderID, p.resolution.spotClientOrderID),
			"",
			spotOrder.ExecutedBaseQty(target.Candidate.SpotBaseAsset),
			0,
		))
		if rescueErr != nil {
			return fmt.Errorf("%s，ExposureManager 也失败: rollbackErr=%v rescueErr=%w", failureMessage, err, rescueErr)
		}
		return fmt.Errorf("%s: %w", scheduledMessage, err)
	}
	return nil
}
