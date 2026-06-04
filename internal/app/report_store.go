package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	settlementCSVName           = "settlements.json"
	monitorCSVName              = "monitor_records.jsonl"
	backtestCSVName             = "backtest_records.json"
	backtestRunCSVName          = "backtest_run_summaries.json"
	residualCSVName             = "residual_risk_records.json"
	executionCSVName            = "execution_incidents.jsonl"
	domainEventJSONLName        = "domain_events.jsonl"
	scanObservationJSONLName    = "scan_observations.jsonl"
	executionIncidentCacheLimit = 5000
	domainEventCacheLimit       = 5000
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

// ReportStore 负责 JSON 落盘与读取。
type ReportStore struct {
	recordDir          string
	settlementPath     string
	monitorPath        string
	backtestPath       string
	backtestRunPath    string
	residualPath       string
	executionPath      string
	domainEventPath    string
	scanObsPath        string
	dustPath           string
	pendingFundingPath string
	monthlyPath        string
	mu                 sync.Mutex

	settlementRecords     []SettlementCSVRecord
	monitorRecords        []MonitorCSVRecord
	backtestRecords       []BacktestCSVRecord
	residualRiskRecords   []ResidualRiskCSVRecord
	executionIncidents    []ExecutionIncidentRecord
	domainEvents          []DomainEvent
	dustLedgerRecords     []DustLedgerRecord
	pendingFundingRecords []PendingFundingSettlementRecord
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
	store := newReportStoreWithPaths(recordDir)
	if err := store.initialize(filepath.Join("backtest", "sample_backtest_records.csv"), time.Now().UTC()); err != nil {
		return nil, err
	}
	return store, nil
}

func newReportStoreWithPaths(recordDir string) *ReportStore {
	return &ReportStore{
		recordDir:          recordDir,
		settlementPath:     filepath.Join(recordDir, settlementCSVName),
		monitorPath:        filepath.Join(recordDir, monitorCSVName),
		backtestPath:       filepath.Join(recordDir, backtestCSVName),
		backtestRunPath:    filepath.Join(recordDir, backtestRunCSVName),
		residualPath:       filepath.Join(recordDir, residualCSVName),
		executionPath:      filepath.Join(recordDir, executionCSVName),
		domainEventPath:    filepath.Join(recordDir, domainEventJSONLName),
		scanObsPath:        filepath.Join(recordDir, scanObservationJSONLName),
		dustPath:           filepath.Join(recordDir, "dust_ledger.json"),
		pendingFundingPath: filepath.Join(recordDir, "pending_funding_settlements.json"),
		monthlyPath:        filepath.Join(recordDir, monthlySimulationLogCSVName),
	}
}

func (s *ReportStore) initialize(sampleBacktestPath string, now time.Time) error {
	if err := s.migrateLegacyCSVFilesLocked(); err != nil {
		return err
	}
	if err := s.seedBacktestRecordsIfEmpty(sampleBacktestPath); err != nil {
		return err
	}
	if err := s.compactRuntimeJSONsLocked(now); err != nil {
		return err
	}
	if err := s.reloadCaches(); err != nil {
		return err
	}
	return nil
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
		FundingIncomePending:  settlement.FundingIncomePending,
		SpotTradingPnL:        formatFloat(settlement.SpotTradingPnL),
		FuturesTradingPnL:     formatFloat(settlement.FuturesTradingPnL),
		TotalFees:             formatFloat(settlement.TotalFees),
		TotalBorrowCosts:      formatFloat(settlement.TotalBorrowCosts),
		NetPnL:                formatFloat(settlement.NetPnL),
		SnapshotNetPnL:        formatFloat(settlement.SnapshotNetPnL),
		ResidualSpotValue:     formatFloat(settlement.ResidualSpotValue),
		SpotUSDTDelta:         formatFloat(settlement.SpotUSDTDelta),
		FuturesUSDTDelta:      formatFloat(settlement.FuturesUSDTDelta),
		HasAlert:              settlement.HasAlert,
		Alerts:                strings.Join(settlement.Alerts, " | "),

		TotalOpenSlippageUSDT:     formatFloat(settlement.TotalOpenSlippageUSDT),
		TotalCloseSlippageUSDT:    formatFloat(settlement.TotalCloseSlippageUSDT),
		TotalRecoveryFrictionUSDT: formatFloat(settlement.TotalRecoveryFrictionUSDT),
		NetPnLAfterFrictions:      formatFloat(settlement.NetPnLAfterFrictions),
	}
	if !settlement.FundingSettledAt.IsZero() {
		record.FundingSettledAt = settlement.FundingSettledAt.UTC().Format(time.RFC3339)
	}
	s.settlementRecords = append(s.settlementRecords, record)
	if err := writeJSONFile(s.settlementPath, s.settlementRecords); err != nil {
		return err
	}
	return s.compactJSONIfNeededLocked(s.settlementPath)
}

func (s *ReportStore) UpdateSettlement(settlement CycleSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for idx := range s.settlementRecords {
		record := &s.settlementRecords[idx]
		if strings.TrimSpace(record.RunID) != strings.TrimSpace(settlement.RunID) || strings.TrimSpace(record.CycleID) != strings.TrimSpace(settlement.CycleID) {
			continue
		}
		record.FundingIncome = formatFloat(settlement.FundingIncome)
		record.ExpectedFundingIncome = formatFloat(settlement.ExpectedFundingIncome)
		record.FundingIncomeDiff = formatFloat(settlement.FundingIncomeDiff)
		record.FundingIncomePending = settlement.FundingIncomePending
		if settlement.FundingSettledAt.IsZero() {
			record.FundingSettledAt = ""
		} else {
			record.FundingSettledAt = settlement.FundingSettledAt.UTC().Format(time.RFC3339)
		}
		record.SpotTradingPnL = formatFloat(settlement.SpotTradingPnL)
		record.FuturesTradingPnL = formatFloat(settlement.FuturesTradingPnL)
		record.TotalFees = formatFloat(settlement.TotalFees)
		record.TotalBorrowCosts = formatFloat(settlement.TotalBorrowCosts)
		record.NetPnL = formatFloat(settlement.NetPnL)
		record.SnapshotNetPnL = formatFloat(settlement.SnapshotNetPnL)
		record.ResidualSpotValue = formatFloat(settlement.ResidualSpotValue)
		record.SpotUSDTDelta = formatFloat(settlement.SpotUSDTDelta)
		record.FuturesUSDTDelta = formatFloat(settlement.FuturesUSDTDelta)
		record.HasAlert = settlement.HasAlert
		record.Alerts = strings.Join(settlement.Alerts, " | ")
		record.TotalOpenSlippageUSDT = formatFloat(settlement.TotalOpenSlippageUSDT)
		record.TotalCloseSlippageUSDT = formatFloat(settlement.TotalCloseSlippageUSDT)
		record.TotalRecoveryFrictionUSDT = formatFloat(settlement.TotalRecoveryFrictionUSDT)
		record.NetPnLAfterFrictions = formatFloat(settlement.NetPnLAfterFrictions)
		if err := writeJSONFile(s.settlementPath, s.settlementRecords); err != nil {
			return err
		}
		return s.compactJSONIfNeededLocked(s.settlementPath)
	}
	return fmt.Errorf("未找到待更新的结算记录: run_id=%s cycle_id=%s", settlement.RunID, settlement.CycleID)
}

// AppendMonitorSnapshot 追加模块三每个目标的监控摘要。
func (s *ReportStore) AppendMonitorSnapshot(stopAt time.Time, targets []*MonitoredTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	recordedAt := time.Now().Format(time.RFC3339)
	records := make([]MonitorCSVRecord, 0, len(targets))
	for _, target := range targets {
		records = append(records, MonitorCSVRecord{
			TraceRecordMetadata:    target.TraceRecordMetadata.normalized(),
			RunID:                  strings.TrimSpace(target.RunID),
			CycleID:                strings.TrimSpace(target.CycleID),
			RecordedAt:             recordedAt,
			MonitorStopAt:          stopAt.Format(time.RFC3339),
			FundingTime:            target.Candidate.NextFundingTime.Format(time.RFC3339),
			Symbol:                 target.Candidate.Symbol,
			Pair:                   target.Candidate.Pair,
			State:                  target.MonitorState,
			Valid:                  atomicLoadTargetValid(target),
			BreachCount:            target.BreachCount,
			EstimatedRate:          formatFloat(target.Candidate.EstimatedRate),
			LastFundingRate:        formatFloat(target.Candidate.LastFundingRate),
			BasisRate:              formatFloat(target.Candidate.BasisRate),
			ScanScoreRate:          formatFloat(target.Candidate.ScanScoreRate),
			LastNetProfit:          formatFloat(target.LastNetProfit),
			LastSpread:             formatFloat(target.LastSpread),
			DepthStabilityRatio:    formatFloat(target.LastDepthStabilityRatio),
			SpotVisibleNotional:    formatFloat(target.LastSpotVisibleNotional),
			FuturesVisibleNotional: formatFloat(target.LastFuturesVisibleNotional),
			MarketRiskCategory:     target.LastMarketRiskCategory,
			MarketRiskSeverity:     target.LastMarketRiskSeverity,
			MarketRiskAction:       target.LastMarketRiskAction,
			MarketRiskDetail:       target.LastMarketRiskDetail,
			InvalidReason:          target.InvalidReason,
		})
	}
	s.monitorRecords = append(s.monitorRecords, records...)
	if err := appendJSONLRecords(s.monitorPath, records); err != nil {
		return err
	}
	return s.compactJSONLIfNeededLocked(s.monitorPath)
}

func (s *ReportStore) AppendDustLedger(record DustLedgerRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dustLedgerRecords = append(s.dustLedgerRecords, record)
	if err := writeJSONFile(s.dustPath, s.dustLedgerRecords); err != nil {
		return err
	}
	return s.compactJSONIfNeededLocked(s.dustPath)
}

func (s *ReportStore) ListDustLedgerRecords(limit int) ([]DustLedgerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := append([]DustLedgerRecord(nil), s.dustLedgerRecords...)
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}
	slices.Reverse(records)
	return records, nil
}

func (s *ReportStore) UpsertPendingFundingSettlement(record PendingFundingSettlementRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for idx := range s.pendingFundingRecords {
		current := &s.pendingFundingRecords[idx]
		if strings.TrimSpace(current.RunID) != strings.TrimSpace(record.RunID) || strings.TrimSpace(current.CycleID) != strings.TrimSpace(record.CycleID) {
			continue
		}
		s.pendingFundingRecords[idx] = record
		if err := writeJSONFile(s.pendingFundingPath, s.pendingFundingRecords); err != nil {
			return err
		}
		return nil
	}

	s.pendingFundingRecords = append(s.pendingFundingRecords, record)
	if err := writeJSONFile(s.pendingFundingPath, s.pendingFundingRecords); err != nil {
		return err
	}
	return nil
}

func (s *ReportStore) DeletePendingFundingSettlement(runID string, cycleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.pendingFundingRecords[:0]
	removed := false
	for _, record := range s.pendingFundingRecords {
		if strings.TrimSpace(record.RunID) == strings.TrimSpace(runID) && strings.TrimSpace(record.CycleID) == strings.TrimSpace(cycleID) {
			removed = true
			continue
		}
		filtered = append(filtered, record)
	}
	if !removed {
		return nil
	}
	s.pendingFundingRecords = append([]PendingFundingSettlementRecord(nil), filtered...)
	return writeJSONFile(s.pendingFundingPath, s.pendingFundingRecords)
}

func (s *ReportStore) ListPendingFundingSettlements(limit int) ([]PendingFundingSettlementRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records := append([]PendingFundingSettlementRecord(nil), s.pendingFundingRecords...)
	sort.Slice(records, func(i int, j int) bool {
		left := records[i].RecordedAt
		right := records[j].RecordedAt
		return left > right
	})
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
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

func (s *ReportStore) AppendDomainEvents(events []DomainEvent) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := make([]DomainEvent, 0, len(events))
	for _, event := range events {
		if event.Type == "" {
			continue
		}
		normalized = append(normalized, DomainEvent{
			TraceRecordMetadata: event.TraceRecordMetadata.normalized(),
			Type:                event.Type,
			Level:               eventLevelOrDefault(event.Level),
			OccurredAt:          event.OccurredAt.UTC(),
			RunID:               strings.TrimSpace(event.RunID),
			CycleID:             strings.TrimSpace(event.CycleID),
			Protocol:            strings.TrimSpace(event.Protocol),
			Stage:               NormalizeLifecycleStage(event.Stage),
			Successful:          event.Successful,
			Reason:              strings.TrimSpace(event.Reason),
		})
	}
	if len(normalized) == 0 {
		return nil
	}
	s.domainEvents = append(s.domainEvents, normalized...)
	s.trimDomainEventsLocked()
	if err := appendJSONLRecords(s.domainEventPath, normalized); err != nil {
		return err
	}
	return s.compactJSONLIfNeededLocked(s.domainEventPath)
}

func (s *ReportStore) ListDomainEvents(limit int) ([]DomainEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := append([]DomainEvent(nil), s.domainEvents...)
	if limit > 0 && len(records) > limit {
		records = records[len(records)-limit:]
	}
	slices.Reverse(records)
	return records, nil
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

	clearTargets := []string{s.settlementPath, s.monitorPath, s.backtestPath, s.residualPath, s.executionPath, s.domainEventPath, s.scanObsPath, s.dustPath, s.pendingFundingPath, s.monthlyPath}
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
	s.domainEvents = nil
	s.dustLedgerRecords = nil
	s.pendingFundingRecords = nil
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
	dustRecords, err := readJSONFile[DustLedgerRecord](s.dustPath)
	if err != nil {
		return err
	}
	pendingFundingRecords, err := readJSONFile[PendingFundingSettlementRecord](s.pendingFundingPath)
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
	if strings.HasSuffix(s.domainEventPath, ".jsonl") {
		domainEventRecords, readErr := readJSONLFile[DomainEvent](s.domainEventPath)
		if readErr != nil {
			return readErr
		}
		s.domainEvents = domainEventRecords
	} else {
		domainEventRecords, readErr := readJSONFile[DomainEvent](s.domainEventPath)
		if readErr != nil {
			return readErr
		}
		s.domainEvents = domainEventRecords
	}

	s.settlementRecords = settlementRecords
	s.backtestRecords = backtestRecords
	s.residualRiskRecords = residualRecords
	s.dustLedgerRecords = dustRecords
	s.pendingFundingRecords = pendingFundingRecords
	s.trimExecutionIncidentsLocked()
	s.trimDomainEventsLocked()
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

func (s *ReportStore) trimDomainEventsLocked() {
	if len(s.domainEvents) <= domainEventCacheLimit {
		return
	}
	s.domainEvents = append([]DomainEvent(nil), s.domainEvents[len(s.domainEvents)-domainEventCacheLimit:]...)
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
		// 种子样本是可选的：缺失时静默跳过。注意 readCSVRows 用 %w 包裹了底层错误，
		// 因此必须用 errors.Is 解包判断（os.IsNotExist 不解包，会漏判导致初始化失败）。
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeJSONFile(s.backtestPath, parseBacktestRows(sampleRows))
}
