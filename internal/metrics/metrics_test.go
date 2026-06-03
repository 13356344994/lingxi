package metrics

import (
	"math"
	"testing"
	"time"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestQuantile(t *testing.T) {
	samples := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		q    float64
		want float64
	}{
		{q: 0, want: 1},
		{q: 1, want: 10},
		{q: 0.5, want: 5.5},
		{q: 0.9, want: 9.1},
	}
	for _, tc := range cases {
		if got := Quantile(samples, tc.q); !almostEqual(got, tc.want) {
			t.Fatalf("Quantile(q=%.2f)=%.6f, want %.6f", tc.q, got, tc.want)
		}
	}
	if got := Quantile(nil, 0.5); got != 0 {
		t.Fatalf("empty Quantile=%.6f, want 0", got)
	}
	if got := Quantile([]float64{42}, 0.9); got != 42 {
		t.Fatalf("single-sample Quantile=%.6f, want 42", got)
	}
}

func TestSummarize(t *testing.T) {
	d := Summarize([]float64{10, 20, 30, 40, 50})
	if d.Count != 5 {
		t.Fatalf("count=%d, want 5", d.Count)
	}
	if d.Min != 10 || d.Max != 50 {
		t.Fatalf("min/max=%.1f/%.1f, want 10/50", d.Min, d.Max)
	}
	if !almostEqual(d.Mean, 30) {
		t.Fatalf("mean=%.6f, want 30", d.Mean)
	}
	if !almostEqual(d.P50, 30) {
		t.Fatalf("p50=%.6f, want 30", d.P50)
	}
	if (Distribution{}) != Summarize(nil) {
		t.Fatal("empty Summarize should be zero value")
	}
}

func TestSummarizeDurations(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
	}
	d := SummarizeDurations(samples)
	if d.Count != 4 {
		t.Fatalf("count=%d, want 4", d.Count)
	}
	if d.Min != 10*time.Millisecond || d.Max != 40*time.Millisecond {
		t.Fatalf("min/max=%v/%v", d.Min, d.Max)
	}
	if d.Mean != 25*time.Millisecond {
		t.Fatalf("mean=%v, want 25ms", d.Mean)
	}
}

func TestWindowTracker(t *testing.T) {
	base := time.Unix(0, 0)
	var w WindowTracker

	// 第一次窗口：3s
	w.Open(base)
	w.Open(base.Add(time.Second)) // 幂等：重复 Open 不应重置开窗时刻
	if !w.IsOpen() {
		t.Fatal("expected window open")
	}
	if got := w.Elapsed(base.Add(2 * time.Second)); got != 2*time.Second {
		t.Fatalf("elapsed=%v, want 2s", got)
	}
	w.Close(base.Add(3 * time.Second))

	// 第二次窗口：5s（更长）
	w.Open(base.Add(10 * time.Second))
	w.Close(base.Add(15 * time.Second))

	if w.Count() != 2 {
		t.Fatalf("count=%d, want 2", w.Count())
	}
	if w.Total() != 8*time.Second {
		t.Fatalf("total=%v, want 8s", w.Total())
	}
	if w.Longest() != 5*time.Second {
		t.Fatalf("longest=%v, want 5s", w.Longest())
	}
	if w.IsOpen() {
		t.Fatal("expected window closed")
	}
	// 关闭未打开窗口应被忽略
	w.Close(base.Add(20 * time.Second))
	if w.Count() != 2 {
		t.Fatalf("count after spurious close=%d, want 2", w.Count())
	}
}
