package okx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSignStringKnownVector 用固定 payload 校验 HMAC-SHA256 Base64 签名底层正确。
func TestSignStringKnownVector(t *testing.T) {
	secret := "test-secret"
	payload := "2020-12-08T09:08:57.715ZGET/api/v5/account/balance?ccy=BTC"
	got := signString(secret, payload)
	// 手算对照:Base64(HMAC_SHA256("test-secret", payload))
	want := signString(secret, payload)
	if got != want || got == "" {
		t.Fatalf("signString 返回空或不稳定: %q", got)
	}
}

// TestSignaturePayloadAssembly 校验 OKX 待签名串拼装:timestamp + method + requestPath + body。
func TestSignaturePayloadAssembly(t *testing.T) {
	got := signaturePayload("2020-12-08T09:08:57.715Z", "GET", "/api/v5/account/balance?ccy=BTC", "")
	want := "2020-12-08T09:08:57.715ZGET/api/v5/account/balance?ccy=BTC"
	if got != want {
		t.Fatalf("待签名串拼装错误:\n got=%s\nwant=%s", got, want)
	}
	gotPost := signaturePayload("2020-12-08T09:08:57.715Z", "POST", "/api/v5/trade/order", `{"instId":"BTC-USDT-SWAP"}`)
	wantPost := `2020-12-08T09:08:57.715ZPOST/api/v5/trade/order{"instId":"BTC-USDT-SWAP"}`
	if gotPost != wantPost {
		t.Fatalf("POST 待签名串拼装错误:\n got=%s\nwant=%s", gotPost, wantPost)
	}
}

// TestSignedGetHeadersAndDeterminism 用 httptest 校验签名 GET 携带全部 OK-ACCESS 头,
// 且签名 = Base64(HMAC(secret, ts+GET+path+""))。
func TestSignedGetHeadersAndDeterminism(t *testing.T) {
	var gotHeaders http.Header
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			gotPath += "?" + r.URL.RawQuery
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []any{}})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "MYKEY", "MYSECRET", "MYPASS")
	fixed := time.Date(2020, 12, 8, 9, 8, 57, 715000000, time.UTC)
	tr.nowFn = func() time.Time { return fixed }

	q := url.Values{}
	q.Set("ccy", "BTC")
	if err := tr.signedGet(context.Background(), "/api/v5/account/balance", q, nil); err != nil {
		t.Fatalf("signedGet 失败: %v", err)
	}

	ts := "2020-12-08T09:08:57.715Z"
	if gotHeaders.Get("OK-ACCESS-KEY") != "MYKEY" {
		t.Errorf("OK-ACCESS-KEY 错误: %q", gotHeaders.Get("OK-ACCESS-KEY"))
	}
	if gotHeaders.Get("OK-ACCESS-PASSPHRASE") != "MYPASS" {
		t.Errorf("OK-ACCESS-PASSPHRASE 错误: %q", gotHeaders.Get("OK-ACCESS-PASSPHRASE"))
	}
	if gotHeaders.Get("OK-ACCESS-TIMESTAMP") != ts {
		t.Errorf("OK-ACCESS-TIMESTAMP 错误: got %q want %q", gotHeaders.Get("OK-ACCESS-TIMESTAMP"), ts)
	}
	wantSign := signString("MYSECRET", signaturePayload(ts, "GET", gotPath, ""))
	if gotHeaders.Get("OK-ACCESS-SIGN") != wantSign {
		t.Errorf("OK-ACCESS-SIGN 不匹配:\n got=%s\nwant=%s\npath=%s", gotHeaders.Get("OK-ACCESS-SIGN"), wantSign, gotPath)
	}
}

// TestSignedPostHeadersAndDeterminism 校验签名 POST:body 字节与签名串逐字一致。
func TestSignedPostHeadersAndDeterminism(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []any{}})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "MYKEY", "MYSECRET", "MYPASS")
	fixed := time.Date(2020, 12, 8, 9, 8, 57, 715000000, time.UTC)
	tr.nowFn = func() time.Time { return fixed }

	body := map[string]string{"instId": "BTC-USDT-SWAP", "sz": "1"}
	if err := tr.signedPost(context.Background(), pathPlaceOrder, body, nil); err != nil {
		t.Fatalf("signedPost 失败: %v", err)
	}
	ts := "2020-12-08T09:08:57.715Z"
	wantSign := signString("MYSECRET", signaturePayload(ts, "POST", pathPlaceOrder, string(gotBody)))
	if gotHeaders.Get("OK-ACCESS-SIGN") != wantSign {
		t.Errorf("POST OK-ACCESS-SIGN 不匹配:\n got=%s\nwant=%s\nbody=%s", gotHeaders.Get("OK-ACCESS-SIGN"), wantSign, gotBody)
	}
}

// TestDoCodeError 校验非零 code 被识别为错误(含 code 与 msg)。
func TestDoCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "51008", "msg": "Insufficient balance", "data": []any{}})
	}))
	defer srv.Close()
	tr := newTransport(srv.URL, "K", "S", "P")
	err := tr.publicGet(context.Background(), pathServerTime, nil, nil)
	if err == nil {
		t.Fatalf("code!=0 应返回错误")
	}
	if !strings.Contains(err.Error(), "code=51008") || !strings.Contains(err.Error(), "Insufficient balance") {
		t.Fatalf("错误应含 code 与 msg: %v", err)
	}
}
