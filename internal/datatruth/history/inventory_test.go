package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInventoryResumeProducesCatalogHashes(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "okx_1h", "btc.jsonl",
		`{"exchange":"okx","symbol":"BTC-USDT","ts":"1710000000000","data":[["1710000000000","1","2","3"],["1710003600000","1","2","3"]]}`,
		`{"exchange":"okx","symbol":"BTC-USDT","ts":"1710007200000","data":[["1710007200000","4","5","6"]]}`,
	)
	checkpoint := filepath.Join(root, "inventory.checkpoint.json")
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	first, err := Inventory(context.Background(), Config{
		RootDir:       root,
		Families:      []string{"okx_1h"},
		MaxLines:      1,
		ProgressEvery: 1,
		Checkpoint:    checkpoint,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("first inventory: %v", err)
	}
	if first.Completed {
		t.Fatalf("first run should stop early by max-lines")
	}
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}

	second, err := Inventory(context.Background(), Config{
		RootDir:       root,
		Families:      []string{"okx_1h"},
		ProgressEvery: 1,
		Checkpoint:    checkpoint,
		Now:           func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatalf("resume inventory: %v", err)
	}
	if !second.Completed {
		t.Fatalf("resume should complete")
	}
	if second.Catalog.DatasetID == "" || second.Catalog.DataHash == "" || second.Catalog.SchemaHash == "" {
		t.Fatalf("catalog hashes missing: %+v", second.Catalog)
	}
	if second.Catalog.FilesScanned != 1 || second.Catalog.LinesScanned != 2 {
		t.Fatalf("unexpected scan counters: %+v", second.Catalog)
	}
	if len(second.Catalog.Families) != 1 || len(second.Catalog.Families[0].Files) != 1 {
		t.Fatalf("unexpected family file summary: %+v", second.Catalog.Families)
	}
	if _, err := os.Stat(checkpointDonePath(checkpoint)); err != nil {
		t.Fatalf(".done marker missing: %v", err)
	}

	third, err := Inventory(context.Background(), Config{
		RootDir:       root,
		Families:      []string{"okx_1h"},
		ProgressEvery: 1,
		Checkpoint:    checkpoint,
	})
	if err != nil {
		t.Fatalf("completed rerun must be idempotent: %v", err)
	}
	if !third.Completed || third.Catalog.DataHash != second.Catalog.DataHash {
		t.Fatalf("unexpected rerun catalog: %+v", third.Catalog)
	}
}

func TestInventoryFailsClosedWhenConfigChanges(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "okx_1h", "btc.jsonl",
		`{"ts":"1710000000000","data":[["1710000000000","1"]]}`,
		`{"ts":"1710003600000","data":[["1710003600000","2"]]}`,
	)
	checkpoint := filepath.Join(root, "inventory.checkpoint.json")
	if _, err := Inventory(context.Background(), Config{
		RootDir:    root,
		MaxFiles:   1,
		MaxLines:   1,
		Checkpoint: checkpoint,
	}); err != nil {
		t.Fatalf("first inventory: %v", err)
	}
	_, err := Inventory(context.Background(), Config{
		RootDir:    root,
		MaxFiles:   2,
		MaxLines:   2,
		Checkpoint: checkpoint,
	})
	if err == nil || !strings.Contains(err.Error(), "input or config changed") {
		t.Fatalf("expected config change fail-closed, got %v", err)
	}
}

func TestInventoryFailsClosedWhenFileTruncated(t *testing.T) {
	root := t.TempDir()
	path := writeFixtureFile(t, root, "okx_5m", "eth.jsonl",
		`{"ts":"1710000000000","data":[["1710000000000","1"]]}`,
		`{"ts":"1710000300000","data":[["1710000300000","2"]]}`,
	)
	checkpoint := filepath.Join(root, "inventory.checkpoint.json")
	if _, err := Inventory(context.Background(), Config{
		RootDir:    root,
		MaxLines:   1,
		Checkpoint: checkpoint,
	}); err != nil {
		t.Fatalf("first inventory: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"ts":"1710000000000","data":[["1710000000000","1"]]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Inventory(context.Background(), Config{
		RootDir:    root,
		Checkpoint: checkpoint,
	})
	if err == nil || !strings.Contains(err.Error(), "file truncated") {
		t.Fatalf("expected truncation fail-closed, got %v", err)
	}
}

func TestInventoryFailsClosedOnDuplicateFileHash(t *testing.T) {
	root := t.TempDir()
	line := `{"ts":"1710000000000","data":[["1710000000000","1"]]}`
	writeFixtureFile(t, root, "bybit_funding", "a.jsonl", line)
	writeFixtureFile(t, root, "bybit_funding", "b.jsonl", line)
	_, err := Inventory(context.Background(), Config{RootDir: root})
	if err == nil || !strings.Contains(err.Error(), "duplicate file hash") {
		t.Fatalf("expected duplicate hash fail-closed, got %v", err)
	}
}

func TestInventoryAllowsDistinctEmptyCompletedFilesButMarksThemEmpty(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "rubik_taker_1h", "a.jsonl")
	writeFixtureFile(t, root, "rubik_taker_1h", "b.jsonl")
	result, err := Inventory(context.Background(), Config{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || len(result.Catalog.Families) != 1 || len(result.Catalog.Families[0].Files) != 2 {
		t.Fatalf("unexpected catalog: %+v", result.Catalog)
	}
	for _, file := range result.Catalog.Families[0].Files {
		if !file.Empty {
			t.Fatalf("empty completed file must be explicit: %+v", file)
		}
	}
}

func TestInventoryFailsClosedOnDoneWithoutCompletedCheckpoint(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "rubik_taker_1h", "r.jsonl",
		`{"ts":"1710000000000","data":[["1710000000000","1"]]}`,
	)
	checkpoint := filepath.Join(root, "inventory.checkpoint.json")
	if _, err := Inventory(context.Background(), Config{
		RootDir:    root,
		MaxLines:   1,
		Checkpoint: checkpoint,
	}); err != nil {
		t.Fatalf("first inventory: %v", err)
	}
	if err := os.WriteFile(checkpointDonePath(checkpoint), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Inventory(context.Background(), Config{
		RootDir:    root,
		Checkpoint: checkpoint,
	})
	if err == nil || !strings.Contains(err.Error(), ".done exists but checkpoint is not completed") {
		t.Fatalf("expected done mismatch fail-closed, got %v", err)
	}
}

func TestInventoryRejectsSourceWithoutDownloaderDoneMarker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "okx_1h")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partial.jsonl"), []byte(`{"ts":"1710000000000"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inventory(context.Background(), Config{RootDir: root}); err == nil ||
		!strings.Contains(err.Error(), "not sealed by downloader .done") {
		t.Fatalf("source without .done must fail closed, got %v", err)
	}
}

func TestParseLineRecognizesBybitFundingTimestamp(t *testing.T) {
	_, minTime, maxTime, err := parseLine([]byte(`{"data":{"fundingRateTimestamp":"1710000000000"},"fetched_at":1710000001000}`))
	if err != nil {
		t.Fatal(err)
	}
	if minTime.IsZero() || maxTime.IsZero() || minTime.After(maxTime) {
		t.Fatalf("funding/fetched timestamps not recognized: min=%v max=%v", minTime, maxTime)
	}
}

func TestValidateCatalogRejectsDeclaredHashForgery(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "okx_1h", "btc.jsonl", `{"ts":"1710000000000"}`)
	result, err := Inventory(context.Background(), Config{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalog(result.Catalog); err != nil {
		t.Fatal(err)
	}
	tampered := result.Catalog
	tampered.DataHash = "forged"
	if err := ValidateCatalog(tampered); err == nil {
		t.Fatal("forged declared catalog hash must fail")
	}
}

func writeFixtureFile(t *testing.T, root, family, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, family)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".done", []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
