package brain

import (
	"math"

	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg6/eventaccount"
	"lingxi/internal/routerg6/sizing"
)

// SizeEventNotional applies G10 stress budget (0.5% event / 2% account / 5× leverage)
// then clips downward by min lot, capacity, correlation, and solvency — never upward.
func SizeEventNotional(
	account *eventaccount.Account,
	edgeUSDT, stressUSDT, refNotional float64,
	marginTimeSec int64,
	minNotional, lotNotional, capacityUSDT float64,
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
		MaxEventStressFraction:   g10protocol.MaxEventStressLossFraction,
		MaxAccountStressFraction: g10protocol.MaxAccountStressLossFraction,
		MaxLeverage:              float64(g10protocol.MaxAccountLeverage),
	}
	res := sizing.SizeEventNotional(in)
	if res.Skipped || res.NotionalUSDT <= 0 {
		return res
	}
	notional := res.NotionalUSDT

	if capacityUSDT > 0 && notional > capacityUSDT {
		notional = capacityUSDT
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

// MaxConcurrent returns frozen concurrent cap for one initial equity tier.
func MaxConcurrent(initialEquityUSDT float64) int {
	tier := int(initialEquityUSDT)
	if cap, ok := g10protocol.MaxConcurrentByCapital[tier]; ok {
		return cap
	}
	for _, eq := range g10protocol.MicroCapitalTiersUSDT {
		if math.Abs(initialEquityUSDT-eq) < 1e-9 {
			return g10protocol.MaxConcurrentByCapital[int(eq)]
		}
	}
	return g10protocol.MaxConcurrent30U
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
