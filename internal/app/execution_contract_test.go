package app

import (
	"fmt"
	"testing"
)

func TestNormalizeExecutionStatus(t *testing.T) {
	tests := map[string]ExecutionStatus{
		"FILLED":           ExecutionStatusFilled,
		"PARTIALLY_FILLED": ExecutionStatusPartiallyFilled,
		"CANCELED":         ExecutionStatusCanceled,
		"EXPIRED":          ExecutionStatusExpired,
		"REJECTED":         ExecutionStatusRejected,
		"NEW":              ExecutionStatusNew,
		"weird":            ExecutionStatusUnknown,
	}
	for input, expected := range tests {
		if got := normalizeExecutionStatus(input); got != expected {
			t.Fatalf("normalizeExecutionStatus(%q)=%q want %q", input, got, expected)
		}
	}
}

func TestSemanticViewFromFuturesResultConfirmed(t *testing.T) {
	view := semanticViewFromFuturesResult(FuturesBatchOrderResult{
		Symbol:        "BTCUSDT",
		ClientOrderID: "cid-1",
		OrderID:       1,
		Status:        "PARTIALLY_FILLED",
		ExecutedQty:   "0.2",
	})
	if !view.Confirmed {
		t.Fatalf("expected confirmed view")
	}
	if view.Status != ExecutionStatusPartiallyFilled {
		t.Fatalf("unexpected normalized status: %s", view.Status)
	}
	if view.Terminal {
		t.Fatalf("partial fill should not be terminal")
	}
}

func TestSemanticViewFromExecutionError(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", unknownOrderStateError{
		Market:        "futures",
		Symbol:        "BTCUSDT",
		ClientOrderID: "cid-2",
		Reason:        "reconcile timeout",
	})
	view := semanticViewFromExecutionError(err)
	if !view.Uncertain {
		t.Fatalf("wrapped unknown order state should be uncertain")
	}
	if view.Status != ExecutionStatusUnknown {
		t.Fatalf("unexpected status: %s", view.Status)
	}
}
