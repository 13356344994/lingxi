package main

import (
	"time"
)

// SystemHealthTrend 用于跟踪系统的慢性劣化趋势（如频繁报错、频繁恢复）
type SystemHealthTrend struct {
	TotalRequests      int64
	ErrorRequests      int64
	TotalRecoveries    int64
	UnknownOrders      int64
	LastDegradation    time.Time
	CurrentHealthScore float64 // 1.0 (极好) 到 0.0 (崩溃)
}

func (s *RiskSupervisor) CurrentHealthScore() float64 {
	engine := s.engine
	engine.platformRiskMu.Lock()
	defer engine.platformRiskMu.Unlock()
	return s.currentHealthScoreLocked()
}

// currentHealthScoreLocked 计算系统健康度，调用方必须已持有 platformRiskMu。
// 用于已经处于锁内的路径（如 RuntimeReporter.emitLockedWithTargets），避免非可重入互斥锁自死锁。
func (s *RiskSupervisor) currentHealthScoreLocked() float64 {
	// 目前通过实时计算代替缓存状态，后续可移入 PlatformState
	score := 1.0
	engine := s.engine

	if engine.consecutivePlatformRejects > 0 {
		score -= float64(engine.consecutivePlatformRejects) * 0.1
	}
	if engine.consecutiveRequestErrors > 0 {
		score -= float64(engine.consecutiveRequestErrors) * 0.15
	}
	if engine.recoveryActionsThisCycle > 0 {
		score -= float64(engine.recoveryActionsThisCycle) * 0.2
	}
	if engine.orderUncertaintyCount > 0 {
		score -= float64(engine.orderUncertaintyCount) * 0.3
	}
	if score < 0 {
		return 0
	}
	return score
}

// CheckHealthTrend 根据最近的错误率和恢复率，评估系统健康度并建议降级
func (s *RiskSupervisor) CheckHealthTrend() {
	score := s.CurrentHealthScore()

	// 触发趋势降级（防御档）
	if score <= 0.4 {
		// 如果还没进入降级，则切入 Defensive 档
		// 注意：这里仅作演示日志，实际接入后续阶段时会直接操作 EngineGear
		if s.engine.logger != nil {
			s.engine.logger.Printf("【趋势风控】系统健康度降至 %.2f，建议切换至 Defensive 档位以自保", score)
		}
	}
}
