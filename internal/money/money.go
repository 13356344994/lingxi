// Package money 提供以「整数最小单位」记账的定点金额内核，消除 float64 在多笔累加时的累积误差
// （Gate 4）。所有 USDT 金额以 micro-USDT（1e-6 USDT，下称 micros）的 int64 表示，加减与求和都是
// 精确整数运算；只有在跨越「人类可读的 float / 交易所字符串」边界时才做一次带四舍五入的换算。
//
// 选择 1e-6 精度的理由：币安 USDT 计价的最小报价/手续费精度通常不细于 1e-6，int64 在该精度下可表示
// 约 ±9.2e12 USDT，远超任何账户规模，不存在溢出风险。
package money

import (
	"fmt"
	"math"
)

// scale 是 1 USDT 对应的 micros 数（1e-6 精度）。
const scale = 1_000_000

// Micros 表示以 micro-USDT 为单位的定点金额。零值即 0 USDT。
type Micros int64

// FromUSDT 把浮点 USDT 金额换算为定点 micros，按「四舍五入、远离零」取整，避免系统性向下偏置。
// NaN/Inf 归零，保证记账不被污染。
func FromUSDT(usdt float64) Micros {
	if math.IsNaN(usdt) || math.IsInf(usdt, 0) {
		return 0
	}
	return Micros(math.Round(usdt * scale))
}

// USDT 把定点金额换算回浮点 USDT，仅用于展示 / 跨边界输出。
func (m Micros) USDT() float64 { return float64(m) / scale }

// Add 返回 m+other 的精确定点和。
func (m Micros) Add(other Micros) Micros { return m + other }

// Sub 返回 m-other 的精确定点差。
func (m Micros) Sub(other Micros) Micros { return m - other }

// Neg 返回相反数。
func (m Micros) Neg() Micros { return -m }

// MulRate 计算 m 的 rate 倍（如名义 × 费率），在浮点域相乘后定点取整。rate 非有限时返回 0。
func (m Micros) MulRate(rate float64) Micros {
	if math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	return Micros(math.Round(float64(m) * rate))
}

// IsZero / IsPositive / IsNegative 提供语义化判定。
func (m Micros) IsZero() bool     { return m == 0 }
func (m Micros) IsPositive() bool { return m > 0 }
func (m Micros) IsNegative() bool { return m < 0 }

// Abs 返回绝对值。
func (m Micros) Abs() Micros {
	if m < 0 {
		return -m
	}
	return m
}

// String 以固定 6 位小数渲染 USDT，便于日志与对账核对。
func (m Micros) String() string {
	neg := m < 0
	v := m
	if neg {
		v = -v
	}
	whole := int64(v) / scale
	frac := int64(v) % scale
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%06d", sign, whole, frac)
}

// SumUSDT 把一组浮点 USDT 金额先各自定点化再做整数求和，最后换回浮点。相比直接用 float64 连加，
// 它消除了累加顺序带来的舍入漂移：无论求和顺序如何，结果都逐位一致。
func SumUSDT(values ...float64) float64 {
	return Sum(values...).USDT()
}

// Sum 把一组浮点 USDT 金额定点化后精确累加，返回定点结果。
func Sum(values ...float64) Micros {
	var total Micros
	for _, v := range values {
		total += FromUSDT(v)
	}
	return total
}
