package app

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// settlement_attribution.go 归因台(只读):把已落盘的五策略 settlements 按"资金费 vs 方向/基差 PnL vs 成本"拆解,
// 并给出小资金语境下的 edge 判据——若意图赚的 funding 中位远小于方向 PnL 中位的绝对值,说明净盈亏由未补偿的
// 方向/基差敞口主导(运气),而非 carry alpha。供 /api/rotation/attribution。不重算、不回放,直接对真实成交归因。

// SettlementAttributionRow 一个分组的归因聚合。
type SettlementAttributionRow struct {
	Key           string  `json:"key"`
	TradeCount    int     `json:"trade_count"`
	NetPnLUSDT    float64 `json:"net_pnl_usdt"`
	FundingUSDT   float64 `json:"funding_usdt"`   // 实收资金费(意图 alpha)
	DirectionUSDT float64 `json:"direction_usdt"` // trade_pnl(含滑点,方向/基差敞口)
	CostUSDT      float64 `json:"cost_usdt"`      // 手续费 + 借币 + 回滚
	WinRatePct    float64 `json:"win_rate_pct"`
	NotionalUSDT  float64 `json:"notional_usdt"`

	winCount int // 非导出:净盈利笔数累计,json 忽略,finalize 时换算 WinRatePct。
}

// SettlementAttributionReport 归因台总报告。
type SettlementAttributionReport struct {
	GeneratedAt time.Time `json:"generated_at"`
	Since       time.Time `json:"since,omitempty"`

	Trusted   SettlementAttributionRow `json:"trusted_overall"`
	Untrusted SettlementAttributionRow `json:"untrusted_overall"`

	// edge 判据(仅基于可信样本)。
	FundingPerNotionalMedianBps      float64 `json:"funding_per_notional_median_bps"`
	DirectionAbsPerNotionalMedianBps float64 `json:"direction_abs_per_notional_median_bps"`
	EdgeRatio                        float64 `json:"edge_ratio"` // funding中位 / 方向绝对中位;>1 才说明 carry 盖过方向噪声
	EdgeVerdict                      string  `json:"edge_verdict"`

	ByStrategy       []SettlementAttributionRow `json:"by_strategy"`
	BySymbol         []SettlementAttributionRow `json:"by_symbol"`
	ByCloseReason    []SettlementAttributionRow `json:"by_close_reason"`
	ByNotionalBucket []SettlementAttributionRow `json:"by_notional_bucket"`
}

// settlementDirectionUSDT 方向/基差 PnL = trade_pnl(滑点已隐含其中,不再另扣)。
func settlementDirectionUSDT(rec strategySettlementRecord) float64 {
	return rec.TradePnL
}

// settlementCostUSDT 显式成本:总手续费 + 借币 + 回滚(滑点已在 trade_pnl,不重复计)。
func settlementCostUSDT(rec strategySettlementRecord) float64 {
	return rec.TotalFees + rec.BorrowCost + rec.RollbackCost
}

func (r *SettlementAttributionRow) add(rec strategySettlementRecord) {
	r.TradeCount++
	r.NetPnLUSDT += rec.NetPnL
	r.FundingUSDT += rec.RealizedFunding
	r.DirectionUSDT += settlementDirectionUSDT(rec)
	r.CostUSDT += settlementCostUSDT(rec)
	r.NotionalUSDT += rec.NotionalUSDT
	if rec.NetPnL > 0 {
		r.winCount++
	}
}

func (r *SettlementAttributionRow) finalize() {
	if r.TradeCount > 0 {
		r.WinRatePct = float64(r.winCount) / float64(r.TradeCount) * 100
	}
}

func normalizeCloseReason(reason string) string {
	r := strings.TrimSpace(reason)
	if r == "" {
		return "(空)"
	}
	switch {
	case strings.Contains(r, "计划离场") || strings.Contains(r, "目标结算"):
		return "计划离场"
	case strings.Contains(r, "硬止损"):
		return "硬止损"
	case strings.Contains(r, "基差"):
		return "基差止损"
	case strings.Contains(r, "年化") || strings.Contains(r, "价差"):
		return "价差止损"
	case strings.Contains(r, "reconcile") || strings.Contains(r, "orphan"):
		return "对账/孤腿"
	case strings.Contains(r, "充提") || strings.Contains(r, "风控") || strings.Contains(r, "硬停"):
		return "风控"
	case strings.Contains(r, "dust") || strings.Contains(r, "粉尘"):
		return "dust"
	case strings.Contains(r, "EV") || strings.Contains(r, "净值"):
		return "EV转弱"
	default:
		if len([]rune(r)) > 14 {
			return string([]rune(r)[:14])
		}
		return r
	}
}

func notionalBucket(n float64) string {
	switch {
	case n <= 0:
		return "0(未知)"
	case n < 10:
		return "a:<10"
	case n < 25:
		return "b:10-25"
	case n < 50:
		return "c:25-50"
	case n < 100:
		return "d:50-100"
	default:
		return "e:>=100"
	}
}

func medianFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func sortedAttributionRows(m map[string]*SettlementAttributionRow) []SettlementAttributionRow {
	out := make([]SettlementAttributionRow, 0, len(m))
	for _, v := range m {
		v.finalize()
		out = append(out, *v)
	}
	// 按净盈亏升序(最拖后腿的排最前,便于定位放血点)。
	sort.Slice(out, func(i, j int) bool { return out[i].NetPnLUSDT < out[j].NetPnLUSDT })
	return out
}

// BuildSettlementAttributionReport 对已落盘五策略 settlements 做归因。since 为零值时不按时间过滤。
func BuildSettlementAttributionReport(records []strategySettlementRecord, since time.Time) SettlementAttributionReport {
	rep := SettlementAttributionReport{GeneratedAt: time.Now().UTC(), Since: since}
	byStrategy := map[string]*SettlementAttributionRow{}
	bySymbol := map[string]*SettlementAttributionRow{}
	byReason := map[string]*SettlementAttributionRow{}
	byBucket := map[string]*SettlementAttributionRow{}
	fundingPerNotionalBps := make([]float64, 0, len(records))
	directionAbsPerNotionalBps := make([]float64, 0, len(records))

	for _, rec := range records {
		if !since.IsZero() && rec.CloseTime.Before(since) {
			continue
		}
		if !isTrustedStrategySettlementRecord(rec) {
			rep.Untrusted.add(rec)
			continue
		}
		rep.Trusted.add(rec)

		key := func(m map[string]*SettlementAttributionRow, k string) *SettlementAttributionRow {
			if m[k] == nil {
				m[k] = &SettlementAttributionRow{Key: k}
			}
			return m[k]
		}
		key(byStrategy, nonEmpty(rec.StrategyType, "(未知)")).add(rec)
		key(bySymbol, nonEmpty(rec.Symbol, "(未知)")).add(rec)
		key(byReason, normalizeCloseReason(rec.CloseReason)).add(rec)
		key(byBucket, notionalBucket(rec.NotionalUSDT)).add(rec)

		if rec.NotionalUSDT > 0 {
			fundingPerNotionalBps = append(fundingPerNotionalBps, rec.RealizedFunding/rec.NotionalUSDT*10000)
			directionAbsPerNotionalBps = append(directionAbsPerNotionalBps, math.Abs(rec.TradePnL)/rec.NotionalUSDT*10000)
		}
	}
	rep.Trusted.Key = "trusted_overall"
	rep.Trusted.finalize()
	rep.Untrusted.Key = "untrusted_overall"
	rep.Untrusted.finalize()

	rep.FundingPerNotionalMedianBps = medianFloat(fundingPerNotionalBps)
	rep.DirectionAbsPerNotionalMedianBps = medianFloat(directionAbsPerNotionalBps)
	if rep.DirectionAbsPerNotionalMedianBps > 1e-9 {
		rep.EdgeRatio = rep.FundingPerNotionalMedianBps / rep.DirectionAbsPerNotionalMedianBps
	}
	rep.EdgeVerdict = attributionEdgeVerdict(rep)

	rep.ByStrategy = sortedAttributionRows(byStrategy)
	rep.BySymbol = sortedAttributionRows(bySymbol)
	rep.ByCloseReason = sortedAttributionRows(byReason)
	rep.ByNotionalBucket = sortedAttributionRows(byBucket)
	return rep
}

// attributionEdgeVerdict 以可信样本的聚合口径为主判据(中位数在多数笔近零时退化,仅作辅助参考)。
// 核心问题:意图赚的资金费(FundingUSDT)是否盖过未补偿的方向/基差敞口(|DirectionUSDT|)与成本(CostUSDT)。
func attributionEdgeVerdict(rep SettlementAttributionReport) string {
	tr := rep.Trusted
	if tr.TradeCount < 30 {
		return "证据不足:可信样本 < 30 笔,无法判定 edge(继续累计或用回放/影子放大样本)"
	}
	absDir := math.Abs(tr.DirectionUSDT)
	fundingDominates := tr.FundingUSDT > 0 && tr.FundingUSDT >= absDir
	switch {
	case tr.NetPnLUSDT > 0 && fundingDominates && tr.FundingUSDT >= tr.CostUSDT:
		return "carry edge 成立:资金费为净盈利主来源,且 ≥ 方向敞口与成本——意图 alpha 在当前样本上成立(样本仍偏小,需持续验证)"
	case tr.NetPnLUSDT > 0 && fundingDominates:
		return "carry 边际成立:资金费盖过方向敞口,但被成本侵蚀大半;降摩擦(maker/多结算/选币)可放大"
	case absDir > tr.FundingUSDT:
		return "edge 不在 carry:方向/基差 PnL 绝对值大于资金费——净盈亏受未补偿的方向敞口(运气)主导"
	default:
		return "edge 微弱:资金费与方向敞口同量级,净盈亏接近噪声,尚不能称为可持续 carry"
	}
}

// handleSettlementAttributionAPI GET /api/rotation/attribution
func (s *DashboardServer) handleSettlementAttributionAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	since := parseSinceQuery(r)
	limit := parseLimit(r, 5000)
	records, err := loadRotationStrategySettlementRecords(s, since, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, BuildSettlementAttributionReport(records, since))
}
