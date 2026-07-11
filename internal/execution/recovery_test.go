package execution

import (
	"testing"
	"time"
)

func TestRecoverConvergesBufferedEventsOntoLocalReducerNoDiscrepancy(t *testing.T) {
	local := NewTradeReducer()
	local.MarkOrderState("cli1", OrderStateAcked)
	if _, err := local.ApplyFill(Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}); err != nil {
		t.Fatalf("预置本地成交失败: %v", err)
	}

	// 崩溃期间又有一笔成交,只出现在缓冲的 WS 事件里(REST 近期成交窗口未必覆盖到)。
	buffered := []BufferedEvent{
		{Fill: &Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "t2", Side: SideBuy, Qty: 1, Price: 110, ReceiveTime: time.Now()}},
	}
	snapshot := ExchangeSnapshot{
		Positions: map[string]float64{"BTC-USDT": 2}, // 交易所真相:2 手,与「本地1手+缓冲1手」收敛后一致
	}

	result := Recover(RecoveryInput{LocalReducer: local, BufferedEvents: buffered, Snapshot: snapshot})
	if len(result.Discrepancies) != 0 {
		t.Fatalf("收敛后应无 discrepancy, got %+v", result.Discrepancies)
	}
	if len(result.Quarantined) != 0 {
		t.Fatalf("收敛后应无 quarantine, got %v", result.Quarantined)
	}
	if qty := result.Reducer.PositionQty("BTC-USDT"); qty != 2 {
		t.Fatalf("收敛后仓位应为 2, got %v", qty)
	}
}

func TestRecoverDoesNotDoubleCountRecentFillsAlreadyInWAL(t *testing.T) {
	local := NewTradeReducer()
	fill := Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}
	if _, err := local.ApplyFill(fill); err != nil {
		t.Fatalf("预置本地成交失败: %v", err)
	}

	// REST 近期成交窗口把同一笔 t1 又拉回来了(与 WAL 重叠),必须被去重,不能双计。
	snapshot := ExchangeSnapshot{
		RecentFills: []Fill{fill},
		Positions:   map[string]float64{"BTC-USDT": 1},
	}
	result := Recover(RecoveryInput{LocalReducer: local, Snapshot: snapshot})
	if qty := result.Reducer.PositionQty("BTC-USDT"); qty != 1 {
		t.Fatalf("重叠的 REST 近期成交不应被双计, got qty=%v", qty)
	}
	if len(result.Discrepancies) != 0 {
		t.Fatalf("不应有 discrepancy, got %+v", result.Discrepancies)
	}
}

func TestRecoverFlagsGenuineLocalExchangeDiscrepancy(t *testing.T) {
	local := NewTradeReducer()
	if _, err := local.ApplyFill(Fill{Symbol: "BTC-USDT", TradeID: "t1", Side: SideBuy, Qty: 2, Price: 100, ReceiveTime: time.Now()}); err != nil {
		t.Fatalf("预置本地成交失败: %v", err)
	}
	// 交易所侧真相是 2.5:存在一笔本地/缓冲/近期成交都覆盖不到的「裸差」。
	snapshot := ExchangeSnapshot{Positions: map[string]float64{"BTC-USDT": 2.5}}

	result := Recover(RecoveryInput{LocalReducer: local, Snapshot: snapshot})
	if len(result.Discrepancies) != 1 {
		t.Fatalf("应有恰好 1 条 discrepancy, got %+v", result.Discrepancies)
	}
	d := result.Discrepancies[0]
	if d.Symbol != "BTC-USDT" || d.Reason != DiscrepancyLocalExchangeQtyMismatch {
		t.Fatalf("discrepancy 内容不符: %+v", d)
	}
	if len(result.Quarantined) != 1 || result.Quarantined[0] != "BTC-USDT" {
		t.Fatalf("应隔离 BTC-USDT, got %v", result.Quarantined)
	}
}

func TestRecoverFlagsExchangeOnlyPositionWithoutLocalRecord(t *testing.T) {
	// 本地完全没有 BTC-USDT 的任何记录(全新进程/WAL 缺失该 symbol),但交易所有仓位——
	// 典型的「本地空但交易所有仓」必须隔离场景。
	snapshot := ExchangeSnapshot{Positions: map[string]float64{"BTC-USDT": 1.5}}
	result := Recover(RecoveryInput{Snapshot: snapshot})
	if len(result.Quarantined) != 1 || result.Quarantined[0] != "BTC-USDT" {
		t.Fatalf("应隔离 BTC-USDT, got %v", result.Quarantined)
	}
	found := false
	for _, d := range result.Discrepancies {
		if d.Reason == DiscrepancyExchangeOnlyPosition {
			found = true
		}
	}
	if !found {
		t.Fatalf("应出现 exchange_position_without_local_record discrepancy, got %+v", result.Discrepancies)
	}
}

func TestRecoverExchangeOnlyPositionStaysQuarantinedEvenIfRecentFillExplainsQty(t *testing.T) {
	fill := Fill{Symbol: "BTC-USDT", TradeID: "recent1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}
	result := Recover(RecoveryInput{Snapshot: ExchangeSnapshot{
		Positions: map[string]float64{"BTC-USDT": 1}, RecentFills: []Fill{fill},
	}})
	if len(result.Quarantined) != 1 || result.Quarantined[0] != "BTC-USDT" {
		t.Fatalf("无本地所有权/归因的交易所真仓必须隔离, got %v", result.Quarantined)
	}
}

func TestRecoverFlagsUnknownOpenOrder(t *testing.T) {
	snapshot := ExchangeSnapshot{
		OpenOrders: []OrderSnapshot{
			{Symbol: "ETH-USDT", ClientOrderID: "cli-unknown", State: OrderStateUnknown},
		},
	}
	result := Recover(RecoveryInput{Snapshot: snapshot})
	if len(result.Quarantined) != 1 || result.Quarantined[0] != "ETH-USDT" {
		t.Fatalf("unknown 未完成订单应隔离所在 symbol, got %v", result.Quarantined)
	}
}

func TestRecoverWithNilLocalReducerBehavesAsFreshStart(t *testing.T) {
	result := Recover(RecoveryInput{Snapshot: ExchangeSnapshot{Positions: map[string]float64{}}})
	if result.Reducer == nil {
		t.Fatal("nil LocalReducer 应得到一个全新 reducer,而不是 nil")
	}
	if len(result.Discrepancies) != 0 || len(result.Quarantined) != 0 {
		t.Fatalf("空快照+空本地应完全干净, got discrepancies=%+v quarantined=%v", result.Discrepancies, result.Quarantined)
	}
}

func TestRecoverInvalidBufferedEventBecomesIssueAndQuarantine(t *testing.T) {
	bad := Fill{Symbol: "ETH-USDT", TradeID: "", Side: SideBuy, Qty: 1, Price: 100}
	result := Recover(RecoveryInput{
		BufferedEvents: []BufferedEvent{{Fill: &bad}},
		Snapshot:       ExchangeSnapshot{Positions: map[string]float64{}},
	})
	if len(result.Issues) != 1 || result.Issues[0].Kind != "ws_fill" {
		t.Fatalf("非法恢复事件不应静默丢弃, got %+v", result.Issues)
	}
	if len(result.Quarantined) != 1 || result.Quarantined[0] != "ETH-USDT" {
		t.Fatalf("非法事件所在 symbol 应隔离, got %v", result.Quarantined)
	}
}
