package app

import (
	"context"
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

// TestClassifyResidentsForRevalidation 钉死 c-iii-3b-2:常驻持仓按 funding 时钟分 due(复核窗内 / funding 已过)
// 与 untouched(远期 / 无时钟)。
func TestClassifyResidentsForRevalidation(t *testing.T) {
	sched := newCycleScheduler(DefaultEngineConfig())
	now := time.Date(2026, 6, 5, 10, 56, 0, 0, time.UTC) // 11:00 结算的复核窗 [10:55,11:00) 内
	residents := []OpenHedge{
		{Target: FundingCandidate{Symbol: "DUE", NextFundingTime: time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)}},
		{Target: FundingCandidate{Symbol: "PASSED", NextFundingTime: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)}},
		{Target: FundingCandidate{Symbol: "FUTURE", NextFundingTime: time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)}},
		{Target: FundingCandidate{Symbol: "ZERO"}},
	}
	due, untouched := classifyResidentsForRevalidation(residents, sched, now)
	if got := hedgeSymbolSet(due); len(got) != 2 || !hasKey(got, "DUE") || !hasKey(got, "PASSED") {
		t.Fatalf("due 应为 {DUE,PASSED}, got %v", got)
	}
	if got := hedgeSymbolSet(untouched); len(got) != 2 || !hasKey(got, "FUTURE") || !hasKey(got, "ZERO") {
		t.Fatalf("untouched 应为 {FUTURE,ZERO}, got %v", got)
	}
}

func hasKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// TestAdvanceResidentFundingClocks 钉死 c-iii-3b-2:推进指定持仓的 NextFundingTime(+8h)并重持久化台账,
// 未指定的持仓时钟不动。
func TestAdvanceResidentFundingClocks(t *testing.T) {
	statePath := t.TempDir() + "/open_hedges.json"
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig(), runID: "r", currentCycleID: "c", openHedgeStatePath: statePath}
	nft := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	engine.activeOpenHedges = []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT", SpotBaseAsset: "BTC", NextFundingTime: nft}, HedgeQty: 1, OpenFuturesAvgPrice: 100},
		{Target: FundingCandidate{Symbol: "ETHUSDT", SpotBaseAsset: "ETH", NextFundingTime: nft}, HedgeQty: 1, OpenFuturesAvgPrice: 50},
	}
	if err := engine.advanceResidentFundingClocks([]OpenHedge{{Target: FundingCandidate{Symbol: "BTCUSDT"}}}); err != nil {
		t.Fatalf("advanceResidentFundingClocks() error = %v", err)
	}
	wantBTC := nft.Add(8 * time.Hour)
	if !engine.activeOpenHedges[0].Target.NextFundingTime.Equal(wantBTC) {
		t.Fatalf("BTC 时钟应推进到 %v, got %v", wantBTC, engine.activeOpenHedges[0].Target.NextFundingTime)
	}
	if !engine.activeOpenHedges[1].Target.NextFundingTime.Equal(nft) {
		t.Fatalf("ETH 时钟不应变, got %v", engine.activeOpenHedges[1].Target.NextFundingTime)
	}
	entries, err := engine.loadPersistedOpenHedges()
	if err != nil {
		t.Fatalf("loadPersistedOpenHedges() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("台账应有 2 条, got %d", len(entries))
	}
}

// TestExpectedFundingIncomeSummaryResidentBypass 钉死 c-iii-3b-2a:被平的常驻仓(drop)开仓前持仓≠0,
// 默认会被「开仓前已有持仓」闸跳过预期资金费;经 fundingEligibleResidents 放行后正常计入(不跳过)。
func TestExpectedFundingIncomeSummaryResidentBypass(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	hedges := []OpenHedge{{
		Target:              FundingCandidate{Symbol: "BTCUSDT", EstimatedRate: 0.001},
		HedgeQty:            1,
		OpenFuturesAvgPrice: 100,
	}}
	before := CycleAccountSnapshot{FuturesPositionAmounts: map[string]float64{"BTCUSDT": -1.0}}

	// 不放行(单周期口径)→ 跳过 → expected=0 且有跳过告警。
	exp, alerts := engine.expectedFundingIncomeSummary(hedges, before, nil)
	if exp != 0 {
		t.Fatalf("未放行应跳过预期资金费(=0), got %.6f", exp)
	}
	if len(alerts) == 0 {
		t.Fatal("未放行应有「开仓前已有持仓」跳过告警")
	}

	// 放行常驻 drop → 正常计入(>0)、无跳过告警。
	expBypass, alertsBypass := engine.expectedFundingIncomeSummary(hedges, before, map[string]struct{}{"BTCUSDT": {}})
	if expBypass <= 0 {
		t.Fatalf("放行后预期资金费应 > 0, got %.6f", expBypass)
	}
	if len(alertsBypass) != 0 {
		t.Fatalf("放行后不应有跳过告警, got %v", alertsBypass)
	}
}

// TestCollectRollFundingSignals 钉死 c-iii-3a:复核拉取持仓币的保守预测费率(复用开仓 fetchPremiumIndex +
// estimateScanFundingSignal),只返回持仓币、非持仓币剔除;空持仓返回空。
func TestCollectRollFundingSignals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"symbol":"BTCUSDT","markPrice":"100","indexPrice":"99.9","estimatedSettlePrice":"100","lastFundingRate":"0.0008","interestRate":"0.0001","nextFundingTime":1,"time":1},
			{"symbol":"DOGEUSDT","markPrice":"0.1","indexPrice":"0.1","estimatedSettlePrice":"0.1","lastFundingRate":"0.0005","interestRate":"0.0001","nextFundingTime":1,"time":1}
		]`))
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

	// 空持仓 → 空 map、不发请求。
	if got, err := engine.collectRollFundingSignals(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("空持仓应返回空 map, got %v err=%v", got, err)
	}

	rates, err := engine.collectRollFundingSignals(context.Background(), map[string]struct{}{"BTCUSDT": {}})
	if err != nil {
		t.Fatalf("collectRollFundingSignals() error = %v", err)
	}
	if _, ok := rates["BTCUSDT"]; !ok {
		t.Fatalf("应含持仓币 BTCUSDT 的费率, got %v", rates)
	}
	if _, ok := rates["DOGEUSDT"]; ok {
		t.Fatalf("非持仓币不应出现, got %v", rates)
	}
}

// TestReconcileResidentPositionsAfterClose 钉死 c-iii-2 的内存常驻态收敛:retained 为空清空(单周期口径);
// 非空原子收敛到 keep 仓(activeOpenHedges + 持仓标签),无先清空再重建窗口。
func TestReconcileResidentPositionsAfterClose(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig(), runtimeReporter: newRuntimeReporter(nil)}
	engine.activeOpenHedges = []OpenHedge{
		{Target: FundingCandidate{Symbol: "BTCUSDT"}, HedgeQty: 1, OpenFuturesAvgPrice: 100},
		{Target: FundingCandidate{Symbol: "ETHUSDT"}, HedgeQty: 2, OpenFuturesAvgPrice: 50},
	}
	engine.currentPositions = []string{"BTCUSDT x", "ETHUSDT y"}

	// retained=keep 子集(只留 BTCUSDT)→ 原子收敛。
	keep := []OpenHedge{{Target: FundingCandidate{Symbol: "BTCUSDT"}, HedgeQty: 1, OpenFuturesAvgPrice: 100}}
	engine.reconcileResidentPositionsAfterClose(keep)
	if engine.portfolioHeldSymbolCount() != 1 {
		t.Fatalf("收敛后应只剩 1 个持仓, got %d", engine.portfolioHeldSymbolCount())
	}
	if _, ok := engine.heldSymbolSet()["BTCUSDT"]; !ok {
		t.Fatalf("收敛后应保留 BTCUSDT, got %v", engine.heldSymbolSet())
	}
	if len(engine.currentPositions) != 1 {
		t.Fatalf("持仓标签应收敛为 1, got %v", engine.currentPositions)
	}

	// retained=空 → 清空(单周期口径)。
	engine.reconcileResidentPositionsAfterClose(nil)
	if engine.portfolioHeldSymbolCount() != 0 || len(engine.currentPositions) != 0 {
		t.Fatalf("空 retained 应清空常驻态, hedges=%d labels=%v", engine.portfolioHeldSymbolCount(), engine.currentPositions)
	}
}

// TestReconcileResidentLedgerRetainsKeeps 钉死 c-iii-2 的台账收敛:retained 非空时本地台账收敛到 keep 仓
// (而非清空),保证滚动续持下台账与交易所持仓一致。
func TestReconcileResidentLedgerRetainsKeeps(t *testing.T) {
	statePath := t.TempDir() + "/open_hedges.json"
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		runID:              "run-x",
		currentCycleID:     "cycle-x",
		openHedgeStatePath: statePath,
	}
	keep := []OpenHedge{{
		Target:              FundingCandidate{Symbol: "BTCUSDT", SpotBaseAsset: "BTC"},
		HedgeQty:            1,
		OpenFuturesAvgPrice: 100,
	}}
	if err := engine.reconcileResidentLedgerAfterClose(CycleAccountSnapshot{}, nil, keep); err != nil {
		t.Fatalf("reconcileResidentLedgerAfterClose() error = %v", err)
	}
	entries, err := engine.loadPersistedOpenHedges()
	if err != nil {
		t.Fatalf("loadPersistedOpenHedges() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Symbol != "BTCUSDT" {
		t.Fatalf("台账应收敛为 [BTCUSDT], got %+v", entries)
	}
}

// TestResidentClocksDrivesPortfolioWake 钉死 c-iii-1:residentClocks 从常驻持仓读出逐币 funding 时钟,
// 空组合 → 退化为全局 :57 新开唤醒;有到期持仓落入复核窗口 → NextPortfolioWake 立即唤醒。
func TestResidentClocksDrivesPortfolioWake(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}

	// 空常驻组合 → 无时钟。
	if got := engine.residentClocks(); len(got) != 0 {
		t.Fatalf("空组合应无逐币时钟, got %v", got)
	}

	sched := newCycleScheduler(engine.cfg)
	now := time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC)
	// 空时钟:NextPortfolioWake 退化为全局新开唤醒(= NextWake)。
	if got, want := sched.NextPortfolioWake(now, engine.residentClocks()), sched.NextWake(now); !got.Equal(want) {
		t.Fatalf("空组合 NextPortfolioWake 应退化为 NextWake: got %v want %v", got, want)
	}

	// 注入一个 11:00 结算的持仓,复核提前量默认 5min → 复核窗口 [10:55, 11:00)。
	nextFunding := time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC)
	engine.activeOpenHedges = []OpenHedge{{
		Target: FundingCandidate{Symbol: "btcusdt", NextFundingTime: nextFunding},
	}}
	clocks := engine.residentClocks()
	if len(clocks) != 1 || clocks[0].Symbol != "BTCUSDT" || !clocks[0].NextFundingTime.Equal(nextFunding) {
		t.Fatalf("应读出 1 个 BTCUSDT 逐币时钟, got %+v", clocks)
	}
	// 10:56 落入复核窗口 → 立即唤醒(now)。
	inWindow := time.Date(2026, 6, 5, 10, 56, 0, 0, time.UTC)
	if got := sched.NextPortfolioWake(inWindow, clocks); !got.Equal(inWindow) {
		t.Fatalf("复核窗口内应立即唤醒 %v, got %v", inWindow, got)
	}
	// 10:30 未到复核窗口 → 唤醒点为 10:55(早于全局 :57)。
	wantRevalidate := time.Date(2026, 6, 5, 10, 55, 0, 0, time.UTC)
	if got := sched.NextPortfolioWake(now, clocks); !got.Equal(wantRevalidate) {
		t.Fatalf("复核前应在 10:55 唤醒, got %v", got)
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
