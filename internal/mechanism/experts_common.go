package mechanism

import (
	"math"

	"lingxi/internal/money"
)

// mechanismCapacity 估计机制可承接的名义上限：取「仍未释放的被迫名义」与盘口一档名义的较小者。
// 被迫流是收益来源上限，盘口深度是即时可执行上限；两者取小即保守容量。
func mechanismCapacity(card MechanismEventCard) money.Micros {
	remaining := card.ForcedFlow.RemainingNotional
	depth := card.Executability.TopDepthNotional
	if depth > 0 && depth < remaining {
		return depth
	}
	return remaining
}

// executable 报告事件卡在可执行性维度是否至少能以某种方式成交，并给出不可执行时的理由。
func executable(card MechanismEventCard) (ok bool, reason string) {
	ex := card.Executability
	if !ex.TakerFeasible && !ex.MakerFeasible {
		return false, RejectNotExecutable
	}
	if ex.LotNotional <= 0 {
		return false, RejectNotExecutable
	}
	capacity := mechanismCapacity(card)
	minimum := ex.LotNotional
	if ex.MinNotional > minimum {
		minimum = ex.MinNotional
	}
	if capacity < minimum {
		return false, RejectNotExecutable
	}
	return true, ""
}

// baseObservationConfidence 把观测本身的信心夹到 [0,1]，作为天道信心的一个上界。
func baseObservationConfidence(card MechanismEventCard) float64 {
	c := card.Quality.ObservationConfidence
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// finiteSignal 读取机制信号并拒绝 NaN / Inf，避免未知观测穿透比较运算后被误判为有效。
func finiteSignal(card MechanismEventCard, name string) (float64, bool) {
	v, ok := card.Signal(name)
	return v, ok && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// validRatio 报告比例是否是有限的 [0,1] 值。
func validRatio(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

// clampUnit 把值夹到 [0,1]；非有限值保守归零。
func clampUnit(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
