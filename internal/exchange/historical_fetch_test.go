package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchFundingRateHistoryWithRetryRetriesAcceptedResponse(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/fapi/v1/fundingRate") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"msg":"accepted"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","fundingRate":"0.0010","fundingTime":1717113600000}]`))
	}))
	defer server.Close()

	client := &BinanceClient{
		spotBaseURL:               "https://spot.local",
		futuresBaseURL:            "https://futures.local",
		httpClient:                server.Client(),
		historicalFundingRetryMax: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	items, err := client.fetchFundingRateHistoryWithRetry(ctx, server.URL+"/fapi/v1/fundingRate?symbol=BTCUSDT&startTime=1717110000000&endTime=1717117200000&limit=1000")
	if err != nil {
		t.Fatalf("fetchFundingRateHistoryWithRetry() error = %v", err)
	}
	if len(items) != 1 || items[0].FundingRate != "0.0010" {
		t.Fatalf("unexpected funding items: %+v", items)
	}
	if calls != 2 {
		t.Fatalf("expected one retry after accepted response, got calls=%d", calls)
	}
}

func TestLoadCachedFundingRateHistoryUsesHistoricalPayloadCache(t *testing.T) {
	cache, err := NewHistoricalPayloadCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewHistoricalPayloadCache() error = %v", err)
	}
	client := &BinanceClient{
		futuresBaseURL:         "https://futures.local",
		historicalPayloadCache: cache,
	}
	endpoint := client.fundingRateHistoryEndpoint("BTCUSDT", 1717110000000, 1717117200000)
	if err := client.storeHistoricalPayload(endpoint, []byte(`[{"symbol":"BTCUSDT","fundingRate":"0.0010","fundingTime":1717113600000}]`)); err != nil {
		t.Fatalf("storeHistoricalPayload() error = %v", err)
	}

	items, ok, err := client.loadCachedFundingRateHistory("BTCUSDT", 1717110000000, 1717117200000)
	if err != nil {
		t.Fatalf("loadCachedFundingRateHistory() error = %v", err)
	}
	if !ok {
		t.Fatal("expected cached funding history to be loaded")
	}
	if len(items) != 1 || items[0].Symbol != "BTCUSDT" {
		t.Fatalf("unexpected cached items: %+v", items)
	}
}
