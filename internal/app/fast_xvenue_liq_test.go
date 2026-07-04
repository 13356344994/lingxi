package app

import (
	"testing"
	"time"
)

// TestParseBybitAllLiquidation 钉死 allLiquidation 解析:方向映射(订单 Sell=多头被强平)、
// symbol 规范化、side_raw 原样保留(映射约定若被实证推翻可重放修正)、非 USDT/无效量拒绝。
func TestParseBybitAllLiquidation(t *testing.T) {
	now := time.Now().UTC()
	// 多头被强平(强平单 Sell);单消息多条 data
	// 映射按 2026-07-04 实证规则:Bybit S=仓位方向(Buy=多头被爆);17,318 对两所重叠事件锚定。
	evs := parseBybitAllLiquidation([]byte(`{"topic":"allLiquidation.BTCUSDT","type":"snapshot","ts":1783100000000,"data":[
		{"T":1783100000001,"s":"BTCUSDT","S":"Buy","v":"0.014","p":"95892.9"},
		{"T":1783100000002,"s":"BTCUSDT","S":"Sell","v":"0.5","p":"95900.1"}]}`), now)
	if len(evs) != 2 {
		t.Fatalf("应解析出 2 条,得到 %d", len(evs))
	}
	if evs[0].Symbol != "BTC-USDT" || evs[0].RawSymbol != "BTCUSDT" || evs[0].PosSide != "long" || evs[0].SideRaw != "Buy" {
		t.Fatalf("多头爆映射错: %+v", evs[0])
	}
	if evs[0].Qty != 0.014 || evs[0].Price != 95892.9 || evs[0].ExchTs != 1783100000001 || evs[0].Venue != "bybit" {
		t.Fatalf("数值解析错: %+v", evs[0])
	}
	if evs[1].PosSide != "short" || evs[1].SideRaw != "Sell" {
		t.Fatalf("空头爆映射错: %+v", evs[1])
	}
	// 1000 前缀币规范化原样保留前缀
	evs2 := parseBybitAllLiquidation([]byte(`{"topic":"allLiquidation.1000PEPEUSDT","ts":1,"data":[{"T":2,"s":"1000PEPEUSDT","S":"Buy","v":"5000","p":"0.028"}]}`), now)
	if len(evs2) != 1 || evs2[0].Symbol != "1000PEPE-USDT" {
		t.Fatalf("1000前缀规范化错: %+v", evs2)
	}
	// 非 allLiquidation 主题拒绝(订阅确认/心跳等)
	if got := parseBybitAllLiquidation([]byte(`{"op":"pong"}`), now); got != nil {
		t.Fatal("非行情消息应返回 nil")
	}
	if got := parseBybitAllLiquidation([]byte(`{"topic":"publicTrade.BTCUSDT","data":[]}`), now); got != nil {
		t.Fatal("非 allLiquidation 主题应返回 nil")
	}
	// 非 USDT 本位/零量拒绝
	if got := parseBybitAllLiquidation([]byte(`{"topic":"allLiquidation.BTCPERP","ts":1,"data":[{"T":1,"s":"BTCPERP","S":"Sell","v":"1","p":"1"}]}`), now); len(got) != 0 {
		t.Fatal("非 USDT 本位应拒绝")
	}
	if got := parseBybitAllLiquidation([]byte(`{"topic":"allLiquidation.BTCUSDT","ts":1,"data":[{"T":1,"s":"BTCUSDT","S":"Sell","v":"0","p":"1"}]}`), now); len(got) != 0 {
		t.Fatal("零量应拒绝")
	}
}

// TestFastLiqFollowShadowGate v5 停火语义:live 关 + shadow 开 → 评估管线继续但来源闸拒实盘;
// 此处验证 fastLiveOpen 侧的硬闸行为(fast 实盘永远只在 OKX,别所数据只做信号研究)。
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
