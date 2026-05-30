package main

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
		Status:        "FILLED",
		ExecutedQty:   1.5,
	})

	got, ok := hub.WaitFor(context.Background(), "cid-1", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.Status == "FILLED"
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
			Status:        "FILLED",
			ExecutedQty:   2,
		})
	}()

	got, ok := hub.WaitFor(ctx, "cid-2", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.Status == "FILLED"
	})
	<-done
	if !ok {
		t.Fatal("expected filled snapshot to be delivered")
	}
	if got.Status != "FILLED" || got.ExecutedQty != 2 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestOrderEventHubWaitForTimeoutRemovesWaiter(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	got, ok := hub.WaitFor(ctx, "cid-timeout", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.Status == "FILLED"
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
				return snapshot.Status == "FILLED"
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
		Status:        "FILLED",
		ExecutedQty:   3,
	})

	first := <-results
	second := <-results
	if first.Status != "FILLED" || second.Status != "FILLED" {
		t.Fatalf("expected both waiters to receive FILLED, got first=%+v second=%+v", first, second)
	}
}

func TestOrderEventHubUpdateAfterTimeoutDoesNotLeakWaiter(t *testing.T) {
	hub := NewOrderEventHub()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, ok := hub.WaitFor(ctx, "cid-expired", func(snapshot OrderExecutionSnapshot) bool {
		return snapshot.Status == "FILLED"
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
			return snapshot.Status == "FILLED"
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
	if got.Status != "FILLED" || got.OrderID != 12345 {
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
