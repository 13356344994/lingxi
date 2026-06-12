package okx

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

func TestQtyToContractSz(t *testing.T) {
	sz, err := qtyToContractSz(250, 100, 1)
	if err != nil {
		t.Fatalf("qtyToContractSz: %v", err)
	}
	if sz != "2" {
		t.Errorf("250 coin / ctVal100 → 2 张, got sz=%q", sz)
	}
}

func TestSanitizeClOrdID(t *testing.T) {
	got := sanitizeClOrdID("order-uuid-1234-ABCD")
	want := "orderuuid1234ABCD"
	if got != want {
		t.Errorf("clOrdId 净化错误: got %q want %q", got, want)
	}
	long := sanitizeClOrdID("abcdefghijklmnopqrstuvwxyz0123456789EXTRA")
	if len(long) != 32 {
		t.Errorf("clOrdId 应截断到 32: len=%d", len(long))
	}
}

func TestBuildPlaceOrderBodySpotMarketBuyTgtCcy(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathInstruments: `{"code":"0","msg":"","data":[{"instId":"BTC-USDT","instType":"SPOT","baseCcy":"BTC","quoteCcy":"USDT","state":"live","tickSz":"0.1","lotSz":"0.0001","minSz":"0.0001","ctVal":"","ctValCcy":""}]}`,
	})
	body, err := a.buildPlaceOrderBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.01, ClientOrderID: "cid-1",
	})
	if err != nil {
		t.Fatalf("buildPlaceOrderBody: %v", err)
	}
	if body.TgtCcy != "base_ccy" {
		t.Errorf("现货市价买单应带 tgtCcy=base_ccy, got %q", body.TgtCcy)
	}
	if body.TdMode != "cash" {
		t.Errorf("现货 tdMode 应为 cash, got %q", body.TdMode)
	}
	if body.ClOrdID != "cid1" {
		t.Errorf("clOrdId 应净化连字符: got %q", body.ClOrdID)
	}
}

func TestBuildPlaceOrderBodyPerpContractSz(t *testing.T) {
	a := fakeOKX(t, map[string]string{
		pathInstruments: `{"code":"0","msg":"","data":[{"instId":"HOME-USDT-SWAP","instType":"SWAP","baseCcy":"HOME","quoteCcy":"USDT","state":"live","tickSz":"0.0001","lotSz":"1","minSz":"1","ctVal":"100","ctValCcy":"HOME"}]}`,
	})
	body, err := a.buildPlaceOrderBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "HOME-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 250,
	})
	if err != nil {
		t.Fatalf("buildPlaceOrderBody: %v", err)
	}
	if body.Sz != "2" {
		t.Errorf("250 HOME / ctVal100 → 2 张, got sz=%q", body.Sz)
	}
	if body.TdMode != "cross" {
		t.Errorf("永续 tdMode 应为 cross, got %q", body.TdMode)
	}
}

func TestArmDeadMansSwitchRequest(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []any{}})
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Passphrase: "P", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.ArmDeadMansSwitch(context.Background(), 60*time.Second); err != nil {
		t.Fatalf("ArmDeadMansSwitch: %v", err)
	}
	var sent struct {
		TimeOut string `json:"timeOut"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("解析 body: %v", err)
	}
	if sent.TimeOut != "60" {
		t.Errorf("timeOut 映射错误: got %q want 60", sent.TimeOut)
	}
}

func TestArmDeadMansSwitchClamp(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []any{}})
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Passphrase: "P", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.ArmDeadMansSwitch(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("ArmDeadMansSwitch: %v", err)
	}
	var sent struct {
		TimeOut string `json:"timeOut"`
	}
	_ = json.Unmarshal(gotBody, &sent)
	if sent.TimeOut != "10" {
		t.Errorf("小于 10s 应夹到 10, got %q", sent.TimeOut)
	}
}

func TestPlaceOrderSignedPOST(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		switch r.URL.Path {
		case pathInstruments:
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","baseCcy":"BTC","quoteCcy":"USDT","state":"live","tickSz":"0.1","lotSz":"0.001","minSz":"0.001","ctVal":"0.01","ctValCcy":"BTC"}]}`))
		case pathPlaceOrder:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "0", "msg": "",
				"data": []map[string]string{{"ordId": "OID-1", "clOrdId": "CID1"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Passphrase: "P", Extra: map[string]string{"base_url": srv.URL}})
	a.t.nowFn = func() time.Time { return time.UnixMilli(1700000000000).UTC() }

	res, err := a.PlaceOrder(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp, Symbol: "BTC-USDT", Side: venue.SideBuy,
		Type: venue.OrderTypeMarket, Qty: 0.05, ClientOrderID: "CID-1",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if res.ExchangeOrderID != "OID-1" || res.Status != "new" {
		t.Errorf("回执解析错误: %+v", res)
	}
	if gotHeaders.Get("OK-ACCESS-KEY") != "K" || gotHeaders.Get("OK-ACCESS-PASSPHRASE") != "P" {
		t.Errorf("签名头缺失: %v", gotHeaders)
	}
}
