package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitForDirectionalSpotOrderWithBlindDeadlineSupportsMarginFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sapi/v1/margin/order" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"symbol":"BTCUSDT",
			"orderId":123456,
			"clientOrderId":"cid-margin",
			"status":"FILLED",
			"executedQty":"1.25",
			"cummulativeQuoteQty":"125",
			"transactTime":1700000000000
		}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			apiKey:      "demo-api-key",
			secretKey:   "demo-secret-key",
			httpClient:  server.Client(),
		},
		cfg:            DefaultEngineConfig(),
		logger:         log.New(io.Discard, "", 0),
		marginOrderHub: NewOrderEventHub(),
	}

	snapshot, err := engine.waitForDirectionalSpotOrderWithBlindDeadline(context.Background(), "BTCUSDT", "cid-margin", spotExecutionVenueCrossMargin)
	if err != nil {
		t.Fatalf("waitForDirectionalSpotOrderWithBlindDeadline() error = %v", err)
	}
	if snapshot.OrderID != 123456 {
		t.Fatalf("expected order id 123456, got %d", snapshot.OrderID)
	}
	if !snapshot.HasConfirmedExecution() || !snapshot.IsClosed() {
		t.Fatalf("expected confirmed closed snapshot, got %+v", snapshot)
	}
}

func TestObserveDirectionalSpotOrderUntilTerminalUpdatesMarginHub(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sapi/v1/margin/order" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"symbol":"BTCUSDT",
			"orderId":88,
			"clientOrderId":"cid-margin-observe",
			"status":"FILLED",
			"executedQty":"2",
			"cummulativeQuoteQty":"200",
			"transactTime":1700000000000
		}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			apiKey:      "demo-api-key",
			secretKey:   "demo-secret-key",
			httpClient:  server.Client(),
		},
		cfg:            DefaultEngineConfig(),
		logger:         log.New(io.Discard, "", 0),
		marginOrderHub: NewOrderEventHub(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine.observeDirectionalSpotOrderUntilTerminal(ctx, "BTCUSDT", "cid-margin-observe", spotExecutionVenueCrossMargin)

	snapshot, ok := engine.marginOrderHub.Get("cid-margin-observe")
	if !ok {
		t.Fatal("expected margin order hub snapshot")
	}
	if snapshot.OrderID != 88 || !snapshot.HasConfirmedExecution() || !snapshot.IsClosed() {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}
