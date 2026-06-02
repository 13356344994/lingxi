package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconcileOrderTerminalStateReturnsConfirmedResult(t *testing.T) {
	attempts := 0
	confirmedCalled := false

	got, err := reconcileOrderTerminalState(
		context.Background(),
		2*time.Second,
		"spot",
		"BTCUSDT",
		"cid-1",
		func(context.Context) (orderTerminalReconcilePollResult[string], error) {
			attempts++
			if attempts == 1 {
				return orderTerminalReconcilePollResult[string]{
					Value: "pending",
					View: ExecutionSemanticView{
						RawStatus:   "NEW",
						Status:      ExecutionStatusNew,
						ExecutedQty: 0,
						Confirmed:   false,
					},
				}, nil
			}
			return orderTerminalReconcilePollResult[string]{
				Value: "filled",
				View: ExecutionSemanticView{
					RawStatus:   "FILLED",
					Status:      ExecutionStatusFilled,
					ExecutedQty: 1,
					Confirmed:   true,
				},
			}, nil
		},
		func(polled orderTerminalReconcilePollResult[string]) {
			confirmedCalled = polled.Value == "filled"
		},
	)
	if err != nil {
		t.Fatalf("reconcileOrderTerminalState() error = %v", err)
	}
	if got != "filled" || !confirmedCalled || attempts != 2 {
		t.Fatalf("unexpected reconcile result: got=%q confirmed=%v attempts=%d", got, confirmedCalled, attempts)
	}
}

func TestReconcileOrderTerminalStateReturnsUnknownOrderStateErrorOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := reconcileOrderTerminalState(
		ctx,
		time.Second,
		"futures",
		"BTCUSDT",
		"cid-timeout",
		func(context.Context) (orderTerminalReconcilePollResult[string], error) {
			return orderTerminalReconcilePollResult[string]{
				Value: "pending",
				View: ExecutionSemanticView{
					RawStatus:   "NEW",
					Status:      ExecutionStatusNew,
					ExecutedQty: 0,
					Confirmed:   false,
				},
			}, nil
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected timeout to become unknownOrderStateError")
	}
	var target unknownOrderStateError
	if !errors.As(err, &target) {
		t.Fatalf("expected unknownOrderStateError, got %T", err)
	}
	if target.Market != "futures" || target.Symbol != "BTCUSDT" || target.ClientOrderID != "cid-timeout" {
		t.Fatalf("unexpected unknown order error: %+v", target)
	}
}

func TestWaitForConfirmedOrderOrBlindStateReturnsConfirmedSnapshot(t *testing.T) {
	expected := OrderExecutionSnapshot{
		Market:        "futures",
		Symbol:        "BTCUSDT",
		ClientOrderID: "cid-confirmed",
		OrderID:       42,
		Status:        "FILLED",
		ExecutedQty:   1.25,
	}
	blindCalled := false

	got, err := waitForConfirmedOrderOrBlindState(
		context.Background(),
		50*time.Millisecond,
		"futures",
		"BTCUSDT",
		"cid-confirmed",
		func(context.Context) (OrderExecutionSnapshot, bool) {
			return expected, true
		},
		func(string, string, string, string) {
			blindCalled = true
		},
	)
	if err != nil {
		t.Fatalf("waitForConfirmedOrderOrBlindState() error = %v", err)
	}
	if blindCalled {
		t.Fatal("expected confirmed wait to skip blind callback")
	}
	if got.OrderID != expected.OrderID || !got.HasConfirmedExecution() {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestWaitForConfirmedOrderOrBlindStateReturnsBlindStateError(t *testing.T) {
	blindCalled := false
	blindReason := ""

	_, err := waitForConfirmedOrderOrBlindState(
		context.Background(),
		50*time.Millisecond,
		"spot",
		"BTCUSDT",
		"cid-blind",
		func(context.Context) (OrderExecutionSnapshot, bool) {
			return OrderExecutionSnapshot{}, false
		},
		func(market string, symbol string, clientOrderID string, reason string) {
			blindCalled = market == "spot" && symbol == "BTCUSDT" && clientOrderID == "cid-blind"
			blindReason = reason
		},
	)
	if err == nil {
		t.Fatal("expected blind-state error")
	}
	var target blindStateError
	if !errors.As(err, &target) {
		t.Fatalf("expected blindStateError, got %T", err)
	}
	if !blindCalled {
		t.Fatal("expected blind callback to be invoked")
	}
	if target.Market != "spot" || target.Symbol != "BTCUSDT" || target.ClientOrderID != "cid-blind" {
		t.Fatalf("unexpected blind error payload: %+v", target)
	}
	if blindReason == "" {
		t.Fatal("expected blind reason to be recorded")
	}
}



func TestResolveFuturesOrderExecutionDoesNotReturnBlindFallbackEmptySnapshot(t *testing.T) {
	engine := &FundingRateSniperEngine{}

	got, err := engine.resolveFuturesOrderExecution(
		context.Background(),
		"BTCUSDT",
		"cid-futures",
		FuturesBatchOrderResult{
			Symbol:        "BTCUSDT",
			ClientOrderID: "cid-futures",
			OrderID:       12345,
			Status:        "FILLED",
			ExecutedQty:   "1.5",
			AvgPrice:      "100",
		},
	)
	if err != nil {
		t.Fatalf("resolveFuturesOrderExecution() error = %v", err)
	}
	if got.OrderID != 12345 {
		t.Fatalf("expected initial confirmed order id, got %+v", got)
	}
	if !got.HasConfirmedExecution() {
		t.Fatalf("expected confirmed execution snapshot, got %+v", got)
	}
}
