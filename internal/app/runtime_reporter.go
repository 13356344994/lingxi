package app

import (
	"log"
	"strings"
	"sync/atomic"
	"time"
)

type RuntimeReporter struct {
	engine *FundingRateSniperEngine
}

func newRuntimeReporter(engine *FundingRateSniperEngine) *RuntimeReporter {
	if engine == nil {
		return nil
	}
	return &RuntimeReporter{engine: engine}
}

func (r *RuntimeReporter) EmitWithReason(stage EngineStage, currentTargets int, reason string) {
	if r == nil || r.engine == nil {
		return
	}
	r.engine.platformRiskMu.Lock()
	defer r.engine.platformRiskMu.Unlock()
	r.emitLockedWithTargets(stage, currentTargets, reason)
}

func (r *RuntimeReporter) EmitLocked(stage EngineStage) {
	r.emitLockedWithTargets(stage, -1, "")
}

// Emit 是 EmitLocked 的「自加锁」版本:供**未持有** platformRiskMu 的调用方使用(典型场景:生命周期迁移
// applyLifecycleProtocolResult 已 defer 释放锁后,需要再发一次状态快照)。EmitLocked 系列会读取 activeOpenHedges、
// platformState、cooldown/exposure 等受 platformRiskMu 保护的字段(含 map 迭代),不持锁直接调用会与后台监控
// (drawdown/hold/margin/结算)的并发写形成数据竞争,严重时触发「concurrent map iteration」运行时致命崩溃。
func (r *RuntimeReporter) Emit(stage EngineStage) {
	if r == nil || r.engine == nil {
		return
	}
	r.engine.platformRiskMu.Lock()
	defer r.engine.platformRiskMu.Unlock()
	r.emitLockedWithTargets(stage, -1, "")
}

func (r *RuntimeReporter) EmitLockedWithReason(stage EngineStage, reason string) {
	r.emitLockedWithTargets(stage, -1, reason)
}

func (r *RuntimeReporter) emitLockedWithTargets(stage EngineStage, currentTargets int, reason string) {
	if r == nil || r.engine == nil || r.engine.liveStatusHook == nil {
		return
	}
	engine := r.engine
	stage = r.transitionStageLocked(stage, reason)
	healthStatus, healthSummary, canContinue := engine.healthStatusLocked()

	// 注入 Health Trend Score 与 Engine Gear
	healthScore := 1.0
	currentGear := string(GearObserve)
	if engine.riskSupervisor != nil {
		healthScore = engine.riskSupervisor.currentHealthScoreLocked()
		currentGear = string(engine.riskSupervisor.currentGearLocked())
	}

	// ---------------------------------------------------------
	// 状态主权解耦（阶段五）：直接使用只读快照替代零散状态组装
	// ---------------------------------------------------------
	snapshotView := engine.generateSnapshotLocked()

	remainingNotionalBudget := -1.0
	if engine.cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0 {
		remainingNotionalBudget = maxFloat(engine.cfg.RiskConfig.MaxCycleOpenNotionalUSDT-snapshotView.Risk.CycleOpenNotionalUsed, 0)
	}

	snapshot := LiveRuntimeSnapshot{
		RunID:                        snapshotView.RunID,
		CycleID:                      snapshotView.CurrentCycleID,
		Stage:                        snapshotView.CurrentStage,
		StageReason:                  snapshotView.CurrentStageReason,
		StageTransitionedAt:          snapshotView.StageTransitionedAt,
		CurrentTargets:               currentTargets,
		CurrentPositions:             snapshotView.CurrentPositions,
		CurrentMonitorTargets:        snapshotView.CurrentMonitorTargets,
		PlatformRejectCount:          snapshotView.Risk.PlatformRejectsThisCycle,
		PlatformRequestErrors:        snapshotView.Risk.RequestErrorsThisCycle,
		RecoveryActionCount:          snapshotView.Risk.RecoveryActionsThisCycle,
		OrderUncertaintyCount:        snapshotView.Risk.OrderUncertaintyCount,
		FuseTripped:                  snapshotView.Platform.FuseTripped,
		FuseReason:                   snapshotView.Platform.FuseReason,
		LastOrderAction:              snapshotView.LastOrderAction,
		LastRecoveryAction:           snapshotView.LastRecoveryAction,
		LastManagedDrawdownAction:    snapshotView.LastManagedDrawdownAction,
		ManagedDrawdownSummary:       snapshotView.ManagedDrawdownSummary,
		HoldMonitorSummary:           snapshotView.HoldMonitorSummary,
		LastUncertainOrder:           snapshotView.LastUncertainOrder,
		RemainingIncidentBudget:      remainingRiskBudget(engine.cfg.RiskConfig.MaxExecutionIncidents, int(atomic.LoadInt32(&engine.executionIncidents))),
		RemainingRecoveryBudget:      remainingRiskBudget(engine.cfg.RiskConfig.MaxRecoveryActionsPerCycle, snapshotView.Risk.RecoveryActionsThisCycle),
		RemainingUncertaintyBudget:   remainingRiskBudget(engine.cfg.RiskConfig.MaxOrderUncertaintyPerCycle, snapshotView.Risk.OrderUncertaintyCount),
		RemainingRejectBudget:        remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutivePlatformRejects, snapshotView.Risk.PlatformRejectsThisCycle),
		RemainingErrorBudget:         remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutiveRequestErrors, snapshotView.Risk.RequestErrorsThisCycle),
		ProtectedIOCAttempts:         snapshotView.ProtectedIOCAttempts,
		ProtectedIOCCanceled:         snapshotView.ProtectedIOCCanceled,
		ProtectedIOCExpired:          snapshotView.ProtectedIOCExpired,
		ProtectedIOCPartial:          snapshotView.ProtectedIOCPartial,
		ProtectedIOCAvgUnfilledRatio: snapshotView.ProtectedIOCAvgUnfilledRatio,
		ProtectedIOCCancelRate:       snapshotView.ProtectedIOCCancelRate,
		HealthStatus:                 healthStatus,
		HealthSummary:                healthSummary,
		HealthScore:                  healthScore,
		EngineGear:                   currentGear,
		CanContinue:                  canContinue,
		ConsecutiveRejects:           engine.consecutivePlatformRejects,
		ConsecutiveErrors:            engine.consecutiveRequestErrors,
		CycleOpenNotionalUSDT:        snapshotView.Risk.CycleOpenNotionalUsed,
		RemainingNotionalBudget:      remainingNotionalBudget,
		LastExceptionCategory:        snapshotView.Platform.LastExceptionCategory,
		LastExceptionSeverity:        snapshotView.Platform.LastExceptionSeverity,
		LastExceptionDetail:          snapshotView.Platform.LastExceptionDetail,
		CurrentCooldownCategory:      snapshotView.Platform.CurrentCooldownCategory,
		CurrentCooldownSeverity:      snapshotView.Platform.CurrentCooldownSeverity,
		CooledSymbols:                snapshotView.CooledSymbols,
		EmergencyStopTriggered:       engine.emergencyStopTriggered,
		EmergencyStopReason:          engine.emergencyStopReason,
		DailyRiskLimitTriggered:      snapshotView.Risk.DailyLimitTriggered,
		DailyRiskLimitReason:         snapshotView.Risk.DailyLimitReason,
		DailyRealizedPnL:             snapshotView.Risk.DailyRealizedPnL,
		DailyOrderCount:              snapshotView.Risk.DailyOrderCount,
		DailyOpenNotionalUSDT:        snapshotView.Risk.DailyOpenNotionalUsed,
		RemainingDailyLossBudget:     engine.remainingDailyLossBudgetLocked(),
		RemainingDailyOrderBudget:    engine.remainingDailyOrderBudgetLocked(),
		RemainingDailyNotionalBudget: engine.remainingDailyNotionalBudgetLocked(),
	}
	if !engine.lastOrderAt.IsZero() {
		snapshot.LastOrderAt = engine.lastOrderAt.Format(time.RFC3339)
	}
	if !engine.lastRecoveryAt.IsZero() {
		snapshot.LastRecoveryAt = engine.lastRecoveryAt.Format(time.RFC3339)
	}
	if !engine.lastManagedDrawdownAt.IsZero() {
		snapshot.LastManagedDrawdownAt = engine.lastManagedDrawdownAt.Format(time.RFC3339)
	}
	if !engine.platformState.CooldownUntil.IsZero() {
		snapshot.CooldownUntil = engine.platformState.CooldownUntil.UTC().Format(time.RFC3339)
	}
	engine.liveStatusHook(snapshot)
}

func (r *RuntimeReporter) transitionStageLocked(next EngineStage, reason string) EngineStage {
	if r == nil || r.engine == nil {
		return NormalizeEngineStage(next)
	}
	engine := r.engine
	if engine.logger == nil {
		engine.logger = log.Default()
	}
	next = NormalizeEngineStage(next)
	if next == "" {
		return NormalizeEngineStage(engine.currentStage)
	}
	current := NormalizeEngineStage(engine.currentStage)
	reason = strings.TrimSpace(reason)
	if current == "" {
		engine.currentStage = next
		engine.currentStageReason = reason
		engine.currentStageTransitionedAt = time.Now().UTC().Format(time.RFC3339)
		return next
	}
	if current == next {
		if reason != "" {
			engine.currentStageReason = reason
		}
		if strings.TrimSpace(engine.currentStageTransitionedAt) == "" {
			engine.currentStageTransitionedAt = time.Now().UTC().Format(time.RFC3339)
		}
		return current
	}
	transition, err := NewEngineStageTransition(current, next, reason)
	if err != nil {
		engine.logger.Printf("忽略非法状态迁移: %v", err)
		return current
	}
	engine.currentStage = transition.To
	engine.currentStageReason = transition.Reason
	engine.currentStageTransitionedAt = transition.Transitioned
	engine.logger.Printf("状态迁移: %s -> %s | reason=%s", current, transition.To, nonEmpty(transition.Reason, "-"))
	return transition.To
}
