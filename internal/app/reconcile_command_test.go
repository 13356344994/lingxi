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

// TestReconcileExpectedSideUsesModelFees 钉死 M-S3.1(R6):预期侧手续费取模型预期值(而非抄实测),
// 从而让 total_fees 字段的偏差暴露成本侧模型误差;历史记录(无该字段)回退到实测费用,零回归。
func TestReconcileExpectedSideUsesModelFees(t *testing.T) {
	// 模型预期手续费 0.30,实测仅 0.18(BNB 抵扣/部分挂单)→ 成本侧应出现 0.12 偏差。
	records := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "2.0", ExpectedFundingIncome: "2.0",
		TotalFees: "0.18", ExpectedTotalFees: "0.30", TotalBorrowCosts: "0.0",
		CycleNotionalUSDT: "1000", NetPnL: "1.82",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	if !approxEqual(expected[0].TotalFees, 0.30) {
		t.Fatalf("预期侧应使用模型预期手续费 0.30, got %.6f", expected[0].TotalFees)
	}
	if !approxEqual(live[0].TotalFees, 0.18) {
		t.Fatalf("实盘侧应使用实测手续费 0.18, got %.6f", live[0].TotalFees)
	}
	if !approxEqual(live[0].Notional, 1000) || !approxEqual(expected[0].Notional, 1000) {
		t.Fatalf("两侧名义应取自落盘 CycleNotionalUSDT=1000, got live=%.4f expected=%.4f", live[0].Notional, expected[0].Notional)
	}
	// expectedNet = 2.0 - 0.30 - 0 = 1.70;成本侧模型误差体现在 net_pnl/total_fees 偏差里。
	if !approxEqual(expected[0].NetPnL, 1.70) {
		t.Fatalf("expectedNet 应为 1.70, got %.6f", expected[0].NetPnL)
	}

	// 历史记录:无 ExpectedTotalFees → 回退实测费用,预期侧 fees == 实测,成本侧偏差归零(旧行为)。
	legacy := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "2.0", ExpectedFundingIncome: "2.0",
		TotalFees: "0.18", NetPnL: "1.82",
	}}
	_, legacyExpected := buildLiveVsExpectedReconcileRecords(legacy)
	if !approxEqual(legacyExpected[0].TotalFees, 0.18) {
		t.Fatalf("历史记录应回退到实测手续费 0.18, got %.6f", legacyExpected[0].TotalFees)
	}
}

// TestReconcileRelativeThresholdCatchesSmallNotionalLeak 钉死 R6 核心修复:小名义下绝对偏差很小(< 绝对
// 阈值)但相对比例很大的泄漏,过去只设绝对阈值会被盖 HEALTHY;设了相对阈值后应判 UNHEALTHY。
func TestReconcileRelativeThresholdCatchesSmallNotionalLeak(t *testing.T) {
	// 小仓:预期净利 2.0,实盘只赚 0.5 → 绝对偏差 1.5(< 绝对阈值 3),相对偏差 75%(> 相对阈值 20%)。
	records := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "0.6", ExpectedFundingIncome: "2.1",
		TotalFees: "0.1", ExpectedTotalFees: "0.1", TotalBorrowCosts: "0.0",
		CycleNotionalUSDT: "50", NetPnL: "0.5",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(records)

	// 仅绝对阈值(旧默认):绝对偏差 1.5 < 3 → 蒙混过 HEALTHY。
	absOnly := reconcile.Reconcile(live, expected, reconcile.Thresholds{AbsUSDT: 3})
	if !absOnly.Healthy {
		t.Fatalf("仅绝对阈值时小额泄漏本应蒙混过 HEALTHY(用于反衬修复), got breached=%d", absOnly.BreachedCount)
	}

	// 引入相对阈值后:相对偏差 75% > 20% → 判 breach。
	withRel := reconcile.Reconcile(live, expected, reconcile.Thresholds{AbsUSDT: 3, Rel: 0.20})
	if withRel.Healthy || withRel.BreachedCount == 0 {
		t.Fatalf("相对阈值应抓出小名义大比例泄漏, got %+v", withRel)
	}
}

// TestReconcileCycleIDNotInSymbol 钉死 R23 修复:CycleID 走专用字段、不再塞进 Symbol;报告用 cycle=<CycleID> 标注。
func TestReconcileCycleIDNotInSymbol(t *testing.T) {
	records := []SettlementCSVRecord{{
		RunID: "r", CycleID: "cycle-42",
		FundingIncome: "0.1", ExpectedFundingIncome: "2.0",
		TotalFees: "0.2", NetPnL: "-0.1",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	if live[0].CycleID != "cycle-42" {
		t.Fatalf("CycleID 应填入专用字段, got %q", live[0].CycleID)
	}
	if live[0].Symbol != "" {
		t.Fatalf("Symbol 不应再被 CycleID 污染, got %q", live[0].Symbol)
	}
	report := reconcile.Reconcile(live, expected, reconcile.Thresholds{AbsUSDT: 1.0})
	text := formatReconcileReport(report, summarizeReconcile(live, expected), reconcile.Thresholds{AbsUSDT: 1.0})
	if !strings.Contains(text, "cycle=cycle-42") {
		t.Fatalf("报告应以 cycle=<CycleID> 标注超阈周期, got:\n%s", text)
	}
}

// TestReconcileFrictionSurfacedNotGated 钉死 S8-A:执行摩擦聚合进对账并在报告里展示,但不参与 breach 判定。
func TestReconcileFrictionSurfacedNotGated(t *testing.T) {
	// 资金费完全符合预期(net gap≈0),但有显著执行摩擦 → 不应因摩擦判 UNHEALTHY,且报告展示摩擦累计。
	records := []SettlementCSVRecord{{
		RunID: "r", CycleID: "c1",
		FundingIncome: "2.0", ExpectedFundingIncome: "2.0",
		TotalFees: "0.2", NetPnL: "1.8",
		TotalOpenSlippageUSDT: "-0.30", TotalCloseSlippageUSDT: "-0.20", TotalRecoveryFrictionUSDT: "0.0",
	}}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	if !approxEqual(live[0].Friction, -0.50) {
		t.Fatalf("摩擦应聚合开+平滑点+恢复=-0.50, got %.6f", live[0].Friction)
	}
	if expected[0].Friction != 0 {
		t.Fatalf("模型理想摩擦应为 0, got %.6f", expected[0].Friction)
	}
	thresholds := reconcile.Thresholds{AbsUSDT: 0.1}
	report := reconcile.Reconcile(live, expected, thresholds)
	if !report.Healthy {
		t.Fatalf("摩擦不应触发 breach(诊断字段), 但 report 判为 unhealthy: breaches=%d", report.BreachedCount)
	}
	summary := summarizeReconcile(live, expected)
	if !approxEqual(summary.FrictionTotal, -0.50) {
		t.Fatalf("摩擦累计应为 -0.50, got %.6f", summary.FrictionTotal)
	}
	text := formatReconcileReport(report, summary, thresholds)
	if !strings.Contains(text, "执行摩擦累计") {
		t.Fatalf("报告应展示执行摩擦累计行, got:\n%s", text)
	}
}

// TestFormatReconcileReportEmpty 断言无已结算周期时给出明确引导而非崩溃/空报告。
func TestFormatReconcileReportEmpty(t *testing.T) {
	text := formatReconcileReport(reconcile.Report{}, reconcileSummary{}, reconcile.Thresholds{AbsUSDT: 1})
	if !strings.Contains(text, "无已定稿") {
		t.Fatalf("expected guidance for empty ledger, got:\n%s", text)
	}
}
