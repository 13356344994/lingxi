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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed webpanel/index.html
var webPanelFS embed.FS

// DashboardServer 本地控制面板:方向引擎控制/观测 + 账户级权益/收益 + 记录浏览。
type DashboardServer struct {
	addr         string
	orchestrator *Orchestrator
	auditStore   *DashboardAuditStore
	controlToken string
	logger       *log.Logger
	indexTmpl    *template.Template
	startedAt    time.Time
	// tokenMirrorDir 是控制令牌文件的固定镜像目录(全局 records/)。
	tokenMirrorDir string
	// tokenNotify 在令牌落盘后被调用(如经 Telegram 推送给用户),nil=不推送。
	tokenNotify func(token string)
	// equitySvc 账户级权益采样/收益核算服务;nil=未注入。
	equitySvc *AccountEquityService
	// directionalEngines 各所方向引擎;键=venue 名。
	directionalEngines map[string]*OKXDirectionalEngine
	// controlPlane 统一控制面; nil=未注入(测试/旧路径)。
	controlPlane *ControlPlane
	// venueEquityCache 缓存各所钱包总额/浮盈亏拉取结果,避免面板 5s 轮询触发交易所限频。
	venueEquityCacheMu sync.Mutex
	venueEquityCache   map[string]venueEquityCacheEntry
	// recordDir 根记录目录(account_risk_equity_state.json 等所在);空=回退 "records"。
	recordDir string
}

// SetRecordDir 注入根记录目录(供面板读取账户风控充提平移状态与 /records/ 静态目录)。
func (s *DashboardServer) SetRecordDir(dir string) { s.recordDir = strings.TrimSpace(dir) }

func (s *DashboardServer) recordDirOrDefault() string {
	if s != nil && strings.TrimSpace(s.recordDir) != "" {
		return strings.TrimSpace(s.recordDir)
	}
	return "records"
}

// SetTokenMirrorDir 设置控制令牌的固定镜像目录(启动前调用)。
func (s *DashboardServer) SetTokenMirrorDir(dir string) { s.tokenMirrorDir = strings.TrimSpace(dir) }

// SetTokenNotifier 设置令牌推送回调(启动前调用)。
func (s *DashboardServer) SetTokenNotifier(fn func(token string)) { s.tokenNotify = fn }

// SetAccountEquityService 注入账户级权益服务(供收益面板「北京0点基准」与快照清理使用)。
func (s *DashboardServer) SetAccountEquityService(svc *AccountEquityService) {
	s.equitySvc = svc
}

// SetOKXDirectionalEngine 按 eng.Venue() 注册方向引擎(供 /api/okx-directional 读取)。
func (s *DashboardServer) SetOKXDirectionalEngine(e *OKXDirectionalEngine) {
	if e == nil {
		return
	}
	if s.directionalEngines == nil {
		s.directionalEngines = make(map[string]*OKXDirectionalEngine)
	}
	s.directionalEngines[e.Venue()] = e
}

// directionalEngine 按 venue 名取方向引擎;空 venue 默认 okx。
func (s *DashboardServer) directionalEngine(venueName string) *OKXDirectionalEngine {
	if s == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(venueName))
	if v == "" {
		v = "okx"
	}
	if s.directionalEngines == nil {
		return nil
	}
	return s.directionalEngines[v]
}

// SetControlPlane 注入统一控制面(供 STOP/START 与 status 展示)。
func (s *DashboardServer) SetControlPlane(cp *ControlPlane) {
	s.controlPlane = cp
}

// NewDashboardServer 创建本地 Web 面板服务。
func NewDashboardServer(addr string, orch *Orchestrator, logger *log.Logger) (*DashboardServer, error) {
	primary := orch.Primary()
	if primary == nil {
		return nil, fmt.Errorf("编排器未注册任何 venue,无法创建面板")
	}
	indexTmpl, err := template.ParseFS(webPanelFS, "webpanel/index.html")
	if err != nil {
		return nil, fmt.Errorf("解析 Web 面板模板失败: %w", err)
	}
	return &DashboardServer{
		addr:         addr,
		orchestrator: orch,
		controlToken: randomDashboardToken(),
		logger:       logger,
		indexTmpl:    indexTmpl,
		startedAt:    time.Now(),
	}, nil
}

// ensureAuditStore 懒初始化审计存储(recordDir 由 SetRecordDir 注入,晚于构造)。
func (s *DashboardServer) ensureAuditStore() *DashboardAuditStore {
	if s == nil {
		return nil
	}
	if s.auditStore != nil {
		return s.auditStore
	}
	store, err := NewDashboardAuditStore(filepath.Join(s.recordDirOrDefault(), dashboardAuditLogJSONLName))
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("初始化 dashboard 审计日志失败: %v", err)
		}
		return nil
	}
	s.auditStore = store
	return store
}

// Start 启动 Web 面板，并在 ctx 取消时优雅关闭。
func (s *DashboardServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/venues", s.authorizeReadAPI(s.handleVenuesAPI))
	mux.HandleFunc("/api/performance", s.authorizeReadAPI(s.handlePerformanceAPI))
	mux.HandleFunc("/api/earnings", s.authorizeReadAPI(s.handleEarningsAPI))
	mux.HandleFunc("/api/earnings/clear", s.handleEarningsClearAPI)
	mux.HandleFunc("/api/okx-directional", s.authorizeReadAPI(s.handleOKXDirectionalAPI))
	mux.HandleFunc("/api/okx-directional/control", s.handleOKXDirectionalControlAPI)
	mux.HandleFunc("/api/fund-flow/declare", s.handleFundFlowDeclareAPI) // ㊳④ 资金划转申报(控制类鉴权)
	mux.HandleFunc("/api/mission", s.authorizeReadAPI(s.handleMissionAPI))
	mux.HandleFunc("/api/params", s.handleRuntimeParamsAPI) // GET 只读现值;POST 内部走控制鉴权+审计
	mux.HandleFunc("/api/strategy-report", s.authorizeReadAPI(s.handleStrategyReportAPI))
	// 探针/指标(不鉴权,只暴露非敏感健康信息)。
	mux.HandleFunc("/livez", s.handleLivez)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.Handle("/records/", s.authorizeRead(http.StripPrefix("/records/", http.FileServer(http.Dir(s.recordDirOrDefault())))))

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
	if s.controlToken != "" {
		if tokenPath, err := s.persistControlToken(); err == nil {
			s.logger.Printf("Dashboard 控制令牌已写入 %s (0600,仅本机可读,可 cat 查看);指纹=%s", tokenPath, hashDashboardToken(s.controlToken))
		} else {
			s.logger.Printf("Dashboard 控制令牌写入文件失败(%v),请改用 cat 令牌文件;指纹=%s", err, hashDashboardToken(s.controlToken))
		}
		if s.tokenNotify != nil {
			go s.tokenNotify(s.controlToken)
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
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	data := struct {
		ControlAuditLog string
		VersionSummary  string
	}{
		ControlAuditLog: path.Join("/records", dashboardAuditLogJSONLName),
		VersionSummary:  buildVersionSummary(),
	}
	if err := s.indexTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// tokenMatches 用恒定时间比较核对面板令牌(防时序侧信道)。空令牌视为不匹配。
func (s *DashboardServer) tokenMatches(provided string) bool {
	if s.controlToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.controlToken)) == 1
}

func (s *DashboardServer) authorizeRuntimeControl(w http.ResponseWriter, r *http.Request, action string) bool {
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

// readTokenFromRequest 只读路径的令牌提取:优先 X-Dashboard-Token 请求头(JS fetch),
// 回退 lx_token cookie(浏览器 <a href> 导航带不了自定义头,如 records/、审计日志链接)。
// 仅用于只读鉴权;控制类接口保持只认请求头——cookie 会被浏览器自动附带,认 cookie 做写操作等于开 CSRF 口子。
func readTokenFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Dashboard-Token"); t != "" {
		return t
	}
	if c, err := r.Cookie("lx_token"); err == nil {
		if v, uerr := url.QueryUnescape(c.Value); uerr == nil {
			return v
		}
		return c.Value
	}
	return ""
}

func (s *DashboardServer) authorizeRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			if s.controlToken != "" && !s.tokenMatches(readTokenFromRequest(r)) {
				http.Error(w, "仅允许本机或持有合法令牌访问记录数据(请先在面板首页保存控制令牌)", http.StatusForbidden)
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
			if s.controlToken != "" && !s.tokenMatches(readTokenFromRequest(r)) {
				http.Error(w, "仅允许本机或持有合法令牌访问 API", http.StatusForbidden)
				return
			}
		}
		handler(w, r)
	}
}

func (s *DashboardServer) auditControlAction(r *http.Request, action string, success bool, detail string) {
	if s == nil {
		return
	}
	store := s.ensureAuditStore()
	if store == nil {
		return
	}
	record := newDashboardAuditRecord(
		action,
		success,
		detail,
		"dashboard",
		strings.TrimSpace(r.RemoteAddr),
		hashDashboardToken(r.Header.Get("X-Dashboard-Token")),
		"",
		"",
		s.auditVenueLabel(r),
		TraceRecordMetadata{},
	)
	if err := store.Append(record); err != nil && s.logger != nil {
		s.logger.Printf("写入 dashboard 审计日志失败: %v", err)
	}
}

// auditVenueLabel 解析本次控制操作作用的所标签供审计。
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

// persistControlToken 把控制令牌以 0600 权限原子写入 RecordDir 下的令牌文件,返回路径。
func (s *DashboardServer) persistControlToken() (string, error) {
	tokenPath := filepath.Join(s.recordDirOrDefault(), dashboardControlTokenFileName)
	if err := writeDashboardTokenFile(tokenPath, s.controlToken); err != nil {
		return "", err
	}
	if s.tokenMirrorDir != "" {
		mirror := filepath.Join(s.tokenMirrorDir, dashboardControlTokenFileName)
		if mirror != tokenPath {
			if err := writeDashboardTokenFile(mirror, s.controlToken); err != nil {
				s.logger.Printf("控制令牌镜像写入失败(主文件已写,仅镜像缺失): %v", err)
			}
		}
	}
	return tokenPath, nil
}

// writeDashboardTokenFile 以 0600 原子写入令牌文件。
func writeDashboardTokenFile(path, token string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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

func (s *DashboardServer) handleEarningsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().UTC()
	byVenue, total, dayStart := s.buildVenuesFinancialSnapshot(now)

	pnlSummary, pnlErr := TodayCSTStrategyPnL(s, now)
	allTimeSummary, _ := AllTimeStrategyPnL(s, now)
	accountEquityDelta := total.TodayEarningUSDT

	fundFlowAdjust := 0.0
	fundFlowKnown := false
	fundFlowSource := ""
	fundFlowUnconfirmed := 0.0
	if st, ok := loadAccountRiskEquityState(s.recordDir); ok {
		fundFlowAdjust = st.DailyFundFlowAdjustUSDT
		fundFlowKnown = true
		fundFlowSource = st.DailyFundFlowSource
		fundFlowUnconfirmed = st.DailyFundFlowReconcileUSDT
	}
	tradingEquityDelta := accountEquityDelta
	if fundFlowKnown {
		tradingEquityDelta = accountEquityDelta - fundFlowAdjust
	}

	resp := map[string]any{
		"basis":                       "settlement_ledger_realized_pnl",
		"window":                      "beijing_cst_day",
		"day_start_cst":               dayStart.Format(time.RFC3339),
		"generated_at":                now.Format(time.RFC3339),
		"realized_pnl_usdt":           pnlSummary.TotalNetPnL,
		"untrusted_realized_pnl_usdt": pnlSummary.UntrustedNetPnL,
		"account_equity_delta_usdt":   accountEquityDelta,
		"fund_flow_adjust_usdt":       fundFlowAdjust,
		"fund_flow_known":             fundFlowKnown,
		"fund_flow_source":            fundFlowSource,
		"fund_flow_unconfirmed_usdt":  fundFlowUnconfirmed,
		"trading_equity_delta_usdt":   tradingEquityDelta,
		"all_time_realized_pnl_usdt":  allTimeSummary.TotalNetPnL,
		"all_time_untrusted_pnl_usdt": allTimeSummary.UntrustedNetPnL,
		"trade_count":                 pnlSummary.TradeCount,
		"untrusted_trade_count":       pnlSummary.UntrustedTradeCount,
		"total_funding_usdt":          pnlSummary.TotalFunding,
		"total_fees_usdt":             pnlSummary.TotalFees,
		"by_strategy":                 pnlSummary.ByStrategy,
		"by_symbol":                   pnlSummary.BySymbol,
		"by_venue_settlement":         pnlSummary.ByVenue,
		"by_venue":                    byVenue,
		"total":                       total,
	}
	if pnlErr != nil {
		resp["pnl_error"] = pnlErr.Error()
	}
	writeJSON(w, resp)
}

func (s *DashboardServer) handleEarningsClearAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "earnings_clear") {
		return
	}
	if s.equitySvc == nil {
		writeJSON(w, map[string]any{"ok": true, "removed": 0, "message": "权益服务未启用,无快照可清"})
		return
	}
	before := time.Now().UTC().Add(-24 * time.Hour)
	removed, err := s.equitySvc.pruneEquitySnapshots(before)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "earnings_clear", true, fmt.Sprintf("removed=%d", removed))
	writeJSON(w, map[string]any{"ok": true, "removed": removed, "message": fmt.Sprintf("已清除 %d 条 24 小时以外的权益快照", removed)})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}
