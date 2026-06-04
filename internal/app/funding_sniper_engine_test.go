package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
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

func TestAssertHedgeInvariantTripsFuseOnBreach(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.HedgeImbalanceAbortUSDT = 20
	cfg.RiskConfig.HedgeImbalanceAbortRate = 0
	engine := &FundingRateSniperEngine{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}

	// 两腿基本平衡：不熔断。
	if err := engine.assertHedgeInvariant("BTCUSDT", 1000, 995); err != nil {
		t.Fatalf("balanced hedge should not trip fuse, got %v", err)
	}
	if engine.platformState.FuseTripped {
		t.Fatal("fuse should remain clear for balanced hedge")
	}

	// 偏离超过 20 USDT：必须熔断。
	err := engine.assertHedgeInvariant("BTCUSDT", 1000, 950)
	var fuseErr platformFuseError
	if !errors.As(err, &fuseErr) {
		t.Fatalf("expected platform fuse error on imbalance breach, got %v", err)
	}
	if !engine.platformState.FuseTripped {
		t.Fatal("expected platform fuse to be tripped on imbalance breach")
	}
}

func TestAssertHedgeInvariantDisabledWhenThresholdsZero(t *testing.T) {
	cfg := DefaultEngineConfig()
	// 两个阈值都显式置 0：完全关闭关键不变量守卫，即使两腿完全不平衡也不应熔断。
	cfg.RiskConfig.HedgeImbalanceAbortUSDT = 0
	cfg.RiskConfig.HedgeImbalanceAbortRate = 0
	engine := &FundingRateSniperEngine{
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
	}
	if err := engine.assertHedgeInvariant("BTCUSDT", 1000, 0); err != nil {
		t.Fatalf("guard disabled (both thresholds 0) should not trip fuse, got %v", err)
	}
	if engine.platformState.FuseTripped {
		t.Fatal("fuse should remain clear when guard disabled")
	}
}

func TestAssertHedgeInvariantEnabledByDefault(t *testing.T) {
	// 默认配置开启相对偏离维度(HedgeImbalanceAbortRate=0.10)：单腿裸露(futures=0, rate=100%)必须熔断。
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	err := engine.assertHedgeInvariant("BTCUSDT", 1000, 0)
	var fuseErr platformFuseError
	if !errors.As(err, &fuseErr) {
		t.Fatalf("default config should trip fuse on naked leg, got %v", err)
	}
	if !engine.platformState.FuseTripped {
		t.Fatal("expected fuse tripped on naked leg under default config")
	}

	// 默认 rate=0.10 应容忍正常开仓的小幅名义偏离(此处约 3%，源于步进取整/基差)。
	tolerant := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
	if err := tolerant.assertHedgeInvariant("BTCUSDT", 1000, 970); err != nil {
		t.Fatalf("default rate guard should tolerate ~3%% imbalance, got %v", err)
	}
	if tolerant.platformState.FuseTripped {
		t.Fatal("fuse should remain clear for ~3% imbalance under default config")
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
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
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
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
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
