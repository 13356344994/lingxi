package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

func TestFetchNetExternalFundFlowUSDT_DepositMinusWithdraw(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, PathCapitalDepositHisrec):
			_, _ = w.Write([]byte(`[{"amount":"100","coin":"USDT","status":1}]`))
		case strings.Contains(r.URL.Path, PathCapitalWithdrawHistory):
			_, _ = w.Write([]byte(`[{"amount":"49.45","coin":"USDT","status":6}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL: srv.URL,
		APIKey:      "k",
		SecretKey:   "s",
		HTTPClient:  srv.Client(),
	})
	a := New(client)
	a.nowFn = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	since := a.nowFn().Add(-2 * time.Hour)
	net, err := a.FetchNetExternalFundFlowUSDT(context.Background(), since)
	if err != nil {
		t.Fatalf("FetchNetExternalFundFlowUSDT: %v", err)
	}
	want := 100.0 - 49.45
	if net != want {
		t.Fatalf("net=%v want %v", net, want)
	}
}

func TestFetchNetExternalFundFlowUSDT_IgnoresNonStableCoin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, PathCapitalDepositHisrec):
			_, _ = w.Write([]byte(`[{"amount":"1","coin":"BTC","status":1}]`))
		case strings.Contains(r.URL.Path, PathCapitalWithdrawHistory):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := exchange.NewClient(exchange.ClientConfig{
		SpotBaseURL: srv.URL,
		APIKey:      "k",
		SecretKey:   "s",
		HTTPClient:  srv.Client(),
	})
	a := New(client)
	a.nowFn = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	net, err := a.FetchNetExternalFundFlowUSDT(context.Background(), a.nowFn().Add(-time.Hour))
	if err != nil {
		t.Fatalf("FetchNetExternalFundFlowUSDT: %v", err)
	}
	if net != 0 {
		t.Fatalf("non-stable deposit should be ignored, net=%v", net)
	}
}
