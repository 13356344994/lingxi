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

	"lingxi/internal/exchange"
)

func TestFlattenUncertainFuturesOpenQueriesCancelsAndTrimsExecutedQty(t *testing.T) {
	var (
		queryCalls  int32
		cancelCalls int32
		trimCalls   int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fapi/v1/order":
			switch r.Method {
			case http.MethodGet:
				atomic.AddInt32(&queryCalls, 1)
				_, _ = w.Write([]byte(`{
					"symbol":"BTCUSDT",
					"orderId":101,
					"clientOrderId":"cid-uncertain",
					"status":"NEW",
					"side":"SELL",
					"executedQty":"0.4",
					"avgPrice":"100",
					"updateTime":1700000000000
				}`))
			case http.MethodDelete:
				atomic.AddInt32(&cancelCalls, 1)
				_, _ = w.Write([]byte(`{
					"symbol":"BTCUSDT",
					"orderId":101,
					"clientOrderId":"cid-uncertain",
					"status":"CANCELED",
					"side":"SELL",
					"executedQty":"0.4",
					"avgPrice":"100",
					"updateTime":1700000001000
				}`))
			case http.MethodPost:
				atomic.AddInt32(&trimCalls, 1)
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm() error = %v", err)
				}
				if got := r.Form.Get("side"); got != "BUY" {
					t.Fatalf("expected trim side BUY, got %q", got)
				}
				if got := r.Form.Get("reduceOnly"); got != "true" {
					t.Fatalf("expected reduceOnly=true, got %q", got)
				}
				if got := r.Form.Get("quantity"); got != "0.400" {
					t.Fatalf("expected trim quantity 0.400, got %q", got)
				}
				if got := r.Form.Get("newClientOrderId"); !strings.HasPrefix(got, "ftrim-") {
					t.Fatalf("expected trim clientOrderId prefix ftrim-, got %q", got)
				}
				_, _ = w.Write([]byte(`{
					"symbol":"BTCUSDT",
					"orderId":202,
					"clientOrderId":"ftrim-btcusdt-1",
					"status":"FILLED",
					"side":"BUY",
					"executedQty":"0.4",
					"avgPrice":"100",
					"updateTime":1700000002000
				}`))
			default:
				t.Fatalf("unexpected method: %s", r.Method)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := DefaultEngineConfig()
	cfg.ExecutionConfig.UseProtectedIOC = false

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:    cfg,
		logger: log.New(io.Discard, "", 0),
		futuresRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, MinQty: 0.001, MinNotional: 1},
		},
	}

	protocol := newRecoveryProtocol(engine)
	result := protocol.FlattenUncertainFuturesOpen(context.Background(), "BTCUSDT", "cid-uncertain", FuturesBatchOrderResult{
		Symbol:        "BTCUSDT",
		ClientOrderID: "cid-uncertain",
		Status:        "NEW",
		Side:          "SELL",
		ExecutedQty:   "0.1",
	})

	if atomic.LoadInt32(&queryCalls) < 1 {
		t.Fatalf("expected at least one query call, got %d", queryCalls)
	}
	if atomic.LoadInt32(&cancelCalls) != 1 {
		t.Fatalf("expected one cancel call, got %d", cancelCalls)
	}
	if atomic.LoadInt32(&trimCalls) != 1 {
		t.Fatalf("expected one trim call, got %d", trimCalls)
	}
	if !result.Successful || !result.Action.Reverted {
		t.Fatalf("expected successful reverted recovery result, got %+v", result)
	}
	if result.Protocol != "recovery.flatten_uncertain_futures_open" {
		t.Fatalf("expected recovery protocol name, got %q", result.Protocol)
	}
	if result.Lifecycle.NextStage != LifecycleStageMonitoring {
		t.Fatalf("expected lifecycle next stage monitoring, got %q", result.Lifecycle.NextStage)
	}
	if result.Risk.Action != RiskActionObserve {
		t.Fatalf("expected observe risk action, got %q", result.Risk.Action)
	}
	if len(result.Events) != 1 || result.Events[0].Type != EventTypeRecoveryStarted {
		t.Fatalf("expected one recovery_started event, got %+v", result.Events)
	}
}

func TestDecideUncertainFuturesOpenFallback(t *testing.T) {
	protocol := newRecoveryProtocol(&FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	})

	noExposure := protocol.decideUncertainFuturesOpenFallback("BTCUSDT", "cid-empty", FuturesBatchOrderResult{
		Symbol:      "BTCUSDT",
		Status:      "CANCELED",
		Side:        "SELL",
		ExecutedQty: "0",
	})
	if noExposure.Outcome != uncertainFuturesOpenFallbackNoExposure || noExposure.TrimQty != 0 {
		t.Fatalf("expected no-exposure decision, got %+v", noExposure)
	}

	trimSell := protocol.decideUncertainFuturesOpenFallback("BTCUSDT", "cid-sell", FuturesBatchOrderResult{
		Symbol:      "BTCUSDT",
		Status:      "CANCELED",
		Side:        "SELL",
		ExecutedQty: "0.25",
	})
	if trimSell.Outcome != uncertainFuturesOpenFallbackTrimRequired || trimSell.TrimSide != "BUY" || trimSell.TrimQty != 0.25 {
		t.Fatalf("expected sell exposure to trim with BUY, got %+v", trimSell)
	}

	trimBuy := protocol.decideUncertainFuturesOpenFallback("BTCUSDT", "cid-buy", FuturesBatchOrderResult{
		Symbol:      "BTCUSDT",
		Status:      "CANCELED",
		Side:        "BUY",
		ExecutedQty: "0.75",
	})
	if trimBuy.Outcome != uncertainFuturesOpenFallbackTrimRequired || trimBuy.TrimSide != "SELL" || trimBuy.TrimQty != 0.75 {
		t.Fatalf("expected buy exposure to trim with SELL, got %+v", trimBuy)
	}
}

func TestFlattenUncertainFuturesOpenReturnsNoExposureProtocolResult(t *testing.T) {
	protocol := newRecoveryProtocol(&FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	})

	result := protocol.FlattenUncertainFuturesOpen(context.Background(), "BTCUSDT", "", FuturesBatchOrderResult{
		Symbol:      "BTCUSDT",
		Status:      "CANCELED",
		Side:        "SELL",
		ExecutedQty: "0",
	})

	if !result.Successful || !result.Action.Aborted || result.Action.Reverted || result.Action.Escalated {
		t.Fatalf("expected successful aborted/no-exposure action, got %+v", result.Action)
	}
	if result.Lifecycle.NextStage != LifecycleStageMonitoring {
		t.Fatalf("expected lifecycle next stage monitoring, got %q", result.Lifecycle.NextStage)
	}
	if result.Risk.Action != RiskActionObserve || !result.Risk.CanContinue {
		t.Fatalf("expected observe risk with canContinue, got %+v", result.Risk)
	}
	if len(result.Events) != 1 || result.Events[0].Type != EventTypeRecoveryStarted {
		t.Fatalf("expected one recovery_started event, got %+v", result.Events)
	}
}

func TestNewRecoveryActionProtocolResult(t *testing.T) {
	protocol := newRecoveryProtocol(&FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	})

	success := protocol.newRecoveryActionProtocolResult(
		"recovery.open_futures_revert",
		"open_futures_revert",
		"fallback",
		recoveryActionResult{
			ProtocolAction: "close_futures_after_zero_spot_fill",
			Summary:        "open revert completed",
			Reverted:       true,
		},
		nil,
	)
	if !success.Successful || success.Risk.Action != RiskActionObserve || !success.Risk.CanContinue {
		t.Fatalf("expected successful observe result, got %+v", success)
	}
	if success.Lifecycle.NextStage != LifecycleStageRecovering {
		t.Fatalf("expected next stage recovering, got %q", success.Lifecycle.NextStage)
	}
	if len(success.Events) != 1 || success.Events[0].Type != EventTypeRecoveryStarted {
		t.Fatalf("expected one recovery_started event, got %+v", success.Events)
	}

	failed := protocol.newRecoveryActionProtocolResult(
		"recovery.spot_market",
		"spot_recovery_market",
		"fallback",
		recoveryActionResult{},
		errors.New("boom"),
	)
	if failed.Successful || failed.Risk.Action != RiskActionEscalateRecovery || failed.Risk.CanContinue {
		t.Fatalf("expected escalated result, got %+v", failed)
	}
	if len(failed.Events) != 2 || failed.Events[1].Type != EventTypeRecoveryEscalated {
		t.Fatalf("expected recovery_escalated event, got %+v", failed.Events)
	}
}
