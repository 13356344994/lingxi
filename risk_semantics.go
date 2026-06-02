package main

import "strings"

type MonitorHealthDecision struct {
	State           string
	Invalidate      bool
	NextBreachCount int
}

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
