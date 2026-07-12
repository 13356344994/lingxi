package sizing

import (
	"math"

	g6protocol "lingxi/internal/routerg6/protocol"
)

const (
	SkipNoPositiveEdge   = "no_positive_edge"
	SkipInvalidStress    = "invalid_stress"
	SkipAccountStressCap = "account_stress_cap"
	SkipLeverageCap      = "leverage_cap"
	SkipBelowMinNotional = "below_min_notional"
	SkipBelowLotFloor    = "below_lot_floor"
)

// RankingScore returns conservative_edge / stress_loss / margin_time for queue ordering.
func RankingScore(edgeUSDT, stressUSDT float64, marginTimeSec int64) float64 {
	if edgeUSDT <= 0 || stressUSDT <= 0 || marginTimeSec <= 0 {
		return 0
	}
	return edgeUSDT / stressUSDT / float64(marginTimeSec)
}

// SizeEventNotional computes event notional from stress budget and clips by account caps.
// Min-notional and lot floors apply downward only; events below floor are skipped.
func SizeEventNotional(in Input) Result {
	res := Result{
		RankingScore: RankingScore(in.ConservativeEdgeUSDT, in.StressLossUSDT, in.MarginTimeSec),
	}
	if in.ConservativeEdgeUSDT <= 0 {
		res.Skipped = true
		res.SkipReason = SkipNoPositiveEdge
		return res
	}
	if in.StressLossUSDT <= 0 || in.EquityUSDT <= 0 {
		res.Skipped = true
		res.SkipReason = SkipInvalidStress
		return res
	}

	eventFrac := in.MaxEventStressFraction
	if eventFrac <= 0 {
		eventFrac = g6protocol.MaxEventStressLossFraction
	}
	accountFrac := in.MaxAccountStressFraction
	if accountFrac <= 0 {
		accountFrac = g6protocol.MaxAccountStressLossFraction
	}
	maxLev := in.MaxLeverage
	if maxLev <= 0 {
		maxLev = float64(g6protocol.MaxAccountLeverage)
	}

	eventBudget := in.EquityUSDT * eventFrac
	accountBudget := in.EquityUSDT*accountFrac - in.UsedAccountStressUSDT
	if accountBudget <= 1e-12 {
		res.Skipped = true
		res.SkipReason = SkipAccountStressCap
		return res
	}
	stressBudget := math.Min(eventBudget, accountBudget)
	res.StressBudgetUSDT = stressBudget

	ref := in.ReferenceNotionalUSDT
	if ref <= 0 {
		ref = 1
	}
	// Scale reference stress loss to the event stress budget.
	notional := stressBudget * ref / in.StressLossUSDT

	levHeadroom := in.EquityUSDT*maxLev - in.UsedNotionalUSDT
	if levHeadroom <= 1e-12 {
		res.Skipped = true
		res.SkipReason = SkipLeverageCap
		return res
	}
	if notional > levHeadroom {
		notional = levHeadroom
	}

	notional = ApplyLotFloorDown(notional, in.LotNotionalUSDT, in.MinNotionalUSDT)
	if notional <= 0 {
		res.Skipped = true
		if in.MinNotionalUSDT > 0 {
			res.SkipReason = SkipBelowMinNotional
		} else {
			res.SkipReason = SkipBelowLotFloor
		}
		return res
	}

	res.NotionalUSDT = notional
	return res
}

// ApplyLotFloorDown rounds notional down to lot steps and rejects below min notional.
// Never rounds up to satisfy exchange minimums.
func ApplyLotFloorDown(notional, lotNotional, minNotional float64) float64 {
	if notional <= 0 {
		return 0
	}
	if lotNotional > 0 {
		steps := math.Floor(notional / lotNotional)
		if steps <= 0 {
			return 0
		}
		notional = steps * lotNotional
	}
	if minNotional > 0 && notional+1e-12 < minNotional {
		return 0
	}
	return notional
}

// MaxConcurrent returns frozen concurrent event cap for one initial equity tier.
func MaxConcurrent(initialEquityUSDT float64) int {
	tier := int(initialEquityUSDT)
	if cap, ok := g6protocol.MaxConcurrentByCapital[tier]; ok {
		return cap
	}
	for _, eq := range g6protocol.CapitalTiersUSDT {
		if math.Abs(initialEquityUSDT-eq) < 1e-9 {
			return g6protocol.MaxConcurrentByCapital[int(eq)]
		}
	}
	return g6protocol.MaxConcurrent30U
}
