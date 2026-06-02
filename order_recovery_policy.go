package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type orderTerminalReconcilePollResult[T any] struct {
	Value T
	View  ExecutionSemanticView
}

func waitForConfirmedOrderOrBlindState(
	ctx context.Context,
	hardDeadline time.Duration,
	market string,
	symbol string,
	clientOrderID string,
	wait func(context.Context) (OrderExecutionSnapshot, bool),
	onBlind func(market string, symbol string, clientOrderID string, reason string),
) (OrderExecutionSnapshot, error) {
	if wait == nil {
		return OrderExecutionSnapshot{}, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, hardDeadline)
	defer cancel()
	if snapshot, ok := wait(waitCtx); ok && snapshot.HasConfirmedExecution() {
		return snapshot, nil
	}
	reason := "ws/rest blind deadline reached"
	if err := waitCtx.Err(); err != nil {
		reason = err.Error()
	}
	if onBlind != nil {
		onBlind(market, symbol, clientOrderID, reason)
	}
	return OrderExecutionSnapshot{}, blindStateError{
		Market:        market,
		Symbol:        symbol,
		ClientOrderID: clientOrderID,
		Reason:        reason,
	}
}

func resolveExecutionSnapshotWithPolling[T any](
	ctx context.Context,
	symbol string,
	clientOrderID string,
	initial T,
	semantic func(T) ExecutionSemanticView,
	query func(context.Context) (T, error),
	reconcile func(context.Context) (T, error),
	toSnapshot func(T) OrderExecutionSnapshot,
	unresolvedErr func(symbol string, clientOrderID string) error,
) (OrderExecutionSnapshot, error) {
	// 架构简化：放弃多次 polling 的灰状态猜测，直接校验当前视图，若未确认则抛出错误交由上层熔断
	view := semantic(initial)
	if view.Confirmed {
		return toSnapshot(initial), nil
	}
	if unresolvedErr != nil {
		return OrderExecutionSnapshot{}, unresolvedErr(symbol, clientOrderID)
	}
	return OrderExecutionSnapshot{}, fmt.Errorf("未能确认订单真实成交数量, symbol=%s clientOrderId=%s", symbol, clientOrderID)
}

func reconcileOrderTerminalState[T any](
	ctx context.Context,
	timeout time.Duration,
	market string,
	symbol string,
	clientOrderID string,
	query func(context.Context) (orderTerminalReconcilePollResult[T], error),
	onConfirmed func(orderTerminalReconcilePollResult[T]),
) (T, error) {
	var zero T
	clientOrderID = strings.TrimSpace(clientOrderID)
	if clientOrderID == "" {
		return zero, unknownOrderStateError{Market: market, Symbol: symbol, Reason: "clientOrderId 为空"}
	}

	reconcileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	backoff := 250 * time.Millisecond
	var lastErr error
	for {
		polled, err := query(reconcileCtx)
		if err == nil && polled.View.Confirmed {
			if onConfirmed != nil {
				onConfirmed(polled)
			}
			return polled.Value, nil
		}
		if err == nil {
			lastErr = fmt.Errorf(
				"%s status=%s normalized=%s executedQty=%.8f",
				nonEmpty(strings.TrimSpace(market), "order"),
				nonEmpty(strings.TrimSpace(polled.View.RawStatus), "UNKNOWN"),
				nonEmpty(string(polled.View.Status), string(ExecutionStatusUnknown)),
				polled.View.ExecutedQty,
			)
		} else {
			lastErr = err
		}
		if reconcileCtx.Err() != nil {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-reconcileCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
			if backoff > time.Second {
				backoff = time.Second
			}
		}
	}
	return zero, unknownOrderStateError{
		Market:        market,
		Symbol:        symbol,
		ClientOrderID: clientOrderID,
		Reason:        nonEmpty(errorString(lastErr), "reconcile timeout"),
	}
}
