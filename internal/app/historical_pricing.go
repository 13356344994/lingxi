package app

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/strategy"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史回测的纯定价/费率内核 ——
// 资金费方向(positive/negative carry)的语义判定、资金费收入与两腿盈亏、借币利息、滑点/冲击/
// 基差、保守资金费率推断，以及 any→float/int 解析助手（同包搬移，行为不变），让主文件收敛到
// 「编排 + 模拟状态机」职责并向单文件 ≤ 800 行门禁靠拢。

type fundingCarryDirection string

const (
	fundingCarryDirectionPositive fundingCarryDirection = "positive_carry"
	fundingCarryDirectionNegative fundingCarryDirection = "negative_carry"
	// 方向模式常量的单一来源在 internal/strategy；这里保留同名别名让既有引用零改动。
	historicalFundingModePositive = strategy.FundingModePositive
	historicalFundingModeNegative = strategy.FundingModeNegative
	historicalFundingModeBoth     = strategy.FundingModeBoth
)

func normalizeHistoricalFundingDirectionMode(mode string) string {
	return strategy.NormalizeFundingDirectionMode(mode)
}

func isValidHistoricalFundingDirectionMode(mode string) bool {
	return strategy.IsValidFundingDirectionMode(mode)
}

func fundingDirectionFromRate(rate float64) fundingCarryDirection {
	if rate < 0 {
		return fundingCarryDirectionNegative
	}
	if rate > 0 {
		return fundingCarryDirectionPositive
	}
	return ""
}

func historicalFundingDirectionEnabled(mode string, direction fundingCarryDirection) bool {
	switch normalizeHistoricalFundingDirectionMode(mode) {
	case historicalFundingModeNegative:
		return direction == fundingCarryDirectionNegative
	case historicalFundingModeBoth:
		return direction == fundingCarryDirectionPositive || direction == fundingCarryDirectionNegative
	default:
		return direction == fundingCarryDirectionPositive
	}
}

func historicalFundingIncome(direction fundingCarryDirection, hedgeQty float64, markPrice float64, fundingRate float64, cycles int) float64 {
	if hedgeQty <= 0 || markPrice <= 0 || cycles <= 0 {
		return 0
	}
	multiplier := 1.0
	if direction == fundingCarryDirectionNegative {
		multiplier = -1
	}
	return hedgeQty * markPrice * fundingRate * float64(cycles) * multiplier
}

func (d fundingCarryDirection) spotGrossPnL(hedgeQty float64, entryPrice float64, exitPrice float64) float64 {
	switch d {
	case fundingCarryDirectionNegative:
		return hedgeQty * (entryPrice - exitPrice)
	default:
		return hedgeQty * (exitPrice - entryPrice)
	}
}

func (d fundingCarryDirection) futuresGrossPnL(hedgeQty float64, entryPrice float64, exitPrice float64) float64 {
	switch d {
	case fundingCarryDirectionNegative:
		return hedgeQty * (exitPrice - entryPrice)
	default:
		return hedgeQty * (entryPrice - exitPrice)
	}
}

// historicalFundingPriorityScore 仅依据「点位预估费率」(EstimatedFundingRate)给候选打分。
// 这里刻意不再回退到已实现的 FundingRate——用结算后的真实费率给候选排序/截断会引入经典的前视偏差
// (look-ahead bias)，等于用未来已知的结果反推选币。点位预估缺失时返回 0，由上游确保使用点位数据。
func historicalFundingPriorityScore(item FundingRateHistoryItem, mode string) float64 {
	return strategy.FundingPriorityScore(parseFloat(item.EstimatedFundingRate), mode)
}

func monthlyPreselectPriorityScore(item FundingRateHistoryItem, mode string) float64 {
	return strategy.PreselectPriorityScore(parseFloat(item.ScreeningScore), parseFloat(item.EstimatedFundingRate), mode)
}

func monthlyCandidateScreeningPriorityScore(candidate monthlyCandidate) float64 {
	if candidate.ScreeningScore != 0 {
		return candidate.ScreeningScore
	}
	return math.Abs(candidate.EstimatedFundingRate)
}

func alignHistoricalEstimatedFundingRate(estimatedRate float64, previousFundingRate float64) float64 {
	estimatedDirection := fundingDirectionFromRate(estimatedRate)
	previousDirection := fundingDirectionFromRate(previousFundingRate)
	if estimatedDirection == "" || previousDirection == "" {
		return estimatedRate
	}
	if estimatedDirection != previousDirection {
		return 0
	}
	return estimatedRate
}

func historicalBorrowInterestHours(direction fundingCarryDirection, entryTime time.Time, exitTime time.Time, minBillingHours int) int {
	if direction != fundingCarryDirectionNegative {
		return 0
	}
	hours := 1
	if exitTime.After(entryTime) {
		hours = int(math.Ceil(exitTime.Sub(entryTime).Hours()))
		if hours < 1 {
			hours = 1
		}
	}
	if minBillingHours > hours {
		hours = minBillingHours
	}
	return hours
}

func historicalBorrowInterestCost(direction fundingCarryDirection, notionalUSDT float64, hourlyRate float64, billedHours int) float64 {
	if direction != fundingCarryDirectionNegative || notionalUSDT <= 0 || hourlyRate <= 0 || billedHours <= 0 {
		return 0
	}
	return notionalUSDT * hourlyRate * float64(billedHours)
}

func historicalSpotImpactMultiplier(hcfg historicalConfig, direction fundingCarryDirection, isEntry bool) float64 {
	if direction != fundingCarryDirectionNegative {
		return 1
	}
	if isEntry {
		return math.Max(hcfg.NegativeCarrySpotEntryImpactMultiplier, 1)
	}
	return math.Max(hcfg.NegativeCarrySpotExitImpactMultiplier, 1)
}

func computeBasisRate(spotPrice float64, futuresPrice float64) float64 {
	return strategy.ComputeBasisRate(spotPrice, futuresPrice)
}

func computeAdverseBasisPenalty(openBasisRate float64, spotEntryNotional float64, futuresEntryNotional float64) float64 {
	return strategy.ComputeAdverseBasisPenalty(openBasisRate, spotEntryNotional, futuresEntryNotional)
}

func ratioOrZero(numerator float64, denominator float64) float64 {
	return strategy.RatioOrZero(numerator, denominator)
}

func computeDrawdownRate(peakEquity float64, equity float64) float64 {
	return strategy.ComputeDrawdownRate(peakEquity, equity)
}

func estimateHistoricalSlipRate(kline MarketKline) float64 {
	if kline.Open <= 0 {
		return 0.0003
	}
	rangeRate := math.Abs(kline.High-kline.Low) / kline.Open
	return math.Max(0.0003, rangeRate*0.10)
}

func estimateMarketImpactRate(notionalUSDT float64, quoteVolume float64) float64 {
	return strategy.EstimateMarketImpactRate(notionalUSDT, quoteVolume)
}

func conservativeHistoricalFundingRate(premiumIndexRate float64, markIndexBasisRate float64) float64 {
	const (
		singleSourceHaircut     = 0.10
		confirmedBaseHaircut    = 0.10
		confirmedAgreementBonus = 0.10
		minAgreementFloor       = 0.35
	)
	premiumSign := 0.0
	if premiumIndexRate > 0 {
		premiumSign = 1
	} else if premiumIndexRate < 0 {
		premiumSign = -1
	}
	basisSign := 0.0
	if markIndexBasisRate > 0 {
		basisSign = 1
	} else if markIndexBasisRate < 0 {
		basisSign = -1
	}
	absPremium := math.Abs(premiumIndexRate)
	absBasis := math.Abs(markIndexBasisRate)
	if absPremium <= 0 && absBasis <= 0 {
		return 0
	}
	switch {
	case absPremium > 0 && absBasis > 0 && premiumSign != 0 && basisSign != 0 && premiumSign != basisSign:
		return 0
	case absPremium > 0 && absBasis <= 0:
		return premiumSign * absPremium * singleSourceHaircut
	case absBasis > 0 && absPremium <= 0:
		return basisSign * absBasis * singleSourceHaircut
	}
	sign := premiumSign
	if sign == 0 {
		sign = basisSign
	}
	lower := math.Min(absPremium, absBasis)
	upper := math.Max(absPremium, absBasis)
	if lower <= 0 || upper <= 0 {
		return 0
	}
	agreement := lower / upper
	if agreement < minAgreementFloor {
		return sign * lower * singleSourceHaircut
	}
	haircut := confirmedBaseHaircut + confirmedAgreementBonus*agreement
	return sign * lower * haircut
}

func parseAnyFloat(value any) float64 {
	switch typed := value.(type) {
	case string:
		return parseFloat(typed)
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	default:
		return parseFloat(fmt.Sprint(value))
	}
}

func parseAnyInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
		return parsed
	}
}
