package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportStoreAppendExecutionIncidentPersistsRiskMetadata(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}

	want := ExecutionIncidentRecord{
		TraceRecordMetadata: TraceRecordMetadata{
			StrategyVersion: "funding-sniper-v1",
			ConfigHash:      "cfg-hash-1",
			AppVersion:      "dev",
			GitCommit:       "unknown",
		},
		RunID:         "live-20260529T120000Z-abcd1234",
		CycleID:       "live-20260529T120000Z-abcd1234-cycle-ef567890",
		RecordedAt:    "2026-05-29T12:00:00Z",
		Level:         "incident",
		Category:      RiskCategoryPlatform,
		Severity:      RiskSeverityCritical,
		Phase:         "platform_fuse",
		Symbol:        "BTCUSDT",
		Action:        "trip",
		IncidentCount: 3,
		IncidentLimit: 3,
		Fatal:         true,
		Detail:        "too many rejects",
	}
	appendErr := store.AppendExecutionIncident(want)
	if appendErr != nil {
		t.Fatalf("AppendExecutionIncident() error = %v", appendErr)
	}
	reloadErr := store.reloadCaches()
	if reloadErr != nil {
		t.Fatalf("reloadCaches() error = %v", reloadErr)
	}

	got, listErr := store.ListExecutionIncidents(10)
	if listErr != nil {
		t.Fatalf("ListExecutionIncidents() error = %v", listErr)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 execution incident, got %d", len(got))
	}
	if got[0].Category != want.Category || got[0].Severity != want.Severity {
		t.Fatalf("risk metadata not persisted: got=%+v want=%+v", got[0], want)
	}
	if got[0].RunID != want.RunID || got[0].CycleID != want.CycleID {
		t.Fatalf("trace ids not persisted: got=%+v want=%+v", got[0], want)
	}
	if got[0].ConfigHash != want.ConfigHash || got[0].StrategyVersion != want.StrategyVersion {
		t.Fatalf("record metadata not persisted in cache: got=%+v want=%+v", got[0], want)
	}
	if got[0].Phase != want.Phase || got[0].Symbol != want.Symbol || got[0].Action != want.Action || got[0].Detail != want.Detail {
		t.Fatalf("execution incident fields drifted in cache: got=%+v want=%+v", got[0], want)
	}

	file, err := os.Open(filepath.Join(store.RecordDir(), executionCSVName))
	if err != nil {
		t.Fatalf("open execution jsonl: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	rows := make([]ExecutionIncidentRecord, 0, 2)
	for scanner.Scan() {
		var row ExecutionIncidentRecord
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("read execution jsonl: %v", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan execution jsonl: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 jsonl row, got %d rows", len(rows))
	}
	if rows[0].Category != want.Category || rows[0].Severity != want.Severity {
		t.Fatalf("json did not persist category/severity: row=%+v", rows[0])
	}
	if rows[0].RunID != want.RunID || rows[0].CycleID != want.CycleID {
		t.Fatalf("json did not persist trace ids: row=%+v", rows[0])
	}
	if rows[0].ConfigHash != want.ConfigHash || rows[0].StrategyVersion != want.StrategyVersion {
		t.Fatalf("json did not persist record metadata: row=%+v", rows[0])
	}
	if rows[0].Phase != want.Phase || rows[0].Symbol != want.Symbol || rows[0].Action != want.Action || rows[0].Detail != want.Detail {
		t.Fatalf("json fields not aligned after storage migration: row=%+v", rows[0])
	}
}

func TestReportStoreAppendDomainEventsPersistsProtocolEvents(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}

	occurredAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	want := DomainEvent{
		TraceRecordMetadata: TraceRecordMetadata{
			StrategyVersion: "funding-sniper-v1",
			ConfigHash:      "cfg-hash-2",
			AppVersion:      "dev",
			GitCommit:       "unknown",
		},
		Type:       EventTypeOpenExecuted,
		Level:      EventLevelInfo,
		OccurredAt: occurredAt,
		RunID:      "live-20260601T120000Z-abcd1234",
		CycleID:    "live-20260601T120000Z-abcd1234-cycle-ef567890",
		Protocol:   "open.execute",
		Stage:      LifecycleStageOpening,
		Successful: true,
		Reason:     "open sequence executed",
	}
	if err := store.AppendDomainEvents([]DomainEvent{want}); err != nil {
		t.Fatalf("AppendDomainEvents() error = %v", err)
	}
	if err := store.reloadCaches(); err != nil {
		t.Fatalf("reloadCaches() error = %v", err)
	}

	got, err := store.ListDomainEvents(10)
	if err != nil {
		t.Fatalf("ListDomainEvents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 domain event, got %d", len(got))
	}
	if got[0].Type != want.Type || got[0].Protocol != want.Protocol || got[0].Stage != want.Stage {
		t.Fatalf("domain event drifted in cache: got=%+v want=%+v", got[0], want)
	}
	if got[0].RunID != want.RunID || got[0].CycleID != want.CycleID {
		t.Fatalf("domain event trace ids not persisted: got=%+v want=%+v", got[0], want)
	}
	if !got[0].OccurredAt.Equal(occurredAt) {
		t.Fatalf("domain event occurred_at mismatch: got=%s want=%s", got[0].OccurredAt.Format(time.RFC3339), occurredAt.Format(time.RFC3339))
	}

	file, err := os.Open(filepath.Join(store.RecordDir(), domainEventJSONLName))
	if err != nil {
		t.Fatalf("open domain event jsonl: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	rows := make([]DomainEvent, 0, 2)
	for scanner.Scan() {
		var row DomainEvent
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("read domain event jsonl: %v", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan domain event jsonl: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 domain event row, got %d", len(rows))
	}
	if rows[0].Type != want.Type || rows[0].Reason != want.Reason || rows[0].Stage != want.Stage {
		t.Fatalf("json did not persist domain event fields: row=%+v", rows[0])
	}
}

func TestReportStoreInitializeSeedsBacktestRecords(t *testing.T) {
	root := t.TempDir()
	backtestDir := filepath.Join(root, "backtest")
	if err := os.MkdirAll(backtestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	samplePath := filepath.Join(backtestDir, "sample.csv")
	sampleCSV := "recorded_at,scenario,symbol,period,trades,win_rate,funding_income,trading_pnl,total_fees,net_pnl,max_drawdown,notes\n2026-05-31T00:00:00Z,baseline,BTCUSDT,2026-05,12,0.75,120.5,-10.2,8.1,102.2,15.4,seeded sample\n"
	if err := os.WriteFile(samplePath, []byte(sampleCSV), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := newReportStoreWithPaths(root)
	if err := store.initialize(samplePath, time.Now().UTC()); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}

	records, err := store.ListBacktestRecords(10)
	if err != nil {
		t.Fatalf("ListBacktestRecords() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected seeded backtest record, got %d", len(records))
	}
	if records[0].Symbol != "BTCUSDT" {
		t.Fatalf("unexpected seeded symbol: %+v", records[0])
	}
}

func TestReportStoreAppendMonitorSnapshotPersistsDepthRiskFields(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}

	target := &MonitoredTarget{
		RunID:   "live-20260531T010000Z-abcd1234",
		CycleID: "live-20260531T010000Z-abcd1234-cycle-ef567890",
		Candidate: FundingCandidate{
			Symbol:          "BTCUSDT",
			Pair:            "BTCUSDT",
			EstimatedRate:   0.0032,
			LastFundingRate: 0.0030,
			BasisRate:       0.0007,
			ScanScoreRate:   0.0025,
			NextFundingTime: time.Date(2026, 5, 31, 8, 0, 0, 0, time.UTC),
		},
		MonitorState:               "observing",
		BreachCount:                2,
		LastNetProfit:              0.0014,
		LastSpread:                 0.0006,
		LastDepthStabilityRatio:    0.61,
		LastSpotVisibleNotional:    1200,
		LastFuturesVisibleNotional: 980,
		LastMarketRiskCategory:     "depth_stability",
		LastMarketRiskSeverity:     "major",
		LastMarketRiskAction:       "symbol_cooldown",
		LastMarketRiskDetail:       "可见深度快速塌陷",
		InvalidReason:              "symbol 冷却: depth_stability severity=major action=symbol_cooldown",
	}

	if err := store.AppendMonitorSnapshot(time.Date(2026, 5, 31, 7, 59, 30, 0, time.UTC), []*MonitoredTarget{target}); err != nil {
		t.Fatalf("AppendMonitorSnapshot() error = %v", err)
	}
	got, err := store.ListMonitorRecords(10)
	if err != nil {
		t.Fatalf("ListMonitorRecords() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 monitor record, got %d", len(got))
	}
	if got[0].DepthStabilityRatio != "0.61" {
		t.Fatalf("unexpected depth ratio: %+v", got[0])
	}
	if got[0].SpotVisibleNotional != "1200" || got[0].FuturesVisibleNotional != "980" {
		t.Fatalf("unexpected visible notionals: %+v", got[0])
	}
	if got[0].MarketRiskCategory != "depth_stability" || got[0].MarketRiskAction != "symbol_cooldown" {
		t.Fatalf("unexpected market risk fields: %+v", got[0])
	}
	if got[0].MarketRiskDetail != "可见深度快速塌陷" {
		t.Fatalf("unexpected market risk detail: %+v", got[0])
	}
}

func TestReportStoreResidualStatsRefreshAfterReplaceAndClear(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}

	records := []ResidualRiskCSVRecord{
		{Scenario: "s1", ResidualUSDT: "10", EmergencyHedgePnL: "-0.01", Severity: "low"},
		{Scenario: "s1", ResidualUSDT: "90", EmergencyHedgePnL: "-0.20", Severity: "high"},
		{Scenario: "s2", ResidualUSDT: "50", EmergencyHedgePnL: "-0.05", Severity: "medium"},
	}
	replaceErr := store.ReplaceResidualRiskRecords(records)
	if replaceErr != nil {
		t.Fatalf("ReplaceResidualRiskRecords() error = %v", replaceErr)
	}

	severityStats, statsErr := store.ListResidualSeverityStats()
	if statsErr != nil {
		t.Fatalf("ListResidualSeverityStats() error = %v", statsErr)
	}
	if len(severityStats) != 3 || severityStats[0].Count != 1 || severityStats[1].Count != 1 || severityStats[2].Count != 1 {
		t.Fatalf("unexpected severity stats: %+v", severityStats)
	}

	scenarioStats, scenarioErr := store.ListResidualScenarioStats()
	if scenarioErr != nil {
		t.Fatalf("ListResidualScenarioStats() error = %v", scenarioErr)
	}
	if len(scenarioStats) != 2 {
		t.Fatalf("expected 2 scenario stats, got %d", len(scenarioStats))
	}
	if scenarioStats[0].Scenario != "s1" || scenarioStats[0].Count != 2 || scenarioStats[0].HighRiskCount != 1 {
		t.Fatalf("unexpected s1 stats: %+v", scenarioStats[0])
	}

	clearErr := store.ClearDashboardData()
	if clearErr != nil {
		t.Fatalf("ClearDashboardData() error = %v", clearErr)
	}
	cleared, listErr := store.ListResidualRiskRecords(10)
	if listErr != nil {
		t.Fatalf("ListResidualRiskRecords() after clear error = %v", listErr)
	}
	if len(cleared) != 0 {
		t.Fatalf("expected no residual records after clear, got %d", len(cleared))
	}
}

func TestReportStoreClearDashboardDataClearsDomainEvents(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	if err := store.AppendDomainEvents([]DomainEvent{{
		Type:       EventTypeOpenExecuted,
		Level:      EventLevelInfo,
		OccurredAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		RunID:      "run-1",
		CycleID:    "cycle-1",
		Protocol:   "open.execute",
		Stage:      LifecycleStageOpening,
		Successful: true,
		Reason:     "open sequence executed",
	}}); err != nil {
		t.Fatalf("AppendDomainEvents() error = %v", err)
	}
	if err := store.ClearDashboardData(); err != nil {
		t.Fatalf("ClearDashboardData() error = %v", err)
	}
	got, err := store.ListDomainEvents(10)
	if err != nil {
		t.Fatalf("ListDomainEvents() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no domain events after clear, got %d", len(got))
	}
}

func TestReportStoreAppendDustLedgerPersistsRecord(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	record := DustLedgerRecord{
		RunID:        "run-1",
		CycleID:      "cycle-1",
		RecordedAt:   "2026-05-30T00:00:00Z",
		Phase:        "open_recovery",
		Symbol:       "DOGEUSDT",
		Pair:         "DOGEUSDT",
		Asset:        "DOGE",
		Quantity:     "1",
		NotionalUSDT: "0.10000000",
		Reason:       "spot_excess_below_min_notional",
	}
	if err := store.AppendDustLedger(record); err != nil {
		t.Fatalf("AppendDustLedger() error = %v", err)
	}
	got, err := store.ListDustLedgerRecords(10)
	if err != nil {
		t.Fatalf("ListDustLedgerRecords() error = %v", err)
	}
	if len(got) != 1 || got[0].Reason != record.Reason || got[0].Asset != "DOGE" {
		t.Fatalf("unexpected dust ledger records: %+v", got)
	}
}

func TestReportStorePendingFundingSettlementPersistsLifecycle(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	record := PendingFundingSettlementRecord{
		RunID:         "run-1",
		CycleID:       "cycle-1",
		RecordedAt:    "2026-05-30T00:00:00Z",
		NextAttemptAt: "2026-05-30T00:00:10Z",
		AttemptCount:  1,
		Settlement: CycleSettlement{
			RunID:                "run-1",
			CycleID:              "cycle-1",
			FundingIncomePending: true,
		},
		Hedges: []OpenHedge{{
			Target: FundingCandidate{Symbol: "BTCUSDT"},
		}},
	}
	if err := store.UpsertPendingFundingSettlement(record); err != nil {
		t.Fatalf("UpsertPendingFundingSettlement() error = %v", err)
	}

	got, err := store.ListPendingFundingSettlements(10)
	if err != nil {
		t.Fatalf("ListPendingFundingSettlements() error = %v", err)
	}
	if len(got) != 1 || got[0].CycleID != record.CycleID || got[0].AttemptCount != 1 {
		t.Fatalf("unexpected pending funding records: %+v", got)
	}

	record.AttemptCount = 2
	record.LastError = "still pending"
	if err := store.UpsertPendingFundingSettlement(record); err != nil {
		t.Fatalf("UpsertPendingFundingSettlement() update error = %v", err)
	}
	got, err = store.ListPendingFundingSettlements(10)
	if err != nil {
		t.Fatalf("ListPendingFundingSettlements() after update error = %v", err)
	}
	if len(got) != 1 || got[0].AttemptCount != 2 || got[0].LastError != "still pending" {
		t.Fatalf("unexpected updated pending funding records: %+v", got)
	}

	if err := store.DeletePendingFundingSettlement(record.RunID, record.CycleID); err != nil {
		t.Fatalf("DeletePendingFundingSettlement() error = %v", err)
	}
	got, err = store.ListPendingFundingSettlements(10)
	if err != nil {
		t.Fatalf("ListPendingFundingSettlements() after delete error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected pending funding records to be deleted, got %+v", got)
	}
}

func TestCompactJSONLByRecordedAtKeepsRecentRowsWhenOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), executionCSVName)
	if err := rewriteJSONLFile(path, []ExecutionIncidentRecord{
		{
			RecordedAt:    time.Now().UTC().Add(-13 * time.Hour).Format(time.RFC3339),
			Level:         "incident",
			Category:      RiskCategoryExecution,
			Severity:      RiskSeverityWarning,
			Phase:         "open",
			Symbol:        "BTCUSDT",
			Action:        "timeout",
			IncidentCount: 1,
			IncidentLimit: 3,
			Fatal:         false,
			Detail:        "old",
		},
		{
			RecordedAt:    time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			Level:         "action",
			Category:      RiskCategoryRecovery,
			Severity:      RiskSeverityInfo,
			Phase:         "recovery",
			Symbol:        "ETHUSDT",
			Action:        "query",
			IncidentCount: 1,
			IncidentLimit: 3,
			Fatal:         false,
			Detail:        "recent",
		},
	}); err != nil {
		t.Fatalf("rewriteJSONLFile() error = %v", err)
	}

	compacted, err := compactJSONLByRecordedAt(path, time.Now().UTC(), 12*time.Hour, 1)
	if err != nil {
		t.Fatalf("compactJSONLByRecordedAt() error = %v", err)
	}
	if !compacted {
		t.Fatal("expected jsonl compaction to run")
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open compacted jsonl: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	rows := make([]ExecutionIncidentRecord, 0, 2)
	for scanner.Scan() {
		var row ExecutionIncidentRecord
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("read compacted jsonl: %v", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan compacted jsonl: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected only recent row after compaction, got %d rows", len(rows))
	}
	if rows[0].Detail != "recent" {
		t.Fatalf("expected only recent row to remain, got %+v", rows[0])
	}
}

func TestExecutionIncidentRecordDomainEvent(t *testing.T) {
	record := ExecutionIncidentRecord{
		TraceRecordMetadata: TraceRecordMetadata{
			StrategyVersion: "funding-sniper-v1",
		},
		RunID:         "run-1",
		CycleID:       "cycle-1",
		RecordedAt:    "2026-06-01T12:00:00Z",
		Category:      RiskCategoryRecovery,
		Severity:      RiskSeverityWarning,
		Phase:         "recovery_close",
		Action:        "recover",
		IncidentCount: 2,
		IncidentLimit: 3,
		Fatal:         false,
		Detail:        "close leg recovered",
	}

	event, ok := record.DomainEvent()
	if !ok {
		t.Fatal("expected execution incident to map to domain event")
	}
	if event.Type != EventTypeRecoveryEscalated || event.Protocol != "legacy.recovery" {
		t.Fatalf("unexpected compatible domain event: %+v", event)
	}
	if event.Stage != LifecycleStageRecovering {
		t.Fatalf("unexpected lifecycle stage: %+v", event)
	}
	if event.RunID != "run-1" || event.CycleID != "cycle-1" {
		t.Fatalf("unexpected trace ids: %+v", event)
	}
}
