package app

import (
	"testing"
	"time"
)

// TestParseBinanceForceOrder 钉死 forceOrder 解析:方向映射(SELL=多头爆)、symbol 规范化、
// 大小写不敏感 e/E 污染防护(fast_peer 踩过的坑)、非 USDT/无效量拒绝。
func TestParseBinanceForceOrder(t *testing.T) {
	now := time.Now().UTC()
	// 多头被强平(强平单 SELL)
	ev := parseBinanceForceOrder([]byte(`{"e":"forceOrder","E":1783100000000,"o":{"s":"BTCUSDT","S":"SELL","q":"0.014","p":"9910","ap":"9905.5","T":1783100000001}}`), now)
	if ev == nil {
		t.Fatal("合法 forceOrder 不应返回 nil")
	}
	if ev.Symbol != "BTC-USDT" || ev.RawSymbol != "BTCUSDT" || ev.PosSide != "long" || ev.Side != "sell" {
		t.Fatalf("字段映射错: %+v", ev)
	}
	if ev.Price != 9905.5 || ev.Qty != 0.014 || ev.ExchTs != 1783100000001 {
		t.Fatalf("数值解析错: %+v", ev)
	}
	// 空头被强平(BUY);ap 缺失回退 p
	ev2 := parseBinanceForceOrder([]byte(`{"e":"forceOrder","E":1,"o":{"s":"1000SHIBUSDT","S":"BUY","q":"5000","p":"0.028","T":2}}`), now)
	if ev2 == nil || ev2.PosSide != "short" || ev2.Side != "buy" || ev2.Symbol != "1000SHIB-USDT" || ev2.Price != 0.028 {
		t.Fatalf("空头爆映射错: %+v", ev2)
	}
	// 非 forceOrder 事件拒绝
	if parseBinanceForceOrder([]byte(`{"e":"aggTrade","E":1,"o":{"s":"BTCUSDT","S":"SELL","q":"1"}}`), now) != nil {
		t.Fatal("非 forceOrder 应拒绝")
	}
	// 非 USDT 本位拒绝
	if parseBinanceForceOrder([]byte(`{"e":"forceOrder","E":1,"o":{"s":"BTCUSD_PERP","S":"SELL","q":"1","p":"1","T":1}}`), now) != nil {
		t.Fatal("非 USDT 本位应拒绝")
	}
	// 零量拒绝
	if parseBinanceForceOrder([]byte(`{"e":"forceOrder","E":1,"o":{"s":"BTCUSDT","S":"SELL","q":"0","p":"1","T":1}}`), now) != nil {
		t.Fatal("零量应拒绝")
	}
}

// TestFastLiqFollowShadowGate v5 停火语义:live 关 + shadow 开 → 评估管线继续但来源闸拒实盘;
// live/shadow 双关 → 循环跳过(此处验证 fastLiveOpen 侧的硬闸行为)。
func TestFastLiqFollowShadowGate(t *testing.T) {
	e := newFastV3TestEngine(t)
	e.cfg.FastLiveEnabled = true
	e.cfg.FastLiqFollowLiveEnabled = false
	e.cfg.FastLiqFollowShadowEnabled = true
	ev := &fastExecShadowEvent{Symbol: "SHDW-USDT", Mid: 1, Bid: 0.999, Ask: 1.001,
		CandidateSource: "liq_follow", FastCandidateSide: "short", PondOK: true, PondSamples: 9999}
	if r := e.fastLiveOpen(nil, ev); r != "record_only_source" {
		t.Fatalf("停火后 liq_follow 必须被来源闸拒为 record_only_source,得到 %s", r)
	}
}
