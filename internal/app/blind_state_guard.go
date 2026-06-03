package app

import (
	"context"
	"fmt"
)

type blindStateError struct {
	Market        string
	Symbol        string
	ClientOrderID string
	Reason        string
}

func (e blindStateError) Error() string {
	return fmt.Sprintf("blind state hard deadline reached: market=%s symbol=%s clientOrderId=%s reason=%s", e.Market, e.Symbol, e.ClientOrderID, e.Reason)
}

func (e *FundingRateSniperEngine) triggerBlindStateEmergencyFlatten(market string, symbol string, clientOrderID string, reason string) {
	if e == nil {
		return
	}
	// 架构简化：放弃异步抢跑平仓，改为安全挂起（Halt），防止竞态反向敞口
	fuseReason := fmt.Sprintf("盲态超时挂起: market=%s symbol=%s clientOrderId=%s reason=%s", market, symbol, clientOrderID, reason)
	_ = e.tripPlatformFuse("blind_state_halt", "critical", fuseReason)
	if e.logger != nil {
		e.logger.Println(fuseReason)
	}
}

func (e *FundingRateSniperEngine) waitForSpotOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, error) {
	if e == nil || e.spotOrderHub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	return waitForConfirmedOrderOrBlindState(
		ctx,
		blindStateHardDeadline,
		"spot",
		symbol,
		clientOrderID,
		func(waitCtx context.Context) (OrderExecutionSnapshot, bool) {
			return e.waitForSpotOrder(waitCtx, symbol, clientOrderID)
		},
		e.triggerBlindStateEmergencyFlatten,
	)
}

func (e *FundingRateSniperEngine) waitForDirectionalSpotOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) (OrderExecutionSnapshot, error) {
	if e == nil {
		return OrderExecutionSnapshot{}, nil
	}
	hub := e.directionalSpotOrderHub(venue)
	if hub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	market := spotExecutionMarketLabel(venue)
	return waitForConfirmedOrderOrBlindState(
		ctx,
		blindStateHardDeadline,
		market,
		symbol,
		clientOrderID,
		func(waitCtx context.Context) (OrderExecutionSnapshot, bool) {
			return e.waitForDirectionalSpotOrder(waitCtx, symbol, clientOrderID, venue)
		},
		e.triggerBlindStateEmergencyFlatten,
	)
}

func (e *FundingRateSniperEngine) waitForFuturesOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, error) {
	if e == nil || e.futuresOrderHub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	return waitForConfirmedOrderOrBlindState(
		ctx,
		blindStateHardDeadline,
		"futures",
		symbol,
		clientOrderID,
		func(waitCtx context.Context) (OrderExecutionSnapshot, bool) {
			return e.waitForFuturesOrder(waitCtx, symbol, clientOrderID)
		},
		e.triggerBlindStateEmergencyFlatten,
	)
}
