// Package reconcile 提供「实盘 ↔ 回测」逐笔对账框架与 gap 阈值守卫（Gate 4）。
//
// 它把两侧的成交记录按业务键配对，逐字段计算净收益/资金费/交易盈亏/手续费的偏差（gap），
// 并用可配置阈值（绝对 USDT + 相对比例）判定单笔是否「对不上」；同时汇总未配对记录与总偏差，
// 用于线上跑 N 天后核对实盘是否系统性偏离回测假设。所有金额走 money 定点内核做精确比较，
// 偏差不会被 float64 连加噪声掩盖。
//
// 本包是纯逻辑、无网络/无状态依赖，可独立单测；实盘侧与回测侧各自把成交映射成 TradeRecord 注入。
package reconcile

import (
	"math"
	"sort"

	"lingxi/internal/money"
)

// TradeRecord 是对账的最小单元：一笔成交在「净收益/资金费/交易盈亏/手续费/名义」上的快照。
// Key 是配对键（建议用 symbol + 资金费时间 或唯一 trace id），同一 Key 在一侧只应出现一次。
type TradeRecord struct {
	Key           string
	Symbol        string
	NetPnL        float64
	FundingIncome float64
	TradingPnL    float64
	TotalFees     float64
	Notional      float64
}

// FieldGap 描述单个字段的实盘-回测偏差：绝对差（USDT）与相对差（相对回测值，回测为 0 时退化为相对名义）。
type FieldGap struct {
	Field    string
	Live     float64
	Backtest float64
	AbsGap   float64
	RelGap   float64
}

// Thresholds 是 gap 守卫阈值。AbsUSDT 为单字段绝对偏差上限（USDT），Rel 为单字段相对偏差上限
// （0~1）。TotalAbsUSDT 为「全部已配对记录净收益偏差绝对值之和」的上限。任一项 <=0 表示该维度不设限。
type Thresholds struct {
	AbsUSDT      float64
	Rel          float64
	TotalAbsUSDT float64
}

// RecordReconciliation 是单笔配对结果：各字段 gap 及是否触发阈值。
type RecordReconciliation struct {
	Key      string
	Symbol   string
	Gaps     []FieldGap
	Breached bool
	Breaches []string
}

// Report 是一次对账的完整结果。
type Report struct {
	Records          []RecordReconciliation
	OnlyLiveKeys     []string
	OnlyBacktestKeys []string
	MatchedCount     int
	BreachedCount    int
	TotalNetAbsGap   float64
	TotalBreached    bool
	Healthy          bool
}

// Reconcile 把实盘与回测记录按 Key 配对，逐字段计算 gap 并按阈值守卫，返回完整对账报告。
// 未配对（仅实盘 / 仅回测出现）的键单列出来，本身视为不健康信号。
func Reconcile(live, backtest []TradeRecord, thresholds Thresholds) Report {
	liveByKey := indexByKey(live)
	backtestByKey := indexByKey(backtest)

	report := Report{}
	var totalNetGap money.Micros

	keys := sortedUnionKeys(liveByKey, backtestByKey)
	for _, key := range keys {
		liveRec, hasLive := liveByKey[key]
		backtestRec, hasBacktest := backtestByKey[key]
		switch {
		case hasLive && !hasBacktest:
			report.OnlyLiveKeys = append(report.OnlyLiveKeys, key)
		case !hasLive && hasBacktest:
			report.OnlyBacktestKeys = append(report.OnlyBacktestKeys, key)
		default:
			rec := reconcileRecord(liveRec, backtestRec, thresholds)
			report.Records = append(report.Records, rec)
			report.MatchedCount++
			if rec.Breached {
				report.BreachedCount++
			}
			netGap := money.FromUSDT(liveRec.NetPnL).Sub(money.FromUSDT(backtestRec.NetPnL))
			totalNetGap = totalNetGap.Add(netGap.Abs())
		}
	}

	report.TotalNetAbsGap = totalNetGap.USDT()
	if thresholds.TotalAbsUSDT > 0 && report.TotalNetAbsGap > thresholds.TotalAbsUSDT {
		report.TotalBreached = true
	}
	report.Healthy = report.BreachedCount == 0 &&
		!report.TotalBreached &&
		len(report.OnlyLiveKeys) == 0 &&
		len(report.OnlyBacktestKeys) == 0
	return report
}

func reconcileRecord(live, backtest TradeRecord, thresholds Thresholds) RecordReconciliation {
	rec := RecordReconciliation{Key: live.Key, Symbol: live.Symbol}
	notional := math.Max(math.Abs(live.Notional), math.Abs(backtest.Notional))
	fields := []struct {
		name           string
		live, backtest float64
	}{
		{"net_pnl", live.NetPnL, backtest.NetPnL},
		{"funding_income", live.FundingIncome, backtest.FundingIncome},
		{"trading_pnl", live.TradingPnL, backtest.TradingPnL},
		{"total_fees", live.TotalFees, backtest.TotalFees},
	}
	for _, f := range fields {
		gap := computeFieldGap(f.name, f.live, f.backtest, notional)
		rec.Gaps = append(rec.Gaps, gap)
		if breachField(gap, thresholds) {
			rec.Breached = true
			rec.Breaches = append(rec.Breaches, f.name)
		}
	}
	return rec
}

// computeFieldGap 用 money 定点内核计算绝对差；相对差以回测值为分母，回测为 0 时退化为名义为分母，
// 名义也为 0 时相对差记为 0（绝对差仍照常守卫）。
func computeFieldGap(field string, live, backtest, notional float64) FieldGap {
	absGap := money.FromUSDT(live).Sub(money.FromUSDT(backtest)).Abs().USDT()
	denom := math.Abs(backtest)
	if denom == 0 {
		denom = math.Abs(notional)
	}
	relGap := 0.0
	if denom > 0 {
		relGap = absGap / denom
	}
	return FieldGap{Field: field, Live: live, Backtest: backtest, AbsGap: absGap, RelGap: relGap}
}

// breachField 报告某字段 gap 是否同时突破了「绝对 + 相对」两道阈值（两者都设限时需同时突破才算 breach，
// 避免噪声级小额绝对差或小分母放大相对差单独触发误报）。只设一道时按该道判定。
func breachField(gap FieldGap, thresholds Thresholds) bool {
	absLimited := thresholds.AbsUSDT > 0
	relLimited := thresholds.Rel > 0
	absBreach := absLimited && gap.AbsGap > thresholds.AbsUSDT
	relBreach := relLimited && gap.RelGap > thresholds.Rel
	switch {
	case absLimited && relLimited:
		return absBreach && relBreach
	case absLimited:
		return absBreach
	case relLimited:
		return relBreach
	default:
		return false
	}
}

func indexByKey(records []TradeRecord) map[string]TradeRecord {
	out := make(map[string]TradeRecord, len(records))
	for _, r := range records {
		out[r.Key] = r
	}
	return out
}

func sortedUnionKeys(a, b map[string]TradeRecord) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
