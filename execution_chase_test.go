package main

import (
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlaceSpotChasingOrderWithOptionsHonorsAnchorAndHardLimit(t *testing.T) {
	prices := make([]float64, 0, executionChaseAttempts)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"90","askPrice":"90.1"}`))
		case "/api/v3/order":
			price := parseFloat(r.URL.Query().Get("price"))
			prices = append(prices, price)

			w.Header().Set("Content-Type", "application/json")
			if len(prices) < executionChaseAttempts {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"CANCELED","clientOrderId":"exp_sell-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":2,"status":"FILLED","clientOrderId":"exp_sell-2","executedQty":"1","cummulativeQuoteQty":"99.6","origQty":"1","fills":[{"price":"99.6","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			apiKey:      "demo-api-key",
			secretKey:   "demo-secret-key",
			httpClient:  server.Client(),
		},
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}

	options := buildAnchoredSpotChaseOptions("SELL", 100, engine.spotRules["BTCUSDT"].PriceTick)
	order, err := engine.placeSpotChasingOrderWithOptions(context.Background(), "BTCUSDT", "SELL", 1, "exp_sell", options)
	if err != nil {
		t.Fatalf("placeSpotChasingOrderWithOptions() error = %v", err)
	}

	want := []float64{99.9, 99.8, 99.7, 99.6}
	if len(prices) != len(want) {
		t.Fatalf("unexpected price attempts: got=%v want=%v", prices, want)
	}
	for idx := range want {
		if math.Abs(prices[idx]-want[idx]) > 1e-9 {
			t.Fatalf("unexpected chase price at attempt %d: got %.8f want %.8f", idx, prices[idx], want[idx])
		}
	}
	if got := parseFloat(order.ExecutedQty); math.Abs(got-1) > 1e-9 {
		t.Fatalf("expected final executed qty 1, got %.8f", got)
	}
}

func TestPlaceSpotPassiveMakerCloseSkipsOnBearishImbalance(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		t.Fatalf("did not expect network request, got %s", r.URL.Path)
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			apiKey:      "demo-api-key",
			secretKey:   "demo-secret-key",
			httpClient:  server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
		spotTickerCache: NewBookTickerCache(),
		spotDepthCache:  NewDepthCache(),
	}
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, time.Now().UTC())
	engine.spotDepthCache.Update("BTCUSDT", ParsedOrderBookDepth{
		Bids: []DepthLevel{
			{Price: 100, Quantity: 1},
			{Price: 99.9, Quantity: 1},
			{Price: 99.8, Quantity: 1},
		},
		Asks: []DepthLevel{
			{Price: 100.1, Quantity: 4},
			{Price: 100.2, Quantity: 4},
			{Price: 100.3, Quantity: 4},
		},
	})

	order, err := engine.placeSpotPassiveMakerClose(context.Background(), "BTCUSDT", 1, "sclosemaker")
	if err != nil {
		t.Fatalf("placeSpotPassiveMakerClose() error = %v", err)
	}
	if atomic.LoadInt32(&requests) != 0 {
		t.Fatalf("expected no network request when bearish imbalance is detected, got %d", requests)
	}
	if order.OrderID != 0 || parseFloat(order.ExecutedQty) > executionQtyTolerance {
		t.Fatalf("expected zero-value order when passive maker is skipped, got %+v", order)
	}
}
