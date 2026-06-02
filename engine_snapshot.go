package main

// EngineSnapshot 是 funding_sniper_engine.go 中状态主权的只读快照。
// 任何需要跨模块读取系统状态的地方（如 Dashboard、Reporter、风控检查器），
// 都必须通过 EngineSnapshot 获取，而不能直接读取 Engine 的内部字段。
type EngineSnapshot struct {
	RunID               string
	CurrentCycleID      string
	CurrentStage        EngineStage
	CurrentStageReason  string
	StageTransitionedAt string

	Platform EnginePlatformState
	Risk     EngineRiskBudgetState

	// 保护性统计
	ProtectedIOCAttempts         int
	ProtectedIOCCanceled         int
	ProtectedIOCExpired          int
	ProtectedIOCPartial          int
	ProtectedIOCAvgUnfilledRatio float64
	ProtectedIOCCancelRate       float64

	// 实时状态
	CurrentPositions      []string
	CurrentMonitorTargets []string
	CooledSymbols         []string

	LastOrderAction           string
	LastRecoveryAction        string
	LastManagedDrawdownAction string
	ManagedDrawdownSummary    string
	LastUncertainOrder        string
}

// GenerateSnapshot 生成当前引擎的强一致性只读快照
func (e *FundingRateSniperEngine) GenerateSnapshot() EngineSnapshot {
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.generateSnapshotLocked()
}

// generateSnapshotLocked 生成只读快照，调用方必须已持有 platformRiskMu。
// 用于已经处于锁内的路径（如 RuntimeReporter.emitLockedWithTargets），避免非可重入互斥锁自死锁。
func (e *FundingRateSniperEngine) generateSnapshotLocked() EngineSnapshot {
	protectedIOCAttempts, protectedIOCCanceled, protectedIOCExpired, protectedIOCPartial, protectedIOCAvgUnfilledRatio, protectedIOCCancelRate := e.protectedIOCStatsLocked()

	return EngineSnapshot{
		RunID:               e.runID,
		CurrentCycleID:      e.currentCycleID,
		CurrentStage:        e.currentStage,
		CurrentStageReason:  e.currentStageReason,
		StageTransitionedAt: e.currentStageTransitionedAt,

		Platform: EnginePlatformState{
			FuseTripped:             e.platformState.FuseTripped,
			FuseReason:              e.platformState.FuseReason,
			CooldownUntil:           e.platformState.CooldownUntil,
			CurrentCooldownCategory: e.platformState.CurrentCooldownCategory,
			CurrentCooldownSeverity: e.platformState.CurrentCooldownSeverity,
			LastExceptionCategory:   e.platformState.LastExceptionCategory,
			LastExceptionSeverity:   e.platformState.LastExceptionSeverity,
			LastExceptionDetail:     e.platformState.LastExceptionDetail,
		},
		Risk: EngineRiskBudgetState{
			RequestErrorsThisCycle:   e.riskBudgetState.RequestErrorsThisCycle,
			PlatformRejectsThisCycle: e.riskBudgetState.PlatformRejectsThisCycle,
			RecoveryActionsThisCycle: e.riskBudgetState.RecoveryActionsThisCycle,
			OrderUncertaintyCount:    e.riskBudgetState.OrderUncertaintyCount,
			CycleOpenNotionalUsed:    e.riskBudgetState.CycleOpenNotionalUsed,
			DailyRealizedPnL:         e.riskBudgetState.DailyRealizedPnL,
			DailyOrderCount:          e.riskBudgetState.DailyOrderCount,
			DailyOpenNotionalUsed:    e.riskBudgetState.DailyOpenNotionalUsed,
			DailyLimitTriggered:      e.riskBudgetState.DailyLimitTriggered,
			DailyLimitReason:         e.riskBudgetState.DailyLimitReason,
		},

		ProtectedIOCAttempts:         protectedIOCAttempts,
		ProtectedIOCCanceled:         protectedIOCCanceled,
		ProtectedIOCExpired:          protectedIOCExpired,
		ProtectedIOCPartial:          protectedIOCPartial,
		ProtectedIOCAvgUnfilledRatio: protectedIOCAvgUnfilledRatio,
		ProtectedIOCCancelRate:       protectedIOCCancelRate,

		CurrentPositions:      append([]string(nil), e.currentPositions...),
		CurrentMonitorTargets: append([]string(nil), e.currentMonitorTargets...),
		CooledSymbols:         e.listActiveSymbolCooldownsLocked(),

		LastOrderAction:           e.lastOrderAction,
		LastRecoveryAction:        e.lastRecoveryAction,
		LastManagedDrawdownAction: e.lastManagedDrawdownAction,
		ManagedDrawdownSummary:    e.managedDrawdownSummary,
		LastUncertainOrder:        e.lastUncertainOrder,
	}
}
