// Package metrics 收敛 Gate 4 的结构化可观测指标的纯计算内核：分位/分布统计与
// 窗口时长追踪。这里只做无网络、无状态依赖的纯计算，便于脱网单测，并让实盘引擎与
// 回测共用同一套口径来事后审计「敞口时长 / 单边窗口时长 / 滑点分布 / 确认延迟分位」。
package metrics

import (
	"math"
	"sort"
	"time"
)

// Quantile 返回样本在 q∈[0,1] 处的分位值（线性插值，方法与 numpy 默认一致）。
// 样本无需预排序；空样本返回 0。
func Quantile(samples []float64, q float64) float64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return samples[0]
	}
	if q <= 0 {
		return minSlice(samples)
	}
	if q >= 1 {
		return maxSlice(samples)
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	pos := q * float64(n-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	frac := pos - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// Distribution 汇总一组样本的计数/极值/均值与常用分位（p50/p90/p99）。
type Distribution struct {
	Count int
	Min   float64
	Max   float64
	Mean  float64
	P50   float64
	P90   float64
	P99   float64
}

// Summarize 计算样本的结构化分布摘要。空样本返回零值 Distribution。
func Summarize(samples []float64) Distribution {
	n := len(samples)
	if n == 0 {
		return Distribution{}
	}
	sum := 0.0
	for _, v := range samples {
		sum += v
	}
	return Distribution{
		Count: n,
		Min:   minSlice(samples),
		Max:   maxSlice(samples),
		Mean:  sum / float64(n),
		P50:   Quantile(samples, 0.50),
		P90:   Quantile(samples, 0.90),
		P99:   Quantile(samples, 0.99),
	}
}

// DurationDistribution 是面向 time.Duration 的分布摘要（如确认延迟、敞口时长）。
type DurationDistribution struct {
	Count int
	Min   time.Duration
	Max   time.Duration
	Mean  time.Duration
	P50   time.Duration
	P90   time.Duration
	P99   time.Duration
}

// SummarizeDurations 计算 time.Duration 样本的分布摘要。
func SummarizeDurations(samples []time.Duration) DurationDistribution {
	if len(samples) == 0 {
		return DurationDistribution{}
	}
	ns := make([]float64, len(samples))
	for i, d := range samples {
		ns[i] = float64(d)
	}
	d := Summarize(ns)
	return DurationDistribution{
		Count: d.Count,
		Min:   time.Duration(d.Min),
		Max:   time.Duration(d.Max),
		Mean:  time.Duration(d.Mean),
		P50:   time.Duration(d.P50),
		P90:   time.Duration(d.P90),
		P99:   time.Duration(d.P99),
	}
}

// WindowTracker 追踪某一状态（如「持有敞口」或「单边裸腿」）的开合窗口，统计累计
// 时长、最长单次时长与发生次数。它是并发不安全的纯状态机，调用方自行保证串行或加锁。
type WindowTracker struct {
	open     bool
	openedAt time.Time
	total    time.Duration
	longest  time.Duration
	windows  int
}

// Open 在 at 时刻打开窗口；若已处于打开态则忽略（幂等）。
func (w *WindowTracker) Open(at time.Time) {
	if w.open {
		return
	}
	w.open = true
	w.openedAt = at
}

// Close 在 at 时刻关闭窗口并累加时长；若未打开或 at 早于开窗时刻则忽略。
func (w *WindowTracker) Close(at time.Time) {
	if !w.open {
		return
	}
	w.open = false
	if at.Before(w.openedAt) {
		return
	}
	d := at.Sub(w.openedAt)
	w.total += d
	if d > w.longest {
		w.longest = d
	}
	w.windows++
}

// IsOpen 报告窗口当前是否处于打开状态。
func (w *WindowTracker) IsOpen() bool { return w.open }

// Total 返回已关闭窗口的累计时长（不含当前未关闭窗口）。
func (w *WindowTracker) Total() time.Duration { return w.total }

// Longest 返回最长一次已关闭窗口的时长。
func (w *WindowTracker) Longest() time.Duration { return w.longest }

// Count 返回已关闭窗口的次数。
func (w *WindowTracker) Count() int { return w.windows }

// Elapsed 返回当前未关闭窗口截至 at 的已持续时长；未打开返回 0。
func (w *WindowTracker) Elapsed(at time.Time) time.Duration {
	if !w.open || at.Before(w.openedAt) {
		return 0
	}
	return at.Sub(w.openedAt)
}

func minSlice(xs []float64) float64 {
	m := xs[0]
	for _, v := range xs[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxSlice(xs []float64) float64 {
	m := xs[0]
	for _, v := range xs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
