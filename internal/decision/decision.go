// Package decision 实现路由器的纯逻辑内核（对应计划第 7 节「极端仓位、天道信心与账户分拣」的
// 单标的部分）：开放世界 OOD 判定、天道信心的「最弱环节」聚合、N_hard 硬上限求交、N_target 缩放
// 与整数 lot 分配。
//
// 本包只依赖 internal/money 与 internal/mechanism，不依赖引擎 / 网络，可脱网单测与确定性回放。
//
// 三条不可逾越的纪律在这里落地：
//   - 信心只能缩放仓位，绝不能突破硬上限（NTarget 恒被夹到 N_hard 以内）；
//   - N_hard 由多来源取「最小」（任一硬约束都是天花板）；
//   - 整数 lot 只向下取整；理论仓位低于交易所最小单位则跳过，绝不为成交向上取整突破风险。
package decision

import (
	"math"

	"lingxi/internal/mechanism"
	"lingxi/internal/money"
)

// clamp01 把值夹到 [0,1]；非有限值归 0（保守）。
func clamp01(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// OODVerdict 开放世界判定结果。
type OODVerdict struct {
	// OutOfDistribution 观测是否落在已知分布之外。
	OutOfDistribution bool
	// ForceFlat 是否必须空仓（OOD、数据红灯或未知制度都会强制空仓）。
	ForceFlat bool
	// Reasons 触发原因（可叠加，用于审计）。
	Reasons []string
}

// OODConfig 开放世界判定的阈值配置。零值给出保守默认（见 DefaultOODConfig）。
type OODConfig struct {
	// MaxDataAgeMs 允许的最大数据年龄；超过即视为数据红灯。<=0 表示不检查年龄。
	MaxDataAgeMs int64
	// MinObservationConfidence 允许的最低观测信心；低于即空仓。
	MinObservationConfidence float64
}

// DefaultOODConfig 返回一组保守默认阈值。
func DefaultOODConfig() OODConfig {
	return OODConfig{
		MaxDataAgeMs:             5_000,
		MinObservationConfidence: 0.10,
	}
}

// knownRegime 明确列出当前机制专家认识的制度。任何未来新增但尚未接入路由器的值都按 OOD 处理，
// 而不是因为字符串非空就被误当成已知。
func knownRegime(kind mechanism.RegimeKind) bool {
	switch kind {
	case mechanism.RegimePanicLiquidation,
		mechanism.RegimeFundingSettlement,
		mechanism.RegimeTieredLiquidation,
		mechanism.RegimeRuleEvent:
		return true
	default:
		return false
	}
}

// AssessOOD 做开放世界判定：未知制度、显式 OOD 标记、数据缺口、数据过期或观测信心过低，
// 任一命中都会强制空仓。这是「未知 / OOD、数据红灯时明确空仓」纪律的执行点。
func AssessOOD(card mechanism.MechanismEventCard, cfg OODConfig) OODVerdict {
	v := OODVerdict{}
	add := func(ood bool, reason string) {
		v.Reasons = append(v.Reasons, reason)
		v.ForceFlat = true
		if ood {
			v.OutOfDistribution = true
		}
	}

	if card.Quality.OutOfDistribution {
		add(true, "quality_out_of_distribution")
	}
	if !knownRegime(card.Regime.Kind) {
		add(true, "unknown_regime")
	}
	if card.Quality.HasGap {
		add(false, "data_gap")
	}
	if cfg.MaxDataAgeMs > 0 && card.Quality.DataAgeMs > cfg.MaxDataAgeMs {
		add(false, "stale_data")
	}
	if card.Quality.DataAgeMs < 0 {
		add(false, "invalid_data_age")
	}
	confidence := card.Quality.ObservationConfidence
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) ||
		confidence < clamp01(cfg.MinObservationConfidence) {
		add(false, "low_observation_confidence")
	}
	return v
}

// WeakestLinkConfidence 按「最弱环节」聚合天道信心：整体信心取所有环节的最小值。
// 任一环节非有限或 <=0 直接返回 0（一条链断了整体就断）。空输入返回 0。
func WeakestLinkConfidence(links ...float64) float64 {
	if len(links) == 0 {
		return 0
	}
	weakest := 1.0
	for _, l := range links {
		if math.IsNaN(l) || math.IsInf(l, 0) || l <= 0 {
			return 0
		}
		if l < weakest {
			weakest = l
		}
	}
	return clamp01(weakest)
}

// HardCaps 汇集 N_hard 的全部来源（均为 USDT 名义上限）。任何一个都是不可突破的天花板，
// N_hard 取所有来源的最小值。零或负值表示该硬约束不允许新增风险，而不是“未设置”；
// 上游缺失任一硬约束时必须显式保持零，开放世界下禁止绕过未知天花板。
type HardCaps struct {
	MaintenanceMargin money.Micros // 逐所维持保证金反推的上限
	LiquidationRoot   money.Micros // 真实强平价求根得到的上限
	ExtremeScenario   money.Micros // 极端场景压力上限
	Liquidity         money.Micros // 流动性上限
	Beta              money.Micros // beta 调整后的上限
	MechanismCluster  money.Micros // 机制簇上限
	OrderGrid         money.Micros // 订单网格上限
	AccountTotalRisk  money.Micros // 账户总风险上限
}

// NHard 返回所有来源的最小值（硬上限求交）。任一来源 <=0 都使结果为 0。
func (h HardCaps) NHard() money.Micros {
	caps := []money.Micros{
		h.MaintenanceMargin, h.LiquidationRoot, h.ExtremeScenario, h.Liquidity,
		h.Beta, h.MechanismCluster, h.OrderGrid, h.AccountTotalRisk,
	}
	min := caps[0]
	for _, c := range caps {
		if c <= 0 {
			return 0
		}
		if c < min {
			min = c
		}
	}
	return min
}

// TargetInputs 是 N_target 的缩放因子，每个都应在 [0,1]，越界会被夹紧。
// 这些因子只能缩小 N_hard，绝不放大。
type TargetInputs struct {
	Confidence           float64 // 天道信心（建议来自 WeakestLinkConfidence）
	DataQuality          float64 // 数据质量
	StateRecognizability float64 // 状态可辨识度
	Liquidity            float64 // 流动性折扣
	CorrelationDiscount  float64 // 相关性折扣
}

// Factor 返回所有缩放因子夹紧后的乘积（∈[0,1]）。
func (in TargetInputs) Factor() float64 {
	return clamp01(in.Confidence) *
		clamp01(in.DataQuality) *
		clamp01(in.StateRecognizability) *
		clamp01(in.Liquidity) *
		clamp01(in.CorrelationDiscount)
}

// NTarget 计算目标名义：N_target = N_hard × 各缩放因子。结果恒被夹到 [0, N_hard]，
// 保证信心 / 质量等只能缩放仓位，绝不能突破硬上限。
func NTarget(nHard money.Micros, in TargetInputs) money.Micros {
	if nHard <= 0 {
		return 0
	}
	target := nHard.MulRate(in.Factor())
	if target < 0 {
		return 0
	}
	if target > nHard {
		return nHard // 信心只能缩放，不能突破硬上限
	}
	return target
}

// LotPlan 整数 lot 分配结果。
type LotPlan struct {
	// Lots 分配到的整数 lot 数（>=0）。
	Lots int64
	// Notional 实际名义（Lots × lotNotional），恒 <= 目标名义。
	Notional money.Micros
	// Skipped 是否跳过（理论仓位不足最小单位 / 最小名义）。
	Skipped bool
	// SkipReason 跳过原因。
	SkipReason string
}

// 跳过原因常量。
const (
	// SkipNonPositiveTarget 目标名义或 lot 名义非正。
	SkipNonPositiveTarget = "non_positive_target"
	// SkipBelowMinLot 目标名义不足一个 lot。
	SkipBelowMinLot = "below_min_lot"
	// SkipBelowMinNotional 取整后名义低于交易所最小下单名义。
	SkipBelowMinNotional = "below_min_notional"
)

// AllocateLots 把目标名义换算成整数 lot：只向下取整（floor），绝不向上取整。
// 理论仓位不足一个 lot，或取整后名义低于交易所最小下单名义时，直接跳过（Skipped），
// 而不是为了成交向上取整突破风险上限。
func AllocateLots(targetNotional, lotNotional, minNotional money.Micros) LotPlan {
	if targetNotional <= 0 || lotNotional <= 0 {
		return LotPlan{Skipped: true, SkipReason: SkipNonPositiveTarget}
	}
	lots := int64(targetNotional) / int64(lotNotional) // 向下取整
	if lots <= 0 {
		return LotPlan{Skipped: true, SkipReason: SkipBelowMinLot}
	}
	notional := money.Micros(lots * int64(lotNotional))
	if minNotional > 0 && notional < minNotional {
		return LotPlan{Skipped: true, SkipReason: SkipBelowMinNotional}
	}
	return LotPlan{Lots: lots, Notional: notional}
}
