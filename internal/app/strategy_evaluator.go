package app

import (
	"fmt"
	"math"
	"strings"
)

// FundingStrategy 描述独立的策略评估层接口
// 策略层不应直接访问网络、配置全局单例，或进行状态改变。
// 它只负责纯函数的输入（Snapshot/Budget/Gate）与输出（Decision）。
type FundingStrategy interface {
	// EvaluateScanCandidate 在海选阶段评估标的
	EvaluateScanCandidate(
		candidate FundingCandidate,
		funding FundingSnapshot,
		profile symbolRiskProfile,
		config EngineConfig,
	) (StrategyAction, string)

	// EvaluateMonitorTick 在高频监控阶段评估标的健康度
	EvaluateMonitorTick(
		candidate FundingCandidate,
		netProfitRate float64,
		profile symbolRiskProfile,
		config EngineConfig,
		currentBreaches int,
	) (StrategyAction, string)

	// EvaluateExecutionOpportunity 在执行前进行全方位的容量与流动性校验
	EvaluateExecutionOpportunity(
		candidate FundingCandidate,
		snapshot MarketCapacitySnapshot,
		referencePrice float64,
		profile symbolRiskProfile,
		config EngineConfig,
	) (float64, string, error)
}

// coherentMinFundingRate 返回「值得进入监控阶段」的最低预估资金费率，即一次单周期对冲往返的真实
// 盈亏平衡线再叠加健康净利目标：
//
//	breakEven = 往返费用预留(FeeReserveRate) + 退出价差预留(ExitSpreadReserveRate) + 健康净利门(MinNetProfitRate)
//
// 预估资金费率低于该值时，监控阶段 estimateReservedNetProfitRate 扣除上述成本后净收益必然落在健康门
// 之下（价差/借币/残腿成本只会让它更低），开仓注定亏损或被拒。海选阶段用它给地板线兜底，使粗筛门槛与
// 监控期净利门保持自洽，避免把结构性亏损单放进监控、白白消耗预算与执行窗口。
// 公式归属已收敛到 EconomicParams.BreakEven()(P2.1);本函数保留为薄包装,确保所有按 cfg 取值的
// 旧调用点(纯策略层 / 回测 / 测试)逐位等价不变。引擎侧改走 e.economicParams().BreakEven()。
func coherentMinFundingRate(config EngineConfig) float64 {
	return economicParamsFromConfig(config).BreakEven()
}

// isBasisFavorableForCarry 判定基差是否与 carry 方向同向(对仓位是方向性顺风)。
//   - 负 carry = 长永续 + 短现货 ⇒ 长「基差」(perp−spot);基差为负(perp 折价)时,收敛上行利好 ⇒ 有利。
//   - 正 carry = 短永续 + 长现货 ⇒ 短「基差」;基差为正(perp 溢价)时,收敛下行利好 ⇒ 有利。
//
// 基差为 0 或方向未定时视为「不有利」(不放宽),保守。
func isBasisFavorableForCarry(direction fundingCarryDirection, basisRate float64) bool {
	if basisRate == 0 {
		return false
	}
	switch direction {
	case fundingCarryDirectionNegative:
		return basisRate < 0
	case fundingCarryDirectionPositive:
		return basisRate > 0
	default:
		return false
	}
}

// effectiveMaxScanBasisRate 计算当前候选适用的基差上限:基差与 carry 同向(有利)且配置了更高的
// FavorableBasisMaxRate(且 ≥ 常规上限)时用更高上限;否则用常规 MaxScanBasisRate。纯函数,scan/monitor 共用。
func effectiveMaxScanBasisRate(config EngineConfig, direction fundingCarryDirection, basisRate float64) float64 {
	base := config.StrategyConfig.MaxScanBasisRate
	favorable := config.StrategyConfig.FavorableBasisMaxRate
	if favorable > base && isBasisFavorableForCarry(direction, basisRate) {
		return favorable
	}
	return base
}

// DefaultFundingStrategy 默认资金费率套利策略实现
type DefaultFundingStrategy struct{}

func NewDefaultFundingStrategy() *DefaultFundingStrategy {
	return &DefaultFundingStrategy{}
}

func (s *DefaultFundingStrategy) EvaluateScanCandidate(
	candidate FundingCandidate,
	funding FundingSnapshot,
	profile symbolRiskProfile,
	config EngineConfig,
) (StrategyAction, string) {
	if funding.IsStale {
		return StrategyActionReject, "资金费率数据过期"
	}
	if funding.FundingJump >= config.StrategyConfig.MaxFundingRateJump {
		return StrategyActionReject, fmt.Sprintf("资金费率跳跃度过大 (%.4f >= %.4f)", funding.FundingJump, config.StrategyConfig.MaxFundingRateJump)
	}
	if maxBasis := effectiveMaxScanBasisRate(config, funding.Direction, funding.BasisRate); math.Abs(funding.BasisRate) > maxBasis {
		return StrategyActionReject, fmt.Sprintf("基差率过大 (%.4f > %.4f)", math.Abs(funding.BasisRate), maxBasis)
	}

	// 计算建议的最低预期收益门槛。海选地板线不允许低于「真实盈亏平衡线」：
	// 低于该线的候选，即便价差/借币/残腿成本全为零，扣掉往返费用预留与退出价差预留后也越不过健康
	// 净利门(MinNetProfitRate)，监控阶段必然拒绝。提前用 coherentMinFundingRate 兜底，避免地板线被
	// 配置成低于成本而放进结构性亏损单。
	// 正 carry 可走解耦的更低地板线(免借币、无可借性风险);负 carry 仍走 MinEstimatedFundingRate。
	// 无论方向,都以真实盈亏平衡线 coherentMinFundingRate 硬兜底,绝不放进结构性亏损单。
	configFloor := config.StrategyConfig.MinEstimatedFundingRate
	if funding.Direction != fundingCarryDirectionNegative {
		if pos := config.StrategyConfig.PositiveMinEstimatedFundingRate; pos > 0 {
			configFloor = pos
		}
	}
	minRate := maxFloat(configFloor, coherentMinFundingRate(config)) * profile.MinEstimatedRateMultiplier
	maxRate := config.StrategyConfig.MaxEstimatedFundingRate

	// 方向校验和收益率大小校验
	if funding.Direction == fundingCarryDirectionNegative {
		if funding.EstimatedRate > -minRate {
			return StrategyActionReject, fmt.Sprintf("负向费率绝对值不足 (%.4f > %.4f)", funding.EstimatedRate, -minRate)
		}
		if maxRate > 0 && funding.EstimatedRate < -maxRate {
			return StrategyActionReject, fmt.Sprintf("负向费率绝对值过高 (%.4f < %.4f)", funding.EstimatedRate, -maxRate)
		}
	} else {
		if funding.EstimatedRate < minRate {
			return StrategyActionReject, fmt.Sprintf("正向费率不足 (%.4f < %.4f)", funding.EstimatedRate, minRate)
		}
		if maxRate > 0 && funding.EstimatedRate > maxRate {
			return StrategyActionReject, fmt.Sprintf("正向费率过高 (%.4f > %.4f)", funding.EstimatedRate, maxRate)
		}
	}

	return StrategyActionOpen, ""
}

func (s *DefaultFundingStrategy) EvaluateMonitorTick(
	candidate FundingCandidate,
	netProfitRate float64,
	profile symbolRiskProfile,
	config EngineConfig,
	currentBreaches int,
) (StrategyAction, string) {
	immediateInvalidateRate := config.RiskConfig.ImmediateInvalidateRate + profile.ImmediateInvalidateBump
	minNetProfitRate := config.RiskConfig.MinNetProfitRate * profile.MinNetProfitMultiplier

	if netProfitRate <= immediateInvalidateRate {
		return StrategyActionReject, fmt.Sprintf("净收益率低于作废阈值 (%.6f <= %.6f)", netProfitRate, immediateInvalidateRate)
	}

	if netProfitRate <= minNetProfitRate {
		return StrategyActionObserve, fmt.Sprintf("净收益率处于观察区间 (%.6f <= %.6f)", netProfitRate, minNetProfitRate)
	}

	return StrategyActionOpen, "净收益回到健康区间"
}

func (s *DefaultFundingStrategy) EvaluateExecutionOpportunity(
	candidate FundingCandidate,
	snapshot MarketCapacitySnapshot,
	referencePrice float64,
	profile symbolRiskProfile,
	config EngineConfig,
) (float64, string, error) {

	if candidate.OrderQuantity <= 0 {
		return 0, "target_or_quantity_invalid", nil
	}

	if referencePrice <= 0 {
		return 0, "reference_price_invalid", fmt.Errorf("参考价格无效")
	}

	desiredQty := candidate.OrderQuantity
	desiredNotional := desiredQty * referencePrice

	limitNotional := math.Inf(1)
	var reasons []string
	reasons = append(reasons, fmt.Sprintf("desiredNotional=%.6f", desiredNotional))

	if snapshot.FuturesOpenInterestNotional > 0 && config.StrategyConfig.MaxOpenInterestShare > 0 {
		capNotional := snapshot.FuturesOpenInterestNotional * config.StrategyConfig.MaxOpenInterestShare * profile.MaxOpenInterestShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("openInterestNotional=%.6f cap=%.6f", snapshot.FuturesOpenInterestNotional, capNotional))
	}

	recentMin := snapshot.RecentSpotQuoteVolume
	if snapshot.RecentFuturesQuoteVolume > 0 && (recentMin == 0 || snapshot.RecentFuturesQuoteVolume < recentMin) {
		recentMin = snapshot.RecentFuturesQuoteVolume
	}
	if recentMin > 0 && config.StrategyConfig.MaxRecentVolumeShare > 0 {
		capNotional := recentMin * config.StrategyConfig.MaxRecentVolumeShare * profile.MaxRecentVolumeShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("recentQuoteVolume=%.6f cap=%.6f", recentMin, capNotional))
	}

	visibleMin := snapshot.VisibleSpotAskNotional
	if snapshot.VisibleFuturesBidNotional > 0 && (visibleMin == 0 || snapshot.VisibleFuturesBidNotional < visibleMin) {
		visibleMin = snapshot.VisibleFuturesBidNotional
	}
	if visibleMin > 0 && config.StrategyConfig.MaxVisibleDepthShare > 0 {
		capNotional := visibleMin * config.StrategyConfig.MaxVisibleDepthShare * profile.MaxVisibleDepthShareMul
		limitNotional = math.Min(limitNotional, capNotional)
		reasons = append(reasons, fmt.Sprintf("visibleDepthNotional=%.6f cap=%.6f", visibleMin, capNotional))
	}

	if math.IsInf(limitNotional, 1) {
		if snapshot.AvailableMetrics == 0 {
			return 0, strings.Join(reasons, " "), fmt.Errorf("市场容量指标不可用")
		}
		return desiredQty, strings.Join(reasons, " "), nil
	}
	if limitNotional <= 0 {
		return 0, strings.Join(reasons, " "), nil
	}
	if desiredNotional <= limitNotional+1e-9 {
		return desiredQty, strings.Join(reasons, " "), nil
	}

	limitedQty := limitNotional / referencePrice
	return limitedQty, strings.Join(reasons, " "), nil
}
