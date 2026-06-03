// Package risk holds the pure, network-free risk semantics shared by the live
// engine and the backtester: the structured action/severity vocabulary and the
// deterministic decision functions. Keeping these here lets internal/risk be
// unit-tested without touching the exchange or the god object.
package risk

import "strings"

// RiskAction 描述系统级风险裁判给出的动作建议。
type RiskAction string

const (
	RiskActionAllow            RiskAction = "allow"
	RiskActionObserve          RiskAction = "observe"
	RiskActionTighten          RiskAction = "tighten" // 收紧：如降低并发、缩小最大仓位
	RiskActionCooldownSymbol   RiskAction = "cooldown_symbol"
	RiskActionPauseCycle       RiskAction = "pause_cycle"        // 暂停：当前 cycle 结束，不再开新仓
	RiskActionTripPlatformFuse RiskAction = "trip_platform_fuse" // 熔断：系统挂起，等待人工或冷却
	RiskActionManualTakeover   RiskAction = "manual_takeover"    // 必须人工介入接管
	RiskActionEscalateRecovery RiskAction = "escalate_to_recovery"
	RiskActionAbortTarget      RiskAction = "abort_target"
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
	Action      RiskAction
	Category    string
	Severity    RiskSeverityLevel
	Reason      string
	CanContinue bool
}

// MonitorHealthDecision 描述监控阶段的健康判定结果。
type MonitorHealthDecision struct {
	State           string
	Invalidate      bool
	NextBreachCount int
}

// MarketRiskDecision 描述市场风险（深度/流动性/执行覆盖率）裁判结果。
type MarketRiskDecision struct {
	Category string
	Severity string
	Action   RiskAction
}

func (d MarketRiskDecision) Normalized() MarketRiskDecision {
	d.Category = strings.TrimSpace(d.Category)
	d.Severity = strings.TrimSpace(d.Severity)
	if d.Action == "" {
		d.Action = RiskActionObserve
	}
	return d
}

// EvaluateExecutionCoverageRisk 把成交覆盖率映射成统一的风险动作。
func EvaluateExecutionCoverageRisk(category string, coverageRatio float64) MarketRiskDecision {
	switch {
	case coverageRatio >= 0.98:
		return MarketRiskDecision{Category: category, Severity: "healthy", Action: RiskActionObserve}
	case coverageRatio >= 0.90:
		return MarketRiskDecision{Category: category, Severity: "minor", Action: RiskActionObserve}
	case coverageRatio >= 0.70:
		return MarketRiskDecision{Category: category, Severity: "major", Action: RiskActionTighten}
	default:
		return MarketRiskDecision{Category: category, Severity: "critical", Action: RiskActionPauseCycle}
	}
}

// HedgeImbalanceDecision 描述对冲两腿名义偏离的运行时裁判结果（Gate 4 关键不变量）。
type HedgeImbalanceDecision struct {
	Action        RiskAction
	Severity      RiskSeverityLevel
	Imbalance     float64 // |spotNotional - futuresNotional|
	ImbalanceRate float64 // Imbalance / max(|spotNotional|, |futuresNotional|)
	Reason        string
	Breached      bool
}

// EvaluateHedgeImbalance 是 Gate 4 的关键不变量守卫：当现货腿名义与合约腿名义的偏离
// 超过阈值时立即给出熔断动作，防止裸敞口在运行时被悄悄放大。
//
// 判定为「或」关系，任一阈值被突破即熔断：
//   - absThresholdUSDT: 绝对偏离阈值（USDT），<=0 表示不启用该维度
//   - rateThreshold:    相对偏离阈值（占较大腿的比例 0~1），<=0 表示不启用该维度
//
// 两个阈值都 <=0 时视为未配置守卫，恒为 Allow（不做运行时拦截）。
func EvaluateHedgeImbalance(spotNotional, futuresNotional, absThresholdUSDT, rateThreshold float64) HedgeImbalanceDecision {
	imbalance := spotNotional - futuresNotional
	if imbalance < 0 {
		imbalance = -imbalance
	}
	larger := spotNotional
	if larger < 0 {
		larger = -larger
	}
	absFutures := futuresNotional
	if absFutures < 0 {
		absFutures = -absFutures
	}
	if absFutures > larger {
		larger = absFutures
	}
	rate := 0.0
	if larger > 0 {
		rate = imbalance / larger
	}

	decision := HedgeImbalanceDecision{
		Action:        RiskActionAllow,
		Severity:      RiskSeverityLevelInfo,
		Imbalance:     imbalance,
		ImbalanceRate: rate,
	}

	absEnabled := absThresholdUSDT > 0
	rateEnabled := rateThreshold > 0
	if !absEnabled && !rateEnabled {
		return decision
	}

	absBreached := absEnabled && imbalance > absThresholdUSDT
	rateBreached := rateEnabled && rate > rateThreshold
	if absBreached || rateBreached {
		decision.Action = RiskActionTripPlatformFuse
		decision.Severity = RiskSeverityLevelCritical
		decision.Breached = true
		decision.Reason = "hedge_imbalance"
		return decision
	}
	return decision
}

// EvaluateLiquidityShareRisk 根据本单占用的流动性份额映射风险动作。
func EvaluateLiquidityShareRisk(share float64, limit float64, severeLimit float64) MarketRiskDecision {
	switch {
	case share <= 0 || limit <= 0:
		return MarketRiskDecision{Category: "liquidity", Severity: "healthy", Action: RiskActionObserve}
	case severeLimit > 0 && share > severeLimit:
		return MarketRiskDecision{Category: "liquidity", Severity: "critical", Action: RiskActionPauseCycle}
	case share > limit:
		return MarketRiskDecision{Category: "liquidity", Severity: "major", Action: RiskActionTighten}
	case share > limit*0.85:
		return MarketRiskDecision{Category: "liquidity", Severity: "minor", Action: RiskActionObserve}
	default:
		return MarketRiskDecision{Category: "liquidity", Severity: "healthy", Action: RiskActionObserve}
	}
}
