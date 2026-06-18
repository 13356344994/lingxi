package app

import "math"

// effectiveLossDrawdownThresholdUSDT 亏损/回撤类有效阈值(纯函数)。
// pct>0 且 equity>0: max(usdtFloor, pct×equity); pct>0 且 equity≤0: 退回 usdtFloor;
// pct=0: 仅 usdtFloor(向后兼容)。
func effectiveLossDrawdownThresholdUSDT(usdtFloor, pct, equityUSDT float64) float64 {
	if pct <= 0 {
		return usdtFloor
	}
	if equityUSDT <= 0 {
		return usdtFloor
	}
	scaled := pct * equityUSDT
	if usdtFloor <= 0 {
		return scaled
	}
	return math.Max(usdtFloor, scaled)
}

// effectiveOpenNotionalLimitUSDT 在场名义上限: max_gross_leverage×当前权益,USDT 封顶作小账户 floor。
// leverage=0 时退回固定 usdtCap。
func effectiveOpenNotionalLimitUSDT(usdtCap, maxGrossLeverage, equityUSDT float64) float64 {
	if maxGrossLeverage > 0 && equityUSDT > 0 {
		scaled := maxGrossLeverage * equityUSDT
		if usdtCap > 0 {
			return math.Max(usdtCap, scaled)
		}
		return scaled
	}
	return usdtCap
}

// effectiveTotalDeployableUSDT 全局可部署预算: pct>0 且 equity>0 → pct×equity; 否则 usdtCap。
func effectiveTotalDeployableUSDT(usdtCap, pct, equityUSDT float64) float64 {
	if pct > 0 && equityUSDT > 0 {
		return pct * equityUSDT
	}
	return usdtCap
}

// effectiveMaxDailyLossUSDT 日亏硬停有效阈值;归一基准=RealEquityTotalUSDT(当前真实现金权益)。
func effectiveMaxDailyLossUSDT(cfg RiskConfig, equityCurrentUSDT float64) float64 {
	return effectiveLossDrawdownThresholdUSDT(cfg.MaxDailyLossUSDT, cfg.MaxDailyLossPct, equityCurrentUSDT)
}

// effectiveMaxAccountDrawdownUSDT settlements 累计回撤有效阈值;归一基准=RealEquityPeakUSDT(峰值权益,与回撤语义一致)。
func effectiveMaxAccountDrawdownUSDT(cfg RiskConfig, equityPeakUSDT float64) float64 {
	return effectiveLossDrawdownThresholdUSDT(cfg.MaxAccountDrawdownUSDT, cfg.MaxAccountDrawdownPct, equityPeakUSDT)
}

// effectiveMaxStrategyCumulativeLossUSDT 单策略累计净亏有效阈值;归一基准=RealEquityTotalUSDT。
func effectiveMaxStrategyCumulativeLossUSDT(cfg RiskConfig, equityCurrentUSDT float64) float64 {
	return effectiveLossDrawdownThresholdUSDT(cfg.MaxStrategyCumulativeLossUSDT, cfg.MaxStrategyCumulativeLossPct, equityCurrentUSDT)
}

// effectiveMaxAccountOpenNotionalUSDT 在场总名义有效上限;优先 max_gross_leverage×RealEquityTotalUSDT。
func effectiveMaxAccountOpenNotionalUSDT(cfg RiskConfig, equityCurrentUSDT float64) float64 {
	return effectiveOpenNotionalLimitUSDT(cfg.MaxAccountOpenNotionalUSDT, cfg.MaxGrossLeverage, equityCurrentUSDT)
}

// effectiveAutoFlattenAccountDrawdownUSDT 二级自动全平有效阈值;归一基准=RealEquityPeakUSDT(真实现金峰值)。
func effectiveAutoFlattenAccountDrawdownUSDT(cfg RiskConfig, equityPeakUSDT float64) float64 {
	usdt := cfg.AutoFlattenAccountDrawdownUSDT
	if usdt <= 0 {
		usdt = defaultAutoFlattenAccountDrawdownUSDT
	}
	return effectiveLossDrawdownThresholdUSDT(usdt, cfg.AutoFlattenAccountDrawdownPct, equityPeakUSDT)
}
