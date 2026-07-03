package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OKXDirectionalAPIResponse GET /api/okx-directional 响应(引擎快照 + 最近结算)。
type OKXDirectionalAPIResponse struct {
	GeneratedAt         time.Time                    `json:"generated_at"`
	Venue               string                       `json:"venue"`
	Enabled             bool                         `json:"enabled"`
	Live                bool                         `json:"live"`
	Active              bool                         `json:"active"`
	TradingCapitalUSDT  float64                      `json:"trading_capital_usdt"`
	StopLossPct         float64                      `json:"stop_loss_pct"`
	Leverage            int                          `json:"leverage"`
	OpenCount           int                          `json:"open_count"`
	MaxConcurrent       int                          `json:"max_concurrent"`
	Positions           []OKXDirectionalPositionView `json:"positions,omitempty"`
	TotalUnrealizedUSDT float64                      `json:"total_unrealized_usdt"`
	TotalPnLTodayUSDT   float64                      `json:"total_pnl_today_usdt"`
	PnLPctOfCapital     float64                      `json:"pnl_pct_of_capital"`
	DeployedMarginUSDT  float64                      `json:"deployed_margin_usdt"`
	HoldingSymbol       string                       `json:"holding_symbol,omitempty"`
	Side                string                       `json:"side,omitempty"`
	EntryPrice          float64                      `json:"entry_price,omitempty"`
	Qty                 float64                      `json:"qty,omitempty"`
	NotionalUSDT        float64                      `json:"notional_usdt,omitempty"`
	UnrealizedPctApprox float64                      `json:"unrealized_pct_approx,omitempty"`
	OpenedAt            time.Time                    `json:"opened_at,omitempty"`
	DailyRealizedUSDT   float64                      `json:"daily_realized_usdt"`
	DailyLossLimitUSDT  float64                      `json:"daily_loss_limit_usdt"`
	HaltedByDailyLoss   bool                         `json:"halted_by_daily_loss"`
	LastError           string                       `json:"last_error,omitempty"`
	Recent              []okxDirectionalSettlement   `json:"recent,omitempty"`
	StatsToday          *directionalStats            `json:"stats_today,omitempty"`
	StatsAllTime        *directionalStats            `json:"stats_all_time,omitempty"`
	// 分层今日统计(2026-07-03):fast 笔数会淹没 slow,混合行意义有限 → slow/fast 各一行。
	StatsTodaySlow *directionalStats `json:"stats_today_slow,omitempty"`
	StatsTodayFast *directionalStats `json:"stats_today_fast,omitempty"`
	// 今日开仓数(含仍在场的):已结算按 close_time−hold 推回开仓时刻,在场按 OpenedAt。
	OpenedTodaySlow int `json:"opened_today_slow"`
	OpenedTodayFast int `json:"opened_today_fast"`
}

func directionalVenueFromRequest(r *http.Request) string {
	if r == nil {
		return "okx"
	}
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("venue")))
	if v == "" {
		return "okx"
	}
	return v
}

func (s *DashboardServer) okxDirectionalStatusOrEmpty(venue string) OKXDirectionalAPIResponse {
	eng := s.directionalEngine(venue)
	if s == nil || eng == nil {
		return OKXDirectionalAPIResponse{GeneratedAt: time.Now().UTC(), Venue: venue, Enabled: false}
	}
	st := eng.OKXDirectionalStatus()
	resp := OKXDirectionalAPIResponse{
		GeneratedAt:         time.Now().UTC(),
		Venue:               st.Venue,
		Enabled:             st.Enabled,
		Live:                st.Live,
		Active:              st.Active,
		TradingCapitalUSDT:  st.TradingCapitalUSDT,
		StopLossPct:         st.StopLossPct,
		Leverage:            st.Leverage,
		OpenCount:           st.OpenCount,
		MaxConcurrent:       st.MaxConcurrent,
		Positions:           st.Positions,
		TotalUnrealizedUSDT: st.TotalUnrealizedUSDT,
		TotalPnLTodayUSDT:   st.TotalPnLTodayUSDT,
		PnLPctOfCapital:     st.PnLPctOfCapital,
		DeployedMarginUSDT:  st.DeployedMarginUSDT,
		HoldingSymbol:       st.HoldingSymbol,
		Side:                st.Side,
		EntryPrice:          st.EntryPrice,
		Qty:                 st.Qty,
		NotionalUSDT:        st.NotionalUSDT,
		UnrealizedPctApprox: st.UnrealizedPctApprox,
		OpenedAt:            st.OpenedAt,
		DailyRealizedUSDT:   st.DailyRealizedUSDT,
		DailyLossLimitUSDT:  st.DailyLossLimitUSDT,
		HaltedByDailyLoss:   st.HaltedByDailyLoss,
		LastError:           st.LastError,
	}
	recent, err := loadTailJSONLFile[okxDirectionalSettlement](eng.SettlementsPath(), time.Time{}, 20)
	if err == nil && len(recent) > 0 {
		sort.Slice(recent, func(i, j int) bool {
			return recent[i].Time.After(recent[j].Time)
		})
		resp.Recent = recent
	}
	allSettles, allErr := loadTailJSONLFile[okxDirectionalSettlement](eng.SettlementsPath(), time.Time{}, 1000)
	if allErr == nil && allSettles != nil {
		now := time.Now().UTC()
		dayStart := beijingCSTDayStart(now)
		today := computeDirectionalStats(allSettles, dayStart)
		allTime := computeDirectionalStats(allSettles, time.Time{})
		resp.StatsToday = &today
		resp.StatsAllTime = &allTime
		// 分层今日统计:fast(signal_layer=fast_v1)与 slow 分开,fast 高笔数不再淹没 slow。
		var slowSettles, fastSettles []okxDirectionalSettlement
		for _, rec := range allSettles {
			if rec.SignalLayer == fastSignalLayer {
				fastSettles = append(fastSettles, rec)
			} else {
				slowSettles = append(slowSettles, rec)
			}
		}
		slowToday := computeDirectionalStats(slowSettles, dayStart)
		fastToday := computeDirectionalStats(fastSettles, dayStart)
		resp.StatsTodaySlow = &slowToday
		resp.StatsTodayFast = &fastToday
		// 今日开仓数(含在场):已结算按 close_time−hold_seconds 推回开仓时刻。
		for _, rec := range allSettles {
			openAt := rec.Time.Add(-time.Duration(rec.HoldSeconds) * time.Second)
			if rec.HoldSeconds == 0 && rec.HoldMinutes > 0 {
				openAt = rec.Time.Add(-time.Duration(rec.HoldMinutes) * time.Minute)
			}
			if openAt.Before(dayStart) {
				continue
			}
			if rec.SignalLayer == fastSignalLayer {
				resp.OpenedTodayFast++
			} else {
				resp.OpenedTodaySlow++
			}
		}
		// 在场仓:slow 按 Positions 视图,fast 读 fast 持仓表。
		for _, p := range st.Positions {
			if !p.OpenedAt.Before(dayStart) {
				resp.OpenedTodaySlow++
			}
		}
		resp.OpenedTodayFast += eng.fastOpenPositionsOpenedSince(dayStart)
		// 今日已实现/合计改用 settlements 口径(北京日切聚合):重启不清零,且已用上修正后的成交价记账。
		// 内存计数器 dailyRealizedUSDT 仅本进程累计、重启归零,不适合作为面板「今日合计」。
		resp.DailyRealizedUSDT = today.NetUSDT
		resp.TotalPnLTodayUSDT = today.NetUSDT
	}
	return resp
}

func (s *DashboardServer) handleOKXDirectionalAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	venue := directionalVenueFromRequest(r)
	eng := s.directionalEngine(venue)
	if eng == nil {
		writeJSON(w, map[string]any{"enabled": false, "venue": venue})
		return
	}
	writeJSON(w, s.okxDirectionalStatusOrEmpty(venue))
}

// OKXDirectionalControlRequest POST /api/okx-directional/control 入参(页面:交易仓本金 + 止损% + 开始/停止)。
type OKXDirectionalControlRequest struct {
	Active             bool    `json:"active"`
	TradingCapitalUSDT float64 `json:"trading_capital_usdt"`
	StopLossPct        float64 `json:"stop_loss_pct"` // 0.5 或 50 均可(>1 自动当百分数)
	Confirm            string  `json:"confirm"`       // active=true 须 confirm=LIVE
}

// handleOKXDirectionalControlAPI 页面设置交易仓本金/止损% + 开始/停止。仅控制,不直接下单(由引擎主循环执行)。
func (s *DashboardServer) handleOKXDirectionalControlAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "okx_directional_control") {
		return
	}
	venue := directionalVenueFromRequest(r)
	eng := s.directionalEngine(venue)
	if eng == nil {
		s.auditControlAction(r, "okx_directional_control", false, fmt.Sprintf("%s 方向引擎未注入", venue))
		http.Error(w, fmt.Sprintf("%s 方向引擎未注入", venue), http.StatusBadRequest)
		return
	}
	var req OKXDirectionalControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "okx_directional_control", false, fmt.Sprintf("解析失败: %v", err))
		http.Error(w, fmt.Sprintf("解析控制参数失败: %v", err), http.StatusBadRequest)
		return
	}
	// 开始=真金 5X,须显式确认(与轮动启动一致)。停止无需确认。
	if req.Active && strings.TrimSpace(req.Confirm) != "LIVE" {
		const msg = "开始实盘方向交易须显式确认:confirm=LIVE"
		s.auditControlAction(r, "okx_directional_control", false, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if err := eng.SetOKXDirectionalControl(req.Active, req.TradingCapitalUSDT, req.StopLossPct); err != nil {
		s.auditControlAction(r, "okx_directional_control", false, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "okx_directional_control", true, fmt.Sprintf("venue=%s active=%v capital=%.2f stop=%.2f", venue, req.Active, req.TradingCapitalUSDT, req.StopLossPct))
	writeJSON(w, s.okxDirectionalStatusOrEmpty(venue))
}
