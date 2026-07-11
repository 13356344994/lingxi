package execution

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/money"
)

func assertApprox(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %v, want %v (tolerance %v)", name, got, want, tolerance)
	}
}

func TestApplyFillDuplicateTradeIDNotDoubleCounted(t *testing.T) {
	r := NewTradeReducer()
	f := Fill{Symbol: "BTC-USDT", ClientOrderID: "cli1", TradeID: "trade-1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()}
	r.MarkOrderState("cli1", OrderStateAcked)

	out1, err := r.ApplyFill(f)
	if err != nil {
		t.Fatalf("首次应用不应报错: %v", err)
	}
	if !out1.Applied {
		t.Fatal("首次应用应生效")
	}
	if out1.Position.Qty != 1 {
		t.Fatalf("首次应用后 qty 应为 1, got %v", out1.Position.Qty)
	}

	for i := 0; i < 100; i++ {
		out2, err := r.ApplyFill(f) // 同一 trade_id 重复投递 100 次
		if err != nil {
			t.Fatalf("重复应用不应报错: %v", err)
		}
		if out2.Applied {
			t.Fatalf("第 %d 次重复应用应被去重(Applied=false)", i)
		}
		if out2.Position.Qty != 1 {
			t.Fatalf("重复应用不应改变仓位, got qty=%v", out2.Position.Qty)
		}
	}

	pos, ok := r.Position("BTC-USDT")
	if !ok || pos.Qty != 1 || pos.AppliedFillCount != 1 {
		t.Fatalf("最终仓位应只计入一次成交, got %+v ok=%v", pos, ok)
	}
}

func TestApplyFillOpenAddReduceFlip(t *testing.T) {
	r := NewTradeReducer()
	sym := "ETH-USDT"
	apply := func(id string, side Side, qty, price float64) Position {
		out, err := r.ApplyFill(Fill{Symbol: sym, TradeID: id, Side: side, Qty: qty, Price: price, ReceiveTime: time.Now()})
		if err != nil {
			t.Fatalf("apply %s 失败: %v", id, err)
		}
		return out.Position
	}

	// 开多 2 @ 100
	pos := apply("t1", SideBuy, 2, 100)
	assertApprox(t, "开仓后 qty", pos.Qty, 2, 1e-9)
	assertApprox(t, "开仓后均价", pos.AvgPrice, 100, 1e-9)

	// 加多 2 @ 200 -> 加权均价 150,qty=4
	pos = apply("t2", SideBuy, 2, 200)
	assertApprox(t, "加仓后 qty", pos.Qty, 4, 1e-9)
	assertApprox(t, "加仓后均价", pos.AvgPrice, 150, 1e-9)

	// 减仓 1 @ 180 -> realize (180-150)*1=30,剩 qty=3,均价不变 150
	pos = apply("t3", SideSell, 1, 180)
	assertApprox(t, "减仓后 qty", pos.Qty, 3, 1e-9)
	assertApprox(t, "减仓后均价", pos.AvgPrice, 150, 1e-9)
	assertApprox(t, "减仓已实现盈亏", pos.RealizedPnLMicros.USDT(), 30, 1e-6)

	// 反手卖 5 @ 160 -> 先平掉剩余 3 手 realize (160-150)*3=30(累计60),
	// 剩余 2 手反手做空,新均价 160
	pos = apply("t4", SideSell, 5, 160)
	assertApprox(t, "反手后 qty", pos.Qty, -2, 1e-9)
	assertApprox(t, "反手后均价", pos.AvgPrice, 160, 1e-9)
	assertApprox(t, "反手后累计已实现盈亏", pos.RealizedPnLMicros.USDT(), 60, 1e-6)

	// 平掉剩余空头 2 @ 140 -> 空头 realize (160-140)*2=40,累计100,归零
	pos = apply("t5", SideBuy, 2, 140)
	assertApprox(t, "全部平仓后 qty", pos.Qty, 0, 1e-9)
	assertApprox(t, "全部平仓后均价", pos.AvgPrice, 0, 1e-9)
	assertApprox(t, "全部平仓后累计已实现盈亏", pos.RealizedPnLMicros.USDT(), 100, 1e-6)
}

func TestApplyFillLateAfterCancelFlaggedButAppliedOnce(t *testing.T) {
	r := NewTradeReducer()
	r.MarkOrderState("cli-cancel-race", OrderStateAcked)
	r.MarkOrderState("cli-cancel-race", OrderStateCanceled) // 撤单已被判定为终态

	f := Fill{Symbol: "BTC-USDT", ClientOrderID: "cli-cancel-race", TradeID: "late-trade-1", Side: SideBuy, Qty: 0.5, Price: 50000, ReceiveTime: time.Now()}
	out, err := r.ApplyFill(f)
	if err != nil {
		t.Fatalf("迟到成交仍应被应用, got err: %v", err)
	}
	if !out.Applied {
		t.Fatal("迟到成交应生效(交易所真相必须被反映)")
	}
	if out.Anomaly != AnomalyLateFillAfterTerminal {
		t.Fatalf("应标记 late_fill_after_terminal 异常, got %q", out.Anomaly)
	}
	if out.Position.Qty != 0.5 {
		t.Fatalf("迟到成交应计入仓位, got qty=%v", out.Position.Qty)
	}

	// 同一笔迟到成交重复投递,仍不应双计。
	out2, err := r.ApplyFill(f)
	if err != nil {
		t.Fatalf("重复投递不应报错: %v", err)
	}
	if out2.Applied {
		t.Fatal("重复的迟到成交也必须被去重")
	}
	if out2.Position.Qty != 0.5 {
		t.Fatalf("重复投递不应改变仓位, got qty=%v", out2.Position.Qty)
	}
}

func TestFinalFillAfterFilledOrderStateIsNormal(t *testing.T) {
	r := NewTradeReducer()
	r.MarkOrderState("cli-filled", OrderStateFilled)
	out, err := r.ApplyFill(Fill{
		Symbol: "BTC-USDT", ClientOrderID: "cli-filled", TradeID: "final-trade",
		Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now(),
	})
	if err != nil {
		t.Fatalf("最终成交应用失败: %v", err)
	}
	if out.Anomaly != "" {
		t.Fatalf("state=filled 与最终 fill 同推送是正常确认,不应隔离: %q", out.Anomaly)
	}
}

func TestApplyFillWithoutOrderRecordAnomaly(t *testing.T) {
	r := NewTradeReducer()
	out, err := r.ApplyFill(Fill{Symbol: "BTC-USDT", ClientOrderID: "never-seen", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if out.Anomaly != AnomalyFillWithoutOrderRecord {
		t.Fatalf("从未见过的 client_order_id 应标记 fill_without_order_record, got %q", out.Anomaly)
	}
}

func TestApplyFillRejectsInvalidInputs(t *testing.T) {
	r := NewTradeReducer()
	base := Fill{Symbol: "BTC-USDT", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100}
	cases := []struct {
		name string
		mod  func(f Fill) Fill
	}{
		{"缺 symbol", func(f Fill) Fill { f.Symbol = ""; return f }},
		{"缺 trade_id", func(f Fill) Fill { f.TradeID = ""; return f }},
		{"qty<=0", func(f Fill) Fill { f.Qty = 0; return f }},
		{"price<=0", func(f Fill) Fill { f.Price = -1; return f }},
		{"非法方向", func(f Fill) Fill { f.Side = "hold"; return f }},
	}
	for _, c := range cases {
		if _, err := r.ApplyFill(c.mod(base)); err == nil {
			t.Fatalf("%s 应报错", c.name)
		}
	}
}

func TestSeedPositionDoesNotAffectDedup(t *testing.T) {
	r := NewTradeReducer()
	r.SeedPosition("BTC-USDT", 2, 100)
	pos, ok := r.Position("BTC-USDT")
	if !ok || pos.Qty != 2 {
		t.Fatalf("seed 后仓位应为 2, got %+v", pos)
	}
	out, err := r.ApplyFill(Fill{Symbol: "BTC-USDT", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, ReceiveTime: time.Now()})
	if err != nil || !out.Applied {
		t.Fatalf("seed 之后应用新成交应正常生效: %v %+v", err, out)
	}
	if out.Position.Qty != 3 {
		t.Fatalf("seed(2) + fill(+1) 应为 3, got %v", out.Position.Qty)
	}
}

func TestFeesAccumulateAcrossFills(t *testing.T) {
	r := NewTradeReducer()
	_, _ = r.ApplyFill(Fill{Symbol: "BTC-USDT", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 100, FeeMicros: money.FromUSDT(0.1), ReceiveTime: time.Now()})
	out, _ := r.ApplyFill(Fill{Symbol: "BTC-USDT", TradeID: "t2", Side: SideBuy, Qty: 1, Price: 100, FeeMicros: money.FromUSDT(0.2), ReceiveTime: time.Now()})
	assertApprox(t, "累计手续费", out.Position.FeesMicros.USDT(), 0.3, 1e-9)
}
