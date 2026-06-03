package exchange

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestSignIsDeterministicHMACSHA256(t *testing.T) {
	// 已知向量：HMAC-SHA256("query", key="secret")
	got := Sign("secret", "query")
	want := "cb92cef40efaad2dcb30b787cfa23fdc45f680d8d6092d4965f80a9eb201b11b"
	if got != want {
		t.Fatalf("Sign mismatch:\n got=%s\nwant=%s", got, want)
	}
	if Sign("secret", "query") != got {
		t.Fatal("Sign must be deterministic")
	}
	if Sign("other", "query") == got {
		t.Fatal("different secret must produce different signature")
	}
}

func TestClassifyException(t *testing.T) {
	cases := []struct {
		err          error
		wantCategory string
		wantSeverity string
	}{
		{nil, "", ""},
		{errors.New("code -1015 too many new orders"), "order_rate_limit", "major"},
		{errors.New("HTTP 418 banned"), "platform_reject", "critical"},
		{errors.New("403 forbidden"), "platform_reject", "critical"},
		{errors.New("429 Too Many Requests"), "platform_reject", "major"},
		{errors.New("context deadline exceeded"), "request_timeout", "minor"},
		{errors.New("签名请求失败: dial tcp"), "request_error", "major"},
		{errors.New("something else"), "request_error", "minor"},
	}
	for _, c := range cases {
		gotCat, gotSev := ClassifyException(c.err)
		if gotCat != c.wantCategory || gotSev != c.wantSeverity {
			t.Fatalf("ClassifyException(%v) = (%q,%q), want (%q,%q)", c.err, gotCat, gotSev, c.wantCategory, c.wantSeverity)
		}
	}
}

func TestIsSignedOrderEndpoint(t *testing.T) {
	signed := []string{
		"/api/v3/order",
		"/sapi/v1/margin/order",
		"/fapi/v1/order",
		"/fapi/v1/batchOrders",
	}
	for _, e := range signed {
		if !IsSignedOrderEndpoint(e) {
			t.Fatalf("expected %q to be a signed order endpoint", e)
		}
	}
	if IsSignedOrderEndpoint("/fapi/v1/premiumIndex") {
		t.Fatal("market data endpoint must not be a signed order endpoint")
	}
}

func TestSignedOrderRequestUnits(t *testing.T) {
	if got := SignedOrderRequestUnits(http.MethodGet, "/fapi/v1/order", nil); got != 0 {
		t.Fatalf("non-POST must cost 0, got %d", got)
	}
	if got := SignedOrderRequestUnits(http.MethodPost, "/fapi/v1/premiumIndex", nil); got != 0 {
		t.Fatalf("non-order POST must cost 0, got %d", got)
	}
	if got := SignedOrderRequestUnits(http.MethodPost, "/fapi/v1/order", nil); got != 1 {
		t.Fatalf("single order must cost 1, got %d", got)
	}
	batch := url.Values{}
	batch.Set("batchOrders", `[{"symbol":"BTCUSDT"},{"symbol":"ETHUSDT"},{"symbol":"BNBUSDT"}]`)
	if got := SignedOrderRequestUnits(http.MethodPost, "/fapi/v1/batchOrders", batch); got != 3 {
		t.Fatalf("batch of 3 must cost 3, got %d", got)
	}
	bad := url.Values{}
	bad.Set("batchOrders", "not-json")
	if got := SignedOrderRequestUnits(http.MethodPost, "/fapi/v1/batchOrders", bad); got != 1 {
		t.Fatalf("unparseable batch must conservatively cost 1, got %d", got)
	}
	empty := url.Values{}
	empty.Set("batchOrders", "[]")
	if got := SignedOrderRequestUnits(http.MethodPost, "/fapi/v1/batchOrders", empty); got != 0 {
		t.Fatalf("empty batch must cost 0, got %d", got)
	}
}
