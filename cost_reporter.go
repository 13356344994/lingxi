package main

import (
	"log"
	"sync"
	"time"
)

// CostReporter 用于集中记录执行成本和利润归因
type CostReporter struct {
	logger *log.Logger
	store  *ReportStore
	
	mu sync.Mutex
	executionCosts []ExecutionCostRecord
}

func NewCostReporter(logger *log.Logger, store *ReportStore) *CostReporter {
	return &CostReporter{
		logger: logger,
		store:  store,
	}
}

func (r *CostReporter) RecordExecutionCost(cost ExecutionCostRecord) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executionCosts = append(r.executionCosts, cost)
	
	if r.logger != nil {
		r.logger.Printf("[执行成本] %s | action=%s qty=%.4f slippage=%.4f USDT latency=%s", 
			cost.Symbol, cost.Action, cost.ExecutedQuantity, cost.SlippageUSDT, cost.Latency.Round(time.Millisecond))
	}
}

func (r *CostReporter) FlushExecutionCosts() []ExecutionCostRecord {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	costs := append([]ExecutionCostRecord(nil), r.executionCosts...)
	r.executionCosts = nil
	return costs
}
