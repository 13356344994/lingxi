package app

// performance_api.go 绩效指标读 API:数据源 = 各所方向引擎结算(strategy_settlements.go),
// 按北京时间自然日聚合 ÷ 分配本金得日收益序列,喂 internal/metrics 纯函数。

import (
	"context"
	"net/http"
	"time"

	"lingxi/internal/metrics"
	"lingxi/internal/money"
)

// PerformanceResponse 是 /api/performance 的响应:绩效指标 + 权益/水下回撤曲线点。
type PerformanceResponse struct {
	Venue   string                     `json:"venue"`
	Metrics metrics.PerformanceMetrics `json:"metrics"`
	Equity  []metrics.EquityPoint      `json:"equity"`
}

// VenuesResponse 列出已注册的所与缺省所,供面板渲染 venue 选择器/Tab。
type VenuesResponse struct {
	Venues      []string                          `json:"venues"`
	Primary     string                            `json:"primary"`
	ByVenue     map[string]VenueFinancialSnapshot `json:"by_venue,omitempty"`
	Total       VenuesFinancialTotal              `json:"total,omitempty"`
	DayStartCST string                            `json:"day_start_cst,omitempty"`
}

// VenueFinancialSnapshot 单所资金快照(实时)与今日收益(北京时间自然日)。
type VenueFinancialSnapshot struct {
	CapitalUSDT         float64 `json:"capital_usdt"`
	EquityUSDT          float64 `json:"equity_usdt"`
	UnrealizedPnLUSDT   float64 `json:"unrealized_pnl_usdt"`
	TodayEarningUSDT    float64 `json:"today_earning_usdt"`
	TodayReturnRate     float64 `json:"today_return_rate"`
	TodayFundFlowUSDT   float64 `json:"today_fund_flow_usdt"`
	TodayFundFlowKnown  bool    `json:"today_fund_flow_known"`
	TodayTradingPnLUSDT float64 `json:"today_trading_pnl_usdt"`
	CapitalError        string  `json:"capital_error,omitempty"`
}

// VenuesFinancialTotal 全部注册所的资金与今日收益合计。
type VenuesFinancialTotal struct {
	CapitalUSDT         float64 `json:"capital_usdt"`
	EquityUSDT          float64 `json:"equity_usdt"`
	UnrealizedPnLUSDT   float64 `json:"unrealized_pnl_usdt"`
	TodayEarningUSDT    float64 `json:"today_earning_usdt"`
	TodayReturnRate     float64 `json:"today_return_rate"`
	TodayFundFlowUSDT   float64 `json:"today_fund_flow_usdt"`
	TodayFundFlowKnown  bool    `json:"today_fund_flow_known"`
	TodayTradingPnLUSDT float64 `json:"today_trading_pnl_usdt"`
}

// todayReturnRate 今日收益率 = 今日真实资金变化 / 日初基准账户总值;基准≤0 时返回 0 避免除零。
func todayReturnRate(earningUSDT, baselineUSDT float64) float64 {
	if baselineUSDT <= 0 {
		return 0
	}
	return earningUSDT / baselineUSDT
}

// buildVenuesFinancialSnapshot 聚合各所钱包总额与今日真实账户总值变化(equity_snapshots 日界 delta)。
func (s *DashboardServer) buildVenuesFinancialSnapshot(now time.Time) (map[string]VenueFinancialSnapshot, VenuesFinancialTotal, time.Time) {
	dayStart := beijingCSTDayStart(now)
	byVenue := make(map[string]VenueFinancialSnapshot)
	total := VenuesFinancialTotal{}
	ctx := context.Background()

	var venueEarnings map[string]float64
	var venueBaselines map[string]float64
	if s != nil && s.equitySvc != nil {
		deltas, _, baselines, snapDayStart, _ := s.equitySvc.TodayCSTEquityDeltas(now)
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
			if snap, err := s.fetchVenueEquitySnapshot(ctx, rt); err != nil {
				entry.CapitalError = err.Error()
			} else {
				entry.EquityUSDT = snap.WalletUSDT
				entry.UnrealizedPnLUSDT = snap.UnrealizedPnLUSDT
				entry.CapitalUSDT = snap.WalletUSDT
				total.EquityUSDT = money.SumUSDT(total.EquityUSDT, snap.WalletUSDT)
				total.UnrealizedPnLUSDT = money.SumUSDT(total.UnrealizedPnLUSDT, snap.UnrealizedPnLUSDT)
				total.CapitalUSDT = money.SumUSDT(total.CapitalUSDT, snap.WalletUSDT)
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
	applyVenueTradingPnLFromFundFlow(byVenue, &total, s.recordDir)
	return byVenue, total, dayStart
}

// applyVenueTradingPnLFromFundFlow 用账户风控 per-venue 充提明细计算各所/合计纯交易收益。
func applyVenueTradingPnLFromFundFlow(byVenue map[string]VenueFinancialSnapshot, total *VenuesFinancialTotal, recordDir string) {
	if total == nil {
		return
	}
	st, ok := loadAccountRiskEquityState(recordDir)
	perVenueKnown := ok && st.DailyFundFlowSource != "heuristic" && len(st.DailyFundFlowByVenueUSDT) > 0

	var sumPerVenueFundFlow float64
	for name, entry := range byVenue {
		if perVenueKnown {
			ff, has := st.DailyFundFlowByVenueUSDT[name]
			if has {
				entry.TodayFundFlowKnown = true
				entry.TodayFundFlowUSDT = ff
				entry.TodayTradingPnLUSDT = money.SumUSDT(entry.TodayEarningUSDT, -ff)
				sumPerVenueFundFlow = money.SumUSDT(sumPerVenueFundFlow, ff)
			} else {
				entry.TodayTradingPnLUSDT = entry.TodayEarningUSDT
			}
		} else {
			entry.TodayTradingPnLUSDT = entry.TodayEarningUSDT
		}
		byVenue[name] = entry
	}

	if perVenueKnown {
		total.TodayFundFlowKnown = true
		total.TodayFundFlowUSDT = sumPerVenueFundFlow
		total.TodayTradingPnLUSDT = money.SumUSDT(total.TodayEarningUSDT, -sumPerVenueFundFlow)
		return
	}
	if ok && st.DailyFundFlowSource != "heuristic" {
		total.TodayFundFlowKnown = true
		total.TodayFundFlowUSDT = st.DailyFundFlowAdjustUSDT
		total.TodayTradingPnLUSDT = money.SumUSDT(total.TodayEarningUSDT, -st.DailyFundFlowAdjustUSDT)
		return
	}
	total.TodayTradingPnLUSDT = total.TodayEarningUSDT
}

// handleVenuesAPI 返回编排器已注册的 venue 列表与缺省所,供前端构建 venue 选择器。
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
	venueLabel := venueParam(r)
	if venueLabel == "" {
		if primary := s.orchestrator.Primary(); primary != nil {
			venueLabel = primary.name
		}
	}
	returns, days, capital, err := mergedStrategyPortfolioReturns(s, venueLabel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	m := metrics.ComputePerformance(metrics.PerformanceInputs{
		Returns:          returns,
		AllocatedCapital: capital,
	})
	equity := metrics.EquityCurve(days, returns, capital)
	writeJSON(w, PerformanceResponse{Venue: venueLabel, Metrics: m, Equity: equity})
}
