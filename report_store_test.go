package main

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
