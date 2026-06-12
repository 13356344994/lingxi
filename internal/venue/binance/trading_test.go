package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

func newHarness(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL:    srv.URL,
		FuturesBaseURL: srv.URL,
		APIKey:         "test-key",
		SecretKey:      "test-secret",
		HTTPClient:     srv.Client(),
	})
	a := New(client)
	a.nowFn = func() time.Time { return time.UnixMilli(1700000000000).UTC() }
	return a
}

func futuresExchangeInfoJSON() string {
	return `{"symbols":[{"symbol":"BTCUSDT","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","quantityPrecision":3,"filters":[{"filterType":"PRICE_FILTER","tickSize":"0.10"},{"filterType":"LOT_SIZE","stepSize":"0.001"},{"filterType":"NOTIONAL","notional":"5"}]}]}`
}

func TestBuildOrderParamsQtyRoundingAndFlags(t *testing.T) {
	var gotQuery url.Values
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "exchangeInfo") {
			_, _ = w.Write([]byte(futuresExchangeInfoJSON()))
			return
		}
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"orderId":1,"clientOrderId":"cid","status":"NEW"}`))
	})

	longID := strings.Repeat("a", 40) + "-ok"
	_, err := a.PlaceOrder(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.0019, ReduceOnly: true, ClientOrderID: longID,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if gotQuery.Get("quantity") != "0.001" {
		t.Fatalf("quantity 应向下取整到 stepSize 0.001, got %q", gotQuery.Get("quantity"))
	}
	if gotQuery.Get("reduceOnly") != "true" {
		t.Fatalf("reduceOnly 应为 true, got %q", gotQuery.Get("reduceOnly"))
	}
	coid := gotQuery.Get("newClientOrderId")
	if len(coid) != maxClientOrderID {
		t.Fatalf("newClientOrderId 应截断至 %d 字符, got len=%d val=%q", maxClientOrderID, len(coid), coid)
	}
}

// TestBuildOrderParamsSpotPostOnlyUsesLimitMaker 钉死现货 post-only 的订单类型映射:
// 现货不支持 timeInForce=GTX(仅永续合法),必须用独立类型 LIMIT_MAKER 且不带 TIF,
// 否则现货 post-only 单会被 API 直接拒绝;永续 post-only 仍是 LIMIT+GTX。
func TestBuildOrderParamsSpotPostOnlyUsesLimitMaker(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(futuresExchangeInfoJSON()))
	})
	spot, err := a.buildOrderParams(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypePostOnly, Qty: 0.01, Price: 60000,
	})
	if err != nil {
		t.Fatalf("buildOrderParams(spot post-only): %v", err)
	}
	if spot.Get("type") != "LIMIT_MAKER" || spot.Get("timeInForce") != "" || spot.Get("price") == "" {
		t.Fatalf("现货 post-only 应为 LIMIT_MAKER 无 TIF 带价格, got type=%q tif=%q price=%q",
			spot.Get("type"), spot.Get("timeInForce"), spot.Get("price"))
	}
	perp, err := a.buildOrderParams(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypePostOnly, Qty: 0.01, Price: 60000,
	})
	if err != nil {
		t.Fatalf("buildOrderParams(perp post-only): %v", err)
	}
	if perp.Get("type") != "LIMIT" || perp.Get("timeInForce") != "GTX" {
		t.Fatalf("永续 post-only 应为 LIMIT+GTX, got type=%q tif=%q", perp.Get("type"), perp.Get("timeInForce"))
	}
}

func TestPlaceOrderPerpMarketSignedPOST(t *testing.T) {
	var method, apiKey, sig string
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "exchangeInfo") {
			_, _ = w.Write([]byte(futuresExchangeInfoJSON()))
			return
		}
		method = r.Method
		apiKey = r.Header.Get("X-MBX-APIKEY")
		sig = r.URL.Query().Get("signature")
		_, _ = w.Write([]byte(`{"orderId":99,"clientOrderId":"CID-1","status":"NEW","symbol":"BTCUSDT"}`))
	})
	res, err := a.PlaceOrder(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideSell,
		Type: venue.OrderTypeMarket, Qty: 0.5, ClientOrderID: "CID-1",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if method != http.MethodPost || apiKey != "test-key" || sig == "" {
		t.Fatalf("签名 POST 头/参数缺失: method=%s key=%s sig=%q", method, apiKey, sig)
	}
	if res.ExchangeOrderID != "99" || res.Status != "new" {
		t.Errorf("回执解析错误: %+v", res)
	}
}

func TestQueryOrderStatusNormalization(t *testing.T) {
	cases := []struct {
		name   string
		market venue.Market
		want   string
		body   string
	}{
		{
			name: "perp_partially_filled", market: venue.MarketPerp, want: "partially_filled",
			body: `{"orderId":1,"clientOrderId":"c1","status":"PARTIALLY_FILLED","executedQty":"0.1","avgPrice":"60000","cumQuote":"6000","updateTime":1700000000000}`,
		},
		{
			name: "spot_canceled", market: venue.MarketSpot, want: "canceled",
			body: `{"orderId":2,"clientOrderId":"c2","status":"CANCELED","executedQty":"0","cummulativeQuoteQty":"0","transactTime":1700000000000}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			})
			res, err := a.QueryOrder(context.Background(), c.market, "BTC-USDT", "c1")
			if err != nil {
				t.Fatalf("QueryOrder: %v", err)
			}
			if res.Status != c.want {
				t.Fatalf("status=%q want %q", res.Status, c.want)
			}
			if c.market == venue.MarketPerp && res.Fee != 0 {
				t.Fatalf("合约查单 Fee 应为 0(无手续费字段), got %v", res.Fee)
			}
		})
	}
}

func TestSpotQueryOrderFeeFromFills(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"orderId":3,"clientOrderId":"c3","status":"FILLED","executedQty":"1","cummulativeQuoteQty":"60000","transactTime":1700000000000,
			"fills":[{"price":"60000","qty":"1","commission":"0.001","commissionAsset":"BNB"}]
		}`))
	})
	res, err := a.QueryOrder(context.Background(), venue.MarketSpot, "BTC-USDT", "c3")
	if err != nil {
		t.Fatalf("QueryOrder: %v", err)
	}
	if res.Fee != 0.001 || res.FeeAsset != "BNB" || res.Status != "filled" {
		t.Fatalf("现货 fills 手续费解析错误: %+v", res)
	}
}

func TestSetMarginModeNoChangeIgnored(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":-4046,"msg":"No need to change margin type."}`))
	})
	if err := a.SetMarginMode(context.Background(), "BTC-USDT", venue.MarginModeCross); err != nil {
		t.Fatalf("SetMarginMode -4046 应视为成功: %v", err)
	}
}

func TestSetLeveragePOST(t *testing.T) {
	var path, lev string
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		lev = r.URL.Query().Get("leverage")
		w.WriteHeader(http.StatusOK)
	})
	if err := a.SetLeverage(context.Background(), "BTC-USDT", 5); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	if path != PathFuturesLeverage || lev != "5" {
		t.Fatalf("leverage 参数错误 path=%s lev=%s", path, lev)
	}
}

func TestCancelOrderDelete(t *testing.T) {
	var method string
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		if r.URL.Query().Get("origClientOrderId") != "CID-X" {
			t.Fatalf("origClientOrderId=%q", r.URL.Query().Get("origClientOrderId"))
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := a.CancelOrder(context.Background(), venue.MarketPerp, "BTC-USDT", "CID-X"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if method != http.MethodDelete {
		t.Fatalf("撤单应为 DELETE, got %s", method)
	}
}

func TestNormalizeVenueOrderStatus(t *testing.T) {
	if normalizeVenueOrderStatus("NEW") != "new" || normalizeVenueOrderStatus("CANCELED") != "canceled" {
		t.Fatal("状态归一错误")
	}
}

func TestSanitizeClientOrderID(t *testing.T) {
	got := sanitizeClientOrderID("abc@123_" + strings.Repeat("x", 50))
	if len(got) > maxClientOrderID {
		t.Fatalf("未截断: len=%d", len(got))
	}
	if strings.Contains(got, "@") {
		t.Fatal("非法字符应剔除")
	}
}

func TestBuildOrderParamsRejectsBadInput(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(futuresExchangeInfoJSON()))
	})
	if _, err := a.buildOrderParams(context.Background(), venue.OrderRequest{Market: venue.MarketPerp, Symbol: "BTC-USDT", Type: venue.OrderTypeMarket, Qty: 0}); err == nil {
		t.Error("数量为 0 应报错")
	}
}

func TestPlaceBatchStillUnsupported(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := a.PlaceBatch(context.Background(), nil); err != venue.ErrUnsupported {
		t.Fatalf("PlaceBatch 应 ErrUnsupported, got %v", err)
	}
}

func TestBinanceOrderTypeTIF(t *testing.T) {
	typ, tif, err := binanceOrderTypeTIF(venue.OrderTypeIOC)
	if err != nil || typ != "LIMIT" || tif != "IOC" {
		t.Fatalf("IOC 映射错误: %s %s %v", typ, tif, err)
	}
}
