// Package lifecycle 是对冲单生命周期的「单一事实来源」(SSOT)：它定义了一笔对冲在其一生中可处的
// 全部状态、状态间合法的迁移表，以及「某状态下允许下哪种单」的前置门判定（Gate 3：消灭双轨状态）。
//
// 之前对冲状态散落在内存 OpenHedge.LifecycleState 的自由赋值、落盘账本、交易所持仓快照三处，彼此可能
// 互相矛盾。本包把「状态词表 + 合法迁移 + 下单意图门禁」收敛成纯逻辑，让 app 侧的 OpenHedge 迁移与
// 下单前置校验都委托到这里，从结构上保证只有一套权威规则。纯逻辑、无状态、可独立单测。
package lifecycle

import "fmt"

// State 是对冲单的生命周期状态。空串 StateNone 表示「尚无对冲」（开仓前的空闲态）。
type State string

const (
	StateNone                State = ""
	StatePendingValidation   State = "pending_validation"
	StateActive              State = "active"
	StateClosing             State = "closing"
	StateClosed              State = "closed"
	StateClosedWithResidual  State = "closed_with_residual"
	StateCloseRecovered      State = "close_recovered"
	StateCloseRecoveredPart  State = "close_recovered_partial"
	StateEscalated           State = "escalated"
	StateAborted             State = "aborted"
	StateAbortedWithResidual State = "aborted_with_residual"
)

// Intent 是一次下单的业务意图，下单前置门据此判定是否与当前生命周期状态自洽。
type Intent string

const (
	// IntentOpenLeg：开仓建腿（买现货 / 开合约）。
	IntentOpenLeg Intent = "open_leg"
	// IntentRevertLeg：撤销/反向平掉刚建立的某条开仓腿（开仓阶段单腿失败的回滚）。
	IntentRevertLeg Intent = "revert_leg"
	// IntentCloseLeg：正常平仓建腿（卖现货 / 平合约）。
	IntentCloseLeg Intent = "close_leg"
	// IntentRescueReduce：敞口救援的只减仓下单（拉平残腿）。
	IntentRescueReduce Intent = "rescue_reduce"
)

// allowedTransitions 是合法迁移表。键为来源状态，值为允许到达的目标状态集合。
// 同状态自迁移（幂等重置）一律允许，不在表中单列。终态默认不可再迁出（除非走显式救援，由 app 侧
// 以 IntentRescueReduce 门禁单独控制减仓下单）。
var allowedTransitions = map[State]map[State]struct{}{
	StateNone: setOf(StatePendingValidation),
	StatePendingValidation: setOf(
		StateActive, StateClosing, StateEscalated,
		StateCloseRecovered, StateCloseRecoveredPart,
		StateAborted, StateAbortedWithResidual,
	),
	StateActive: setOf(StateClosing, StateEscalated),
	StateClosing: setOf(
		StateClosed, StateClosedWithResidual,
		StateCloseRecovered, StateCloseRecoveredPart,
		StateEscalated, StateAborted, StateAbortedWithResidual,
	),
	StateEscalated: setOf(
		StateClosed, StateClosedWithResidual,
		StateCloseRecovered, StateCloseRecoveredPart,
		StateAborted, StateAbortedWithResidual,
	),
	StateCloseRecovered: setOf(
		StateClosed, StateClosedWithResidual,
		StateEscalated, StateAbortedWithResidual,
	),
	StateCloseRecoveredPart: setOf(
		StateClosed, StateClosedWithResidual, StateCloseRecovered,
		StateEscalated, StateAbortedWithResidual,
	),
	// 终态：无后继。
	StateClosed:              {},
	StateClosedWithResidual:  {},
	StateAborted:             {},
	StateAbortedWithResidual: {},
}

// intentByState 描述每个状态下允许的下单意图集合。未列出的状态默认禁止一切下单。
var intentByState = map[State]map[Intent]struct{}{
	// 空闲/未知态（含重启后从落盘账本重建、尚无 lifecycle 标注的持仓）：保持宽松，允许建仓，也允许
	// 平仓与救援减仓——绝不能因为「生命周期未知」而挡住对一笔真实存在持仓的平仓/拉平。
	StateNone: intentSet(IntentOpenLeg, IntentCloseLeg, IntentRescueReduce),
	// 开仓待校验：可继续建腿、回滚单腿、或在异常时直接转入平仓/救援。
	StatePendingValidation: intentSet(IntentOpenLeg, IntentRevertLeg, IntentCloseLeg, IntentRescueReduce),
	// 持仓中：只允许平仓或救援减仓，禁止再加开仓腿。
	StateActive: intentSet(IntentCloseLeg, IntentRescueReduce),
	// 平仓中 / 升级处理 / 部分恢复：允许继续平仓与救援减仓。
	StateClosing:            intentSet(IntentCloseLeg, IntentRescueReduce),
	StateEscalated:          intentSet(IntentCloseLeg, IntentRescueReduce),
	StateCloseRecovered:     intentSet(IntentRescueReduce),
	StateCloseRecoveredPart: intentSet(IntentCloseLeg, IntentRescueReduce),
	// 带残腿的终态：仍允许救援减仓去拉平残腿；干净终态禁止一切下单。
	StateClosedWithResidual:  intentSet(IntentRescueReduce),
	StateAbortedWithResidual: intentSet(IntentRescueReduce),
	StateClosed:              {},
	StateAborted:             {},
}

// IsTerminal 报告状态是否为「干净终态」（无残腿、不应再有任何下单）。
func IsTerminal(state State) bool {
	return state == StateClosed || state == StateAborted
}

// CanTransition 报告从 from 到 to 的迁移是否合法（含同状态幂等）。
func CanTransition(from, to State) bool {
	if from == to {
		return true
	}
	targets, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, allowed := targets[to]
	return allowed
}

// ValidateTransition 在迁移非法时返回带上下文的错误，否则返回 nil。
func ValidateTransition(from, to State) error {
	if to == "" {
		return fmt.Errorf("lifecycle: 不允许迁移到空状态")
	}
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("lifecycle: 非法状态迁移 %q -> %q", from, to)
}

// IntentAllowed 报告在 state 下是否允许 intent 对应的下单。
func IntentAllowed(state State, intent Intent) bool {
	intents, ok := intentByState[state]
	if !ok {
		return false
	}
	_, allowed := intents[intent]
	return allowed
}

// GuardIntent 是下单前置门：意图与当前状态不自洽时返回错误，调用方据此拒绝下单。
func GuardIntent(state State, intent Intent) error {
	if IntentAllowed(state, intent) {
		return nil
	}
	return fmt.Errorf("lifecycle: 状态 %q 下不允许下单意图 %q", stateLabel(state), intent)
}

func stateLabel(state State) string {
	if state == StateNone {
		return "none"
	}
	return string(state)
}

func setOf(states ...State) map[State]struct{} {
	out := make(map[State]struct{}, len(states))
	for _, s := range states {
		out[s] = struct{}{}
	}
	return out
}

func intentSet(intents ...Intent) map[Intent]struct{} {
	out := make(map[Intent]struct{}, len(intents))
	for _, i := range intents {
		out[i] = struct{}{}
	}
	return out
}
