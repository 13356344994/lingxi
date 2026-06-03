package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed webpanel/*
var webPanelFS embed.FS

// DashboardServer 提供记录查看和下载面板。
type DashboardServer struct {
	addr         string
	store        *ReportStore
	controller   *RuntimeController
	auditStore   *DashboardAuditStore
	controlToken string
	logger       *log.Logger
	indexTmpl    *template.Template
}

// DashboardSummaryResponse 表示面板一次刷新所需的聚合数据。
type DashboardSummaryResponse struct {
	RuntimeStatus         RuntimeTaskStatus         `json:"runtime_status"`
	CapitalSnapshot       RuntimeCapitalSnapshot    `json:"capital_snapshot"`
	CapitalSnapshotError  string                    `json:"capital_snapshot_error"`
	RuntimeLogs           []RuntimeLogRecord        `json:"runtime_logs"`
	RuntimeConfig         RuntimeRiskConfig         `json:"runtime_config"`
	UnifiedEvents         []UnifiedDashboardEvent   `json:"unified_events"`
	ExecutionIncidents    []ExecutionIncidentRecord `json:"execution_incidents"`
	DomainEvents          []DomainEvent             `json:"domain_events"`
	Settlements           []SettlementCSVRecord     `json:"settlements"`
	Monitors              []MonitorCSVRecord        `json:"monitors"`
	Backtests             []BacktestCSVRecord       `json:"backtests"`
	ResidualRisks         []ResidualRiskCSVRecord   `json:"residual_risks"`
	ResidualSeverityStats []ResidualSeverityStats   `json:"residual_severity_stats"`
	ResidualScenarioStats []ResidualScenarioStats   `json:"residual_scenario_stats"`
}

// NewDashboardServer 创建一个只读 Web 面板服务。
func NewDashboardServer(addr string, store *ReportStore, controller *RuntimeController, logger *log.Logger) (*DashboardServer, error) {
	indexTmpl, err := template.ParseFS(webPanelFS, "webpanel/index.html")
	if err != nil {
		return nil, fmt.Errorf("解析 Web 面板模板失败: %w", err)
	}
	auditStore, err := NewDashboardAuditStore(filepath.Join(store.RecordDir(), dashboardAuditLogJSONLName))
	if err != nil {
		return nil, err
	}
	return &DashboardServer{
		addr:         addr,
		store:        store,
		controller:   controller,
		auditStore:   auditStore,
		controlToken: randomDashboardToken(),
		logger:       logger,
		indexTmpl:    indexTmpl,
	}, nil
}

// Start 启动 Web 面板，并在 ctx 取消时优雅关闭。
func (s *DashboardServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/dashboard/summary", s.authorizeReadAPI(s.handleDashboardSummaryAPI))
	mux.HandleFunc("/api/settlements", s.authorizeReadAPI(s.handleSettlementAPI))
	mux.HandleFunc("/api/monitor-records", s.authorizeReadAPI(s.handleMonitorAPI))
	mux.HandleFunc("/api/backtest-records", s.authorizeReadAPI(s.handleBacktestAPI))
	mux.HandleFunc("/api/residual-risk-records", s.authorizeReadAPI(s.handleResidualRiskAPI))
	mux.HandleFunc("/api/residual-severity-stats", s.authorizeReadAPI(s.handleResidualSeverityStatsAPI))
	mux.HandleFunc("/api/residual-scenario-stats", s.authorizeReadAPI(s.handleResidualScenarioStatsAPI))
	mux.HandleFunc("/api/runtime/status", s.authorizeReadAPI(s.handleRuntimeStatusAPI))
	mux.HandleFunc("/api/runtime/logs", s.authorizeReadAPI(s.handleRuntimeLogsAPI))
	mux.HandleFunc("/api/unified-events", s.authorizeReadAPI(s.handleUnifiedEventsAPI))
	mux.HandleFunc("/api/execution-incidents", s.authorizeReadAPI(s.handleExecutionIncidentsAPI))
	mux.HandleFunc("/api/domain-events", s.authorizeReadAPI(s.handleDomainEventsAPI))
	mux.HandleFunc("/api/runtime/config", s.authorizeReadAPI(s.handleRuntimeConfigAPI))
	mux.HandleFunc("/api/runtime/clear-logs", s.handleRuntimeClearLogsAPI)
	mux.HandleFunc("/api/runtime/clear-data", s.handleRuntimeClearDataAPI)
	mux.HandleFunc("/api/runtime/clear-cache", s.handleRuntimeClearCacheAPI)
	mux.HandleFunc("/api/runtime/clear-all", s.handleRuntimeClearAllAPI)
	mux.HandleFunc("/api/runtime/capital", s.handleRuntimeCapitalAPI)
	mux.HandleFunc("/api/runtime/start", s.handleRuntimeStartAPI)
	mux.HandleFunc("/api/runtime/stop", s.handleRuntimeStopAPI)
	mux.Handle("/records/", s.authorizeRead(http.StripPrefix("/records/", http.FileServer(http.Dir(s.store.RecordDir())))))

	server := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	s.logger.Printf("Web 面板已启动: %s", dashboardBaseURL(s.addr))
	// 面板默认仅监听本机，控制类操作需要该令牌；启动时打印到控制台/日志，方便本机填入面板。
	// 注意：请勿把含有该令牌的日志公开分享。
	if s.controlToken != "" {
		s.logger.Printf("Dashboard 控制令牌: %s", s.controlToken)
	} else {
		s.logger.Printf("Dashboard 控制令牌未设置（控制类操作不需要令牌）")
	}
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *DashboardServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := struct {
		SettlementCSV        string
		MonitorCSV           string
		BacktestCSV          string
		ResidualCSV          string
		ExecutionCSV         string
		DomainEventCSV       string
		MonthlySimulationCSV string
		RuntimeEventLog      string
		ControlAuditLog      string
		VersionSummary       string
	}{
		SettlementCSV:        path.Join("/records", settlementCSVName),
		MonitorCSV:           path.Join("/records", monitorCSVName),
		BacktestCSV:          path.Join("/records", backtestCSVName),
		ResidualCSV:          path.Join("/records", residualCSVName),
		ExecutionCSV:         path.Join("/records", executionCSVName),
		DomainEventCSV:       path.Join("/records", domainEventJSONLName),
		MonthlySimulationCSV: path.Join("/records", monthlySimulationLogCSVName),
		RuntimeEventLog:      path.Join("/records", runtimeEventLogJSONLName),
		ControlAuditLog:      path.Join("/records", dashboardAuditLogJSONLName),
		VersionSummary:       buildVersionSummary(),
	}
	if err := s.indexTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *DashboardServer) handleSettlementAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListSettlementRecords(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleDashboardSummaryAPI(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}

	limit := parseLimit(r, 200)
	runtimeLogs, err := s.controller.RecentLogs(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	executionIncidents, err := s.store.ListExecutionIncidents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	domainEvents, err := s.store.ListDomainEvents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settlements, err := s.store.ListSettlementRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	monitors, err := s.store.ListMonitorRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	backtests, err := s.store.ListBacktestRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualRisks, err := s.store.ListResidualRiskRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualSeverityStats, err := s.store.ListResidualSeverityStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualScenarioStats, err := s.store.ListResidualScenarioStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	capitalSnapshot := RuntimeCapitalSnapshot{}
	capitalSnapshotError := ""
	if snapshot, snapshotErr := s.controller.CurrentCapitalSnapshot(); snapshotErr != nil {
		capitalSnapshotError = snapshotErr.Error()
	} else {
		capitalSnapshot = snapshot
	}

	writeJSON(w, DashboardSummaryResponse{
		RuntimeStatus:         s.controller.Status(),
		CapitalSnapshot:       capitalSnapshot,
		CapitalSnapshotError:  capitalSnapshotError,
		RuntimeLogs:           runtimeLogs,
		RuntimeConfig:         s.controller.CurrentRiskConfig(),
		UnifiedEvents:         buildUnifiedDashboardEvents(domainEvents, executionIncidents, limit),
		ExecutionIncidents:    executionIncidents,
		DomainEvents:          domainEvents,
		Settlements:           settlements,
		Monitors:              monitors,
		Backtests:             backtests,
		ResidualRisks:         residualRisks,
		ResidualSeverityStats: residualSeverityStats,
		ResidualScenarioStats: residualScenarioStats,
	})
}

func (s *DashboardServer) handleUnifiedEventsAPI(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 200)
	executionIncidents, err := s.store.ListExecutionIncidents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	domainEvents, err := s.store.ListDomainEvents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, buildUnifiedDashboardEvents(domainEvents, executionIncidents, limit))
}

func (s *DashboardServer) handleMonitorAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListMonitorRecords(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleBacktestAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListBacktestRecords(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleResidualRiskAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListResidualRiskRecords(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleResidualSeverityStatsAPI(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.ListResidualSeverityStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *DashboardServer) handleResidualScenarioStatsAPI(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.ListResidualScenarioStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *DashboardServer) handleRuntimeStatusAPI(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.controller.Status())
}

func (s *DashboardServer) handleRuntimeLogsAPI(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	logs, err := s.controller.RecentLogs(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, logs)
}

func (s *DashboardServer) handleExecutionIncidentsAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListExecutionIncidents(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleDomainEventsAPI(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListDomainEvents(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleRuntimeConfigAPI(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.controller.CurrentRiskConfig())
	case http.MethodPost:
		if !s.authorizeRuntimeControl(w, r, "config_update") {
			return
		}
		var req RuntimeRiskConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.auditControlAction(r, "config_update", false, fmt.Sprintf("解析风控参数失败: %v", err))
			http.Error(w, fmt.Sprintf("解析风控参数失败: %v", err), http.StatusBadRequest)
			return
		}
		updated, err := s.controller.UpdateRiskConfig(req)
		if err != nil {
			s.auditControlAction(r, "config_update", false, err.Error())
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.auditControlAction(r, "config_update", true, "风控参数已更新")
		writeJSON(w, map[string]any{
			"message": "风控参数已更新，将在下一次启动任务时生效",
			"config":  updated,
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *DashboardServer) handleRuntimeClearLogsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "clear_logs") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := s.controller.ClearRuntimeLogs(); err != nil {
		s.auditControlAction(r, "clear_logs", false, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "clear_logs", true, "后台运行日志已清空")
	writeJSON(w, map[string]string{"message": "后台运行日志已清空"})
}

func (s *DashboardServer) handleRuntimeClearDataAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "clear_data") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := s.controller.ClearDashboardData(); err != nil {
		s.auditControlAction(r, "clear_data", false, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "clear_data", true, "页面 JSON 数据已清空")
	writeJSON(w, map[string]string{"message": "页面 JSON 数据已清空"})
}

func (s *DashboardServer) handleRuntimeClearCacheAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "clear_cache") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := s.controller.ClearHistoricalCache(); err != nil {
		s.auditControlAction(r, "clear_cache", false, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.auditControlAction(r, "clear_cache", true, "历史缓存已清空")
	writeJSON(w, map[string]string{"message": "历史缓存已清空"})
}

func (s *DashboardServer) handleRuntimeClearAllAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "clear_all") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := s.controller.ClearAllRecords(); err != nil {
		s.auditControlAction(r, "clear_all", false, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.auditControlAction(r, "clear_all", true, "所有日志与记录已清空")
	writeJSON(w, map[string]string{"message": "所有日志与记录已清空"})
}

func (s *DashboardServer) handleRuntimeCapitalAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "capital_adjust") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	var req RuntimeCapitalAdjustmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "capital_adjust", false, fmt.Sprintf("解析仓位调整参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析仓位调整参数失败: %v", err), http.StatusBadRequest)
		return
	}
	result, err := s.controller.AdjustTradingCapital(req)
	if err != nil {
		s.auditControlAction(r, "capital_adjust", false, err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditControlAction(r, "capital_adjust", true, result.Message)
	writeJSON(w, result)
}

func (s *DashboardServer) handleRuntimeStartAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "start") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}

	var req RuntimeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "start", false, fmt.Sprintf("解析启动参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析启动参数失败: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.controller.Start(req); err != nil {
		s.auditControlAction(r, "start", false, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.auditControlAction(r, "start", true, fmt.Sprintf("后台任务已启动 mode=%s lookback_days=%d", strings.TrimSpace(req.Mode), req.LookbackDays))
	writeJSON(w, map[string]string{"message": "后台任务已启动"})
}

func (s *DashboardServer) handleRuntimeStopAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "stop") {
		return
	}
	if s.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := s.controller.Stop(); err != nil {
		s.auditControlAction(r, "stop", false, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.auditControlAction(r, "stop", true, "已发送停止信号")
	writeJSON(w, map[string]string{"message": "已发送停止信号"})
}

func (s *DashboardServer) authorizeRuntimeControl(w http.ResponseWriter, r *http.Request, action string) bool {
	if !isLoopbackRequest(r) {
		s.auditControlAction(r, action, false, "仅允许本机请求控制后台任务")
		http.Error(w, "仅允许本机请求控制后台任务", http.StatusForbidden)
		return false
	}
	if s.controlToken == "" {
		return true
	}
	if r.Header.Get("X-Dashboard-Token") != s.controlToken {
		s.auditControlAction(r, action, false, "后台控制令牌无效")
		http.Error(w, "后台控制令牌无效", http.StatusForbidden)
		return false
	}
	return true
}

func (s *DashboardServer) authorizeRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			if s.controlToken != "" && r.Header.Get("X-Dashboard-Token") != s.controlToken {
				http.Error(w, "仅允许本机或持有合法令牌访问记录数据", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authorizeReadAPI 对单个 API 接口进行包装鉴权，逻辑同 authorizeRead
func (s *DashboardServer) authorizeReadAPI(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			if s.controlToken != "" && r.Header.Get("X-Dashboard-Token") != s.controlToken {
				http.Error(w, "仅允许本机或持有合法令牌访问 API", http.StatusForbidden)
				return
			}
		}
		handler(w, r)
	}
}

func (s *DashboardServer) auditControlAction(r *http.Request, action string, success bool, detail string) {
	if s == nil || s.auditStore == nil {
		return
	}
	status := RuntimeTaskStatus{}
	metadata := TraceRecordMetadata{}
	if s.controller != nil {
		status = s.controller.Status()
		metadata = s.controller.currentRecordMetadata()
	}
	record := newDashboardAuditRecord(
		action,
		success,
		detail,
		"dashboard",
		strings.TrimSpace(r.RemoteAddr),
		hashDashboardToken(r.Header.Get("X-Dashboard-Token")),
		status.RunID,
		status.CurrentCycleID,
		metadata,
	)
	if err := s.auditStore.Append(record); err != nil && s.logger != nil {
		s.logger.Printf("写入 dashboard 审计日志失败: %v", err)
	}
}

func hashDashboardToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomDashboardToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("dashboard-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func dashboardBaseURL(addr string) string {
	if addr == "" {
		return "http://127.0.0.1:8080/"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		if strings.HasSuffix(addr, "/") {
			return addr
		}
		return addr + "/"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + "/"
	}
	return "http://" + addr + "/"
}

func parseLimit(r *http.Request, fallback int) int {
	limitText := r.URL.Query().Get("limit")
	if limitText == "" {
		return fallback
	}
	limit, err := strconv.Atoi(limitText)
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}
