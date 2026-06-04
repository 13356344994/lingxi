package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

func TestSignedRequestBlocksNonCriticalOrderWhenLocalRateLimiterExceeded(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	now := time.Now().UTC()
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:                  DefaultEngineConfig(),
		logger:               log.New(io.Discard, "", 0),
		runtimeReporter:      newRuntimeReporter(nil),
		orderRateRequests10s: make([]time.Time, orderRateLimitCapacity10s-orderRateLimitReserve10s),
	}
	engine.orderRateRequests1m = append(engine.orderRateRequests1m, now)
	for i := range engine.orderRateRequests10s {
		engine.orderRateRequests10s[i] = now
	}

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("side", "SELL")
	params.Set("type", "MARKET")
	params.Set("quantity", "1")
	params.Set("newClientOrderId", "fopen-btc-1")
	_, err := engine.signedRequest(context.Background(), http.MethodPost, server.URL+"/fapi/v1/order", params)
	if err == nil || !strings.Contains(err.Error(), "order rate limiter") {
		t.Fatalf("expected local order rate limiter error, got %v", err)
	}
	if atomic.LoadInt32(&requests) != 0 {
		t.Fatalf("expected request to be blocked before exchange, got %d", requests)
	}
}

func TestSignedRequestAllowsCriticalOrderUsingReservedBudget(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	now := time.Now().UTC()
	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
	}
	engine.orderRateRequests10s = make([]time.Time, orderRateLimitCapacity10s-orderRateLimitReserve10s)
	engine.orderRateRequests1m = make([]time.Time, orderRateLimitCapacity1m-orderRateLimitReserve1m)
	for i := range engine.orderRateRequests10s {
		engine.orderRateRequests10s[i] = now
	}
	for i := range engine.orderRateRequests1m {
		engine.orderRateRequests1m[i] = now
	}

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("side", "BUY")
	params.Set("type", "MARKET")
	params.Set("quantity", "1")
	params.Set("reduceOnly", "true")
	params.Set("newClientOrderId", "ftrim-btc-1")
	if _, err := engine.signedRequest(context.Background(), http.MethodPost, server.URL+"/fapi/v1/order", params); err != nil {
		t.Fatalf("expected critical order to use reserved budget, got %v", err)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("expected one exchange request, got %d", requests)
	}
}

func TestSignedRequestAppliesExchangeOrderRateCooldownAfter1015(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-1015,"msg":"Too many new orders"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		runtimeReporter: newRuntimeReporter(nil),
	}

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("side", "SELL")
	params.Set("type", "MARKET")
	params.Set("quantity", "1")
	params.Set("newClientOrderId", "fopen-btc-2")
	_, err := engine.signedRequest(context.Background(), http.MethodPost, server.URL+"/fapi/v1/order", params)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "-1015") {
		t.Fatalf("expected exchange -1015 error, got %v", err)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("expected one exchange request before cooldown, got %d", requests)
	}
	if _, err := engine.signedRequest(context.Background(), http.MethodPost, server.URL+"/fapi/v1/order", params); err == nil || !strings.Contains(err.Error(), "remaining=") {
		t.Fatalf("expected local exchange cooldown block, got %v", err)
	}
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("expected cooldown to stop second request from hitting exchange, got %d", requests)
	}
}

func TestAssessFuturesLeverageRiskRejectsExcessConfiguredLeverage(t *testing.T) {
	brackets := []futuresLeverageBracket{
		{Bracket: 1, InitialLeverage: 2, NotionalFloor: 0, NotionalCap: 50000, MaintMarginRatio: 0.01, Cum: 0},
	}
	_, ok, detail := assessFuturesLeverageRisk("BTCUSDT", 3, 0, 100, 100, 100000, brackets)
	if ok {
		t.Fatal("expected leverage bracket risk to reject configured leverage")
	}
	if !strings.Contains(detail, "最大初始杠杆") {
		t.Fatalf("unexpected detail: %s", detail)
	}
}

func TestAssessFuturesLeverageRiskRejectsInsufficientMaintMarginBuffer(t *testing.T) {
	brackets := []futuresLeverageBracket{
		{Bracket: 1, InitialLeverage: 5, NotionalFloor: 0, NotionalCap: 50000, MaintMarginRatio: 0.10, Cum: 50},
	}
	_, ok, detail := assessFuturesLeverageRisk("BTCUSDT", 3, 0, 10, 100, 400, brackets)
	if ok {
		t.Fatal("expected leverage bracket risk to reject insufficient margin buffer")
	}
	if !strings.Contains(detail, "maintMargin") {
		t.Fatalf("unexpected detail: %s", detail)
	}
}

func TestLiquidationDistanceRate(t *testing.T) {
	// 2x 杠杆、维持保证金率 0.4%：初始保证金 50% 名义，强平距离≈(0.5-0.004)=0.496。
	low := []futuresLeverageBracket{
		{Bracket: 1, InitialLeverage: 2, NotionalFloor: 0, NotionalCap: 1000000, MaintMarginRatio: 0.004, Cum: 0},
	}
	assessment, ok, detail := assessFuturesLeverageRisk("BTCUSDT", 2, 0, 10, 100, 1000000, low)
	if !ok {
		t.Fatalf("expected leverage assessment to pass, got: %s", detail)
	}
	if d := assessment.liquidationDistanceRate(); d < 0.495 || d > 0.497 {
		t.Fatalf("2x distance = %.6f, want ~0.496", d)
	}

	// 20x 杠杆、维持保证金率 5%：初始保证金 5% 名义，强平距离≈0（被任何正阈值拦下）。
	high := []futuresLeverageBracket{
		{Bracket: 1, InitialLeverage: 20, NotionalFloor: 0, NotionalCap: 1000000, MaintMarginRatio: 0.05, Cum: 0},
	}
	highAssessment, ok, detail := assessFuturesLeverageRisk("BTCUSDT", 20, 0, 10, 100, 1000000, high)
	if !ok {
		t.Fatalf("expected high-leverage assessment to pass margin buffer, got: %s", detail)
	}
	if d := highAssessment.liquidationDistanceRate(); d > 0.01 {
		t.Fatalf("20x distance = %.6f, expected near 0", d)
	}

	// 退化输入：名义为 0 → 0；维持>初始（负距离）→ 钳到 0。
	if d := (futuresLeverageRiskAssessment{}).liquidationDistanceRate(); d != 0 {
		t.Fatalf("zero-notional distance = %.6f, want 0", d)
	}
	negative := futuresLeverageRiskAssessment{ProjectedNotionalUSDT: 1000, InitialMarginUSDT: 1, MaintenanceMarginUSDT: 5}
	if d := negative.liquidationDistanceRate(); d != 0 {
		t.Fatalf("negative distance should clamp to 0, got %.6f", d)
	}
}

func TestVerifyPreparedPlanFundingStabilityAcceptsStableSamples(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/premiumIndex":
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"100","estimatedSettlePrice":"100.70","lastFundingRate":"0.0070","interestRate":"0.0001","nextFundingTime":%d,"time":%d}`,
				time.Now().Add(time.Minute).UnixMilli(), time.Now().UnixMilli())))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    "https://api.binance.com",
			FuturesBaseURL: server.URL,
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	plan := openSequencePlan{
		target: &MonitoredTarget{
			Candidate: FundingCandidate{
				Symbol:        "BTCUSDT",
				Direction:     fundingCarryDirectionPositive,
				EstimatedRate: 0.0070,
			},
		},
	}

	ok, detail := engine.verifyPreparedPlanFundingStability(context.Background(), &plan, time.Now().Add(600*time.Millisecond))
	if !ok {
		t.Fatalf("expected stable funding samples to pass, got detail=%s", detail)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected multiple premiumIndex samples, got %d", calls)
	}
}

func TestVerifyPreparedPlanFundingStabilityRejectsDirectionFlip(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/premiumIndex":
			call := atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			rate := "0.0070"
			settlePrice := "100.70"
			if call >= 2 {
				rate = "-0.0070"
				settlePrice = "99.30"
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"100","estimatedSettlePrice":"%s","lastFundingRate":"%s","interestRate":"0.0001","nextFundingTime":%d,"time":%d}`,
				settlePrice, rate, time.Now().Add(time.Minute).UnixMilli(), time.Now().UnixMilli())))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    "https://api.binance.com",
			FuturesBaseURL: server.URL,
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	plan := openSequencePlan{
		target: &MonitoredTarget{
			Candidate: FundingCandidate{
				Symbol:        "BTCUSDT",
				Direction:     fundingCarryDirectionPositive,
				EstimatedRate: 0.0070,
			},
		},
	}

	ok, detail := engine.verifyPreparedPlanFundingStability(context.Background(), &plan, time.Now().Add(600*time.Millisecond))
	if ok {
		t.Fatalf("expected funding direction flip to fail stability gate, detail=%s", detail)
	}
	if !strings.Contains(detail, "方向翻转") {
		t.Fatalf("expected direction flip detail, got %s", detail)
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
	transport, ok := client.HTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.HTTPClient().Transport)
	}
	if transport.MaxIdleConns != 200 || transport.MaxIdleConnsPerHost != 80 {
		t.Fatalf("unexpected transport pool config: %+v", transport)
	}
	if client.HTTPClient().Timeout != 9*time.Second {
		t.Fatalf("unexpected client timeout: %s", client.HTTPClient().Timeout)
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

func TestNoteProtectedIOCObservationAccumulatesStats(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		symbolCooldowns: make(map[string]symbolCooldownState),
	}

	engine.noteProtectedIOCObservation("BTCUSDT", "futures", newProtectedIOCObservation(10, 6, "EXPIRED"))
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
	got := estimateReservedNetProfitRate(0.0025, 0.0001, 0.0030, 0.0004, 0)
	if diff := got - (-0.001); diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("unexpected reserved net profit rate: %.6f", got)
	}
}

func TestEstimateReservedNetProfitRateIncludesBorrowReserve(t *testing.T) {
	got := estimateReservedNetProfitRate(0.0025, 0.0001, 0.0030, 0.0004, 0.0006)
	if diff := got - (-0.0016); diff < -1e-12 || diff > 1e-12 {
		t.Fatalf("unexpected reserved net profit rate with borrow reserve: %.6f", got)
	}
}

func TestEstimateTargetCapitalRequirementIncludesBorrowReserveForNegativeCarry(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
		borrowHourlyInterestByAsset: map[string]float64{
			"BTC": 0.0015,
		},
	}
	engine.cfg.HistoricalConfig.SpotTakerFeeRate = 0.001
	engine.cfg.HistoricalConfig.FuturesTakerFeeRate = 0.0005
	engine.cfg.HistoricalConfig.BorrowInterestMinBillingHours = 2

	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:          "BTCUSDT",
			Direction:       fundingCarryDirectionNegative,
			SpotBaseAsset:   "BTC",
			OrderQuantity:   2,
			MarkPrice:       100,
			NextFundingTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		},
	}
	entryTime := time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC)

	spotRequired, futuresRequired := engine.estimateTargetCapitalRequirement(target, 100, 101, entryTime)
	if diff := spotRequired - 200.8; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("unexpected spot capital requirement: %.6f", spotRequired)
	}
	if diff := futuresRequired - 202.101; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("unexpected futures capital requirement: %.6f", futuresRequired)
	}
}

func TestMonitorTargetLabelsIncludeBorrowReserve(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: DefaultEngineConfig(),
		borrowHourlyInterestByAsset: map[string]float64{
			"BTC": 0.0015,
		},
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:          "BTCUSDT",
			Direction:       fundingCarryDirectionNegative,
			EstimatedRate:   0.0025,
			SpotBaseAsset:   "BTC",
			OrderQuantity:   1,
			MarkPrice:       100,
			NextFundingTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		},
	}

	engine.setCurrentMonitorTargets([]*MonitoredTarget{target})
	if got := strings.Join(engine.currentMonitorTargets, " "); !strings.Contains(got, "borrow=") || !strings.Contains(got, "funding=") {
		t.Fatalf("expected monitor labels to include funding and borrow reserve, got %q", got)
	}

	target.LastNetProfit = 0.0012
	target.LastBorrowReserveRate = 0.0006
	engine.updateCurrentMonitorTargetLabel(target)
	if got := strings.Join(engine.currentMonitorTargets, " "); !strings.Contains(got, "net=0.1200%") || !strings.Contains(got, "borrow=0.0600%") {
		t.Fatalf("expected updated monitor label to reflect runtime reserve values, got %q", got)
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
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
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
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
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
