package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
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
	LastUncertainOrder           string                    `json:"last_uncertain_order"`
	RemainingIncidentBudget      int                       `json:"remaining_incident_budget"`
	RemainingRecoveryBudget      int                       `json:"remaining_recovery_budget"`
	RemainingUncertaintyBudget   int                       `json:"remaining_uncertainty_budget"`
	RemainingRejectBudget        int                       `json:"remaining_reject_budget"`
	RemainingErrorBudget         int                       `json:"remaining_error_budget"`
	HealthStatus                 string                    `json:"health_status"`
	HealthSummary                string                    `json:"health_summary"`
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
	LastUncertainOrder           string
	RemainingIncidentBudget      int
	RemainingRecoveryBudget      int
	RemainingUncertaintyBudget   int
	RemainingRejectBudget        int
	RemainingErrorBudget         int
	HealthStatus                 string
	HealthSummary                string
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

	mu     sync.Mutex
	status RuntimeTaskStatus
	cancel context.CancelFunc
}

// RuntimeLogStore 负责把后台运行日志保存到 jsonl 文件，并支持页面读取最近日志。
type RuntimeLogStore struct {
	path           string
	currentDay     string
	mu             sync.Mutex
	lastCleanupAt  time.Time
	cleanupRunning bool
	records        []RuntimeLogRecord
}

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

func NewRuntimeLogStore(path string) (*RuntimeLogStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建运行日志目录失败: %w", err)
	}
	now := time.Now().UTC()
	file, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("初始化运行日志文件失败: %w", err)
	}
	_ = file.Close()
	store := &RuntimeLogStore{path: path, currentDay: runtimeLogDay(now)}
	if existingDay, ok := detectRuntimeLogBaseFileDay(path); ok && existingDay != "" && existingDay != store.currentDay {
		if err := store.archiveBaseFileToDayLocked(existingDay); err != nil {
			return nil, err
		}
	}
	if err := store.reloadRecordsLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *RuntimeLogStore) Path() string {
	return s.path
}

func (s *RuntimeLogStore) Append(record RuntimeLogRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if err := s.rotateIfNeededLocked(now); err != nil {
		return err
	}

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开运行日志失败: %w", err)
	}
	defer file.Close()

	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化运行日志失败: %w", err)
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("写入运行日志失败: %w", err)
	}
	s.records = append(s.records, record)
	s.trimRecordsLocked(now)
	s.maybeStartCleanupLocked(now)
	return nil
}

func (s *RuntimeLogStore) List(limit int) ([]RuntimeLogRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-runtimeLogRetention)
	s.trimRecordsLocked(time.Now().UTC())
	result := append([]RuntimeLogRecord(nil), s.records...)
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	for len(result) > 0 {
		recordedAt, ok := parseRuntimeLogRecordedAt(result[0].RecordedAt)
		if !ok || !recordedAt.Before(cutoff) {
			break
		}
		result = result[1:]
	}
	return result, nil
}

func (s *RuntimeLogStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths, err := s.listRuntimeLogFilesLocked()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == s.path {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("删除轮转运行日志失败: %w", removeErr)
		}
	}
	file, err := os.Create(s.path)
	if err != nil {
		return fmt.Errorf("清空运行日志失败: %w", err)
	}
	s.records = nil
	s.currentDay = runtimeLogDay(time.Now().UTC())
	return file.Close()
}

func (s *RuntimeLogStore) maybeStartCleanupLocked(now time.Time) {
	if s.cleanupRunning {
		return
	}
	if !s.lastCleanupAt.IsZero() && now.Sub(s.lastCleanupAt) < runtimeLogCleanupInterval {
		return
	}
	s.cleanupRunning = true
	s.lastCleanupAt = now
	go s.cleanupExpired(now)
}

func (s *RuntimeLogStore) cleanupExpired(now time.Time) {
	cutoff := now.Add(-runtimeLogRetention)

	s.mu.Lock()
	defer func() {
		s.cleanupRunning = false
		s.mu.Unlock()
	}()
	_ = s.removeExpiredRotatedFilesLocked(cutoff)
	s.trimRecordsLocked(now)
}

func (s *RuntimeLogStore) reloadRecordsLocked() error {
	records := make([]RuntimeLogRecord, 0, 256)
	cutoff := time.Now().UTC().Add(-runtimeLogRetention)
	paths, err := s.listRuntimeLogFilesLocked()
	if err != nil {
		return err
	}
	for _, logPath := range paths {
		file, err := os.Open(logPath)
		if err != nil {
			return fmt.Errorf("打开运行日志失败: %w", err)
		}
		scanner := newRuntimeLogScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record RuntimeLogRecord
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt); ok && recordedAt.Before(cutoff) {
				continue
			}
			records = append(records, record)
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = file.Close()
			return fmt.Errorf("读取运行日志失败: %w", scanErr)
		}
		_ = file.Close()
	}
	s.records = records
	s.trimRecordsLocked(time.Now().UTC())
	return nil
}

func (s *RuntimeLogStore) trimRecordsLocked(now time.Time) {
	cutoff := now.Add(-runtimeLogRetention)
	if len(s.records) > 0 {
		filtered := s.records[:0]
		for _, record := range s.records {
			if recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt); ok && recordedAt.Before(cutoff) {
				continue
			}
			filtered = append(filtered, record)
		}
		s.records = filtered
	}
	if len(s.records) > runtimeLogMemoryLimit {
		s.records = append([]RuntimeLogRecord(nil), s.records[len(s.records)-runtimeLogMemoryLimit:]...)
	}
}

func newRuntimeLogScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), runtimeLogScannerBuffer)
	return scanner
}

func parseRuntimeLogRecordedAt(raw string) (time.Time, bool) {
	recordedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, false
	}
	return recordedAt.UTC(), true
}

func runtimeLogDay(now time.Time) string {
	return now.UTC().Format("20060102")
}

func detectRuntimeLogBaseFileDay(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := newRuntimeLogScanner(file)
	lastDay := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record RuntimeLogRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt)
		if !ok {
			continue
		}
		lastDay = runtimeLogDay(recordedAt)
	}
	if lastDay != "" {
		return lastDay, true
	}
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	return runtimeLogDay(info.ModTime().UTC()), true
}

func (s *RuntimeLogStore) rotateIfNeededLocked(now time.Time) error {
	currentDay := runtimeLogDay(now)
	if s.currentDay == "" {
		s.currentDay = currentDay
	}
	if s.currentDay == currentDay {
		return nil
	}
	if err := s.archiveBaseFileToDayLocked(s.currentDay); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("创建新运行日志文件失败: %w", err)
	}
	_ = file.Close()
	s.currentDay = currentDay
	return nil
}

func runtimeLogRotatedPath(basePath string, day string) string {
	ext := filepath.Ext(basePath)
	name := strings.TrimSuffix(filepath.Base(basePath), ext)
	if ext == "" {
		ext = ".jsonl"
	}
	return filepath.Join(filepath.Dir(basePath), fmt.Sprintf("%s_%s%s", name, day, ext))
}

func (s *RuntimeLogStore) listRuntimeLogFilesLocked() ([]string, error) {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "_"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取运行日志目录失败: %w", err)
	}
	paths := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	paths = append(paths, s.path)
	sort.Strings(paths)
	return paths, nil
}

func (s *RuntimeLogStore) removeExpiredRotatedFilesLocked(cutoff time.Time) error {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "_"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoffDay := runtimeLogDay(cutoff)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext)
		if day >= cutoffDay {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

func (s *RuntimeLogStore) archiveBaseFileToDayLocked(day string) error {
	day = strings.TrimSpace(day)
	if day == "" {
		return nil
	}
	info, err := os.Stat(s.path)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取当前运行日志失败: %w", err)
	}
	rotatedPath := runtimeLogRotatedPath(s.path, day)
	if _, err := os.Stat(rotatedPath); os.IsNotExist(err) {
		if renameErr := os.Rename(s.path, rotatedPath); renameErr != nil && !os.IsNotExist(renameErr) {
			return fmt.Errorf("轮转运行日志失败: %w", renameErr)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("检查轮转运行日志失败: %w", err)
	}

	src, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("打开当前运行日志失败: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(rotatedPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开轮转运行日志失败: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("合并轮转运行日志失败: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("关闭轮转运行日志失败: %w", err)
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧运行日志失败: %w", err)
	}
	return nil
}

func (c *RuntimeController) Start(req RuntimeStartRequest) error {
	mode := RuntimeMode(strings.TrimSpace(req.Mode))
	if mode != RuntimeModeLive && mode != RuntimeModeMonthly {
		return fmt.Errorf("不支持的运行模式: %s", req.Mode)
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

	if mode == RuntimeModeLive {
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

	c.appendControllerTraceLog(runID, "", "已接收运行请求，mode=%s secret=%s lookbackDays=%d", mode, secretPath, lookbackDays)
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

func (c *RuntimeController) CurrentRiskConfig() RuntimeRiskConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return runtimeRiskConfigFromEngineConfig(c.cfg)
}

func (c *RuntimeController) UpdateRiskConfig(req RuntimeRiskConfig) (RuntimeRiskConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	window, err := parseRuntimeWindowDuration(req.RecentTradeVolumeWindow, c.cfg.StrategyConfig.RecentTradeVolumeWindow)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if req.PerSymbolQuoteUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("单标的名义资金必须大于 0")
	}
	if req.MaxCycleOpenNotionalUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("单周期总敞口上限必须大于 0")
	}
	if req.MaxRecentVolumeShare <= 0 || req.MaxRecentVolumeShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("最近成交额占比必须在 (0, 1] 之间")
	}
	if req.MaxOpenInterestShare <= 0 || req.MaxOpenInterestShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("未平仓名义占比必须在 (0, 1] 之间")
	}
	if req.MaxVisibleDepthShare <= 0 || req.MaxVisibleDepthShare > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("可见深度占比必须在 (0, 1] 之间")
	}
	if req.MinNetProfitRate <= 0 || req.MinNetProfitRate > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("最低净收益率必须在 (0, 1] 之间")
	}
	if req.ImmediateInvalidateRate <= 0 || req.ImmediateInvalidateRate > 1 {
		return RuntimeRiskConfig{}, fmt.Errorf("立即作废阈值必须在 (0, 1] 之间")
	}
	if req.ImmediateInvalidateRate >= req.MinNetProfitRate {
		return RuntimeRiskConfig{}, fmt.Errorf("立即作废阈值必须小于最低净收益率")
	}
	if req.MonitorMaxConsecutiveBreach <= 0 || req.MonitorMaxConsecutiveBreach > 20 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续观察 breach 必须在 1 到 20 之间")
	}
	if req.MaxCycleLossUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大单周期亏损必须大于 0")
	}
	if req.MaxExecutionIncidents <= 0 || req.MaxExecutionIncidents > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大执行事故数必须在 1 到 100 之间")
	}
	if req.MaxOrderUncertaintyPerCycle <= 0 || req.MaxOrderUncertaintyPerCycle > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大不确定成交数必须在 1 到 100 之间")
	}
	if req.MaxConsecutivePlatformRejects <= 0 || req.MaxConsecutivePlatformRejects > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续平台拒绝数必须在 1 到 100 之间")
	}
	if req.MaxConsecutiveRequestErrors <= 0 || req.MaxConsecutiveRequestErrors > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大连续异常请求数必须在 1 到 100 之间")
	}
	confirmTimeout, err := parseRuntimeFlexibleDuration(req.ExecutionConfirmTimeout, c.cfg.ExecutionConfig.ExecutionConfirmTimeout, "开平仓确认超时", 100*time.Millisecond, 30*time.Second)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	symbolCooldown, err := parseRuntimeFlexibleDuration(req.SymbolAbnormalCooldown, c.cfg.RiskConfig.SymbolAbnormalCooldown, "symbol 异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	minorCooldown, err := parseRuntimeFlexibleDuration(req.PlatformMinorCooldown, c.cfg.RiskConfig.PlatformMinorCooldown, "轻微异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if req.MaxRecoveryActionsPerCycle <= 0 || req.MaxRecoveryActionsPerCycle > 100 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大回补次数必须在 1 到 100 之间")
	}
	if req.MaxDailyLossUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日亏损必须大于 0")
	}
	if req.MaxDailyOrderCount <= 0 || req.MaxDailyOrderCount > 10000 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日下单次数必须在 1 到 10000 之间")
	}
	if req.MaxDailyOpenNotionalUSDT <= 0 {
		return RuntimeRiskConfig{}, fmt.Errorf("最大日开仓名义必须大于 0")
	}
	cooldown, err := parseRuntimeCooldownDuration(req.PlatformAbnormalCooldown, c.cfg.RiskConfig.PlatformAbnormalCooldown)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	criticalCooldown, err := parseRuntimeFlexibleDuration(req.PlatformCriticalCooldown, c.cfg.RiskConfig.PlatformCriticalCooldown, "严重异常冷却时间", time.Second, 24*time.Hour)
	if err != nil {
		return RuntimeRiskConfig{}, err
	}
	if minorCooldown > cooldown {
		return RuntimeRiskConfig{}, fmt.Errorf("轻微异常冷却时间不能大于平台异常冷却时间")
	}
	if criticalCooldown < cooldown {
		return RuntimeRiskConfig{}, fmt.Errorf("严重异常冷却时间不能小于平台异常冷却时间")
	}

	updated := c.cfg
	updated.StrategyConfig.PerSymbolQuoteUSDT = req.PerSymbolQuoteUSDT
	updated.RiskConfig.MaxCycleOpenNotionalUSDT = req.MaxCycleOpenNotionalUSDT
	updated.StrategyConfig.RecentTradeVolumeWindow = window
	updated.StrategyConfig.MaxRecentVolumeShare = req.MaxRecentVolumeShare
	updated.StrategyConfig.MaxOpenInterestShare = req.MaxOpenInterestShare
	updated.StrategyConfig.MaxVisibleDepthShare = req.MaxVisibleDepthShare
	updated.RiskConfig.MinNetProfitRate = req.MinNetProfitRate
	updated.RiskConfig.ImmediateInvalidateRate = req.ImmediateInvalidateRate
	updated.RiskConfig.MonitorMaxConsecutiveBreach = req.MonitorMaxConsecutiveBreach
	updated.RiskConfig.MaxCycleLossUSDT = req.MaxCycleLossUSDT
	updated.RiskConfig.MaxExecutionIncidents = req.MaxExecutionIncidents
	updated.RiskConfig.MaxOrderUncertaintyPerCycle = req.MaxOrderUncertaintyPerCycle
	updated.RiskConfig.MaxConsecutivePlatformRejects = req.MaxConsecutivePlatformRejects
	updated.RiskConfig.MaxConsecutiveRequestErrors = req.MaxConsecutiveRequestErrors
	updated.ExecutionConfig.ExecutionConfirmTimeout = confirmTimeout
	updated.RiskConfig.SymbolAbnormalCooldown = symbolCooldown
	updated.RiskConfig.PlatformMinorCooldown = minorCooldown
	updated.RiskConfig.PlatformAbnormalCooldown = cooldown
	updated.RiskConfig.PlatformCriticalCooldown = criticalCooldown
	updated.RiskConfig.MaxRecoveryActionsPerCycle = req.MaxRecoveryActionsPerCycle
	updated.RiskConfig.MaxDailyLossUSDT = req.MaxDailyLossUSDT
	updated.RiskConfig.MaxDailyOrderCount = req.MaxDailyOrderCount
	updated.RiskConfig.MaxDailyOpenNotionalUSDT = req.MaxDailyOpenNotionalUSDT
	if err := updated.Validate(); err != nil {
		return RuntimeRiskConfig{}, err
	}
	c.cfg = updated

	config := runtimeRiskConfigFromEngineConfig(updated)
	c.status.LastMessage = "风控参数已更新，将在下一次启动任务时生效"
	return config, nil
}

func runtimeRiskConfigFromEngineConfig(cfg EngineConfig) RuntimeRiskConfig {
	return RuntimeRiskConfig{
		PerSymbolQuoteUSDT:            cfg.StrategyConfig.PerSymbolQuoteUSDT,
		MaxCycleOpenNotionalUSDT:      cfg.RiskConfig.MaxCycleOpenNotionalUSDT,
		RecentTradeVolumeWindow:       formatRuntimeRiskDuration(cfg.StrategyConfig.RecentTradeVolumeWindow),
		MaxRecentVolumeShare:          cfg.StrategyConfig.MaxRecentVolumeShare,
		MaxOpenInterestShare:          cfg.StrategyConfig.MaxOpenInterestShare,
		MaxVisibleDepthShare:          cfg.StrategyConfig.MaxVisibleDepthShare,
		MinNetProfitRate:              cfg.RiskConfig.MinNetProfitRate,
		ImmediateInvalidateRate:       cfg.RiskConfig.ImmediateInvalidateRate,
		MonitorMaxConsecutiveBreach:   cfg.RiskConfig.MonitorMaxConsecutiveBreach,
		MaxCycleLossUSDT:              cfg.RiskConfig.MaxCycleLossUSDT,
		MaxExecutionIncidents:         cfg.RiskConfig.MaxExecutionIncidents,
		MaxOrderUncertaintyPerCycle:   cfg.RiskConfig.MaxOrderUncertaintyPerCycle,
		MaxConsecutivePlatformRejects: cfg.RiskConfig.MaxConsecutivePlatformRejects,
		MaxConsecutiveRequestErrors:   cfg.RiskConfig.MaxConsecutiveRequestErrors,
		ExecutionConfirmTimeout:       formatRuntimeRiskDuration(cfg.ExecutionConfig.ExecutionConfirmTimeout),
		SymbolAbnormalCooldown:        formatRuntimeRiskDuration(cfg.RiskConfig.SymbolAbnormalCooldown),
		PlatformMinorCooldown:         formatRuntimeRiskDuration(cfg.RiskConfig.PlatformMinorCooldown),
		PlatformAbnormalCooldown:      formatRuntimeRiskDuration(cfg.RiskConfig.PlatformAbnormalCooldown),
		PlatformCriticalCooldown:      formatRuntimeRiskDuration(cfg.RiskConfig.PlatformCriticalCooldown),
		MaxRecoveryActionsPerCycle:    cfg.RiskConfig.MaxRecoveryActionsPerCycle,
		MaxDailyLossUSDT:              cfg.RiskConfig.MaxDailyLossUSDT,
		MaxDailyOrderCount:            cfg.RiskConfig.MaxDailyOrderCount,
		MaxDailyOpenNotionalUSDT:      cfg.RiskConfig.MaxDailyOpenNotionalUSDT,
	}
}

func parseRuntimeWindowDuration(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if fallback > 0 {
			return fallback, nil
		}
		return 0, fmt.Errorf("最近成交额窗口不能为空")
	}
	window, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("最近成交额窗口格式错误: %w", err)
	}
	if window < time.Minute || window > time.Hour {
		return 0, fmt.Errorf("最近成交额窗口必须在 1m 到 1h 之间")
	}
	return window, nil
}

func parseRuntimeCooldownDuration(raw string, fallback time.Duration) (time.Duration, error) {
	return parseRuntimeFlexibleDuration(raw, fallback, "平台异常冷却时间", time.Second, 24*time.Hour)
}

func parseRuntimeFlexibleDuration(raw string, fallback time.Duration, fieldName string, min time.Duration, max time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if fallback > 0 {
			return fallback, nil
		}
		return 0, fmt.Errorf("%s不能为空", fieldName)
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s格式错误: %w", fieldName, err)
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s必须在 %s 到 %s 之间", fieldName, min, max)
	}
	return value, nil
}

func formatRuntimeRiskDuration(window time.Duration) string {
	if window <= 0 {
		return ""
	}
	return window.String()
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
		message, err = c.runLiveEngine(ctx, secretPath, cfg)
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
	c.status.LastUncertainOrder = strings.TrimSpace(snapshot.LastUncertainOrder)
	c.status.RemainingIncidentBudget = snapshot.RemainingIncidentBudget
	c.status.RemainingRecoveryBudget = snapshot.RemainingRecoveryBudget
	c.status.RemainingUncertaintyBudget = snapshot.RemainingUncertaintyBudget
	c.status.RemainingRejectBudget = snapshot.RemainingRejectBudget
	c.status.RemainingErrorBudget = snapshot.RemainingErrorBudget
	c.status.HealthStatus = strings.TrimSpace(snapshot.HealthStatus)
	c.status.HealthSummary = strings.TrimSpace(snapshot.HealthSummary)
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
	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		c.appendControllerLog("创建实盘引擎失败: %v", err)
		return "", err
	}
	engine.runID = c.currentRunID()
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

type runtimeLogWriter struct {
	store            *RuntimeLogStore
	source           string
	mirror           io.Writer
	traceIDProvider  func() (string, string)
	metadataProvider func() TraceRecordMetadata
	mu               sync.Mutex
	pending          string
}

func newRuntimeLogWriter(store *RuntimeLogStore, source string, mirror io.Writer, traceIDProvider func() (string, string), metadataProvider func() TraceRecordMetadata) *runtimeLogWriter {
	return &runtimeLogWriter{
		store:            store,
		source:           source,
		mirror:           mirror,
		traceIDProvider:  traceIDProvider,
		metadataProvider: metadataProvider,
	}
}

func (w *runtimeLogWriter) Write(p []byte) (int, error) {
	if w.mirror != nil {
		if _, err := w.mirror.Write(p); err != nil {
			return 0, err
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	data := w.pending + string(p)
	lines := strings.Split(data, "\n")
	if !strings.HasSuffix(data, "\n") {
		w.pending = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	} else {
		w.pending = ""
	}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		runID, cycleID := "", ""
		if w.traceIDProvider != nil {
			runID, cycleID = w.traceIDProvider()
		}
		metadata := TraceRecordMetadata{}
		if w.metadataProvider != nil {
			metadata = w.metadataProvider().normalized()
		}
		if err := w.store.Append(RuntimeLogRecord{
			TraceRecordMetadata: metadata,
			RecordedAt:          time.Now().UTC().Format(time.RFC3339),
			RunID:               strings.TrimSpace(runID),
			CycleID:             strings.TrimSpace(cycleID),
			Source:              w.source,
			Message:             line,
		}); err != nil {
			return 0, err
		}
	}
	return len(p), nil
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

func (c *RuntimeController) runLiveStartupChecks(ctx context.Context, secretPath string, cfg EngineConfig, runID string, cooldownUntil string) error {
	c.appendControllerTraceLog(runID, "", "开始执行实盘启动前检查")
	if err := validateLocalDashboardAddr(cfg.NetworkConfig.DashboardAddr); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: dashboard 仅监听本地地址")
	if err := ensureWritableDirectory(cfg.NetworkConfig.RecordDir); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: records 目录可写")
	if err := ensureEmergencyStopClear(effectiveEmergencyStopFile(cfg)); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: 未检测到 emergency stop 文件")
	if err := ensureCooldownExpired(cooldownUntil); err != nil {
		return err
	}
	if strings.TrimSpace(cooldownUntil) != "" {
		c.appendControllerTraceLog(runID, "", "启动前检查通过: 本地冷却期已结束")
	}

	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		return fmt.Errorf("创建实盘引擎失败: %w", err)
	}
	engine.runID = strings.TrimSpace(runID)
	engine.logger = log.New(newRuntimeLogWriter(c.logs, "live-preflight", os.Stdout, engine.traceIDs, engine.recordMetadata), "[LivePreflight] ", log.LstdFlags|log.Lmicroseconds)

	if err := engine.syncServerTimeOffset(ctx); err != nil {
		return fmt.Errorf("同步服务器时间失败: %w", err)
	}
	offset := engine.serverTimeOffset
	if offset < 0 {
		offset = -offset
	}
	if offset > maxServerTimeOffset {
		return fmt.Errorf("服务器时间偏移过大: %s，超过允许阈值 %s", offset, maxServerTimeOffset)
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: 服务器时间偏移=%s", engine.serverTimeOffset)
	if err := engine.verifyTradingEnvironment(ctx); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "实盘启动前检查已全部通过")
	return nil
}

func validateLocalDashboardAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("dashboard 地址不能为空")
	}
	host := addr
	if strings.Contains(addr, "://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return fmt.Errorf("dashboard 地址格式非法: %w", err)
		}
		host = parsed.Host
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else if addrErr, ok := err.(*net.AddrError); ok && !strings.Contains(addrErr.Err, "missing port in address") {
		return fmt.Errorf("dashboard 地址格式非法: %w", err)
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "127.0.0.1", "localhost", "::1":
		return nil
	default:
		return fmt.Errorf("dashboard 必须只监听本地地址，当前配置=%s", addr)
	}
}

func ensureWritableDirectory(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("records 目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 records 目录失败: %w", err)
	}
	tempFile, err := os.CreateTemp(dir, ".lingxi-write-check-*")
	if err != nil {
		return fmt.Errorf("records 目录不可写: %w", err)
	}
	path := tempFile.Name()
	if _, err := tempFile.WriteString("ok"); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(path)
		return fmt.Errorf("records 目录写入校验失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("records 目录关闭校验文件失败: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("records 目录清理校验文件失败: %w", err)
	}
	return nil
}

func ensureCooldownExpired(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("读取当前冷却时间失败: %w", err)
	}
	if until.After(time.Now().UTC()) {
		return fmt.Errorf("当前仍处于冷却期，截止到 %s", until.UTC().Format(time.RFC3339))
	}
	return nil
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
}
