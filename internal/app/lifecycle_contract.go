package app

import "time"

// LifecycleStage 描述交易周期在编排层上的生命周期阶段。
type LifecycleStage string

const (
	LifecycleStageReady           LifecycleStage = "ready"
	LifecycleStageScanning        LifecycleStage = "scanning"
	LifecycleStageMonitoring      LifecycleStage = "monitoring"
	LifecycleStagePreOpenCheck    LifecycleStage = "pre_open_check"
	LifecycleStageOpening         LifecycleStage = "opening"
	LifecycleStageValidatingOpen  LifecycleStage = "validating_open"
	LifecycleStageHolding         LifecycleStage = "holding"
	LifecycleStageCloseMonitoring LifecycleStage = "close_monitoring"
	LifecycleStageClosing         LifecycleStage = "closing"
	LifecycleStageSettling        LifecycleStage = "settling"
	LifecycleStageRecovering      LifecycleStage = "recovering"
	LifecycleStageCoolingDown     LifecycleStage = "cooling_down"
	LifecycleStagePaused          LifecycleStage = "paused"
	LifecycleStageFused           LifecycleStage = "fused"
	LifecycleStageCompleted       LifecycleStage = "completed"
)

// LifecycleTransition 表示一次结构化的状态迁移。
type LifecycleTransition struct {
	From         LifecycleStage
	To           LifecycleStage
	Reason       string
	Transitioned time.Time
}

// LifecycleSuggestion 用于让协议层向编排层建议下一阶段。
type LifecycleSuggestion struct {
	NextStage LifecycleStage
	Reason    string
}

// LifecycleResult 用于统一表达一次生命周期动作的最终结果。
type LifecycleResult struct {
	CurrentStage LifecycleStage
	Suggestion   LifecycleSuggestion
}
