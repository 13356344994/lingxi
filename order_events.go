package main

import (
	"context"
	"strings"
	"sync"
	"time"
)

const orderEventSnapshotLimit = 4096

// OrderExecutionSnapshot 表示一个订单在用户数据流中的最新执行状态。
type OrderExecutionSnapshot struct {
	Market          string
	Symbol          string
	ClientOrderID   string
	OrderID         int64
	Status          string
	ExecutionType   string
	ExecutedQty     float64
	CumQuoteQty     float64
	AvgPrice        float64
	Commission      float64
	CommissionAsset string
	RealizedPnL     float64
	EventTime       time.Time
}

// OrderEventHub 用于缓存最新订单回报，并支持基于 clientOrderId 等待。
type OrderEventHub struct {
	mu            sync.Mutex
	snapshots     map[string]OrderExecutionSnapshot
	snapshotOrder []string
	waiters       map[string][]chan OrderExecutionSnapshot
}

// NewOrderEventHub 创建订单回报中心。
func NewOrderEventHub() *OrderEventHub {
	return &OrderEventHub{
		snapshots:     make(map[string]OrderExecutionSnapshot),
		snapshotOrder: make([]string, 0, orderEventSnapshotLimit),
		waiters:       make(map[string][]chan OrderExecutionSnapshot),
	}
}

// Update 写入最新订单状态并唤醒等待者。
func (h *OrderEventHub) Update(snapshot OrderExecutionSnapshot) {
	if strings.TrimSpace(snapshot.ClientOrderID) == "" {
		return
	}

	h.mu.Lock()
	if _, existed := h.snapshots[snapshot.ClientOrderID]; !existed {
		h.snapshotOrder = append(h.snapshotOrder, snapshot.ClientOrderID)
	}
	h.snapshots[snapshot.ClientOrderID] = snapshot
	h.trimSnapshotsLocked()
	waiters := append([]chan OrderExecutionSnapshot(nil), h.waiters[snapshot.ClientOrderID]...)
	h.mu.Unlock()

	for _, waiter := range waiters {
		select {
		case waiter <- snapshot:
		default:
			select {
			case <-waiter:
			default:
			}
			select {
			case waiter <- snapshot:
			default:
			}
		}
	}
}

// Get 返回当前缓存中的订单快照。
func (h *OrderEventHub) Get(clientOrderID string) (OrderExecutionSnapshot, bool) {
	h.mu.Lock()
	snapshot, ok := h.snapshots[clientOrderID]
	h.mu.Unlock()
	return snapshot, ok
}

// Forget 删除某个 clientOrderId 的缓存快照。
func (h *OrderEventHub) Forget(clientOrderID string) {
	if strings.TrimSpace(clientOrderID) == "" {
		return
	}
	h.mu.Lock()
	delete(h.snapshots, clientOrderID)
	h.mu.Unlock()
}

// WaitFor 等待某个 clientOrderId 满足条件。
func (h *OrderEventHub) WaitFor(ctx context.Context, clientOrderID string, predicate func(OrderExecutionSnapshot) bool) (OrderExecutionSnapshot, bool) {
	return h.WaitForWithFallback(ctx, clientOrderID, predicate, 0, 0, nil)
}

// WaitForWithFallback 在等待 WS 回报时支持定时执行兜底查询。
func (h *OrderEventHub) WaitForWithFallback(
	ctx context.Context,
	clientOrderID string,
	predicate func(OrderExecutionSnapshot) bool,
	fallbackDelay time.Duration,
	fallbackInterval time.Duration,
	fallback func(context.Context) (OrderExecutionSnapshot, bool),
) (OrderExecutionSnapshot, bool) {
	if snapshot, ok := h.Get(clientOrderID); ok && predicate(snapshot) {
		h.Forget(clientOrderID)
		return snapshot, true
	}

	waiter := make(chan OrderExecutionSnapshot, 1)

	h.mu.Lock()
	h.waiters[clientOrderID] = append(h.waiters[clientOrderID], waiter)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		waiters := h.waiters[clientOrderID]
		filtered := make([]chan OrderExecutionSnapshot, 0, len(waiters))
		for _, item := range waiters {
			if item != waiter {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 {
			delete(h.waiters, clientOrderID)
		} else {
			h.waiters[clientOrderID] = filtered
		}
		h.mu.Unlock()
	}()

	var fallbackTimer *time.Timer
	var fallbackTicker *time.Ticker
	var fallbackTimerC <-chan time.Time
	var fallbackTickerC <-chan time.Time
	if fallback != nil && fallbackDelay > 0 {
		fallbackTimer = time.NewTimer(fallbackDelay)
		fallbackTimerC = fallbackTimer.C
	}
	defer func() {
		if fallbackTimer != nil && !fallbackTimer.Stop() {
			select {
			case <-fallbackTimer.C:
			default:
			}
		}
		if fallbackTicker != nil {
			fallbackTicker.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return OrderExecutionSnapshot{}, false
		case snapshot := <-waiter:
			if predicate(snapshot) {
				h.Forget(clientOrderID)
				return snapshot, true
			}
		case <-fallbackTimerC:
			fallbackTimerC = nil
			if fallbackInterval <= 0 {
				fallbackInterval = 500 * time.Millisecond
			}
			fallbackTicker = time.NewTicker(fallbackInterval)
			fallbackTickerC = fallbackTicker.C
			if snapshot, ok := h.tryFallbackSnapshot(ctx, fallback, predicate, clientOrderID); ok {
				return snapshot, true
			}
		case <-fallbackTickerC:
			if snapshot, ok := h.tryFallbackSnapshot(ctx, fallback, predicate, clientOrderID); ok {
				return snapshot, true
			}
		}
	}
}

func (h *OrderEventHub) tryFallbackSnapshot(
	ctx context.Context,
	fallback func(context.Context) (OrderExecutionSnapshot, bool),
	predicate func(OrderExecutionSnapshot) bool,
	clientOrderID string,
) (OrderExecutionSnapshot, bool) {
	if fallback == nil {
		return OrderExecutionSnapshot{}, false
	}
	fallbackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snapshot, ok := fallback(fallbackCtx)
	if !ok {
		return OrderExecutionSnapshot{}, false
	}
	if strings.TrimSpace(snapshot.ClientOrderID) == "" {
		snapshot.ClientOrderID = clientOrderID
	}
	h.Update(snapshot)
	if predicate(snapshot) {
		h.Forget(clientOrderID)
		return snapshot, true
	}
	return OrderExecutionSnapshot{}, false
}

func (h *OrderEventHub) trimSnapshotsLocked() {
	for len(h.snapshotOrder) > orderEventSnapshotLimit {
		oldest := h.snapshotOrder[0]
		h.snapshotOrder = trimSnapshotOrderIndex(h.snapshotOrder)
		if _, ok := h.snapshots[oldest]; !ok {
			continue
		}
		if _, waiting := h.waiters[oldest]; waiting {
			h.snapshotOrder = append(h.snapshotOrder, oldest)
			return
		}
		delete(h.snapshots, oldest)
	}
}

func trimSnapshotOrderIndex(items []string) []string {
	if len(items) <= 1 {
		return items[:0]
	}
	trimmed := make([]string, len(items)-1)
	copy(trimmed, items[1:])
	return trimmed
}
