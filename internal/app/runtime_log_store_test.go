package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeLogStoreRotateIfNeededLocked(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), runtimeEventLogJSONLName)
	if err := os.WriteFile(basePath, []byte("{\"recorded_at\":\"2026-05-28T23:59:59Z\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := &RuntimeLogStore{
		path:       basePath,
		currentDay: "20260528",
	}

	if err := store.rotateIfNeededLocked(time.Date(2026, 5, 29, 0, 0, 1, 0, time.UTC)); err != nil {
		t.Fatalf("rotateIfNeededLocked() error = %v", err)
	}

	rotatedPath := runtimeLogRotatedPath(basePath, "20260528")
	if _, err := os.Stat(rotatedPath); err != nil {
		t.Fatalf("expected rotated file to exist, stat error = %v", err)
	}
	if store.currentDay != "20260529" {
		t.Fatalf("unexpected current day after rotate: %s", store.currentDay)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected fresh base file to exist, stat error = %v", err)
	}
}

func TestNewRuntimeLogStoreArchivesStaleBaseFileOnStartup(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), runtimeEventLogJSONLName)
	staleLog := "{\"recorded_at\":\"2026-05-28T23:59:59Z\",\"message\":\"stale\"}\n"
	if err := os.WriteFile(basePath, []byte(staleLog), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &RuntimeLogStore{
		path:       basePath,
		currentDay: "20260529",
	}
	if err := store.archiveBaseFileToDayLocked("20260528"); err != nil {
		t.Fatalf("archiveBaseFileToDayLocked() error = %v", err)
	}

	rotatedPath := runtimeLogRotatedPath(basePath, "20260528")
	payload, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("ReadFile() rotated error = %v", err)
	}
	if string(payload) != staleLog {
		t.Fatalf("unexpected rotated payload: %q", string(payload))
	}
}

func TestNewRuntimeLogStoreMissingBaseWithRotatedOnly(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, runtimeEventLogJSONLName)
	rotatedPath := runtimeLogRotatedPath(basePath, "20260612")
	oldLog := "{\"recorded_at\":\"2026-06-12T10:00:00Z\",\"message\":\"archived\"}\n"
	if err := os.WriteFile(rotatedPath, []byte(oldLog), 0o644); err != nil {
		t.Fatalf("WriteFile() rotated error = %v", err)
	}

	store, err := NewRuntimeLogStore(basePath)
	if err != nil {
		t.Fatalf("NewRuntimeLogStore() error = %v", err)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected base file created, stat error = %v", err)
	}
	if err := store.Append(RuntimeLogRecord{RecordedAt: "2026-06-13T10:00:00Z", Message: "fresh"}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	payload, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile() base error = %v", err)
	}
	if !strings.Contains(string(payload), "fresh") {
		t.Fatalf("expected append payload in base file, got %q", string(payload))
	}
}

func TestNewRuntimeLogStoreArchivesStaleBaseWhenRotatedExists(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, runtimeEventLogJSONLName)
	rotatedPath := runtimeLogRotatedPath(basePath, "20260612")
	if err := os.WriteFile(rotatedPath, []byte("{\"recorded_at\":\"2026-06-12T08:00:00Z\",\"message\":\"old\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() rotated error = %v", err)
	}
	if err := os.WriteFile(basePath, []byte("{\"recorded_at\":\"2026-06-12T23:00:00Z\",\"message\":\"stale base\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() base error = %v", err)
	}

	store, err := NewRuntimeLogStore(basePath)
	if err != nil {
		t.Fatalf("NewRuntimeLogStore() error = %v", err)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected base file after startup archive, stat error = %v", err)
	}
	rotatedPayload, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("ReadFile() rotated error = %v", err)
	}
	if !strings.Contains(string(rotatedPayload), "stale base") {
		t.Fatalf("expected stale base merged into rotated file, got %q", string(rotatedPayload))
	}
	if err := store.Append(RuntimeLogRecord{RecordedAt: "2026-06-13T10:00:00Z", Message: "after archive"}); err != nil {
		t.Fatalf("Append() after startup archive error = %v", err)
	}
}

func TestRuntimeLogStoreAppendAfterDayRotation(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), runtimeEventLogJSONLName)
	if err := os.WriteFile(basePath, []byte("{\"recorded_at\":\"2026-05-28T23:59:59Z\",\"message\":\"yesterday\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := &RuntimeLogStore{
		path:       basePath,
		currentDay: "20260528",
	}

	if err := store.rotateIfNeededLocked(time.Date(2026, 5, 29, 0, 0, 1, 0, time.UTC)); err != nil {
		t.Fatalf("rotateIfNeededLocked() error = %v", err)
	}
	rotatedPath := runtimeLogRotatedPath(basePath, "20260528")
	if _, err := os.Stat(rotatedPath); err != nil {
		t.Fatalf("expected rotated file retained, stat error = %v", err)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("expected fresh base file after rotation, stat error = %v", err)
	}
	if err := store.Append(RuntimeLogRecord{RecordedAt: "2026-05-29T00:00:01Z", Message: "today"}); err != nil {
		t.Fatalf("Append() after rotation error = %v", err)
	}
	payload, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile() base error = %v", err)
	}
	if !strings.Contains(string(payload), "today") {
		t.Fatalf("expected append in base file, got %q", string(payload))
	}
}

func TestRuntimeLogStoreClearRemovesRotatedFiles(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), runtimeEventLogJSONLName)
	rotatedPath := runtimeLogRotatedPath(basePath, "20260528")
	if err := os.WriteFile(basePath, []byte("{\"recorded_at\":\"2026-05-29T00:00:01Z\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() base error = %v", err)
	}
	if err := os.WriteFile(rotatedPath, []byte("{\"recorded_at\":\"2026-05-28T23:59:59Z\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() rotated error = %v", err)
	}

	store := &RuntimeLogStore{
		path:       basePath,
		currentDay: "20260529",
		records:    []RuntimeLogRecord{{RecordedAt: "2026-05-29T00:00:01Z"}},
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	if _, err := os.Stat(rotatedPath); !os.IsNotExist(err) {
		t.Fatalf("expected rotated file removed, stat err = %v", err)
	}
	payload, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("ReadFile() base error = %v", err)
	}
	if len(payload) != 0 {
		t.Fatalf("expected cleared base file, got %q", string(payload))
	}
	if len(store.records) != 0 {
		t.Fatalf("expected in-memory records cleared, got %d", len(store.records))
	}
}
