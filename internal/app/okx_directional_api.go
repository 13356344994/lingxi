package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OKXDirectionalAPIResponse GET /api/okx-directional 响应(引擎快照 + 最近结算)。
type OKXDirectionalAPIResponse struct {
	GeneratedAt         time.Time                    `json:"generated_at"`
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
}

func (s *DashboardServer) okxDirectionalStatusOrEmpty() OKXDirectionalAPIResponse {
	if s == nil || s.okxDirectional == nil {
		return OKXDirectionalAPIResponse{GeneratedAt: time.Now().UTC()}
	}
	st := s.okxDirectional.OKXDirectionalStatus()
	resp := OKXDirectionalAPIResponse{
		GeneratedAt:         time.Now().UTC(),
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
	dir := resolveStrategyRecordDir(s)
	path := filepath.Join(okxDirectionalRecordDir(dir), okxDirectionalSettlementsFile)
	recent, err := loadTailJSONLFile[okxDirectionalSettlement](path, time.Time{}, 20)
	if err == nil && len(recent) > 0 {
		sort.Slice(recent, func(i, j int) bool {
			return recent[i].Time.After(recent[j].Time)
		})
		resp.Recent = recent
	}
	return resp
}

func (s *DashboardServer) handleOKXDirectionalAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.okxDirectional == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, s.okxDirectionalStatusOrEmpty())
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
	if s.okxDirectional == nil {
		s.auditControlAction(r, "okx_directional_control", false, "OKX 方向引擎未注入")
		http.Error(w, "OKX 方向引擎未注入(需 okx_directional.enabled=true)", http.StatusBadRequest)
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
	if err := s.okxDirectional.SetOKXDirectionalControl(req.Active, req.TradingCapitalUSDT, req.StopLossPct); err != nil {
		s.auditControlAction(r, "okx_directional_control", false, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "okx_directional_control", true, fmt.Sprintf("active=%v capital=%.2f stop=%.2f", req.Active, req.TradingCapitalUSDT, req.StopLossPct))
	writeJSON(w, s.okxDirectionalStatusOrEmpty())
}
