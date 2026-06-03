// Package strategy 收敛选币 / 打分 / 容量过滤的纯决策逻辑（Gate 3）。
//
// 本包只接收原始数值与字符串，不依赖引擎结构体、网络或交易所类型，因此可以
// 完全脱离网络做单元测试（对应 Gate 3 门禁：internal/strategy 能脱离网络纯单测）。
// 主包保留同名薄适配函数委托到这里，既建立包边界，又让既有调用点零改动。
package strategy

import (
	"math"
	"strings"
)

// 资金费方向模式。决定候选打分时如何对待正/负费率。
const (
	FundingModePositive = "positive_only"
	FundingModeNegative = "negative_only"
	FundingModeBoth     = "both"
)

// NormalizeFundingDirectionMode 把任意输入规整为受支持的方向模式，未知输入退化为 Both。
func NormalizeFundingDirectionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case FundingModePositive:
		return FundingModePositive
	case FundingModeNegative:
		return FundingModeNegative
	case FundingModeBoth:
		return FundingModeBoth
	default:
		return FundingModeBoth
	}
}

// IsValidFundingDirectionMode 报告 mode 是否为受支持的方向模式（大小写/空白不敏感）。
func IsValidFundingDirectionMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case FundingModePositive, FundingModeNegative, FundingModeBoth:
		return true
	default:
		return false
	}
}

// FundingPriorityScore 仅依据「点位预估费率」给候选打分。
//
// 这里刻意只吃预估费率，不接受已结算的真实费率——用未来已知的结算结果反推选币是
// 经典的前视偏差（look-ahead bias）。调用方负责传入点位预估值；缺失时传 0 即得 0 分。
func FundingPriorityScore(estimatedRate float64, mode string) float64 {
	switch NormalizeFundingDirectionMode(mode) {
	case FundingModeNegative:
		return math.Max(-estimatedRate, 0)
	case FundingModeBoth:
		return math.Abs(estimatedRate)
	default:
		return math.Max(estimatedRate, 0)
	}
}

// PreselectPriorityScore 预筛排序分：优先用已算好的筛选分，缺失（为 0）时退回点位预估打分。
func PreselectPriorityScore(screeningScore, estimatedRate float64, mode string) float64 {
	if screeningScore != 0 {
		return screeningScore
	}
	return FundingPriorityScore(estimatedRate, mode)
}

// ComputeBasisRate 计算 (现货价 - 合约价) / 现货价 的基差率。现货价非正时返回 0。
func ComputeBasisRate(spotPrice, futuresPrice float64) float64 {
	if spotPrice <= 0 {
		return 0
	}
	return (spotPrice - futuresPrice) / spotPrice
}

// ComputeAdverseBasisPenalty 仅对不利方向（开仓基差 >= 0）计提名义惩罚，取双腿名义较大者。
func ComputeAdverseBasisPenalty(openBasisRate, spotEntryNotional, futuresEntryNotional float64) float64 {
	if openBasisRate >= 0 {
		return openBasisRate * math.Max(spotEntryNotional, futuresEntryNotional)
	}
	return 0
}

// RatioOrZero 返回 numerator/denominator，任一非正时返回 0，避免除零与负值传播。
func RatioOrZero(numerator, denominator float64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

// ComputeDrawdownRate 返回相对峰值权益的回撤率；权益不低于峰值或峰值非正时返回 0。
func ComputeDrawdownRate(peakEquity, equity float64) float64 {
	if peakEquity <= 0 || equity >= peakEquity {
		return 0
	}
	return (peakEquity - equity) / peakEquity
}

// EstimateMarketImpactRate 用名义占成交额的比例估算市场冲击率，并夹在 [0.0001, 0.0025]。
// 缺乏成交额数据时返回保守上限 0.0025。
func EstimateMarketImpactRate(notionalUSDT, quoteVolume float64) float64 {
	if notionalUSDT <= 0 || quoteVolume <= 0 {
		return 0.0025
	}
	impact := (notionalUSDT / quoteVolume) * 0.50
	return math.Max(0.0001, math.Min(0.0025, impact))
}
