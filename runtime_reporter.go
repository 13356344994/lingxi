package main

import (
	"fmt"
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

func (r *RuntimeReporter) Emit(stage EngineStage, currentTargets int) {
	if r == nil || r.engine == nil {
		return
	}
	r.engine.platformRiskMu.Lock()
	defer r.engine.platformRiskMu.Unlock()
	r.emitLockedWithTargets(stage, currentTargets, "")
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
	activeCooldowns := engine.listActiveSymbolCooldownsLocked()
	remainingNotionalBudget := -1.0
	if engine.cfg.RiskConfig.MaxCycleOpenNotionalUSDT > 0 {
		remainingNotionalBudget = maxFloat(engine.cfg.RiskConfig.MaxCycleOpenNotionalUSDT-engine.cycleOpenNotionalUsed, 0)
	}
	snapshot := LiveRuntimeSnapshot{
		RunID:                        engine.runID,
		CycleID:                      engine.currentCycleID,
		Stage:                        stage,
		StageReason:                  engine.currentStageReason,
		StageTransitionedAt:          engine.currentStageTransitionedAt,
		CurrentTargets:               currentTargets,
		CurrentPositions:             append([]string(nil), engine.currentPositions...),
		CurrentMonitorTargets:        append([]string(nil), engine.currentMonitorTargets...),
		PlatformRejectCount:          engine.platformRejectsThisCycle,
		PlatformRequestErrors:        engine.requestErrorsThisCycle,
		RecoveryActionCount:          engine.recoveryActionsThisCycle,
		OrderUncertaintyCount:        engine.orderUncertaintyCount,
		FuseTripped:                  engine.platformFuseTripped,
		FuseReason:                   engine.platformFuseReason,
		LastOrderAction:              engine.lastOrderAction,
		LastRecoveryAction:           engine.lastRecoveryAction,
		LastUncertainOrder:           engine.lastUncertainOrder,
		RemainingIncidentBudget:      remainingRiskBudget(engine.cfg.RiskConfig.MaxExecutionIncidents, int(atomic.LoadInt32(&engine.executionIncidents))),
		RemainingRecoveryBudget:      remainingRiskBudget(engine.cfg.RiskConfig.MaxRecoveryActionsPerCycle, engine.recoveryActionsThisCycle),
		RemainingUncertaintyBudget:   remainingRiskBudget(engine.cfg.RiskConfig.MaxOrderUncertaintyPerCycle, engine.orderUncertaintyCount),
		RemainingRejectBudget:        remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutivePlatformRejects, engine.platformRejectsThisCycle),
		RemainingErrorBudget:         remainingRiskBudget(engine.cfg.RiskConfig.MaxConsecutiveRequestErrors, engine.requestErrorsThisCycle),
		HealthStatus:                 healthStatus,
		HealthSummary:                healthSummary,
		CanContinue:                  canContinue,
		ConsecutiveRejects:           engine.consecutivePlatformRejects,
		ConsecutiveErrors:            engine.consecutiveRequestErrors,
		CycleOpenNotionalUSDT:        engine.cycleOpenNotionalUsed,
		RemainingNotionalBudget:      remainingNotionalBudget,
		LastExceptionCategory:        engine.lastExceptionCategory,
		LastExceptionSeverity:        engine.lastExceptionSeverity,
		LastExceptionDetail:          engine.lastExceptionDetail,
		CurrentCooldownCategory:      engine.currentCooldownCategory,
		CurrentCooldownSeverity:      engine.currentCooldownSeverity,
		CooledSymbols:                activeCooldowns,
		EmergencyStopTriggered:       engine.emergencyStopTriggered,
		EmergencyStopReason:          engine.emergencyStopReason,
		DailyRiskLimitTriggered:      engine.dailyLimitTriggered,
		DailyRiskLimitReason:         engine.dailyLimitReason,
		DailyRealizedPnL:             engine.dailyRealizedPnL,
		DailyOrderCount:              engine.dailyOrderCount,
		DailyOpenNotionalUSDT:        engine.dailyOpenNotionalUsed,
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
	if !engine.platformCooldownUntil.IsZero() {
		snapshot.CooldownUntil = engine.platformCooldownUntil.UTC().Format(time.RFC3339)
	}
	engine.liveStatusHook(snapshot)
}

func (r *RuntimeReporter) TraceIDs() (string, string) {
	if r == nil || r.engine == nil {
		return "", ""
	}
	r.engine.platformRiskMu.Lock()
	defer r.engine.platformRiskMu.Unlock()
	return strings.TrimSpace(r.engine.runID), strings.TrimSpace(r.engine.currentCycleID)
}

func (r *RuntimeReporter) RecordMetadata() TraceRecordMetadata {
	if r == nil || r.engine == nil {
		return TraceRecordMetadata{}
	}
	return buildTraceRecordMetadata(r.engine.cfg)
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

func (r *RuntimeReporter) CurrentStage() EngineStage {
	if r == nil || r.engine == nil {
		return ""
	}
	return NormalizeEngineStage(r.engine.currentStage)
}

func (r *RuntimeReporter) StageContext() (string, string) {
	if r == nil || r.engine == nil {
		return "", ""
	}
	return r.engine.currentStageReason, r.engine.currentStageTransitionedAt
}

func (r *RuntimeReporter) String() string {
	if r == nil || r.engine == nil {
		return "runtime-reporter<nil>"
	}
	stage, transitionedAt := r.StageContext()
	return fmt.Sprintf("runtime-reporter<stage=%s transitioned_at=%s>", r.CurrentStage(), transitionedAt+stage)
}
