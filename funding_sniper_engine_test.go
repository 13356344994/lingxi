package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestOpenHedgeLifecycleTransitions(t *testing.T) {
	hedge := &OpenHedge{HedgeQty: 1}
	hedge.markPendingValidation("awaiting validation")
	if hedge.LifecycleState != OpenHedgeLifecyclePendingValidation {
		t.Fatalf("expected pending validation, got %s", hedge.LifecycleState)
	}

	hedge.markActive("validated")
	if hedge.LifecycleState != OpenHedgeLifecycleActive {
		t.Fatalf("expected active, got %s", hedge.LifecycleState)
	}

	hedge.markClosing("close started")
	hedge.CloseFuturesExecutedQty = 1
	hedge.CloseSpotExecutedQty = 1
	hedge.completeCloseLifecycle(false, "closed cleanly")
	if hedge.LifecycleState != OpenHedgeLifecycleClosed {
		t.Fatalf("expected closed, got %s", hedge.LifecycleState)
	}

	hedge.CloseSpotExecutedQty = 0.4
	hedge.completeCloseLifecycle(true, "abort left residual")
	if hedge.LifecycleState != OpenHedgeLifecycleAbortedWithResidual {
		t.Fatalf("expected aborted with residual, got %s", hedge.LifecycleState)
	}
	if hedge.LifecycleUpdatedAt.IsZero() {
		t.Fatal("expected lifecycle updated timestamp to be recorded")
	}
}

func TestScheduleExposureRescueOrFailWrapsFailureMessage(t *testing.T) {
	var engine *FundingRateSniperEngine
	err := engine.scheduleExposureRescueOrFail(context.Background(), exposureRescueTask{
		Mode:   exposureRescueModeOpen,
		Phase:  "open_futures_unknown",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}, "托管失败", "已转入 ExposureManager 托管")
	if err == nil || !strings.Contains(err.Error(), "托管失败") {
		t.Fatalf("unexpected rescue result: %v", err)
	}
	if !strings.Contains(err.Error(), "exposure manager 未初始化") {
		t.Fatalf("expected wrapped rescue failure, got %v", err)
	}
}

func TestRecoveryActionResultFeeHelpers(t *testing.T) {
	engine := &FundingRateSniperEngine{
		logger:   log.New(io.Discard, "", 0),
		spotMeta: map[string]SpotSymbol{"BTCUSDT": {BaseAsset: "BTC"}},
	}
	result := recoveryActionResult{
		SpotOrder: SpotOrderResponse{
			Symbol:              "BTCUSDT",
			ExecutedQty:         "1",
			CummulativeQuoteQty: "100",
			Fills:               []SpotFill{{Commission: "0.25", CommissionAsset: "USDT"}},
		},
		FuturesExecution: OrderExecutionSnapshot{
			Symbol:          "BTCUSDT",
			OrderID:         1,
			ExecutedQty:     1,
			AvgPrice:        100,
			Commission:      0.5,
			CommissionAsset: "USDT",
		},
	}

	if got := result.spotFeeUSDT(engine, context.Background()); got != 0.25 {
		t.Fatalf("expected spot fee 0.25, got %.8f", got)
	}
	if got := result.futuresFeeUSDT(engine, context.Background()); got != 0.5 {
		t.Fatalf("expected futures fee 0.5, got %.8f", got)
	}
}

func TestRecoveryActionResultApplyCloseProtocolMarksRecoveredState(t *testing.T) {
	hedge := &OpenHedge{HedgeQty: 1}
	result := recoveryActionResult{
		Summary:       "spot close failed, hedge restored",
		RestoredHedge: true,
		PartialClose:  true,
	}

	result.applyCloseProtocol(hedge, "fallback")
	if hedge.LifecycleState != OpenHedgeLifecycleCloseRecoveredPart {
		t.Fatalf("expected close recovered partial, got %s", hedge.LifecycleState)
	}
	if hedge.LifecycleReason != "spot close failed, hedge restored" {
		t.Fatalf("unexpected lifecycle reason: %s", hedge.LifecycleReason)
	}
}

func TestRecoveryActionResultApplyCloseProtocolMarksEscalatedState(t *testing.T) {
	hedge := &OpenHedge{HedgeQty: 1}
	result := recoveryActionResult{
		Summary:   "unknown order escalated",
		Escalated: true,
	}

	result.applyCloseProtocol(hedge, "fallback")
	if hedge.LifecycleState != OpenHedgeLifecycleEscalated {
		t.Fatalf("expected escalated, got %s", hedge.LifecycleState)
	}
}

func TestRecoveryActionResultCloseDecisionRecoveredPartial(t *testing.T) {
	result := recoveryActionResult{
		Summary:       "partial close restored hedge",
		RestoredHedge: true,
		PartialClose:  true,
	}
	decision := result.closeDecision("fallback")
	if decision.Outcome != RecoveryOutcomeCloseRecoveredPt {
		t.Fatalf("expected recovered partial outcome, got %s", decision.Outcome)
	}
	if decision.LifecycleState != OpenHedgeLifecycleCloseRecoveredPart {
		t.Fatalf("expected close recovered partial state, got %s", decision.LifecycleState)
	}
	if !decision.RestoredHedge {
		t.Fatal("expected restored hedge decision")
	}
}

func TestRecoveryActionResultCloseDecisionEscalated(t *testing.T) {
	result := recoveryActionResult{
		Summary:   "escalated to exposure manager",
		Escalated: true,
	}
	decision := result.closeDecision("fallback")
	if decision.Outcome != RecoveryOutcomeEscalated {
		t.Fatalf("expected escalated outcome, got %s", decision.Outcome)
	}
	if !decision.RequiresEscalation {
		t.Fatal("expected escalation flag")
	}
}

func TestRecoveryProtocolResultDelegatesRecoveryBehavior(t *testing.T) {
	hedge := &OpenHedge{HedgeQty: 1}
	result := RecoveryProtocolResult{
		ProtocolResult: ProtocolResult{
			Summary: "protocol summary",
		},
		Action: recoveryActionResult{
			RestoredHedge: true,
			PartialClose:  true,
		},
	}

	if got := result.lifecycleReason("fallback"); got != "protocol summary" {
		t.Fatalf("expected protocol summary, got %q", got)
	}
	if err := result.openError("fallback", nil); err == nil || err.Error() != "protocol summary" {
		t.Fatalf("expected delegated open error, got %v", err)
	}

	result.applyCloseProtocol(hedge, "fallback")
	if hedge.LifecycleState != OpenHedgeLifecycleCloseRecoveredPart {
		t.Fatalf("expected close recovered partial, got %s", hedge.LifecycleState)
	}
	if hedge.LifecycleReason != "protocol summary" {
		t.Fatalf("unexpected lifecycle reason: %s", hedge.LifecycleReason)
	}
}

func TestOpenLifecycleReasonWithProtocolResults(t *testing.T) {
	detail := openLifecycleReason(
		"open orders filled; awaiting post-open validation",
		RecoveryProtocolResult{
			ProtocolResult: ProtocolResult{Summary: "spot excess rebalanced"},
			Action: recoveryActionResult{
				ProtocolAction: "rebalance_spot_excess",
				Summary:        "spot excess rebalanced",
			},
		},
		RecoveryProtocolResult{
			ProtocolResult: ProtocolResult{Summary: "futures residual closed"},
			Action: recoveryActionResult{
				ProtocolAction: "close_futures_residual",
				Summary:        "futures residual closed",
			},
		},
	)
	if detail != "open orders filled; awaiting post-open validation | spot excess rebalanced | futures residual closed" {
		t.Fatalf("unexpected lifecycle detail: %s", detail)
	}
}

func TestRecoveryActionResultOpenDecisionEscalatedAndReverted(t *testing.T) {
	escalated := recoveryActionResult{
		Summary:   "open unknown escalated",
		Escalated: true,
	}
	escalatedDecision := escalated.openDecision("fallback")
	if escalatedDecision.Outcome != OpenRecoveryOutcomeEscalated {
		t.Fatalf("expected escalated open outcome, got %s", escalatedDecision.Outcome)
	}
	if !escalatedDecision.RequiresEscalation {
		t.Fatal("expected escalation requirement")
	}

	reverted := recoveryActionResult{
		ProtocolAction: "close_futures_after_spot_open_failure",
		Summary:        "spot open failed, futures reverted",
		Reverted:       true,
	}
	revertedDecision := reverted.openDecision("fallback")
	if revertedDecision.Outcome != OpenRecoveryOutcomeReverted {
		t.Fatalf("expected reverted open outcome, got %s", revertedDecision.Outcome)
	}
	if !revertedDecision.Reverted {
		t.Fatal("expected reverted flag")
	}
}

func TestRecoveryActionResultOpenDecisionAbortedAndOpened(t *testing.T) {
	aborted := recoveryActionResult{
		ProtocolAction: "abort_after_adverse_selection",
		Summary:        "opened but aborted immediately",
		Aborted:        true,
	}
	abortedDecision := aborted.openDecision("fallback")
	if abortedDecision.Outcome != OpenRecoveryOutcomeAborted {
		t.Fatalf("expected aborted open outcome, got %s", abortedDecision.Outcome)
	}
	if !abortedDecision.Aborted {
		t.Fatal("expected aborted flag")
	}

	opened := recoveryActionResult{
		ProtocolAction: "rebalance_spot_excess",
		Summary:        "spot excess rebalanced",
	}
	openedDecision := opened.openDecision("fallback")
	if openedDecision.Outcome != OpenRecoveryOutcomeOpened {
		t.Fatalf("expected opened outcome for rebalance-only action, got %s", openedDecision.Outcome)
	}
	if openedDecision.Reverted || openedDecision.Aborted || openedDecision.RequiresEscalation {
		t.Fatalf("unexpected opened decision flags: %+v", openedDecision)
	}
}

func TestBuildOpenExposureRescueTaskCarriesOpenContext(t *testing.T) {
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol:            "BTCUSDT",
			Pair:              "BTCUSDT",
			SpotBaseAsset:     "BTC",
			SpotBasePrecision: 3,
		},
	}

	task := buildOpenExposureRescueTask(
		target,
		fundingCarryDirectionPositive,
		"parallel_open_spot_unknown",
		101.25,
		"spot-cid",
		"futures-cid",
		0.4,
		0.5,
	)

	if task.Mode != exposureRescueModeOpen {
		t.Fatalf("expected open rescue mode, got %s", task.Mode)
	}
	if task.Phase != "parallel_open_spot_unknown" {
		t.Fatalf("unexpected phase: %s", task.Phase)
	}
	if task.Symbol != "BTCUSDT" || task.Pair != "BTCUSDT" {
		t.Fatalf("unexpected symbol context: %+v", task)
	}
	if task.SpotBaseAsset != "BTC" || task.SpotBasePrecision != 3 {
		t.Fatalf("unexpected spot metadata: %+v", task)
	}
	if task.SpotClientOrderID != "spot-cid" || task.FuturesClientOrderID != "futures-cid" {
		t.Fatalf("unexpected order ids: %+v", task)
	}
	if task.KnownSpotExecutedQty != 0.4 || task.KnownFuturesExecutedQty != 0.5 {
		t.Fatalf("unexpected known quantities: %+v", task)
	}
}

func TestParallelOpenResolutionDecisionOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		resolution parallelOpenResolution
		want       parallelOpenDecisionOutcome
	}{
		{
			name: "futures failed",
			resolution: parallelOpenResolution{
				futuresResult: parallelOpenFuturesResult{err: errors.New("timeout")},
			},
			want: parallelOpenDecisionFuturesFailed,
		},
		{
			name: "zero futures fill",
			resolution: parallelOpenResolution{
				futuresResult: parallelOpenFuturesResult{snapshot: OrderExecutionSnapshot{ExecutedQty: 0}},
			},
			want: parallelOpenDecisionZeroFuturesFill,
		},
		{
			name: "spot failed",
			resolution: parallelOpenResolution{
				futuresResult: parallelOpenFuturesResult{snapshot: OrderExecutionSnapshot{ExecutedQty: 1}},
				spotResult:    parallelOpenSpotResult{err: errors.New("spot reject")},
			},
			want: parallelOpenDecisionSpotFailed,
		},
		{
			name: "proceed",
			resolution: parallelOpenResolution{
				futuresResult: parallelOpenFuturesResult{snapshot: OrderExecutionSnapshot{ExecutedQty: 1}},
				spotResult:    parallelOpenSpotResult{},
			},
			want: parallelOpenDecisionProceed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := tc.resolution.decision()
			if decision.Outcome != tc.want {
				t.Fatalf("expected outcome %s, got %s", tc.want, decision.Outcome)
			}
		})
	}
}

func TestApplyMarketRiskDecisionSetsSymbolCooldownWithoutInvalidation(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
	}
	engine.cfg.RiskConfig.SymbolAbnormalCooldown = time.Minute
	target := &MonitoredTarget{
		Candidate: FundingCandidate{Symbol: "BTCUSDT"},
		IsValid:   1,
	}

	err := engine.applyMarketRiskDecision(target, MarketRiskDecision{
		Category: "liquidity",
		Severity: "major",
		Action:   RiskActionTighten,
	}, marketRiskActionOptions{
		detail:               "容量不足",
		symbolCooldownReason: "容量不足",
	})
	if err != nil {
		t.Fatalf("applyMarketRiskDecision() error = %v", err)
	}
	if atomic.LoadInt32(&target.IsValid) != 1 {
		t.Fatal("expected target to remain valid")
	}
	if target.LastMarketRiskAction != string(RiskActionTighten) {
		t.Fatalf("unexpected action: %s", target.LastMarketRiskAction)
	}
	state, ok := engine.symbolCooldowns["BTCUSDT"]
	if !ok {
		t.Fatal("expected symbol cooldown to be recorded")
	}
	if state.Category != "liquidity" || state.Severity != "major" {
		t.Fatalf("unexpected cooldown state: %+v", state)
	}
}

func TestApplyMarketRiskDecisionGlobalPauseInvalidatesTargetAndTripsFuse(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{Symbol: "BTCUSDT"},
		IsValid:   1,
	}

	err := engine.applyMarketRiskDecision(target, MarketRiskDecision{
		Category: "liquidity",
		Severity: "critical",
		Action:   RiskActionPauseCycle,
	}, marketRiskActionOptions{
		detail:                  "流动性极差",
		netProfit:               -0.2,
		invalidateOnGlobalPause: true,
		globalPauseReason:       "symbol=BTCUSDT 流动性极差",
	})
	var fuseErr platformFuseError
	if !errors.As(err, &fuseErr) {
		t.Fatalf("expected platform fuse error, got %v", err)
	}
	if atomic.LoadInt32(&target.IsValid) != 0 {
		t.Fatal("expected target to be invalidated")
	}
	if !engine.platformState.FuseTripped {
		t.Fatal("expected platform fuse to be tripped")
	}
	if target.LastMarketRiskAction != string(RiskActionPauseCycle) {
		t.Fatalf("unexpected action: %s", target.LastMarketRiskAction)
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

func TestPlaceFuturesBatchOrdersIdempotentFallsBackAfterExpiredProtectedOpen(t *testing.T) {
	var batchCalls int32
	var singleOrderCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/batchOrders":
			atomic.AddInt32(&batchCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"symbol":"BTCUSDT","orderId":1,"status":"EXPIRED","clientOrderId":"cid-1","executedQty":"0","origQty":"1","side":"SELL"}]`))
		case "/fapi/v1/ticker/bookTicker":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","bidPrice":"100","askPrice":"100.1"}`))
		case "/fapi/v1/order":
			atomic.AddInt32(&singleOrderCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":2,"status":"FILLED","clientOrderId":"cid-2","executedQty":"1","origQty":"1","avgPrice":"100","side":"SELL"}`))
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
		cfg:                DefaultEngineConfig(),
		logger:             log.New(io.Discard, "", 0),
		runtimeReporter:    newRuntimeReporter(nil),
		futuresTickerCache: NewBookTickerCache(),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
		futuresOrderHub: NewOrderEventHub(),
	}
	engine.cfg.ExecutionConfig.FuturesLimitSlippageRate = 0.001
	engine.futuresTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, time.Now().UTC())

	results, err := engine.placeFuturesBatchOrdersIdempotent(context.Background(), []FuturesBatchOrderRequest{{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             "LIMIT",
		Quantity:         "1",
		Price:            "99.9",
		TimeInForce:      "IOC",
		NewClientOrderID: "cid-1",
		NewOrderRespType: "RESULT",
	}})
	if err != nil {
		t.Fatalf("placeFuturesBatchOrdersIdempotent() error = %v", err)
	}
	if atomic.LoadInt32(&batchCalls) != 1 {
		t.Fatalf("expected one batch call, got %d", batchCalls)
	}
	if atomic.LoadInt32(&singleOrderCalls) < 1 {
		t.Fatalf("expected fallback single futures order call, got %d", singleOrderCalls)
	}
	if len(results) != 1 || results[0].OrderID != 2 {
		t.Fatalf("expected fallback result to replace expired batch result, got %+v", results)
	}
}

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
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
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
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
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
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
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

func TestVerifyPreparedPlanFundingStabilityAcceptsStableSamples(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/premiumIndex":
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"100","estimatedSettlePrice":"100.30","lastFundingRate":"0.0030","interestRate":"0.0001","nextFundingTime":%d,"time":%d}`,
				time.Now().Add(time.Minute).UnixMilli(), time.Now().UnixMilli())))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    "https://api.binance.com",
			futuresBaseURL: server.URL,
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	plan := openSequencePlan{
		target: &MonitoredTarget{
			Candidate: FundingCandidate{
				Symbol:        "BTCUSDT",
				Direction:     fundingCarryDirectionPositive,
				EstimatedRate: 0.0030,
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
			rate := "0.0030"
			settlePrice := "100.30"
			if call >= 2 {
				rate = "-0.0030"
				settlePrice = "99.70"
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"100","estimatedSettlePrice":"%s","lastFundingRate":"%s","interestRate":"0.0001","nextFundingTime":%d,"time":%d}`,
				settlePrice, rate, time.Now().Add(time.Minute).UnixMilli(), time.Now().UnixMilli())))
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			spotBaseURL:    "https://api.binance.com",
			futuresBaseURL: server.URL,
			httpClient:     server.Client(),
		},
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	plan := openSequencePlan{
		target: &MonitoredTarget{
			Candidate: FundingCandidate{
				Symbol:        "BTCUSDT",
				Direction:     fundingCarryDirectionPositive,
				EstimatedRate: 0.0030,
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

func TestFetchFuturesAccountStateUsesCacheAndStaleFallback(t *testing.T) {
	accountCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v3/account":
			accountCalls++
			if accountCalls == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"250","availableBalance":"200"}],"positions":[{"symbol":"BTCUSDT","positionAmt":"1.25"}]}`))
				return
			}
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"Way too many requests"}`))
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
		logger: log.New(io.Discard, "", 0),
	}

	walletUSDT, availableUSDT, positions, err := engine.fetchFuturesAccountState(context.Background())
	if err != nil {
		t.Fatalf("fetchFuturesAccountState() first error = %v", err)
	}
	if walletUSDT != 250 || availableUSDT != 200 || positions["BTCUSDT"] != 1.25 {
		t.Fatalf("unexpected first futures account state: wallet=%.2f available=%.2f positions=%+v", walletUSDT, availableUSDT, positions)
	}

	_, _, _, err = engine.fetchFuturesAccountState(context.Background())
	if err != nil {
		t.Fatalf("fetchFuturesAccountState() cache hit error = %v", err)
	}
	if accountCalls != 1 {
		t.Fatalf("expected hot cache to avoid extra request, got %d calls", accountCalls)
	}

	engine.futuresAccountStateMu.Lock()
	engine.futuresAccountStateCachedAt = time.Now().Add(-(futuresAccountStateCacheTTL + time.Second))
	engine.futuresAccountStateMu.Unlock()

	walletUSDT, availableUSDT, positions, err = engine.fetchFuturesAccountState(context.Background())
	if err != nil {
		t.Fatalf("fetchFuturesAccountState() stale fallback error = %v", err)
	}
	if walletUSDT != 250 || availableUSDT != 200 || positions["BTCUSDT"] != 1.25 {
		t.Fatalf("unexpected stale fallback state: wallet=%.2f available=%.2f positions=%+v", walletUSDT, availableUSDT, positions)
	}
	if accountCalls != 2 {
		t.Fatalf("expected one retry request before stale fallback, got %d calls", accountCalls)
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
	if hedge.LifecycleState != OpenHedgeLifecycleAborted {
		t.Fatalf("expected aborted lifecycle after immediate unwind, got %s", hedge.LifecycleState)
	}
	if !strings.Contains(hedge.LifecycleReason, "已即时撤退") {
		t.Fatalf("expected aborted lifecycle reason to mention immediate unwind, got %s", hedge.LifecycleReason)
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
			if engine.riskBudgetState.DailyRealizedPnL != 1.25 {
				t.Fatalf("expected daily pnl to be adjusted by funding delta, got %.8f", engine.riskBudgetState.DailyRealizedPnL)
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
