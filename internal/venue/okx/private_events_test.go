package okx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestBuildPrivateLoginArgSignatureMatchesReferenceComputation(t *testing.T) {
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	secret := "test-secret-key"
	arg := BuildPrivateLoginArg("api-key-1", secret, "pass-1", at)

	if arg.Timestamp != "1783771200" {
		t.Fatalf("timestamp 应为该时刻的 Unix 秒, got %q", arg.Timestamp)
	}
	wantMsg := arg.Timestamp + "GET" + "/users/self/verify"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(wantMsg))
	wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if arg.Sign != wantSign {
		t.Fatalf("签名不匹配独立计算结果: got %q want %q", arg.Sign, wantSign)
	}
	if arg.APIKey != "api-key-1" || arg.Passphrase != "pass-1" {
		t.Fatalf("apiKey/passphrase 应原样传递, got %+v", arg)
	}
}

func TestBuildPrivateLoginFrameJSONShape(t *testing.T) {
	raw, err := BuildPrivateLoginFrame("k", "s", "p", time.Now())
	if err != nil {
		t.Fatalf("构造登录帧失败: %v", err)
	}
	var decoded struct {
		Op   string `json:"op"`
		Args []struct {
			APIKey     string `json:"apiKey"`
			Passphrase string `json:"passphrase"`
			Timestamp  string `json:"timestamp"`
			Sign       string `json:"sign"`
		} `json:"args"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("解析登录帧失败: %v", err)
	}
	if decoded.Op != "login" {
		t.Fatalf("op 应为 login, got %q", decoded.Op)
	}
	if len(decoded.Args) != 1 || decoded.Args[0].APIKey != "k" || decoded.Args[0].Sign == "" {
		t.Fatalf("登录帧内容不符: %+v", decoded)
	}
}

func TestBuildOrdersSubscribeFrameDefaultsToAny(t *testing.T) {
	raw, err := BuildOrdersSubscribeFrame("")
	if err != nil {
		t.Fatalf("构造订阅帧失败: %v", err)
	}
	var decoded PrivateSubscribeFrame
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("解析订阅帧失败: %v", err)
	}
	if decoded.Op != "subscribe" || len(decoded.Args) != 1 {
		t.Fatalf("订阅帧结构不符: %+v", decoded)
	}
	if decoded.Args[0].Channel != "orders" || decoded.Args[0].InstType != "ANY" {
		t.Fatalf("channel/instType 默认值不符: %+v", decoded.Args[0])
	}

	raw2, err := BuildOrdersSubscribeFrame("SWAP")
	if err != nil {
		t.Fatalf("构造订阅帧失败: %v", err)
	}
	var decoded2 PrivateSubscribeFrame
	_ = json.Unmarshal(raw2, &decoded2)
	if decoded2.Args[0].InstType != "SWAP" {
		t.Fatalf("显式 instType 应被保留, got %q", decoded2.Args[0].InstType)
	}
}

func TestBuildAndParsePositionsFixture(t *testing.T) {
	rawSub, err := BuildPositionsSubscribeFrame("")
	if err != nil {
		t.Fatalf("构造 positions 订阅帧失败: %v", err)
	}
	var sub PrivateSubscribeFrame
	if err := json.Unmarshal(rawSub, &sub); err != nil {
		t.Fatalf("解析订阅帧失败: %v", err)
	}
	if len(sub.Args) != 1 || sub.Args[0].Channel != "positions" || sub.Args[0].InstType != "ANY" {
		t.Fatalf("positions 订阅帧不符: %+v", sub)
	}

	push, err := ParsePrivatePositionsPush([]byte(`{
		"arg":{"channel":"positions","instType":"SWAP"},
		"data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","posSide":"short","pos":"3","avgPx":"100","markPx":"99","uTime":"1597026383085"}]
	}`))
	if err != nil {
		t.Fatalf("解析 positions fixture 失败: %v", err)
	}
	if len(push.Data) != 1 || push.Data[0].SignedQty(0.01) != -0.03 {
		t.Fatalf("short 3 张、ctVal .01 应归一为 -0.03 币, got %+v", push.Data)
	}
}

// ordersPushFixture 是按 OKX v5 官方文档字段手工构造的 orders 频道推送样例(非真实账户数据),
// 用于离线验证解析逻辑,不代表已连通真实网络。
const ordersPushFixture = `{
  "arg": {"channel": "orders", "instType": "SWAP"},
  "data": [{
    "instType": "SWAP",
    "instId": "BTC-USDT-SWAP",
    "ordId": "680800019903107072",
    "clOrdId": "abc123",
    "ordType": "limit",
    "side": "buy",
    "state": "partially_filled",
    "px": "50000",
    "sz": "10",
    "avgPx": "50000",
    "accFillSz": "4",
    "fillSz": "4",
    "fillPx": "50000",
    "fillPnl": "0",
    "tradeId": "trade-001",
    "feeCcy": "USDT",
    "fee": "-0.02",
    "reduceOnly": "false",
    "category": "normal",
    "code": "0",
    "msg": "",
    "uTime": "1597026383085",
    "cTime": "1597026383085"
  }]
}`

func TestParsePrivateOrdersPushFixture(t *testing.T) {
	push, err := ParsePrivateOrdersPush([]byte(ordersPushFixture))
	if err != nil {
		t.Fatalf("解析 fixture 失败: %v", err)
	}
	if push.Arg.Channel != "orders" || push.Arg.InstType != "SWAP" {
		t.Fatalf("arg 解析不符: %+v", push.Arg)
	}
	if len(push.Data) != 1 {
		t.Fatalf("data 应有 1 条, got %d", len(push.Data))
	}
	d := push.Data[0]
	if d.OrdID != "680800019903107072" || d.ClOrdID != "abc123" || d.State != "partially_filled" {
		t.Fatalf("订单字段解析不符: %+v", d)
	}
	if !d.HasFill() || d.FillTradeID() != "trade-001" {
		t.Fatalf("应识别出携带成交, got HasFill=%v tradeId=%q", d.HasFill(), d.FillTradeID())
	}
	if d.IsUnknownState() {
		t.Fatal("partially_filled 应是已知状态")
	}
}

func TestParsePrivateOrdersPushRejectsWrongChannel(t *testing.T) {
	raw := `{"arg":{"channel":"positions"},"data":[]}`
	if _, err := ParsePrivateOrdersPush([]byte(raw)); err == nil {
		t.Fatal("非 orders 频道应报错")
	}
}

func TestPrivateOrderDataToOrderResultSwapCtValConversion(t *testing.T) {
	d := PrivateOrderData{
		ClOrdID: "cli1", OrdID: "ord1", State: "filled",
		AccFillSz: "3", AvgPx: "100", FeeCcy: "USDT", Fee: "-0.5",
	}
	result := d.ToOrderResult("BTC-USDT", venue.MarketPerp, 0.01) // ctVal=0.01,3 张 = 0.03 币
	if result.ExecutedQty != 0.03 {
		t.Fatalf("SWAP 张数应按 ctVal 换算成币数量, got %v", result.ExecutedQty)
	}
	if result.Status != "filled" {
		t.Fatalf("状态应归一化为 filled, got %q", result.Status)
	}
	if result.Fee != 0.5 {
		t.Fatalf("手续费应取绝对值, got %v", result.Fee)
	}
	if result.CumQuoteUSDT != result.ExecutedQty*result.AvgPrice {
		t.Fatalf("CumQuoteUSDT 应为 executed*avgPrice, got %v", result.CumQuoteUSDT)
	}
}

func TestPrivateOrderDataFillQtySpotNoCtValConversion(t *testing.T) {
	d := PrivateOrderData{FillSz: "2", FillPx: "10"}
	if qty := d.FillQty(venue.MarketSpot, 5); qty != 2 {
		t.Fatalf("现货不应乘 ctVal, got %v", qty)
	}
	if qty := d.FillQty(venue.MarketPerp, 5); qty != 10 {
		t.Fatalf("SWAP 应乘 ctVal, got %v", qty)
	}
	if d.FillPrice() != 10 {
		t.Fatalf("FillPrice 应为 10, got %v", d.FillPrice())
	}
}

func TestPrivateOrderDataToExecutionEvents(t *testing.T) {
	receivedAt := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	d := PrivateOrderData{
		InstID: "BTC-USDT-SWAP", ClOrdID: "cli1", OrdID: "ord1",
		State: "partially_filled", Side: "sell", AccFillSz: "4",
		FillSz: "1", FillPx: "50000", TradeID: "trade1", UTime: "1597026383085",
	}
	events := d.ToExecutionEvents("BTC-USDT", venue.MarketPerp, 0.01, receivedAt)
	if len(events) != 2 || events[0].Order == nil || events[1].Fill == nil {
		t.Fatalf("应拆为 order+fill 两条 execution 事件, got %+v", events)
	}
	if events[0].Order.ExecutedQty != 0.04 {
		t.Fatalf("累计成交 4 张应换算为 0.04 币, got %v", events[0].Order.ExecutedQty)
	}
	if events[1].Fill.Qty != 0.01 || events[1].Fill.Side != "sell" || events[1].Fill.TradeID != "trade1" {
		t.Fatalf("fill 翻译不符: %+v", events[1].Fill)
	}
}

func TestPrivateOrderDataIsUnknownStateFlagsUnrecognizedValues(t *testing.T) {
	known := PrivateOrderData{State: "live"}
	if known.IsUnknownState() {
		t.Fatal("live 应是已知状态")
	}
	unknown := PrivateOrderData{State: "some_future_state_v99"}
	if !unknown.IsUnknownState() {
		t.Fatal("未识别的 state 应标记为 unknown,不能静默归一化")
	}
}

func TestDialLivePrivateStreamFailsClosedWithoutDialerOrCredentials(t *testing.T) {
	_, err := DialLivePrivateStream(context.Background(), nil, PrivateStreamConfig{})
	if err == nil {
		t.Fatal("缺少 dialer/凭证必须 fail-closed")
	}
}
