package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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

func TestPlaceSpotChasingOrderRetriesAfterCanceledAttempt(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
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
		spotTickerCache: NewBookTickerCache(),
	}
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, now)

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
