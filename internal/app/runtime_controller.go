package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

const runtimeEventLogJSONLName = "runtime_events.jsonl"

const (
	runtimeLogRetention       = 6 * time.Hour
	runtimeLogCleanupInterval = 10 * time.Minute
	runtimeLogScannerBuffer   = 1024 * 1024
	runtimeLogMemoryLimit     = 2000
	liveStartupCheckTimeout   = 30 * time.Second
	maxServerTimeOffset       = 5 * time.Second
)

type RuntimeMode string

const (
	RuntimeModeLive    RuntimeMode = "live"
	RuntimeModeMonthly RuntimeMode = "monthly"
)

// RuntimeTaskStatus 表示当前后台任务状态。
type RuntimeTaskStatus struct {
	Running                      bool                      `json:"running"`
	RunID                        string                    `json:"run_id"`
	CurrentCycleID               string                    `json:"current_cycle_id"`
	Mode                         string                    `json:"mode"`
	Status                       string                    `json:"status"`
	CurrentStage                 string                    `json:"current_stage"`
	StageReason                  string                    `json:"stage_reason"`
	StageTransitionedAt          string                    `json:"stage_transitioned_at"`
	CurrentTargets               int                       `json:"current_targets"`
	CurrentPositions             []string                  `json:"current_positions"`
	CurrentMonitorTargets        []string                  `json:"current_monitor_targets"`
	ProgressCurrent              int                       `json:"progress_current"`
	ProgressTotal                int                       `json:"progress_total"`
	StartedAt                    string                    `json:"started_at"`
	FinishedAt                   string                    `json:"finished_at"`
	SecretPath                   string                    `json:"secret_path"`
	LookbackDays                 int                       `json:"lookback_days"`
	LastError                    string                    `json:"last_error"`
	LastMessage                  string                    `json:"last_message"`
	ExecutionIncidentCount       int                       `json:"execution_incident_count"`
	ExecutionIncidentLimit       int                       `json:"execution_incident_limit"`
	PlatformRejectCount          int                       `json:"platform_reject_count"`
	PlatformRequestErrors        int                       `json:"platform_request_errors"`
	RecoveryActionCount          int                       `json:"recovery_action_count"`
	OrderUncertaintyCount        int                       `json:"order_uncertainty_count"`
	CircuitBreakerTripped        bool                      `json:"circuit_breaker_tripped"`
	CircuitBreakerReason         string                    `json:"circuit_breaker_reason"`
	CooldownUntil                string                    `json:"cooldown_until"`
	LastOrderAt                  string                    `json:"last_order_at"`
	LastOrderAction              string                    `json:"last_order_action"`
	LastRecoveryAt               string                    `json:"last_recovery_at"`
	LastRecoveryAction           string                    `json:"last_recovery_action"`
	LastManagedDrawdownAt        string                    `json:"last_managed_drawdown_at"`
	LastManagedDrawdownAction    string                    `json:"last_managed_drawdown_action"`
	ManagedDrawdownSummary       string                    `json:"managed_drawdown_summary"`
	HoldMonitorSummary           string                    `json:"hold_monitor_summary"`
	LastUncertainOrder           string                    `json:"last_uncertain_order"`
	RemainingIncidentBudget      int                       `json:"remaining_incident_budget"`
	RemainingRecoveryBudget      int                       `json:"remaining_recovery_budget"`
	RemainingUncertaintyBudget   int                       `json:"remaining_uncertainty_budget"`
	RemainingRejectBudget        int                       `json:"remaining_reject_budget"`
	RemainingErrorBudget         int                       `json:"remaining_error_budget"`
	ProtectedIOCAttempts         int                       `json:"protected_ioc_attempts"`
	ProtectedIOCCanceled         int                       `json:"protected_ioc_canceled"`
	ProtectedIOCExpired          int                       `json:"protected_ioc_expired"`
	ProtectedIOCPartial          int                       `json:"protected_ioc_partial"`
	ProtectedIOCAvgUnfilledRatio float64                   `json:"protected_ioc_avg_unfilled_ratio"`
	ProtectedIOCCancelRate       float64                   `json:"protected_ioc_cancel_rate"`
	HealthStatus                 string                    `json:"health_status"`
	HealthSummary                string                    `json:"health_summary"`
	HealthScore                  float64                   `json:"health_score"`
	EngineGear                   string                    `json:"engine_gear"`
	CanContinue                  bool                      `json:"can_continue"`
	ConsecutiveRejects           int                       `json:"consecutive_rejects"`
	ConsecutiveErrors            int                       `json:"consecutive_errors"`
	CycleOpenNotionalUSDT        float64                   `json:"cycle_open_notional_usdt"`
	RemainingNotionalBudget      float64                   `json:"remaining_notional_budget_usdt"`
	LastExceptionCategory        string                    `json:"last_exception_category"`
	LastExceptionSeverity        string                    `json:"last_exception_severity"`
	LastExceptionDetail          string                    `json:"last_exception_detail"`
	CurrentCooldownCategory      string                    `json:"current_cooldown_category"`
	CurrentCooldownSeverity      string                    `json:"current_cooldown_severity"`
	CooledSymbols                []string                  `json:"cooled_symbols"`
	LastExecutionEvent           string                    `json:"last_execution_event"`
	RecentExecutionEvents        []ExecutionIncidentRecord `json:"recent_execution_events"`
	EmergencyStopTriggered       bool                      `json:"emergency_stop_triggered"`
	EmergencyStopReason          string                    `json:"emergency_stop_reason"`
	DailyRiskLimitTriggered      bool                      `json:"daily_risk_limit_triggered"`
	DailyRiskLimitReason         string                    `json:"daily_risk_limit_reason"`
	DailyRealizedPnL             float64                   `json:"daily_realized_pnl"`
	DailyOrderCount              int                       `json:"daily_order_count"`
	DailyOpenNotionalUSDT        float64                   `json:"daily_open_notional_usdt"`
	RemainingDailyLossBudget     float64                   `json:"remaining_daily_loss_budget_usdt"`
	RemainingDailyOrderBudget    int                       `json:"remaining_daily_order_budget"`
	RemainingDailyNotionalBudget float64                   `json:"remaining_daily_notional_budget_usdt"`
}

// RuntimeLogRecord 表示一条页面可见的后台运行日志。
type RuntimeLogRecord struct {
	TraceRecordMetadata
	RecordedAt string `json:"recorded_at"`
	RunID      string `json:"run_id"`
	CycleID    string `json:"cycle_id"`
	Source     string `json:"source"`
	Message    string `json:"message"`
}

// RuntimeStartRequest 表示页面发起的运行请求。
type RuntimeStartRequest struct {
	Mode         string `json:"mode"`
	SecretPath   string `json:"secret_path"`
	LookbackDays int    `json:"lookback_days"`
}

// RuntimeRiskConfig 表示页面可调的容量与风控参数。
type RuntimeRiskConfig struct {
	PerSymbolQuoteUSDT            float64 `json:"per_symbol_quote_usdt"`
	MaxCycleOpenNotionalUSDT      float64 `json:"max_cycle_open_notional_usdt"`
	RecentTradeVolumeWindow       string  `json:"recent_trade_volume_window"`
	MaxRecentVolumeShare          float64 `json:"max_recent_volume_share"`
	MaxOpenInterestShare          float64 `json:"max_open_interest_share"`
	MaxVisibleDepthShare          float64 `json:"max_visible_depth_share"`
	MinNetProfitRate              float64 `json:"min_net_profit_rate"`
	ImmediateInvalidateRate       float64 `json:"immediate_invalidate_rate"`
	MonitorMaxConsecutiveBreach   int     `json:"monitor_max_consecutive_breach"`
	MaxCycleLossUSDT              float64 `json:"max_cycle_loss_usdt"`
	MaxExecutionIncidents         int     `json:"max_execution_incidents"`
	MaxOrderUncertaintyPerCycle   int     `json:"max_order_uncertainty_per_cycle"`
	MaxConsecutivePlatformRejects int     `json:"max_consecutive_platform_rejects"`
	MaxConsecutiveRequestErrors   int     `json:"max_consecutive_request_errors"`
	ProtectedIOCMinAttempts       int     `json:"protected_ioc_min_attempts"`
	ProtectedIOCMaxUnfilledRatio  float64 `json:"protected_ioc_max_unfilled_ratio"`
	ProtectedIOCMaxCancelRate     float64 `json:"protected_ioc_max_cancel_rate"`
	ExitDelayAfterFunding         string  `json:"exit_delay_after_funding"`
	ExitMonitorInterval           string  `json:"exit_monitor_interval"`
	ExecutionConfirmTimeout       string  `json:"execution_confirm_timeout"`
	SymbolAbnormalCooldown        string  `json:"symbol_abnormal_cooldown"`
	PlatformMinorCooldown         string  `json:"platform_minor_cooldown"`
	PlatformAbnormalCooldown      string  `json:"platform_abnormal_cooldown"`
	PlatformCriticalCooldown      string  `json:"platform_critical_cooldown"`
	MaxRecoveryActionsPerCycle    int     `json:"max_recovery_actions_per_cycle"`
	MaxDailyLossUSDT              float64 `json:"max_daily_loss_usdt"`
	MaxDailyOrderCount            int     `json:"max_daily_order_count"`
	MaxDailyOpenNotionalUSDT      float64 `json:"max_daily_open_notional_usdt"`
}

type LiveRuntimeSnapshot struct {
	RunID                        string
	CycleID                      string
	Stage                        EngineStage
	StageReason                  string
	StageTransitionedAt          string
	CurrentTargets               int
	CurrentPositions             []string
	CurrentMonitorTargets        []string
	PlatformRejectCount          int
	PlatformRequestErrors        int
	RecoveryActionCount          int
	OrderUncertaintyCount        int
	FuseTripped                  bool
	FuseReason                   string
	CooldownUntil                string
	LastOrderAt                  string
	LastOrderAction              string
	LastRecoveryAt               string
	LastRecoveryAction           string
	LastManagedDrawdownAt        string
	LastManagedDrawdownAction    string
	ManagedDrawdownSummary       string
	HoldMonitorSummary           string
	LastUncertainOrder           string
	RemainingIncidentBudget      int
	RemainingRecoveryBudget      int
	RemainingUncertaintyBudget   int
	RemainingRejectBudget        int
	RemainingErrorBudget         int
	ProtectedIOCAttempts         int
	ProtectedIOCCanceled         int
	ProtectedIOCExpired          int
	ProtectedIOCPartial          int
	ProtectedIOCAvgUnfilledRatio float64
	ProtectedIOCCancelRate       float64
	HealthStatus                 string
	HealthSummary                string
	HealthScore                  float64
	EngineGear                   string
	CanContinue                  bool
	ConsecutiveRejects           int
	ConsecutiveErrors            int
	CycleOpenNotionalUSDT        float64
	RemainingNotionalBudget      float64
	LastExceptionCategory        string
	LastExceptionSeverity        string
	LastExceptionDetail          string
	CurrentCooldownCategory      string
	CurrentCooldownSeverity      string
	CooledSymbols                []string
	EmergencyStopTriggered       bool
	EmergencyStopReason          string
	DailyRiskLimitTriggered      bool
	DailyRiskLimitReason         string
	DailyRealizedPnL             float64
	DailyOrderCount              int
	DailyOpenNotionalUSDT        float64
	RemainingDailyLossBudget     float64
	RemainingDailyOrderBudget    int
	RemainingDailyNotionalBudget float64
}

// RuntimeController 负责后台运行模式选择、任务互斥和日志持久化。
type RuntimeController struct {
	rootCtx context.Context
	store   *ReportStore
	cfg     EngineConfig
	logger  *log.Logger
	logs    *RuntimeLogStore

	// venueName 标识本控制器归属的交易所(""/"binance"=币安主路径,走引擎;其它=多所适配器路径)。
	// 由 buildVenueRuntime 注入;资金快照/调仓据此分支到对应 venue.CapitalManagement,绝不改币安行为。
	venueName string

	// bybitAlerts 是 Bybit 执行器的实时告警分发器(开/平/启停/失败推送到 Telegram 等出口)。
	// 仅在 runBybitExecutor 启动时按 secret.yaml 的 Telegram 凭据构造;未配置或非 bybit 路径时为 nil(所有调用 nil-safe)。
	bybitAlerts *AlertDispatcher

	// bybitPendingPersist 是「交易所已成交但台账落盘失败」的持仓内存兜底(PROJECT_AUDIT C-10):
	// manage 每 tick 先重试落盘,且把仍未落盘的持仓并入管理视图——即便磁盘持续故障,平仓调度照常,
	// 绝不因记账失败让真实持仓失管。仅 Bybit 执行循环 goroutine 访问(单写者),无需额外锁。
	bybitPendingPersist map[venue.Symbol]BybitLivePosition

	mu                             sync.Mutex
	status                         RuntimeTaskStatus
	cancel                         context.CancelFunc
	cachedCapitalSnapshot          RuntimeCapitalSnapshot
	cachedCapitalSnapshotAt        time.Time
	cachedCapitalSnapshotSecretKey string
	// riskConfigHistory 保存每次在线改参前的配置快照(最近 riskConfigHistoryLimit 条),供 R35 一键回滚(S5-F)。
	riskConfigHistory []EngineConfig
}

// riskConfigHistoryLimit 限制在线改参历史栈深度,防止无界增长。
const riskConfigHistoryLimit = 10

// RuntimeLogStore 负责把后台运行日志保存到 jsonl 文件，并支持页面读取最近日志。
func NewRuntimeController(ctx context.Context, store *ReportStore, cfg EngineConfig, logger *log.Logger) (*RuntimeController, error) {
	if logger == nil {
		logger = log.Default()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logStore, err := NewRuntimeLogStore(filepath.Join(store.RecordDir(), runtimeEventLogJSONLName))
	if err != nil {
		return nil, err
	}
	return &RuntimeController{
		rootCtx: ctx,
		store:   store,
		cfg:     cfg,
		logger:  logger,
		logs:    logStore,
		status: RuntimeTaskStatus{
			Status:      "idle",
			LastMessage: "等待页面选择运行模式",
		},
	}, nil
}

func (c *RuntimeController) Start(req RuntimeStartRequest) error {
	// 安全闸:Bybit 走独立的精简执行器(B5,runBybitExecutor,主闸 LiveTradingEnabled 默认 OFF=只 shadow);
	// 其余非币安所(OKX/Gate)的交易引擎尚未接入,Start 会构造币安引擎——若误启会变成「第二个币安引擎」双开实盘,
	// 故一律拒绝,当前仅开放资金管理(调仓)。
	isBybit := c.isBybitVenue()
	if c.isNonBinanceVenue() && !isBybit {
		return fmt.Errorf("venue %q 的自动交易引擎尚未接入,当前仅支持资金管理(调仓);请勿在该所启动任务", c.venueName)
	}
	mode := RuntimeMode(strings.TrimSpace(req.Mode))
	if mode != RuntimeModeLive && mode != RuntimeModeMonthly {
		return fmt.Errorf("不支持的运行模式: %s", req.Mode)
	}
	if isBybit && mode != RuntimeModeLive {
		return fmt.Errorf("Bybit 执行器仅支持实盘(live)模式,不支持历史模拟")
	}

	secretPath := strings.TrimSpace(req.SecretPath)
	if secretPath == "" {
		secretPath = "secret.yaml"
	}
	lookbackDays := req.LookbackDays
	if lookbackDays <= 0 {
		lookbackDays = c.cfg.HistoricalConfig.DefaultLookbackDays
	}
	runID := newRunID(string(mode))

	c.mu.Lock()
	if c.status.Running {
		c.mu.Unlock()
		return fmt.Errorf("已有后台任务正在运行，请先停止当前任务")
	}
	taskCfg := c.cfg
	cooldownUntil := strings.TrimSpace(c.status.CooldownUntil)
	c.mu.Unlock()

	// 币安实盘启动前检查是币安专属(账户/持仓 reconcile 等);Bybit 执行器有自己的启动前置(构造适配器 + 单写租约 +
	// reconcile,见 runBybitExecutor),故跳过这套币安检查。
	if mode == RuntimeModeLive && !isBybit {
		preflightCtx, cancel := context.WithTimeout(c.rootCtx, liveStartupCheckTimeout)
		defer cancel()
		if err := c.runLiveStartupChecks(preflightCtx, secretPath, taskCfg, runID, cooldownUntil); err != nil {
			c.appendControllerTraceLog(runID, "", "实盘启动前检查失败: %v", err)
			return err
		}
	}

	c.mu.Lock()
	if c.status.Running {
		c.mu.Unlock()
		return fmt.Errorf("已有后台任务正在运行，请先停止当前任务")
	}
	taskCtx, cancel := context.WithCancel(c.rootCtx)
	c.cancel = cancel
	c.status = RuntimeTaskStatus{
		Running:                    true,
		RunID:                      runID,
		CurrentCycleID:             "",
		Mode:                       string(mode),
		Status:                     "running",
		HealthStatus:               "healthy",
		HealthSummary:              "任务已创建，等待进入实盘阶段",
		CanContinue:                true,
		StartedAt:                  time.Now().UTC().Format(time.RFC3339),
		SecretPath:                 secretPath,
		LookbackDays:               lookbackDays,
		LastMessage:                "任务已创建，等待后台执行",
		ExecutionIncidentLimit:     taskCfg.MaxExecutionIncidents,
		RemainingIncidentBudget:    taskCfg.MaxExecutionIncidents,
		RemainingRecoveryBudget:    taskCfg.MaxRecoveryActionsPerCycle,
		RemainingUncertaintyBudget: taskCfg.MaxOrderUncertaintyPerCycle,
		RemainingRejectBudget:      taskCfg.MaxConsecutivePlatformRejects,
		RemainingErrorBudget:       taskCfg.MaxConsecutiveRequestErrors,
		RemainingNotionalBudget:    taskCfg.MaxCycleOpenNotionalUSDT,
		RecentExecutionEvents:      []ExecutionIncidentRecord{},
	}
	c.mu.Unlock()

	c.appendControllerTraceLog(runID, "", "已接收运行请求，mode=%s secret=%s lookbackDays=%d", mode, redactSecretPath(secretPath), lookbackDays)
	go c.runTask(taskCtx, mode, secretPath, lookbackDays, taskCfg)
	return nil
}

func (c *RuntimeController) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.status.Running || c.cancel == nil {
		return fmt.Errorf("当前没有正在运行的后台任务")
	}
	c.status.LastMessage = "已发送停止信号，等待任务退出"
	c.cancel()
	return nil
}

func (c *RuntimeController) Status() RuntimeTaskStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := c.status
	if len(c.status.CurrentPositions) > 0 {
		status.CurrentPositions = append([]string(nil), c.status.CurrentPositions...)
	}
	if len(c.status.CurrentMonitorTargets) > 0 {
		status.CurrentMonitorTargets = append([]string(nil), c.status.CurrentMonitorTargets...)
	}
	if len(c.status.CooledSymbols) > 0 {
		status.CooledSymbols = append([]string(nil), c.status.CooledSymbols...)
	}
	if len(c.status.RecentExecutionEvents) > 0 {
		status.RecentExecutionEvents = append([]ExecutionIncidentRecord(nil), c.status.RecentExecutionEvents...)
	}
	return status
}

func (c *RuntimeController) RecentLogs(limit int) ([]RuntimeLogRecord, error) {
	return c.logs.List(limit)
}

func (c *RuntimeController) ClearRuntimeLogs() error {
	if err := c.logs.Clear(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.LastMessage = "运行日志已清空"
	c.mu.Unlock()
	return nil
}

func (c *RuntimeController) ClearDashboardData() error {
	if err := c.store.ClearDashboardData(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.LastMessage = "CSV 记录已清空"
	c.mu.Unlock()
	return nil
}

func (c *RuntimeController) ClearHistoricalCache() error {
	c.mu.Lock()
	running := c.status.Running
	c.mu.Unlock()
	if running {
		return fmt.Errorf("后台任务运行中，不能清理历史缓存")
	}
	cacheDir := strings.TrimSpace(c.cfg.HistoricalConfig.CacheDir)
	if cacheDir == "" {
		recordDir := strings.TrimSpace(c.cfg.NetworkConfig.RecordDir)
		if recordDir == "" {
			recordDir = "records"
		}
		cacheDir = filepath.Join(recordDir, "historical_cache")
	}
	cache, err := newHistoricalPayloadCache(cacheDir)
	if err != nil {
		return err
	}
	if err := cache.Clear(); err != nil {
		return err
	}
	c.mu.Lock()
	c.status.LastMessage = "历史缓存已清空"
	c.mu.Unlock()
	return nil
}

// ClearAllRecords 一键清空所有页面日志与落盘记录：后台运行日志（含按天轮转）、各类记录文件、
// 历史缓存，以及运行态状态台账（回测汇总、控制审计、日级风控、残腿救援、未平仓对冲）。
// 要求后台任务未运行，避免与正在写盘的引擎竞争导致记录不一致。
func (c *RuntimeController) ClearAllRecords() error {
	c.mu.Lock()
	running := c.status.Running
	c.mu.Unlock()
	if running {
		return fmt.Errorf("后台任务运行中，请先停止任务再清空所有日志与记录")
	}

	// 1) 页面 JSON/JSONL 记录（结算/监控/回测/残腿/执行事故/领域事件/灰尘/待结算资金费/月度模拟）。
	if err := c.store.ClearDashboardData(); err != nil {
		return err
	}
	// 2) 后台运行日志（runtime_events.jsonl 及其按天轮转文件）。
	if err := c.logs.Clear(); err != nil {
		return err
	}
	// 3) 历史缓存。
	if err := c.ClearHistoricalCache(); err != nil {
		return err
	}
	// 4) 其余运行态台账 / 审计 / 事件文件：直接删除，引擎会在需要时按需重建。
	recordDir := strings.TrimSpace(c.cfg.NetworkConfig.RecordDir)
	if recordDir == "" {
		recordDir = "records"
	}
	auxiliary := []string{
		backtestRunCSVName,
		dashboardAuditLogJSONLName,
		liveDailyRiskStateName,
		string(liveExposureRescueStateName),
		liveOpenHedgesStateName,
	}
	for _, name := range auxiliary {
		target := filepath.Join(recordDir, name)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除记录文件失败(%s): %w", name, err)
		}
	}

	c.mu.Lock()
	c.status.LastMessage = "所有日志与记录已清空"
	c.mu.Unlock()
	return nil
}

func (c *RuntimeController) runTask(ctx context.Context, mode RuntimeMode, secretPath string, lookbackDays int, cfg EngineConfig) {
	var (
		err     error
		message string
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("后台任务 panic: %v", recovered)
			c.appendControllerLog("%v\n%s", panicErr, strings.TrimSpace(string(debug.Stack())))
			c.finishTask(panicErr, "任务因 panic 退出")
			return
		}
		c.finishTask(err, message)
	}()

	switch mode {
	case RuntimeModeLive:
		if c.isBybitVenue() {
			message, err = c.runBybitExecutor(ctx, secretPath, cfg)
		} else {
			message, err = c.runLiveEngine(ctx, secretPath, cfg)
		}
	case RuntimeModeMonthly:
		message, err = c.runMonthlySimulation(ctx, secretPath, lookbackDays, cfg)
	default:
		err = fmt.Errorf("未知模式: %s", mode)
	}
}

func (c *RuntimeController) finishTask(err error, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cancel = nil
	c.status.Running = false
	c.status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	switch {
	case err == nil:
		c.status.Status = "completed"
		c.setStageLocked(StageCompleted, nonEmpty(message, "本轮任务已正常完成"), "controller")
		c.status.HealthStatus = "completed"
		c.status.HealthSummary = "本轮任务已结束"
		c.status.CanContinue = false
		c.status.LastError = ""
		c.status.LastMessage = message
	case errors.Is(err, context.Canceled):
		c.status.Status = "stopped"
		c.setStageLocked(StageStopped, "任务已手动停止", "controller")
		c.status.HealthStatus = "stopped"
		c.status.HealthSummary = "任务已手动停止"
		c.status.CanContinue = false
		c.status.LastError = ""
		c.status.LastMessage = "任务已停止"
	default:
		c.status.Status = "failed"
		c.setStageLocked(StageFailed, nonEmpty(err.Error(), "任务执行失败"), "controller")
		c.status.HealthStatus = "failed"
		c.status.HealthSummary = nonEmpty(err.Error(), "任务执行失败")
		c.status.CanContinue = false
		c.status.LastError = err.Error()
		c.status.LastMessage = nonEmpty(message, "任务执行失败")
	}
}

func (c *RuntimeController) recordExecutionEvent(record ExecutionIncidentRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if strings.TrimSpace(record.RunID) != "" {
		c.status.RunID = strings.TrimSpace(record.RunID)
	}
	if strings.TrimSpace(record.CycleID) != "" {
		c.status.CurrentCycleID = strings.TrimSpace(record.CycleID)
	}
	c.status.ExecutionIncidentCount = record.IncidentCount
	if record.IncidentLimit > 0 {
		c.status.ExecutionIncidentLimit = record.IncidentLimit
	}
	c.status.LastExecutionEvent = fmt.Sprintf("%s | %s | %s", nonEmpty(record.Phase, "-"), nonEmpty(record.Action, "-"), nonEmpty(record.Detail, "-"))
	if strings.TrimSpace(record.Category) != "" || strings.TrimSpace(record.Severity) != "" {
		c.status.LastExecutionEvent = fmt.Sprintf("%s | %s | %s | %s", nonEmpty(record.Category, "-"), nonEmpty(record.Severity, "-"), nonEmpty(record.Action, "-"), nonEmpty(record.Detail, "-"))
	}
	if record.Fatal {
		c.status.CircuitBreakerTripped = true
		c.status.CircuitBreakerReason = nonEmpty(record.Detail, c.status.CircuitBreakerReason)
		c.status.LastMessage = "执行熔断已触发，等待任务退出"
	}
	c.status.RecentExecutionEvents = append(c.status.RecentExecutionEvents, record)
	if len(c.status.RecentExecutionEvents) > 8 {
		c.status.RecentExecutionEvents = c.status.RecentExecutionEvents[len(c.status.RecentExecutionEvents)-8:]
	}
}

func (c *RuntimeController) recordLiveEngineStatus(snapshot LiveRuntimeSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Mode != string(RuntimeModeLive) {
		return
	}
	if strings.TrimSpace(string(snapshot.Stage)) != "" {
		c.setStageLocked(snapshot.Stage, snapshot.StageReason, "runtime-state")
		if strings.TrimSpace(snapshot.StageTransitionedAt) != "" {
			c.status.StageTransitionedAt = strings.TrimSpace(snapshot.StageTransitionedAt)
		}
	}
	c.status.RunID = strings.TrimSpace(snapshot.RunID)
	c.status.CurrentCycleID = strings.TrimSpace(snapshot.CycleID)
	if snapshot.CurrentTargets >= 0 {
		c.status.CurrentTargets = snapshot.CurrentTargets
	}
	c.status.PlatformRejectCount = snapshot.PlatformRejectCount
	c.status.PlatformRequestErrors = snapshot.PlatformRequestErrors
	c.status.RecoveryActionCount = snapshot.RecoveryActionCount
	c.status.OrderUncertaintyCount = snapshot.OrderUncertaintyCount
	c.status.CurrentPositions = append([]string(nil), snapshot.CurrentPositions...)
	c.status.CurrentMonitorTargets = append([]string(nil), snapshot.CurrentMonitorTargets...)
	c.status.CircuitBreakerTripped = snapshot.FuseTripped
	if strings.TrimSpace(snapshot.FuseReason) != "" {
		c.status.CircuitBreakerReason = snapshot.FuseReason
	} else if !snapshot.FuseTripped {
		c.status.CircuitBreakerReason = ""
	}
	c.status.CooldownUntil = strings.TrimSpace(snapshot.CooldownUntil)
	c.status.LastOrderAt = strings.TrimSpace(snapshot.LastOrderAt)
	c.status.LastOrderAction = strings.TrimSpace(snapshot.LastOrderAction)
	c.status.LastRecoveryAt = strings.TrimSpace(snapshot.LastRecoveryAt)
	c.status.LastRecoveryAction = strings.TrimSpace(snapshot.LastRecoveryAction)
	c.status.LastManagedDrawdownAt = strings.TrimSpace(snapshot.LastManagedDrawdownAt)
	c.status.LastManagedDrawdownAction = strings.TrimSpace(snapshot.LastManagedDrawdownAction)
	c.status.ManagedDrawdownSummary = strings.TrimSpace(snapshot.ManagedDrawdownSummary)
	c.status.HoldMonitorSummary = strings.TrimSpace(snapshot.HoldMonitorSummary)
	c.status.LastUncertainOrder = strings.TrimSpace(snapshot.LastUncertainOrder)
	c.status.RemainingIncidentBudget = snapshot.RemainingIncidentBudget
	c.status.RemainingRecoveryBudget = snapshot.RemainingRecoveryBudget
	c.status.RemainingUncertaintyBudget = snapshot.RemainingUncertaintyBudget
	c.status.RemainingRejectBudget = snapshot.RemainingRejectBudget
	c.status.RemainingErrorBudget = snapshot.RemainingErrorBudget
	c.status.ProtectedIOCAttempts = snapshot.ProtectedIOCAttempts
	c.status.ProtectedIOCCanceled = snapshot.ProtectedIOCCanceled
	c.status.ProtectedIOCExpired = snapshot.ProtectedIOCExpired
	c.status.ProtectedIOCPartial = snapshot.ProtectedIOCPartial
	c.status.ProtectedIOCAvgUnfilledRatio = snapshot.ProtectedIOCAvgUnfilledRatio
	c.status.ProtectedIOCCancelRate = snapshot.ProtectedIOCCancelRate
	c.status.HealthStatus = strings.TrimSpace(snapshot.HealthStatus)
	c.status.HealthSummary = strings.TrimSpace(snapshot.HealthSummary)
	c.status.HealthScore = snapshot.HealthScore

	oldGear := c.status.EngineGear
	c.status.EngineGear = snapshot.EngineGear
	if oldGear != "" && oldGear != snapshot.EngineGear {
		if c.store != nil {
			event := DomainEvent{
				Type:       EventTypeEngineGearChanged,
				Level:      EventLevelWarning,
				OccurredAt: time.Now().UTC(),
				RunID:      c.status.RunID,
				CycleID:    c.status.CurrentCycleID,
				Protocol:   "RiskSupervisor",
				Stage:      LifecycleStage(c.status.CurrentStage),
				Successful: true,
				Reason:     fmt.Sprintf("EngineGear %s -> %s", oldGear, snapshot.EngineGear),
			}
			_ = c.store.AppendDomainEvents([]DomainEvent{event})
		}
	}

	c.status.CanContinue = snapshot.CanContinue
	c.status.ConsecutiveRejects = snapshot.ConsecutiveRejects
	c.status.ConsecutiveErrors = snapshot.ConsecutiveErrors
	c.status.CycleOpenNotionalUSDT = snapshot.CycleOpenNotionalUSDT
	c.status.RemainingNotionalBudget = snapshot.RemainingNotionalBudget
	c.status.LastExceptionCategory = strings.TrimSpace(snapshot.LastExceptionCategory)
	c.status.LastExceptionSeverity = strings.TrimSpace(snapshot.LastExceptionSeverity)
	c.status.LastExceptionDetail = strings.TrimSpace(snapshot.LastExceptionDetail)
	c.status.CurrentCooldownCategory = strings.TrimSpace(snapshot.CurrentCooldownCategory)
	c.status.CurrentCooldownSeverity = strings.TrimSpace(snapshot.CurrentCooldownSeverity)
	c.status.CooledSymbols = append([]string(nil), snapshot.CooledSymbols...)
	c.status.EmergencyStopTriggered = snapshot.EmergencyStopTriggered
	c.status.EmergencyStopReason = strings.TrimSpace(snapshot.EmergencyStopReason)
	c.status.DailyRiskLimitTriggered = snapshot.DailyRiskLimitTriggered
	c.status.DailyRiskLimitReason = strings.TrimSpace(snapshot.DailyRiskLimitReason)
	c.status.DailyRealizedPnL = snapshot.DailyRealizedPnL
	c.status.DailyOrderCount = snapshot.DailyOrderCount
	c.status.DailyOpenNotionalUSDT = snapshot.DailyOpenNotionalUSDT
	c.status.RemainingDailyLossBudget = snapshot.RemainingDailyLossBudget
	c.status.RemainingDailyOrderBudget = snapshot.RemainingDailyOrderBudget
	c.status.RemainingDailyNotionalBudget = snapshot.RemainingDailyNotionalBudget
}

func (c *RuntimeController) updateMonthlyProgress(stage EngineStage, message string, current int, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.status.Running || c.status.Mode != string(RuntimeModeMonthly) {
		return
	}
	c.setStageLocked(stage, strings.TrimSpace(message), "monthly-simulation")
	c.status.ProgressCurrent = current
	c.status.ProgressTotal = total
	if strings.TrimSpace(message) != "" {
		c.status.LastMessage = message
	}
}

func (c *RuntimeController) setStageLocked(stage EngineStage, reason string, source string) {
	stage = NormalizeEngineStage(stage)
	if stage == "" {
		return
	}
	current := NormalizeEngineStage(EngineStage(c.status.CurrentStage))
	reason = strings.TrimSpace(reason)
	if current == "" {
		transition, err := NewEngineStageTransition(current, stage, reason)
		if err != nil {
			return
		}
		c.status.CurrentStage = string(transition.To)
		c.status.StageReason = transition.Reason
		c.status.StageTransitionedAt = transition.Transitioned
		c.appendStageTransitionLogLocked(source, transition)
		return
	}
	if current == stage {
		if reason != "" {
			c.status.StageReason = reason
		}
		if strings.TrimSpace(c.status.StageTransitionedAt) == "" {
			c.status.StageTransitionedAt = time.Now().UTC().Format(time.RFC3339)
		}
		return
	}
	transition, err := NewEngineStageTransition(current, stage, reason)
	if err != nil {
		c.appendStageTransitionLogLocked(source, EngineStageTransition{
			From:   current,
			To:     stage,
			Reason: fmt.Sprintf("ignored_invalid_transition: %v", err),
		})
		return
	}
	c.status.CurrentStage = string(transition.To)
	c.status.StageReason = transition.Reason
	c.status.StageTransitionedAt = transition.Transitioned
	c.appendStageTransitionLogLocked(source, transition)
}

func (c *RuntimeController) runLiveEngine(ctx context.Context, secretPath string, cfg EngineConfig) (string, error) {
	c.appendControllerLog("开始启动实盘引擎")
	// S7-A:抢单写租约,防止同一账户双开(双写撞单/对账错乱)。flock 在进程退出时由内核自动释放,无陈旧锁问题。
	lease, err := acquireSingleWriterLease(cfg.NetworkConfig.RecordDir)
	if err != nil {
		c.appendControllerLog("单写租约获取失败: %v", err)
		return "", err
	}
	defer func() {
		if rerr := lease.Release(); rerr != nil {
			c.appendControllerLog("释放单写租约失败: %v", rerr)
		} else {
			c.appendControllerLog("已释放单写租约")
		}
	}()
	c.appendControllerLog("已获取单写租约,确保单实例独占写入")
	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		c.appendControllerLog("创建实盘引擎失败: %v", err)
		return "", err
	}
	engine.runID = c.currentRunID()
	engine.setTraceLogIDs(engine.runID, "")
	engine.reportStore = c.store
	engine.logger = log.New(newRuntimeLogWriter(c.logs, "live-engine", os.Stdout, engine.traceIDs, engine.recordMetadata), "[FundingRateSniper] ", log.LstdFlags|log.Lmicroseconds)
	engine.executionEventHook = c.recordExecutionEvent
	engine.liveStatusHook = c.recordLiveEngineStatus

	if err := engine.Start(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			c.appendControllerLog("实盘引擎已停止")
			return "实盘引擎已停止", err
		}
		c.appendControllerLog("实盘引擎异常退出: %v", err)
		return "", err
	}
	c.appendControllerLog("实盘引擎已正常退出")
	return "实盘引擎已正常退出", nil
}

func (c *RuntimeController) runMonthlySimulation(ctx context.Context, secretPath string, lookbackDays int, cfg EngineConfig) (string, error) {
	c.appendControllerLog("开始执行历史模拟，lookbackDays=%d", lookbackDays)
	runner := NewBacktestRunner(c.store, log.New(newRuntimeLogWriter(c.logs, "monthly-simulation", os.Stdout, c.currentTraceIDs, c.currentRecordMetadata), "[MonthlySimulation] ", log.LstdFlags|log.Lmicroseconds), cfg)
	runner.progressHook = c.updateMonthlyProgress
	records, err := runner.RunMonthlyMarketSimulation(ctx, secretPath, lookbackDays)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			c.appendControllerLog("历史模拟已停止，未写入本轮汇总")
			return "历史模拟已停止，未写入本轮汇总", err
		}
		c.appendControllerLog("历史模拟失败: %v", err)
		return "", err
	}
	message := fmt.Sprintf("历史模拟完成，汇总记录=%d，详细日志=%s", len(records), filepath.Join(c.store.RecordDir(), monthlySimulationLogCSVName))
	c.appendControllerLog("%s", message)
	return message, nil
}

func (c *RuntimeController) appendControllerLog(format string, args ...any) {
	c.appendControllerTraceLog(c.currentRunID(), c.currentCycleID(), format, args...)
}

func (c *RuntimeController) appendControllerTraceLog(runID string, cycleID string, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	record := RuntimeLogRecord{
		TraceRecordMetadata: buildTraceRecordMetadata(c.cfg),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		RunID:               strings.TrimSpace(runID),
		CycleID:             strings.TrimSpace(cycleID),
		Source:              "controller",
		Message:             message,
	}
	if err := c.logs.Append(record); err != nil && c.logger != nil {
		c.logger.Printf("写入控制器日志失败: %v", err)
	}
	if c.logger != nil {
		c.logger.Printf("%s", message)
	}
}

func (c *RuntimeController) currentRunID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.status.RunID)
}

func (c *RuntimeController) currentCycleID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.status.CurrentCycleID)
}

func (c *RuntimeController) currentTraceIDs() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(c.status.RunID), strings.TrimSpace(c.status.CurrentCycleID)
}

func (c *RuntimeController) currentRecordMetadata() TraceRecordMetadata {
	c.mu.Lock()
	defer c.mu.Unlock()
	return buildTraceRecordMetadata(c.cfg)
}

func (c *RuntimeController) appendStageTransitionLogLocked(source string, transition EngineStageTransition) {
	if c == nil || c.logs == nil {
		return
	}
	if strings.TrimSpace(string(transition.To)) == "" {
		return
	}
	record := RuntimeLogRecord{
		TraceRecordMetadata: buildTraceRecordMetadata(c.cfg),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		RunID:               strings.TrimSpace(c.status.RunID),
		CycleID:             strings.TrimSpace(c.status.CurrentCycleID),
		Source:              nonEmpty(strings.TrimSpace(source), "runtime-state"),
		Message:             fmt.Sprintf("状态迁移: %s -> %s | reason=%s", nonEmpty(string(transition.From), "-"), transition.To, nonEmpty(strings.TrimSpace(transition.Reason), "-")),
	}
	if err := c.logs.Append(record); err != nil && c.logger != nil {
		c.logger.Printf("写入状态迁移日志失败: %v", err)
	}

	if c.store != nil {
		event := DomainEvent{
			Type:       EventTypeStageTransition,
			Level:      EventLevelInfo,
			OccurredAt: time.Now().UTC(),
			RunID:      c.status.RunID,
			CycleID:    c.status.CurrentCycleID,
			Protocol:   "RuntimeController",
			Stage:      LifecycleStage(transition.To),
			Successful: transition.To != StageFailed && transition.To != StageStopped,
			Reason:     fmt.Sprintf("%s -> %s (%s)", transition.From, transition.To, transition.Reason),
		}
		if transition.To == StageFailed || transition.To == StageStopped {
			event.Level = EventLevelWarning
		}
		_ = c.store.AppendDomainEvents([]DomainEvent{event})
	}
}
