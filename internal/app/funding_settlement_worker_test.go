package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

// newFundingIncomeMockEngine 构造一个仅对接 mock /fapi/v1/income 的引擎,用于测试资金费归因闸。
func newFundingIncomeMockEngine(t *testing.T, incomeJSON string) *FundingRateSniperEngine {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(incomeJSON))
	}))
	t.Cleanup(server.Close)
	return &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
	}
}

// TestSettleFundingIncomeKeptPositionAttributed 钉死 M-S4.4b:滚动续持(keep)仓即便开仓前快照已有持仓
// (beforeQty≠0),其本周期资金费仍必须被归因(不被"开仓前已有持仓"闸跳过)。
func TestSettleFundingIncomeKeptPositionAttributed(t *testing.T) {
	engine := newFundingIncomeMockEngine(t, `[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"1.25","time":1}]`)
	hedges := []OpenHedge{{Target: FundingCandidate{Symbol: "BTCUSDT"}, HedgeQty: 1, OpenFuturesAvgPrice: 100}}
	// 开仓前快照已有 -1.0 的合约持仓(keep 仓从上周期持有过来,这是正常预期)。
	before := CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{"BTCUSDT": -1.0}}
	fundingTime := time.Now().UTC().Add(-time.Minute)

	kept := map[string]struct{}{"BTCUSDT": {}}
	res := engine.settleFundingIncomeDetailed(context.Background(), hedges, before, fundingTime, kept)
	if got := res.FundingIncomeBySymbol["BTCUSDT"]; got != 1.25 {
		t.Fatalf("keep 仓资金费应被归因为 1.25, got %.6f (alerts=%v)", got, res.Alerts)
	}
	if res.TotalFundingIncome != 1.25 {
		t.Fatalf("keep 仓 TotalFundingIncome 应为 1.25, got %.6f", res.TotalFundingIncome)
	}
}

// TestSettleFundingIncomeNonKeptPreexistingSkipped 回归锁:非 keep 的单周期路径下,开仓前已有持仓仍按既有行为
// 跳过资金费归因(确保 M-S4.4b 的白名单不改变单周期口径)。
func TestSettleFundingIncomeNonKeptPreexistingSkipped(t *testing.T) {
	engine := newFundingIncomeMockEngine(t, `[{"symbol":"BTCUSDT","incomeType":"FUNDING_FEE","income":"1.25","time":1}]`)
	hedges := []OpenHedge{{Target: FundingCandidate{Symbol: "BTCUSDT"}, HedgeQty: 1, OpenFuturesAvgPrice: 100}}
	before := CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{"BTCUSDT": -1.0}}
	fundingTime := time.Now().UTC().Add(-time.Minute)

	res := engine.settleFundingIncomeDetailed(context.Background(), hedges, before, fundingTime, nil)
	if _, attributed := res.FundingIncomeBySymbol["BTCUSDT"]; attributed {
		t.Fatalf("非 keep + 开仓前已有持仓应跳过归因, got %+v", res.FundingIncomeBySymbol)
	}
	if res.TotalFundingIncome != 0 {
		t.Fatalf("跳过后 TotalFundingIncome 应为 0, got %.6f", res.TotalFundingIncome)
	}
	foundSkipAlert := false
	for _, a := range res.Alerts {
		if strings.Contains(a, "开仓前已有持仓") {
			foundSkipAlert = true
		}
	}
	if !foundSkipAlert {
		t.Fatalf("应有「开仓前已有持仓」跳过告警, got %v", res.Alerts)
	}
}

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
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
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

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, listErr := store.ListPendingFundingSettlements(10)
		if listErr != nil {
			t.Fatalf("ListPendingFundingSettlements() error = %v", listErr)
		}
		records, listErr := store.ListSettlementRecords(5)
		if listErr != nil {
			t.Fatalf("ListSettlementRecords() error = %v", listErr)
		}
		// 后台 worker 先写结算记录、再写 riskBudgetState.DailyRealizedPnL（两步非原子）。必须把「日内盈亏已更新」
		// 一并纳入轮询终态条件，否则会撞上两步之间的窗口（记录已结算但 dailyPnL 仍为 0）而误判失败（flaky）。
		// 持有 platformRiskMu 读 riskBudgetState，避免与 worker 的并发写竞争。
		engine.platformRiskMu.Lock()
		dailyPnL := engine.riskBudgetState.DailyRealizedPnL
		engine.platformRiskMu.Unlock()
		if len(pending) == 0 && len(records) == 1 && records[0].FundingIncome == "1.25" && !records[0].FundingIncomePending && dailyPnL == 1.25 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected recovered pending funding settlement to be processed and daily pnl updated within deadline")
}
