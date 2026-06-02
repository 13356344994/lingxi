package main

import (
	"fmt"
)

// EngineRiskBudgetState 保存日内与周期级别的预算使用情况
type EngineRiskBudgetState struct {
	RequestErrorsThisCycle   int
	PlatformRejectsThisCycle int
	RecoveryActionsThisCycle int
	OrderUncertaintyCount    int
	CycleOpenNotionalUsed    float64

	DailyRealizedPnL      float64
	DailyOrderCount       int
	DailyOpenNotionalUsed float64
	DailyLimitTriggered   bool
	DailyLimitReason      string
}

func (s *EngineRiskBudgetState) AddRequestError() {
	s.RequestErrorsThisCycle++
}

func (s *EngineRiskBudgetState) AddPlatformReject() {
	s.PlatformRejectsThisCycle++
}

func (s *EngineRiskBudgetState) AddRecoveryAction() {
	s.RecoveryActionsThisCycle++
}

func (s *EngineRiskBudgetState) AddOrderUncertainty() {
	s.OrderUncertaintyCount++
}

func (s *EngineRiskBudgetState) AddCycleNotional(amount float64) {
	s.CycleOpenNotionalUsed += amount
}

func (s *EngineRiskBudgetState) AddDailyPnL(pnl float64) {
	s.DailyRealizedPnL += pnl
}

func (s *EngineRiskBudgetState) AddDailyOrder() {
	s.DailyOrderCount++
}

func (s *EngineRiskBudgetState) AddDailyNotional(amount float64) {
	s.DailyOpenNotionalUsed += amount
}

func (s *EngineRiskBudgetState) TripDailyLimit(reason string) {
	s.DailyLimitTriggered = true
	s.DailyLimitReason = fmt.Sprintf("日内风控触发: %s", reason)
}

func (s *EngineRiskBudgetState) ResetCycle() {
	s.RequestErrorsThisCycle = 0
	s.PlatformRejectsThisCycle = 0
	s.RecoveryActionsThisCycle = 0
	s.OrderUncertaintyCount = 0
	s.CycleOpenNotionalUsed = 0
}
