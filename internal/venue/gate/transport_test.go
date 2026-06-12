package gate

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// TestHashBodySHA512Empty GET 无 body 时对空串做 SHA512(Gate 用 SHA512 而非 SHA256)。
func TestHashBodySHA512Empty(t *testing.T) {
	want := "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
	if got := hashBodySHA512(nil); got != want {
		t.Fatalf("空 body SHA512: got %q want %q", got, want)
	}
}

// TestSignaturePayloadAssembly 校验 Gate 待签名串五段换行拼装。
func TestSignaturePayloadAssembly(t *testing.T) {
	bodyHash := hashBodySHA512(nil)
	got := signaturePayload("GET", "/api/v4/spot/accounts", "", bodyHash, "1541993715")
	want := "GET\n/api/v4/spot/accounts\n\n" + bodyHash + "\n1541993715"
	if got != want {
		t.Fatalf("GET 待签名串错误:\n got=%q\nwant=%q", got, want)
	}
	postBody := `{"contract":"HOME_USDT","size":-2}`
	postHash := hashBodySHA512([]byte(postBody))
	gotPost := signaturePayload("POST", pathFuturesOrders, "", postHash, "1541993715")
	wantPost := "POST\n" + pathFuturesOrders + "\n\n" + postHash + "\n1541993715"
	if gotPost != wantPost {
		t.Fatalf("POST 待签名串错误:\n got=%q\nwant=%q", gotPost, wantPost)
	}
	gotQ := signaturePayload("GET", pathFuturesOrders, "contract=BTC_USDT", bodyHash, "1541993715")
	wantQ := "GET\n" + pathFuturesOrders + "\ncontract=BTC_USDT\n" + bodyHash + "\n1541993715"
	if gotQ != wantQ {
		t.Fatalf("GET+query 待签名串错误:\n got=%q\nwant=%q", gotQ, wantQ)
	}
}

func hmacSHA512Hex(secret, payload string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestSignedGetHeadersAndDeterminism 用 httptest 校验签名 GET 头与手算 SIGN 一致。
func TestSignedGetHeadersAndDeterminism(t *testing.T) {
	var gotHeaders http.Header
	var gotPath string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "MYKEY", "MYSECRET")
	fixed := time.Unix(1541993715, 0).UTC()
	tr.nowFn = func() time.Time { return fixed }

	q := url.Values{}
	q.Set("contract", "BTC_USDT")
	if err := tr.signedGet(context.Background(), pathFuturesOrders, q, nil); err != nil {
		t.Fatalf("signedGet 失败: %v", err)
	}
	ts := "1541993715"
	if gotHeaders.Get("KEY") != "MYKEY" {
		t.Errorf("KEY 错误: %q", gotHeaders.Get("KEY"))
	}
	if gotHeaders.Get("Timestamp") != ts {
		t.Errorf("Timestamp 错误: got %q want %q", gotHeaders.Get("Timestamp"), ts)
	}
	bodyHash := hashBodySHA512(nil)
	wantSign := hmacSHA512Hex("MYSECRET", signaturePayload("GET", gotPath, gotQuery, bodyHash, ts))
	if gotHeaders.Get("SIGN") != wantSign {
		t.Errorf("SIGN 不匹配:\n got=%s\nwant=%s", gotHeaders.Get("SIGN"), wantSign)
	}
}

// TestSignedPostHeadersAndDeterminism 校验签名 POST:body 字节与签名串逐字一致。
func TestSignedPostHeadersAndDeterminism(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "MYKEY", "MYSECRET")
	fixed := time.Unix(1541993715, 0).UTC()
	tr.nowFn = func() time.Time { return fixed }

	body := map[string]any{"contract": "HOME_USDT", "size": -2, "price": "0", "tif": "ioc"}
	if err := tr.signedPost(context.Background(), pathFuturesOrders, nil, body, nil); err != nil {
		t.Fatalf("signedPost 失败: %v", err)
	}
	ts := "1541993715"
	bodyHash := hashBodySHA512(gotBody)
	wantSign := hmacSHA512Hex("MYSECRET", signaturePayload("POST", pathFuturesOrders, "", bodyHash, ts))
	if gotHeaders.Get("SIGN") != wantSign {
		t.Errorf("POST SIGN 不匹配:\n got=%s\nwant=%s\nbody=%s", gotHeaders.Get("SIGN"), wantSign, gotBody)
	}
}

// TestAPIErrorParse 校验非 2xx 时 label/message 被解析进错误。
func TestAPIErrorParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"label":"BALANCE_NOT_ENOUGH","message":"balance not enough"}`))
	}))
	defer srv.Close()
	tr := newTransport(srv.URL, "K", "S")
	err := tr.publicGet(context.Background(), pathSpotTime, nil, nil)
	if err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
	if !strings.Contains(err.Error(), "label=BALANCE_NOT_ENOUGH") || !strings.Contains(err.Error(), "balance not enough") {
		t.Fatalf("错误应含 label 与 message: %v", err)
	}
}

func TestClassifyGateLabels(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		err     error
		wantCat string
		wantSev string
	}{
		{&apiError{Label: "INVALID_KEY", Message: "bad key"}, "api_key_error", "critical"},
		{&apiError{Label: "TOO_MANY_REQUESTS", Message: "rate"}, "order_rate_limit", "major"},
		{&apiError{Label: "BALANCE_NOT_ENOUGH", Message: "no money"}, "platform_reject", "major"},
		{&apiError{Label: "OTHER", Message: "x"}, "request_error", "minor"},
	}
	for _, tc := range cases {
		cat, sev := a.Classify(tc.err)
		if cat != tc.wantCat || sev != tc.wantSev {
			t.Errorf("Classify(%v): got (%q,%q) want (%q,%q)", tc.err, cat, sev, tc.wantCat, tc.wantSev)
		}
	}
}

func TestArmDeadMansSwitchRequest(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.ArmDeadMansSwitch(context.Background(), 60*time.Second); err != nil {
		t.Fatalf("ArmDeadMansSwitch: %v", err)
	}
	var sent struct {
		Timeout int `json:"timeout"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("解析 body: %v", err)
	}
	if sent.Timeout != 60 {
		t.Errorf("timeout 映射错误: got %d want 60", sent.Timeout)
	}
}

func TestArmDeadMansSwitchClamp(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	a, _ := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err := a.ArmDeadMansSwitch(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("ArmDeadMansSwitch: %v", err)
	}
	var sent struct {
		Timeout int `json:"timeout"`
	}
	_ = json.Unmarshal(gotBody, &sent)
	if sent.Timeout != 10 {
		t.Errorf("小于 10s 应夹到 10, got %d", sent.Timeout)
	}
}
