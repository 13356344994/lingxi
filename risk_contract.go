package main

// RiskAction 描述系统级风险裁判给出的动作建议。
type RiskAction string

const (
	RiskActionAllow             RiskAction = "allow"
	RiskActionObserve           RiskAction = "observe"
	RiskActionTighten           RiskAction = "tighten"         // 收紧：如降低并发、缩小最大仓位
	RiskActionCooldownSymbol    RiskAction = "cooldown_symbol"
	RiskActionPauseCycle        RiskAction = "pause_cycle"     // 暂停：当前 cycle 结束，不再开新仓
	RiskActionTripPlatformFuse  RiskAction = "trip_platform_fuse" // 熔断：系统挂起，等待人工或冷却
	RiskActionManualTakeover    RiskAction = "manual_takeover" // 必须人工介入接管
	RiskActionEscalateRecovery  RiskAction = "escalate_to_recovery"
	RiskActionAbortTarget       RiskAction = "abort_target"
)

// RiskSeverityLevel 用于统一高层风险语义，和现有 risk_event 中的字符串值兼容。
type RiskSeverityLevel string

const (
	RiskSeverityLevelInfo     RiskSeverityLevel = "info"
	RiskSeverityLevelWarning  RiskSeverityLevel = "warning"
	RiskSeverityLevelMajor    RiskSeverityLevel = "major"
	RiskSeverityLevelCritical RiskSeverityLevel = "critical"
)

// RiskDecision 是策略/协议/监督器之间共享的结构化风险决策。
type RiskDecision struct {
	Action   RiskAction
	Category string
	Severity RiskSeverityLevel
	Reason   string
	CanContinue bool
}
