package app

import (
	"math"
	"strings"

	"lingxi/internal/exchange"
)

// defaultFundingRateCapAbs 是未在交易所 fundingInfo 中单独列出的 symbol 的默认资金费上下限绝对值。
// 币安 U 本位永续默认单次结算资金费封顶 ±2%;撞限豁免在缺少逐 symbol 数据时按此回退。
const defaultFundingRateCapAbs = 0.02

// fundingRateCapFloor 是一个 symbol 的资金费单次结算上下限(cap=封顶,floor=封底,通常对称如 +0.02 / -0.02)。
type fundingRateCapFloor struct {
	Cap   float64
	Floor float64
}

// loadFundingRateCaps 把交易所 fundingInfo 回执转成「symbol → 上下限」缓存,只收录给出有效上下限的条目。
func loadFundingRateCaps(items []exchange.FundingInfoItem) map[string]fundingRateCapFloor {
	caps := make(map[string]fundingRateCapFloor, len(items))
	for _, item := range items {
		symbol := strings.ToUpper(strings.TrimSpace(item.Symbol))
		if symbol == "" {
			continue
		}
		cap := parseFloat(item.AdjustedFundingRateCap)
		floor := parseFloat(item.AdjustedFundingRateFloor)
		if cap == 0 && floor == 0 {
			continue
		}
		caps[symbol] = fundingRateCapFloor{Cap: cap, Floor: floor}
	}
	return caps
}

// fundingCapFloorFor 返回某 symbol 的资金费上下限:优先用交易所 fundingInfo 缓存,缺失则回退默认 ±2%。
func (e *FundingRateSniperEngine) fundingCapFloorFor(symbol string) fundingRateCapFloor {
	if e != nil && e.fundingRateCapBySymbol != nil {
		if info, ok := e.fundingRateCapBySymbol[strings.ToUpper(strings.TrimSpace(symbol))]; ok {
			return info
		}
	}
	return fundingRateCapFloor{Cap: defaultFundingRateCapAbs, Floor: -defaultFundingRateCapAbs}
}

// capPinnedFundingEstimate 判定「实收资金费是否已贴住交易所上下限」,若是且豁免能提升同向确信度,
// 返回修正后的预估费率(lastFundingRate × factor)与 true。它是纯函数,便于单测且不触网。
//
// 触发条件(全部满足):
//   - proximity、factor 合法(均 > 0);
//   - lastFundingRate 与上限(正费率贴 cap)或下限(负费率贴 floor)同向且达到其 proximity 倍;
//   - 候选修正值 |last×factor| 严格大于当前 estimate(即原估计确被 R7/保守化下拉,豁免才有意义);
//   - 候选修正值与 last 同号(安全闸,杜绝反向开仓)。
//
// 任一不满足则原样返回 (estimate, false),完全保持既有行为。
func capPinnedFundingEstimate(last, estimate, cap, floor, proximity, factor float64) (float64, bool) {
	if proximity <= 0 || factor <= 0 {
		return estimate, false
	}

	pinned := false
	switch {
	case last < 0 && floor < 0:
		pinned = last <= floor*proximity
	case last > 0 && cap > 0:
		pinned = last >= cap*proximity
	}
	if !pinned {
		return estimate, false
	}

	candidate := last * factor
	if math.Abs(candidate) <= math.Abs(estimate) {
		return estimate, false
	}
	if (candidate > 0) != (last > 0) {
		return estimate, false
	}
	return candidate, true
}

// applyCapPinnedFundingOverride 是撞限豁免在引擎侧的统一入口:按配置开关 + 逐 symbol 上下限决定是否修正
// 预估费率,并在修正时重算跳变(fundingJump = |估计 − last|)。返回 (新估计, 新跳变, 是否已修正)。
// 开关关闭或未触发时原样返回输入,绝不改变既有判定。
func (e *FundingRateSniperEngine) applyCapPinnedFundingOverride(symbol string, lastFundingRate, estimatedRate, fundingJump float64) (float64, float64, bool) {
	if e == nil || !e.cfg.StrategyConfig.CapPinnedFundingOverrideEnabled {
		return estimatedRate, fundingJump, false
	}
	caps := e.fundingCapFloorFor(symbol)
	newEstimate, applied := capPinnedFundingEstimate(
		lastFundingRate,
		estimatedRate,
		caps.Cap,
		caps.Floor,
		e.cfg.StrategyConfig.CapPinnedFundingProximityRate,
		e.cfg.StrategyConfig.CapPinnedFundingEstimateFactor,
	)
	if !applied {
		return estimatedRate, fundingJump, false
	}
	newJump := math.Abs(newEstimate - lastFundingRate)
	if e.logger != nil {
		e.logger.Printf(
			"[撞限豁免] %s 实收费率=%.6f 已贴上下限[%.4f, %.4f] → 预估由 %.6f 修正为 %.6f(跳变 %.6f→%.6f),撞限改信持续不再保守归零",
			symbol, lastFundingRate, caps.Floor, caps.Cap, estimatedRate, newEstimate, fundingJump, newJump,
		)
	}
	return newEstimate, newJump, true
}
