package app

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/execution"
)

const executionRecoveryIncidentFile = "execution_recovery_incidents.jsonl"

type executionRecoveryIncident struct {
	RecordedAt  time.Time `json:"recorded_at"`
	Venue       string    `json:"venue"`
	Symbol      string    `json:"symbol"`
	LocalQty    float64   `json:"local_qty"`
	ExchangeQty float64   `json:"exchange_qty"`
	Reason      string    `json:"reason"`
}

func (e *OKXDirectionalEngine) setExecutionRecoveryReady(ready bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.executionRecoveryReady = ready
	e.mu.Unlock()
}

func (e *OKXDirectionalEngine) isExecutionRecoveryReady() bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.executionRecoveryReady
}

// reconcileExchangePositionsOnBoot 以交易所持仓为风险真相，对本地 slow/fast 投影做启动屏障。
// 不自动认领未知仓；任何差异都隔离 symbol，只允许后续 reduce-only/人工对账。
func (e *OKXDirectionalEngine) reconcileExchangePositionsOnBoot(ctx context.Context) error {
	if e == nil || e.v == nil {
		return fmt.Errorf("execution recovery: venue 未接线")
	}
	poss, err := e.v.FetchPositions(ctx)
	if err != nil {
		e.setExecutionRecoveryReady(false)
		return fmt.Errorf("execution recovery: FetchPositions: %w", err)
	}

	local := make(map[string]float64)
	e.mu.RLock()
	for symbol, p := range e.positions {
		if p == nil {
			continue
		}
		qty := p.Qty
		if p.Side == "short" {
			qty = -qty
		}
		local[symbol] += qty
	}
	e.mu.RUnlock()
	fastLiveMu.Lock()
	for symbol, p := range fastLivePos[e.venueName] {
		if p == nil {
			continue
		}
		qty := p.Qty
		if p.Side == "short" {
			qty = -qty
		}
		local[symbol] += qty
	}
	fastLiveMu.Unlock()

	exchangeQty := make(map[string]float64)
	for _, p := range poss {
		if math.Abs(p.PositionAmt) <= executionQtyTolerance {
			continue
		}
		exchangeQty[string(p.Symbol)] += p.PositionAmt
	}
	symbolSet := make(map[string]struct{}, len(local)+len(exchangeQty))
	for symbol := range local {
		symbolSet[symbol] = struct{}{}
	}
	for symbol := range exchangeQty {
		symbolSet[symbol] = struct{}{}
	}
	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	if e.executionClaims == nil {
		e.executionClaims = execution.NewClaimManager()
	}
	var mismatches int
	for _, symbol := range symbols {
		lq, eq := local[symbol], exchangeQty[symbol]
		if math.Abs(lq-eq) <= executionQtyTolerance {
			continue
		}
		mismatches++
		reason := execution.DiscrepancyLocalExchangeQtyMismatch
		if math.Abs(lq) <= executionQtyTolerance && math.Abs(eq) > executionQtyTolerance {
			reason = execution.DiscrepancyExchangeOnlyPosition
		}
		e.executionClaims.Quarantine(symbol, reason, e.nowFn().UTC(), "")
		_ = appendJSONLRecord(filepath.Join(e.recordDir, executionRecoveryIncidentFile), executionRecoveryIncident{
			RecordedAt: e.nowFn().UTC(), Venue: e.venueName, Symbol: symbol,
			LocalQty: lq, ExchangeQty: eq, Reason: reason,
		})
	}
	e.setExecutionRecoveryReady(mismatches == 0)
	if mismatches > 0 {
		return fmt.Errorf("execution recovery: %d 个 symbol 与交易所真仓不一致，已隔离", mismatches)
	}
	return nil
}
