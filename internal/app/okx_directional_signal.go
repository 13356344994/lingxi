package app

// okx_directional_signal.go OKX 方向引擎的纯信号/风控核心(无 IO、可单测)。
//
// 设计(综合日志实证 + 用户决策):
//   - 选币交给引擎:OKX 资费快照按 |费率| 排序筛"被资费标记的币"(用户的底子)。
//   - 方向由价格 regime 决定(双向 × 两态):趋势态顺势(EMA 快慢),震荡态反转(z 分),
//     既非只单方向、也非只震荡——满足"两种方向"。
//   - 资费做对齐闸(不预测"价格跟随/反向资费"这一日志无法验证的因果):只在资费不逆持仓时开
//     (多要费率≤阈值、空要费率≥−阈值),令持仓顺带收资费、绝不逆扛资费;资费强对齐时加信心。
//   - 成本地板:预期移动 bps 须 ≥ 成本地板,挡薄盘穿价(OKX 实测往返成本中位 ~0、p95 ~177bps)。
//
// 本文件只做"算",不下单、不落盘、不拉数据;由 okx_directional engine(plumbing)调用。

import (
	"fmt"
	"math"
)

// DirectionSide 方向信号。
type DirectionSide int

const (
	DirectionFlat DirectionSide = iota
	DirectionLong
	DirectionShort
)

func (d DirectionSide) String() string {
	switch d {
	case DirectionLong:
		return "long"
	case DirectionShort:
		return "short"
	default:
		return "flat"
	}
}

// DirectionRegime 价格态。
type DirectionRegime int

const (
	RegimeRange DirectionRegime = iota
	RegimeTrend
)

func (r DirectionRegime) String() string {
	if r == RegimeTrend {
		return "trend"
	}
	return "range"
}

// DirectionalSignal 单币方向判定结果。
type DirectionalSignal struct {
	Side            DirectionSide
	Regime          DirectionRegime
	Conviction      float64 // [0,1] 信心
	ExpectedMoveBps float64 // 预期可捕获移动(bps),供成本地板对比
	Reason          string
}

// emaLast 返回收盘序列的最后一个 EMA 值(period≤1 或样本不足时回退末值/简单均值)。
func emaLast(closes []float64, period int) float64 {
	n := len(closes)
	if n == 0 {
		return 0
	}
	if period <= 1 || n == 1 {
		return closes[n-1]
	}
	k := 2.0 / (float64(period) + 1.0)
	ema := closes[0]
	for i := 1; i < n; i++ {
		ema = closes[i]*k + ema*(1-k)
	}
	return ema
}

// efficiencyRatio Kaufman 效率比 = |净位移| / Σ|逐根位移|,∈[0,1];越高越趋势,越低越震荡。
func efficiencyRatio(closes []float64) float64 {
	n := len(closes)
	if n < 2 {
		return 0
	}
	net := math.Abs(closes[n-1] - closes[0])
	var path float64
	for i := 1; i < n; i++ {
		path += math.Abs(closes[i] - closes[i-1])
	}
	if path <= 0 {
		return 0
	}
	return net / path
}

// meanStd 返回均值与样本标准差(总体口径,n 分母)。
func meanStd(xs []float64) (mean, std float64) {
	n := len(xs)
	if n == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(n)
	var v float64
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return mean, math.Sqrt(v / float64(n))
}

// closeZScore 最新收盘相对窗口均值的 z 分(std=0 时返回 0)。
func closeZScore(closes []float64) (z, mean, std float64) {
	n := len(closes)
	if n < 2 {
		return 0, 0, 0
	}
	mean, std = meanStd(closes)
	if std <= 0 {
		return 0, mean, 0
	}
	return (closes[n-1] - mean) / std, mean, std
}

// fundingPermits 资费对齐闸:返回该方向是否被资费允许(不逆扛资费)+ 是否强对齐(加信心)。
//
//	多:费率 ≤ +minAbs 允许(费率为负=做多还收资费,强对齐);
//	空:费率 ≥ −minAbs 允许(费率为正=做空还收资费,强对齐)。
func fundingPermits(side DirectionSide, fundingRate, minAbs float64) (permit, strong bool) {
	if minAbs < 0 {
		minAbs = 0
	}
	switch side {
	case DirectionLong:
		return fundingRate <= minAbs, fundingRate < 0
	case DirectionShort:
		return fundingRate >= -minAbs, fundingRate > 0
	default:
		return false, false
	}
}

// computeDirectionalSignal 纯函数:由收盘序列 + 当前资费 + 配置算出方向信号。
// closes 须为 OLDEST-first 收盘价。fundingRate 为该币 OKX 单次结算费率(小数)。
func computeDirectionalSignal(closes []float64, fundingRate float64, cfg OKXDirectionalConfig) DirectionalSignal {
	out := DirectionalSignal{Side: DirectionFlat, Regime: RegimeRange}
	n := len(closes)
	if n < 5 || closes[n-1] <= 0 {
		out.Reason = "样本不足"
		return out
	}
	last := closes[n-1]
	er := efficiencyRatio(closes)
	trendThresh := cfg.TrendEfficiencyThreshold
	if trendThresh <= 0 {
		trendThresh = 0.30
	}

	var base DirectionSide
	if er >= trendThresh {
		// 趋势态:顺势(EMA 快慢)。
		out.Regime = RegimeTrend
		fast := emaLast(closes, cfg.EMAFast)
		slow := emaLast(closes, cfg.EMASlow)
		if fast > slow {
			base = DirectionLong
		} else if fast < slow {
			base = DirectionShort
		}
		// 预期移动:趋势态用近窗净位移幅度近似。
		out.ExpectedMoveBps = math.Abs(last-closes[0]) / last * 10000
		out.Conviction = clamp01(er)
	} else {
		// 震荡态:反转(z 分)。
		out.Regime = RegimeRange
		z, _, std := closeZScore(closes)
		zEntry := cfg.ZScoreEntry
		if zEntry <= 0 {
			zEntry = 1.5
		}
		if z >= zEntry {
			base = DirectionShort // 偏高 → 回归做空
		} else if z <= -zEntry {
			base = DirectionLong // 偏低 → 回归做多
		}
		// 预期移动:回归到均值约 |z|×std。
		if std > 0 {
			out.ExpectedMoveBps = math.Abs(z) * std / last * 10000
		}
		out.Conviction = clamp01(math.Abs(z) / (zEntry * 2))
	}

	if base == DirectionFlat {
		out.Reason = "无方向信号(regime=" + out.Regime.String() + ")"
		return out
	}

	// 资费对齐闸。
	if cfg.RequireFundingAlignment {
		permit, strong := fundingPermits(base, fundingRate, cfg.MinFundingAbsPerInterval)
		if !permit {
			out.Reason = "资费逆持仓,放弃(" + base.String() + " 费率=" + formatRatePct(fundingRate) + ")"
			return out
		}
		if strong {
			out.Conviction = clamp01(out.Conviction + 0.2)
		}
	}

	out.Side = base
	out.Reason = out.Regime.String() + " " + base.String()
	return out
}

// directionalShouldClose 持仓出场判定(纯):止损/止盈/信号反转/超时。返回 (是否平, 原因)。
//
//	uPnLPct = 未实现盈亏 / 名义(已含方向符号,正=盈)。holdMin = 已持有分钟。
func directionalShouldClose(uPnLPct float64, holdMin int, side DirectionSide, fresh DirectionalSignal, cfg OKXDirectionalConfig) (bool, string) {
	sl := cfg.StopLossPct
	if sl <= 0 {
		sl = 0.025
	}
	tp := cfg.TakeProfitPct
	if tp <= 0 {
		tp = 0.015
	}
	if uPnLPct <= -sl {
		return true, "硬止损"
	}
	if uPnLPct >= tp {
		return true, "止盈"
	}
	if cfg.MaxHoldMinutes > 0 && holdMin >= cfg.MaxHoldMinutes {
		return true, "超时离场"
	}
	// 信号反转:新信号给出相反方向且有信心 → 平(由引擎决定是否反手)。
	if fresh.Side != DirectionFlat && fresh.Side != side {
		return true, "信号反转"
	}
	return false, ""
}

// formatRatePct 把单次费率格式化成百分比字符串(供日志)。
func formatRatePct(r float64) string {
	return fmt.Sprintf("%.4f%%", r*100)
}
