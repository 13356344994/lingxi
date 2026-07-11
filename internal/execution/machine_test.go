package execution

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestEngineOpenIntentClaimAndFillClosedLoop(t *testing.T) {
	dir := t.TempDir()
	wal, err := OpenWAL(filepath.Join(dir, "wal.jsonl"))
	if err != nil {
		t.Fatalf("OpenWAL 失败: %v", err)
	}
	defer wal.Close()

	e := NewEngine(wal)
	now := time.Now()
	intentID, release, err := e.OpenIntent("BTC-USDT", NewDecisionID(now), 0, now)
	if err != nil {
		t.Fatalf("OpenIntent 失败: %v", err)
	}
	clientOrderID := DeriveClientOrderID(intentID, 0)
	e.MarkOrderState(clientOrderID, "BTC-USDT", OrderStateAcked, now)

	outcome, err := e.ApplyFill(Fill{
		Symbol: "BTC-USDT", ClientOrderID: clientOrderID, TradeID: DeriveLocalTradeID(clientOrderID, 0),
		Side: SideBuy, Qty: 1, Price: 50000, ReceiveTime: now,
	})
	if err != nil {
		t.Fatalf("ApplyFill 失败: %v", err)
	}
	if !outcome.Applied || outcome.Anomaly != "" {
		t.Fatalf("正常闭环内的成交不应有异常, got %+v", outcome)
	}
	e.MarkOrderState(clientOrderID, "BTC-USDT", OrderStateFilled, now)
	release()

	if _, held := e.Claims.Holder("BTC-USDT"); held {
		t.Fatal("release 后 claim 应已释放")
	}

	records, err := ReplayWAL(wal.Path())
	if err != nil {
		t.Fatalf("重放 WAL 失败: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("闭环内的关键事件应全部落 WAL")
	}
}

func TestEngineConsumePrivateEventsFromFixture(t *testing.T) {
	e := NewEngine(nil)
	e.Reducer.MarkOrderState("cli1", OrderStateAcked)
	events := []PrivateEvent{
		{Order: &OrderSnapshot{Symbol: "BTC-USDT", ClientOrderID: "cli1", State: OrderStatePartiallyFilled}},
		{Fill: &Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}},
		{Fill: &Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}}, // 重复,应被去重
		{Order: &OrderSnapshot{Symbol: "BTC-USDT", ClientOrderID: "cli1", State: OrderStateFilled}},
	}
	src := NewFixtureEventSource(events)
	if err := e.ConsumePrivateEvents(src); err != nil {
		t.Fatalf("ConsumePrivateEvents 失败: %v", err)
	}
	if qty := e.Reducer.PositionQty("BTC-USDT"); qty != 1 {
		t.Fatalf("fixture 事件流收敛后仓位应为 1(重复 fill 去重), got %v", qty)
	}
	state, ok := e.Reducer.OrderState("cli1")
	if !ok || state != OrderStateFilled {
		t.Fatalf("订单最终状态应为 filled, got %v ok=%v", state, ok)
	}
}

func TestEngineUnknownOrderQuarantinesWithoutReleasingClaim(t *testing.T) {
	e := NewEngine(nil)
	now := time.Now()
	_, _, err := e.OpenIntent("BTC-USDT", "dunknown", 0, now)
	if err != nil {
		t.Fatalf("OpenIntent: %v", err)
	}
	if err := e.MarkOrderState("cli-unknown", "BTC-USDT", OrderStateUnknown, now); err != nil {
		t.Fatalf("记录 unknown 状态失败: %v", err)
	}
	if _, held := e.Claims.Holder("BTC-USDT"); !held {
		t.Fatal("UNKNOWN 期间不得释放既有 claim")
	}
	if q, ok := e.Claims.IsQuarantined("BTC-USDT"); !ok || q.Reason != "unknown_order_state" {
		t.Fatalf("UNKNOWN 必须 quarantine, got %+v ok=%v", q, ok)
	}
}

func TestEngineSeedFromRecoveryQuarantinesDiscrepancies(t *testing.T) {
	e := NewEngine(nil)
	result := Recover(RecoveryInput{
		Snapshot: ExchangeSnapshot{Positions: map[string]float64{"BTC-USDT": 1}},
	})
	e.SeedFromRecovery(result, time.Now())

	if _, err := e.Claims.TryAcquire("BTC-USDT", "intent-x", time.Now()); !errors.Is(err, ErrSymbolQuarantined) {
		t.Fatalf("恢复出的 discrepancy 应阻断新 claim, got %v", err)
	}
}

func TestReplayEngineRestoresFillClaimAndQuarantine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.wal")
	wal, err := OpenWAL(path)
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	e := NewEngine(wal)
	now := time.Now().UTC()
	intentID, _, err := e.OpenIntent("BTC-USDT", "dcrash", 0, now)
	if err != nil {
		t.Fatalf("OpenIntent: %v", err)
	}
	clientID := DeriveClientOrderID(intentID, 0)
	if err := e.MarkOrderState(clientID, "BTC-USDT", OrderStateCanceled, now); err != nil {
		t.Fatalf("MarkOrderState: %v", err)
	}
	// canceled 后的迟到 fill 会应用仓位并落 quarantine；模拟进程在未释放 claim 前崩溃。
	if _, err := e.ApplyFill(Fill{
		Symbol: "BTC-USDT", ClientOrderID: clientID, ExchangeOrderID: "ord1",
		TradeID: "trade1", Side: SideBuy, Qty: 0.25, Price: 100, ReceiveTime: now,
	}); err != nil {
		t.Fatalf("ApplyFill: %v", err)
	}
	if err := wal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records, err := ReplayWAL(path)
	if err != nil {
		t.Fatalf("ReplayWAL: %v", err)
	}
	recovered, err := ReplayEngine(records)
	if err != nil {
		t.Fatalf("ReplayEngine: %v", err)
	}
	if qty := recovered.Reducer.PositionQty("BTC-USDT"); qty != 0.25 {
		t.Fatalf("crash 重放后仓位=%v, want 0.25", qty)
	}
	if claim, ok := recovered.Claims.Holder("BTC-USDT"); !ok || claim.Owner != intentID {
		t.Fatalf("未释放 claim 应恢复, got %+v ok=%v", claim, ok)
	}
	if q, ok := recovered.Claims.IsQuarantined("BTC-USDT"); !ok || q.Reason != AnomalyLateFillAfterTerminal {
		t.Fatalf("迟到 fill quarantine 应恢复, got %+v ok=%v", q, ok)
	}
}
