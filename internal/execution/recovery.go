package execution

// recovery.go 实现启动收敛的纯逻辑:「WS 缓冲 → REST 未完成订单/真仓/近期 fills →
// 应用缓冲事件 → 收敛后才允许决策」(plan §3)。本文件不发任何网络请求——REST 快照与 WS
// 缓冲事件都由调用方(venue 适配器 + 启动流程)采集好后一次性传入,本文件只负责「给定输入,
// 确定性地算出收敛后状态与需要 quarantine 的 symbol」,因此可以完全离线单测,不需要真实网络凭证。
//
// 关键设计决策:收敛「继续」在 WAL 重放得到的同一个 TradeReducer 实例上应用新事件,而不是
// 用 REST 仓位重新起一个空 reducer 后再叠加成交——REST /positions 本身已经是「历史全部成交
// 累积后」的真相,若再把 recent fills 当作增量叠加到它上面会双计。正确顺序是:
//  1. 以 WAL 重放得到的 reducer(崩溃前最后已知状态,含 seenTrades 去重表)为基底,
//     LocalReducer 为 nil 时等价于全新空 reducer(首次启动)。
//  2. 在同一个 reducer 上按接收顺序应用缓冲 WS 事件与 REST 近期成交——WAL 里已经记过的
//     成交因 trade_id 已在 seenTrades 中而被自动去重,只有「崩溃/掉线期间真正发生但本地
//     从未记录过」的成交会被计入。
//  3. 拿收敛后的 reducer 仓位与 REST 仓位真相逐 symbol 比对:仍然不一致,说明存在既不在
//     WAL、也不在缓冲 WS、也不在 REST 近期成交窗口内的「裸差」,必须 quarantine 而不能猜测。
//
// 调用方责任(未在本文件内强制,但必须遵守,否则收敛结论不可信):
//  1. 在发出 REST 快照请求「之前」开始缓冲私有 WS 事件,快照返回后才停止缓冲,
//     保证「REST 快照与 WS 事件之间不存在缝隙」。
//  2. 缓冲事件按接收顺序(FIFO)整体传入 BufferedEvents。
//  3. 在 Recover 返回且 Quarantined 为空之前,不允许任何新开仓决策使用其 Reducer。
import (
	"math"
	"sort"
)

// OrderSnapshot 是 REST 快照里一笔未完成订单的中性视图。
type OrderSnapshot struct {
	Symbol          string
	ClientOrderID   string
	ExchangeOrderID string
	State           OrderState
	ExecutedQty     float64
	AvgPrice        float64
}

// ExchangeSnapshot 汇总启动时 REST 拉取到的交易所真相:未完成订单 + 逐 symbol 真仓 + 近期成交。
// Positions 以 symbol 为 key,值为有符号持仓数量——这是「历史全部成交累积后」的权威真相,
// 只用于与本地重放结果比对,不作为 reducer 的再叠加基底(见文件头注释)。
type ExchangeSnapshot struct {
	OpenOrders  []OrderSnapshot
	Positions   map[string]float64
	RecentFills []Fill
}

// BufferedEvent 是启动收敛期间缓冲到的一条私有事件(订单状态变更或成交),携带接收顺序。
type BufferedEvent struct {
	OrderUpdate *OrderSnapshot
	Fill        *Fill
}

// RecoveryInput 汇总一次启动收敛所需的全部输入。
type RecoveryInput struct {
	// LocalReducer 是由 WAL 重放得到的、崩溃前最后已知状态的 reducer(含 seenTrades 去重表);
	// 首次启动(无 WAL 历史)传 nil,等价于全新空 reducer。收敛过程直接在这个实例上继续应用
	// 新事件,保证 WAL 里已记录过的成交不会被重复计入。
	LocalReducer *TradeReducer
	// BufferedEvents 是快照请求发出前开始缓冲、到快照返回为止收集到的 WS 事件,按接收顺序。
	BufferedEvents []BufferedEvent
	Snapshot       ExchangeSnapshot
}

// Discrepancy 描述一处本地-交易所不一致。LocalQty 是收敛前(WAL 重放得到)的基线仓位,
// ReconciledQty 是应用完全部缓冲事件与近期成交之后的仓位,ExchangeQty 是 REST 真相;
// ReconciledQty 与 ExchangeQty 仍不一致才是真正需要 quarantine 的「裸差」。
type Discrepancy struct {
	Symbol        string
	Reason        string
	LocalQty      float64
	ReconciledQty float64
	ExchangeQty   float64
}

const (
	DiscrepancyLocalExchangeQtyMismatch = "local_vs_exchange_qty_mismatch"
	DiscrepancyExchangeOnlyPosition     = "exchange_position_without_local_record"
	DiscrepancyUnknownOpenOrder         = "unknown_open_order"
)

// RecoveryResult 是一次收敛的完整结果。Reducer 是收敛后的权威 reducer,可直接作为运行态种子;
// Quarantined 非空时,调用方必须先对相应 symbol 调用 ClaimManager.Quarantine 才能继续启动。
type RecoveryResult struct {
	Reducer       *TradeReducer
	Discrepancies []Discrepancy
	Quarantined   []string
	Issues        []RecoveryIssue
}

// RecoveryIssue 是恢复输入中无法合法应用的事件；该 symbol 会被隔离，错误不会被静默丢弃。
type RecoveryIssue struct {
	Symbol string
	Kind   string
	ID     string
	Error  string
}

// qtyMatchTolerance 是本地-交易所持仓数量判定「一致」的容差(吸收合约张数换算的浮点噪声)。
const qtyMatchTolerance = 1e-6

// Recover 按固定顺序做纯逻辑收敛,不做任何 I/O(细节见文件头注释)。
func Recover(input RecoveryInput) RecoveryResult {
	reducer := input.LocalReducer
	if reducer == nil {
		reducer = NewTradeReducer()
	}

	localBaseline := reducer.Snapshot() // 收敛前(WAL 重放得到)的基线,仅用于诊断字段

	result := RecoveryResult{Reducer: reducer}
	quarantineSet := make(map[string]struct{})

	// REST 快照先建立切点，再按 FIFO 应用从 WS 缓冲的事件，符合「先开 WS 缓冲，再取 REST
	// 快照，最后排空缓冲」的启动协议。重叠成交由 trade_id 去重。
	for _, o := range input.Snapshot.OpenOrders {
		if err := reducer.UpdateOrderState(o.ClientOrderID, o.State); err != nil {
			result.Issues = append(result.Issues, RecoveryIssue{Symbol: o.Symbol, Kind: "rest_order", ID: o.ClientOrderID, Error: err.Error()})
			quarantineSet[o.Symbol] = struct{}{}
		}
	}
	for _, f := range input.Snapshot.RecentFills {
		if _, err := reducer.ApplyFill(f); err != nil {
			result.Issues = append(result.Issues, RecoveryIssue{Symbol: f.Symbol, Kind: "rest_fill", ID: f.TradeID, Error: err.Error()})
			quarantineSet[f.Symbol] = struct{}{}
		}
	}
	for _, ev := range input.BufferedEvents {
		if ev.OrderUpdate != nil {
			o := ev.OrderUpdate
			if err := reducer.UpdateOrderState(o.ClientOrderID, o.State); err != nil {
				result.Issues = append(result.Issues, RecoveryIssue{Symbol: o.Symbol, Kind: "ws_order", ID: o.ClientOrderID, Error: err.Error()})
				quarantineSet[o.Symbol] = struct{}{}
			}
		}
		if ev.Fill != nil {
			f := ev.Fill
			if _, err := reducer.ApplyFill(*f); err != nil {
				result.Issues = append(result.Issues, RecoveryIssue{Symbol: f.Symbol, Kind: "ws_fill", ID: f.TradeID, Error: err.Error()})
				quarantineSet[f.Symbol] = struct{}{}
			}
		}
	}

	symbolSet := make(map[string]struct{}, len(localBaseline)+len(input.Snapshot.Positions))
	for symbol := range localBaseline {
		symbolSet[symbol] = struct{}{}
	}
	for symbol := range input.Snapshot.Positions {
		symbolSet[symbol] = struct{}{}
	}
	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	for _, symbol := range symbols {
		localQty := localBaseline[symbol].Qty
		reconciledQty := reducer.PositionQty(symbol)
		exchangeQty := input.Snapshot.Positions[symbol]
		// 启动前本地为空、交易所却有真仓时，即使 recent fills 恰好能解释数量也必须隔离：
		// 归因/退出论点/claim 所有权仍然缺失，不能仅凭数量相等自动接管未知仓位。
		if math.Abs(localQty) <= qtyMatchTolerance && math.Abs(exchangeQty) > qtyMatchTolerance {
			result.Discrepancies = append(result.Discrepancies, Discrepancy{
				Symbol: symbol, Reason: DiscrepancyExchangeOnlyPosition,
				LocalQty: localQty, ReconciledQty: reconciledQty, ExchangeQty: exchangeQty,
			})
			quarantineSet[symbol] = struct{}{}
			continue
		}
		if !floatsClose(reconciledQty, exchangeQty, qtyMatchTolerance) {
			reason := DiscrepancyLocalExchangeQtyMismatch
			result.Discrepancies = append(result.Discrepancies, Discrepancy{
				Symbol: symbol, Reason: reason,
				LocalQty: localQty, ReconciledQty: reconciledQty, ExchangeQty: exchangeQty,
			})
			quarantineSet[symbol] = struct{}{}
		}
	}

	for _, o := range input.Snapshot.OpenOrders {
		if o.State == OrderStateUnknown {
			result.Discrepancies = append(result.Discrepancies, Discrepancy{
				Symbol: o.Symbol, Reason: DiscrepancyUnknownOpenOrder,
			})
			quarantineSet[o.Symbol] = struct{}{}
		}
	}

	result.Quarantined = make([]string, 0, len(quarantineSet))
	for symbol := range quarantineSet {
		result.Quarantined = append(result.Quarantined, symbol)
	}
	sort.Strings(result.Quarantined)
	return result
}

func floatsClose(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}
