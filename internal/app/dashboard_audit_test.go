package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDashboardAuditStoreAppendPersistsRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), dashboardAuditLogJSONLName)
	store, err := NewDashboardAuditStore(path)
	if err != nil {
		t.Fatalf("NewDashboardAuditStore() error = %v", err)
	}

	want := newDashboardAuditRecord(
		"start",
		true,
		"后台任务已启动",
		"dashboard",
		"127.0.0.1:12345",
		"abcd1234",
		"live-20260529T120000Z-a1b2c3d4",
		"live-20260529T120000Z-a1b2c3d4-cycle-e5f60708",
		"binance",
		TraceRecordMetadata{
			StrategyVersion: "funding-sniper-v1",
			ConfigHash:      "cfg123",
			AppVersion:      "dev",
			GitCommit:       "unknown",
		},
	)
	if err := store.Append(want); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	file, openErr := os.Open(path)
	if openErr != nil {
		t.Fatalf("Open() error = %v", openErr)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("expected one audit line")
	}
	var got DashboardAuditRecord
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.Action != want.Action || got.Detail != want.Detail || !got.Success {
		t.Fatalf("unexpected audit record: got=%+v want=%+v", got, want)
	}
	if got.RunID != want.RunID || got.CycleID != want.CycleID || got.ConfigHash != want.ConfigHash {
		t.Fatalf("unexpected audit trace metadata: got=%+v want=%+v", got, want)
	}
}
