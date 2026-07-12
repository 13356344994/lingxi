package fingerprint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSources(t *testing.T) SourcePaths {
	dir := filepath.Join("testdata")
	return SourcePaths{
		Settlements:      filepath.Join(dir, "settlements.jsonl"),
		MakerAttempts:    filepath.Join(dir, "maker_attempts.jsonl"),
		OpenDecisions:    filepath.Join(dir, "open_decisions.jsonl"),
		ExecutableShadow: filepath.Join(dir, "executable_shadow.jsonl"),
		EquitySnapshots:  filepath.Join(dir, "equity_snapshots.jsonl"),
		InstrumentMeta:   filepath.Join(dir, "instrument_meta.json"),
	}
}

func runFixture(t *testing.T, sources SourcePaths, maxLines int) RunResult {
	t.Helper()
	outDir := t.TempDir()
	cpPath := filepath.Join(t.TempDir(), "checkpoint.json")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		RecordRoot:    t.TempDir(),
		OutputDir:     outDir,
		Sources:       sources,
		MaxLines:      maxLines,
	}
	runner := Runner{Config: cfg, CheckpointPath: cpPath}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func TestPercentileAggregation(t *testing.T) {
	result := runFixture(t, testSources(t), 100)
	if len(result.Fingerprint.Cells) == 0 {
		t.Fatal("expected cells")
	}
	var btcSettlement *ExecutionCell
	var btcMakerAttempt *ExecutionCell
	for i := range result.Fingerprint.Cells {
		c := &result.Fingerprint.Cells[i]
		if c.Symbol != "BTC-USDT" || c.Direction != "long" {
			continue
		}
		if c.OrderType == "maker" && c.NotionalBucket == "small" {
			btcSettlement = c
		}
		if c.OrderType == "maker" && c.NotionalBucket == "unknown" {
			btcMakerAttempt = c
		}
	}
	if btcSettlement == nil {
		t.Fatal("expected BTC-USDT maker small settlement cell")
	}
	if btcSettlement.FeeBPS.N < 1 || btcSettlement.FeeBPS.Status != StatusAvailable {
		t.Fatalf("fee bps: %+v", btcSettlement.FeeBPS)
	}
	if btcMakerAttempt == nil {
		t.Fatal("expected BTC-USDT maker attempt cell")
	}
	if btcMakerAttempt.MakerFillRate.N < 1 {
		t.Fatalf("maker fill: %+v", btcMakerAttempt.MakerFillRate)
	}
	if btcMakerAttempt.MakerWaitMS.P50 <= 0 {
		t.Fatalf("maker wait p50: %+v", btcMakerAttempt.MakerWaitMS)
	}
	if btcMakerAttempt.LatencyMS.P90 <= 0 {
		t.Fatalf("latency p90: %+v", btcMakerAttempt.LatencyMS)
	}
}

func TestPercentileExactValues(t *testing.T) {
	got := finalizeQuantile([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if got.P50 != 5.5 {
		t.Fatalf("p50=%v want 5.5", got.P50)
	}
	if got.P90 != 9.1 {
		t.Fatalf("p90=%v want 9.1", got.P90)
	}
}

func TestMissingFilesMarkedUnknown(t *testing.T) {
	sources := testSources(t)
	sources.Settlements = filepath.Join(t.TempDir(), "missing.jsonl")
	sources.MakerAttempts = ""
	result := runFixture(t, sources, 50)
	if result.Fingerprint.SourceStatus["settlements"].Status != StatusUnknown {
		t.Fatalf("settlements: %+v", result.Fingerprint.SourceStatus["settlements"])
	}
	if result.Fingerprint.SourceStatus["maker_attempts"].Status != StatusUnknown {
		t.Fatalf("maker_attempts: %+v", result.Fingerprint.SourceStatus["maker_attempts"])
	}
	if result.Fingerprint.Equity.Status != StatusAvailable {
		t.Fatalf("equity: %+v", result.Fingerprint.Equity)
	}
}

func TestSchemaAndHash(t *testing.T) {
	result := runFixture(t, testSources(t), 100)
	fp := result.Fingerprint
	if err := ValidateFingerprintSchema(fp); err != nil {
		t.Fatalf("ValidateFingerprintSchema: %v", err)
	}
	raw, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PersonalExecutionFingerprint
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFingerprintSchema(decoded); err != nil {
		t.Fatalf("decoded schema: %v", err)
	}
}

func TestTamperDetection(t *testing.T) {
	result := runFixture(t, testSources(t), 100)
	cpPath := filepath.Join(filepath.Dir(result.OutputPath), "..", "checkpoint.json")
	// checkpoint lives in separate temp dir; rebuild path from runner
	outDir := filepath.Dir(result.OutputPath)
	cpFiles, _ := filepath.Glob(filepath.Join(filepath.Dir(outDir), "*", "checkpoint.json"))
	_ = cpFiles

	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		RecordRoot:    t.TempDir(),
		OutputDir:     t.TempDir(),
		Sources:       testSources(t),
		MaxLines:      100,
	}
	cpPath = filepath.Join(t.TempDir(), "checkpoint.json")
	runner := Runner{Config: cfg, CheckpointPath: cpPath}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), StatusAvailable, "TAMPERED", 1))
	if err := os.WriteFile(cpPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadCheckpoint(cpPath); err == nil || ok {
		t.Fatalf("expected tamper error, ok=%v err=%v", ok, err)
	}

	fp := result.Fingerprint
	fp.FingerprintHash = "deadbeef"
	if err := ValidateFingerprintSchema(fp); err == nil {
		t.Fatal("expected fingerprint hash tamper error")
	}
}

func TestRedactionRejectsForbiddenFields(t *testing.T) {
	leaky := map[string]any{
		"schema_version": SchemaVersion,
		"position_id":    "secret-position",
		"cells":          []any{},
	}
	raw, err := json.Marshal(leaky)
	if err != nil {
		t.Fatal(err)
	}
	if err := AssertNoForbiddenFields(raw); err == nil {
		t.Fatal("expected forbidden field rejection")
	}
	redacted := RedactMap(leaky)
	if _, ok := redacted["position_id"]; ok {
		t.Fatal("position_id should be redacted")
	}
}

func TestOutputHasNoForbiddenFields(t *testing.T) {
	result := runFixture(t, testSources(t), 100)
	raw, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := AssertNoForbiddenFields(raw); err != nil {
		t.Fatalf("output leak: %v", err)
	}
	body := string(raw)
	for _, forbidden := range []string{"order_id", "position_id", "snapshot_id", "token", "raw_line"} {
		if strings.Contains(body, forbidden) {
			// field names may appear in source_status keys only as part of path names; ensure not as JSON keys
			if strings.Contains(body, "\""+forbidden+"\"") {
				t.Fatalf("forbidden key %q in output", forbidden)
			}
		}
	}
}

func TestInstrumentMetaUnknownWhenMissing(t *testing.T) {
	sources := testSources(t)
	sources.InstrumentMeta = filepath.Join(t.TempDir(), "missing.json")
	result := runFixture(t, sources, 100)
	if result.Fingerprint.SourceStatus["instrument_meta"].Status != StatusUnknown {
		t.Fatalf("instrument_meta status: %+v", result.Fingerprint.SourceStatus["instrument_meta"])
	}
	foundUnknown := false
	for _, cell := range result.Fingerprint.Cells {
		if cell.Instrument.Status == StatusUnknown {
			foundUnknown = true
			break
		}
	}
	if !foundUnknown {
		t.Fatal("expected at least one cell with UNKNOWN instrument constraints")
	}
}

func TestCheckpointResumeBinding(t *testing.T) {
	outDir := t.TempDir()
	cpPath := filepath.Join(t.TempDir(), "checkpoint.json")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		RecordRoot:    t.TempDir(),
		OutputDir:     outDir,
		Sources:       testSources(t),
		MaxLines:      2,
	}
	runner := Runner{Config: cfg, CheckpointPath: cpPath}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.OutputDir = t.TempDir()
	runner.Config = cfg
	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("expected output dir mismatch on resume")
	}
}

func TestNotionalBuckets(t *testing.T) {
	if notionalBucket(25) != "micro" {
		t.Fatal("micro")
	}
	if notionalBucket(50) != "small" {
		t.Fatal("small")
	}
	if notionalBucket(200) != "medium" {
		t.Fatal("medium")
	}
	if notionalBucket(600) != "large" {
		t.Fatal("large")
	}
}
