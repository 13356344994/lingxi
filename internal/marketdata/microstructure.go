// Package marketdata 承载行情微结构的「纯计算内核」：盘口深度的成交均价/可见名义估算、
// 套利方向的入场价差、价格不利漂移判定，以及基于事件时间戳的陈旧度（staleness）计算。
// 这些函数只依赖基础类型，不触碰网络/缓存/引擎状态，因此可被实盘风控、回测与监控复用并独立
// 做单元测试，是 Gate 3「真正包边界抽离」在行情域的落点。
package marketdata

import (
	"math"
	"time"
)

// DepthLevel 表示一档已完成数值解析的盘口（价格 + 数量）。作为行情域的规范类型，
// 上层（app）以类型别名复用它，避免在热路径上做切片转换。
type DepthLevel struct {
	Price    float64
	Quantity float64
}

// ComputeAverageFillPrice 按盘口档位自上而下吃单，估算成交 requiredQty 所需的成交均价：
// 返回（成交均价, 最优价, 已成交量, 是否完全成交）。任一前置条件不满足时 ok=false。
func ComputeAverageFillPrice(levels []DepthLevel, requiredQty float64) (avgPrice float64, bestPrice float64, filledQty float64, complete bool) {
	if requiredQty <= 0 || len(levels) == 0 {
		return 0, 0, 0, false
	}

	bestPrice = levels[0].Price
	remaining := requiredQty
	filled := 0.0
	totalCost := 0.0

	for _, level := range levels {
		if level.Price <= 0 || level.Quantity <= 0 {
			continue
		}
		takeQty := math.Min(remaining, level.Quantity)
		totalCost += takeQty * level.Price
		filled += takeQty
		remaining -= takeQty
		if remaining <= 1e-12 {
			break
		}
	}

	if filled <= 0 {
		return 0, bestPrice, 0, false
	}
	return totalCost / filled, bestPrice, filled, remaining <= 1e-12
}

// EstimateVisibleDepthNotional 估算可见盘口名义价值（Σ 价格×数量）。
func EstimateVisibleDepthNotional(levels []DepthLevel) float64 {
	notional := 0.0
	for _, level := range levels {
		if level.Price <= 0 || level.Quantity <= 0 {
			continue
		}
		notional += level.Price * level.Quantity
	}
	return notional
}

// MinPositiveFloat 返回所有「严格大于 0」的数值中的最小值；没有正值时返回 0。
func MinPositiveFloat(values ...float64) float64 {
	best := 0.0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if best == 0 || value < best {
			best = value
		}
	}
	return best
}

// EntrySpreadRate 计算开仓时现货腿与合约腿之间的入场价差率（以现货 VWAP 为分母）。
// negativeCarry=true（负向套利：做多现货/做空合约的反向，即买合约卖现货）时取 (futures-spot)/spot，
// 否则取 (spot-futures)/spot。任一价格 <=0 时返回 0。
func EntrySpreadRate(negativeCarry bool, spotVWAP float64, futuresVWAP float64) float64 {
	if spotVWAP <= 0 || futuresVWAP <= 0 {
		return 0
	}
	if negativeCarry {
		return (futuresVWAP - spotVWAP) / spotVWAP
	}
	return (spotVWAP - futuresVWAP) / spotVWAP
}

// PriceMovedAdversely 判断现货/合约的最新价相对上一次观测是否已朝不利方向漂移超过 driftRate。
// negativeCarry 决定哪一侧的下行/上行算「不利」。任一价格 <=0 的腿不参与判定。
func PriceMovedAdversely(negativeCarry bool, previousSpot, currentSpot, previousFutures, currentFutures, driftRate float64) bool {
	if negativeCarry {
		spotWorse := previousSpot > 0 && currentSpot > 0 && currentSpot < previousSpot*(1-driftRate)
		futuresWorse := previousFutures > 0 && currentFutures > 0 && currentFutures > previousFutures*(1+driftRate)
		return spotWorse || futuresWorse
	}
	spotWorse := previousSpot > 0 && currentSpot > 0 && currentSpot > previousSpot*(1+driftRate)
	futuresWorse := previousFutures > 0 && currentFutures > 0 && currentFutures < previousFutures*(1-driftRate)
	return spotWorse || futuresWorse
}

// Staleness 返回事件相对 now 的陈旧度：eventMillis<=0（无时间戳）或 now 早于事件（时钟漂移）时返回 0。
func Staleness(now time.Time, eventMillis int64) time.Duration {
	if eventMillis <= 0 {
		return 0
	}
	age := now.Sub(time.UnixMilli(eventMillis))
	if age < 0 {
		return 0
	}
	return age
}
