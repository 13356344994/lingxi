package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// strategyControlVenues 策略总控涉及的交易所(每所独立调到同一目标总额,非分摊)。
var strategyControlVenues = []string{binance.VenueName, bybit.VenueName, okx.VenueName, gate.VenueName}

// legacyBinanceStrategyAutoStart 控制策略总控 executeStrategyStart 是否自动拉起币安遗留 FundingRateSniper。
// 币安已接入跨所 perp-perp 对冲,默认 false 避免同所双引擎互踩;恢复单所自动交易改为 true 即可。
const legacyBinanceStrategyAutoStart = false

// singleVenueStrategyAutoStart 控制策略总控是否自动拉起 Bybit/OKX/Gate 单所引擎。
// 2026-06-15 起默认 false:生产只跑跨所 perp-perp,单所引擎已退役;恢复单所自动启改为 true。
const singleVenueStrategyAutoStart = false

// StrategyCapitalRequest 策略级四所并行调仓请求。
type StrategyCapitalRequest struct {
	TargetUSDT float64 `json:"target_usdt"`
	SecretPath string  `json:"secret_path"`
}

// StrategyCapitalSnapshotSummary 调仓前后摘要(面板展示用)。
type StrategyCapitalSnapshotSummary struct {
	TradingUSDT      float64 `json:"trading_usdt"`
	ReserveUSDT      float64 `json:"reserve_usdt"`
	TotalMovableUSDT float64 `json:"total_movable_usdt"`
}

// StrategyVenueCapitalResult 单所调仓结果(成功/失败互不影响)。
type StrategyVenueCapitalResult struct {
	Venue   string                          `json:"venue"`
	Success bool                            `json:"success"`
	Error   string                          `json:"error,omitempty"`
	Message string                          `json:"message,omitempty"`
	Before  *StrategyCapitalSnapshotSummary `json:"before,omitempty"`
	After   *StrategyCapitalSnapshotSummary `json:"after,omitempty"`
}

// StrategyCapitalResponse 策略调仓聚合响应。
type StrategyCapitalResponse struct {
	TargetUSDT float64                      `json:"target_usdt"`
	Results    []StrategyVenueCapitalResult `json:"results"`
}

// StrategyStartRequest 策略启动请求(默认仅开跨所运行时开仓闸;单所引擎默认不自动启,见 singleVenueStrategyAutoStart/legacyBinanceStrategyAutoStart)。
type StrategyStartRequest struct {
	SecretPath   string `json:"secret_path"`
	LookbackDays int    `json:"lookback_days"`
}

// StrategyStepResult 策略编排单步结果。
type StrategyStepResult struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Skipped bool   `json:"skipped,omitempty"`
	Message string `json:"message"`
}

// StrategyStartResponse 策略启动聚合响应。
type StrategyStartResponse struct {
	Steps []StrategyStepResult `json:"steps"`
}

// StrategyStopResponse 策略停止聚合响应。
type StrategyStopResponse struct {
	Steps []StrategyStepResult `json:"steps"`
}

// StrategyFlattenResult 单策略全平结果。
type StrategyFlattenResult struct {
	Strategy string   `json:"strategy"`
	Closed   int      `json:"closed"`
	Errors   []string `json:"errors,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// StrategyFlattenAllResponse 五策略一键全平聚合响应。
type StrategyFlattenAllResponse struct {
	Results     []StrategyFlattenResult `json:"results"`
	TotalClosed int                     `json:"total_closed"`
}

// StrategyBybitStatus Bybit 执行引擎运行态。
type StrategyBybitStatus struct {
	Running bool   `json:"running"`
	Mode    string `json:"mode,omitempty"`
	Status  string `json:"status,omitempty"`
}

// StrategyCrossVenueStatus 跨所执行引擎聚合态。
type StrategyCrossVenueStatus struct {
	Enabled         bool   `json:"enabled"`
	Live            bool   `json:"live"`
	Mode            string `json:"mode"`
	OpeningsEnabled bool   `json:"openings_enabled"`
	HealthHaltVenue string `json:"health_halt_venue"`
	ActivePositions int    `json:"active_positions"`
}

// StrategyVenueCapitalCache 各所可调余额缓存(来自 CurrentCapitalSnapshot)。
type StrategyVenueCapitalCache struct {
	Venue            string  `json:"venue"`
	TradingUSDT      float64 `json:"trading_usdt"`
	ReserveUSDT      float64 `json:"reserve_usdt"`
	TotalMovableUSDT float64 `json:"total_movable_usdt"`
	Error            string  `json:"error,omitempty"`
}

// StrategyStatusResponse 策略总控状态聚合。
type StrategyStatusResponse struct {
	Bybit         StrategyBybitStatus         `json:"bybit"`
	CrossVenue    StrategyCrossVenueStatus    `json:"cross_venue"`
	ControlPlane  ControlPlaneStatusView      `json:"control_plane"`
	VenueCapitals []StrategyVenueCapitalCache `json:"venue_capitals"`
}

func strategyCapitalBreakdown(s RuntimeCapitalSnapshot) (trading, reserve float64) {
	trading = s.SpotFreeUSDT + s.FuturesAvailableUSDT + s.CrossMarginFreeUSDT
	reserve = s.FundingFreeUSDT
	if trading == 0 {
		if reserve > 0 && s.TotalMovableUSDT >= reserve {
			trading = s.TotalMovableUSDT - reserve
		} else {
			trading = s.TotalMovableUSDT
			reserve = 0
		}
	}
	return trading, reserve
}

func strategyCapitalSummary(s RuntimeCapitalSnapshot) StrategyCapitalSnapshotSummary {
	trading, reserve := strategyCapitalBreakdown(s)
	return StrategyCapitalSnapshotSummary{
		TradingUSDT:      trading,
		ReserveUSDT:      reserve,
		TotalMovableUSDT: s.TotalMovableUSDT,
	}
}

func (s *DashboardServer) handleStrategyStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.buildStrategyStatus())
}

func (s *DashboardServer) handleStrategyCapitalAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "strategy_capital") {
		return
	}
	var req StrategyCapitalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "strategy_capital", false, fmt.Sprintf("解析策略调仓参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析策略调仓参数失败: %v", err), http.StatusBadRequest)
		return
	}
	if req.TargetUSDT <= 0 {
		s.auditControlAction(r, "strategy_capital", false, "目标 USDT 必须大于 0")
		http.Error(w, "目标 USDT 必须大于 0", http.StatusBadRequest)
		return
	}
	resp := s.executeStrategyCapital(req)
	s.auditControlAction(r, "strategy_capital", true, fmt.Sprintf("策略四所调仓 target=%.2f", req.TargetUSDT))
	writeJSON(w, resp)
}

func (s *DashboardServer) handleStrategyStartAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "strategy_start") {
		return
	}
	var req StrategyStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
		s.auditControlAction(r, "strategy_start", false, fmt.Sprintf("解析策略启动参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析策略启动参数失败: %v", err), http.StatusBadRequest)
		return
	}
	resp := s.executeStrategyStart(req)
	allOK := true
	for _, step := range resp.Steps {
		if !step.Success && !step.Skipped {
			allOK = false
			break
		}
	}
	if allOK {
		s.auditControlAction(r, "strategy_start", true, "策略已启动")
		writeJSON(w, resp)
		return
	}
	s.auditControlAction(r, "strategy_start", false, "策略启动部分步骤失败")
	writeJSON(w, resp)
}

func (s *DashboardServer) handleStrategyStopAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "strategy_stop") {
		return
	}
	resp := s.executeStrategyStop()
	allOK := true
	for _, step := range resp.Steps {
		if !step.Success && !step.Skipped {
			allOK = false
			break
		}
	}
	if allOK {
		s.auditControlAction(r, "strategy_stop", true, "策略已停止")
		writeJSON(w, resp)
		return
	}
	s.auditControlAction(r, "strategy_stop", false, "策略停止部分步骤失败")
	writeJSON(w, resp)
}

// StrategyFlattenAllRequest 一键全平仓请求;confirm 须为 FLATTEN 才执行。
type StrategyFlattenAllRequest struct {
	Confirm string `json:"confirm"`
}

func (s *DashboardServer) handleStrategyFlattenAllAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "flatten_all") {
		return
	}
	var req StrategyFlattenAllRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		const msg = "flatten_all 须显式确认:请带 confirm=FLATTEN"
		s.auditControlAction(r, "flatten_all", false, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Confirm) != "FLATTEN" {
		const msg = "flatten_all 为毁灭性操作,须显式确认:请带 confirm=FLATTEN"
		s.auditControlAction(r, "flatten_all", false, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	resp := s.executeStrategyFlattenAll(r.Context())
	allOK := true
	for _, result := range resp.Results {
		if len(result.Errors) > 0 {
			allOK = false
			break
		}
	}
	if allOK {
		s.auditControlAction(r, "flatten_all", true, fmt.Sprintf("全平完成 closed=%d", resp.TotalClosed))
	} else {
		s.auditControlAction(r, "flatten_all", false, fmt.Sprintf("全平部分失败 closed=%d", resp.TotalClosed))
	}
	writeJSON(w, resp)
}

func (s *DashboardServer) executeStrategyFlattenAll(ctx context.Context) StrategyFlattenAllResponse {
	return flattenStrategyPositionsReport(ctx, strategyFlattenDeps{
		CrossVenue: s.crossVenueExecutor,
		SVSP:       s.svspExec,
		CVSP:       s.cvspExec,
		SVPS:       s.svpsExec,
		CVPS:       s.cvpsExec,
	}, true)
}

func (s *DashboardServer) executeStrategyCapital(req StrategyCapitalRequest) StrategyCapitalResponse {
	secretPath := strings.TrimSpace(req.SecretPath)
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	results := make([]StrategyVenueCapitalResult, len(strategyControlVenues))
	var wg sync.WaitGroup
	for i, venueName := range strategyControlVenues {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			results[idx] = s.adjustStrategyVenueCapital(name, secretPath, req.TargetUSDT)
		}(i, venueName)
	}
	wg.Wait()
	return StrategyCapitalResponse{TargetUSDT: req.TargetUSDT, Results: results}
}

func (s *DashboardServer) adjustStrategyVenueCapital(venueName, secretPath string, targetUSDT float64) StrategyVenueCapitalResult {
	out := StrategyVenueCapitalResult{Venue: venueName}
	if s == nil || s.orchestrator == nil {
		out.Error = "编排器未初始化"
		return out
	}
	rt, ok := s.orchestrator.Get(venueName)
	if !ok || rt == nil || rt.controller == nil {
		out.Error = fmt.Sprintf("venue %q 未注册或未初始化", venueName)
		return out
	}
	result, err := rt.controller.AdjustTradingCapital(RuntimeCapitalAdjustmentRequest{
		TargetTradingUSDT: targetUSDT,
		SecretPath:        secretPath,
		Mode:              string(RuntimeModeLive),
	})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	before := strategyCapitalSummary(result.Before)
	after := strategyCapitalSummary(result.After)
	out.Success = true
	out.Message = result.Message
	out.Before = &before
	out.After = &after
	return out
}

func (s *DashboardServer) executeStrategyStart(req StrategyStartRequest) StrategyStartResponse {
	var steps []StrategyStepResult
	secretPath := strings.TrimSpace(req.SecretPath)
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	lookbackDays := req.LookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 30
	}

	// ⓪ 解除控制面 global_halt(与既有开仓开关/单所启停并行)。
	haltStep := StrategyStepResult{Step: "control_plane_global_halt_off"}
	if s.controlPlane == nil {
		haltStep.Success = true
		haltStep.Skipped = true
		haltStep.Message = "控制面未注入,跳过"
	} else if err := s.controlPlane.SetGlobalHalt(false); err != nil {
		haltStep.Message = err.Error()
	} else {
		haltStep.Success = true
		haltStep.Message = "控制面 global_halt 已解除"
	}
	steps = append(steps, haltStep)

	// ① 启动 Bybit 执行任务(已运行则跳过);单所退役时默认 skipped。
	bybitStep := StrategyStepResult{Step: "bybit_start"}
	if !singleVenueStrategyAutoStart {
		bybitStep.Success = true
		bybitStep.Skipped = true
		bybitStep.Message = "Bybit 单所引擎默认不自动启动(仅跨所;恢复:singleVenueStrategyAutoStart=true)"
		steps = append(steps, bybitStep)
	} else if s.orchestrator == nil {
		bybitStep.Message = "编排器未初始化"
		steps = append(steps, bybitStep)
	} else if rt, ok := s.orchestrator.Get(bybit.VenueName); !ok || rt == nil || rt.controller == nil {
		bybitStep.Message = "Bybit 控制器未注册"
		steps = append(steps, bybitStep)
	} else if rt.controller.Status().Running {
		bybitStep.Success = true
		bybitStep.Skipped = true
		bybitStep.Message = "Bybit 已在运行,跳过"
		steps = append(steps, bybitStep)
	} else if err := rt.controller.Start(RuntimeStartRequest{
		Mode:         string(RuntimeModeLive),
		SecretPath:   secretPath,
		LookbackDays: lookbackDays,
	}); err != nil {
		bybitStep.Message = err.Error()
		steps = append(steps, bybitStep)
	} else {
		bybitStep.Success = true
		bybitStep.Message = "Bybit 执行任务已启动"
		steps = append(steps, bybitStep)
	}

	// ② 跨所开仓开关 ON。
	openStep := StrategyStepResult{Step: "cross_venue_openings_on"}
	if s.crossVenueExecutor == nil {
		openStep.Success = true
		openStep.Skipped = true
		openStep.Message = "跨所执行引擎未注入,跳过"
	} else if err := s.crossVenueExecutor.SetOpeningsEnabled(true); err != nil {
		openStep.Message = err.Error()
	} else {
		openStep.Success = true
		openStep.Message = "跨所运行时开仓开关已开启"
	}
	steps = append(steps, openStep)

	// ③ OKX/Gate funding 执行器(默认 OFF,日志标明);单所退役时默认 skipped。
	for _, vname := range []string{okx.VenueName, gate.VenueName} {
		step := StrategyStepResult{Step: "funding_start_" + vname}
		if !singleVenueStrategyAutoStart {
			step.Success = true
			step.Skipped = true
			step.Message = fmt.Sprintf("%s 单所 funding 引擎默认不自动启动(仅跨所;恢复:singleVenueStrategyAutoStart=true)", vname)
			steps = append(steps, step)
			continue
		}
		if s.orchestrator == nil {
			step.Message = "编排器未初始化"
			steps = append(steps, step)
			continue
		}
		rt, ok := s.orchestrator.Get(vname)
		if !ok || rt == nil || rt.controller == nil {
			step.Message = fmt.Sprintf("%s 控制器未注册", vname)
			steps = append(steps, step)
			continue
		}
		if rt.controller.Status().Running {
			step.Success = true
			step.Skipped = true
			step.Message = fmt.Sprintf("%s 已在运行,跳过", vname)
			steps = append(steps, step)
			continue
		}
		if err := rt.controller.Start(RuntimeStartRequest{
			Mode:         string(RuntimeModeLive),
			SecretPath:   secretPath,
			LookbackDays: lookbackDays,
		}); err != nil {
			step.Message = err.Error()
		} else {
			step.Success = true
			live := rt.controller.cfg.FundingExecutor.LiveTradingEnabled
			if live {
				step.Message = fmt.Sprintf("%s funding 执行器已启动(实盘)", vname)
			} else {
				step.Message = fmt.Sprintf("%s funding 执行器已启动(OFF,不下单)", vname)
			}
		}
		steps = append(steps, step)
	}

	// ④ 币安遗留 FundingRateSniper:默认跳过(币安仅经跨所参与);legacyBinanceStrategyAutoStart=true 可恢复自动启。
	binanceStep := StrategyStepResult{Step: "binance_start"}
	if !legacyBinanceStrategyAutoStart {
		binanceStep.Success = true
		binanceStep.Skipped = true
		binanceStep.Message = "Binance 遗留引擎默认不自动启动(币安经跨所参与;恢复:legacyBinanceStrategyAutoStart=true)"
		steps = append(steps, binanceStep)
	} else if s.orchestrator == nil {
		binanceStep.Message = "编排器未初始化"
		steps = append(steps, binanceStep)
	} else if rt, ok := s.orchestrator.Get(binance.VenueName); !ok || rt == nil || rt.controller == nil {
		binanceStep.Message = "Binance 控制器未注册"
		steps = append(steps, binanceStep)
	} else if rt.controller.Status().Running {
		binanceStep.Success = true
		binanceStep.Skipped = true
		binanceStep.Message = "Binance 已在运行,跳过"
		steps = append(steps, binanceStep)
	} else if err := rt.controller.Start(RuntimeStartRequest{
		Mode:         string(RuntimeModeLive),
		SecretPath:   secretPath,
		LookbackDays: lookbackDays,
	}); err != nil {
		binanceStep.Message = err.Error()
		steps = append(steps, binanceStep)
	} else {
		binanceStep.Success = true
		binanceStep.Message = "Binance 执行任务已启动"
		steps = append(steps, binanceStep)
	}

	return StrategyStartResponse{Steps: steps}
}

func (s *DashboardServer) executeStrategyStop() StrategyStopResponse {
	var steps []StrategyStepResult

	// ⓪ 控制面 global_halt 总闸(附加层,manage/verified-close 仍按各自语义)。
	haltStep := StrategyStepResult{Step: "control_plane_global_halt_on"}
	if s.controlPlane == nil {
		haltStep.Success = true
		haltStep.Skipped = true
		haltStep.Message = "控制面未注入,跳过"
	} else if err := s.controlPlane.SetGlobalHalt(true); err != nil {
		haltStep.Message = err.Error()
	} else {
		haltStep.Success = true
		haltStep.Message = "控制面 global_halt 已生效(禁止一切新开仓)"
	}
	steps = append(steps, haltStep)

	// ① 停止 Bybit(在场持仓走既有停止语义:停开仓不停管理)。
	bybitStep := StrategyStepResult{Step: "bybit_stop"}
	if s.orchestrator == nil {
		bybitStep.Message = "编排器未初始化"
		steps = append(steps, bybitStep)
	} else if rt, ok := s.orchestrator.Get(bybit.VenueName); !ok || rt == nil || rt.controller == nil {
		bybitStep.Success = true
		bybitStep.Skipped = true
		bybitStep.Message = "Bybit 控制器未注册,跳过"
		steps = append(steps, bybitStep)
	} else if !rt.controller.Status().Running {
		bybitStep.Success = true
		bybitStep.Skipped = true
		bybitStep.Message = "Bybit 未在运行,跳过"
		steps = append(steps, bybitStep)
	} else if err := rt.controller.Stop(); err != nil {
		bybitStep.Message = err.Error()
		steps = append(steps, bybitStep)
	} else {
		bybitStep.Success = true
		bybitStep.Message = "Bybit 已发送停止信号"
		steps = append(steps, bybitStep)
	}

	// ①b 停止 Binance(对称于 Bybit;在场持仓走既有停止语义)。
	binanceStep := StrategyStepResult{Step: "binance_stop"}
	if s.orchestrator == nil {
		binanceStep.Message = "编排器未初始化"
		steps = append(steps, binanceStep)
	} else if rt, ok := s.orchestrator.Get(binance.VenueName); !ok || rt == nil || rt.controller == nil {
		binanceStep.Success = true
		binanceStep.Skipped = true
		binanceStep.Message = "Binance 控制器未注册,跳过"
		steps = append(steps, binanceStep)
	} else if !rt.controller.Status().Running {
		binanceStep.Success = true
		binanceStep.Skipped = true
		binanceStep.Message = "Binance 未在运行,跳过"
		steps = append(steps, binanceStep)
	} else if err := rt.controller.Stop(); err != nil {
		binanceStep.Message = err.Error()
		steps = append(steps, binanceStep)
	} else {
		binanceStep.Success = true
		binanceStep.Message = "Binance 已发送停止信号"
		steps = append(steps, binanceStep)
	}

	// ② 跨所开仓开关 OFF(在场持仓继续管理)。
	openStep := StrategyStepResult{Step: "cross_venue_openings_off"}
	if s.crossVenueExecutor == nil {
		openStep.Success = true
		openStep.Skipped = true
		openStep.Message = "跨所执行引擎未注入,跳过"
	} else if err := s.crossVenueExecutor.SetOpeningsEnabled(false); err != nil {
		openStep.Message = err.Error()
	} else {
		openStep.Success = true
		openStep.Message = "跨所运行时开仓开关已关闭(在场持仓管理照常)"
	}
	steps = append(steps, openStep)

	// ③ 停止所有仍在运行的 per-venue 控制器。
	if s.orchestrator != nil {
		for _, rt := range s.orchestrator.All() {
			if rt == nil || rt.controller == nil {
				continue
			}
			if !rt.controller.Status().Running {
				continue
			}
			stopStep := StrategyStepResult{Step: "stop_" + rt.name}
			if err := rt.controller.Stop(); err != nil {
				stopStep.Message = err.Error()
			} else {
				stopStep.Success = true
				stopStep.Message = fmt.Sprintf("%s 已发送停止信号", rt.name)
			}
			steps = append(steps, stopStep)
		}
	}

	return StrategyStopResponse{Steps: steps}
}

func (s *DashboardServer) buildStrategyStatus() StrategyStatusResponse {
	resp := StrategyStatusResponse{
		VenueCapitals: make([]StrategyVenueCapitalCache, 0, len(strategyControlVenues)),
	}
	if s != nil && s.orchestrator != nil {
		if rt, ok := s.orchestrator.Get(bybit.VenueName); ok && rt != nil && rt.controller != nil {
			st := rt.controller.Status()
			resp.Bybit = StrategyBybitStatus{
				Running: st.Running,
				Mode:    st.Mode,
				Status:  st.Status,
			}
		}
		for _, name := range strategyControlVenues {
			entry := StrategyVenueCapitalCache{Venue: name}
			if rt, ok := s.orchestrator.Get(name); ok && rt != nil && rt.controller != nil {
				if snap, err := rt.controller.CurrentCapitalSnapshot(); err != nil {
					entry.Error = err.Error()
				} else {
					entry.TradingUSDT, entry.ReserveUSDT = strategyCapitalBreakdown(snap)
					entry.TotalMovableUSDT = snap.TotalMovableUSDT
				}
			} else {
				entry.Error = fmt.Sprintf("venue %q 未注册", name)
			}
			resp.VenueCapitals = append(resp.VenueCapitals, entry)
		}
	}
	if s != nil && s.crossVenueExecutor != nil {
		snap := s.crossVenueExecutor.PositionsSnapshot()
		resp.CrossVenue = StrategyCrossVenueStatus{
			Enabled:         snap.Enabled,
			Live:            snap.Live,
			Mode:            snap.Mode,
			OpeningsEnabled: snap.OpeningsEnabled,
			HealthHaltVenue: snap.HealthHaltVenue,
			ActivePositions: snap.ActivePositions,
		}
	} else {
		resp.CrossVenue = StrategyCrossVenueStatus{
			Mode: "未注入",
		}
	}
	resp.ControlPlane = s.buildControlPlaneStatusView()
	return resp
}

func (s *DashboardServer) buildControlPlaneStatusView() ControlPlaneStatusView {
	perpConfigLive := false
	perpOpeningsOn := false
	perpHealthHalt := false
	if s != nil && s.crossVenueExecutor != nil {
		snap := s.crossVenueExecutor.PositionsSnapshot()
		perpConfigLive = snap.Live
		perpOpeningsOn = snap.OpeningsEnabled
		perpHealthHalt = strings.TrimSpace(snap.HealthHaltVenue) != ""
	}
	rotationActive := false
	if s != nil && s.rotationRunner != nil {
		rotationActive = s.rotationRunner.Status().Active
	}
	var cp *ControlPlane
	if s != nil {
		cp = s.controlPlane
	}
	view := cp.BuildStatusView(perpConfigLive, perpOpeningsOn, rotationActive, perpHealthHalt)
	strategyMode := CurrentStrategyMode()
	view.StrategyMode = string(strategyMode)
	view.PerpPerpRunMode = string(PerpPerpRunMode(perpConfigLive, view.PerpPerpLiveEnabled, strategyMode))
	view.RotationRunMode = string(RotationRunMode(perpConfigLive, view.RotationLiveEnabled, strategyMode))
	return view
}
