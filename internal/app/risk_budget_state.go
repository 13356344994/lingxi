package app

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

	// ConsecutiveLossCycles 记录「按 funding 实收结算后真实净利为负」的连续周期数（当日维度）。
	// 达到 RiskConfig.MaxConsecutiveLossCycles 即触发日级熔断停机，要求人工复核系统性失效。
	ConsecutiveLossCycles int

	// RealizedPnLCumulativeUSDT / RealizedPnLPeakUSDT 维护「全部已结算周期净利累计曲线」及其历史高水位，
	// 用于账户级回撤熔断：drawdown = Peak − Cumulative。两者在启动时由已落盘结算记录回放重算复原，
	// 之后每个周期 funding 实收结算完成时增量更新，因此跨重启自洽、不会被重启清零绕过。
	RealizedPnLCumulativeUSDT float64
	RealizedPnLPeakUSDT       float64
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
