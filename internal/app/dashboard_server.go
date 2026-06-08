package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
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
	addr string
	// orchestrator 持有所有 venue 运行时(P3a)。store/controller 是 Primary() 的快捷引用,使现有 handler
	// 保持走缺省所、路由零变化;P3 后续 handler 改按 ?venue= 经 orchestrator 路由。
	orchestrator *Orchestrator
	store        *ReportStore
	controller   *RuntimeController
	auditStore   *DashboardAuditStore
	controlToken string
	logger       *log.Logger
	indexTmpl    *template.Template
	startedAt    time.Time
}

// DashboardSummaryResponse 表示面板一次刷新所需的聚合数据。
type DashboardSummaryResponse struct {
	// Venue 标识本次 summary 代表的所(读时注入,§9.2):summary 是单所视图,缺省/all → 缺省所(Primary),
	// 具名 → 该所;前端据此知道当前 Tab 展示的是哪家。
	Venue                 string                    `json:"venue"`
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

// NewDashboardServer 创建一个只读 Web 面板服务。P3a 起接收 Orchestrator;缺省所(Primary)的 store/controller
// 作为现有 handler 的快捷引用,保持路由与行为零变化。
func NewDashboardServer(addr string, orch *Orchestrator, logger *log.Logger) (*DashboardServer, error) {
	primary := orch.Primary()
	if primary == nil {
		return nil, fmt.Errorf("编排器未注册任何 venue,无法创建面板")
	}
	store := primary.store
	controller := primary.controller
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
		orchestrator: orch,
		store:        store,
		controller:   controller,
		auditStore:   auditStore,
		controlToken: randomDashboardToken(),
		logger:       logger,
		indexTmpl:    indexTmpl,
		startedAt:    time.Now(),
	}, nil
}

// Start 启动 Web 面板，并在 ctx 取消时优雅关闭。
func (s *DashboardServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/dashboard/summary", s.authorizeReadAPI(s.handleDashboardSummaryAPI))
	mux.HandleFunc("/api/performance", s.authorizeReadAPI(s.handlePerformanceAPI))
	mux.HandleFunc("/api/venues", s.authorizeReadAPI(s.handleVenuesAPI))
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
	mux.HandleFunc("/api/runtime/config/preview", s.authorizeReadAPI(s.handleRuntimeConfigPreviewAPI))
	mux.HandleFunc("/api/runtime/config/rollback", s.handleRuntimeConfigRollbackAPI)
	mux.HandleFunc("/api/runtime/clear-logs", s.handleRuntimeClearLogsAPI)
	mux.HandleFunc("/api/runtime/clear-data", s.handleRuntimeClearDataAPI)
	mux.HandleFunc("/api/runtime/clear-cache", s.handleRuntimeClearCacheAPI)
	mux.HandleFunc("/api/runtime/clear-all", s.handleRuntimeClearAllAPI)
	mux.HandleFunc("/api/runtime/capital", s.handleRuntimeCapitalAPI)
	mux.HandleFunc("/api/runtime/start", s.handleRuntimeStartAPI)
	mux.HandleFunc("/api/runtime/stop", s.handleRuntimeStopAPI)
	// S5-B 探针/指标(不鉴权,只暴露非敏感健康信息):供 k8s liveness/readiness 与 Prometheus 抓取。
	mux.HandleFunc("/livez", s.handleLivez)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/metrics", s.handleMetrics)
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
	// 安全:控制令牌**不再打印明文到日志**(日志会被转存/分享,明文令牌等于裸奔),只写入仅本机可读(0600)的
	// 令牌文件,并在日志里打印「文件路径 + 指纹」便于核对。start.sh 从该文件读取令牌打印到本机控制台。
	if s.controlToken != "" {
		if tokenPath, err := s.persistControlToken(); err == nil {
			s.logger.Printf("Dashboard 控制令牌已写入 %s (0600,仅本机可读,可 cat 查看);指纹=%s", tokenPath, hashDashboardToken(s.controlToken))
		} else {
			s.logger.Printf("Dashboard 控制令牌写入文件失败(%v),请改用 cat 令牌文件;指纹=%s", err, hashDashboardToken(s.controlToken))
		}
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
	// 面板 HTML 内嵌于二进制,版本随构建变化;禁用浏览器缓存,避免改版后用户仍看到旧页面(需强刷)。
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
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
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListSettlementRecords, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleDashboardSummaryAPI(w http.ResponseWriter, r *http.Request) {
	// summary 是「单所视图」:缺省/all → 缺省所(Primary);具名 → 该所。跨所 all 聚合视图是独立后续项(§9.2)。
	rt, err := s.resolveVenue(venueParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	ctrl := rt.controller
	store := rt.store

	limit := parseLimit(r, 200)
	runtimeLogs, err := ctrl.RecentLogs(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	executionIncidents, err := store.ListExecutionIncidents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	domainEvents, err := store.ListDomainEvents(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settlements, err := store.ListSettlementRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	monitors, err := store.ListMonitorRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	backtests, err := store.ListBacktestRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualRisks, err := store.ListResidualRiskRecords(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualSeverityStats, err := store.ListResidualSeverityStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	residualScenarioStats, err := store.ListResidualScenarioStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	capitalSnapshot := RuntimeCapitalSnapshot{}
	capitalSnapshotError := ""
	if snapshot, snapshotErr := ctrl.CurrentCapitalSnapshot(); snapshotErr != nil {
		capitalSnapshotError = snapshotErr.Error()
	} else {
		capitalSnapshot = snapshot
	}

	writeJSON(w, DashboardSummaryResponse{
		Venue:                 rt.name,
		RuntimeStatus:         ctrl.Status(),
		CapitalSnapshot:       capitalSnapshot,
		CapitalSnapshotError:  capitalSnapshotError,
		RuntimeLogs:           runtimeLogs,
		RuntimeConfig:         ctrl.CurrentRiskConfig(),
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
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	limit := parseLimit(r, 200)
	injectVenue := venueParam(r) == venueAllSelector
	executionIncidents, err := aggregateRecords(targets, limit, (*ReportStore).ListExecutionIncidents, injectVenue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	domainEvents, err := aggregateRecords(targets, limit, (*ReportStore).ListDomainEvents, injectVenue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, buildUnifiedDashboardEvents(domainEvents, executionIncidents, limit))
}

func (s *DashboardServer) handleMonitorAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListMonitorRecords, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleBacktestAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListBacktestRecords, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleResidualRiskAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListResidualRiskRecords, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleResidualSeverityStatsAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	stats, err := aggregateStats(targets, (*ReportStore).ListResidualSeverityStats)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *DashboardServer) handleResidualScenarioStatsAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	stats, err := aggregateStats(targets, (*ReportStore).ListResidualScenarioStats)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *DashboardServer) handleRuntimeStatusAPI(w http.ResponseWriter, r *http.Request) {
	rt, err := s.resolveVenue(venueParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, rt.controller.Status())
}

func (s *DashboardServer) handleRuntimeLogsAPI(w http.ResponseWriter, r *http.Request) {
	rt, err := s.resolveVenue(venueParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	logs, err := rt.controller.RecentLogs(parseLimit(r, 200))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, logs)
}

func (s *DashboardServer) handleExecutionIncidentsAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListExecutionIncidents, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleDomainEventsAPI(w http.ResponseWriter, r *http.Request) {
	targets, err := s.readTargets(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	records, err := aggregateRecords(targets, parseLimit(r, 200), (*ReportStore).ListDomainEvents, venueParam(r) == venueAllSelector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}

func (s *DashboardServer) handleRuntimeConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rt, err := s.resolveVenue(venueParam(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if rt.controller == nil {
			http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, rt.controller.CurrentRiskConfig())
	case http.MethodPost:
		if !s.authorizeRuntimeControl(w, r, "config_update") {
			return
		}
		rt, ok := s.resolveControlTarget(w, r, "config_update")
		if !ok {
			return
		}
		if rt.controller == nil {
			http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
			return
		}
		var req RuntimeRiskConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.auditControlAction(r, "config_update", false, fmt.Sprintf("解析风控参数失败: %v", err))
			http.Error(w, fmt.Sprintf("解析风控参数失败: %v", err), http.StatusBadRequest)
			return
		}
		updated, err := rt.controller.UpdateRiskConfig(req)
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

// handleRuntimeConfigPreviewAPI 干跑预览改参的字段级 before/after diff,不落地任何改动(R35/S5-F)。
func (s *DashboardServer) handleRuntimeConfigPreviewAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 预览为干跑(不落地),按单所视图解析(缺省/all → 缺省所)。
	rt, err := s.resolveVenue(venueParam(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	var req RuntimeRiskConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("解析风控参数失败: %v", err), http.StatusBadRequest)
		return
	}
	diffs, err := rt.controller.PreviewRiskConfigUpdate(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"diffs": diffs, "changed": len(diffs)})
}

// handleRuntimeConfigRollbackAPI 一键回滚到上一次改参前的配置(R35/S5-F)。
func (s *DashboardServer) handleRuntimeConfigRollbackAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "config_rollback") {
		return
	}
	rt, ok := s.resolveControlTarget(w, r, "config_rollback")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	rolledBack, err := rt.controller.RollbackRiskConfig()
	if err != nil {
		s.auditControlAction(r, "config_rollback", false, err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditControlAction(r, "config_rollback", true, "风控参数已回滚到上一版本")
	writeJSON(w, map[string]any{
		"message": "风控参数已回滚到上一版本，将在下一次启动任务时生效",
		"config":  rolledBack,
	})
}

func (s *DashboardServer) handleRuntimeClearLogsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "clear_logs") {
		return
	}
	rt, ok := s.resolveControlTarget(w, r, "clear_logs")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := rt.controller.ClearRuntimeLogs(); err != nil {
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
	rt, ok := s.resolveControlTarget(w, r, "clear_data")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := rt.controller.ClearDashboardData(); err != nil {
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
	rt, ok := s.resolveControlTarget(w, r, "clear_cache")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := rt.controller.ClearHistoricalCache(); err != nil {
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
	rt, ok := s.resolveControlTarget(w, r, "clear_all")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	// clear_all 毁灭性(清空该所全部日志+记录),强制 confirm:必须显式带 confirm=<venue 名>,防误触(§7.3/§13.2)。
	if strings.TrimSpace(r.URL.Query().Get("confirm")) != rt.name {
		const msg = "clear_all 为毁灭性操作,须显式确认:请带 confirm=<交易所名>(与目标所一致)"
		s.auditControlAction(r, "clear_all", false, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if err := rt.controller.ClearAllRecords(); err != nil {
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
	rt, ok := s.resolveControlTarget(w, r, "capital_adjust")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	var req RuntimeCapitalAdjustmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "capital_adjust", false, fmt.Sprintf("解析仓位调整参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析仓位调整参数失败: %v", err), http.StatusBadRequest)
		return
	}
	result, err := rt.controller.AdjustTradingCapital(req)
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
	rt, ok := s.resolveControlTarget(w, r, "start")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}

	var req RuntimeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditControlAction(r, "start", false, fmt.Sprintf("解析启动参数失败: %v", err))
		http.Error(w, fmt.Sprintf("解析启动参数失败: %v", err), http.StatusBadRequest)
		return
	}
	if err := rt.controller.Start(req); err != nil {
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
	rt, ok := s.resolveControlTarget(w, r, "stop")
	if !ok {
		return
	}
	if rt.controller == nil {
		http.Error(w, "后台控制器未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := rt.controller.Stop(); err != nil {
		s.auditControlAction(r, "stop", false, err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.auditControlAction(r, "stop", true, "已发送停止信号")
	writeJSON(w, map[string]string{"message": "已发送停止信号"})
}

// tokenMatches 用恒定时间比较核对面板令牌(防时序侧信道泄露令牌长度/前缀)。空令牌视为不匹配。
func (s *DashboardServer) tokenMatches(provided string) bool {
	if s.controlToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.controlToken)) == 1
}

func (s *DashboardServer) authorizeRuntimeControl(w http.ResponseWriter, r *http.Request, action string) bool {
	// 远程请求(非本机回环):按需放开——只要持有合法控制令牌即放行(原为一律拒绝本机外请求)。
	// 令牌是此处唯一鉴权凭据;未配置令牌时仍只允许本机,避免裸奔写接口被公网直接调用。
	if !isLoopbackRequest(r) {
		if s.controlToken == "" {
			s.auditControlAction(r, action, false, "未设置控制令牌时仅允许本机控制")
			http.Error(w, "未设置控制令牌时仅允许本机控制", http.StatusForbidden)
			return false
		}
		if !s.tokenMatches(r.Header.Get("X-Dashboard-Token")) {
			s.auditControlAction(r, action, false, "远程控制令牌无效")
			http.Error(w, "远程控制令牌无效", http.StatusForbidden)
			return false
		}
		return true
	}
	if s.controlToken == "" {
		return true
	}
	if !s.tokenMatches(r.Header.Get("X-Dashboard-Token")) {
		s.auditControlAction(r, action, false, "后台控制令牌无效")
		http.Error(w, "后台控制令牌无效", http.StatusForbidden)
		return false
	}
	return true
}

func (s *DashboardServer) authorizeRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			if s.controlToken != "" && !s.tokenMatches(r.Header.Get("X-Dashboard-Token")) {
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
			if s.controlToken != "" && !s.tokenMatches(r.Header.Get("X-Dashboard-Token")) {
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
		s.auditVenueLabel(r),
		metadata,
	)
	if err := s.auditStore.Append(record); err != nil && s.logger != nil {
		s.logger.Printf("写入 dashboard 审计日志失败: %v", err)
	}
}

// auditVenueLabel 解析本次控制操作作用的所标签供审计:具名 → 该名;all → "all"(扇出拒绝场景也如实记录意图);
// 缺省(空)→ 缺省所名(Primary)。编排器缺失时回退空串。
func (s *DashboardServer) auditVenueLabel(r *http.Request) string {
	name := venueParam(r)
	if name != "" {
		return name
	}
	if s.orchestrator != nil {
		if primary := s.orchestrator.Primary(); primary != nil {
			return primary.name
		}
	}
	return ""
}

// dashboardControlTokenFileName 是控制令牌落盘文件名(写在 RecordDir 下,0600 权限)。
const dashboardControlTokenFileName = ".dashboard_control_token"

// persistControlToken 把控制令牌以 0600 权限原子写入 RecordDir 下的令牌文件,返回路径。供操作者本机读取,避免明文进日志。
func (s *DashboardServer) persistControlToken() (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("记录存储未初始化")
	}
	tokenPath := filepath.Join(s.store.RecordDir(), dashboardControlTokenFileName)
	tmp := tokenPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.controlToken+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, tokenPath); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tokenPath, nil
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
