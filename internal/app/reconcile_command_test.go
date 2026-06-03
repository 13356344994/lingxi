package app

import (
	"strings"
	"testing"

	"lingxi/internal/reconcile"
)

func approxEqual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// TestBuildLiveVsExpectedReconcileSkipsPending 断言仍在等待 funding 实收的周期不参与对账。
func TestBuildLiveVsExpectedReconcileSkipsPending(t *testing.T) {
	records := []SettlementCSVRecord{
		{RunID: "r", CycleID: "c1", FundingIncome: "2.0", ExpectedFundingIncome: "1.5", NetPnL: "1.7", FundingIncomePending: false},
		{RunID: "r", CycleID: "c2", FundingIncome: "9.9", ExpectedFundingIncome: "9.9", NetPnL: "9.9", FundingIncomePending: true},
	}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	if len(live) != 1 || len(expected) != 1 {
		t.Fatalf("expected only the settled cycle to be included, got live=%d expected=%d", len(live), len(expected))
	}
	if live[0].Key != "r/c1" {
		t.Fatalf("unexpected key: %s", live[0].Key)
	}
}

// TestBuildLiveVsExpectedNetGapDecomposition 钉死核心语义：net_pnl 偏差 = 资金费预测误差 + 交易盈亏泄漏。
func TestBuildLiveVsExpectedNetGapDecomposition(t *testing.T) {
	// actualFunding=2.0 expectedFunding=1.5 -> 资金费预测误差 +0.5
	// tradingPnL = spot(-0.05)+futures(-0.05) = -0.10 (执行泄漏)
	// fees=0.20 borrow=0.00
	// actualNet = 2.0 + (-0.10) - 0.20 = 1.70
	// expectedNet = 1.5 + 0 - 0.20 = 1.30
	// netGap = 1.70 - 1.30 = 0.40 = 0.5(funding) + (-0.10)(trading)
	records := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "2.0", ExpectedFundingIncome: "1.5",
		SpotTradingPnL: "-0.05", FuturesTradingPnL: "-0.05",
		TotalFees: "0.20", TotalBorrowCosts: "0.0",
		NetPnL: "1.70",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	if !approxEqual(expected[0].NetPnL, 1.30) {
		t.Fatalf("expected expectedNet 1.30, got %.6f", expected[0].NetPnL)
	}
	netGap := live[0].NetPnL - expected[0].NetPnL
	if !approxEqual(netGap, 0.40) {
		t.Fatalf("expected net gap 0.40, got %.6f", netGap)
	}
	fundingGap := live[0].FundingIncome - expected[0].FundingIncome
	if !approxEqual(fundingGap, 0.50) {
		t.Fatalf("expected funding gap 0.50, got %.6f", fundingGap)
	}
	if expected[0].TradingPnL != 0 {
		t.Fatalf("expected model trading pnl assumed 0, got %.6f", expected[0].TradingPnL)
	}
}

// TestSummarizeReconcileTotalsAndWinRate 断言汇总的累计净利/资金费与胜率正确。
func TestSummarizeReconcileTotalsAndWinRate(t *testing.T) {
	records := []SettlementCSVRecord{
		{RunID: "r", CycleID: "c1", FundingIncome: "2.0", ExpectedFundingIncome: "1.5", TotalFees: "0.2", NetPnL: "1.8"},
		{RunID: "r", CycleID: "c2", FundingIncome: "0.1", ExpectedFundingIncome: "1.0", TotalFees: "0.3", NetPnL: "-0.2"},
	}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	summary := summarizeReconcile(live, expected)
	if summary.Cycles != 2 || summary.WinningCycles != 1 {
		t.Fatalf("unexpected cycles=%d winning=%d", summary.Cycles, summary.WinningCycles)
	}
	if !approxEqual(summary.ActualNetTotal, 1.6) {
		t.Fatalf("expected actual net total 1.6, got %.6f", summary.ActualNetTotal)
	}
	if !approxEqual(summary.ActualFundingTotal, 2.1) || !approxEqual(summary.ExpectedFundingTotal, 2.5) {
		t.Fatalf("unexpected funding totals actual=%.6f expected=%.6f", summary.ActualFundingTotal, summary.ExpectedFundingTotal)
	}
}

// TestReconcileReportBreachAndHealthy 端到端验证：偏差超阈值时报告 UNHEALTHY 并列出超阈周期；
// 偏差在阈值内时报告 HEALTHY。
func TestReconcileReportBreachAndHealthy(t *testing.T) {
	// 一个资金费严重落空的周期：actualFunding=0.1 expectedFunding=2.0 -> net gap≈1.9，超过阈值 1.0。
	breachRecords := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "0.1", ExpectedFundingIncome: "2.0",
		TotalFees: "0.2", NetPnL: "-0.1",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(breachRecords)
	thresholds := reconcile.Thresholds{AbsUSDT: 1.0}
	report := reconcile.Reconcile(live, expected, thresholds)
	if report.Healthy {
		t.Fatal("expected unhealthy report when funding falls far short of expectation")
	}
	text := formatReconcileReport(report, summarizeReconcile(live, expected), thresholds)
	if !strings.Contains(text, "UNHEALTHY") {
		t.Fatalf("expected UNHEALTHY verdict, got:\n%s", text)
	}

	// 一个资金费基本符合预期的周期：net gap 远小于阈值。
	healthyRecords := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "1.98", ExpectedFundingIncome: "2.0",
		TotalFees: "0.2", NetPnL: "1.78",
	}}
	live2, expected2 := buildLiveVsExpectedReconcileRecords(healthyRecords)
	report2 := reconcile.Reconcile(live2, expected2, thresholds)
	if !report2.Healthy {
		t.Fatalf("expected healthy report when funding matches expectation within threshold")
	}
	text2 := formatReconcileReport(report2, summarizeReconcile(live2, expected2), thresholds)
	if !strings.Contains(text2, "HEALTHY") {
		t.Fatalf("expected HEALTHY verdict, got:\n%s", text2)
	}
}

// TestFormatReconcileReportEmpty 断言无已结算周期时给出明确引导而非崩溃/空报告。
func TestFormatReconcileReportEmpty(t *testing.T) {
	text := formatReconcileReport(reconcile.Report{}, reconcileSummary{}, reconcile.Thresholds{AbsUSDT: 1})
	if !strings.Contains(text, "无已定稿") {
		t.Fatalf("expected guidance for empty ledger, got:\n%s", text)
	}
}
