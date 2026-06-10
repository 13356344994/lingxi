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
//
//	→ f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8
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

// TestSyncServerTimeAdjustsSignTimestamp 钉死时钟偏移自愈(PROJECT_AUDIT C-5):
// 服务器时间领先本地 10s 时,SyncServerTime 后签名时间戳应叠加该偏移。
func TestSyncServerTimeAdjustsSignTimestamp(t *testing.T) {
	fixedLocal := time.UnixMilli(1_700_000_000_000).UTC()
	serverMs := fixedLocal.UnixMilli() + 10_000 // 服务器快 10s
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/time" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"retCode": 0, "retMsg": "OK",
			"result": map[string]string{
				"timeSecond": "1700000010",
				"timeNano":   "1700000010000000000",
			},
		})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "K", "S")
	tr.nowFn = func() time.Time { return fixedLocal }
	offset, err := tr.syncServerTime(context.Background())
	if err != nil {
		t.Fatalf("syncServerTime() error = %v", err)
	}
	if offset != 10*time.Second {
		t.Fatalf("偏移计算错误: 期望 10s, 实际 %s", offset)
	}
	if got := tr.signTimestampMs(); got != "1700000010000" {
		t.Fatalf("签名时间戳未叠加偏移: %s(期望 %d)", got, serverMs)
	}
}

// TestDoRetriesOn5xx 钉死 GET 瞬时故障重试(PROJECT_AUDIT C-6):前两次 500,第三次成功。
func TestDoRetriesOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]string{}})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "", "")
	if err := tr.publicGet(context.Background(), "/v5/test", nil, nil); err != nil {
		t.Fatalf("第三次应成功, 实际 err=%v", err)
	}
	if attempts != 3 {
		t.Fatalf("期望重试到第 3 次, 实际 %d 次", attempts)
	}
}

// TestDoDoesNotRetryBusinessError 业务错误(retCode≠0 且非限频)不应重试。
func TestDoDoesNotRetryBusinessError(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		_ = json.NewEncoder(w).Encode(map[string]any{"retCode": 170137, "retMsg": "too many decimals"})
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, "", "")
	if err := tr.publicGet(context.Background(), "/v5/test", nil, nil); err == nil {
		t.Fatal("业务错误应上抛")
	}
	if attempts != 1 {
		t.Fatalf("业务错误不应重试, 实际请求 %d 次", attempts)
	}
}
