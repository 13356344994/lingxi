package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExposureManagerSubmitDoesNotBlockOnTaskCompletion(t *testing.T) {
	manager := &ExposureManager{
		logger:    log.New(io.Discard, "", 0),
		taskQueue: make(chan exposureRescueTask, 1),
	}

	done := make(chan error, 1)
	task := exposureRescueTask{
		Phase:  "open_recovery",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}
	go func() {
		done <- manager.Submit(context.Background(), task)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected Submit() to return immediately after enqueue")
	}

	select {
	case queued := <-manager.taskQueue:
		if queued.Symbol != task.Symbol || queued.Phase != task.Phase {
			t.Fatalf("unexpected queued task: %+v", queued)
		}
	default:
		t.Fatal("expected task to be enqueued")
	}
}

func TestExposureManagerFlattenSpotResidualUsesSpotReferenceAnchor(t *testing.T) {
	var capturedPrice float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/order" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		capturedPrice = parseFloat(r.URL.Query().Get("price"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"FILLED","clientOrderId":"exp_sell-1","executedQty":"1","cummulativeQuoteQty":"99.9","origQty":"1","fills":[{"price":"99.9","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
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
	}
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "90", AskPrice: "90.1"}, now)

	manager := &ExposureManager{
		engine: engine,
		logger: engine.logger,
	}
	task := exposureRescueTask{
		Phase:              "open_recovery",
		Symbol:             "BTCUSDT",
		Pair:               "BTCUSDT",
		SpotBaseAsset:      "BTC",
		SpotBasePrecision:  3,
		SpotReferencePrice: 100,
	}

	if err := manager.flattenSpotResidual(context.Background(), task, 1, "SELL", "exposure_rescue_sell_spot"); err != nil {
		t.Fatalf("flattenSpotResidual() error = %v", err)
	}
	if capturedPrice < 99.9-1e-9 {
		t.Fatalf("expected anchored rescue price near 99.9, got %.8f", capturedPrice)
	}
}

func TestExposureLockBlocksSymbolUntilRescueReportsCompletion(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.New(io.Discard, "", 0),
		symbolCooldowns: make(map[string]symbolCooldownState),
		exposureLocks:   make(map[string]exposureLockState),
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol: "BTCUSDT",
			Pair:   "BTCUSDT",
		},
	}
	task := exposureRescueTask{
		Mode:   exposureRescueModeOpen,
		Phase:  "parallel_open_spot_unknown",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}
	engine.lockSymbolForExposure(task)

	filtered := engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target})
	if len(filtered) != 0 {
		t.Fatalf("expected exposure-locked symbol to be skipped, got %+v", filtered)
	}

	manager := &ExposureManager{
		engine: engine,
		logger: engine.logger,
	}
	manager.reportTaskResult(task, nil)

	filtered = engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target})
	if len(filtered) != 1 || filtered[0].Candidate.Symbol != "BTCUSDT" {
		t.Fatalf("expected symbol to be released after rescue completion, got %+v", filtered)
	}
}
