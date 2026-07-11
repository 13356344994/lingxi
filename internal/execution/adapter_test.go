package execution

import "testing"

func TestFixtureEventSourceDeterministicReplay(t *testing.T) {
	events := []PrivateEvent{
		{Fill: &Fill{Symbol: "BTC-USDT", TradeID: "t1", Side: SideBuy, Qty: 1, Price: 1}},
		{Fill: &Fill{Symbol: "BTC-USDT", TradeID: "t2", Side: SideBuy, Qty: 1, Price: 1}},
	}
	src := NewFixtureEventSource(events)
	count := 0
	for range src.Events() {
		count++
	}
	if count != len(events) {
		t.Fatalf("应回放全部 %d 条事件, got %d", len(events), count)
	}
	if err := src.Err(); err != nil {
		t.Fatalf("确定性 fixture 源不应有错误: %v", err)
	}
	if src.Closed() {
		t.Fatal("未调用 Close 前 Closed() 应为 false")
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close 不应报错: %v", err)
	}
	if !src.Closed() {
		t.Fatal("调用 Close 后 Closed() 应为 true")
	}
}
