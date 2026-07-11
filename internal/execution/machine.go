package execution

// machine.go 把 claim + reducer + WAL 粘成一个最小可用闭环(decision → intent → claim →
// journal → fill → position),供上层引擎(如 internal/app 的方向执行器)接入,也是本包
// 并发/崩溃恢复场景测试的驱动对象。它本身不知道任何交易所细节,只认 execution 包内的中性类型。
import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Engine 是订单真相底座的进程内单实例:一个 symbol 集合共享同一个 ClaimManager/TradeReducer,
// 全部状态变更(claim 获取释放、成交应用)都尝试写 WAL——WAL 为 nil 时退化为纯内存模式
// (供不需要落盘的单测/回放场景使用)。
type Engine struct {
	mu      sync.Mutex
	Claims  *ClaimManager
	Reducer *TradeReducer
	wal     *WAL
}

// NewEngine 构造一个新的 Engine;wal 可为 nil(纯内存模式,不落盘)。
func NewEngine(wal *WAL) *Engine {
	return &Engine{Claims: NewClaimManager(), Reducer: NewTradeReducer(), wal: wal}
}

// appendWAL 是内部小工具:wal 为 nil 时表示显式纯内存模式；有 WAL 时任何落盘失败都向上传递，
// 绝不吞错后继续修改内存状态。
func (e *Engine) appendWAL(rec Record) error {
	if e == nil || e.wal == nil {
		return nil
	}
	_, err := e.wal.Append(rec)
	return err
}

func recordPayload(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("execution: 序列化 WAL payload 失败: %w", err)
	}
	return data, nil
}

// OpenIntent 是「决策 → per-symbol claim → journal」的最小闭环:同一 symbol 并发多个决策
// 只有一个能拿到 claim,其余必须整体放弃本轮决策(上层重试/换币),不得绕过 claim 直接下单。
// 成功返回的 release 必须在 intent 收尾(成交/撤单/拒单终态确认)后调用一次。
func (e *Engine) OpenIntent(symbol, decisionID string, intentSeq int, now time.Time) (intentID string, release func() error, err error) {
	if e == nil {
		return "", nil, fmt.Errorf("execution: engine 未初始化")
	}
	intentID = DeriveIntentID(decisionID, intentSeq)
	rel, err := e.Claims.TryAcquire(symbol, intentID, now)
	if err != nil {
		return "", nil, err
	}
	payload, err := recordPayload(Claim{Symbol: symbol, Owner: intentID, AcquiredAt: now})
	if err != nil {
		rel()
		return "", nil, err
	}
	if err := e.appendWAL(Record{Kind: RecordIntentCreated, DecisionID: decisionID, IntentID: intentID, Symbol: symbol, WrittenAt: now}); err != nil {
		rel()
		return "", nil, err
	}
	if err := e.appendWAL(Record{Kind: RecordClaimAcquired, DecisionID: decisionID, IntentID: intentID, Symbol: symbol, WrittenAt: now, Payload: payload}); err != nil {
		rel()
		return "", nil, err
	}
	release = func() error {
		at := time.Now().UTC()
		if err := e.appendWAL(Record{Kind: RecordClaimReleased, DecisionID: decisionID, IntentID: intentID, Symbol: symbol, WrittenAt: at, Payload: payload}); err != nil {
			return err // WAL 未确认前不释放内存 claim
		}
		rel()
		return nil
	}
	return intentID, release, nil
}

// MarkOrderState 把订单状态迁移记录进 reducer(供后续迟到成交异常判定)并写 WAL。
func (e *Engine) MarkOrderState(clientOrderID, symbol string, state OrderState, now time.Time) error {
	if e == nil {
		return fmt.Errorf("execution: engine 未初始化")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.Reducer.ValidateOrderUpdate(clientOrderID, state); err != nil {
		if qErr := e.quarantineSymbol(symbol, "illegal_order_transition", now, clientOrderID); qErr != nil {
			return fmt.Errorf("%v; quarantine WAL 失败: %w", err, qErr)
		}
		return err
	}
	payload, err := recordPayload(OrderSnapshot{Symbol: symbol, ClientOrderID: clientOrderID, State: state})
	if err != nil {
		return err
	}
	if err := e.appendWAL(Record{Kind: RecordOrderTransition, Symbol: symbol, ClientOrderID: clientOrderID, WrittenAt: now, Payload: payload}); err != nil {
		return err
	}
	if err := e.Reducer.UpdateOrderState(clientOrderID, state); err != nil {
		return err
	}
	if state == OrderStateUnknown {
		return e.quarantineSymbol(symbol, "unknown_order_state", now, clientOrderID)
	}
	return nil
}

func (e *Engine) quarantineSymbol(symbol, reason string, at time.Time, relatedOrderID string) error {
	q := QuarantineRecord{Symbol: symbol, Reason: reason, Since: at, RelatedOrderID: relatedOrderID}
	payload, err := recordPayload(q)
	if err != nil {
		return err
	}
	if err := e.appendWAL(Record{Kind: RecordQuarantined, Symbol: symbol, WrittenAt: at, Payload: payload}); err != nil {
		return err
	}
	e.Claims.Quarantine(symbol, reason, at, relatedOrderID)
	return nil
}

// ApplyFill 幂等应用一笔成交并(在确实生效时)写 WAL。返回值语义见 TradeReducer.ApplyFill。
func (e *Engine) ApplyFill(f Fill) (FillOutcome, error) {
	if e == nil {
		return FillOutcome{}, fmt.Errorf("execution: engine 未初始化")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ValidateFill(f); err != nil {
		return FillOutcome{}, err
	}
	if e.Reducer.HasTrade(f.Symbol, f.TradeID) {
		return e.Reducer.ApplyFill(f)
	}
	payload, err := recordPayload(f)
	if err != nil {
		return FillOutcome{}, err
	}
	if err := e.appendWAL(Record{
		Kind: RecordFillApplied, Symbol: f.Symbol, ClientOrderID: f.ClientOrderID,
		ExchangeOrderID: f.ExchangeOrderID, TradeID: f.TradeID,
		WrittenAt: timeOrNow(f.ReceiveTime), Payload: payload,
	}); err != nil {
		return FillOutcome{}, err
	}
	outcome, err := e.Reducer.ApplyFill(f)
	if err != nil {
		return outcome, err
	}
	if outcome.Anomaly != "" {
		at := timeOrNow(f.ReceiveTime)
		q := QuarantineRecord{Symbol: f.Symbol, Reason: outcome.Anomaly, Since: at, RelatedOrderID: f.ExchangeOrderID}
		qPayload, payloadErr := recordPayload(q)
		if payloadErr != nil {
			return outcome, payloadErr
		}
		if walErr := e.appendWAL(Record{Kind: RecordQuarantined, Symbol: f.Symbol, TradeID: f.TradeID, WrittenAt: at, Payload: qPayload}); walErr != nil {
			return outcome, walErr
		}
		e.Claims.Quarantine(f.Symbol, outcome.Anomaly, at, f.ExchangeOrderID)
	}
	return outcome, nil
}

// ConsumePrivateEvents 从 src 逐条消费事件直至通道关闭,把 order 更新与 fill 分别路由到
// MarkOrderState/ApplyFill。用于把任意 PrivateEventSource(真实适配器或 FixtureEventSource)
// 接入本 Engine;调用方应在收敛完成(如 Recover 已跑过且 Quarantined 为空)后才启动本消费循环
// 用于正常运行期,recovery 阶段的缓冲事件走 RecoveryInput.BufferedEvents,不走本方法。
func (e *Engine) ConsumePrivateEvents(src PrivateEventSource) error {
	if e == nil || src == nil {
		return fmt.Errorf("execution: engine 或事件源未初始化")
	}
	for ev := range src.Events() {
		if ev.Order != nil {
			if err := e.MarkOrderState(ev.Order.ClientOrderID, ev.Order.Symbol, ev.Order.State, timeOrNow(ev.ReceivedAt)); err != nil {
				return err
			}
		}
		if ev.Fill != nil {
			if _, err := e.ApplyFill(*ev.Fill); err != nil {
				return err
			}
		}
	}
	return src.Err()
}

// SeedFromRecovery 用 Recover 的结果重建 Engine 的运行态:替换 Reducer 为收敛后的权威版本,
// 并把全部 discrepancy symbol 打上 quarantine,阻断后续新 claim,直到人工/对账流程显式清除。
func (e *Engine) SeedFromRecovery(result RecoveryResult, now time.Time) error {
	if e == nil {
		return fmt.Errorf("execution: engine 未初始化")
	}
	if result.Reducer != nil {
		e.Reducer = result.Reducer
	}
	for _, symbol := range result.Quarantined {
		q := QuarantineRecord{Symbol: symbol, Reason: "recovery_discrepancy", Since: now}
		payload, err := recordPayload(q)
		if err != nil {
			return err
		}
		if err := e.appendWAL(Record{Kind: RecordQuarantined, Symbol: symbol, WrittenAt: now, Payload: payload}); err != nil {
			return err
		}
		e.Claims.Quarantine(symbol, q.Reason, now, "")
	}
	return e.appendWAL(Record{Kind: RecordRecoverySnapshot, WrittenAt: now})
}

// ReplayEngine 从 WAL 记录恢复 reducer、未释放 claim 与 quarantine。它是 crash recovery 的
// 本地第一阶段；随后调用方再把返回的 Reducer 传给 Recover，与 REST 快照和 WS 缓冲收敛。
func ReplayEngine(records []Record) (*Engine, error) {
	e := NewEngine(nil)
	releases := make(map[string]func())
	for _, rec := range records {
		switch rec.Kind {
		case RecordClaimAcquired:
			var claim Claim
			if err := json.Unmarshal(rec.Payload, &claim); err != nil {
				return nil, fmt.Errorf("execution: 重放 claim seq=%d: %w", rec.Seq, err)
			}
			release, err := e.Claims.TryAcquire(claim.Symbol, claim.Owner, claim.AcquiredAt)
			if err != nil {
				return nil, fmt.Errorf("execution: 重放 claim seq=%d: %w", rec.Seq, err)
			}
			releases[claim.Symbol+"|"+claim.Owner] = release
		case RecordClaimReleased:
			var claim Claim
			if err := json.Unmarshal(rec.Payload, &claim); err != nil {
				return nil, fmt.Errorf("execution: 重放 claim release seq=%d: %w", rec.Seq, err)
			}
			if release := releases[claim.Symbol+"|"+claim.Owner]; release != nil {
				release()
				delete(releases, claim.Symbol+"|"+claim.Owner)
			}
		case RecordOrderTransition:
			var order OrderSnapshot
			if err := json.Unmarshal(rec.Payload, &order); err != nil {
				return nil, fmt.Errorf("execution: 重放 order seq=%d: %w", rec.Seq, err)
			}
			if err := e.Reducer.UpdateOrderState(order.ClientOrderID, order.State); err != nil {
				return nil, fmt.Errorf("execution: 重放 order seq=%d: %w", rec.Seq, err)
			}
			if order.State == OrderStateUnknown {
				e.Claims.Quarantine(order.Symbol, "unknown_order_state", rec.WrittenAt, order.ClientOrderID)
			}
		case RecordFillApplied:
			var fill Fill
			if err := json.Unmarshal(rec.Payload, &fill); err != nil {
				return nil, fmt.Errorf("execution: 重放 fill seq=%d: %w", rec.Seq, err)
			}
			outcome, err := e.Reducer.ApplyFill(fill)
			if err != nil {
				return nil, fmt.Errorf("execution: 重放 fill seq=%d: %w", rec.Seq, err)
			}
			if outcome.Anomaly != "" {
				e.Claims.Quarantine(fill.Symbol, outcome.Anomaly, timeOrNow(fill.ReceiveTime), fill.ExchangeOrderID)
			}
		case RecordQuarantined:
			var q QuarantineRecord
			if err := json.Unmarshal(rec.Payload, &q); err != nil {
				return nil, fmt.Errorf("execution: 重放 quarantine seq=%d: %w", rec.Seq, err)
			}
			e.Claims.Quarantine(q.Symbol, q.Reason, q.Since, q.RelatedOrderID)
		case RecordQuarantineCleared:
			e.Claims.ClearQuarantine(rec.Symbol)
		}
	}
	return e, nil
}

func timeOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
