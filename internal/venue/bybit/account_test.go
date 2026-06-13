package bybit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lingxi/internal/venue"
)

func fakeBybitAccount(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{
		Name:      VenueName,
		APIKey:    "KEY",
		APISecret: "SECRET",
		Extra:     map[string]string{"base_url": srv.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestFetchBorrowQuoteParseSuccess(t *testing.T) {
	a := fakeBybitAccount(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, pathCollateralInfo):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retCode": 0,
				"result": map[string]any{
					"list": []map[string]any{{
						"currency":         "H",
						"borrowable":       true,
						"hourlyBorrowRate": "0.0001",
					}},
				},
			})
		case strings.Contains(r.URL.Path, pathSpotBorrowCheck):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retCode": 0,
				"result":  map[string]any{"maxTradeQty": "5000"},
			})
		default:
			http.NotFound(w, r)
		}
	})
	q, err := a.FetchBorrowQuote(context.Background(), "H")
	if err != nil {
		t.Fatalf("FetchBorrowQuote: %v", err)
	}
	if q.Asset != "H" || !q.Available || q.MaxBorrowable != 5000 || q.HourlyRate != 0.0001 {
		t.Fatalf("解析错误: %+v", q)
	}
}

func TestFetchBorrowQuotePropagatesError(t *testing.T) {
	a := fakeBybitAccount(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"retCode":10001,"retMsg":"boom"}`))
	})
	if _, err := a.FetchBorrowQuote(context.Background(), "H"); err == nil {
		t.Fatal("应传递上游错误")
	}
}
