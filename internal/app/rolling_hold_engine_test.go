package app

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

// TestSettleKeptPositionsEmitsFundingOnlySettlement 钉死 M-S4.4c-ii:keep 仓发 funding-only 结算——
// 无平仓盈亏/手续费、只记本周期借币(正向 carry 为 0)、FundingIncomePending,按 pre-funding NetPnL 推进当日盈亏,
// 并把 keep symbol 写入待结算记录的 RollingHoldSymbols(供 worker 放行归因)。不平仓、不动 currentPositions。
func TestSettleKeptPositionsEmitsFundingOnlySettlement(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:            DefaultEngineConfig(),
		logger:         log.New(io.Discard, "", 0),
		reportStore:    store,
		runID:          "run-keep",
		currentCycleID: "cycle-keep",
		// 大延迟:worker 被唤醒也不会在测试期内处理(NextAttemptAt 远在未来),保证断言确定性。
		fundingSettlementDelay:  time.Hour,
		fundingSettlementWakeCh: make(chan struct{}, 1),
	}

	// 正向 carry 持仓(EstimatedRate>0)→ 无借币(borrow=0),预期资金费>0。
	keepHedges := []OpenHedge{{
		Target:              FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: 0.0005, MarkPrice: 100},
		HedgeQty:            1,
		OpenFuturesAvgPrice: 100,
	}}
	before := CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{"BTCUSDT": -1.0}}
	fundingTime := time.Now().UTC().Add(-time.Minute)

	settlement, err := engine.settleKeptPositions(keepHedges, before, fundingTime)
	if err != nil {
		t.Fatalf("settleKeptPositions() error = %v", err)
	}

	if !settlement.FundingIncomePending {
		t.Fatal("keep 结算应为资金费待到账(FundingIncomePending=true)")
	}
	if settlement.Targets != 1 {
		t.Fatalf("Targets 应为 1, got %d", settlement.Targets)
	}
	if settlement.TradingPnL != 0 || settlement.TotalFees != 0 {
		t.Fatalf("keep 结算不应有交易盈亏/手续费, TradingPnL=%.6f TotalFees=%.6f", settlement.TradingPnL, settlement.TotalFees)
	}
	if settlement.TotalBorrowCosts != 0 {
		t.Fatalf("正向 carry 不应有借币成本, got %.6f", settlement.TotalBorrowCosts)
	}
	if settlement.NetPnL != -settlement.TotalBorrowCosts {
		t.Fatalf("pre-funding NetPnL 应等于 -借币, NetPnL=%.6f borrow=%.6f", settlement.NetPnL, settlement.TotalBorrowCosts)
	}
	if settlement.ExpectedFundingIncome <= 0 {
		t.Fatalf("正向 carry 预期资金费应 > 0, got %.6f", settlement.ExpectedFundingIncome)
	}

	// pre-funding NetPnL 已推进当日已实现盈亏。
	if engine.riskBudgetState.DailyRealizedPnL != settlement.NetPnL {
		t.Fatalf("当日已实现盈亏应被 keep 结算推进到 %.6f, got %.6f", settlement.NetPnL, engine.riskBudgetState.DailyRealizedPnL)
	}

	// 结算已落盘。
	records, err := store.ListSettlementRecords(5)
	if err != nil {
		t.Fatalf("ListSettlementRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("应落盘 1 笔 keep 结算, got %d", len(records))
	}

	// 待结算记录带 RollingHoldSymbols(放行 worker 资金费归因)。
	pending, err := store.ListPendingFundingSettlements(5)
	if err != nil {
		t.Fatalf("ListPendingFundingSettlements() error = %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("应有 1 笔待结算任务, got %d", len(pending))
	}
	if len(pending[0].RollingHoldSymbols) != 1 || pending[0].RollingHoldSymbols[0] != "BTCUSDT" {
		t.Fatalf("待结算任务应标记 RollingHoldSymbols=[BTCUSDT], got %v", pending[0].RollingHoldSymbols)
	}
}

// TestSettleKeptPositionsEmptyNoop 空 keep 集合应为 no-op(无落盘、无待结算、不推进盈亏)。
func TestSettleKeptPositionsEmptyNoop(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	engine := &FundingRateSniperEngine{
		cfg:         DefaultEngineConfig(),
		logger:      log.New(io.Discard, "", 0),
		reportStore: store,
	}
	settlement, err := engine.settleKeptPositions(nil, CycleAccountSnapshot{}, time.Now().UTC())
	if err != nil {
		t.Fatalf("settleKeptPositions() error = %v", err)
	}
	if settlement.Targets != 0 || settlement.FundingIncomePending {
		t.Fatalf("空 keep 应返回零值结算, got %+v", settlement)
	}
	if engine.riskBudgetState.DailyRealizedPnL != 0 {
		t.Fatalf("空 keep 不应推进当日盈亏, got %.6f", engine.riskBudgetState.DailyRealizedPnL)
	}
}
