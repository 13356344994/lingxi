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
	originalJitter := executionChaseJitterDuration
	executionChaseJitterDuration = func(time.Duration) time.Duration { return 0 }
	defer func() { executionChaseJitterDuration = originalJitter }()

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
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 20

	options := buildAnchoredSpotChaseOptions("SELL", 100, engine.spotRules["BTCUSDT"].PriceTick)
	order, err := engine.placeSpotChasingOrderWithOptions(context.Background(), "BTCUSDT", "SELL", 1, "exp_sell", options)
	if err != nil {
		t.Fatalf("placeSpotChasingOrderWithOptions() error = %v", err)
	}

	want := []float64{99.9, 99.7, 99.8, 99.6}
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

func TestChaseHelpersUseNonLinearScheduleAndBoundedJitter(t *testing.T) {
	got := []float64{
		chaseSlippageForAttempt(0),
		chaseSlippageForAttempt(1),
		chaseSlippageForAttempt(2),
		chaseSlippageForAttempt(3),
	}
	want := []float64{0.0010, 0.0025, 0.0020, 0.0035}
	for idx := range want {
		if math.Abs(got[idx]-want[idx]) > 1e-9 {
			t.Fatalf("unexpected slippage at attempt %d: got %.6f want %.6f", idx, got[idx], want[idx])
		}
	}

	originalJitter := executionChaseJitterDuration
	executionChaseJitterDuration = func(maxJitter time.Duration) time.Duration {
		if maxJitter != executionChaseIntervalJitter {
			t.Fatalf("unexpected jitter max: got %s want %s", maxJitter, executionChaseIntervalJitter)
		}
		return -executionChaseIntervalJitter
	}
	defer func() { executionChaseJitterDuration = originalJitter }()

	if gotWait := executionChaseWaitDuration(executionChaseInterval); gotWait != executionChaseInterval-executionChaseIntervalJitter {
		t.Fatalf("unexpected jittered wait duration: got %s", gotWait)
	}
}

func TestEnforceChaseLossLimitTripsPlatformFuse(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
	}
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 5
	engine.cfg.RiskConfig.MaxExecutionIncidents = 10

	err := engine.enforceChaseLossLimit("BTCUSDT", "BUY", 1, 100, 106, "spot_buy_chase")
	if err == nil {
		t.Fatal("expected chase loss limit to trip")
	}
	if !engine.platformFuseTripped {
		t.Fatal("expected platform fuse to be tripped")
	}
	if got := atomic.LoadInt32(&engine.executionIncidents); got != 1 {
		t.Fatalf("expected one execution incident, got %d", got)
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

func TestPlaceSpotPassiveMakerCloseStopsOnAdverseBidTrend(t *testing.T) {
	var (
		bookTickerHits int32
		orderPosts     int32
		orderQueries   int32
		orderCancels   int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/ticker/bookTicker":
			hit := atomic.AddInt32(&bookTickerHits, 1)
			if hit == 1 {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"99.8","askPrice":"100.1"}`))
		case "/api/v3/order":
			switch r.Method {
			case http.MethodPost:
				atomic.AddInt32(&orderPosts, 1)
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"NEW","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			case http.MethodGet:
				atomic.AddInt32(&orderQueries, 1)
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"NEW","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			case http.MethodDelete:
				atomic.AddInt32(&orderCancels, 1)
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"CANCELED","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			default:
				t.Fatalf("unexpected method: %s", r.Method)
			}
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
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}

	order, err := engine.placeSpotPassiveMakerClose(context.Background(), "BTCUSDT", 1, "sclosemaker")
	if err != nil {
		t.Fatalf("placeSpotPassiveMakerClose() error = %v", err)
	}
	if atomic.LoadInt32(&orderPosts) != 1 {
		t.Fatalf("expected only one maker post before adverse trend abort, got %d", orderPosts)
	}
	if atomic.LoadInt32(&orderCancels) != 1 {
		t.Fatalf("expected first maker order to be canceled, got %d", orderCancels)
	}
	if parseFloat(order.ExecutedQty) > executionQtyTolerance {
		t.Fatalf("expected no maker fill before fallback, got %+v", order)
	}
}

func TestPlaceSpotPassiveMakerCloseHonorsCancelBudget(t *testing.T) {
	var orderPosts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/ticker/bookTicker":
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/api/v3/order":
			switch r.Method {
			case http.MethodPost:
				atomic.AddInt32(&orderPosts, 1)
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"NEW","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			case http.MethodGet:
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"NEW","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			case http.MethodDelete:
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"CANCELED","clientOrderId":"sclosemaker-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
			default:
				t.Fatalf("unexpected method: %s", r.Method)
			}
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
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}

	order, err := engine.placeSpotPassiveMakerClose(context.Background(), "BTCUSDT", 1, "sclosemaker")
	if err != nil {
		t.Fatalf("placeSpotPassiveMakerClose() error = %v", err)
	}
	if atomic.LoadInt32(&orderPosts) != passiveMakerCancelBudget {
		t.Fatalf("expected maker attempts to stop at cancel budget %d, got %d", passiveMakerCancelBudget, orderPosts)
	}
	if parseFloat(order.ExecutedQty) > executionQtyTolerance {
		t.Fatalf("expected no maker fill, got %+v", order)
	}
}
