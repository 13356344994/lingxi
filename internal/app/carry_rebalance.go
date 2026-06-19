package app

import "time"

// carry 持有期两腿 qty 偏差再平衡:阈值与 perp-perp manageCrossVenuePosition 持有期再平衡同源(5%)。
const (
	carryHedgeRebalanceDeviationRate = 0.05
	carryHedgeRebalanceMinInterval   = 5 * time.Minute
)

// carryShouldRebalanceHedge 判定是否触发 carry 对冲再平衡:超偏差阈值且距上次再平衡已过最小间隔。
func carryShouldRebalanceHedge(dev float64, lastAt, now time.Time) bool {
	if dev <= carryHedgeRebalanceDeviationRate {
		return false
	}
	if !lastAt.IsZero() && now.Sub(lastAt) < carryHedgeRebalanceMinInterval {
		return false
	}
	return true
}

// carryPositiveRebalancePlan 正 carry(多现货+空永续)再平衡:减大腿恢复 delta 中性。
func carryPositiveRebalancePlan(spotQty, perpQty float64) (reduceSpot bool, delta float64) {
	sAbs := absQty(spotQty)
	pAbs := absQty(perpQty)
	if mathAbsDiff(sAbs, pAbs)/maxNonZero(sAbs, pAbs) <= carryHedgeRebalanceDeviationRate {
		return false, 0
	}
	if sAbs > pAbs {
		return true, sAbs - pAbs
	}
	return false, pAbs - sAbs
}

// carryNegativeRebalancePlan 负 carry(空现货+多永续)再平衡:减大腿恢复 delta 中性。
func carryNegativeRebalancePlan(spotShort, perpLong float64) (reduceSpotShort, reducePerpLong bool, delta float64) {
	sAbs := absQty(spotShort)
	pAbs := absQty(perpLong)
	if mathAbsDiff(sAbs, pAbs)/maxNonZero(sAbs, pAbs) <= carryHedgeRebalanceDeviationRate {
		return false, false, 0
	}
	if sAbs > pAbs {
		return true, false, sAbs - pAbs
	}
	return false, true, pAbs - sAbs
}

func mathAbsDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

func maxNonZero(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
