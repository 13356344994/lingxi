package app

import "lingxi/internal/core"

// effectiveRiskBudgetLimit 计算随托管持仓数缩放后的风控次数预算。
// 开关关闭时返回 base(默认行为);开启时 budget = base + k×openPositions,且永不小于 base(更严不更松)。
func effectiveRiskBudgetLimit(base int, openPositions int, cfg core.RiskConfig) int {
	if base <= 0 {
		return base
	}
	if !cfg.RiskBudgetScaleEnabled || cfg.RiskBudgetScalePerOpenPosition <= 0 {
		return base
	}
	if openPositions < 0 {
		openPositions = 0
	}
	scaled := base + cfg.RiskBudgetScalePerOpenPosition*openPositions
	if scaled < base {
		return base
	}
	return scaled
}

func (e *FundingRateSniperEngine) managedOpenPositionCount() int {
	if e == nil {
		return 0
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return len(e.activeOpenHedges)
}

func (e *FundingRateSniperEngine) effectiveMaxExecutionIncidentsLocked() int {
	if e == nil {
		return 0
	}
	return effectiveRiskBudgetLimit(e.cfg.RiskConfig.MaxExecutionIncidents, len(e.activeOpenHedges), e.cfg.RiskConfig)
}

func (e *FundingRateSniperEngine) effectiveMaxRecoveryActionsPerCycleLocked() int {
	if e == nil {
		return 0
	}
	return effectiveRiskBudgetLimit(e.cfg.RiskConfig.MaxRecoveryActionsPerCycle, len(e.activeOpenHedges), e.cfg.RiskConfig)
}

func (e *FundingRateSniperEngine) effectiveMaxConsecutivePlatformRejectsLocked() int {
	if e == nil {
		return 0
	}
	return effectiveRiskBudgetLimit(e.cfg.RiskConfig.MaxConsecutivePlatformRejects, len(e.activeOpenHedges), e.cfg.RiskConfig)
}

func (e *FundingRateSniperEngine) effectiveMaxConsecutiveRequestErrorsLocked() int {
	if e == nil {
		return 0
	}
	return effectiveRiskBudgetLimit(e.cfg.RiskConfig.MaxConsecutiveRequestErrors, len(e.activeOpenHedges), e.cfg.RiskConfig)
}

func (e *FundingRateSniperEngine) effectiveMaxExecutionIncidents() int {
	if e == nil {
		return 0
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.effectiveMaxExecutionIncidentsLocked()
}

func (e *FundingRateSniperEngine) effectiveMaxRecoveryActionsPerCycle() int {
	if e == nil {
		return 0
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.effectiveMaxRecoveryActionsPerCycleLocked()
}

func (e *FundingRateSniperEngine) effectiveMaxConsecutivePlatformRejects() int {
	if e == nil {
		return 0
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.effectiveMaxConsecutivePlatformRejectsLocked()
}

func (e *FundingRateSniperEngine) effectiveMaxConsecutiveRequestErrors() int {
	if e == nil {
		return 0
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.effectiveMaxConsecutiveRequestErrorsLocked()
}
