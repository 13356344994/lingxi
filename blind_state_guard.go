package main

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
	go e.panicFlattenManagedExposure(fmt.Sprintf("blind_state market=%s symbol=%s clientOrderId=%s reason=%s", market, symbol, clientOrderID, reason))
}

func (e *FundingRateSniperEngine) waitForSpotOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, error) {
	if e == nil || e.spotOrderHub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, blindStateHardDeadline)
	defer cancel()
	if snapshot, ok := e.waitForSpotOrder(waitCtx, symbol, clientOrderID); ok && hasConfirmedOrderExecution(snapshot) {
		return snapshot, nil
	}
	if err := waitCtx.Err(); err != nil {
		e.triggerBlindStateEmergencyFlatten("spot", symbol, clientOrderID, err.Error())
		return OrderExecutionSnapshot{}, blindStateError{Market: "spot", Symbol: symbol, ClientOrderID: clientOrderID, Reason: err.Error()}
	}
	return OrderExecutionSnapshot{}, blindStateError{Market: "spot", Symbol: symbol, ClientOrderID: clientOrderID, Reason: "ws/rest blind deadline reached"}
}

func (e *FundingRateSniperEngine) waitForDirectionalSpotOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string, venue spotExecutionVenue) (OrderExecutionSnapshot, error) {
	if e == nil {
		return OrderExecutionSnapshot{}, nil
	}
	hub := e.directionalSpotOrderHub(venue)
	if hub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, blindStateHardDeadline)
	defer cancel()
	if snapshot, ok := e.waitForDirectionalSpotOrder(waitCtx, symbol, clientOrderID, venue); ok && hasConfirmedOrderExecution(snapshot) {
		return snapshot, nil
	}
	market := spotExecutionMarketLabel(venue)
	if err := waitCtx.Err(); err != nil {
		e.triggerBlindStateEmergencyFlatten(market, symbol, clientOrderID, err.Error())
		return OrderExecutionSnapshot{}, blindStateError{Market: market, Symbol: symbol, ClientOrderID: clientOrderID, Reason: err.Error()}
	}
	return OrderExecutionSnapshot{}, blindStateError{Market: market, Symbol: symbol, ClientOrderID: clientOrderID, Reason: "ws/rest blind deadline reached"}
}

func (e *FundingRateSniperEngine) waitForFuturesOrderWithBlindDeadline(ctx context.Context, symbol string, clientOrderID string) (OrderExecutionSnapshot, error) {
	if e == nil || e.futuresOrderHub == nil {
		return OrderExecutionSnapshot{}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, blindStateHardDeadline)
	defer cancel()
	if snapshot, ok := e.waitForFuturesOrder(waitCtx, symbol, clientOrderID); ok && hasConfirmedOrderExecution(snapshot) {
		return snapshot, nil
	}
	if err := waitCtx.Err(); err != nil {
		e.triggerBlindStateEmergencyFlatten("futures", symbol, clientOrderID, err.Error())
		return OrderExecutionSnapshot{}, blindStateError{Market: "futures", Symbol: symbol, ClientOrderID: clientOrderID, Reason: err.Error()}
	}
	return OrderExecutionSnapshot{}, blindStateError{Market: "futures", Symbol: symbol, ClientOrderID: clientOrderID, Reason: "ws/rest blind deadline reached"}
}
