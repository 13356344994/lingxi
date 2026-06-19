package app

import "lingxi/internal/core"

const defaultMinLiquidationDistanceRate = 0.15

// BaseMinLiquidationDistanceRate 强平距离阈值单一来源:优先 RiskConfig.MinLiquidationDistanceRate,
// 其次 CrossVenueExecutor.MinLiqDistance(legacy YAML 兼容),最后默认 0.15。0 表示关闭该维度。
func BaseMinLiquidationDistanceRate(risk core.RiskConfig, crossVenue core.CrossVenueExecutorConfig) float64 {
	if risk.MinLiquidationDistanceRate > 0 {
		return risk.MinLiquidationDistanceRate
	}
	if crossVenue.MinLiqDistance > 0 {
		return crossVenue.MinLiqDistance
	}
	return defaultMinLiquidationDistanceRate
}

// SymbolVolatilityMultiplier 可选 hook:返回 symbol 相对基准波动倍数(≥1 才放大 minDist)。
// 未注入或返回 ≤0 时不做 vol 缩放。接入近 N 根 kline realized vol 等信号时在外部赋值即可。
type SymbolVolatilityMultiplier func(symbol string) float64

// EffectiveMinLiquidationDistance 计算生效强平距离下限。
// volMult 来自 SymbolVolatilityMultiplier hook;未启用 vol 缩放或 hook 无信号时等价于 base(向后兼容)。
func EffectiveMinLiquidationDistance(risk core.RiskConfig, crossVenue core.CrossVenueExecutorConfig, symbol string, volMult float64) float64 {
	base := BaseMinLiquidationDistanceRate(risk, crossVenue)
	if base <= 0 {
		return 0
	}
	if !risk.MinLiqDistanceVolScaleEnabled {
		return base
	}
	if volMult <= 0 {
		volMult = 1.0
	}
	if volMult < 1.0 {
		volMult = 1.0 // 更严不更松:低波动不缩小缓冲
	}
	if factor := risk.MinLiqDistanceVolScaleFactor; factor > 0 {
		volMult *= factor
	}
	_ = symbol // 预留:vol hook 可按 symbol 查表,当前由调用方传入 volMult
	return base * volMult
}
