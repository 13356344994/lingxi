package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

func TestWaitForSpotOrderWithBlindDeadlineReturnsBlindStateError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/order":
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"code":-1007,"msg":"timeout"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL: server.URL,
			APIKey:      "demo-api-key",
			SecretKey:   "demo-secret-key",
			HTTPClient:  server.Client(),
		}),
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
		spotOrderHub:    NewOrderEventHub(),
	}

	start := time.Now()
	_, err := engine.waitForSpotOrderWithBlindDeadline(context.Background(), "BTCUSDT", "cid-spot")
	if err == nil {
		t.Fatal("expected blind-state error")
	}
	var blindErr blindStateError
	if !errors.As(err, &blindErr) {
		t.Fatalf("expected blindStateError, got %T", err)
	}
	if elapsed := time.Since(start); elapsed < blindStateHardDeadline {
		t.Fatalf("expected wait to last at least blind hard deadline, got %s", elapsed)
	}
}

func TestWaitForSpotOrderWithBlindDeadlineFallsBackWhenOrderHubUnavailable(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
	}

	start := time.Now()
	snapshot, err := engine.waitForSpotOrderWithBlindDeadline(context.Background(), "BTCUSDT", "cid-spot")
	if err != nil {
		t.Fatalf("expected nil error without spot order hub, got %v", err)
	}
	if snapshot.OrderID != 0 {
		t.Fatalf("expected empty snapshot without spot order hub, got %+v", snapshot)
	}
	if elapsed := time.Since(start); elapsed >= blindStateHardDeadline {
		t.Fatalf("expected immediate fallback without spot order hub, got %s", elapsed)
	}
}
