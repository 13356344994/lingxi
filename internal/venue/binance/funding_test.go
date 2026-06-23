package binance

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
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

func TestBinanceFetchRealizedFunding_ParseSum(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantCount int
		wantSum   float64
	}{
		{
			name:      "received and paid net",
			payload:   `[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"1.25","asset":"USDT","time":1700000000000},{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"-0.40","asset":"USDT","time":1700000300000}]`,
			wantCount: 2,
			wantSum:   0.85,
		},
		{
			name:      "filters non funding income and other symbols",
			payload:   `[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"2.00","asset":"USDT","time":1700000000000},{"symbol":"BTCUSDT","incomeType":"COMMISSION","income":"-0.10","asset":"USDT","time":1700000000000},{"symbol":"ETHUSDT","incomeType":"FUNDING_FEE","income":"99","asset":"USDT","time":1700000000000}]`,
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
			a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, PathFuturesIncome) {
					http.NotFound(w, r)
					return
				}
				gotQuery = r.URL.Query()
				_, _ = w.Write([]byte(c.payload))
			})
			since := time.UnixMilli(1700000000000).Add(-2 * time.Hour)
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
					t.Fatalf("time must be parsed from ms, got zero")
				}
			}
			// 必带签名鉴权参数 + FUNDING_FEE 过滤 + 原始符号。
			if gotQuery.Get("incomeType") != "FUNDING_FEE" {
				t.Fatalf("incomeType=%q", gotQuery.Get("incomeType"))
			}
			if gotQuery.Get("symbol") != "BTCUSDT" {
				t.Fatalf("symbol=%q want BTCUSDT", gotQuery.Get("symbol"))
			}
			if gotQuery.Get("signature") == "" || gotQuery.Get("timestamp") == "" {
				t.Fatalf("missing signed query params: %v", gotQuery)
			}
		})
	}
}

func TestBinanceFetchRealizedFunding_PropagatesError(t *testing.T) {
	a := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":-1021,"msg":"timestamp"}`, http.StatusBadRequest)
	})
	if _, err := a.FetchRealizedFunding(context.Background(), "BTC-USDT", time.Time{}); err == nil {
		t.Fatal("expected error on non-200 response (must not silently report zero funding)")
	}
}
