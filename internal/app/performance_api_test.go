package app

import (
	"math"
	"testing"
	"time"
)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.8f, want %.8f (tol %.0e)", name, got, want, tol)
	}
}

func TestSettlementDayTimestampFallback(t *testing.T) {
	// 优先 FundingSettledAt。
	rec := SettlementCSVRecord{
		FundingSettledAt: "2026-03-01T08:00:00Z",
		CloseExecutedAt:  "2026-02-01T00:00:00Z",
		RecordedAt:       "2026-01-01T00:00:00Z",
	}
	ts, ok := settlementDayTimestamp(rec)
	if !ok || ts.UTC().Format("2006-01-02") != "2026-03-01" {
		t.Fatalf("应优先 FundingSettledAt, got %v ok=%v", ts, ok)
	}
	// FundingSettledAt 空 → 回退 CloseExecutedAt。
	rec.FundingSettledAt = ""
	ts, ok = settlementDayTimestamp(rec)
	if !ok || ts.UTC().Format("2006-01-02") != "2026-02-01" {
		t.Fatalf("应回退 CloseExecutedAt, got %v ok=%v", ts, ok)
	}
	// 全空 → false。
	if _, ok := settlementDayTimestamp(SettlementCSVRecord{}); ok {
		t.Fatalf("全空时刻应返回 false")
	}
}

func TestVenueDailyPnLExcludesPending(t *testing.T) {
	records := []SettlementCSVRecord{
		{FundingSettledAt: "2026-03-01T08:00:00Z", NetPnL: "10", FundingIncomePending: false},
		{FundingSettledAt: "2026-03-02T08:00:00Z", NetPnL: "999", FundingIncomePending: true}, // 未结算 → 排除
		{FundingSettledAt: "", CloseExecutedAt: "", RecordedAt: "", NetPnL: "5"},              // 无时刻 → 跳过
		{FundingSettledAt: "2026-03-03T08:00:00Z", NetPnL: "-20", FundingIncomePending: false},
	}
	got := venueDailyPnL(records)
	if len(got) != 2 {
		t.Fatalf("应仅保留 2 条已结算且有时刻的记录, got %d (%+v)", len(got), got)
	}
	if got[0].Day != "2026-03-01" || math.Abs(got[0].PnL-10) > 1e-9 {
		t.Fatalf("条目0 = %+v", got[0])
	}
	if got[1].Day != "2026-03-03" || math.Abs(got[1].PnL+20) > 1e-9 {
		t.Fatalf("条目1 = %+v", got[1])
	}
}

func TestComputePerformanceForTargets(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	day := func(s string) time.Time {
		tm, _ := time.Parse(time.RFC3339, s)
		return tm
	}
	seed := func(cycle string, settledAt time.Time, net float64, pending bool) {
		if err := store.AppendSettlement(CycleSettlement{
			RunID:                "run-1",
			CycleID:              cycle,
			CloseExecutedAt:      settledAt,
			FundingSettledAt:     settledAt,
			NetPnL:               net,
			FundingIncomePending: pending,
		}); err != nil {
			t.Fatalf("AppendSettlement() error = %v", err)
		}
	}
	// 三个已结算日 + 一个未结算(必须被排除):r = [0.01,-0.02,0.03] @ capital 1000(锚定 metrics 已知序列)。
	seed("c1", day("2026-03-01T08:00:00Z"), 10, false)
	seed("c2", day("2026-03-02T08:00:00Z"), -20, false)
	seed("c3", day("2026-03-03T08:00:00Z"), 30, false)
	seed("c4", day("2026-03-04T08:00:00Z"), 999, true)

	rt := &venueRuntime{name: "binance", store: store, allocatedCapitalUSDT: 1000}
	m, equity, err := computePerformanceForTargets([]*venueRuntime{rt})
	if err != nil {
		t.Fatalf("computePerformanceForTargets() error = %v", err)
	}
	if m.Samples != 3 || m.Insufficient {
		t.Fatalf("应有 3 个样本且非 Insufficient, got %+v", m)
	}
	approx(t, "TotalReturn", m.TotalReturn, 0.02, 1e-9)
	approx(t, "MaxDrawdown", m.MaxDrawdown, 20, 1e-9)
	if len(equity) != 3 {
		t.Fatalf("权益曲线应 3 点, got %d", len(equity))
	}
	approx(t, "equity end", equity[2].Equity, 1020, 1e-9)

	// 分配本金为 0 → 分母缺失,指标 Insufficient(诚实,不静默猜)。
	rt0 := &venueRuntime{name: "binance", store: store, allocatedCapitalUSDT: 0}
	m0, _, err := computePerformanceForTargets([]*venueRuntime{rt0})
	if err != nil {
		t.Fatalf("computePerformanceForTargets(capital=0) error = %v", err)
	}
	if !m0.Insufficient || m0.Samples != 0 {
		t.Fatalf("分配本金为 0 应 Insufficient/Samples=0, got %+v", m0)
	}
}
