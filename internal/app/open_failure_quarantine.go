package app

import (
	"sync"
	"time"
)

const (
	openFailureQuarantineThreshold = 3
	openFailureQuarantineDuration  = 30 * time.Minute
)

// openFailureQuarantine 跟踪连续开仓失败并在达阈值后隔离 symbol(内存,不持久化)。
type openFailureQuarantine struct {
	mu                  sync.Mutex
	consecutiveFailures map[string]int
	quarantineUntil     map[string]time.Time
	nowFn               func() time.Time
}

func newOpenFailureQuarantine(nowFn func() time.Time) *openFailureQuarantine {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &openFailureQuarantine{
		consecutiveFailures: make(map[string]int),
		quarantineUntil:     make(map[string]time.Time),
		nowFn:               nowFn,
	}
}

func (q *openFailureQuarantine) isQuarantined(symbol string) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	until, ok := q.quarantineUntil[symbol]
	return ok && q.nowFn().Before(until)
}

func (q *openFailureQuarantine) recordOpenSuccess(symbol string) {
	if q == nil || symbol == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.consecutiveFailures, symbol)
	delete(q.quarantineUntil, symbol)
}

func (q *openFailureQuarantine) recordOpenFailure(symbol string) {
	if q == nil || symbol == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.consecutiveFailures[symbol]++
	if q.consecutiveFailures[symbol] >= openFailureQuarantineThreshold {
		q.quarantineUntil[symbol] = q.nowFn().Add(openFailureQuarantineDuration)
	}
}
