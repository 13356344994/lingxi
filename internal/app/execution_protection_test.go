package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

func TestNormalizeProtectedLimitPriceAlignsToTick(t *testing.T) {
	buy := normalizeProtectedLimitPrice("BUY", 100.0003, 0.001, 0.05)
	if buy != 100.15 {
		t.Fatalf("unexpected aligned buy price: %v", buy)
	}

	sell := normalizeProtectedLimitPrice("SELL", 100.0003, 0.001, 0.05)
	if sell != 99.9 {
		t.Fatalf("unexpected aligned sell price: %v", sell)
	}
}

func TestFetchBookTickerPrefersWarmCache(t *testing.T) {
	engine := &FundingRateSniperEngine{
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
	}
	engine.spotTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.1"})
	engine.futuresTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "99.9", AskPrice: "100.0"})

	spotTicker, err := engine.fetchSpotBookTicker(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("fetchSpotBookTicker() error = %v", err)
	}
	if spotTicker.AskPrice != "100.1" {
		t.Fatalf("unexpected spot ticker from cache: %+v", spotTicker)
	}

	futuresTicker, err := engine.fetchFuturesBookTicker(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("fetchFuturesBookTicker() error = %v", err)
	}
	if futuresTicker.BidPrice != "99.9" {
		t.Fatalf("unexpected futures ticker from cache: %+v", futuresTicker)
	}
}

func TestFetchBookTickerRejectsStaleWarmCache(t *testing.T) {
	engine := &FundingRateSniperEngine{
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
	}
	staleAt := time.Now().UTC().Add(-2 * protectedTickerMaxStaleness)
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.1"}, staleAt)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "99.9", AskPrice: "100.0"}, staleAt)

	if _, err := engine.fetchSpotBookTicker(context.Background(), "BTCUSDT"); err == nil {
		t.Fatal("expected stale spot ticker to be rejected")
	}
	if _, err := engine.fetchFuturesBookTicker(context.Background(), "BTCUSDT"); err == nil {
		t.Fatal("expected stale futures ticker to be rejected")
	}
}

func TestFetchBookTickerAllowsSilentAltWhenHeartbeatIsFresh(t *testing.T) {
	engine := &FundingRateSniperEngine{
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
	}
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, now)
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "DOGEUSDT", BidPrice: "0.1", AskPrice: "0.11"}, now.Add(-2*time.Second))
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "99", AskPrice: "99.1"}, now)
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "DOGEUSDT", BidPrice: "0.09", AskPrice: "0.10"}, now.Add(-2*time.Second))

	if _, err := engine.fetchSpotBookTicker(context.Background(), "DOGEUSDT"); err != nil {
		t.Fatalf("expected silent alt spot ticker to be accepted with fresh heartbeat, got %v", err)
	}
	if _, err := engine.fetchFuturesBookTicker(context.Background(), "DOGEUSDT"); err != nil {
		t.Fatalf("expected silent alt futures ticker to be accepted with fresh heartbeat, got %v", err)
	}
}

func TestFuturesProtectedOpenFallbackNeededUsesUnifiedIOCObservation(t *testing.T) {
	order := FuturesBatchOrderRequest{
		Type:        "LIMIT",
		TimeInForce: protectedExecutionIOC,
		Quantity:    "1",
	}
	if !futuresProtectedOpenFallbackNeeded(order, FuturesBatchOrderResult{
		Status:      "REJECTED",
		ExecutedQty: "0",
	}) {
		t.Fatal("expected rejected zero-fill protected IOC to require fallback")
	}
	if futuresProtectedOpenFallbackNeeded(order, FuturesBatchOrderResult{
		Status:      "FILLED",
		ExecutedQty: "0.2",
	}) {
		t.Fatal("expected partial progress protected IOC to avoid fallback")
	}
}

func TestPlaceSpotChasingOrderRetriesAfterCanceledAttempt(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/api/v3/order":
			count := atomic.AddInt32(&posts, 1)
			w.Header().Set("Content-Type", "application/json")
			if count == 1 {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"CANCELED","clientOrderId":"cid-1","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
				return
			}
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":2,"status":"FILLED","clientOrderId":"cid-2","executedQty":"1","cummulativeQuoteQty":"100","origQty":"1","fills":[{"price":"100","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
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
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 20

	order, err := engine.placeSpotChasingOrder(context.Background(), "BTCUSDT", "SELL", 1, "exp_sell")
	if err != nil {
		t.Fatalf("placeSpotChasingOrder() error = %v", err)
	}
	if atomic.LoadInt32(&posts) < 2 {
		t.Fatalf("expected chase to retry after canceled attempt, got %d posts", posts)
	}
	if got := parseFloat(order.ExecutedQty); got != 1 {
		t.Fatalf("expected final executed qty 1, got %.8f", got)
	}
}

func TestSpotProtectedOrderParamsSetsSTPMode(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = false
	engine.cfg.ExecutionConfig.SpotSTPMode = "expire_both"

	params, err := engine.spotProtectedOrderParams(context.Background(), "BTCUSDT", "BUY", 1.25, "cid-spot")
	if err != nil {
		t.Fatalf("spotProtectedOrderParams() error = %v", err)
	}
	if got := params.Get("selfTradePreventionMode"); got != "EXPIRE_BOTH" {
		t.Fatalf("expected spot stp mode EXPIRE_BOTH, got %q", got)
	}
	if got := params.Get("type"); got != "MARKET" {
		t.Fatalf("expected market fallback order type, got %q", got)
	}
}

func TestSpotProtectedOrderParamsFormatsRequestPrecisionByRules(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		spotTickerCache: NewBookTickerCache(),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.1, PriceTick: 0.05, MinQty: 0.1, MinNotional: 1},
		},
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = true
	engine.cfg.ExecutionConfig.SpotLimitSlippageRate = 0.001
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.0003"}, time.Now().UTC())

	params, err := engine.spotProtectedOrderParams(context.Background(), "BTCUSDT", "BUY", 0.30000000000000004, "cid-spot")
	if err != nil {
		t.Fatalf("spotProtectedOrderParams() error = %v", err)
	}
	if got := params.Get("quantity"); got != "0.3" {
		t.Fatalf("expected normalized spot quantity 0.3, got %q", got)
	}
	if got := params.Get("price"); got != "100.15" {
		t.Fatalf("expected protected spot price 100.15, got %q", got)
	}
}

func TestFuturesProtectedOrderParamsSetsSTPMode(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = false
	engine.cfg.ExecutionConfig.FuturesSTPMode = "expire_maker"

	params, err := engine.futuresProtectedOrderParams(context.Background(), "BTCUSDT", "SELL", 2, true, "cid-futures")
	if err != nil {
		t.Fatalf("futuresProtectedOrderParams() error = %v", err)
	}
	if got := params.Get("selfTradePreventionMode"); got != "EXPIRE_MAKER" {
		t.Fatalf("expected futures stp mode EXPIRE_MAKER, got %q", got)
	}
	if got := params.Get("type"); got != "MARKET" {
		t.Fatalf("expected market fallback order type, got %q", got)
	}
}

func TestFuturesProtectedOrderParamsFormatsRequestPrecisionByRules(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		futuresTickerCache: NewBookTickerCache(),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = true
	engine.cfg.ExecutionConfig.FuturesLimitSlippageRate = 0
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "99.9", AskPrice: "100.0003"}, time.Now().UTC())

	params, err := engine.futuresProtectedOrderParams(context.Background(), "BTCUSDT", "SELL", 1.2300000000000002, true, "cid-futures")
	if err != nil {
		t.Fatalf("futuresProtectedOrderParams() error = %v", err)
	}
	if got := params.Get("quantity"); got != "1.230" {
		t.Fatalf("expected normalized futures quantity 1.230, got %q", got)
	}
	if got := params.Get("price"); got != "99.9" {
		t.Fatalf("expected protected futures price 99.9, got %q", got)
	}
}

func TestPercentPriceRejectLikelyUsesSideSpecificBounds(t *testing.T) {
	engine := &FundingRateSniperEngine{}
	rules := SymbolTradeRules{
		BidPercentPriceUp:   1.02,
		BidPercentPriceDown: 0.98,
		AskPercentPriceUp:   1.01,
		AskPercentPriceDown: 0.99,
	}

	reject, _ := engine.percentPriceRejectLikely("BTCUSDT", "BUY", 103, 100, rules)
	if !reject {
		t.Fatal("expected BUY price above bid upper bound to be rejected")
	}
	reject, _ = engine.percentPriceRejectLikely("BTCUSDT", "SELL", 98, 100, rules)
	if !reject {
		t.Fatal("expected SELL price below ask lower bound to be rejected")
	}
}

func TestPlaceSpotProtectedOpenDualTrackFallsBackAfterExpiredIOC(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100.0","askPrice":"100.1"}`))
		case "/api/v3/order":
			count := atomic.AddInt32(&posts, 1)
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if count == 1 {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"EXPIRED","clientOrderId":"cid-spot","executedQty":"0","cummulativeQuoteQty":"0","origQty":"1"}`))
				return
			}
			if got := r.Form.Get("type"); got != "LIMIT" && got != "MARKET" {
				t.Fatalf("expected fallback order type LIMIT or MARKET, got %q", got)
			}
			resp := SpotOrderResponse{
				Symbol:              "BTCUSDT",
				OrderID:             2,
				Status:              "FILLED",
				ClientOrderID:       r.Form.Get("newClientOrderId"),
				ExecutedQty:         "1",
				CummulativeQuoteQty: "100",
				OrigQty:             "1",
			}
			_ = json.NewEncoder(w).Encode(resp)
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
		spotTickerCache: NewBookTickerCache(),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = true
	engine.cfg.ExecutionConfig.SpotLimitSlippageRate = 0.001
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.1"}, time.Now().UTC())

	protectedParams := url.Values{}
	protectedParams.Set("symbol", "BTCUSDT")
	protectedParams.Set("side", "BUY")
	protectedParams.Set("type", "LIMIT")
	protectedParams.Set("timeInForce", "IOC")
	protectedParams.Set("quantity", "1")
	protectedParams.Set("price", "100.2")
	protectedParams.Set("newOrderRespType", "FULL")
	protectedParams.Set("newClientOrderId", "cid-spot")

	order, err := engine.placeSpotProtectedOpenDualTrack(context.Background(), "BTCUSDT", "BUY", 1, "cid-spot", protectedParams)
	if err != nil {
		t.Fatalf("placeSpotProtectedOpenDualTrack() error = %v", err)
	}
	if parseFloat(order.ExecutedQty) != 1 {
		t.Fatalf("expected fallback executed qty 1, got %s", order.ExecutedQty)
	}
	if atomic.LoadInt32(&posts) < 2 {
		t.Fatalf("expected fallback order after expired IOC, got %d posts", posts)
	}
}

func TestPlaceFuturesProtectedOpenDualTrackFallsBackAfterExpiredIOC(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100.0","askPrice":"100.1"}`))
		case "/fapi/v1/order":
			count := atomic.AddInt32(&posts, 1)
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if count == 1 {
				_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"EXPIRED","clientOrderId":"cid-futures","executedQty":"0","avgPrice":"0","origQty":"1","side":"SELL"}`))
				return
			}
			resp := FuturesBatchOrderResult{
				Symbol:        "BTCUSDT",
				OrderID:       2,
				Status:        "FILLED",
				ClientOrderID: r.Form.Get("newClientOrderId"),
				ExecutedQty:   "1",
				OrigQty:       "1",
				AvgPrice:      "100",
				Side:          "SELL",
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:                DefaultEngineConfig(),
		logger:             log.New(io.Discard, "", 0),
		runtimeReporter:    newRuntimeReporter(nil),
		futuresTickerCache: NewBookTickerCache(),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
		futuresOrderHub: NewOrderEventHub(),
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = true
	engine.cfg.ExecutionConfig.FuturesLimitSlippageRate = 0.001
	engine.cfg.ExecutionConfig.ExecutionConfirmTimeout = 200 * time.Millisecond
	engine.cfg.ExecutionConfig.ExecutionRecoveryTimeout = 400 * time.Millisecond
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.1"}, time.Now().UTC())

	order := FuturesBatchOrderRequest{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             "LIMIT",
		Quantity:         "1",
		Price:            "99.9",
		TimeInForce:      "IOC",
		NewClientOrderID: "cid-futures",
		NewOrderRespType: "RESULT",
	}
	snapshot, err := engine.placeSingleFuturesOpenOrderIdempotent(context.Background(), order)
	if err != nil {
		t.Fatalf("placeSingleFuturesOpenOrderIdempotent() error = %v", err)
	}
	if snapshot.ExecutedQty != 1 {
		t.Fatalf("expected fallback executed qty 1, got %.8f", snapshot.ExecutedQty)
	}
	if atomic.LoadInt32(&posts) < 2 {
		t.Fatalf("expected fallback futures order after expired IOC, got %d posts", posts)
	}
}

func TestBuildProtectedOpenFuturesOrderCarriesSTPMode(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = false
	engine.cfg.ExecutionConfig.FuturesSTPMode = "EXPIRE_TAKER"

	order := engine.buildProtectedOpenFuturesOrder(&MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:        "BTCUSDT",
			OrderQuantity: 0.5,
		},
	})
	if order.SelfTradeMode != "EXPIRE_TAKER" {
		t.Fatalf("expected open futures order to carry stp mode, got %q", order.SelfTradeMode)
	}
}

func TestBuildProtectedOpenFuturesOrderFormatsQuantityByRules(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
	}
	engine.cfg.ExecutionConfig.UseProtectedIOC = false

	order := engine.buildProtectedOpenFuturesOrder(&MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:            "BTCUSDT",
			OrderQuantity:     0.30000000000000004,
			QuantityPrecision: 3,
			MarkPrice:         100,
		},
	})
	if order.Quantity != "0.300" {
		t.Fatalf("expected formatted futures open quantity 0.300, got %q", order.Quantity)
	}
}
