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

// ProtectedIOCObservation 是保护 IOC 订单结果的统一语义视图。
type ProtectedIOCObservation struct {
	RequestedQty  float64
	ExecutedQty   float64
	RawStatus     string
	Status        ExecutionStatus
	UnfilledRatio float64
	Partial       bool
	Canceled      bool
	Expired       bool
	Rejected      bool
}

func (s OrderExecutionSnapshot) SemanticView() ExecutionSemanticView {
	return semanticViewFromSnapshot(s)
}

func (s OrderExecutionSnapshot) ExecutionStatus() ExecutionStatus {
	return s.SemanticView().Status
}

func (s OrderExecutionSnapshot) IsTerminal() bool {
	return s.SemanticView().Terminal
}

func (s OrderExecutionSnapshot) IsClosed() bool {
	return s.SemanticView().Closed
}

func (s OrderExecutionSnapshot) HasConfirmedExecution() bool {
	return s.SemanticView().Confirmed
}

func (s OrderExecutionSnapshot) HasExecutionProgress() bool {
	return s.ExecutedQty > executionQtyTolerance
}

func (s OrderExecutionSnapshot) IsPartialFill(requestedQty float64) bool {
	if requestedQty <= executionQtyTolerance {
		return false
	}
	return s.ExecutedQty > executionQtyTolerance && s.ExecutedQty+executionQtyTolerance < requestedQty
}

func (s OrderExecutionSnapshot) ProtectedIOCObservation(requestedQty float64) ProtectedIOCObservation {
	return protectedIOCObservationFromView(requestedQty, s.SemanticView())
}

func (s OrderExecutionSnapshot) RequiresProtectedOpenFallback(requestedQty float64) bool {
	return s.ProtectedIOCObservation(requestedQty).RequiresFallback()
}

func (o SpotOrderResponse) SemanticView() ExecutionSemanticView {
	return semanticViewFromSpotOrder(o)
}

func (o SpotOrderResponse) HasConfirmedExecution() bool {
	return o.SemanticView().Confirmed
}

func (o SpotOrderResponse) IsClosed() bool {
	return o.SemanticView().Closed
}

func (o SpotOrderResponse) IsTerminal() bool {
	return o.SemanticView().Terminal
}

func (o SpotOrderResponse) ExecutedBaseQty(baseAsset string) float64 {
	if len(o.Fills) > 0 {
		return resolveSpotNetBaseQuantity(o, baseAsset)
	}
	return maxFloat(o.SemanticView().ExecutedQty, 0)
}

func (o SpotOrderResponse) ProtectedIOCObservation(requestedQty float64) ProtectedIOCObservation {
	return protectedIOCObservationFromView(requestedQty, o.SemanticView())
}

func (o SpotOrderResponse) RequiresProtectedOpenFallback(requestedQty float64) bool {
	return o.ProtectedIOCObservation(requestedQty).RequiresFallback()
}

func (r FuturesBatchOrderResult) SemanticView() ExecutionSemanticView {
	return semanticViewFromFuturesResult(r)
}

func (r FuturesBatchOrderResult) HasConfirmedExecution() bool {
	return r.SemanticView().Confirmed
}

func (r FuturesBatchOrderResult) IsClosed() bool {
	return r.SemanticView().Closed
}

func (r FuturesBatchOrderResult) IsTerminal() bool {
	return r.SemanticView().Terminal
}

func (r FuturesBatchOrderResult) ExecutedQtyValue() float64 {
	return maxFloat(r.SemanticView().ExecutedQty, 0)
}

func (r FuturesBatchOrderResult) ProtectedIOCObservation(requestedQty float64) ProtectedIOCObservation {
	return protectedIOCObservationFromView(requestedQty, r.SemanticView())
}

func (r FuturesBatchOrderResult) RejectedByPercentPriceFilter() bool {
	return r.Code != 0 && isPercentPriceFilterMessage(r.Msg)
}

func (r FuturesBatchOrderResult) RequiresProtectedOpenFallback(requestedQty float64) bool {
	return r.ProtectedIOCObservation(requestedQty).RequiresFallback() || r.RejectedByPercentPriceFilter()
}

func protectedIOCObservationFromView(requestedQty float64, view ExecutionSemanticView) ProtectedIOCObservation {
	return newProtectedIOCObservation(requestedQty, view.ExecutedQty, view.RawStatus)
}

func newProtectedIOCObservation(requestedQty float64, executedQty float64, rawStatus string) ProtectedIOCObservation {
	status := normalizeExecutionStatus(rawStatus)
	return ProtectedIOCObservation{
		RequestedQty:  requestedQty,
		ExecutedQty:   maxFloat(executedQty, 0),
		RawStatus:     strings.TrimSpace(rawStatus),
		Status:        status,
		UnfilledRatio: protectedIOCUnfilledRatio(requestedQty, executedQty),
		Partial:       requestedQty > executionQtyTolerance && executedQty > executionQtyTolerance && executedQty+executionQtyTolerance < requestedQty,
		Canceled:      status == ExecutionStatusCanceled,
		Expired:       status == ExecutionStatusExpired,
		Rejected:      status == ExecutionStatusRejected,
	}
}

func (o ProtectedIOCObservation) NeedsAttention() bool {
	return o.Canceled || o.Expired || o.Partial
}

func (o ProtectedIOCObservation) ExhaustedWithoutFill() bool {
	return o.ExecutedQty <= executionQtyTolerance && (o.Canceled || o.Expired || o.Rejected)
}

func (o ProtectedIOCObservation) RequiresFallback() bool {
	return o.ExhaustedWithoutFill()
}

func isPercentPriceFilterMessage(message string) bool {
	text := strings.ToUpper(strings.TrimSpace(message))
	return strings.Contains(text, "PERCENT_PRICE") || strings.Contains(text, "PERCENT_PRICE_BY_SIDE")
}

// OrderEventHub 用于缓存最新订单回报，并支持基于 clientOrderId 等待。
type OrderEventHub struct {
	mu            sync.Mutex
	snapshots     map[string]OrderExecutionSnapshot
	snapshotOrder []string
	waiters       map[string][]chan OrderExecutionSnapshot
}

func normalizedOrderStatus(status string) string {
	return strings.ToUpper(strings.TrimSpace(status))
}

func orderStatusRank(status string) int {
	switch normalizeExecutionStatus(status) {
	case ExecutionStatusFilled:
		return 5
	case ExecutionStatusCanceled, ExecutionStatusExpired, ExecutionStatusRejected:
		return 4
	case ExecutionStatusPartiallyFilled:
		return 3
	case ExecutionStatusNew:
		return 2
	default:
		return 1
	}
}

func shouldReplaceOrderSnapshot(current OrderExecutionSnapshot, incoming OrderExecutionSnapshot) bool {
	if strings.TrimSpace(current.ClientOrderID) == "" {
		return true
	}
	currentTerminal := current.IsTerminal()
	incomingTerminal := incoming.IsTerminal()
	if !incoming.EventTime.IsZero() && !current.EventTime.IsZero() {
		if incoming.EventTime.After(current.EventTime) {
			if currentTerminal && !incomingTerminal {
				return false
			}
			return true
		}
		if incoming.EventTime.Before(current.EventTime) {
			return false
		}
	}
	if currentTerminal && !incomingTerminal {
		return false
	}
	if incomingTerminal && !currentTerminal {
		return true
	}
	if orderStatusRank(incoming.Status) != orderStatusRank(current.Status) {
		return orderStatusRank(incoming.Status) > orderStatusRank(current.Status)
	}
	if incoming.ExecutedQty > current.ExecutedQty {
		return true
	}
	if incoming.CumQuoteQty > current.CumQuoteQty {
		return true
	}
	if current.OrderID == 0 && incoming.OrderID != 0 {
		return true
	}
	return false
}

func mergeOrderExecutionSnapshots(current OrderExecutionSnapshot, incoming OrderExecutionSnapshot) OrderExecutionSnapshot {
	if strings.TrimSpace(current.ClientOrderID) == "" {
		return incoming
	}
	merged := current
	if shouldReplaceOrderSnapshot(current, incoming) {
		merged = incoming
	}
	if strings.TrimSpace(merged.Market) == "" && strings.TrimSpace(incoming.Market) != "" {
		merged.Market = incoming.Market
	}
	if strings.TrimSpace(merged.Symbol) == "" && strings.TrimSpace(incoming.Symbol) != "" {
		merged.Symbol = incoming.Symbol
	}
	if strings.TrimSpace(merged.ClientOrderID) == "" && strings.TrimSpace(incoming.ClientOrderID) != "" {
		merged.ClientOrderID = incoming.ClientOrderID
	}
	if merged.OrderID == 0 && incoming.OrderID != 0 {
		merged.OrderID = incoming.OrderID
	}
	if merged.ExecutedQty < incoming.ExecutedQty {
		merged.ExecutedQty = incoming.ExecutedQty
	}
	if merged.CumQuoteQty < incoming.CumQuoteQty {
		merged.CumQuoteQty = incoming.CumQuoteQty
	}
	if merged.AvgPrice <= 0 && incoming.AvgPrice > 0 {
		merged.AvgPrice = incoming.AvgPrice
	}
	if merged.Commission < incoming.Commission {
		merged.Commission = incoming.Commission
	}
	if strings.TrimSpace(merged.CommissionAsset) == "" && strings.TrimSpace(incoming.CommissionAsset) != "" {
		merged.CommissionAsset = incoming.CommissionAsset
	}
	if mathAbs(merged.RealizedPnL) < mathAbs(incoming.RealizedPnL) {
		merged.RealizedPnL = incoming.RealizedPnL
	}
	if merged.EventTime.IsZero() || (!incoming.EventTime.IsZero() && incoming.EventTime.After(merged.EventTime) && !merged.IsTerminal()) {
		merged.EventTime = incoming.EventTime
	}
	if merged.AvgPrice <= 0 && merged.ExecutedQty > 0 && merged.CumQuoteQty > 0 {
		merged.AvgPrice = merged.CumQuoteQty / merged.ExecutedQty
	}
	return merged
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
	h.snapshots[snapshot.ClientOrderID] = mergeOrderExecutionSnapshots(h.snapshots[snapshot.ClientOrderID], snapshot)
	h.trimSnapshotsLocked()
	waiters := append([]chan OrderExecutionSnapshot(nil), h.waiters[snapshot.ClientOrderID]...)
	resolvedSnapshot := h.snapshots[snapshot.ClientOrderID]
	h.mu.Unlock()

	for _, waiter := range waiters {
		select {
		case waiter <- resolvedSnapshot:
		default:
			select {
			case <-waiter:
			default:
			}
			select {
			case waiter <- resolvedSnapshot:
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
	h.snapshotOrder = removeSnapshotOrderEntries(h.snapshotOrder, clientOrderID)
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
	if len(h.snapshotOrder) <= orderEventSnapshotLimit {
		return
	}
	removable := len(h.snapshotOrder) - orderEventSnapshotLimit
	kept := make([]string, 0, minInt(len(h.snapshotOrder), orderEventSnapshotLimit))
	for _, clientOrderID := range h.snapshotOrder {
		if removable > 0 && h.shouldDropSnapshotLocked(clientOrderID) {
			delete(h.snapshots, clientOrderID)
			removable--
			continue
		}
		if strings.TrimSpace(clientOrderID) == "" {
			continue
		}
		kept = append(kept, clientOrderID)
	}
	h.snapshotOrder = append([]string(nil), kept...)
}

func (h *OrderEventHub) shouldDropSnapshotLocked(clientOrderID string) bool {
	if strings.TrimSpace(clientOrderID) == "" {
		return true
	}
	if _, ok := h.snapshots[clientOrderID]; !ok {
		return true
	}
	if waiters := h.waiters[clientOrderID]; len(waiters) > 0 {
		return false
	}
	return true
}

func removeSnapshotOrderEntries(items []string, target string) []string {
	target = strings.TrimSpace(target)
	if len(items) == 0 || target == "" {
		return items
	}
	filtered := items[:0]
	for _, item := range items {
		if item == target {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == len(items) {
		return items
	}
	return append([]string(nil), filtered...)
}

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
