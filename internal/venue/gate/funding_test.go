package gate

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func sumFundingIncome(in []venue.FundingIncome) float64 {
	var total float64
	for _, it := range in {
		total += it.Income
	}
	return total
}

func fundingFakeGate(t *testing.T, payload string, gotQuery *url.Values) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != pathFuturesAccountBook {
			http.Error(w, "no route "+r.URL.Path, http.StatusNotFound)
			return
		}
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestGateFetchRealizedFunding_ParseSum(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantCount int
		wantSum   float64
	}{
		{
			name:      "received and paid net",
			payload:   `[{"time":1700000000.5,"change":"1.25","type":"fund","contract":"BTC_USDT","text":"BTC_USDT"},{"time":1700000300,"change":"-0.40","type":"fund","contract":"BTC_USDT","text":"BTC_USDT"}]`,
			wantCount: 2,
			wantSum:   0.85,
		},
		{
			name:      "filters non fund rows",
			payload:   `[{"time":1700000000,"change":"2.00","type":"fund","contract":"BTC_USDT"},{"time":1700000000,"change":"-9.99","type":"fee","contract":"BTC_USDT"},{"time":1700000000,"change":"-9.99","type":"pnl","contract":"BTC_USDT"}]`,
			wantCount: 1,
			wantSum:   2.0,
		},
		{
			name:      "empty window returns zero not error",
			payload:   `[]`,
			wantCount: 0,
			wantSum:   0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotQuery url.Values
			a := fundingFakeGate(t, c.payload, &gotQuery)
			since := time.Unix(1700000000, 0).Add(-2 * time.Hour)
			got, err := a.FetchRealizedFunding(context.Background(), "BTC-USDT", since)
			if err != nil {
				t.Fatalf("FetchRealizedFunding: %v", err)
			}
			if len(got) != c.wantCount {
				t.Fatalf("entries=%d want %d (%+v)", len(got), c.wantCount, got)
			}
			if math.Abs(sumFundingIncome(got)-c.wantSum) > 1e-9 {
				t.Fatalf("sum=%v want %v", sumFundingIncome(got), c.wantSum)
			}
			for _, it := range got {
				if it.Symbol != "BTC-USDT" {
					t.Fatalf("symbol=%q want canonical BTC-USDT", it.Symbol)
				}
				if it.Asset != "USDT" {
					t.Fatalf("asset=%q want USDT", it.Asset)
				}
				if it.Time.IsZero() {
					t.Fatalf("time must be parsed from unix seconds, got zero")
				}
			}
			if gotQuery.Get("type") != "fund" {
				t.Fatalf("type=%q want fund", gotQuery.Get("type"))
			}
			if gotQuery.Get("contract") != "BTC_USDT" {
				t.Fatalf("contract=%q want BTC_USDT", gotQuery.Get("contract"))
			}
		})
	}
}

func TestGateFetchRealizedFunding_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"label":"BALANCE_NOT_ENOUGH"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	a, err := New(venue.Config{Name: VenueName, APIKey: "K", APISecret: "S", Extra: map[string]string{"base_url": srv.URL}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.FetchRealizedFunding(context.Background(), "BTC-USDT", time.Time{}); err == nil {
		t.Fatal("expected error on non-2xx response (must not silently report zero funding)")
	}
}
