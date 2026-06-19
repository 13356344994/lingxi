package bybit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestBuildCreateBodyMappings(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		name      string
		req       venue.OrderRequest
		wantType  string
		wantTIF   string
		wantPrice string
		wantSide  string
		wantCat   string
		wantMU    string
	}{
		{
			name:     "perp market buy",
			req:      venue.OrderRequest{Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy, Type: venue.OrderTypeMarket, Qty: 0.5},
			wantType: "Market", wantTIF: "", wantPrice: "", wantSide: "Buy", wantCat: categoryLinear, wantMU: "",
		},
		{
			name:     "perp IOC sell with price",
			req:      venue.OrderRequest{Market: venue.MarketPerp, Symbol: "ETH-USDT", Side: venue.SideSell, Type: venue.OrderTypeIOC, Qty: 2, Price: 3000},
			wantType: "Limit", wantTIF: "IOC", wantPrice: "3000", wantSide: "Sell", wantCat: categoryLinear, wantMU: "",
		},
		{
			name:     "spot market buy uses baseCoin unit",
			req:      venue.OrderRequest{Market: venue.MarketSpot, Symbol: "BTC-USDT", Side: venue.SideBuy, Type: venue.OrderTypeMarket, Qty: 0.01},
			wantType: "Market", wantTIF: "", wantPrice: "", wantSide: "Buy", wantCat: categorySpot, wantMU: "baseCoin",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := a.buildCreateBody(context.Background(), c.req)
			if err != nil {
				t.Fatalf("buildCreateBody: %v", err)
			}
			if b.OrderType != c.wantType || b.TimeInForce != c.wantTIF || b.Price != c.wantPrice ||
				b.Side != c.wantSide || b.Category != c.wantCat || b.MarketUnit != c.wantMU {
				t.Errorf("映射错误: %+v", b)
			}
		})
	}
}

func TestBuildCreateBodyRejectsBadInput(t *testing.T) {
	a := &Adapter{}
	if _, err := a.buildCreateBody(context.Background(), venue.OrderRequest{Market: venue.MarketPerp, Symbol: "BTC-USDT", Type: venue.OrderTypeMarket, Qty: 0}); err == nil {
		t.Error("数量为 0 应报错")
	}
	if _, err := a.buildCreateBody(context.Background(), venue.OrderRequest{Market: venue.MarketPerp, Symbol: "BTC-USDT", Type: venue.OrderTypeIOC, Qty: 1, Price: 0}); err == nil {
		t.Error("限价单缺价格应报错")
	}
}

// TestPlaceOrderSignedPOST 用 httptest 校验下单走签名 POST:头齐全、签名 = HMAC(secret, ts+key+recv+body),
// 且请求体与返回解析正确。
func TestPlaceOrderSignedPOST(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"retCode": 0, "retMsg": "OK",
			"result": map[string]any{"orderId": "OID-1", "orderLinkId": "CID-1"},
		})
	}))
	defer srv.Close()

	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	a.t.nowFn = func() time.Time { return time.UnixMilli(1700000000000).UTC() }

	res, err := a.PlaceOrder(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeIOC, Qty: 0.5, Price: 60000, ClientOrderID: "CID-1",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if res.ExchangeOrderID != "OID-1" || res.ClientOrderID != "CID-1" || res.Status != "new" {
		t.Errorf("回执解析错误: %+v", res)
	}
	if gotHeaders.Get("X-BAPI-SIGN-TYPE") != "2" || gotHeaders.Get("X-BAPI-API-KEY") != "K" {
		t.Errorf("签名头缺失: %v", gotHeaders)
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("POST 应为 JSON: %q", gotHeaders.Get("Content-Type"))
	}
	wantSign := signString("S", signaturePayload("1700000000000", "K", defaultRecvWindow, string(gotBody)))
	if gotHeaders.Get("X-BAPI-SIGN") != wantSign {
		t.Errorf("POST 签名不匹配:\n got=%s\nwant=%s\nbody=%s", gotHeaders.Get("X-BAPI-SIGN"), wantSign, gotBody)
	}
	// body 应含关键字段。
	var sent bybitCreateOrderBody
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("解析发出的 body: %v", err)
	}
	if sent.Symbol != "BTCUSDT" || sent.Side != "Buy" || sent.OrderType != "Limit" || sent.TimeInForce != "IOC" || sent.Price != "60000" {
		t.Errorf("发出的 body 不符: %+v", sent)
	}
}

// TestCancelAllOrders 钉死启动清扫的请求映射:linear 必带 settleCoin=USDT(v5 强制三选一),
// spot 不带 settleCoin;返回撤单数 = result.list 长度。
func TestCancelAllOrders(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"retCode": 0, "retMsg": "OK",
			"result": map[string]any{"list": []map[string]any{{"orderId": "O1"}, {"orderId": "O2"}}},
		})
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})

	n, err := a.CancelAllOrders(context.Background(), venue.MarketPerp)
	if err != nil || n != 2 {
		t.Fatalf("CancelAllOrders(perp): n=%d err=%v", n, err)
	}
	var sentPerp struct {
		Category   string `json:"category"`
		SettleCoin string `json:"settleCoin"`
	}
	_ = json.Unmarshal(gotBody, &sentPerp)
	if sentPerp.Category != categoryLinear || sentPerp.SettleCoin != "USDT" {
		t.Errorf("perp 清扫应 category=linear settleCoin=USDT, got %+v", sentPerp)
	}

	if _, err := a.CancelAllOrders(context.Background(), venue.MarketSpot); err != nil {
		t.Fatalf("CancelAllOrders(spot): %v", err)
	}
	var sentSpot struct {
		Category   string `json:"category"`
		SettleCoin string `json:"settleCoin"`
	}
	_ = json.Unmarshal(gotBody, &sentSpot)
	if sentSpot.Category != categorySpot || sentSpot.SettleCoin != "" {
		t.Errorf("spot 清扫应 category=spot 且不带 settleCoin, got %+v", sentSpot)
	}
}

func TestQueryOrderParse(t *testing.T) {
	a := fakeBybit(t, map[string]string{
		pathOrderRealtime: `{"retCode":0,"retMsg":"OK","result":{"list":[
			{"orderId":"OID-9","orderLinkId":"CID-9","orderStatus":"PartiallyFilled","cumExecQty":"0.3","avgPrice":"59990","cumExecValue":"17997"}
		]}}`,
	})
	res, err := a.QueryOrder(context.Background(), venue.MarketPerp, "BTC-USDT", "CID-9")
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	if res.Status != "partially_filled" || res.ExecutedQty != 0.3 || res.AvgPrice != 59990 || res.CumQuoteUSDT != 17997 {
		t.Errorf("查单解析错误: %+v", res)
	}
}

// TestQueryOrderSpotFeeAssetInference 钉死现货 feeCurrency 为空时按方向推断计费币种:
// Bybit 规则是市价 BUY 的 taker 费在 base 资产里扣、SELL 在 quote 里扣;feeCurrency 实测常为空串,
// 不推断会让上层把 base 计价的手续费当 USDT 入账(盈亏失真两个数量级)。
func TestQueryOrderSpotFeeAssetInference(t *testing.T) {
	cases := []struct {
		name      string
		side      string
		feeCur    string
		market    venue.Market
		wantAsset string
	}{
		{"现货买入费币种为空→推断为 base", "Buy", "", venue.MarketSpot, "MBOX"},
		{"现货卖出费币种为空→推断为 quote", "Sell", "", venue.MarketSpot, "USDT"},
		{"现货已带费币种→保持原值", "Buy", "MBOX", venue.MarketSpot, "MBOX"},
		{"永续费币种为空→不推断", "Buy", "", venue.MarketPerp, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := fakeBybit(t, map[string]string{
				pathOrderRealtime: `{"retCode":0,"retMsg":"OK","result":{"list":[
					{"orderId":"OID-1","orderLinkId":"CID-1","orderStatus":"Filled","side":"` + tc.side + `","cumExecQty":"6266","avgPrice":"0.00479","cumExecValue":"30.01","cumExecFee":"6.266","feeCurrency":"` + tc.feeCur + `"}
				]}}`,
			})
			res, err := a.QueryOrder(context.Background(), tc.market, "MBOX-USDT", "CID-1")
			if err != nil {
				t.Fatalf("QueryOrder: %v", err)
			}
			if res.FeeAsset != tc.wantAsset {
				t.Errorf("FeeAsset 推断错误: got %q want %q", res.FeeAsset, tc.wantAsset)
			}
		})
	}
}

func TestCancelOrderSigned(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.Method != http.MethodPost {
			t.Errorf("撤单应为 POST, got %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"orderId": "OID-1"}})
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.CancelOrder(context.Background(), venue.MarketPerp, "BTC-USDT", "CID-1"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !hit {
		t.Error("撤单未发出")
	}
}

func TestBatchSameCategoryEnforced(t *testing.T) {
	a := &Adapter{}
	_, err := a.PlaceBatch(context.Background(), []venue.OrderRequest{
		{Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy, Type: venue.OrderTypeMarket, Qty: 1},
		{Market: venue.MarketSpot, Symbol: "ETH-USDT", Side: venue.SideBuy, Type: venue.OrderTypeMarket, Qty: 1},
	})
	if err == nil {
		t.Error("混合 spot/linear 批量应报错")
	}
}

func TestNormalizeOrderStatus(t *testing.T) {
	cases := map[string]string{
		"Filled": "filled", "PartiallyFilled": "partially_filled",
		"Cancelled": "canceled", "Rejected": "rejected", "New": "new",
	}
	for in, want := range cases {
		if got := normalizeOrderStatus(in); got != want {
			t.Errorf("normalizeOrderStatus(%q)=%q want %q", in, got, want)
		}
	}
}
