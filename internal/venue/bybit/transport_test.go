package bybit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestSignStringKnownVector 用业界公认的 HMAC-SHA256 测试向量校验签名底层正确:
// key="key", msg="The quick brown fox jumps over the lazy dog"
//   → f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8
func TestSignStringKnownVector(t *testing.T) {
	got := signString("key", "The quick brown fox jumps over the lazy dog")
	want := "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got != want {
		t.Fatalf("signString 向量不匹配:\n got=%s\nwant=%s", got, want)
	}
}

// TestSignaturePayloadAssembly 校验 v5 GET 待签名串拼装顺序:timestamp+apiKey+recvWindow+queryString。
func TestSignaturePayloadAssembly(t *testing.T) {
	got := signaturePayload("1658384314791", "XXXXXXXXXX", "5000", "category=option&symbol=BTC-29JUL22-25000-C")
	want := "1658384314791XXXXXXXXXX5000category=option&symbol=BTC-29JUL22-25000-C"
	if got != want {
		t.Fatalf("待签名串拼装错误:\n got=%s\nwant=%s", got, want)
	}
}

// TestSignedGetHeadersAndDeterminism 用 httptest 校验签名 GET 携带全部 X-BAPI 头,
// 且签名 = HMAC(secret, ts+key+recv+qs)(用固定时间戳保证可复现)。
func TestSignedGetHeadersAndDeterminism(t *testing.T) {
	var gotHeaders http.Header
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{}})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "MYKEY", "MYSECRET")
	fixed := time.UnixMilli(1700000000000).UTC()
	tr.nowFn = func() time.Time { return fixed }

	q := url.Values{}
	q.Set("accountType", "UNIFIED")
	if err := tr.signedGet(context.Background(), "/v5/account/wallet-balance", q, nil); err != nil {
		t.Fatalf("signedGet 失败: %v", err)
	}

	if gotHeaders.Get("X-BAPI-API-KEY") != "MYKEY" {
		t.Errorf("X-BAPI-API-KEY 缺失/错误: %q", gotHeaders.Get("X-BAPI-API-KEY"))
	}
	if gotHeaders.Get("X-BAPI-SIGN-TYPE") != "2" {
		t.Errorf("X-BAPI-SIGN-TYPE 应为 2, got %q", gotHeaders.Get("X-BAPI-SIGN-TYPE"))
	}
	if gotHeaders.Get("X-BAPI-TIMESTAMP") != "1700000000000" {
		t.Errorf("X-BAPI-TIMESTAMP 错误: %q", gotHeaders.Get("X-BAPI-TIMESTAMP"))
	}
	if gotHeaders.Get("X-BAPI-RECV-WINDOW") != defaultRecvWindow {
		t.Errorf("X-BAPI-RECV-WINDOW 错误: %q", gotHeaders.Get("X-BAPI-RECV-WINDOW"))
	}
	// 复算期望签名:必须与实际发出的 query 串严格一致。
	wantSign := signString("MYSECRET", signaturePayload("1700000000000", "MYKEY", defaultRecvWindow, gotQuery))
	if gotHeaders.Get("X-BAPI-SIGN") != wantSign {
		t.Errorf("X-BAPI-SIGN 不匹配:\n got=%s\nwant=%s\nquery=%s", gotHeaders.Get("X-BAPI-SIGN"), wantSign, gotQuery)
	}
}

// TestDoRetCodeError 校验非零 retCode 被识别为错误。
func TestDoRetCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 10003, "retMsg": "API key invalid", "result": map[string]any{}})
	}))
	defer srv.Close()
	tr := newTransport(srv.URL, "", "")
	err := tr.publicGet(context.Background(), "/v5/market/time", nil, nil)
	if err == nil {
		t.Fatalf("retCode!=0 应返回错误")
	}
}
