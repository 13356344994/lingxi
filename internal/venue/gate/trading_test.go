package gate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestQtyToSignedContractSize(t *testing.T) {
	n, err := qtyToSignedContractSize(venue.SideSell, 250, 100)
	if err != nil {
		t.Fatalf("qtyToSignedContractSize: %v", err)
	}
	if n != -2 {
		t.Errorf("Side=Sell Qty=250 multiplier=100 → size=-2, got %d", n)
	}
	n2, err := qtyToSignedContractSize(venue.SideBuy, 250, 100)
	if err != nil || n2 != 2 {
		t.Errorf("Buy 250/100 → 2, got %d err=%v", n2, err)
	}
	if _, err := qtyToSignedContractSize(venue.SideBuy, 50, 100); err == nil {
		t.Error("向零取整为 0 应报错")
	}
}

func TestSanitizeOrderText(t *testing.T) {
	got := sanitizeOrderText("order-uuid-1234-ABCD")
	if !strings.HasPrefix(got, "t-") {
		t.Errorf("应带 t- 前缀: %q", got)
	}
	if len(got) > 2+28 {
		t.Errorf("t- 后应 ≤28 字符: len=%d %q", len(got), got)
	}
	long := sanitizeOrderText("abcdefghijklmnopqrstuvwxyz0123456789EXTRA")
	if len(long) > 30 {
		t.Errorf("应截断: len=%d", len(long))
	}
}

func TestBuildSpotOrderBodyMarketBuyRequiresPrice(t *testing.T) {
	a := fakeGate(t, map[string]string{})
	_, err := a.buildSpotOrderBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.01,
	})
	if err == nil {
		t.Fatal("现货市价买单缺价格应报错")
	}
}

func TestBuildSpotOrderBodyMarketBuyIOC(t *testing.T) {
	a := fakeGate(t, map[string]string{})
	body, err := a.buildSpotOrderBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.01, Price: 60000,
	})
	if err != nil {
		t.Fatalf("buildSpotOrderBody: %v", err)
	}
	if body.Type != "limit" || body.TimeInForce != "ioc" {
		t.Errorf("现货买单应走 IOC 限价: %+v", body)
	}
	if body.Amount != "0.01" || body.Price != "60000" {
		t.Errorf("amount/price 错误: %+v", body)
	}
}

func TestBuildFuturesOrderBodySignedSize(t *testing.T) {
	a := fakeGate(t, map[string]string{
		pathFuturesContracts + "/HOME_USDT": `{"name":"HOME_USDT","quanto_multiplier":"100","order_price_round":"0.0001","order_size_round":1}`,
	})
	body, err := a.buildFuturesOrderBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "HOME-USDT", Side: venue.SideSell,
		Type: venue.OrderTypeMarket, Qty: 250,
	})
	if err != nil {
		t.Fatalf("buildFuturesOrderBody: %v", err)
	}
	if body.Size != -2 {
		t.Errorf("250/100 sell → size=-2, got %d", body.Size)
	}
	if body.Price != "0" || body.TIF != "ioc" {
		t.Errorf("市价单 price=0 tif=ioc: %+v", body)
	}
}

func TestPlaceOrderSignedPOST(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		switch r.URL.Path {
		case pathFuturesContracts + "/BTC_USDT":
			_, _ = w.Write([]byte(`{"name":"BTC_USDT","quanto_multiplier":"0.001","order_price_round":"0.1","order_size_round":1}`))
		case pathFuturesOrders:
			_, _ = w.Write([]byte(`{"id":12345,"text":"t-CID1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	a.t.nowFn = func() time.Time { return time.Unix(1541993715, 0).UTC() }

	res, err := a.PlaceOrder(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.05, ClientOrderID: "CID-1",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if res.ExchangeOrderID != "12345" || res.Status != "new" {
		t.Errorf("回执解析错误: %+v", res)
	}
	if gotHeaders.Get("KEY") != "K" || gotHeaders.Get("SIGN") == "" {
		t.Errorf("签名头缺失: %v", gotHeaders)
	}
}

func TestArmDeadMansSwitchEndpoint(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		gotBody, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gotBody)
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.ArmDeadMansSwitch(context.Background(), 30*time.Second); err != nil {
		t.Fatalf("ArmDeadMansSwitch: %v", err)
	}
	if path != pathFuturesCountdownCancelAll {
		t.Errorf("dead-man 路径错误: %q", path)
	}
}
