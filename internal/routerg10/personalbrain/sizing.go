package personalbrain

import (
	"math"

	g10constitution "lingxi/internal/routerg10/constitution"
	"lingxi/internal/routerg6/eventaccount"
	"lingxi/internal/routerg6/sizing"
)

// SizeEventNotional applies 40%/70% terminal risk ceilings (not targets) and clips
// downward by min lot, capacity, correlation, and solvency — never upward.
func SizeEventNotional(
	account *eventaccount.Account,
	edgeUSDT, stressUSDT, refNotional float64,
	marginTimeSec int64,
	minNotional, lotNotional, capacityUSDT, maxSizeUSDT float64,
	correlatedOpen int,
) sizing.Result {
	in := sizing.Input{
		EquityUSDT:               account.Core.EquityUSDT,
		ConservativeEdgeUSDT:     edgeUSDT,
		StressLossUSDT:           stressUSDT,
		ReferenceNotionalUSDT:    refNotional,
		MarginTimeSec:            marginTimeSec,
		MinNotionalUSDT:          minNotional,
		LotNotionalUSDT:          lotNotional,
		UsedAccountStressUSDT:    account.UsedAccountStress(),
		UsedNotionalUSDT:         account.UsedNotional(),
		MaxEventStressFraction:   MaxEventTerminalLossFraction,
		MaxAccountStressFraction: MaxAccountTerminalRiskFraction,
		MaxLeverage:              5,
	}
	res := sizing.SizeEventNotional(in)
	if res.Skipped || res.NotionalUSDT <= 0 {
		return res
	}
	notional := res.NotionalUSDT

	if capacityUSDT > 0 && notional > capacityUSDT {
		notional = capacityUSDT
	}
	if maxSizeUSDT > 0 && notional > maxSizeUSDT {
		notional = maxSizeUSDT
	}
	if correlatedOpen > 0 {
		corrScale := 1.0 / float64(1+correlatedOpen)
		notional = notional * corrScale
	}
	if account.Core.SolvencyBreach || account.Core.LeverageBreach {
		notional = 0
	}

	notional = sizing.ApplyLotFloorDown(notional, lotNotional, minNotional)
	if notional <= 0 {
		res.Skipped = true
		if res.SkipReason == "" {
			res.SkipReason = sizing.SkipBelowMinNotional
		}
		res.NotionalUSDT = 0
		return res
	}
	res.NotionalUSDT = notional
	return res
}

// TerminalRiskUsage computes ceiling occupancy for trace binding.
func ComputeTerminalRiskUsage(account *eventaccount.Account, eventStressUSDT float64) TerminalRiskUsage {
	eq := account.Core.EquityUSDT
	if eq <= 0 {
		eq = 1
	}
	eventBudget := eq * MaxEventTerminalLossFraction
	accountBudget := eq * MaxAccountTerminalRiskFraction
	used := account.UsedAccountStress()
	eventPct := 0.0
	if eventBudget > 0 {
		eventPct = eventStressUSDT / eventBudget * 100
	}
	accountPct := 0.0
	if accountBudget > 0 {
		accountPct = used / accountBudget * 100
	}
	return TerminalRiskUsage{
		EventTerminalBudgetUSDT:   eventBudget,
		AccountTerminalBudgetUSDT: accountBudget,
		EventOccupancyPct:         eventPct,
		AccountOccupancyPct:       accountPct,
		CeilingIsNotTarget:        g10constitution.RiskCeilingIsNotSizingTarget,
	}
}

// TerminalRiskCeilingBlocksOpen hard-blocks new opens when 40%/70% terminal ceilings would be touched.
func TerminalRiskCeilingBlocksOpen(account *eventaccount.Account, eventStressUSDT float64) bool {
	usage := ComputeTerminalRiskUsage(account, eventStressUSDT)
	if eventStressUSDT >= usage.EventTerminalBudgetUSDT {
		return true
	}
	if account.UsedAccountStress()+eventStressUSDT >= usage.AccountTerminalBudgetUSDT {
		return true
	}
	return false
}

// MaxConcurrent returns frozen concurrent cap for one initial equity tier.
func MaxConcurrent(initialEquityUSDT float64) int {
	tier := int(initialEquityUSDT)
	if cap, ok := g10constitution.MaxConcurrentByCapital[tier]; ok {
		return cap
	}
	for _, eq := range g10constitution.CapitalTiersUSDT {
		if math.Abs(initialEquityUSDT-eq) < 1e-9 {
			return g10constitution.MaxConcurrentByCapital[int(eq)]
		}
	}
	return g10constitution.MaxConcurrent30U
}

// CountCorrelatedOpen counts open slots sharing event family with candidate.
func CountCorrelatedOpen(account *eventaccount.Account, family string) int {
	n := 0
	for _, slot := range account.OpenSlots() {
		if slot.Candidate.EventFamily == family {
			n++
		}
	}
	return n
}
