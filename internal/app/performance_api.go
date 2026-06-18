package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"lingxi/internal/metrics"
	"lingxi/internal/money"
)

// performance_api.go 落地 §9 绩效指标的读 API(P3f.3)。数据管道:读该所(或 venue=all 组合)的 settlements,
// 只取资金费已到账的最终净利,按 FundingSettledAt 的 UTC 日聚合 + 补零,÷分配本金得日收益序列,喂
// internal/metrics 纯函数算 Sharpe/Sortino/Calmar/MaxDD/WinRate/Alpha/Beta 与权益曲线。
//
// 基准(BTC 现货日 K → Alpha/Beta)由 benchmark.go 尽力而为拉取公共 K 线并对齐日序后喂入;拉取失败/缺日 →
// Benchmark=nil → 指标里 BenchmarkMissing=true、Alpha/Beta=0(§9.5 小样本本就主推 MaxDD/Sortino/胜率,不阻塞)。

// PerformanceResponse 是 /api/performance 的响应:绩效指标 + 权益/水下回撤曲线点。
type PerformanceResponse struct {
	Venue   string                     `json:"venue"`
	Metrics metrics.PerformanceMetrics `json:"metrics"`
	Equity  []metrics.EquityPoint      `json:"equity"`
}

// settlementDayTimestamp 取结算记录用于按日归组的时刻:优先 FundingSettledAt(资金费到账时刻),
// 回退 CloseExecutedAt、再回退 RecordedAt(与 seedDailyRealizedPnL 的兜底口径一致)。
func settlementDayTimestamp(rec SettlementCSVRecord) (time.Time, bool) {
	for _, raw := range []string{rec.FundingSettledAt, rec.CloseExecutedAt, rec.RecordedAt} {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// venueDailyPnL 把一所的结算记录转成 (UTC 日, 最终净利) 条目:只保留 FundingIncomePending=false 的最终净利
// (§9.5 铁律,避免序列抖动/双计),无可解析时刻的记录跳过。
func venueDailyPnL(records []SettlementCSVRecord) []metrics.DailyPnL {
	var out []metrics.DailyPnL
	for _, rec := range records {
		if rec.FundingIncomePending {
			continue
		}
		ts, ok := settlementDayTimestamp(rec)
		if !ok {
			continue
		}
		out = append(out, metrics.DailyPnL{Day: metrics.UTCDay(ts), PnL: parseFloat(rec.NetPnL)})
	}
	return out
}

// portfolioReturns 把一组所的结算聚合为「按 UTC 日重采样的日收益序列 r_t + 对齐日序 + 总分配本金」
// (单所=该所;venue=all=组合:Σpnl_t / Σ分配本金,§9.3 步骤 4)。capital≤0 → returns 为 nil(诚实:分母缺失)。
func portfolioReturns(targets []*venueRuntime) (returns []float64, days []string, capital float64, err error) {
	var entries []metrics.DailyPnL
	for _, rt := range targets {
		if rt == nil {
			continue
		}
		capital += rt.allocatedCapitalUSDT
		if rt.store == nil {
			continue
		}
		records, e := rt.store.ListSettlementRecords(0) // 0 = 全部
		if e != nil {
			return nil, nil, 0, e
		}
		entries = append(entries, venueDailyPnL(records)...)
	}
	series := metrics.BuildDailySeries(entries)
	returns = metrics.ReturnsFromDaily(series, capital)
	days = make([]string, len(series))
	for i, d := range series {
		days[i] = d.Day
	}
	return returns, days, capital, nil
}

// computePerformanceForTargets 聚合一组所的结算为组合绩效。benchmark 为与日序同长的基准日收益(可 nil → 跳过
// Alpha/Beta,置 BenchmarkMissing)。
func computePerformanceForTargets(targets []*venueRuntime, benchmark []float64) (metrics.PerformanceMetrics, []metrics.EquityPoint, error) {
	returns, days, capital, err := portfolioReturns(targets)
	if err != nil {
		return metrics.PerformanceMetrics{}, nil, err
	}
	m := metrics.ComputePerformance(metrics.PerformanceInputs{
		Returns:          returns,
		Benchmark:        benchmark,
		AllocatedCapital: capital,
	})
	equity := metrics.EquityCurve(days, returns, capital)
	return m, equity, nil
}

// VenuesResponse 列出已注册的所与缺省所,供面板渲染 venue 选择器/Tab。
type VenuesResponse struct {
	Venues      []string                          `json:"venues"`
	Primary     string                            `json:"primary"`
	ByVenue     map[string]VenueFinancialSnapshot `json:"by_venue,omitempty"`
	Total       VenuesFinancialTotal              `json:"total,omitempty"`
	DayStartCST string                            `json:"day_start_cst,omitempty"`
}

// VenueFinancialSnapshot 单所资金快照(实时)与今日收益(北京时间自然日,跨所结算按腿归因)。
type VenueFinancialSnapshot struct {
	CapitalUSDT       float64 `json:"capital_usdt"`
	EquityUSDT        float64 `json:"equity_usdt"`
	UnrealizedPnLUSDT float64 `json:"unrealized_pnl_usdt"`
	TodayEarningUSDT  float64 `json:"today_earning_usdt"`
	TodayReturnRate   float64 `json:"today_return_rate"`
	CapitalError      string  `json:"capital_error,omitempty"`
}

// VenuesFinancialTotal 全部注册所的资金与今日收益合计。
type VenuesFinancialTotal struct {
	CapitalUSDT       float64 `json:"capital_usdt"`
	EquityUSDT        float64 `json:"equity_usdt"`
	UnrealizedPnLUSDT float64 `json:"unrealized_pnl_usdt"`
	TodayEarningUSDT  float64 `json:"today_earning_usdt"`
	TodayReturnRate   float64 `json:"today_return_rate"`
}

// todayReturnRate 今日收益率 = 今日真实资金变化 / 日初基准账户总值;基准≤0 时返回 0 避免除零。
func todayReturnRate(earningUSDT, baselineUSDT float64) float64 {
	if baselineUSDT <= 0 {
		return 0
	}
	return earningUSDT / baselineUSDT
}

// buildVenuesFinancialSnapshot 聚合各所可调 USDT 余额(实时快照)、钱包总额(本金,不含浮盈亏)与
// 今日真实账户总值变化(equity_snapshots 日界 delta,非 settlements 已实现净利)。
func (s *DashboardServer) buildVenuesFinancialSnapshot(now time.Time) (map[string]VenueFinancialSnapshot, VenuesFinancialTotal, time.Time) {
	dayStart := beijingCSTDayStart(now)
	byVenue := make(map[string]VenueFinancialSnapshot)
	total := VenuesFinancialTotal{}
	ctx := context.Background()

	var venueEarnings map[string]float64
	var venueBaselines map[string]float64
	if s != nil && s.crossVenueExecutor != nil {
		deltas, _, baselines, snapDayStart, _ := s.crossVenueExecutor.TodayCSTEquityDeltas(now)
		if !snapDayStart.IsZero() {
			dayStart = snapDayStart
		}
		venueEarnings = deltas
		venueBaselines = baselines
		for _, delta := range deltas {
			total.TodayEarningUSDT = money.SumUSDT(total.TodayEarningUSDT, delta)
		}
	}

	if s != nil && s.orchestrator != nil {
		for _, rt := range s.orchestrator.All() {
			if rt == nil || rt.name == "" {
				continue
			}
			entry := VenueFinancialSnapshot{}
			if rt.controller != nil {
				if snap, err := rt.controller.CurrentCapitalSnapshot(); err != nil {
					entry.CapitalError = err.Error()
				} else {
					entry.CapitalUSDT = snap.TotalMovableUSDT
					total.CapitalUSDT = money.SumUSDT(total.CapitalUSDT, entry.CapitalUSDT)
				}
			} else {
				entry.CapitalError = fmt.Sprintf("venue %q 控制器未就绪", rt.name)
			}
			if snap, err := s.fetchVenueEquitySnapshot(ctx, rt); err != nil {
				if entry.CapitalError != "" {
					entry.CapitalError += "; " + err.Error()
				} else {
					entry.CapitalError = err.Error()
				}
			} else {
				entry.EquityUSDT = snap.WalletUSDT
				entry.UnrealizedPnLUSDT = snap.UnrealizedPnLUSDT
				total.EquityUSDT = money.SumUSDT(total.EquityUSDT, snap.WalletUSDT)
				total.UnrealizedPnLUSDT = money.SumUSDT(total.UnrealizedPnLUSDT, snap.UnrealizedPnLUSDT)
			}
			if venueEarnings != nil {
				entry.TodayEarningUSDT = venueEarnings[rt.name]
			}
			baseline := entry.EquityUSDT
			if venueBaselines != nil {
				if b, ok := venueBaselines[rt.name]; ok && b > 0 {
					baseline = b
				}
			}
			entry.TodayReturnRate = todayReturnRate(entry.TodayEarningUSDT, baseline)
			byVenue[rt.name] = entry
		}
	}
	totalBaseline := total.EquityUSDT - total.TodayEarningUSDT
	if totalBaseline <= 0 && venueBaselines != nil {
		for _, b := range venueBaselines {
			totalBaseline = money.SumUSDT(totalBaseline, b)
		}
	}
	total.TodayReturnRate = todayReturnRate(total.TodayEarningUSDT, totalBaseline)
	return byVenue, total, dayStart
}

// handleVenuesAPI 返回编排器已注册的 venue 列表(注册序)与缺省所,供前端构建 venue 选择器。
func (s *DashboardServer) handleVenuesAPI(w http.ResponseWriter, _ *http.Request) {
	resp := VenuesResponse{}
	if s.orchestrator != nil {
		for _, rt := range s.orchestrator.All() {
			if rt != nil {
				resp.Venues = append(resp.Venues, rt.name)
			}
		}
		if primary := s.orchestrator.Primary(); primary != nil {
			resp.Primary = primary.name
		}
	}
	now := time.Now().UTC()
	byVenue, total, dayStart := s.buildVenuesFinancialSnapshot(now)
	if len(byVenue) > 0 {
		resp.ByVenue = byVenue
		resp.Total = total
		resp.DayStartCST = dayStart.Format(time.RFC3339)
	}
	writeJSON(w, resp)
}

func (s *DashboardServer) handlePerformanceAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	venueLabel := venueParam(r)
	if venueLabel == "" {
		if primary := s.orchestrator.Primary(); primary != nil {
			venueLabel = primary.name
		}
	}

	useLegacy := strings.TrimSpace(r.URL.Query().Get("legacy")) == "1"
	var returns []float64
	var days []string
	var capital float64
	if useLegacy {
		returns, days, capital, err = portfolioReturns(targets)
	} else {
		returns, days, capital, err = mergedStrategyPortfolioReturns(s, venueLabel)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	benchmark := s.fetchBenchmarkReturns(r.Context(), days)
	m := metrics.ComputePerformance(metrics.PerformanceInputs{
		Returns:          returns,
		Benchmark:        benchmark,
		AllocatedCapital: capital,
	})
	equity := metrics.EquityCurve(days, returns, capital)
	writeJSON(w, PerformanceResponse{Venue: venueLabel, Metrics: m, Equity: equity})
}
