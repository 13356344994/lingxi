package portfolio

import (
	"sort"

	"lingxi/internal/money"
)

// ============================================================================
// 止错状态机（StopError）：只看论点死亡。
//
// 它只读运行时论点健康（证伪器是否触发、期限是否已过、被迫付款方是否消失），
// 完全不读账户权益 / 保证金 —— 与偿付状态机在输入上就彻底解耦。策略「不认固定价格止损，
// 只认论点证伪」的纪律由此落地。
// ============================================================================

// StopErrorState 止错状态。
type StopErrorState string

const (
	// StopErrorHolding 论点仍存活，继续持有。
	StopErrorHolding StopErrorState = "holding"
	// StopErrorExit 论点已死，应离场。
	StopErrorExit StopErrorState = "exit"
)

// ThesisHealth 运行时论点健康观测。字段刻意不含任何账户 / 权益信息。
type ThesisHealth struct {
	NowUnixMs      int64
	DeadlineUnixMs int64
	// InvalidatorFired 是否有运行时可观测证伪器触发。
	InvalidatorFired bool
	// PayerGone 被迫付款方是否已消失（被迫流枯竭）。
	PayerGone bool
}

// StopErrorDecision 止错裁决。
type StopErrorDecision struct {
	State  StopErrorState
	Exit   bool
	Reason string
}

// 止错离场原因常量。
const (
	// StopErrorReasonInvalidator 证伪器触发。
	StopErrorReasonInvalidator = "invalidator_fired"
	// StopErrorReasonDeadline 期限已过。
	StopErrorReasonDeadline = "deadline_passed"
	// StopErrorReasonPayerGone 被迫付款方消失。
	StopErrorReasonPayerGone = "payer_gone"
)

// EvaluateStopError 只依据论点死亡三条件裁决离场：证伪器触发、期限已过、payer 消失。
// 一旦进入 Exit 便不再回到 Holding（论点死亡不可逆）。
func EvaluateStopError(prev StopErrorState, h ThesisHealth) StopErrorDecision {
	if prev == StopErrorExit {
		return StopErrorDecision{State: StopErrorExit, Exit: true, Reason: "already_exited"}
	}
	switch {
	case h.InvalidatorFired:
		return StopErrorDecision{State: StopErrorExit, Exit: true, Reason: StopErrorReasonInvalidator}
	case h.DeadlineUnixMs > 0 && h.NowUnixMs >= h.DeadlineUnixMs:
		return StopErrorDecision{State: StopErrorExit, Exit: true, Reason: StopErrorReasonDeadline}
	case h.PayerGone:
		return StopErrorDecision{State: StopErrorExit, Exit: true, Reason: StopErrorReasonPayerGone}
	default:
		return StopErrorDecision{State: StopErrorHolding, Exit: false}
	}
}

// ============================================================================
// 偿付状态机（Solvency）：独立防线。
//
// 它只读账户偿付权益与最差场景所需缓冲，完全不读论点是否存活 —— 与止错状态机在输入上彻底解耦。
// 当缓冲被击穿时，按「边际风险最大优先」给出削减顺序。任何专家 / 论点都不能关闭这条防线：
// 本包不提供任何禁用偿付判定的开关，EvaluateSolvency 也不接受来自专家的否决输入。
// ============================================================================

// PositionRisk 描述一个在场仓位在最差场景下的边际风险与可削减名义。
type PositionRisk struct {
	ID      string
	Cluster string
	// MarginalStressLoss 该仓位在最差场景下贡献的边际损失（USDT，正数）。
	MarginalStressLoss money.Micros
	// ReducibleNotional 该仓位可被削减的名义（USDT）。
	ReducibleNotional money.Micros
}

// SolvencyInputs 偿付判定输入。刻意不含任何论点 / 专家字段。
type SolvencyInputs struct {
	// SolvencyEquity 当前偿付权益。
	SolvencyEquity money.Micros
	// RequiredBuffer 最差场景下所需的偿付缓冲。
	RequiredBuffer money.Micros
	// Positions 在场仓位的最差场景风险画像。
	Positions []PositionRisk
}

// SolvencyDecision 偿付裁决。
type SolvencyDecision struct {
	// Breached 缓冲是否被击穿（SolvencyEquity < RequiredBuffer）。
	Breached bool
	// Shortfall 缺口额（RequiredBuffer - SolvencyEquity，未击穿时为 0）。
	Shortfall money.Micros
	// ReduceOrder 建议削减的仓位 ID 顺序（边际风险最大优先）。
	ReduceOrder []string
	// ReduceNotional 为覆盖缺口建议削减的名义合计（按顺序累计到覆盖缺口为止）。
	ReduceNotional money.Micros
	// RecoveredBuffer 按最差场景压力损失估计可恢复的偿付缓冲。
	RecoveredBuffer money.Micros
}

// EvaluateSolvency 只依据「偿付权益是否低于最差场景所需缓冲」裁决减仓：
// 击穿时把在场仓位按边际压力损失从大到小排序，依次纳入削减直到累计可恢复压力缓冲覆盖缺口。
// 名义与权益缺口单位不可直接等同，因此停止条件使用 MarginalStressLoss，而非 ReducibleNotional。
//
// 它不看论点是否存活，也不接受任何专家否决 —— 偿付防线不可被关闭。
func EvaluateSolvency(in SolvencyInputs) SolvencyDecision {
	if in.SolvencyEquity >= in.RequiredBuffer {
		return SolvencyDecision{Breached: false}
	}
	shortfall := in.RequiredBuffer - in.SolvencyEquity

	ranked := make([]PositionRisk, len(in.Positions))
	copy(ranked, in.Positions)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].MarginalStressLoss != ranked[j].MarginalStressLoss {
			return ranked[i].MarginalStressLoss > ranked[j].MarginalStressLoss
		}
		return ranked[i].ID < ranked[j].ID
	})

	dec := SolvencyDecision{Breached: true, Shortfall: shortfall}
	for _, p := range ranked {
		if p.ReducibleNotional <= 0 {
			continue
		}
		dec.ReduceOrder = append(dec.ReduceOrder, p.ID)
		dec.ReduceNotional += p.ReducibleNotional
		if p.MarginalStressLoss > 0 {
			dec.RecoveredBuffer += p.MarginalStressLoss
		}
		if dec.RecoveredBuffer >= shortfall {
			break
		}
	}
	return dec
}
