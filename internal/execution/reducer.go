package execution

// reducer.go 实现幂等成交 reducer:同一笔成交(以交易所 trade_id 为准,交易所缺失时调用方必须
// 用 DeriveLocalTradeID 合成占位键)无论被投递多少次,只被计入 Position 一次;撤单后迟到的
// 成交依然会被计入仓位真相(交易所已真实成交,必须反映),但会被标记为 anomaly 供上层触发对账/
// quarantine,而不是被吞掉造成本地-交易所仓位漂移。
import (
	"fmt"
	"math"
	"sync"
	"time"

	"lingxi/internal/money"
)

// Side 是成交方向,独立于任何具体交易所类型(本包不依赖 internal/venue)。
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

func (s Side) signedQty(qty float64) float64 {
	if s == SideSell {
		return -math.Abs(qty)
	}
	return math.Abs(qty)
}

// Fill 是一笔幂等去重的成交事件。TradeID 是去重键的核心:同一 (Symbol, TradeID) 只应用一次。
// 交易所侧真实 tradeId 到达前,调用方可用本地合成键(DeriveLocalTradeID)占位,但一旦交易所侧
// tradeId 到达必须以其为准重新投递,避免本地占位键与交易所真相长期错位。
type Fill struct {
	Symbol          string
	ClientOrderID   string
	ExchangeOrderID string
	TradeID         string
	Side            Side
	Qty             float64 // 必须 >0(币数量);方向由 Side 决定符号
	Price           float64 // 必须 >0
	FeeMicros       money.Micros
	IsMaker         bool
	ExchangeTime    time.Time
	ReceiveTime     time.Time
}

func (f Fill) dedupKey() string {
	return f.Symbol + "|" + f.TradeID
}

// ValidateFill 在任何 WAL/状态变更前校验成交事件。Engine 先调用它、再把完整 Fill
// 写入 WAL、最后才交给 reducer 应用，从而保持真正的 write-ahead 顺序。
func ValidateFill(f Fill) error {
	if f.Symbol == "" {
		return fmt.Errorf("execution: fill 缺少 symbol")
	}
	if f.TradeID == "" {
		return fmt.Errorf("execution: fill 缺少 trade_id,拒绝在无法去重的情况下入账")
	}
	if f.Qty <= 0 {
		return fmt.Errorf("execution: fill 数量必须为正, got %v", f.Qty)
	}
	if f.Price <= 0 {
		return fmt.Errorf("execution: fill 价格必须为正, got %v", f.Price)
	}
	if f.Side != SideBuy && f.Side != SideSell {
		return fmt.Errorf("execution: fill 方向非法, got %q", f.Side)
	}
	return nil
}

// Position 是某 symbol 的持仓真相快照,由 reducer 从零态起逐笔幂等应用 Fill 推导而来。
// AvgPrice 是当前净头寸的加权平均建仓价;RealizedPnLMicros 是自 reducer 创建以来累计已实现盈亏
// (不含手续费,手续费单独累计在 FeesMicros,归因时按需相减)。
type Position struct {
	Symbol            string
	Qty               float64 // 有符号:多为正、空为负
	AvgPrice          float64
	RealizedPnLMicros money.Micros
	FeesMicros        money.Micros
	LastTradeID       string
	LastAppliedAt     time.Time
	AppliedFillCount  int
}

// Side 返回当前净头寸方向(仅展示/归因用,不是独立状态源)。
func (p Position) Side() PositionSide { return sideOfQty(p.Qty) }

// FillOutcome 是单次 ApplyFill 调用的结果。
type FillOutcome struct {
	// Applied 为 false 表示本次调用因重复 trade_id 被去重跳过,Position 字段仍返回当前快照
	// (未变化),方便调用方无脑记录/展示而不用额外判空。
	Applied bool
	// Anomaly 非空时表示本次成交在正常路径之外(如撤单后迟到、无对应挂单记录等),调用方应据此
	// 决定是否触发 quarantine;anomaly 不会阻止仓位被正确更新——交易所真相必须被反映。
	Anomaly  string
	Position Position
}

const (
	// AnomalyLateFillAfterTerminal 表示成交到达时,该订单已被本地记录为终态(filled/canceled/
	// rejected),典型场景为「撤单请求与最后一笔成交竞态,交易所判撤单成功但仍有在途成交」。
	AnomalyLateFillAfterTerminal = "late_fill_after_terminal"
	// AnomalyFillWithoutOrderRecord 表示成交到达时,reducer 从未见过该 client_order_id 的任何
	// 状态记录(典型场景为进程重启后 WAL 未覆盖到、或成交先于订单确认事件到达)。
	AnomalyFillWithoutOrderRecord = "fill_without_order_record"
)

// TradeReducer 是并发安全的幂等成交/持仓聚合器。
type TradeReducer struct {
	mu            sync.Mutex
	seenTrades    map[string]struct{}
	positions     map[string]Position
	orderState    map[string]OrderState // client_order_id -> 最近已知状态,用于迟到成交异常判定
	orderSeenOnce map[string]struct{}   // client_order_id -> 是否曾经被 MarkOrderState 记录过
}

// NewTradeReducer 构造空的 reducer(零持仓、空去重表)。
func NewTradeReducer() *TradeReducer {
	return &TradeReducer{
		seenTrades:    make(map[string]struct{}),
		positions:     make(map[string]Position),
		orderState:    make(map[string]OrderState),
		orderSeenOnce: make(map[string]struct{}),
	}
}

// MarkOrderState 记录某 client_order_id 的最新已知订单状态,供后续 ApplyFill 判定「迟到成交」
// 异常。调用方(订单事件路径)应在每次收到 order 状态更新时调用。
func (r *TradeReducer) MarkOrderState(clientOrderID string, state OrderState) {
	if clientOrderID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orderState[clientOrderID] = state
	r.orderSeenOnce[clientOrderID] = struct{}{}
}

// UpdateOrderState 按合法状态机更新订单；重复状态幂等，非法倒退/终态跳转会报错并保留旧状态。
// 首次见到的订单事件可从任意状态建立基线，因为启动恢复可能从中途接入。
func (r *TradeReducer) UpdateOrderState(clientOrderID string, state OrderState) error {
	if clientOrderID == "" {
		return fmt.Errorf("execution: order 缺少 client_order_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, known := r.orderState[clientOrderID]
	if known && old != state {
		if old == OrderStateUnknown {
			if err := ResolveUnknownOrderTransition(state); err != nil {
				return err
			}
		} else if err := ValidateOrderTransition(old, state); err != nil {
			return err
		}
	}
	r.orderState[clientOrderID] = state
	r.orderSeenOnce[clientOrderID] = struct{}{}
	return nil
}

// ValidateOrderUpdate 只校验、不修改状态，供 Engine 在 WAL write-ahead 之前 fail-fast。
func (r *TradeReducer) ValidateOrderUpdate(clientOrderID string, state OrderState) error {
	if clientOrderID == "" {
		return fmt.Errorf("execution: order 缺少 client_order_id")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, known := r.orderState[clientOrderID]
	if !known || old == state {
		return nil
	}
	if old == OrderStateUnknown {
		return ResolveUnknownOrderTransition(state)
	}
	return ValidateOrderTransition(old, state)
}

// OrderState 返回某 client_order_id 当前已知的最近状态(未见过则 ok=false)。
func (r *TradeReducer) OrderState(clientOrderID string) (OrderState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.orderState[clientOrderID]
	return s, ok
}

// ApplyFill 幂等应用一笔成交。同一 (Symbol, TradeID) 的重复投递只会在首次调用时生效,
// 之后所有重复调用都返回 Applied=false 且不改变 Position,从而保证「重复 fill 不双计」。
func (r *TradeReducer) ApplyFill(f Fill) (FillOutcome, error) {
	if err := ValidateFill(f); err != nil {
		return FillOutcome{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := f.dedupKey()
	if _, dup := r.seenTrades[key]; dup {
		return FillOutcome{Applied: false, Position: r.positions[f.Symbol]}, nil
	}

	anomaly := ""
	if f.ClientOrderID != "" {
		if state, known := r.orderState[f.ClientOrderID]; known {
			// OKX 等交易所会在同一条推送里同时给出 state=filled 与最后一笔 fill；
			// filled 后收到对应成交是正常最终确认。只有 canceled/rejected 后到达的成交才是竞态异常。
			if state == OrderStateCanceled || state == OrderStateRejected {
				anomaly = AnomalyLateFillAfterTerminal
			}
		} else if _, everSeen := r.orderSeenOnce[f.ClientOrderID]; !everSeen {
			anomaly = AnomalyFillWithoutOrderRecord
		}
	}

	pos := r.positions[f.Symbol]
	pos.Symbol = f.Symbol
	pos = applyFillToPosition(pos, f)
	pos.LastTradeID = f.TradeID
	pos.LastAppliedAt = f.ReceiveTime
	pos.AppliedFillCount++
	pos.FeesMicros = pos.FeesMicros.Add(f.FeeMicros)

	r.seenTrades[key] = struct{}{}
	r.positions[f.Symbol] = pos

	return FillOutcome{Applied: true, Anomaly: anomaly, Position: pos}, nil
}

// HasTrade 报告某笔交易是否已应用，用于 Engine 避免为重复 WS/REST fill 再写一条 WAL。
func (r *TradeReducer) HasTrade(symbol, tradeID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.seenTrades[symbol+"|"+tradeID]
	return ok
}

// applyFillToPosition 是纯函数版持仓更新:同向加仓走加权平均价,反向减仓/反手在被平掉的
// 那部分上结算已实现盈亏,剩余部分(反手时)按本次成交价重新开仓。
func applyFillToPosition(pos Position, f Fill) Position {
	signedDelta := f.Side.signedQty(f.Qty)
	newQty := pos.Qty + signedDelta

	switch {
	case math.Abs(pos.Qty) <= positionQtyTolerance || sameSign(pos.Qty, signedDelta):
		// 空仓开新仓,或同向加仓:用定点金额算加权平均价,避免连续加仓的浮点漂移。
		existingNotional := money.FromUSDT(pos.AvgPrice * math.Abs(pos.Qty))
		addedNotional := money.FromUSDT(f.Price * f.Qty)
		totalQty := math.Abs(pos.Qty) + f.Qty
		if totalQty > positionQtyTolerance {
			pos.AvgPrice = existingNotional.Add(addedNotional).USDT() / totalQty
		}
		pos.Qty = newQty
	default:
		// 反向:先在「被平掉的那部分」结算已实现盈亏。
		closingQty := math.Min(math.Abs(signedDelta), math.Abs(pos.Qty))
		pnlPerUnit := (f.Price - pos.AvgPrice)
		if pos.Qty < 0 {
			pnlPerUnit = (pos.AvgPrice - f.Price)
		}
		realized := money.FromUSDT(pnlPerUnit * closingQty)
		pos.RealizedPnLMicros = pos.RealizedPnLMicros.Add(realized)

		if math.Abs(newQty) <= positionQtyTolerance {
			pos.Qty = 0
			pos.AvgPrice = 0
		} else if sameSign(newQty, signedDelta) {
			// 反手:平掉原有全部头寸后,剩余数量按本次成交价重新开仓。
			pos.Qty = newQty
			pos.AvgPrice = f.Price
		} else {
			// 仍是原方向的部分减仓,均价不变。
			pos.Qty = newQty
		}
	}
	return pos
}

func sameSign(a, b float64) bool {
	if a == 0 || b == 0 {
		return true
	}
	return (a > 0) == (b > 0)
}

// Position 返回 symbol 当前持仓快照(不存在则返回零值, ok=false)。
func (r *TradeReducer) Position(symbol string) (Position, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos, ok := r.positions[symbol]
	return pos, ok
}

// PositionQty 返回 symbol 当前有符号持仓数量(未知 symbol 视为 0,即「本地无记录=本地视为空仓」,
// 由调用方结合交易所快照自行判定是否构成 discrepancy)。
func (r *TradeReducer) PositionQty(symbol string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.positions[symbol].Qty
}

// Snapshot 返回全部 symbol 的持仓快照拷贝,用于落盘/展示/recovery 种子。
func (r *TradeReducer) Snapshot() map[string]Position {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Position, len(r.positions))
	for k, v := range r.positions {
		out[k] = v
	}
	return out
}

// SeedPosition 用交易所真相(REST 快照)直接设定某 symbol 的基线持仓,不经过 Fill 路径。
// 仅供 recovery 在「用交易所仓位作为权威基线」阶段调用;运行期业务代码应始终走 ApplyFill,
// 不应绕过成交路径直接篡改持仓,否则会破坏「持仓=成交逐笔累积」的可审计性。
func (r *TradeReducer) SeedPosition(symbol string, qty float64, avgPrice float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.positions[symbol] = Position{Symbol: symbol, Qty: qty, AvgPrice: avgPrice}
}
