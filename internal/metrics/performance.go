package metrics

import (
	"math"
	"sort"
	"time"
)

// 本文件实现组合/单所绩效指标的纯计算内核(MULTI_VENUE_REFACTOR §9.4 口径已锁定)。无网络、无状态、不依赖
// internal/app:输入是「按 UTC 日重采样后的日收益序列 r_t」与「同一批 UTC 日对齐的基准日收益 b_t」,输出
// PerformanceMetrics。数据管道(读 records/<venue>/settlements.json、按 FundingSettledAt 的 UTC 日聚合、
// 只取 FundingIncomePending=false 的最终净利、补零空白日、除以分配本金得 r_t)由调用方(面板/聚合层)装配,
// 这里只做可单测的纯数学,使实盘与回测共用同一套口径。

const (
	// DefaultAnnualFactor 是 crypto 7×24 的年化因子(按 365 个 UTC 日,§9.1)。
	DefaultAnnualFactor = 365.0
	// DefaultBenchmarkSymbol 固定为 Binance 现货 BTCUSDT 日 K(公共、与被测 venue 无关,§9.1)。
	DefaultBenchmarkSymbol = "BTCUSDT"
)

// PerformanceMetrics 是一段日收益序列的绩效摘要(§9.4)。比率类在 N<2 时无定义,返回 0 且 Insufficient=true。
// Alpha/Beta 另有 BenchmarkInsufficient:基准缺失/长度不齐/Var(b)=0 时置 true 且 Alpha=Beta=0(不静默换源,§9.1)。
type PerformanceMetrics struct {
	StartEquity      float64 `json:"start_equity"`
	EndEquity        float64 `json:"end_equity"`
	TotalReturn      float64 `json:"total_return"`
	AnnualizedReturn float64 `json:"annualized_return"`
	MaxDrawdown      float64 `json:"max_drawdown"`      // USDT(分配本金为基)
	MaxDrawdownRate  float64 `json:"max_drawdown_rate"` // 比率,本金无关
	Sharpe           float64 `json:"sharpe"`
	Sortino          float64 `json:"sortino"`
	Calmar           float64 `json:"calmar"`
	Alpha            float64 `json:"alpha"`
	Beta             float64 `json:"beta"`
	WinRate          float64 `json:"win_rate"`
	Samples          int     `json:"samples"`
	Insufficient     bool    `json:"insufficient"`           // N<2:比率无定义
	BenchmarkMissing bool    `json:"benchmark_insufficient"` // 基准不可用 → Alpha/Beta 无效
	RiskFree         float64 `json:"risk_free"`
	AnnualFactor     float64 `json:"annual_factor"`
	BenchmarkSymbol  string  `json:"benchmark_symbol"`
}

// PerformanceInputs 是 ComputePerformance 的入参。Returns 是按 UTC 日重采样的日收益 r_t(已除以分配本金);
// Benchmark 是与 Returns **同序、同长** 的基准日收益 b_t(可为 nil/空 → 跳过 Alpha/Beta)。
type PerformanceInputs struct {
	Returns          []float64
	Benchmark        []float64
	AllocatedCapital float64
	RiskFree         float64 // 日无风险利率,默认 0
	AnnualFactor     float64 // 默认 365;≤0 时回退 DefaultAnnualFactor
	BenchmarkSymbol  string  // 默认 DefaultBenchmarkSymbol
}

// ComputePerformance 按 §9.4 公式计算绩效指标。纯函数:不改入参、无副作用。
func ComputePerformance(in PerformanceInputs) PerformanceMetrics {
	annual := in.AnnualFactor
	if annual <= 0 {
		annual = DefaultAnnualFactor
	}
	benchSymbol := in.BenchmarkSymbol
	if benchSymbol == "" {
		benchSymbol = DefaultBenchmarkSymbol
	}
	r := in.Returns
	n := len(r)
	rf := in.RiskFree
	capital := in.AllocatedCapital

	m := PerformanceMetrics{
		Samples:         n,
		RiskFree:        rf,
		AnnualFactor:    annual,
		BenchmarkSymbol: benchSymbol,
		StartEquity:     capital,
		EndEquity:       capital,
	}

	if n == 0 {
		m.Insufficient = true
		m.BenchmarkMissing = true
		return m
	}

	// 权益曲线(简单累加口径,§9.3):equityFactor_t = 1 + Σ_{i≤t} r_i;equity_t = capital * equityFactor_t。
	// 回撤率与本金无关(capital 约分),回撤 USDT 以分配本金为基放大。
	cum := 0.0
	wins := 0
	peakFactor := 1.0
	maxDDRate := 0.0
	maxDDFactor := 0.0 // peakFactor - equityFactor 的最大值(乘 capital 得 USDT 回撤)
	for _, rt := range r {
		cum += rt
		if rt > 0 {
			wins++
		}
		factor := 1.0 + cum
		if factor > peakFactor {
			peakFactor = factor
		}
		ddFactor := peakFactor - factor
		if ddFactor > maxDDFactor {
			maxDDFactor = ddFactor
		}
		if peakFactor > 0 {
			if rate := ddFactor / peakFactor; rate > maxDDRate {
				maxDDRate = rate
			}
		}
	}
	m.TotalReturn = cum
	m.EndEquity = capital * (1.0 + cum)
	m.MaxDrawdown = capital * maxDDFactor
	m.MaxDrawdownRate = maxDDRate
	m.WinRate = float64(wins) / float64(n)

	if n < 2 {
		// 单样本:权益/回撤/胜率有意义,但比率(Sharpe/Sortino/Calmar/Alpha/Beta/年化)无定义。
		m.Insufficient = true
		m.BenchmarkMissing = true
		return m
	}

	mean := cum / float64(n)
	// 样本标准差(N-1);下行偏差用 N(§9.4)。
	var sumSqDev, sumDownside float64
	for _, rt := range r {
		dev := rt - mean
		sumSqDev += dev * dev
		if d := rt - rf; d < 0 {
			sumDownside += d * d
		}
	}
	std := math.Sqrt(sumSqDev / float64(n-1))
	downside := math.Sqrt(sumDownside / float64(n))
	sqrtAnnual := math.Sqrt(annual)

	if std > 0 {
		m.Sharpe = (mean - rf) / std * sqrtAnnual
	}
	if downside > 0 {
		m.Sortino = (mean - rf) / downside * sqrtAnnual
	}

	// 年化收益(几何):(Π(1+r_t))^(annual/N) − 1。任一 (1+r_t)≤0 则乘积≤0,几何年化无定义 → 置 0。
	growth := 1.0
	valid := true
	for _, rt := range r {
		g := 1.0 + rt
		if g <= 0 {
			valid = false
			break
		}
		growth *= g
	}
	if valid && growth > 0 {
		m.AnnualizedReturn = math.Pow(growth, annual/float64(n)) - 1.0
	}

	if m.MaxDrawdownRate > 0 {
		m.Calmar = m.AnnualizedReturn / math.Abs(m.MaxDrawdownRate)
	}

	// Alpha/Beta:基准须与收益同长且非空,且 Var(b)≠0。
	b := in.Benchmark
	if len(b) != n {
		m.BenchmarkMissing = true
	} else {
		var meanB float64
		for _, bt := range b {
			meanB += bt
		}
		meanB /= float64(n)
		var cov, varB float64
		for i := 0; i < n; i++ {
			db := b[i] - meanB
			cov += (r[i] - mean) * db
			varB += db * db
		}
		if varB <= 0 {
			m.BenchmarkMissing = true
		} else {
			m.Beta = cov / varB
			// Alpha 年化:(1 + (mean(r) − Beta·mean(b)))^annual − 1。底数≤0 时年化无定义 → 置 0。
			base := 1.0 + (mean - m.Beta*meanB)
			if base > 0 {
				m.Alpha = math.Pow(base, annual) - 1.0
			}
		}
	}

	return m
}

// DailyPnL 是某 UTC 日的已结算净利(资金费已到账)。Day 为 "2006-01-02"。
type DailyPnL struct {
	Day string
	PnL float64
}

const dayLayout = "2006-01-02"

// UTCDay 把时刻归一为其所在 UTC 日的 "2006-01-02"。
func UTCDay(t time.Time) string {
	return t.UTC().Format(dayLayout)
}

// BuildDailySeries 把零散的 (UTC 日, 净利) 条目按日汇总,并在首末活动日之间**补零空白日**,返回按日期升序的连续序列
// (§9.3 步骤 2)。非法日期字符串的条目被跳过。空输入返回 nil。
func BuildDailySeries(entries []DailyPnL) []DailyPnL {
	if len(entries) == 0 {
		return nil
	}
	byDay := make(map[string]float64, len(entries))
	days := make([]time.Time, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		parsed, err := time.Parse(dayLayout, e.Day)
		if err != nil {
			continue
		}
		key := parsed.UTC().Format(dayLayout)
		byDay[key] += e.PnL
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			days = append(days, parsed.UTC())
		}
	}
	if len(days) == 0 {
		return nil
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	first := days[0]
	last := days[len(days)-1]

	var out []DailyPnL
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		key := d.Format(dayLayout)
		out = append(out, DailyPnL{Day: key, PnL: byDay[key]}) // 缺失日 → map 零值 0,即补零
	}
	return out
}

// ReturnsFromDaily 把日净利序列除以分配本金得日收益 r_t(§9.3 步骤 3)。capital≤0 返回 nil(无意义分母)。
func ReturnsFromDaily(series []DailyPnL, capital float64) []float64 {
	if capital <= 0 || len(series) == 0 {
		return nil
	}
	out := make([]float64, len(series))
	for i, d := range series {
		out[i] = d.PnL / capital
	}
	return out
}

// EquityPoint 是权益曲线上的一个点,供面板画权益曲线 + 水下回撤(§9.6)。
type EquityPoint struct {
	Day          string  `json:"day"`
	Equity       float64 `json:"equity"`
	Drawdown     float64 `json:"drawdown"`      // USDT,自前缀高水位的回撤(≥0)
	DrawdownRate float64 `json:"drawdown_rate"` // 比率,自前缀高水位(≥0)
}

// EquityCurve 由日收益与分配本金生成权益曲线点序列(与 ComputePerformance 同口径:equity=capital*(1+Σr))。
// days 可为 nil(则点不带日期标签)。capital≤0 返回 nil。
func EquityCurve(days []string, returns []float64, capital float64) []EquityPoint {
	if capital <= 0 || len(returns) == 0 {
		return nil
	}
	out := make([]EquityPoint, len(returns))
	cum := 0.0
	peak := capital
	for i, rt := range returns {
		cum += rt
		equity := capital * (1.0 + cum)
		if equity > peak {
			peak = equity
		}
		dd := peak - equity
		rate := 0.0
		if peak > 0 {
			rate = dd / peak
		}
		day := ""
		if i < len(days) {
			day = days[i]
		}
		out[i] = EquityPoint{Day: day, Equity: equity, Drawdown: dd, DrawdownRate: rate}
	}
	return out
}
