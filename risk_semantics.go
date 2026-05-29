package main

type MonitorHealthDecision struct {
	State           string
	Invalidate      bool
	NextBreachCount int
}

type MarketRiskDecision struct {
	Category string
	Severity string
	Action   string
}

func EvaluateMonitorNetProfit(netProfit float64, immediateInvalidateRate float64, minNetProfitRate float64, maxConsecutiveBreach int, currentBreaches int) MonitorHealthDecision {
	if netProfit <= immediateInvalidateRate {
		return MonitorHealthDecision{
			State:           "invalidated",
			Invalidate:      true,
			NextBreachCount: currentBreaches,
		}
	}
	if netProfit <= minNetProfitRate {
		nextBreaches := currentBreaches + 1
		return MonitorHealthDecision{
			State:           "observing",
			Invalidate:      maxConsecutiveBreach > 0 && nextBreaches >= maxConsecutiveBreach,
			NextBreachCount: nextBreaches,
		}
	}
	return MonitorHealthDecision{
		State:           "healthy",
		Invalidate:      false,
		NextBreachCount: 0,
	}
}

func EvaluateExecutionCoverageRisk(category string, coverageRatio float64) MarketRiskDecision {
	switch {
	case coverageRatio >= 0.98:
		return MarketRiskDecision{Category: category, Severity: "healthy", Action: "healthy"}
	case coverageRatio >= 0.90:
		return MarketRiskDecision{Category: category, Severity: "minor", Action: "observe"}
	case coverageRatio >= 0.70:
		return MarketRiskDecision{Category: category, Severity: "major", Action: "symbol_cooldown"}
	default:
		return MarketRiskDecision{Category: category, Severity: "critical", Action: "global_pause"}
	}
}

func EvaluateLiquidityShareRisk(share float64, limit float64, severeLimit float64) MarketRiskDecision {
	switch {
	case share <= 0 || limit <= 0:
		return MarketRiskDecision{Category: "liquidity", Severity: "healthy", Action: "healthy"}
	case severeLimit > 0 && share > severeLimit:
		return MarketRiskDecision{Category: "liquidity", Severity: "critical", Action: "global_pause"}
	case share > limit:
		return MarketRiskDecision{Category: "liquidity", Severity: "major", Action: "symbol_cooldown"}
	case share > limit*0.85:
		return MarketRiskDecision{Category: "liquidity", Severity: "minor", Action: "observe"}
	default:
		return MarketRiskDecision{Category: "liquidity", Severity: "healthy", Action: "healthy"}
	}
}
