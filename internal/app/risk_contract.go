package app

import "lingxi/internal/risk"

// 风险语义内核已迁移到 internal/risk（Gate 3 解耦：纯函数、可脱网单测）。
// 这里保留同名别名/转发，让既有大量引用无需改动即可继续编译。
type (
	RiskAction             = risk.RiskAction
	RiskSeverityLevel      = risk.RiskSeverityLevel
	RiskDecision           = risk.RiskDecision
	MonitorHealthDecision  = risk.MonitorHealthDecision
	MarketRiskDecision     = risk.MarketRiskDecision
	HedgeImbalanceDecision = risk.HedgeImbalanceDecision
)

const (
	RiskActionAllow            = risk.RiskActionAllow
	RiskActionObserve          = risk.RiskActionObserve
	RiskActionTighten          = risk.RiskActionTighten
	RiskActionCooldownSymbol   = risk.RiskActionCooldownSymbol
	RiskActionPauseCycle       = risk.RiskActionPauseCycle
	RiskActionTripPlatformFuse = risk.RiskActionTripPlatformFuse
	RiskActionManualTakeover   = risk.RiskActionManualTakeover
	RiskActionEscalateRecovery = risk.RiskActionEscalateRecovery
	RiskActionAbortTarget      = risk.RiskActionAbortTarget

	RiskSeverityLevelInfo     = risk.RiskSeverityLevelInfo
	RiskSeverityLevelWarning  = risk.RiskSeverityLevelWarning
	RiskSeverityLevelMajor    = risk.RiskSeverityLevelMajor
	RiskSeverityLevelCritical = risk.RiskSeverityLevelCritical
)

// EvaluateExecutionCoverageRisk / EvaluateLiquidityShareRisk 转发到 internal/risk。
var (
	EvaluateExecutionCoverageRisk = risk.EvaluateExecutionCoverageRisk
	EvaluateLiquidityShareRisk    = risk.EvaluateLiquidityShareRisk
	EvaluateHedgeImbalance        = risk.EvaluateHedgeImbalance
)
