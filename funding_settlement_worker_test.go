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

func TestFundingSettlementWorkerRecoversPendingTaskAfterRestart(t *testing.T) {
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

	settlement := CycleSettlement{
		RunID:                 "run-1",
		CycleID:               "cycle-1",
		FundingTimePoint:      time.Now().UTC().Add(-time.Minute),
		FundingIncomePending:  true,
		ExpectedFundingIncome: 1.00,
		SpotTradingPnL:        2.00,
		FuturesTradingPnL:     0.50,
		TotalFees:             0.25,
		NetPnL:                2.25,
		BeforeSnapshot:        CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{}},
		Alerts:                []string{"资金费异步结算待处理，预计在 0s 后更新实收流水"},
	}
	if err := store.AppendSettlement(settlement); err != nil {
		t.Fatalf("AppendSettlement() error = %v", err)
	}
	if err := store.UpsertPendingFundingSettlement(PendingFundingSettlementRecord{
		RunID:         settlement.RunID,
		CycleID:       settlement.CycleID,
		RecordedAt:    time.Now().UTC().Format(time.RFC3339),
		NextAttemptAt: time.Now().UTC().Add(-10 * time.Millisecond).Format(time.RFC3339),
		Settlement:    settlement,
		Hedges: []OpenHedge{{
			Target:              FundingCandidate{Symbol: "BTCUSDT"},
			HedgeQty:            1,
			OpenFuturesAvgPrice: 100,
		}},
	}); err != nil {
		t.Fatalf("UpsertPendingFundingSettlement() error = %v", err)
	}

	engine := &FundingRateSniperEngine{
		client: &BinanceClient{
			futuresBaseURL: server.URL,
			apiKey:         "demo-api-key",
			secretKey:      "demo-secret-key",
			httpClient:     server.Client(),
		},
		cfg:                     DefaultEngineConfig(),
		logger:                  log.New(io.Discard, "", 0),
		reportStore:             store,
		runtimeReporter:         newRuntimeReporter(nil),
		fundingSettlementDelay:  10 * time.Millisecond,
		fundingSettlementWakeCh: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.startFundingSettlementWorker(ctx)
	engine.wakeFundingSettlementWorker()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		pending, listErr := store.ListPendingFundingSettlements(10)
		if listErr != nil {
			t.Fatalf("ListPendingFundingSettlements() error = %v", listErr)
		}
		records, listErr := store.ListSettlementRecords(5)
		if listErr != nil {
			t.Fatalf("ListSettlementRecords() error = %v", listErr)
		}
		if len(pending) == 0 && len(records) == 1 && records[0].FundingIncome == "1.25" && !records[0].FundingIncomePending {
			if engine.riskBudgetState.DailyRealizedPnL != 1.25 {
				t.Fatalf("expected daily pnl to be adjusted by recovered funding delta, got %.8f", engine.riskBudgetState.DailyRealizedPnL)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected recovered pending funding settlement to be processed")
}
