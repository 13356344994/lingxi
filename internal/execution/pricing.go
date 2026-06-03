// Package execution 承载与交易所执行相关的「纯计算内核」：限价/保护单定价、追价滑点调度、
// 价格-步进对齐等。这些函数只依赖基础类型，不触碰引擎状态/网络/缓存，因此可被实盘引擎与回测
// 共用并独立做单元测试，是 Gate 3「真正包边界抽离」的执行域落点。
package execution

import (
	"math"
	"strings"
)

// FloorToStep 把 value 向下对齐到 step 的整数倍（step<=0 时原样返回）。
// 加 1e-12 容差以抵消浮点误差导致的「差一步」问题。
func FloorToStep(value float64, step float64) float64 {
	if step <= 0 {
		return value
	}
	steps := math.Floor((value + 1e-12) / step)
	return steps * step
}

// CeilToStep 把 value 向上对齐到 step 的整数倍（step<=0 时原样返回）。
func CeilToStep(value float64, step float64) float64 {
	if step <= 0 {
		return value
	}
	steps := math.Ceil((value - 1e-12) / step)
	return steps * step
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

// MakerLimitPrice 计算被动 maker 限价：BUY 取 bestBid 向下对齐、SELL 取 bestAsk 向上对齐，
// 以保证挂单尽量是 maker。报价缺失或方向非法时返回 0。
func MakerLimitPrice(side string, bestBid float64, bestAsk float64, tickSize float64) float64 {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		if bestBid <= 0 {
			return 0
		}
		if tickSize > 0 {
			return FloorToStep(bestBid, tickSize)
		}
		return bestBid
	case "SELL":
		if bestAsk <= 0 {
			return 0
		}
		if tickSize > 0 {
			return CeilToStep(bestAsk, tickSize)
		}
		return bestAsk
	default:
		return 0
	}
}

// ProtectedLimitPrice 在参考价上施加方向性滑点得到保护性限价（未对齐 tick）：
// BUY 抬价、SELL 压价（SELL 至少保留 0.0001 比例避免归零）。
func ProtectedLimitPrice(side string, referencePrice float64, slippageRate float64) float64 {
	referencePrice = nonNegative(referencePrice)
	if referencePrice <= 0 {
		return 0
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		return referencePrice * (1 + nonNegative(slippageRate))
	case "SELL":
		return referencePrice * math.Max(1-nonNegative(slippageRate), 0.0001)
	default:
		return referencePrice
	}
}

// NormalizeProtectedLimitPrice 在 ProtectedLimitPrice 基础上对齐到 tick：
// BUY 向上对齐（更易成交）、SELL 向下对齐。tick<=0 时返回未对齐价。
func NormalizeProtectedLimitPrice(side string, referencePrice float64, slippageRate float64, tickSize float64) float64 {
	limitPrice := ProtectedLimitPrice(side, referencePrice, slippageRate)
	if limitPrice <= 0 || tickSize <= 0 {
		return limitPrice
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		return CeilToStep(limitPrice, tickSize)
	case "SELL":
		return FloorToStep(limitPrice, tickSize)
	default:
		return limitPrice
	}
}

// WidenedProtectedLimitPrice 用放大倍数的滑点扩宽保护限价，供盲态/保护单失败回退时使用。
func WidenedProtectedLimitPrice(side string, referencePrice float64, slippageRate float64, tickSize float64, multiplier float64) float64 {
	return NormalizeProtectedLimitPrice(side, referencePrice, slippageRate*multiplier, tickSize)
}

// ClampChaseLimitPrice 把追价限价钳制在硬上限内：SELL 不得低于 hardLimit、BUY 不得高于 hardLimit，
// 以约束追价过程中的最大让价。任一价格 <=0 时不钳制。
func ClampChaseLimitPrice(side string, limitPrice float64, hardLimitPrice float64) float64 {
	if hardLimitPrice <= 0 || limitPrice <= 0 {
		return limitPrice
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "SELL":
		return math.Max(limitPrice, hardLimitPrice)
	case "BUY":
		return math.Min(limitPrice, hardLimitPrice)
	default:
		return limitPrice
	}
}

// SlippageForAttempt 按调度表取第 attempt 次追价的滑点比例：命中且为正取该值；
// 越界则回退到末位有效值，调度为空或末位非正时回退到 fallback。
func SlippageForAttempt(schedule []float64, attempt int, fallback float64) float64 {
	if attempt < 0 {
		attempt = 0
	}
	if attempt < len(schedule) && schedule[attempt] > 0 {
		return schedule[attempt]
	}
	if len(schedule) == 0 {
		return fallback
	}
	last := schedule[len(schedule)-1]
	if last <= 0 {
		return fallback
	}
	return last
}

// ChaseOptions 描述追价的锚定参数。
type ChaseOptions struct {
	// AnchorPrice 是锚定参考价（开/平时的目标价），>0 时在锚定期内固定使用。
	AnchorPrice float64
	// HardLimitPrice 是追价让价的硬上限（由最大滑点归一化得到）。
	HardLimitPrice float64
	// ReanchorAfter 表示在第几次尝试后改用市场价（<=0 表示始终用锚价）。
	ReanchorAfter int
}

// ChaseReferencePrice 决定第 attempt 次追价的参考价：锚定期内用 AnchorPrice，
// 之后优先用市场价，市场价缺失时回退到锚价。
func ChaseReferencePrice(marketReference float64, attempt int, options ChaseOptions) float64 {
	if options.AnchorPrice > 0 {
		if options.ReanchorAfter <= 0 || attempt < options.ReanchorAfter {
			return options.AnchorPrice
		}
	}
	if marketReference > 0 {
		return marketReference
	}
	return options.AnchorPrice
}

// BuildAnchoredChaseOptions 由锚价与滑点调度构造追价选项：
// HardLimitPrice 取「调度内最大滑点」对应的归一化保护价，ReanchorAfter 设为 attempts-1。
// anchorPrice<=0 时返回零值（表示不锚定）。
func BuildAnchoredChaseOptions(side string, anchorPrice float64, tickSize float64, schedule []float64, attempts int, fallbackSlippage float64) ChaseOptions {
	if anchorPrice <= 0 {
		return ChaseOptions{}
	}
	maxSlippage := 0.0
	for attempt := 0; attempt < attempts; attempt++ {
		maxSlippage = math.Max(maxSlippage, SlippageForAttempt(schedule, attempt, fallbackSlippage))
	}
	return ChaseOptions{
		AnchorPrice:    anchorPrice,
		HardLimitPrice: NormalizeProtectedLimitPrice(side, anchorPrice, maxSlippage, tickSize),
		ReanchorAfter:  attempts - 1,
	}
}
