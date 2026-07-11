package mechanism

import (
	"strings"

	"lingxi/internal/money"
)

// Invalidator 描述一个运行时可观测的证伪条件。只有 Observable=true 的证伪器才算有效：
// 无法在运行时观测的「证伪」等于没有证伪器，据此产生的论点会被拒。
type Invalidator struct {
	// Metric 可观测指标名（如 remaining_forced_notional / absorption_ratio / basis_bps）。
	Metric string
	// Condition 触发失效的条件描述（审计用自由文本）。
	Condition string
	// Observable 该证伪条件是否运行时可观测。
	Observable bool
}

// Thesis 是专家出具的方向论点，统一携带计划第 6 节要求的全部字段。
// 论点只认证伪，不认固定价格止损：Invalidators 才是它的死亡条件。
type Thesis struct {
	// Expert 出具该论点的专家名。
	Expert string
	// CardID 关联的事件卡 ID。
	CardID string

	// Payer 被迫付款 / 被迫行动方（必填，PayerUnknown 会被拒）。
	Payer PayerRole
	// ForcedAction 被迫方必须执行的动作描述（必填审计文本）。
	ForcedAction string
	// Direction 交易方向（必须为 Long / Short）。
	Direction Direction
	// DeadlineUnixMs 论点期限（必填，>0；被迫流有时限，无限期的「终会反转」不算论点）。
	DeadlineUnixMs int64
	// Evidence 证据链。
	Evidence []string
	// Invalidators 运行时可观测证伪条件（至少一个 Observable）。
	Invalidators []Invalidator
	// Extremeness 极端度（0~1，越靠尾部越极端）。
	Extremeness float64
	// HeavenConfidence 天道信心（0~1），专家给出的基础信心；路由器还会按最弱环节再缩放。
	HeavenConfidence float64
	// Capacity 机制容量：该机制可承接的名义上限（USDT）。
	Capacity money.Micros
	// Cluster 相关簇 ID。
	Cluster string
	// ExitThesis 退出论点：论点如何了结（获利兑现 / 证伪离场）。
	ExitThesis string
}

// 拒绝理由常量。任何专家在 accept 前都会跑 Validate，保证不满足硬前提的论点无法被接单。
const (
	// RejectNoPayer 找不到被迫付款方。
	RejectNoPayer = "no_payer"
	// RejectNoDeadline 论点没有运行时期限。
	RejectNoDeadline = "no_deadline"
	// RejectNoInvalidator 没有任何运行时可观测证伪器（仅凭「跌多会涨」即属此类）。
	RejectNoInvalidator = "no_observable_invalidator"
	// RejectNoDirection 方向不是可交易的多 / 空。
	RejectNoDirection = "no_direction"
	// RejectRegimeMismatch 事件制度与专家不匹配。
	RejectRegimeMismatch = "regime_mismatch"
	// RejectOutOfDistribution 观测落在已知分布之外（开放世界空仓）。
	RejectOutOfDistribution = "out_of_distribution"
	// RejectNoCapacity 机制容量不足以承接任何名义。
	RejectNoCapacity = "no_capacity"
	// RejectNotExecutable 盘口 / 最小单位使该论点不可执行。
	RejectNotExecutable = "not_executable"
	// RejectMissingEvidence 机制判定所需的运行时观测缺失或非法。
	RejectMissingEvidence = "missing_or_invalid_evidence"
)

// HasObservableInvalidator 报告论点是否至少含一个运行时可观测证伪器。
func (t Thesis) HasObservableInvalidator() bool {
	for _, inv := range t.Invalidators {
		if inv.Observable && strings.TrimSpace(inv.Metric) != "" && strings.TrimSpace(inv.Condition) != "" {
			return true
		}
	}
	return false
}

// Validate 强制机制专家的三条硬前提：必须有 payer、必须有期限、必须有运行时可观测证伪器；
// 方向也必须是可交易的多 / 空。任一缺失即返回对应拒绝理由。
//
// 这是「无 payer / 无期限 / 无运行时可观测 invalidator、仅凭『跌多会涨』的候选直接淘汰」
// 这条计划纪律的唯一执行点。
func (t Thesis) Validate() (ok bool, reason string) {
	if t.Payer == PayerUnknown {
		return false, RejectNoPayer
	}
	if t.DeadlineUnixMs <= 0 {
		return false, RejectNoDeadline
	}
	if !t.HasObservableInvalidator() {
		return false, RejectNoInvalidator
	}
	if !t.Direction.IsDirectional() {
		return false, RejectNoDirection
	}
	return true, ""
}
