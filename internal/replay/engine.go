package replay

import (
	"fmt"
	"math"
	"sort"
	"time"

	"lingxi/internal/money"
)

// ReplayTrade 结算单笔交易:拆解毛 PnL、fee、funding、滑点与未成交反事实。纯函数。
func ReplayTrade(t Trade) TradeResult {
	r := TradeResult{Trade: t}
	sign := t.Side.Sign()
	notional := math.Max(0, t.NotionalUSDT)
	fillRatio := t.FillRatio
	if fillRatio < 0 {
		fillRatio = 0
	}
	if fillRatio > 1 {
		fillRatio = 1
	}

	// 未成交反事实:以入场/出场 mid 计算 mid-to-mid 毛收益(不含费用)。
	if fillRatio == 0 || sign == 0 {
		r.Unfilled = true
		if sign != 0 && t.EntryMid > 0 && t.ExitMid > 0 {
			r.CounterfactualGrossUSDT = sign * (t.ExitMid - t.EntryMid) / t.EntryMid * notional
		}
		return r
	}

	filledNotional := notional * fillRatio
	// 毛 PnL:交易所真相优先,否则由实际成交价推导。
	if t.GrossPnLUSDT != nil {
		r.GrossUSDT = *t.GrossPnLUSDT
	} else if t.EntryPrice > 0 && t.ExitPrice > 0 {
		r.GrossUSDT = sign * (t.ExitPrice - t.EntryPrice) / t.EntryPrice * filledNotional
	}
	// 滑点归因(不重复扣减,只拆解):相对 mid 的执行损耗,正=成本。
	if t.EntryMid > 0 && t.EntryPrice > 0 {
		r.EntrySlipUSDT = sign * (t.EntryPrice - t.EntryMid) / t.EntryMid * filledNotional
	}
	if t.ExitMid > 0 && t.ExitPrice > 0 {
		r.ExitSlipUSDT = sign * (t.ExitMid - t.ExitPrice) / t.ExitMid * filledNotional
	}
	r.FeeUSDT = t.FeeUSDT
	r.FundingUSDT = t.FundingUSDT
	r.NetUSDT = money.SumUSDT(r.GrossUSDT, -r.FeeUSDT, -r.FundingUSDT)
	return r
}

// ReplayAll 按 CloseTime 稳定排序后逐笔结算。输入切片不被修改。
func ReplayAll(trades []Trade) []TradeResult {
	sorted := make([]Trade, len(trades))
	copy(sorted, trades)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CloseTime.Before(sorted[j].CloseTime) })
	out := make([]TradeResult, 0, len(sorted))
	for _, t := range sorted {
		out = append(out, ReplayTrade(t))
	}
	return out
}

// BuildEquityPath 从初始权益与逐笔净结果构造账户权益路径(定点累加,避免顺序漂移)。
// 首点为起始权益(时间取首笔 CloseTime 前,若无交易则只有起点、时间零值)。
func BuildEquityPath(initialUSDT float64, results []TradeResult) []PathPoint {
	path := make([]PathPoint, 0, len(results)+1)
	var t0 time.Time
	if len(results) > 0 {
		t0 = results[0].Trade.CloseTime
	}
	path = append(path, PathPoint{Time: t0, EquityUSDT: initialUSDT})
	equity := money.FromUSDT(initialUSDT)
	for _, r := range results {
		equity = equity.Add(money.FromUSDT(r.NetUSDT))
		path = append(path, PathPoint{Time: r.Trade.CloseTime, EquityUSDT: equity.USDT()})
	}
	return path
}

// PathMetrics 账户路径指标(计划文档第 5 节:净 USDT、净对数增长、最大回撤、CVaR)。
type PathMetrics struct {
	NetUSDT        float64 // 终点-起点
	LogGrowth      float64 // ln(end/start);任一端 <=0 时为 -Inf(视为破产/不可比)
	MaxDrawdownPct float64 // 峰值回撤比例(0~1,正值)
	Ruined         bool    // 路径上是否触及 <=0
}

// ComputePathMetrics 计算路径指标。空路径或单点路径返回零值。
func ComputePathMetrics(path []PathPoint) PathMetrics {
	var m PathMetrics
	if len(path) == 0 {
		return m
	}
	start := path[0].EquityUSDT
	end := path[len(path)-1].EquityUSDT
	m.NetUSDT = money.SumUSDT(end, -start)
	if start > 0 && end > 0 {
		m.LogGrowth = math.Log(end / start)
	} else {
		m.LogGrowth = math.Inf(-1)
	}
	peak := math.Inf(-1)
	for _, p := range path {
		if p.EquityUSDT <= 0 {
			m.Ruined = true
		}
		if p.EquityUSDT > peak {
			peak = p.EquityUSDT
		}
		if peak > 0 {
			dd := (peak - p.EquityUSDT) / peak
			if dd > m.MaxDrawdownPct {
				m.MaxDrawdownPct = dd
			}
		}
	}
	if m.Ruined {
		m.LogGrowth = math.Inf(-1)
	}
	return m
}

// GroupEpisodes 把逐笔结果聚成相关 episode:
//  1. EpisodeID 非空 → 同 ID 一簇;
//  2. 否则同 Cluster 且与簇内上一笔 OpenTime 间隔 <= window 的交易链入同簇
//     (强平接力:每一档都与上一档相邻,整条链是一个 episode);
//  3. Cluster 也为空 → 每笔独立 episode。
//
// 返回按 Start 升序。window<=0 时默认 10 分钟。
func GroupEpisodes(results []TradeResult, window time.Duration) []Episode {
	if window <= 0 {
		window = 10 * time.Minute
	}
	type acc struct {
		ep       *Episode
		net      money.Micros
		lastOpen time.Time // 簇内最后一笔 OpenTime(链式相邻判定)
	}
	byKey := map[string]*acc{}
	order := []*acc{}
	newAcc := func(key, cluster string, start time.Time) *acc {
		a := &acc{ep: &Episode{Key: key, Cluster: cluster, Start: start}}
		byKey[key] = a
		order = append(order, a)
		return a
	}
	sorted := make([]TradeResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Trade.OpenTime.Before(sorted[j].Trade.OpenTime) })

	soloSeq := 0
	for _, r := range sorted {
		t := r.Trade
		var a *acc
		switch {
		case t.EpisodeID != "":
			key := "id:" + t.EpisodeID
			a = byKey[key]
			if a == nil {
				a = newAcc(key, t.Cluster, t.OpenTime)
			}
		case t.Cluster != "":
			key := "cluster:" + t.Cluster
			if prev := byKey[key]; prev != nil && t.OpenTime.Sub(prev.lastOpen) <= window {
				a = prev
			} else {
				// 超窗:开新簇,并让 key 指向最新簇(旧簇留在 order 中)。
				a = newAcc(key, t.Cluster, t.OpenTime)
				a.ep.Key = fmt.Sprintf("%s@%d", key, t.OpenTime.UnixNano())
			}
		default:
			soloSeq++
			a = newAcc(fmt.Sprintf("solo:%d", soloSeq), "", t.OpenTime)
		}
		a.lastOpen = t.OpenTime
		a.net = a.net.Add(money.FromUSDT(r.NetUSDT))
		a.ep.Trades++
		if r.Unfilled {
			a.ep.Unfilled++
		}
		if t.OpenTime.Before(a.ep.Start) {
			a.ep.Start = t.OpenTime
		}
	}
	out := make([]Episode, 0, len(order))
	for _, a := range order {
		a.ep.NetUSDT = a.net.USDT()
		out = append(out, *a.ep)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// EpisodeCVaR 计算 episode 净 PnL 的 CVaR(期望短缺,损失口径为正):
// 取最差 ceil(alpha*n) 个 episode 的平均亏损。alpha∈(0,1],常用 0.05。
// 样本为空返回 0;alpha 越界会被夹到 (0,1]。
func EpisodeCVaR(episodes []Episode, alpha float64) float64 {
	if len(episodes) == 0 {
		return 0
	}
	if alpha <= 0 {
		alpha = 0.05
	}
	if alpha > 1 {
		alpha = 1
	}
	pnl := make([]float64, len(episodes))
	for i, e := range episodes {
		pnl[i] = e.NetUSDT
	}
	sort.Float64s(pnl) // 升序:最差在前
	k := int(math.Ceil(alpha * float64(len(pnl))))
	if k < 1 {
		k = 1
	}
	var sum money.Micros
	for _, v := range pnl[:k] {
		sum = sum.Add(money.FromUSDT(v))
	}
	mean := sum.USDT() / float64(k)
	return -mean // 损失为正
}
