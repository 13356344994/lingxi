// Package execution 承载订单真相底座的纯逻辑内核:Intent/Order/Fill/Position 状态机、
// per-symbol 原子 claim、幂等成交 reducer、append-only WAL 与启动收敛(recovery)。
//
// 设计边界(见 /root/.cursor/plans/首战完全体_*.plan.md 第 3 节「订单真相与智能执行底座」):
//   - 本包不依赖任何具体交易所类型(不 import internal/venue),只依赖标准库 + internal/money,
//     保证可被任意 venue 适配器与回测复用、可独立单测。
//   - 本包不发任何网络请求;真实网络私有流由各 venue 适配器实现,产出翻译后的中性事件喂给本包。
//   - UNKNOWN/本地-交易所不一致必须落 quarantine,quarantine 期间不释放已有 claim、不允许同 symbol
//     新开 claim;是否清除 quarantine 由收敛/对账流程显式判定,本包不自动过期清除。
package execution

import "fmt"

// IntentState 是一次交易决策(intent)的生命周期状态。
//
//	pending      → 已创建,尚未拿到 per-symbol claim
//	claimed      → 已拿到 claim,尚未提交订单
//	submitted    → 订单已提交给交易所,等待确认
//	working      → 交易所已确认(new/live)或部分成交,敞口可能非零
//	filled       → 终态:完全成交
//	canceled     → 终态:撤单收敛(可能带部分成交)
//	rejected     → 终态:交易所拒单,未产生任何成交
//	quarantined  → 因 UNKNOWN/本地-交易所不一致被隔离,阻断同 symbol 新 intent
type IntentState string

const (
	IntentStatePending     IntentState = "pending"
	IntentStateClaimed     IntentState = "claimed"
	IntentStateSubmitted   IntentState = "submitted"
	IntentStateWorking     IntentState = "working"
	IntentStateFilled      IntentState = "filled"
	IntentStateCanceled    IntentState = "canceled"
	IntentStateRejected    IntentState = "rejected"
	IntentStateQuarantined IntentState = "quarantined"
)

// IntentTerminal 判断 intent 是否已到达正常终态(不含 quarantined:quarantined 是「悬而未决」
// 而非「已收敛」,必须经显式对账清除才能再离开)。
func (s IntentState) IntentTerminal() bool {
	switch s {
	case IntentStateFilled, IntentStateCanceled, IntentStateRejected:
		return true
	default:
		return false
	}
}

// intentTransitions 是合法迁移表:key 为起点,value 为该起点允许到达的终点集合。
// quarantined 可从任何非终态(以及 working,用于处理迟到的不一致)进入,且本身没有出边——
// 必须由外部显式调用「清除隔离」重建全新 intent,而不是在状态机内部继续演化。
var intentTransitions = map[IntentState]map[IntentState]bool{
	IntentStatePending: {
		IntentStateClaimed:     true,
		IntentStateRejected:    true, // 建 claim 前置校验失败(如已隔离)可直接判定为拒绝
		IntentStateQuarantined: true,
	},
	IntentStateClaimed: {
		IntentStateSubmitted:   true,
		IntentStateCanceled:    true, // 拿到 claim 后决策撤销(未提交订单)
		IntentStateQuarantined: true,
	},
	IntentStateSubmitted: {
		IntentStateWorking:     true,
		IntentStateFilled:      true, // 交易所可能直接以 filled 确认(taker/IOC 一次成交)
		IntentStateCanceled:    true,
		IntentStateRejected:    true,
		IntentStateQuarantined: true,
	},
	IntentStateWorking: {
		IntentStateWorking:     true, // 追加部分成交,状态不变但允许「自环」记录一次迁移校验
		IntentStateFilled:      true,
		IntentStateCanceled:    true,
		IntentStateQuarantined: true,
	},
	// 终态默认无出边;quarantined 也无出边(见上)。
	IntentStateFilled:      {},
	IntentStateCanceled:    {},
	IntentStateRejected:    {},
	IntentStateQuarantined: {},
}

// ErrIllegalIntentTransition 在状态机拒绝一次非法迁移时返回。
type ErrIllegalIntentTransition struct {
	From IntentState
	To   IntentState
}

func (e *ErrIllegalIntentTransition) Error() string {
	return fmt.Sprintf("execution: intent 非法迁移 %s -> %s", e.From, e.To)
}

// ValidateIntentTransition 校验 from -> to 是否为合法迁移;非法返回 *ErrIllegalIntentTransition。
func ValidateIntentTransition(from, to IntentState) error {
	if from == to && from == IntentStateWorking {
		return nil // working 的部分成交自环允许
	}
	allowed, ok := intentTransitions[from]
	if !ok || !allowed[to] {
		return &ErrIllegalIntentTransition{From: from, To: to}
	}
	return nil
}

// OrderState 是单笔订单在交易所侧的确认状态,与 IntentState 分层:一个 intent 可能对应
// 多笔订单(追价/重试),每笔订单各自有自己的 OrderState。
//
//	new              → 已提交,尚未收到交易所确认(ack)
//	acked            → 交易所已确认存在(new/live),尚无成交
//	partially_filled → 部分成交,订单仍存活
//	filled           → 完全成交(终态)
//	canceled         → 已撤销(终态,executedQty 可能 >0)
//	rejected         → 交易所拒单(终态,executedQty 必为 0)
//	unknown          → 状态未知/失联(如查单超时、连接中断且无法确认),必须触发 quarantine
type OrderState string

const (
	OrderStateNew             OrderState = "new"
	OrderStateAcked           OrderState = "acked"
	OrderStatePartiallyFilled OrderState = "partially_filled"
	OrderStateFilled          OrderState = "filled"
	OrderStateCanceled        OrderState = "canceled"
	OrderStateRejected        OrderState = "rejected"
	OrderStateUnknown         OrderState = "unknown"
)

// OrderTerminal 报告该状态是否为「交易所侧已收敛」的终态(unknown 不算,它是悬而未决)。
func (s OrderState) OrderTerminal() bool {
	switch s {
	case OrderStateFilled, OrderStateCanceled, OrderStateRejected:
		return true
	default:
		return false
	}
}

// orderTransitions 是订单状态合法迁移表。任何状态都可以迁移到 unknown(失联无需前置条件),
// 但 unknown 本身只能通过「显式对账」(ResolveUnknownOrderTransition)离开,不在此表内自由流转,
// 避免把「猜测」当成状态机的默认出口。
var orderTransitions = map[OrderState]map[OrderState]bool{
	OrderStateNew: {
		OrderStateAcked:           true,
		OrderStateRejected:        true,
		OrderStateCanceled:        true, // IOC 可在首个交易所回执中直接取消
		OrderStatePartiallyFilled: true, // 极快市价单可能 ack 与首笔成交几乎同时到达
		OrderStateFilled:          true,
	},
	OrderStateAcked: {
		OrderStatePartiallyFilled: true,
		OrderStateFilled:          true,
		OrderStateCanceled:        true,
	},
	OrderStatePartiallyFilled: {
		OrderStatePartiallyFilled: true,
		OrderStateFilled:          true,
		OrderStateCanceled:        true,
	},
	OrderStateFilled:   {},
	OrderStateCanceled: {},
	OrderStateRejected: {},
	OrderStateUnknown:  {},
}

// ErrIllegalOrderTransition 在订单状态机拒绝一次非法迁移时返回。
type ErrIllegalOrderTransition struct {
	From OrderState
	To   OrderState
}

func (e *ErrIllegalOrderTransition) Error() string {
	return fmt.Sprintf("execution: order 非法迁移 %s -> %s", e.From, e.To)
}

// ValidateOrderTransition 校验 from -> to 是否合法。任何状态 -> unknown 总是合法(失联无前置条件)。
// unknown -> 任意状态只允许通过 ResolveUnknownOrderTransition 完成,普通迁移校验对此返回非法,
// 强制调用方走「显式对账已收敛」路径而不是静默恢复。
func ValidateOrderTransition(from, to OrderState) error {
	if to == OrderStateUnknown {
		return nil
	}
	if from == to && (from == OrderStatePartiallyFilled) {
		return nil // 追加部分成交自环
	}
	allowed, ok := orderTransitions[from]
	if !ok || !allowed[to] {
		return &ErrIllegalOrderTransition{From: from, To: to}
	}
	return nil
}

// ResolveUnknownOrderTransition 是 unknown 状态唯一的合法出口:必须显式声明「已通过对账收敛」,
// 目标状态必须是终态(filled/canceled/rejected)——不允许把 unknown 静默收敛回 new/acked 之类的
// 「继续悬置」状态,悬置态只能停留在 unknown 直到彻底查清。
func ResolveUnknownOrderTransition(to OrderState) error {
	if !to.OrderTerminal() {
		return fmt.Errorf("execution: unknown 只能收敛到终态, got %s", to)
	}
	return nil
}

// PositionSide 是持仓方向,由 Position.Qty 的符号派生,仅用于展示/归因,不是独立状态源。
type PositionSide string

const (
	PositionFlat  PositionSide = "flat"
	PositionLong  PositionSide = "long"
	PositionShort PositionSide = "short"
)

// positionQtyTolerance 是把浮点持仓数量判定为「零」的容差,吸收币种精度带来的浮点噪声。
const positionQtyTolerance = 1e-9

func sideOfQty(qty float64) PositionSide {
	switch {
	case qty > positionQtyTolerance:
		return PositionLong
	case qty < -positionQtyTolerance:
		return PositionShort
	default:
		return PositionFlat
	}
}
