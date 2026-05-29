package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	settlementCSVName           = "settlements.json"
	monitorCSVName              = "monitor_records.jsonl"
	backtestCSVName             = "backtest_records.json"
	backtestRunCSVName          = "backtest_run_summaries.json"
	residualCSVName             = "residual_risk_records.json"
	executionCSVName            = "execution_incidents.jsonl"
	executionIncidentCacheLimit = 5000
	reportCSVRetention          = 6 * time.Hour
	reportCSVMaxFileSizeBytes   = 8 << 20
)

const (
	legacySettlementCSVName  = "settlements.csv"
	legacyMonitorCSVName     = "monitor_records.csv"
	legacyBacktestCSVName    = "backtest_records.csv"
	legacyBacktestRunCSVName = "backtest_run_summaries.csv"
	legacyResidualCSVName    = "residual_risk_records.csv"
	legacyExecutionCSVName   = "execution_incidents.csv"
	legacyMonitorJSONName    = "monitor_records.json"
	legacyExecutionJSONName  = "execution_incidents.json"
)

var settlementCSVHeader = []string{
	"recorded_at",
	"funding_time",
	"close_executed_at",
	"targets",
	"funding_income",
	"expected_funding_income",
	"funding_income_diff",
	"spot_trading_pnl",
	"futures_trading_pnl",
	"total_fees",
	"net_pnl",
	"snapshot_net_pnl",
	"residual_spot_value",
	"spot_usdt_delta",
	"futures_usdt_delta",
	"has_alert",
	"alerts",
}

var monitorCSVHeader = []string{
	"recorded_at",
	"monitor_stop_at",
	"funding_time",
	"symbol",
	"pair",
	"state",
	"valid",
	"breach_count",
	"estimated_rate",
	"last_funding_rate",
	"basis_rate",
	"scan_score_rate",
	"last_net_profit",
	"last_spread",
	"invalid_reason",
}

var backtestCSVHeader = []string{
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"trades",
	"win_rate",
	"funding_income",
	"trading_pnl",
	"total_fees",
	"net_pnl",
	"max_drawdown",
	"notes",
}

var backtestRunCSVHeader = []string{
	"run_id",
	"run_mode",
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"trades",
	"win_rate",
	"funding_income",
	"trading_pnl",
	"total_fees",
	"net_pnl",
	"max_drawdown",
	"notes",
}

var residualCSVHeader = []string{
	"recorded_at",
	"scenario",
	"symbol",
	"period",
	"phase",
	"event_time",
	"monitor_state",
	"futures_leg_ok",
	"spot_leg_ok",
	"futures_fill_ratio",
	"spot_fill_ratio",
	"futures_filled_usdt",
	"spot_filled_usdt",
	"residual_usdt",
	"emergency_hedge_cost_usdt",
	"emergency_hedge_fee_usdt",
	"emergency_hedge_pnl_estimate",
	"severity",
	"reason",
	"notes",
}

var executionCSVHeader = []string{
	"recorded_at",
	"level",
	"category",
	"severity",
	"phase",
	"symbol",
	"action",
	"incident_count",
	"incident_limit",
	"fatal",
	"detail",
}

// SettlementCSVRecord 表示模块五单轮落盘记录。
type SettlementCSVRecord struct {
	TraceRecordMetadata
	RunID                 string `json:"run_id"`
	CycleID               string `json:"cycle_id"`
	RecordedAt            string `json:"recorded_at"`
	FundingTime           string `json:"funding_time"`
	CloseExecutedAt       string `json:"close_executed_at"`
	Targets               int    `json:"targets"`
	FundingIncome         string `json:"funding_income"`
	ExpectedFundingIncome string `json:"expected_funding_income"`
	FundingIncomeDiff     string `json:"funding_income_diff"`
	SpotTradingPnL        string `json:"spot_trading_pnl"`
	FuturesTradingPnL     string `json:"futures_trading_pnl"`
	TotalFees             string `json:"total_fees"`
	NetPnL                string `json:"net_pnl"`
	SnapshotNetPnL        string `json:"snapshot_net_pnl"`
	ResidualSpotValue     string `json:"residual_spot_value"`
	SpotUSDTDelta         string `json:"spot_usdt_delta"`
	FuturesUSDTDelta      string `json:"futures_usdt_delta"`
	HasAlert              bool   `json:"has_alert"`
	Alerts                string `json:"alerts"`
}

// MonitorCSVRecord 表示模块三单目标落盘记录。
type MonitorCSVRecord struct {
	TraceRecordMetadata
	RunID           string `json:"run_id"`
	CycleID         string `json:"cycle_id"`
	RecordedAt      string `json:"recorded_at"`
	MonitorStopAt   string `json:"monitor_stop_at"`
	FundingTime     string `json:"funding_time"`
	Symbol          string `json:"symbol"`
	Pair            string `json:"pair"`
	State           string `json:"state"`
	Valid           bool   `json:"valid"`
	BreachCount     int    `json:"breach_count"`
	EstimatedRate   string `json:"estimated_rate"`
	LastFundingRate string `json:"last_funding_rate"`
	BasisRate       string `json:"basis_rate"`
	ScanScoreRate   string `json:"scan_score_rate"`
	LastNetProfit   string `json:"last_net_profit"`
	LastSpread      string `json:"last_spread"`
	InvalidReason   string `json:"invalid_reason"`
}

// BacktestCSVRecord 表示回测结果的单行记录。
type BacktestCSVRecord struct {
	TraceRecordMetadata
	RecordedAt    string `json:"recorded_at"`
	Scenario      string `json:"scenario"`
	Symbol        string `json:"symbol"`
	Period        string `json:"period"`
	Trades        int    `json:"trades"`
	WinRate       string `json:"win_rate"`
	FundingIncome string `json:"funding_income"`
	TradingPnL    string `json:"trading_pnl"`
	TotalFees     string `json:"total_fees"`
	NetPnL        string `json:"net_pnl"`
	MaxDrawdown   string `json:"max_drawdown"`
	Notes         string `json:"notes"`
}

// BacktestRunSummaryRecord 表示按运行批次归档的回测摘要记录。
type BacktestRunSummaryRecord struct {
	TraceRecordMetadata
	RunID         string `json:"run_id"`
	RunMode       string `json:"run_mode"`
	RecordedAt    string `json:"recorded_at"`
	Scenario      string `json:"scenario"`
	Symbol        string `json:"symbol"`
	Period        string `json:"period"`
	Trades        int    `json:"trades"`
	WinRate       string `json:"win_rate"`
	FundingIncome string `json:"funding_income"`
	TradingPnL    string `json:"trading_pnl"`
	TotalFees     string `json:"total_fees"`
	NetPnL        string `json:"net_pnl"`
	MaxDrawdown   string `json:"max_drawdown"`
	Notes         string `json:"notes"`
}

// ResidualRiskCSVRecord 表示回放中产生的残腿/数量不一致风险记录。
type ResidualRiskCSVRecord struct {
	RecordedAt         string `json:"recorded_at"`
	Scenario           string `json:"scenario"`
	Symbol             string `json:"symbol"`
	Period             string `json:"period"`
	Phase              string `json:"phase"`
	EventTime          string `json:"event_time"`
	MonitorState       string `json:"monitor_state"`
	FuturesLegOK       bool   `json:"futures_leg_ok"`
	SpotLegOK          bool   `json:"spot_leg_ok"`
	FuturesFillRatio   string `json:"futures_fill_ratio"`
	SpotFillRatio      string `json:"spot_fill_ratio"`
	FuturesFilledUSDT  string `json:"futures_filled_usdt"`
	SpotFilledUSDT     string `json:"spot_filled_usdt"`
	ResidualUSDT       string `json:"residual_usdt"`
	EmergencyHedgeCost string `json:"emergency_hedge_cost_usdt"`
	EmergencyHedgeFee  string `json:"emergency_hedge_fee_usdt"`
	EmergencyHedgePnL  string `json:"emergency_hedge_pnl_estimate"`
	Severity           string `json:"severity"`
	Reason             string `json:"reason"`
	Notes              string `json:"notes"`
}

// ResidualSeverityStats 表示残腿风险的分级统计。
type ResidualSeverityStats struct {
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

// ResidualScenarioStats 表示按场景聚合的残腿风险统计。
type ResidualScenarioStats struct {
	Scenario            string `json:"scenario"`
	Count               int    `json:"count"`
	AverageResidualUSDT string `json:"average_residual_usdt"`
	WorstEmergencyPnL   string `json:"worst_emergency_hedge_pnl"`
	HighRiskCount       int    `json:"high_risk_count"`
}

// ExecutionIncidentRecord 表示实盘执行事故、恢复动作与熔断事件。
type ExecutionIncidentRecord struct {
	TraceRecordMetadata
	RunID         string `json:"run_id"`
	CycleID       string `json:"cycle_id"`
	RecordedAt    string `json:"recorded_at"`
	Level         string `json:"level"`
	Category      string `json:"category"`
	Severity      string `json:"severity"`
	Phase         string `json:"phase"`
	Symbol        string `json:"symbol"`
	Action        string `json:"action"`
	IncidentCount int    `json:"incident_count"`
	IncidentLimit int    `json:"incident_limit"`
	Fatal         bool   `json:"fatal"`
	Detail        string `json:"detail"`
}

// ReportStore 负责 JSON 落盘与读取。
type ReportStore struct {
	recordDir       string
	settlementPath  string
	monitorPath     string
	backtestPath    string
	backtestRunPath string
	residualPath    string
	executionPath   string
	monthlyPath     string
	mu              sync.Mutex

	settlementRecords     []SettlementCSVRecord
	monitorRecords        []MonitorCSVRecord
	backtestRecords       []BacktestCSVRecord
	residualRiskRecords   []ResidualRiskCSVRecord
	executionIncidents    []ExecutionIncidentRecord
	residualSeverityStats []ResidualSeverityStats
	residualScenarioStats []ResidualScenarioStats
}

// NewReportStore 初始化记录目录和 JSON 记录文件。
func NewReportStore(recordDir string) (*ReportStore, error) {
	recordDir = strings.TrimSpace(recordDir)
	if recordDir == "" {
		recordDir = "records"
	}
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建记录目录失败: %w", err)
	}

	store := &ReportStore{
		recordDir:       recordDir,
		settlementPath:  filepath.Join(recordDir, settlementCSVName),
		monitorPath:     filepath.Join(recordDir, monitorCSVName),
		backtestPath:    filepath.Join(recordDir, backtestCSVName),
		backtestRunPath: filepath.Join(recordDir, backtestRunCSVName),
		residualPath:    filepath.Join(recordDir, residualCSVName),
		executionPath:   filepath.Join(recordDir, executionCSVName),
		monthlyPath:     filepath.Join(recordDir, monthlySimulationLogCSVName),
	}
	if err := store.migrateLegacyCSVFilesLocked(); err != nil {
		return nil, err
	}
	if err := store.seedBacktestRecordsIfEmpty(filepath.Join("backtest", "sample_backtest_records.csv")); err != nil {
		return nil, err
	}
	if err := store.compactRuntimeJSONsLocked(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := store.reloadCaches(); err != nil {
		return nil, err
	}
	return store, nil
}

// RecordDir 返回记录目录，供 Web 面板暴露下载链接。
func (s *ReportStore) RecordDir() string {
	return s.recordDir
}

// AppendSettlement 追加模块五结算记录。
func (s *ReportStore) AppendSettlement(settlement CycleSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := SettlementCSVRecord{
		TraceRecordMetadata:   settlement.TraceRecordMetadata.normalized(),
		RunID:                 strings.TrimSpace(settlement.RunID),
		CycleID:               strings.TrimSpace(settlement.CycleID),
		RecordedAt:            time.Now().UTC().Format(time.RFC3339),
		FundingTime:           settlement.FundingTimePoint.Format(time.RFC3339),
		CloseExecutedAt:       settlement.CloseExecutedAt.Format(time.RFC3339),
		Targets:               settlement.Targets,
		FundingIncome:         formatFloat(settlement.FundingIncome),
		ExpectedFundingIncome: formatFloat(settlement.ExpectedFundingIncome),
		FundingIncomeDiff:     formatFloat(settlement.FundingIncomeDiff),
		SpotTradingPnL:        formatFloat(settlement.SpotTradingPnL),
		FuturesTradingPnL:     formatFloat(settlement.FuturesTradingPnL),
		TotalFees:             formatFloat(settlement.TotalFees),
		NetPnL:                formatFloat(settlement.NetPnL),
		SnapshotNetPnL:        formatFloat(settlement.SnapshotNetPnL),
		ResidualSpotValue:     formatFloat(settlement.ResidualSpotValue),
		SpotUSDTDelta:         formatFloat(settlement.SpotUSDTDelta),
		FuturesUSDTDelta:      formatFloat(settlement.FuturesUSDTDelta),
		HasAlert:              settlement.HasAlert,
		Alerts:                strings.Join(settlement.Alerts, " | "),
	}
	s.settlementRecords = append(s.settlementRecords, record)
	if err := writeJSONFile(s.settlementPath, s.settlementRecords); err != nil {
		return err
	}
	return s.compactJSONIfNeededLocked(s.settlementPath)
}

// AppendMonitorSnapshot 追加模块三每个目标的监控摘要。
func (s *ReportStore) AppendMonitorSnapshot(stopAt time.Time, targets []*MonitoredTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	recordedAt := time.Now().Format(time.RFC3339)
	records := make([]MonitorCSVRecord, 0, len(targets))
	for _, target := range targets {
		records = append(records, MonitorCSVRecord{
			TraceRecordMetadata: target.TraceRecordMetadata.normalized(),
			RunID:               strings.TrimSpace(target.RunID),
			CycleID:             strings.TrimSpace(target.CycleID),
			RecordedAt:          recordedAt,
			MonitorStopAt:       stopAt.Format(time.RFC3339),
			FundingTime:         target.Candidate.NextFundingTime.Format(time.RFC3339),
			Symbol:              target.Candidate.Symbol,
			Pair:                target.Candidate.Pair,
			State:               target.MonitorState,
			Valid:               atomicLoadTargetValid(target),
			BreachCount:         target.BreachCount,
			EstimatedRate:       formatFloat(target.Candidate.EstimatedRate),
			LastFundingRate:     formatFloat(target.Candidate.LastFundingRate),
			BasisRate:           formatFloat(target.Candidate.BasisRate),
			ScanScoreRate:       formatFloat(target.Candidate.ScanScoreRate),
			LastNetProfit:       formatFloat(target.LastNetProfit),
			LastSpread:          formatFloat(target.LastSpread),
			InvalidReason:       target.InvalidReason,
		})
	}
	s.monitorRecords = append(s.monitorRecords, records...)
	if err := appendJSONLRecords(s.monitorPath, records); err != nil {
		return err
	}
	return s.compactJSONLIfNeededLocked(s.monitorPath)
}

// AppendExecutionIncident 追加执行事故或恢复动作记录。
func (s *ReportStore) AppendExecutionIncident(record ExecutionIncidentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.executionIncidents = append(s.executionIncidents, ExecutionIncidentRecord{
		TraceRecordMetadata: record.TraceRecordMetadata.normalized(),
		RunID:               strings.TrimSpace(record.RunID),
		CycleID:             strings.TrimSpace(record.CycleID),
		RecordedAt:          record.RecordedAt,
		Level:               record.Level,
		Category:            record.Category,
		Severity:            record.Severity,
		Phase:               record.Phase,
		Symbol:              record.Symbol,
		Action:              record.Action,
		IncidentCount:       record.IncidentCount,
		IncidentLimit:       record.IncidentLimit,
		Fatal:               record.Fatal,
		Detail:              record.Detail,
	})
	s.trimExecutionIncidentsLocked()
	if err := appendJSONLRecord(s.executionPath, s.executionIncidents[len(s.executionIncidents)-1]); err != nil {
		return err
	}
	return s.compactJSONLIfNeededLocked(s.executionPath)
}

// ListSettlementRecords 读取最近 N 条结算记录。
func (s *ReportStore) ListSettlementRecords(limit int) ([]SettlementCSVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettlementRecordsDescending(s.settlementRecords, limit), nil
}

// ListMonitorRecords 读取最近 N 条模块三摘要记录。
func (s *ReportStore) ListMonitorRecords(limit int) ([]MonitorCSVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMonitorRecordsDescending(s.monitorRecords, limit), nil
}

// ListBacktestRecords 读取最近 N 条回测记录。
func (s *ReportStore) ListBacktestRecords(limit int) ([]BacktestCSVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneBacktestRecordsDescending(s.backtestRecords, limit), nil
}

// ListResidualRiskRecords 读取最近 N 条残腿风险记录。
func (s *ReportStore) ListResidualRiskRecords(limit int) ([]ResidualRiskCSVRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneResidualRiskRecordsDescending(s.residualRiskRecords, limit), nil
}

// ListExecutionIncidents 读取最近 N 条执行事故与恢复动作记录。
func (s *ReportStore) ListExecutionIncidents(limit int) ([]ExecutionIncidentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneExecutionIncidentsDescending(s.executionIncidents, limit), nil
}

// ListResidualSeverityStats 统计 low/medium/high 残腿次数。
func (s *ReportStore) ListResidualSeverityStats() ([]ResidualSeverityStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneResidualSeverityStats(s.residualSeverityStats), nil
}

// ListResidualScenarioStats 按 scenario 聚合残腿风险次数、平均残差和最坏应急损益。
func (s *ReportStore) ListResidualScenarioStats() ([]ResidualScenarioStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneResidualScenarioStats(s.residualScenarioStats), nil
}

// ReplaceBacktestRecords 用一批新的回测结果覆盖现有 backtest_records.json。
func (s *ReportStore) ReplaceBacktestRecords(records []BacktestCSVRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backtestRecords = append([]BacktestCSVRecord(nil), records...)
	return writeJSONFile(s.backtestPath, s.backtestRecords)
}

// AppendBacktestRunSummaries 将本次回测运行结果追加到归档文件，便于后续做多轮参数对比。
func (s *ReportStore) AppendBacktestRunSummaries(runMode string, records []BacktestCSVRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	runMode = strings.TrimSpace(runMode)
	if runMode == "" {
		runMode = "backtest"
	}
	if len(records) == 0 {
		return nil
	}
	runID := time.Now().UTC().Format(time.RFC3339Nano)
	existing, err := readJSONFile[BacktestRunSummaryRecord](s.backtestRunPath)
	if err != nil {
		return err
	}
	for _, record := range records {
		existing = append(existing, BacktestRunSummaryRecord{
			TraceRecordMetadata: record.TraceRecordMetadata.normalized(),
			RunID:               runID,
			RunMode:             runMode,
			RecordedAt:          record.RecordedAt,
			Scenario:            record.Scenario,
			Symbol:              record.Symbol,
			Period:              record.Period,
			Trades:              record.Trades,
			WinRate:             record.WinRate,
			FundingIncome:       record.FundingIncome,
			TradingPnL:          record.TradingPnL,
			TotalFees:           record.TotalFees,
			NetPnL:              record.NetPnL,
			MaxDrawdown:         record.MaxDrawdown,
			Notes:               record.Notes,
		})
	}
	return writeJSONFile(s.backtestRunPath, existing)
}

// ReplaceResidualRiskRecords 用一批新的残腿风险结果覆盖现有 residual_risk_records.json。
func (s *ReportStore) ReplaceResidualRiskRecords(records []ResidualRiskCSVRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.residualRiskRecords = append([]ResidualRiskCSVRecord(nil), records...)
	s.refreshResidualStatsLocked()
	return writeJSONFile(s.residualPath, s.residualRiskRecords)
}

func (s *ReportStore) compactJSONIfNeededLocked(path string) error {
	compacted, err := compactJSONByRecordedAt(path, time.Now().UTC(), reportCSVRetention, reportCSVMaxFileSizeBytes)
	if err != nil {
		return err
	}
	if !compacted {
		return nil
	}
	return s.reloadCachesLocked()
}

func (s *ReportStore) compactJSONLIfNeededLocked(path string) error {
	compacted, err := compactJSONLByRecordedAt(path, time.Now().UTC(), reportCSVRetention, reportCSVMaxFileSizeBytes)
	if err != nil {
		return err
	}
	if !compacted {
		return nil
	}
	return s.reloadCachesLocked()
}

func (s *ReportStore) compactRuntimeJSONsLocked(now time.Time) error {
	jsonTargets := []string{s.settlementPath, s.residualPath, s.monthlyPath}
	compactedAny := false
	for _, target := range jsonTargets {
		compacted, err := compactJSONByRecordedAt(target, now, reportCSVRetention, reportCSVMaxFileSizeBytes)
		if err != nil {
			return err
		}
		if compacted {
			compactedAny = true
		}
	}
	jsonlTargets := []string{s.monitorPath, s.executionPath}
	for _, target := range jsonlTargets {
		compacted, err := compactJSONLByRecordedAt(target, now, reportCSVRetention, reportCSVMaxFileSizeBytes)
		if err != nil {
			return err
		}
		if compacted {
			compactedAny = true
		}
	}
	if compactedAny {
		return s.reloadCachesLocked()
	}
	return nil
}

// ClearDashboardData 清空页面中的记录文件，保留空 JSON/JSONL 文件以避免后续写入失败。
func (s *ReportStore) ClearDashboardData() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clearTargets := []string{s.settlementPath, s.monitorPath, s.backtestPath, s.residualPath, s.executionPath, s.monthlyPath}
	for _, target := range clearTargets {
		if strings.HasSuffix(target, ".jsonl") {
			if err := clearJSONLFile(target); err != nil {
				return err
			}
		} else {
			if err := writeJSONFile(target, []any{}); err != nil {
				return err
			}
		}
	}
	s.settlementRecords = nil
	s.monitorRecords = nil
	s.backtestRecords = nil
	s.residualRiskRecords = nil
	s.executionIncidents = nil
	s.refreshResidualStatsLocked()
	return nil
}

func (s *ReportStore) reloadCaches() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadCachesLocked()
}

func (s *ReportStore) reloadCachesLocked() error {
	settlementRecords, err := readJSONFile[SettlementCSVRecord](s.settlementPath)
	if err != nil {
		return err
	}
	if strings.HasSuffix(s.monitorPath, ".jsonl") {
		monitorRecords, readErr := readJSONLFile[MonitorCSVRecord](s.monitorPath)
		if readErr != nil {
			return readErr
		}
		s.monitorRecords = monitorRecords
	} else {
		monitorRecords, readErr := readJSONFile[MonitorCSVRecord](s.monitorPath)
		if readErr != nil {
			return readErr
		}
		s.monitorRecords = monitorRecords
	}
	backtestRecords, err := readJSONFile[BacktestCSVRecord](s.backtestPath)
	if err != nil {
		return err
	}
	residualRecords, err := readJSONFile[ResidualRiskCSVRecord](s.residualPath)
	if err != nil {
		return err
	}
	if strings.HasSuffix(s.executionPath, ".jsonl") {
		executionRecords, readErr := readJSONLFile[ExecutionIncidentRecord](s.executionPath)
		if readErr != nil {
			return readErr
		}
		s.executionIncidents = executionRecords
	} else {
		executionRecords, readErr := readJSONFile[ExecutionIncidentRecord](s.executionPath)
		if readErr != nil {
			return readErr
		}
		s.executionIncidents = executionRecords
	}

	s.settlementRecords = settlementRecords
	s.backtestRecords = backtestRecords
	s.residualRiskRecords = residualRecords
	s.trimExecutionIncidentsLocked()
	s.refreshResidualStatsLocked()
	return nil
}

func (s *ReportStore) refreshResidualStatsLocked() {
	s.residualSeverityStats = buildResidualSeverityStats(s.residualRiskRecords)
	s.residualScenarioStats = buildResidualScenarioStats(s.residualRiskRecords)
}

func (s *ReportStore) trimExecutionIncidentsLocked() {
	if len(s.executionIncidents) <= executionIncidentCacheLimit {
		return
	}
	s.executionIncidents = append([]ExecutionIncidentRecord(nil), s.executionIncidents[len(s.executionIncidents)-executionIncidentCacheLimit:]...)
}

func (s *ReportStore) migrateLegacyCSVFilesLocked() error {
	migrations := []func() error{
		func() error {
			return migrateLegacyJSONFile(
				s.settlementPath,
				filepath.Join(s.recordDir, legacySettlementCSVName),
				func(rows [][]string) any { return parseSettlementRows(rows) },
			)
		},
		func() error {
			return migrateLegacyJSONLFile(
				s.monitorPath,
				filepath.Join(s.recordDir, legacyMonitorJSONName),
				filepath.Join(s.recordDir, legacyMonitorCSVName),
				func(rows [][]string) []MonitorCSVRecord { return parseMonitorRows(rows) },
			)
		},
		func() error {
			return migrateLegacyJSONFile(
				s.backtestPath,
				filepath.Join(s.recordDir, legacyBacktestCSVName),
				func(rows [][]string) any { return parseBacktestRows(rows) },
			)
		},
		func() error {
			return migrateLegacyJSONFile(
				s.backtestRunPath,
				filepath.Join(s.recordDir, legacyBacktestRunCSVName),
				func(rows [][]string) any { return parseBacktestRunSummaryRows(rows) },
			)
		},
		func() error {
			return migrateLegacyJSONFile(
				s.residualPath,
				filepath.Join(s.recordDir, legacyResidualCSVName),
				func(rows [][]string) any { return parseResidualRiskRows(rows) },
			)
		},
		func() error {
			return migrateLegacyJSONLFile(
				s.executionPath,
				filepath.Join(s.recordDir, legacyExecutionJSONName),
				filepath.Join(s.recordDir, legacyExecutionCSVName),
				func(rows [][]string) []ExecutionIncidentRecord { return parseExecutionRows(rows) },
			)
		},
		func() error {
			return migrateLegacyMonthlyJSONFile(
				s.monthlyPath,
				filepath.Join(s.recordDir, strings.TrimSuffix(monthlySimulationLogCSVName, ".json")+".csv"),
			)
		},
	}
	for _, migrate := range migrations {
		if err := migrate(); err != nil {
			return err
		}
	}
	return nil
}

func ensureJSONFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSON 文件失败: %w", err)
	}
	return writeJSONFile(path, []any{})
}

func ensureJSONLFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSONL 文件失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 JSONL 目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("初始化 JSONL 文件失败: %w", err)
	}
	return file.Close()
}

func writeJSONFile(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	data = append(data, '\n')
	mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755)
	if mkdirErr != nil {
		return fmt.Errorf("创建 JSON 目录失败: %w", mkdirErr)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建 JSON 临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	success := false
	defer func() {
		_ = tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(data); err != nil {
		return fmt.Errorf("写入 JSON 临时文件失败: %w", err)
	}
	if syncErr := tempFile.Sync(); syncErr != nil {
		return fmt.Errorf("刷新 JSON 临时文件失败: %w", syncErr)
	}
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("关闭 JSON 临时文件失败: %w", closeErr)
	}
	if renameErr := os.Rename(tempPath, path); renameErr != nil {
		return fmt.Errorf("提交 JSON 文件失败: %w", renameErr)
	}
	success = true
	return nil
}

func appendJSONLRecord(path string, payload any) error {
	if err := ensureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 JSONL 追加失败: %w", err)
	}
	defer file.Close()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 JSONL 记录失败: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("写入 JSONL 记录失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("刷新 JSONL 文件失败: %w", err)
	}
	return nil
}

func appendJSONLRecords[T any](path string, payloads []T) error {
	if len(payloads) == 0 {
		return nil
	}
	if err := ensureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 JSONL 追加失败: %w", err)
	}
	defer file.Close()
	for _, payload := range payloads {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return fmt.Errorf("序列化 JSONL 记录失败: %w", marshalErr)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("写入 JSONL 记录失败: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("刷新 JSONL 文件失败: %w", err)
	}
	return nil
}

func readJSONFile[T any](path string) ([]T, error) {
	if err := ensureJSONFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 JSON 文件失败: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []T{}, nil
	}
	var records []T
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("解析 JSON 文件失败: %w", err)
	}
	if records == nil {
		return []T{}, nil
	}
	return records, nil
}

func readJSONLFile[T any](path string) ([]T, error) {
	if err := ensureJSONLFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 JSONL 文件失败: %w", err)
	}
	defer file.Close()

	result := make([]T, 0, 128)
	scanner := newRuntimeLogScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("解析 JSONL 记录失败: %w", err)
		}
		result = append(result, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 JSONL 文件失败: %w", err)
	}
	return result, nil
}

func compactJSONByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 JSON 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取 JSON 文件失败: %w", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return false, fmt.Errorf("解析 JSON 文件失败: %w", err)
	}
	cutoff := now.Add(-retention)
	kept := make([]map[string]any, 0, len(rows))
	compacted := false
	for _, row := range rows {
		recordedAtText, _ := row["recorded_at"].(string)
		recordedAt, ok := parseCSVRecordedAt(recordedAtText)
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		kept = append(kept, row)
	}
	if !compacted {
		return false, nil
	}
	if err := writeJSONFile(path, kept); err != nil {
		return false, err
	}
	return true, nil
}

func compactJSONLByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 JSONL 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}

	source, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 JSONL 文件失败: %w", err)
	}
	defer source.Close()

	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("创建 JSONL 临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	success := false
	defer func() {
		_ = tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	cutoff := now.Add(-retention)
	scanner := newRuntimeLogScanner(source)
	compacted := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return false, fmt.Errorf("解析 JSONL 记录失败: %w", err)
		}
		recordedAtText, _ := row["recorded_at"].(string)
		recordedAt, ok := parseCSVRecordedAt(recordedAtText)
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		if _, err := tempFile.WriteString(line + "\n"); err != nil {
			return false, fmt.Errorf("写入 JSONL 临时文件失败: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读取 JSONL 文件失败: %w", err)
	}
	if !compacted {
		return false, nil
	}
	if err := tempFile.Sync(); err != nil {
		return false, fmt.Errorf("刷新 JSONL 临时文件失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return false, fmt.Errorf("关闭 JSONL 临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("提交 JSONL 文件失败: %w", err)
	}
	success = true
	return true, nil
}

func clearJSONLFile(path string) error {
	if err := ensureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("清空 JSONL 文件失败: %w", err)
	}
	return file.Close()
}

func migrateLegacyJSONFile(path string, legacyCSVPath string, parser func(rows [][]string) any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err != nil {
		if os.IsNotExist(err) {
			return ensureJSONFile(path)
		}
		return fmt.Errorf("检查旧 CSV 文件失败: %w", err)
	}
	rows, err := readCSVRows(legacyCSVPath)
	if err != nil {
		return err
	}
	return writeJSONFile(path, parser(rows))
}

func migrateLegacyJSONLFile[T any](path string, legacyJSONPath string, legacyCSVPath string, csvParser func(rows [][]string) []T) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSONL 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyJSONPath); err == nil {
		records, readErr := readJSONFile[T](legacyJSONPath)
		if readErr != nil {
			return readErr
		}
		return rewriteJSONLFile(path, records)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err == nil {
		rows, readErr := readCSVRows(legacyCSVPath)
		if readErr != nil {
			return readErr
		}
		return rewriteJSONLFile(path, csvParser(rows))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧 CSV 文件失败: %w", err)
	}
	return ensureJSONLFile(path)
}

func rewriteJSONLFile[T any](path string, records []T) error {
	if err := ensureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 JSONL 文件失败: %w", err)
	}
	defer file.Close()
	for _, record := range records {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return fmt.Errorf("序列化 JSONL 记录失败: %w", marshalErr)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("写入 JSONL 记录失败: %w", err)
		}
	}
	return file.Sync()
}

func migrateLegacyMonthlyJSONFile(path string, legacyCSVPath string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查月度模拟 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err != nil {
		if os.IsNotExist(err) {
			return ensureJSONFile(path)
		}
		return fmt.Errorf("检查旧月度模拟 CSV 文件失败: %w", err)
	}
	table, err := readCSVTable(legacyCSVPath)
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return ensureJSONFile(path)
	}
	header := table[0]
	payload := make([]map[string]string, 0, maxIntValue(0, len(table)-1))
	for _, row := range table[1:] {
		item := make(map[string]string, len(header))
		for index, key := range header {
			if index < len(row) {
				item[key] = row[index]
			} else {
				item[key] = ""
			}
		}
		payload = append(payload, item)
	}
	return writeJSONFile(path, payload)
}

func ensureCSVHeader(path string, header []string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 CSV 文件失败: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 表头失败: %w", err)
	}
	return nil
}

func migrateExecutionCSVSchema(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开执行事件 CSV 失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return fmt.Errorf("读取执行事件 CSV 失败: %w", err)
	}
	if len(rows) == 0 {
		return rewriteCSVWithHeader(path, executionCSVHeader)
	}
	header := rows[0]
	if len(header) == len(executionCSVHeader) {
		return nil
	}
	if len(header) != 9 {
		return nil
	}

	records := parseExecutionRows(rows[1:])
	if err := rewriteCSVWithHeader(path, executionCSVHeader); err != nil {
		return err
	}
	payload := make([][]string, 0, len(records))
	for _, record := range records {
		payload = append(payload, executionIncidentRow(record))
	}
	return appendCSVRows(path, payload)
}

func rewriteCSVWithHeader(path string, header []string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("重写 CSV 表头失败: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新重写 CSV 表头失败: %w", err)
	}
	return nil
}

func rewriteCSV(path string, rows [][]string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("重写 CSV 记录失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新重写 CSV 记录失败: %w", err)
	}
	return nil
}

func appendCSVRow(path string, row []string) error {
	return appendCSVRows(path, [][]string{row})
}

func appendCSVRows(path string, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 CSV 追加失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("写入 CSV 记录失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 记录失败: %w", err)
	}
	return nil
}

func compactCSVByRecordedAt(path string, header []string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 CSV 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}
	recordedAtIndex := csvColumnIndex(header, "recorded_at")
	if recordedAtIndex < 0 {
		return false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return false, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}
	cutoff := now.Add(-retention)
	kept := make([][]string, 0, len(rows))
	kept = append(kept, rows[0])
	compacted := false
	for _, row := range rows[1:] {
		if len(row) <= recordedAtIndex {
			kept = append(kept, row)
			continue
		}
		recordedAt, ok := parseCSVRecordedAt(row[recordedAtIndex])
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		kept = append(kept, row)
	}
	if !compacted {
		return false, nil
	}
	if err := rewriteCSV(path, kept); err != nil {
		return false, err
	}
	return true, nil
}

func readCSVRows(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	if len(allRows) <= 1 {
		return [][]string{}, nil
	}
	return allRows[1:], nil
}

func readCSVTable(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	allRows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	return allRows, nil
}

func csvColumnIndex(header []string, field string) int {
	field = strings.TrimSpace(field)
	for index, item := range header {
		if strings.TrimSpace(item) == field {
			return index
		}
	}
	return -1
}

func parseCSVRecordedAt(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func atomicLoadTargetValid(target *MonitoredTarget) bool {
	return target != nil && atomic.LoadInt32(&target.IsValid) == 1
}

func (s *ReportStore) seedBacktestRecordsIfEmpty(samplePath string) error {
	records, err := readJSONFile[BacktestCSVRecord](s.backtestPath)
	if err != nil {
		return err
	}
	if len(records) > 0 {
		return nil
	}

	sampleRows, err := readCSVRows(samplePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeJSONFile(s.backtestPath, parseBacktestRows(sampleRows))
}

func parseSettlementRows(rows [][]string) []SettlementCSVRecord {
	result := make([]SettlementCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(settlementCSVHeader) {
			continue
		}
		targets, _ := strconv.Atoi(row[3])
		hasAlert, _ := strconv.ParseBool(row[15])
		result = append(result, SettlementCSVRecord{
			RecordedAt:            row[0],
			FundingTime:           row[1],
			CloseExecutedAt:       row[2],
			Targets:               targets,
			FundingIncome:         row[4],
			ExpectedFundingIncome: row[5],
			FundingIncomeDiff:     row[6],
			SpotTradingPnL:        row[7],
			FuturesTradingPnL:     row[8],
			TotalFees:             row[9],
			NetPnL:                row[10],
			SnapshotNetPnL:        row[11],
			ResidualSpotValue:     row[12],
			SpotUSDTDelta:         row[13],
			FuturesUSDTDelta:      row[14],
			HasAlert:              hasAlert,
			Alerts:                row[16],
		})
	}
	return result
}

func parseMonitorRows(rows [][]string) []MonitorCSVRecord {
	result := make([]MonitorCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(monitorCSVHeader) {
			continue
		}
		valid, _ := strconv.ParseBool(row[6])
		breachCount, _ := strconv.Atoi(row[7])
		result = append(result, MonitorCSVRecord{
			RecordedAt:      row[0],
			MonitorStopAt:   row[1],
			FundingTime:     row[2],
			Symbol:          row[3],
			Pair:            row[4],
			State:           row[5],
			Valid:           valid,
			BreachCount:     breachCount,
			EstimatedRate:   row[8],
			LastFundingRate: row[9],
			BasisRate:       row[10],
			ScanScoreRate:   row[11],
			LastNetProfit:   row[12],
			LastSpread:      row[13],
			InvalidReason:   row[14],
		})
	}
	return result
}

func parseBacktestRows(rows [][]string) []BacktestCSVRecord {
	result := make([]BacktestCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(backtestCSVHeader) {
			continue
		}
		trades, _ := strconv.Atoi(row[4])
		result = append(result, BacktestCSVRecord{
			RecordedAt:    row[0],
			Scenario:      row[1],
			Symbol:        row[2],
			Period:        row[3],
			Trades:        trades,
			WinRate:       row[5],
			FundingIncome: row[6],
			TradingPnL:    row[7],
			TotalFees:     row[8],
			NetPnL:        row[9],
			MaxDrawdown:   row[10],
			Notes:         row[11],
		})
	}
	return result
}

func parseBacktestRunSummaryRows(rows [][]string) []BacktestRunSummaryRecord {
	result := make([]BacktestRunSummaryRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(backtestRunCSVHeader) {
			continue
		}
		trades, _ := strconv.Atoi(row[6])
		result = append(result, BacktestRunSummaryRecord{
			RunID:         row[0],
			RunMode:       row[1],
			RecordedAt:    row[2],
			Scenario:      row[3],
			Symbol:        row[4],
			Period:        row[5],
			Trades:        trades,
			WinRate:       row[7],
			FundingIncome: row[8],
			TradingPnL:    row[9],
			TotalFees:     row[10],
			NetPnL:        row[11],
			MaxDrawdown:   row[12],
			Notes:         row[13],
		})
	}
	return result
}

func parseResidualRiskRows(rows [][]string) []ResidualRiskCSVRecord {
	result := make([]ResidualRiskCSVRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < len(residualCSVHeader) {
			continue
		}
		futuresLegOK, _ := strconv.ParseBool(row[7])
		spotLegOK, _ := strconv.ParseBool(row[8])
		result = append(result, ResidualRiskCSVRecord{
			RecordedAt:         row[0],
			Scenario:           row[1],
			Symbol:             row[2],
			Period:             row[3],
			Phase:              row[4],
			EventTime:          row[5],
			MonitorState:       row[6],
			FuturesLegOK:       futuresLegOK,
			SpotLegOK:          spotLegOK,
			FuturesFillRatio:   row[9],
			SpotFillRatio:      row[10],
			FuturesFilledUSDT:  row[11],
			SpotFilledUSDT:     row[12],
			ResidualUSDT:       row[13],
			EmergencyHedgeCost: row[14],
			EmergencyHedgeFee:  row[15],
			EmergencyHedgePnL:  row[16],
			Severity:           row[17],
			Reason:             row[18],
			Notes:              row[19],
		})
	}
	return result
}

func parseExecutionRows(rows [][]string) []ExecutionIncidentRecord {
	result := make([]ExecutionIncidentRecord, 0, len(rows))
	for _, row := range rows {
		if len(row) < 9 {
			continue
		}
		record := ExecutionIncidentRecord{
			RecordedAt: row[0],
			Level:      row[1],
		}
		if len(row) >= len(executionCSVHeader) {
			record.Category = row[2]
			record.Severity = row[3]
			record.Phase = row[4]
			record.Symbol = row[5]
			record.Action = row[6]
			record.IncidentCount, _ = strconv.Atoi(row[7])
			record.IncidentLimit, _ = strconv.Atoi(row[8])
			record.Fatal, _ = strconv.ParseBool(row[9])
			record.Detail = row[10]
		} else {
			record.Phase = row[2]
			record.Symbol = row[3]
			record.Action = row[4]
			record.IncidentCount, _ = strconv.Atoi(row[5])
			record.IncidentLimit, _ = strconv.Atoi(row[6])
			record.Fatal, _ = strconv.ParseBool(row[7])
			record.Detail = row[8]
		}
		result = append(result, record)
	}
	return result
}

func executionIncidentRow(record ExecutionIncidentRecord) []string {
	return []string{
		record.RecordedAt,
		record.Level,
		record.Category,
		record.Severity,
		record.Phase,
		record.Symbol,
		record.Action,
		strconv.Itoa(record.IncidentCount),
		strconv.Itoa(record.IncidentLimit),
		strconv.FormatBool(record.Fatal),
		record.Detail,
	}
}

func buildResidualSeverityStats(records []ResidualRiskCSVRecord) []ResidualSeverityStats {
	counts := map[string]int{
		"low":    0,
		"medium": 0,
		"high":   0,
	}
	for _, record := range records {
		severity := strings.TrimSpace(record.Severity)
		if severity == "" {
			severity = "low"
		}
		counts[severity]++
	}
	return []ResidualSeverityStats{
		{Severity: "low", Count: counts["low"]},
		{Severity: "medium", Count: counts["medium"]},
		{Severity: "high", Count: counts["high"]},
	}
}

func buildResidualScenarioStats(records []ResidualRiskCSVRecord) []ResidualScenarioStats {
	type scenarioAggregate struct {
		count             int
		totalResidual     float64
		worstEmergencyPnL float64
		highRiskCount     int
	}

	aggregates := make(map[string]*scenarioAggregate)
	for _, record := range records {
		stats, ok := aggregates[record.Scenario]
		if !ok {
			stats = &scenarioAggregate{}
			aggregates[record.Scenario] = stats
		}
		stats.count++
		stats.totalResidual += parseFloat(record.ResidualUSDT)
		pnl := parseFloat(record.EmergencyHedgePnL)
		if stats.count == 1 || pnl < stats.worstEmergencyPnL {
			stats.worstEmergencyPnL = pnl
		}
		if record.Severity == "high" {
			stats.highRiskCount++
		}
	}

	scenarios := make([]string, 0, len(aggregates))
	for scenario := range aggregates {
		scenarios = append(scenarios, scenario)
	}
	sort.Strings(scenarios)

	result := make([]ResidualScenarioStats, 0, len(scenarios))
	for _, scenario := range scenarios {
		stats := aggregates[scenario]
		avgResidual := 0.0
		if stats.count > 0 {
			avgResidual = stats.totalResidual / float64(stats.count)
		}
		result = append(result, ResidualScenarioStats{
			Scenario:            scenario,
			Count:               stats.count,
			AverageResidualUSDT: formatFloat(avgResidual),
			WorstEmergencyPnL:   formatFloat(stats.worstEmergencyPnL),
			HighRiskCount:       stats.highRiskCount,
		})
	}
	return result
}

func cloneSettlementRecordsDescending(records []SettlementCSVRecord, limit int) []SettlementCSVRecord {
	if len(records) == 0 {
		return []SettlementCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneMonitorRecordsDescending(records []MonitorCSVRecord, limit int) []MonitorCSVRecord {
	if len(records) == 0 {
		return []MonitorCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneBacktestRecordsDescending(records []BacktestCSVRecord, limit int) []BacktestCSVRecord {
	if len(records) == 0 {
		return []BacktestCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneResidualRiskRecordsDescending(records []ResidualRiskCSVRecord, limit int) []ResidualRiskCSVRecord {
	if len(records) == 0 {
		return []ResidualRiskCSVRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneExecutionIncidentsDescending(records []ExecutionIncidentRecord, limit int) []ExecutionIncidentRecord {
	if len(records) == 0 {
		return []ExecutionIncidentRecord{}
	}
	return cloneDescending(records, limit)
}

func cloneResidualSeverityStats(records []ResidualSeverityStats) []ResidualSeverityStats {
	if len(records) == 0 {
		return []ResidualSeverityStats{}
	}
	return append([]ResidualSeverityStats(nil), records...)
}

func cloneResidualScenarioStats(records []ResidualScenarioStats) []ResidualScenarioStats {
	if len(records) == 0 {
		return []ResidualScenarioStats{}
	}
	return append([]ResidualScenarioStats(nil), records...)
}

func cloneDescending[T any](records []T, limit int) []T {
	if len(records) == 0 {
		return []T{}
	}
	size := len(records)
	if limit > 0 && limit < size {
		size = limit
	}
	result := make([]T, 0, size)
	for index := len(records) - 1; index >= 0 && len(result) < size; index-- {
		result = append(result, records[index])
	}
	return result
}
