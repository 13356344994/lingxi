package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"lingxi/internal/exchange"
)

func TestShadowShouldBlockSignedRequest(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		method  string
		want    bool
	}{
		{"disabled passes POST", false, http.MethodPost, false},
		{"disabled passes GET", false, http.MethodGet, false},
		{"enabled blocks POST", true, http.MethodPost, true},
		{"enabled blocks DELETE", true, http.MethodDelete, true},
		{"enabled blocks PUT", true, http.MethodPut, true},
		{"enabled allows GET", true, http.MethodGet, false},
		{"enabled allows lowercase get", true, "get", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shadowShouldBlockSignedRequest(tc.enabled, tc.method); got != tc.want {
				t.Fatalf("shadowShouldBlockSignedRequest(%v,%q)=%v want %v", tc.enabled, tc.method, got, tc.want)
			}
		})
	}
}

func TestIsShadowBlockedError(t *testing.T) {
	if !isShadowBlockedError(shadowBlockedError{method: "POST", endpoint: "/fapi/v1/order"}) {
		t.Fatal("expected sentinel to be recognized")
	}
	if isShadowBlockedError(errors.New("real exchange error")) {
		t.Fatal("non-shadow error must not be recognized as shadow blocked")
	}
	if isShadowBlockedError(nil) {
		t.Fatal("nil must not be recognized as shadow blocked")
	}
}

// TestSignedRequestHardBlocksOrdersInShadowMode 钉死「无泄漏」核心保证：影子模式下下单(POST)在
// signedRequest 咽喉点即被拦截、返回哨兵错误，请求绝不触达交易所；关闭影子模式则照常发到交易所。
// 用真实 httptest server + 命中计数器直接证明「请求是否真的发出去了」。
func TestSignedRequestHardBlocksOrdersInShadowMode(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	newEngine := func(shadow bool) *FundingRateSniperEngine {
		cfg := DefaultEngineConfig()
		cfg.ExecutionConfig.ShadowTrading = shadow
		return &FundingRateSniperEngine{
			client: exchange.NewClient(exchange.ClientConfig{
				FuturesBaseURL: server.URL,
				APIKey:         "demo-api-key",
				SecretKey:      "demo-secret-key",
				HTTPClient:     server.Client(),
			}),
			cfg:    cfg,
			logger: log.New(io.Discard, "", 0),
		}
	}

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("side", "SELL")
	params.Set("quantity", "0.01")
	endpoint := server.URL + "/fapi/v1/order"

	// 影子开启：必须被哨兵拦截，且 server 命中数保持为 0（证明请求没发出去）。
	shadowEngine := newEngine(true)
	if _, err := shadowEngine.signedRequest(context.Background(), http.MethodPost, endpoint, params); !isShadowBlockedError(err) {
		t.Fatalf("expected shadow-blocked sentinel for POST order, got %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 0 {
		t.Fatalf("影子模式下下单请求绝不能触达交易所，但 server 被命中 %d 次", got)
	}

	// 影子关闭：照常发到交易所，server 命中且不是 shadow-blocked。
	liveEngine := newEngine(false)
	if _, err := liveEngine.signedRequest(context.Background(), http.MethodPost, endpoint, params); isShadowBlockedError(err) {
		t.Fatal("影子模式关闭时不应出现 shadow-blocked 错误")
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("影子关闭时下单请求应正常发出，期望 server 命中 1 次，实际 %d", got)
	}
}
