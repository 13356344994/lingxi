package app

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestOrderEventHubWaitForImmediateSnapshot(t *testing.T) {
	hub := NewOrderEventHub()
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-1",
		OrderID:       1001,
		Status:        "FILLED",
		ExecutedQty:   1.5,
	})

	got, ok := hub.WaitFor(context.Background(), "cid-1", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.HasConfirmedExecution()
	})
	if !ok {
		t.Fatal("expected snapshot to be found")
	}
	if got.ClientOrderID != "cid-1" || got.ExecutedQty != 1.5 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if _, stillCached := hub.Get("cid-1"); stillCached {
		t.Fatal("expected snapshot to be forgotten after successful wait")
	}
}

func TestOrderEventHubWaitForAsyncUpdate(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		hub.Update(OrderExecutionSnapshot{
			ClientOrderID: "cid-2",
			Status:        "PARTIALLY_FILLED",
		})
		time.Sleep(20 * time.Millisecond)
		hub.Update(OrderExecutionSnapshot{
			ClientOrderID: "cid-2",
			OrderID:       2002,
			Status:        "FILLED",
			ExecutedQty:   2,
		})
	}()

	got, ok := hub.WaitFor(ctx, "cid-2", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.HasConfirmedExecution()
	})
	<-done
	if !ok {
		t.Fatal("expected filled snapshot to be delivered")
	}
	if !got.HasConfirmedExecution() || got.ExecutedQty != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestOrderEventHubWaitForTimeoutRemovesWaiter(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	got, ok := hub.WaitFor(ctx, "cid-timeout", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.HasConfirmedExecution()
	})
	if ok {
		t.Fatalf("expected timeout, got snapshot: %+v", got)
	}

	hub.mu.Lock()
	waiters := len(hub.waiters["cid-timeout"])
	hub.mu.Unlock()
	if waiters != 0 {
		t.Fatalf("expected waiter to be removed after timeout, got %d", waiters)
	}
}

func TestOrderEventHubWaitForSupportsMultipleWaiters(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	results := make(chan OrderExecutionSnapshot, 2)
	for range 2 {
		go func() {
			got, ok := hub.WaitFor(ctx, "cid-multi", func(snapshot OrderExecutionSnapshot) bool {
				return snapshot.HasConfirmedExecution()
			})
			if !ok {
				results <- OrderExecutionSnapshot{}
				return
			}
			results <- got
		}()
	}

	time.Sleep(20 * time.Millisecond)
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-multi",
		OrderID:       3003,
		Status:        "FILLED",
		ExecutedQty:   3,
	})

	first := <-results
	second := <-results
	if !first.HasConfirmedExecution() || !second.HasConfirmedExecution() {
		t.Fatalf("expected both waiters to receive FILLED, got first=%+v second=%+v", first, second)
	}
}

func TestOrderEventHubUpdateAfterTimeoutDoesNotLeakWaiter(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, ok := hub.WaitFor(ctx, "cid-expired", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.HasConfirmedExecution()
	}); ok {
		t.Fatal("expected timeout before filled event")
	}

	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-expired",
		Status:        "FILLED",
	})

	hub.mu.Lock()
	waiters := len(hub.waiters["cid-expired"])
	hub.mu.Unlock()
	if waiters != 0 {
		t.Fatalf("expected no waiter leak after timeout and update, got %d", waiters)
	}
}

func TestOrderEventHubWaitForWithFallbackPoll(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	polls := 0
	got, ok := hub.WaitForWithFallback(
		ctx,
		"cid-fallback",
		func(snapshot OrderExecutionSnapshot) bool {
			return snapshot.HasConfirmedExecution()
		},
		10*time.Millisecond,
		10*time.Millisecond,
		func(context.Context) (OrderExecutionSnapshot, bool) {
			polls++
			if polls < 2 {
				return OrderExecutionSnapshot{}, false
			}
			return OrderExecutionSnapshot{
				ClientOrderID: "cid-fallback",
				Status:        "FILLED",
				OrderID:       12345,
				ExecutedQty:   1.25,
			}, true
		},
	)
	if !ok {
		t.Fatal("expected fallback snapshot to satisfy waiter")
	}
	if !got.HasConfirmedExecution() || got.OrderID != 12345 {
		t.Fatalf("unexpected fallback snapshot: %+v", got)
	}
	if polls < 2 {
		t.Fatalf("expected fallback polling to run, polls=%d", polls)
	}
}

func TestOrderEventHubTrimSnapshotsLockedCompactsBackingSlice(t *testing.T) {
	hub := NewOrderEventHub()
	for i := range orderEventSnapshotLimit + 32 {
		hub.Update(OrderExecutionSnapshot{
			ClientOrderID: "cid-" + strconv.Itoa(i),
			Status:        "FILLED",
		})
	}
	if len(hub.snapshotOrder) != orderEventSnapshotLimit {
		t.Fatalf("expected snapshot order length %d, got %d", orderEventSnapshotLimit, len(hub.snapshotOrder))
	}
	if cap(hub.snapshotOrder) > orderEventSnapshotLimit+1 {
		t.Fatalf("expected trimmed slice capacity to stay compact, got %d", cap(hub.snapshotOrder))
	}
}

func TestOrderEventHubTrimSnapshotsLockedSkipsWaitingSnapshotAndContinuesEviction(t *testing.T) {
	hub := NewOrderEventHub()
	waiter := make(chan OrderExecutionSnapshot, 1)

	hub.mu.Lock()
	hub.waiters["cid-0"] = []chan OrderExecutionSnapshot{waiter}
	hub.mu.Unlock()

	for i := range orderEventSnapshotLimit + 4 {
		hub.Update(OrderExecutionSnapshot{
			ClientOrderID: "cid-" + strconv.Itoa(i),
			Status:        "FILLED",
		})
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.snapshotOrder) != orderEventSnapshotLimit {
		t.Fatalf("expected snapshot order length %d, got %d", orderEventSnapshotLimit, len(hub.snapshotOrder))
	}
	if _, ok := hub.snapshots["cid-0"]; !ok {
		t.Fatal("expected waiting snapshot to be retained")
	}
	if _, ok := hub.snapshots["cid-1"]; ok {
		t.Fatal("expected non-waiting oldest snapshot cid-1 to be evicted")
	}
	if _, ok := hub.snapshots["cid-2"]; ok {
		t.Fatal("expected non-waiting oldest snapshot cid-2 to be evicted")
	}
}

func TestOrderEventHubDoesNotDowngradeTerminalStatusOnOutOfOrderEvent(t *testing.T) {
	hub := NewOrderEventHub()
	filledAt := time.UnixMilli(2000)
	newAt := time.UnixMilli(1000)
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-terminal",
		Status:        "FILLED",
		ExecutedQty:   1,
		CumQuoteQty:   100,
		EventTime:     filledAt,
	})
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-terminal",
		Status:        "NEW",
		EventTime:     newAt,
	})

	got, ok := hub.Get("cid-terminal")
	if !ok {
		t.Fatal("expected snapshot to remain cached")
	}
	if got.ExecutionStatus() != ExecutionStatusFilled {
		t.Fatalf("expected terminal status to be retained, got %+v", got)
	}
	if !got.EventTime.Equal(filledAt) {
		t.Fatalf("expected terminal event time %s, got %s", filledAt, got.EventTime)
	}
}

func TestOrderEventHubPrefersNewerTerminalSnapshot(t *testing.T) {
	hub := NewOrderEventHub()
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-latest",
		Status:        "NEW",
		EventTime:     time.UnixMilli(1000),
	})
	hub.Update(OrderExecutionSnapshot{
		ClientOrderID: "cid-latest",
		Status:        "FILLED",
		ExecutedQty:   2,
		CumQuoteQty:   200,
		EventTime:     time.UnixMilli(2000),
	})

	got, ok := hub.Get("cid-latest")
	if !ok {
		t.Fatal("expected snapshot to remain cached")
	}
	if got.ExecutionStatus() != ExecutionStatusFilled || got.ExecutedQty != 2 {
		t.Fatalf("expected newer terminal snapshot to win, got %+v", got)
	}
}

func TestOrderEventHubForgetRemovesSnapshotOrderEntries(t *testing.T) {
	hub := NewOrderEventHub()
	hub.Update(OrderExecutionSnapshot{ClientOrderID: "cid-1", Status: "FILLED"})
	hub.Update(OrderExecutionSnapshot{ClientOrderID: "cid-2", Status: "FILLED"})

	hub.Forget("cid-1")

	if _, ok := hub.Get("cid-1"); ok {
		t.Fatal("expected forgotten snapshot to be removed from cache")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.snapshotOrder) != 1 {
		t.Fatalf("expected snapshot order to contain one entry after forget, got %d", len(hub.snapshotOrder))
	}
	if hub.snapshotOrder[0] != "cid-2" {
		t.Fatalf("expected remaining snapshot order to be cid-2, got %+v", hub.snapshotOrder)
	}
}

func TestOrderExecutionSnapshotSemanticHelpers(t *testing.T) {
	snapshot := OrderExecutionSnapshot{
		Market:        "spot",
		Symbol:        "BTCUSDT",
		ClientOrderID: "cid-helpers",
		OrderID:       42,
		Status:        "FILLED",
		ExecutedQty:   1.2,
		CumQuoteQty:   120,
	}

	if !snapshot.HasConfirmedExecution() {
		t.Fatalf("expected snapshot to be confirmed: %+v", snapshot)
	}
	if !snapshot.IsTerminal() || !snapshot.IsClosed() {
		t.Fatalf("expected filled snapshot to be terminal/closed: %+v", snapshot)
	}
	if snapshot.ExecutionStatus() != ExecutionStatusFilled {
		t.Fatalf("unexpected execution status: %s", snapshot.ExecutionStatus())
	}
	if !snapshot.IsPartialFill(2) {
		t.Fatalf("expected snapshot to be partial against requested qty")
	}
}

func TestProtectedIOCObservationSemantics(t *testing.T) {
	expired := newProtectedIOCObservation(2, 0, "EXPIRED")
	if !expired.ExhaustedWithoutFill() || !expired.NeedsAttention() || !expired.Expired {
		t.Fatalf("unexpected expired protected ioc observation: %+v", expired)
	}

	partial := newProtectedIOCObservation(2, 1, "FILLED")
	if partial.ExhaustedWithoutFill() || !partial.NeedsAttention() || !partial.Partial {
		t.Fatalf("unexpected partial protected ioc observation: %+v", partial)
	}

	rejected := newProtectedIOCObservation(2, 0, "REJECTED")
	if !rejected.ExhaustedWithoutFill() || rejected.NeedsAttention() {
		t.Fatalf("unexpected rejected protected ioc observation: %+v", rejected)
	}
}

func TestSpotOrderResponseDomainHelpers(t *testing.T) {
	order := SpotOrderResponse{
		Symbol:              "BTCUSDT",
		OrderID:             99,
		Status:              "FILLED",
		ClientOrderID:       "cid-spot-domain",
		ExecutedQty:         "1.5",
		CummulativeQuoteQty: "150",
		Fills: []SpotFill{
			{Qty: "1.2"},
			{Qty: "0.3"},
		},
	}

	if !order.HasConfirmedExecution() || !order.IsClosed() {
		t.Fatalf("expected spot order helpers to expose confirmed closed semantics: %+v", order)
	}
	if got := order.ExecutedBaseQty("BTC"); got != 1.5 {
		t.Fatalf("expected executed base qty 1.5, got %.8f", got)
	}
	observation := order.ProtectedIOCObservation(2)
	if !observation.Partial || !observation.NeedsAttention() {
		t.Fatalf("expected partial protected ioc observation from spot order: %+v", observation)
	}
}

func TestFuturesBatchOrderResultDomainHelpers(t *testing.T) {
	result := FuturesBatchOrderResult{
		Symbol:        "BTCUSDT",
		OrderID:       123,
		Status:        "EXPIRED",
		ClientOrderID: "cid-futures-domain",
		ExecutedQty:   "0",
	}

	if !result.HasConfirmedExecution() || !result.IsClosed() {
		t.Fatalf("expected futures result helpers to expose confirmed closed semantics: %+v", result)
	}
	if got := result.ExecutedQtyValue(); got != 0 {
		t.Fatalf("expected executed qty 0, got %.8f", got)
	}
	observation := result.ProtectedIOCObservation(1)
	if !observation.ExhaustedWithoutFill() || !observation.Expired {
		t.Fatalf("expected exhausted expired observation from futures result: %+v", observation)
	}
}

func TestProtectedIOCFallbackDomainHelpers(t *testing.T) {
	spot := SpotOrderResponse{
		Status:      "EXPIRED",
		ExecutedQty: "0",
	}
	if !spot.RequiresProtectedOpenFallback(1) {
		t.Fatal("expected zero-fill expired spot order to require fallback")
	}

	futures := FuturesBatchOrderResult{
		Code:        -1013,
		Msg:         "Filter failure: PERCENT_PRICE",
		Status:      "REJECTED",
		ExecutedQty: "0",
	}
	if !futures.RejectedByPercentPriceFilter() {
		t.Fatal("expected percent-price rejection to be recognized")
	}
	if !futures.RequiresProtectedOpenFallback(1) {
		t.Fatal("expected rejected futures protected order to require fallback")
	}

	snapshot := OrderExecutionSnapshot{
		Status:      "PARTIALLY_FILLED",
		ExecutedQty: 0.4,
	}
	if snapshot.RequiresProtectedOpenFallback(1) {
		t.Fatal("expected partial execution snapshot to avoid fallback")
	}
}
