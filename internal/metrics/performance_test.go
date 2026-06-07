package metrics

import (
	"math"
	"testing"
)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.8f, want %.8f (tol %.0e)", name, got, want, tol)
	}
}

func TestComputePerformanceInsufficient(t *testing.T) {
	// N=0:全 0 + Insufficient + BenchmarkMissing。
	m := ComputePerformance(PerformanceInputs{AllocatedCapital: 1000})
	if !m.Insufficient || !m.BenchmarkMissing || m.Samples != 0 {
		t.Fatalf("空序列应 Insufficient/BenchmarkMissing/Samples=0, got %+v", m)
	}
	if m.AnnualFactor != DefaultAnnualFactor || m.BenchmarkSymbol != DefaultBenchmarkSymbol {
		t.Fatalf("默认年化/基准符号未生效, got factor=%.0f sym=%s", m.AnnualFactor, m.BenchmarkSymbol)
	}
	// N=1:权益/胜率有意义,比率无定义。
	m1 := ComputePerformance(PerformanceInputs{Returns: []float64{0.05}, AllocatedCapital: 1000})
	if !m1.Insufficient {
		t.Fatalf("单样本比率应 Insufficient")
	}
	if m1.Samples != 1 || m1.WinRate != 1 {
		t.Fatalf("单样本 WinRate 应为 1, got %+v", m1)
	}
	if m1.Sharpe != 0 || m1.Sortino != 0 || m1.AnnualizedReturn != 0 {
		t.Fatalf("单样本比率应为 0, got %+v", m1)
	}
	approx(t, "N=1 EndEquity", m1.EndEquity, 1050, 1e-9)
}

func TestComputePerformanceKnownSeries(t *testing.T) {
	r := []float64{0.01, -0.02, 0.03}
	m := ComputePerformance(PerformanceInputs{Returns: r, AllocatedCapital: 1000})

	if m.Samples != 3 || m.Insufficient {
		t.Fatalf("3 样本不应 Insufficient, got %+v", m)
	}
	approx(t, "TotalReturn", m.TotalReturn, 0.02, 1e-9)
	approx(t, "StartEquity", m.StartEquity, 1000, 1e-9)
	approx(t, "EndEquity", m.EndEquity, 1020, 1e-9)
	approx(t, "WinRate", m.WinRate, 2.0/3.0, 1e-9)
	approx(t, "MaxDrawdown(USDT)", m.MaxDrawdown, 20, 1e-9)
	approx(t, "MaxDrawdownRate", m.MaxDrawdownRate, 0.02/1.01, 1e-9)

	// 手算锚定(见测试推导):Sharpe≈5.0606, Sortino≈11.0299。
	approx(t, "Sharpe", m.Sharpe, 5.0606, 5e-3)
	approx(t, "Sortino", m.Sortino, 11.0299, 5e-3)

	// 年化(几何)应 >0 有限;Calmar 与年化/回撤率自洽。
	if m.AnnualizedReturn <= 0 || math.IsInf(m.AnnualizedReturn, 0) || math.IsNaN(m.AnnualizedReturn) {
		t.Fatalf("年化收益应为正有限值, got %v", m.AnnualizedReturn)
	}
	approx(t, "Calmar*MaxDDRate==Annualized", m.Calmar*m.MaxDrawdownRate, m.AnnualizedReturn, 1e-6)
}

func TestComputePerformanceBetaAlpha(t *testing.T) {
	r := []float64{0.01, -0.02, 0.03, 0.005}

	// 基准 = 收益本身 → Beta=1, Alpha=0。
	same := ComputePerformance(PerformanceInputs{Returns: r, Benchmark: r, AllocatedCapital: 1000})
	if same.BenchmarkMissing {
		t.Fatalf("对齐基准不应 BenchmarkMissing")
	}
	approx(t, "Beta(identical)", same.Beta, 1.0, 1e-9)
	approx(t, "Alpha(identical)", same.Alpha, 0.0, 1e-9)

	// 基准 = 2×收益 → Beta=0.5, Alpha=0。
	b2 := make([]float64, len(r))
	for i := range r {
		b2[i] = 2 * r[i]
	}
	scaled := ComputePerformance(PerformanceInputs{Returns: r, Benchmark: b2, AllocatedCapital: 1000})
	approx(t, "Beta(2x)", scaled.Beta, 0.5, 1e-9)
	approx(t, "Alpha(2x)", scaled.Alpha, 0.0, 1e-9)

	// 基准缺失 → BenchmarkMissing,Alpha/Beta=0。
	none := ComputePerformance(PerformanceInputs{Returns: r, AllocatedCapital: 1000})
	if !none.BenchmarkMissing || none.Beta != 0 || none.Alpha != 0 {
		t.Fatalf("缺基准应 BenchmarkMissing 且 Alpha/Beta=0, got %+v", none)
	}

	// 长度不齐 → BenchmarkMissing。
	mismatch := ComputePerformance(PerformanceInputs{Returns: r, Benchmark: []float64{0.01}, AllocatedCapital: 1000})
	if !mismatch.BenchmarkMissing {
		t.Fatalf("基准长度不齐应 BenchmarkMissing")
	}

	// 基准方差为 0(常数)→ BenchmarkMissing。
	flat := ComputePerformance(PerformanceInputs{Returns: r, Benchmark: make([]float64, len(r)), AllocatedCapital: 1000})
	if !flat.BenchmarkMissing {
		t.Fatalf("基准方差为 0 应 BenchmarkMissing")
	}
}

func TestBuildDailySeriesGapFill(t *testing.T) {
	entries := []DailyPnL{
		{Day: "2026-01-01", PnL: 5},
		{Day: "2026-01-03", PnL: -2},
		{Day: "2026-01-01", PnL: 1}, // 同日累加 → 6
		{Day: "bad-date", PnL: 999}, // 非法 → 跳过
	}
	series := BuildDailySeries(entries)
	want := []DailyPnL{
		{Day: "2026-01-01", PnL: 6},
		{Day: "2026-01-02", PnL: 0}, // 空白日补零
		{Day: "2026-01-03", PnL: -2},
	}
	if len(series) != len(want) {
		t.Fatalf("序列长度 = %d, want %d (%+v)", len(series), len(want), series)
	}
	for i := range want {
		if series[i].Day != want[i].Day || math.Abs(series[i].PnL-want[i].PnL) > 1e-9 {
			t.Fatalf("series[%d] = %+v, want %+v", i, series[i], want[i])
		}
	}
	if BuildDailySeries(nil) != nil {
		t.Fatalf("空输入应返回 nil")
	}
}

func TestReturnsFromDaily(t *testing.T) {
	series := []DailyPnL{{Day: "2026-01-01", PnL: 6}, {Day: "2026-01-02", PnL: 0}, {Day: "2026-01-03", PnL: -2}}
	got := ReturnsFromDaily(series, 100)
	want := []float64{0.06, 0, -0.02}
	if len(got) != 3 {
		t.Fatalf("长度 = %d", len(got))
	}
	for i := range want {
		approx(t, "r_t", got[i], want[i], 1e-9)
	}
	if ReturnsFromDaily(series, 0) != nil {
		t.Fatalf("capital<=0 应返回 nil")
	}
}

func TestEquityCurve(t *testing.T) {
	returns := []float64{0.06, 0, -0.02}
	days := []string{"2026-01-01", "2026-01-02", "2026-01-03"}
	pts := EquityCurve(days, returns, 100)
	if len(pts) != 3 {
		t.Fatalf("点数 = %d", len(pts))
	}
	approx(t, "equity0", pts[0].Equity, 106, 1e-9)
	approx(t, "equity2", pts[2].Equity, 104, 1e-9)
	approx(t, "dd2", pts[2].Drawdown, 2, 1e-9)
	approx(t, "ddrate2", pts[2].DrawdownRate, 2.0/106.0, 1e-9)
	if pts[0].Drawdown != 0 {
		t.Fatalf("首点回撤应为 0, got %v", pts[0].Drawdown)
	}
	if EquityCurve(days, returns, 0) != nil {
		t.Fatalf("capital<=0 应返回 nil")
	}
}
