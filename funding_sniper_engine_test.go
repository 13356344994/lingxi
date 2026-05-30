package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsUnknownOrderRequestError(t *testing.T) {
	timeoutErr := context.DeadlineExceeded
	if !isUnknownOrderRequestError(timeoutErr) {
		t.Fatal("expected deadline exceeded to be treated as unknown order state")
	}
	if !isUnknownOrderRequestError(io.EOF) {
		t.Fatal("expected EOF to be treated as unknown order state")
	}
	if !isUnknownOrderRequestError(signedRequestError{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"}) {
		t.Fatal("expected 502 to be treated as unknown order state")
	}
	if !isUnknownOrderRequestError(signedRequestError{StatusCode: http.StatusGatewayTimeout, Status: "504 Gateway Timeout"}) {
		t.Fatal("expected 504 to be treated as unknown order state")
	}
	if isUnknownOrderRequestError(signedRequestError{StatusCode: http.StatusBadRequest, Status: "400 Bad Request"}) {
		t.Fatal("did not expect 400 to be treated as unknown order state")
	}
	if isUnknownOrderRequestError(errors.New("签名请求失败: endpoint=/api/v3/order status=400 Bad Request body=bad symbol")) {
		t.Fatal("did not expect deterministic 400 error to be treated as unknown order state")
	}
}

func TestRequestFormattingUsesRulePrecision(t *testing.T) {
	rules := SymbolTradeRules{StepSize: 0.001, PriceTick: 0.05}
	if got := formatQuantityForRules(0.30000000000000004, rules, 3); got != "0.300" {
		t.Fatalf("expected formatted quantity 0.300, got %q", got)
	}
	if got := formatPriceForTick(100.15000000000002, rules.PriceTick); got != "100.15" {
		t.Fatalf("expected formatted price 100.15, got %q", got)
	}
	if got := formatRequestAmount(1.234567890123456); got != "1.234567890123" {
		t.Fatalf("expected formatted amount 1.234567890123, got %q", got)
	}
}

func TestPlaceFuturesBatchOrdersIdempotentRecoversUnknownOrderState(t *testing.T) {
	postCalls := 0
	queryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/batchOrders":
			postCalls++
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte(`{"code":-1007,"msg":"timeout"}`))
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/fapi/v1/order":
			queryCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"cid-1","status":"FILLED","executedQty":"1","orderId":123}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}

	results, err := engine.placeFuturesBatchOrdersIdempotent(context.Background(), []FuturesBatchOrderRequest{{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             "MARKET",
		Quantity:         "1",
		NewClientOrderID: "cid-1",
	}})
	if err != nil {
		t.Fatalf("placeFuturesBatchOrdersIdempotent() error = %v", err)
	}
	if postCalls != 1 {
		t.Fatalf("expected one batch POST, got %d", postCalls)
	}
	if queryCalls != 1 {
		t.Fatalf("expected one order query after unknown state, got %d", queryCalls)
	}
	if len(results) != 1 || results[0].OrderID != 123 {
		t.Fatalf("unexpected recovered results: %+v", results)
	}
}

func TestPlaceFuturesBatchOrdersIdempotentSkipsFallbackForDeterministicReject(t *testing.T) {
	queryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/batchOrders":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-1102,"msg":"mandatory parameter missing"}`))
		case "/fapi/v1/order":
			queryCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"cid-1","status":"FILLED","executedQty":"1","orderId":123}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}

	_, err := engine.placeFuturesBatchOrdersIdempotent(context.Background(), []FuturesBatchOrderRequest{{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             "MARKET",
		Quantity:         "1",
		NewClientOrderID: "cid-1",
	}})
	if err == nil {
		t.Fatal("expected deterministic 400 reject to be returned directly")
	}
	if queryCalls != 0 {
		t.Fatalf("did not expect fallback order query for deterministic reject, got %d", queryCalls)
	}
}

func TestNewAuthenticatedBinanceClientUsesNetworkConfig(t *testing.T) {
	client, err := NewAuthenticatedBinanceClient(SecretConfig{
		APIKey:   "demo-api-key",
		Secret:   "demo-secret-key",
		ProxyURL: "",
	}, NetworkConfig{
		HTTPTimeout:           9 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   80,
		IdleConnTimeout:       70 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		ExpectContinueTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewAuthenticatedBinanceClient() error = %v", err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.MaxIdleConns != 200 || transport.MaxIdleConnsPerHost != 80 {
		t.Fatalf("unexpected transport pool config: %+v", transport)
	}
	if client.httpClient.Timeout != 9*time.Second {
		t.Fatalf("unexpected client timeout: %s", client.httpClient.Timeout)
	}
}

func TestEffectiveOpenHedgeMaxConcurrency(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.OpenHedgeMaxConcurrency = 4
	if got := effectiveOpenHedgeMaxConcurrency(cfg); got != 4 {
		t.Fatalf("expected configured concurrency to be used, got %d", got)
	}

	cfg.ExecutionConfig.OpenHedgeMaxConcurrency = 0
	if got := effectiveOpenHedgeMaxConcurrency(cfg); got != 1 {
		t.Fatalf("expected fallback concurrency 1, got %d", got)
	}
}

func TestNoteProtectedIOCResultAccumulatesStats(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		symbolCooldowns: make(map[string]symbolCooldownState),
	}

	engine.noteProtectedIOCResult("BTCUSDT", "futures", 10, 6, "EXPIRED")
	engine.platformRiskMu.Lock()
	attempts, canceled, expired, partial, avgUnfilled, cancelRate := engine.protectedIOCStatsLocked()
	engine.platformRiskMu.Unlock()

	if attempts != 1 || canceled != 0 || expired != 1 || partial != 1 {
		t.Fatalf("unexpected protected ioc stats: attempts=%d canceled=%d expired=%d partial=%d", attempts, canceled, expired, partial)
	}
	if avgUnfilled != 0.4 {
		t.Fatalf("unexpected avg unfilled ratio: %v", avgUnfilled)
	}
	if cancelRate != 1 {
		t.Fatalf("unexpected cancel rate: %v", cancelRate)
	}
}

func TestHealthStatusLockedPausesWhenProtectedIOCExceedsThreshold(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		symbolCooldowns: make(map[string]symbolCooldownState),
	}
	engine.cfg.RiskConfig.ProtectedIOCMinAttempts = 3
	engine.cfg.RiskConfig.ProtectedIOCMaxUnfilledRatio = 0.4
	engine.cfg.RiskConfig.ProtectedIOCMaxCancelRate = 0.5
	engine.protectedIOCAttempts = 3
	engine.protectedIOCCanceled = 2
	engine.protectedIOCUnfilledSum = 1.5

	engine.platformRiskMu.Lock()
	status, summary, canContinue := engine.healthStatusLocked()
	engine.platformRiskMu.Unlock()

	if status != "paused" || canContinue {
		t.Fatalf("expected protected ioc breach to pause engine, got status=%s canContinue=%t", status, canContinue)
	}
	if summary == "" {
		t.Fatal("expected non-empty health summary")
	}
}

func TestRunBoundedConcurrentTasksHonorsConcurrencyLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var (
		current int32
		maxSeen int32
	)
	err := runBoundedConcurrentTasks(ctx, 2, 6, func(context.Context, int) error {
		now := atomic.AddInt32(&current, 1)
		defer atomic.AddInt32(&current, -1)

		for {
			seen := atomic.LoadInt32(&maxSeen)
			if now <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("runBoundedConcurrentTasks() error = %v", err)
	}
	if maxSeen > 2 {
		t.Fatalf("expected max concurrency <= 2, got %d", maxSeen)
	}
}

func TestShouldUseParallelOpenHedge(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.ParallelOpenSymbols = "BTCUSDT, ETH"

	if !shouldUseParallelOpenHedge(cfg, "BTCUSDT") {
		t.Fatal("expected exact symbol match to enable parallel open hedge")
	}
	if !shouldUseParallelOpenHedge(cfg, "ETHUSDT") {
		t.Fatal("expected base asset match to enable parallel open hedge")
	}
	if shouldUseParallelOpenHedge(cfg, "SOLUSDT") {
		t.Fatal("did not expect unrelated symbol to enable parallel open hedge")
	}
}

func TestEstimateReservedNetProfitRateUsesRoundTripReserve(t *testing.T) {
	got := estimateReservedNetProfitRate(0.0025, 0.0001, 0.0030, 0.0004)
	if diff := got - (-0.001); diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("unexpected reserved net profit rate: %.6f", got)
	}
}

func TestOpenHedgeForTargetParallelSendsDualPostsConcurrently(t *testing.T) {
	var arrivals int32
	bothRequestsSeen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/order", "/api/v3/order":
			if atomic.AddInt32(&arrivals, 1) == 2 {
				close(bothRequestsSeen)
			}
			select {
			case <-bothRequestsSeen:
			case <-time.After(100 * time.Millisecond):
				http.Error(w, "concurrent request not observed", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/fapi/v1/order" {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fopen-test","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":0,"updateTime":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"FILLED","clientOrderId":"sopen-test","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 5 * time.Millisecond
	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:                cfg,
		logger:             log.New(io.Discard, "", 0),
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			EstimatedRate:     0.01,
			MarkPrice:         100,
			OrderQuantity:     1,
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
		LastSpotAsk: 100,
	}
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.05"}, now)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.02"}, now)
	plan := openSequencePlan{
		target: target,
		futuresOrder: FuturesBatchOrderRequest{
			Symbol:           "BTCUSDT",
			Side:             "SELL",
			Type:             "MARKET",
			Quantity:         "1",
			NewClientOrderID: "fopen-test",
			NewOrderRespType: "RESULT",
		},
	}

	hedge, err := engine.openHedgeForTargetParallel(context.Background(), plan)
	if err != nil {
		t.Fatalf("openHedgeForTargetParallel() error = %v", err)
	}
	if hedge.HedgeQty != 1 {
		t.Fatalf("expected hedge qty 1, got %.8f", hedge.HedgeQty)
	}
	if atomic.LoadInt32(&arrivals) != 2 {
		t.Fatalf("expected two concurrent POST requests, got %d", arrivals)
	}
}

func TestCloseHedgePositionParallelSendsDualPostsConcurrently(t *testing.T) {
	var arrivals int32
	bothRequestsSeen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/order", "/api/v3/order":
			if atomic.AddInt32(&arrivals, 1) == 2 {
				close(bothRequestsSeen)
			}
			select {
			case <-bothRequestsSeen:
			case <-time.After(100 * time.Millisecond):
				http.Error(w, "concurrent request not observed", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/fapi/v1/order" {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fclose-test","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":2,"updateTime":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":3,"status":"FILLED","clientOrderId":"sclose-test","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 5 * time.Millisecond
	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}
	hedge := OpenHedge{
		Target: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			MarkPrice:         100,
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
		HedgeQty:         1,
		HedgeQtyText:     "1",
		OpenSpotQuoteQty: 100,
		SpotOrder: SpotOrderResponse{
			ExecutedQty:         "1",
			CummulativeQuoteQty: "100",
		},
	}
	plan := closeSequencePlan{
		hedge: &hedge,
		futuresOrder: FuturesBatchOrderRequest{
			Symbol:           "BTCUSDT",
			Side:             "BUY",
			Type:             "MARKET",
			Quantity:         "1",
			ReduceOnly:       "true",
			NewClientOrderID: "fclose-test",
			NewOrderRespType: "RESULT",
		},
	}

	alerts, err := engine.closeHedgePositionParallel(context.Background(), plan)
	if err != nil {
		t.Fatalf("closeHedgePositionParallel() error = %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts, got %v", alerts)
	}
	if hedge.CloseFuturesExecutedQty != 1 || hedge.CloseSpotExecutedQty != 1 {
		t.Fatalf("expected close qty 1/1, got futures=%.8f spot=%.8f", hedge.CloseFuturesExecutedQty, hedge.CloseSpotExecutedQty)
	}
	if atomic.LoadInt32(&arrivals) != 2 {
		t.Fatalf("expected two concurrent POST requests, got %d", arrivals)
	}
}

func TestEnsureUSDTFundingBalancesForOpenTransfersFromFuturesToSpot(t *testing.T) {
	transferCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"0","locked":"0"}]}`))
		case "/fapi/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"250","availableBalance":"250"}],"positions":[]}`))
		case "/sapi/v1/asset/transfer":
			transferCalls++
			if got := r.URL.Query().Get("type"); got != "UMFUTURE_MAIN" {
				t.Fatalf("unexpected transfer type: %s", got)
			}
			if got := r.URL.Query().Get("asset"); got != "USDT" {
				t.Fatalf("unexpected transfer asset: %s", got)
			}
			if got := r.URL.Query().Get("amount"); got != "100" {
				t.Fatalf("unexpected transfer amount: %s", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tranId":12345}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.AutoBalanceUSDT = true
	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}

	targets := []*MonitoredTarget{{Candidate: FundingCandidate{Symbol: "BTCUSDT"}}}
	if err := engine.ensureUSDTFundingBalancesForOpen(context.Background(), targets); err != nil {
		t.Fatalf("ensureUSDTFundingBalancesForOpen() error = %v", err)
	}
	if transferCalls != 1 {
		t.Fatalf("expected one transfer call, got %d", transferCalls)
	}
}

func TestOpenPreparationTimingUsesConfiguredLead(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	fundingTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	targets := []*MonitoredTarget{{Candidate: FundingCandidate{NextFundingTime: fundingTime}}}

	fireAt := engine.openFireAt(targets)
	if want := fundingTime.Add(-engine.cfg.StrategyConfig.MonitorStopLead); !fireAt.Equal(want) {
		t.Fatalf("expected fireAt=%s, got %s", want.Format(time.RFC3339), fireAt.Format(time.RFC3339))
	}
	if want := fireAt.Add(-engine.cfg.ExecutionConfig.OpenPreparationLead); !engine.openPreparationStartAt(targets).Equal(want) {
		t.Fatalf("expected preparation start=%s, got %s", want.Format(time.RFC3339), engine.openPreparationStartAt(targets).Format(time.RFC3339))
	}
	if want := fireAt.Add(-engine.cfg.ExecutionConfig.PreOpenNoGoLead); !engine.openNoGoCheckAt(targets).Equal(want) {
		t.Fatalf("expected no-go time=%s, got %s", want.Format(time.RFC3339), engine.openNoGoCheckAt(targets).Format(time.RFC3339))
	}
}

func TestEnsureFuturesExecutionSettingsConfiguresMarginTypeAndLeverage(t *testing.T) {
	marginCalls := 0
	leverageCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/marginType":
			marginCalls++
			if got := r.URL.Query().Get("marginType"); got != "CROSSED" {
				t.Fatalf("unexpected marginType: %s", got)
			}
			if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
				t.Fatalf("unexpected marginType symbol: %s", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case "/fapi/v1/leverage":
			leverageCalls++
			if got := r.URL.Query().Get("leverage"); got != "3" {
				t.Fatalf("unexpected leverage: %s", got)
			}
			if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
				t.Fatalf("unexpected leverage symbol: %s", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	targets := []*MonitoredTarget{
		{Candidate: FundingCandidate{Symbol: "BTCUSDT"}},
		{Candidate: FundingCandidate{Symbol: "BTCUSDT"}},
	}
	if err := engine.ensureFuturesExecutionSettings(context.Background(), targets); err != nil {
		t.Fatalf("ensureFuturesExecutionSettings() error = %v", err)
	}
	if marginCalls != 1 || leverageCalls != 1 {
		t.Fatalf("expected one margin + one leverage call, got margin=%d leverage=%d", marginCalls, leverageCalls)
	}
}

func TestVerifyPreOpenGoNoGoRejectsStaleTicker(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.New(io.Discard, "", 0),
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
	}
	staleAt := time.Now().UTC().Add(-2 * time.Second)
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, staleAt)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, staleAt)

	err := engine.verifyPreOpenGoNoGo([]*MonitoredTarget{{Candidate: FundingCandidate{Pair: "BTCUSDT", Symbol: "BTCUSDT"}}})
	if err == nil || !strings.Contains(err.Error(), "延迟过高") {
		t.Fatalf("expected stale ticker rejection, got %v", err)
	}
}

func TestValidateFreshOpenHedgeAbortsOnAdverseSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/order":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fclose-now","status":"FILLED","executedQty":"1","avgPrice":"101","orderId":2,"updateTime":1}`))
		case "/fapi/v1/userTrades":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/api/v3/order":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":3,"status":"FILLED","clientOrderId":"sclose-now","executedQty":"1","cummulativeQuoteQty":"98","fills":[{"price":"98","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:                cfg,
		logger:             log.New(io.Discard, "", 0),
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "98", AskPrice: "98.1"}, now)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.9", AskPrice: "101"}, now)

	hedge := OpenHedge{
		Target: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			EstimatedRate:     0.002,
			MarkPrice:         100,
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
		HedgeQty:         1,
		HedgeQtyText:     "1",
		OpenSpotQuoteQty: 100,
		SpotOrder: SpotOrderResponse{
			ExecutedQty:         "1",
			CummulativeQuoteQty: "100",
		},
	}

	err := engine.validateFreshOpenHedge(context.Background(), &hedge)
	var skipped nonFatalOpenAbortError
	if !errors.As(err, &skipped) {
		t.Fatalf("expected nonFatalOpenAbortError, got %v", err)
	}
	if hedge.CloseFuturesExecutedQty != 1 || hedge.CloseSpotExecutedQty != 1 {
		t.Fatalf("expected immediate unwind to close hedge, got futures=%.8f spot=%.8f", hedge.CloseFuturesExecutedQty, hedge.CloseSpotExecutedQty)
	}
}

func TestFetchSpotBookTickerRejectsRESTFallbackInCacheOnlyMode(t *testing.T) {
	restCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restCalls++
		t.Fatalf("unexpected REST fallback request: %s", r.URL.Path)
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			httpClient:  server.Client(),
		},
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		spotTickerCache: NewBookTickerCache(),
	}
	engine.setBookTickerCacheOnlyUntil(time.Now().UTC().Add(time.Second))
	defer engine.setBookTickerCacheOnlyUntil(time.Time{})

	_, err := engine.fetchSpotBookTicker(context.Background(), "BTCUSDT")
	if err == nil || !strings.Contains(err.Error(), "仅允许使用缓存") {
		t.Fatalf("expected cache-only rejection, got %v", err)
	}
	if restCalls != 0 {
		t.Fatalf("expected zero REST fallback calls, got %d", restCalls)
	}
}

func TestEnsureSpotBNBFeeCoverageForOpenRejectsInsufficientBNB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"1000","locked":"0"},{"asset":"BNB","free":"0.0001","locked":"0"}]}`))
		case "/api/v3/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BNBUSDT","bidPrice":"600","askPrice":"600"}`))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
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
	}

	err := engine.ensureSpotBNBFeeCoverageForOpen(context.Background(), []*MonitoredTarget{{
		Candidate:   FundingCandidate{Symbol: "BTCUSDT", OrderQuantity: 1, MarkPrice: 100},
		LastSpotAsk: 100,
	}})
	if err == nil {
		t.Fatal("expected insufficient BNB to reject open")
	}
}

func TestIsLiveSpotPairFundableRespectsLiveFundingAsset(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT":  {Symbol: "BTCUSDT", QuoteAsset: "USDT"},
			"BTCFDUSD": {Symbol: "BTCFDUSD", QuoteAsset: "FDUSD"},
		},
	}
	if !engine.isLiveSpotPairFundable("BTCUSDT") {
		t.Fatal("expected BTCUSDT to be fundable with USDT live funding asset")
	}
	if engine.isLiveSpotPairFundable("BTCFDUSD") {
		t.Fatal("did not expect BTCFDUSD to be fundable with USDT live funding asset")
	}
}

func TestOpenHedgeForTargetParallelUnknownSpotSchedulesExposureRescue(t *testing.T) {
	var futuresOpenPosts int32
	var futuresRecoveryPosts int32
	var spotOpenPosts int32
	var spotRecoveryPosts int32
	var spotQueryCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"100"}],"positions":[{"symbol":"BTCUSDT","positionAmt":"-1"}]}`))
		case "/api/v3/account":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"BTC","free":"0","locked":"0"},{"asset":"USDT","free":"100","locked":"0"}]}`))
		case "/fapi/v1/order":
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if r.Method == http.MethodGet {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected futures method: %s", r.Method)
			}
			if strings.HasPrefix(clientOrderID, "ftrim") || strings.HasPrefix(clientOrderID, "frescue") || strings.HasPrefix(clientOrderID, "fclose") || strings.HasPrefix(clientOrderID, "expfclose") {
				atomic.AddInt32(&futuresRecoveryPosts, 1)
			} else {
				atomic.AddInt32(&futuresOpenPosts, 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","clientOrderId":"fopen-test","status":"FILLED","executedQty":"1","avgPrice":"100","orderId":1,"updateTime":1}`))
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/api/v3/order":
			clientOrderID := r.URL.Query().Get("newClientOrderId")
			if r.Method == http.MethodGet {
				atomic.AddInt32(&spotQueryCalls, 1)
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
				return
			}
			if strings.HasPrefix(clientOrderID, "srevert") || strings.HasPrefix(clientOrderID, "sclose_revert") || strings.HasPrefix(clientOrderID, "exp_sell") {
				atomic.AddInt32(&spotRecoveryPosts, 1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":9,"status":"FILLED","clientOrderId":"exp-sell","executedQty":"1","cummulativeQuoteQty":"100","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
				return
			} else {
				atomic.AddInt32(&spotOpenPosts, 1)
			}
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false
	cfg.ExecutionConfig.ExecutionConfirmTimeout = 5 * time.Millisecond
	cfg.ExecutionConfig.ExecutionRecoveryTimeout = 20 * time.Millisecond
	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:             cfg,
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
		spotMeta: map[string]SpotSymbol{
			"BTCUSDT": {Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT"},
		},
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {MinQty: 0.001, StepSize: 0.001, MinNotional: 1},
		},
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			MarkPrice:         100,
			OrderQuantity:     1,
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
		LastSpotAsk: 100,
	}
	plan := openSequencePlan{
		target: target,
		futuresOrder: FuturesBatchOrderRequest{
			Symbol:           "BTCUSDT",
			Side:             "SELL",
			Type:             "MARKET",
			Quantity:         "1",
			NewClientOrderID: "fopen-test",
			NewOrderRespType: "RESULT",
		},
	}

	err := error(nil)
	_, err = engine.openHedgeForTargetParallel(context.Background(), plan)
	if err == nil {
		t.Fatal("expected unknown spot order to abort open")
	}
	if _, ok := engine.symbolCooldowns["BTCUSDT"]; !ok {
		t.Fatal("expected symbol cooldown to be set for unknown order")
	}
	if atomic.LoadInt32(&futuresOpenPosts) != 1 || atomic.LoadInt32(&spotOpenPosts) != 1 {
		t.Fatalf("expected one open request per leg, got futures=%d spot=%d", futuresOpenPosts, spotOpenPosts)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&futuresRecoveryPosts) > 0 && atomic.LoadInt32(&spotQueryCalls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&futuresRecoveryPosts) == 0 {
		t.Fatal("expected exposure manager to eventually close residual futures short")
	}
	if atomic.LoadInt32(&spotRecoveryPosts) != 0 {
		t.Fatalf("did not expect spot rescue without confirmed spot fill, got %d", spotRecoveryPosts)
	}
	if atomic.LoadInt32(&spotQueryCalls) == 0 {
		t.Fatal("expected reconcile queries for unknown spot order")
	}
}

func TestVerifySpotBNBBurnEnabledRejectsDisabledFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sapi/v1/bnbBurn" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"spotBNBBurn":false}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL: server.URL,
			apiKey:      "demo-api-key",
			secretKey:   "demo-secret-key",
			httpClient:  server.Client(),
		},
		cfg: DefaultEngineConfig(),
	}
	if err := engine.verifySpotBNBBurnEnabled(context.Background()); err == nil {
		t.Fatal("expected disabled BNB burn flag to block initialize")
	}
}

func TestGetJSONBlocksNonCriticalRequestWhenUsedWeightIsHigh(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"serverTime":1}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:              server.URL,
			futuresBaseURL:           server.URL,
			httpClient:               server.Client(),
			usedWeight1m:             5000,
			usedWeightLimit1m:        6000,
			usedWeightBlockThreshold: 4800,
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	var payload map[string]any
	if err := engine.getJSON(context.Background(), server.URL+"/api/v3/exchangeInfo", &payload); err == nil {
		t.Fatal("expected high used weight to block non-critical request")
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("expected blocked request to avoid hitting server, got %d", hits)
	}
}

func TestSignedRequestReservesUsedWeightBudgetAcrossParallelRequests(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Header().Set("X-MBX-USED-WEIGHT-1M", "4799")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:               server.URL,
			futuresBaseURL:            server.URL,
			apiKey:                    "demo-api-key",
			secretKey:                 "demo-secret-key",
			httpClient:                server.Client(),
			usedWeight1m:              4798,
			usedWeightLimit1m:         6000,
			usedWeightBlockThreshold:  4800,
			usedWeightWindowMinuteUTC: time.Now().UTC().Truncate(time.Minute),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := engine.signedRequest(context.Background(), http.MethodGet, server.URL+"/fapi/v1/account", nil)
		results <- err
	}()
	time.Sleep(10 * time.Millisecond)
	go func() {
		defer wg.Done()
		_, err := engine.signedRequest(context.Background(), http.MethodGet, server.URL+"/fapi/v1/account", nil)
		results <- err
	}()

	<-started
	close(release)
	wg.Wait()
	close(results)

	var blocked, succeeded int
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if strings.Contains(err.Error(), "usedWeightProjected") {
			blocked++
			continue
		}
		t.Fatalf("unexpected signedRequest error: %v", err)
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("expected one request to succeed and one to be blocked, got succeeded=%d blocked=%d", succeeded, blocked)
	}
}

func TestScheduleAsyncFundingSettlementUpdatesSettlementAndDailyPnL(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/income" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"1.25","time":1}]`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:                    DefaultEngineConfig(),
		logger:                 log.New(io.Discard, "", 0),
		reportStore:            store,
		runtimeReporter:        newRuntimeReporter(nil),
		fundingSettlementDelay: 10 * time.Millisecond,
	}
	settlement := CycleSettlement{
		RunID:                 "run-1",
		CycleID:               "cycle-1",
		FundingTimePoint:      time.Now().UTC(),
		FundingIncomePending:  true,
		ExpectedFundingIncome: 1.00,
		SpotTradingPnL:        2.00,
		FuturesTradingPnL:     0.50,
		TotalFees:             0.25,
		NetPnL:                2.25,
		BeforeSnapshot:        CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{}},
		Alerts:                []string{"资金费异步结算待处理，预计在 0s 后更新实收流水"},
	}
	hedges := []OpenHedge{{
		Target:              FundingCandidate{Symbol: "BTCUSDT"},
		HedgeQty:            1,
		OpenFuturesAvgPrice: 100,
	}}
	if err := store.AppendSettlement(settlement); err != nil {
		t.Fatalf("AppendSettlement() error = %v", err)
	}

	engine.scheduleAsyncFundingSettlement(settlement, hedges)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		records, listErr := store.ListSettlementRecords(5)
		if listErr != nil {
			t.Fatalf("ListSettlementRecords() error = %v", listErr)
		}
		if len(records) == 1 && records[0].FundingIncome == "1.25" && !records[0].FundingIncomePending {
			if engine.dailyRealizedPnL != 1.25 {
				t.Fatalf("expected daily pnl to be adjusted by funding delta, got %.8f", engine.dailyRealizedPnL)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected async funding settlement to update store")
}

func TestWaitForCloseExecutionWindowPrefersPassiveSpotWhenSpreadStaysWide(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/time":
		case "/fapi/v1/time":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"serverTime":%d}`, time.Now().UTC().UnixMilli())))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		client: &BinanceClient{
			spotBaseURL:    server.URL,
			futuresBaseURL: server.URL,
			httpClient:     server.Client(),
		},
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
		runtimeReporter:    newRuntimeReporter(nil),
	}
	engine.cfg.ExecutionConfig.ExitDelayAfterFunding = 0
	engine.cfg.ExecutionConfig.ExitMonitorInterval = 5 * time.Millisecond
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, now)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.8", AskPrice: "101"}, now)

	_, _, preferPassiveSpot, err := engine.waitForCloseExecutionWindow(context.Background(), []OpenHedge{{
		Target: FundingCandidate{Pair: "BTCUSDT", Symbol: "BTCUSDT"},
	}}, now)
	if err != nil {
		t.Fatalf("waitForCloseExecutionWindow() error = %v", err)
	}
	if !preferPassiveSpot {
		t.Fatal("expected wide spread timeout to prefer passive spot close")
	}
}

func TestLivePreferredSpotQuoteAssetsUsesLiveFundingAsset(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.StrategyConfig.PreferredSpotQuoteAssets = "FDUSD,USDC,USDT"
	cfg.StrategyConfig.LiveFundingAsset = "USDT"
	engine := &FundingRateSniperEngine{cfg: cfg}

	got := engine.livePreferredSpotQuoteAssets()
	if len(got) != 1 || got[0] != "USDT" {
		t.Fatalf("expected live preferred quotes to collapse to USDT, got %v", got)
	}
}
