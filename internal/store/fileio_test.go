package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

type sampleRecord struct {
	Symbol     string `json:"symbol"`
	RecordedAt string `json:"recorded_at"`
	Value      int    `json:"value"`
}

func TestJSONFileRoundTripAndEnsure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "records.json")
	// 读取不存在的文件应补建为空数组而非报错。
	got, err := ReadJSONFile[sampleRecord](path)
	if err != nil {
		t.Fatalf("ReadJSONFile() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
	want := []sampleRecord{{Symbol: "BTCUSDT", Value: 1}, {Symbol: "ETHUSDT", Value: 2}}
	if err := WriteJSONFile(path, want); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}
	back, err := ReadJSONFile[sampleRecord](path)
	if err != nil {
		t.Fatalf("ReadJSONFile() error = %v", err)
	}
	if len(back) != 2 || back[0].Symbol != "BTCUSDT" || back[1].Value != 2 {
		t.Fatalf("round trip mismatch: %+v", back)
	}
}

func TestJSONLAppendReadRewriteClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := AppendJSONLRecord(path, sampleRecord{Symbol: "A", Value: 1}); err != nil {
		t.Fatalf("AppendJSONLRecord() error = %v", err)
	}
	if err := AppendJSONLRecords(path, []sampleRecord{{Symbol: "B", Value: 2}, {Symbol: "C", Value: 3}}); err != nil {
		t.Fatalf("AppendJSONLRecords() error = %v", err)
	}
	rows, err := ReadJSONLFile[sampleRecord](path)
	if err != nil {
		t.Fatalf("ReadJSONLFile() error = %v", err)
	}
	if len(rows) != 3 || rows[0].Symbol != "A" || rows[2].Value != 3 {
		t.Fatalf("unexpected jsonl rows: %+v", rows)
	}
	if err := RewriteJSONLFile(path, []sampleRecord{{Symbol: "Z", Value: 9}}); err != nil {
		t.Fatalf("RewriteJSONLFile() error = %v", err)
	}
	rows, _ = ReadJSONLFile[sampleRecord](path)
	if len(rows) != 1 || rows[0].Symbol != "Z" {
		t.Fatalf("rewrite mismatch: %+v", rows)
	}
	if err := ClearJSONLFile(path); err != nil {
		t.Fatalf("ClearJSONLFile() error = %v", err)
	}
	rows, _ = ReadJSONLFile[sampleRecord](path)
	if len(rows) != 0 {
		t.Fatalf("expected cleared file, got %d rows", len(rows))
	}
}

func TestCSVRoundTripAndColumnIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table.csv")
	header := []string{"recorded_at", "symbol", "value"}
	if err := EnsureCSVHeader(path, header); err != nil {
		t.Fatalf("EnsureCSVHeader() error = %v", err)
	}
	if err := AppendCSVRow(path, []string{"2026-06-03T00:00:00Z", "BTCUSDT", "1"}); err != nil {
		t.Fatalf("AppendCSVRow() error = %v", err)
	}
	rows, err := ReadCSVRows(path)
	if err != nil {
		t.Fatalf("ReadCSVRows() error = %v", err)
	}
	if len(rows) != 1 || rows[0][1] != "BTCUSDT" {
		t.Fatalf("unexpected csv rows: %+v", rows)
	}
	if idx := CSVColumnIndex(header, "value"); idx != 2 {
		t.Fatalf("CSVColumnIndex=%d want 2", idx)
	}
	if idx := CSVColumnIndex(header, "missing"); idx != -1 {
		t.Fatalf("CSVColumnIndex missing=%d want -1", idx)
	}
}

func TestCompactJSONByRecordedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.json")
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	old := now.Add(-72 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-1 * time.Hour).Format(time.RFC3339)
	rows := []sampleRecord{{Symbol: "OLD", RecordedAt: old}, {Symbol: "FRESH", RecordedAt: fresh}}
	if err := WriteJSONFile(path, rows); err != nil {
		t.Fatalf("WriteJSONFile() error = %v", err)
	}
	// maxFileSizeBytes=1 强制触发压缩；retention=24h 应剔除 72h 前的记录。
	compacted, err := CompactJSONByRecordedAt(path, now, 24*time.Hour, 1)
	if err != nil {
		t.Fatalf("CompactJSONByRecordedAt() error = %v", err)
	}
	if !compacted {
		t.Fatalf("expected compaction to occur")
	}
	back, _ := ReadJSONFile[sampleRecord](path)
	if len(back) != 1 || back[0].Symbol != "FRESH" {
		t.Fatalf("compaction kept wrong rows: %+v", back)
	}
}

func TestParseCSVRecordedAt(t *testing.T) {
	if _, ok := ParseCSVRecordedAt("  "); ok {
		t.Fatalf("blank should not parse")
	}
	if ts, ok := ParseCSVRecordedAt("2026-06-03T01:02:03Z"); !ok || ts.Year() != 2026 {
		t.Fatalf("RFC3339 parse failed: %v ok=%v", ts, ok)
	}
	if ts, ok := ParseCSVRecordedAt("2026-06-03T01:02:03.123456Z"); !ok || ts.Nanosecond() == 0 {
		t.Fatalf("RFC3339Nano parse failed: %v ok=%v", ts, ok)
	}
}

func TestMigrateLegacyJSONFileFromCSV(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "out.json")
	csvPath := filepath.Join(dir, "legacy.csv")
	csvBody := "symbol,value\nBTCUSDT,7\n"
	if err := os.WriteFile(csvPath, []byte(csvBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	parser := func(rows [][]string) any {
		out := make([]sampleRecord, 0, len(rows))
		for _, row := range rows {
			if len(row) >= 2 {
				out = append(out, sampleRecord{Symbol: row[0]})
			}
		}
		return out
	}
	if err := MigrateLegacyJSONFile(jsonPath, csvPath, parser); err != nil {
		t.Fatalf("MigrateLegacyJSONFile() error = %v", err)
	}
	back, _ := ReadJSONFile[sampleRecord](jsonPath)
	if len(back) != 1 || back[0].Symbol != "BTCUSDT" {
		t.Fatalf("migration mismatch: %+v", back)
	}
}
