package evidenceaudit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"lingxi/internal/datatruth"
)

func TestBoundedExternalProjectionManyRowsAndRedaction(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 256*1024)
	const unique = 100000
	for i := 0; i < unique; i++ {
		id := fmt.Sprintf("secret-trade-%06d", i)
		fmt.Fprintf(w, `{"tradeId":%q,"fee_usdt":1,"notional":100,"realized_usdt":999}`+"\n", id)
		fmt.Fprintf(w, `{"tradeId":%q,"fee_usdt":1,"notional":100,"realized_usdt":-999}`+"\n", id)
	}
	fmt.Fprintln(w, `{"tradeId":"secret-trade-000000","fee_usdt":2,"notional":100,"realized_usdt":123}`)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "manifest"
	cfg := Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt",
		ProjectBuckets: 128, MaxBucketRows: 2500, CheckpointEvery: 250000,
	}
	manifest := SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}
	stats, _, err := runProjectFixture(context.Background(), cfg, manifest, nil)
	if err != nil {
		t.Fatal(err)
	}
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	// Rows counts unique identities, including the conflicting identity.
	// That identity keeps one denominator slot but its fee fact is UNKNOWN.
	if stats.ProjectedTotal != 200001 || stats.Rows != unique || stats.Duplicates != unique || stats.Conflicts != 1 ||
		fee.Denominator != unique || fee.Numerator != unique-1 || fee.Reasons["SOURCE_CONFLICT"] != 1 ||
		stats.Known["fee_bps"] != unique-1 || stats.Unknown["fee_bps"] != 1 {
		t.Fatalf("unexpected aggregate %#v", stats)
	}
	if stats.PeakBufferedRows > 2500 || stats.PeakBufferedRows >= stats.ProjectedTotal {
		t.Fatalf("unbounded instrumentation %#v", stats)
	}
	entries, err := os.ReadDir(stats.StagingDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw := mustRead(t, filepath.Join(stats.StagingDir, entry.Name()))
		if bytes.Contains(raw, []byte("secret-trade-")) || bytes.Contains(raw, []byte("realized_usdt")) {
			t.Fatalf("private/forbidden data leaked to %s", entry.Name())
		}
	}
	artifacts, summary := buildArtifacts(SourceManifest{CapturedAt: "frozen"}, stats)
	bounded, ok := summary["bounded_memory"].(map[string]any)
	if !ok || bounded["peak_buffered_rows"] != stats.PeakBufferedRows || bounded["staging_files_hash"] == "" {
		t.Fatalf("missing bounded-memory instrumentation %#v", bounded)
	}
	raw := mustJSON(t, map[string]any{"artifacts": artifacts, "summary": summary})
	if bytes.Contains(raw, []byte("secret-trade-")) || bytes.Contains(raw, []byte("realized_usdt")) {
		t.Fatal("private/forbidden data leaked to artifacts")
	}
}

func TestBucketCapFailsClosedAndKeepsDiagnosticStaging(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	if err := os.WriteFile(path, []byte(
		"{\"tradeId\":\"a\",\"fee_usdt\":1,\"notional\":100}\n"+
			"{\"tradeId\":\"b\",\"fee_usdt\":1,\"notional\":100}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "manifest"
	cfg := Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 1, MaxBucketRows: 1,
	}
	_, _, err = runProjectFixture(context.Background(), cfg, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds frozen") {
		t.Fatalf("expected hard cap failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".evidenceaudit-staging", "binding.json")); err != nil {
		t.Fatal("diagnostic staging not retained")
	}
	if _, err := os.Stat(filepath.Join(out, "audit_receipt.json")); !os.IsNotExist(err) {
		t.Fatal("failed projection published receipt")
	}
}

func TestProjectionCheckpointCancelResumeByteIdentical(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	var input bytes.Buffer
	for i := 0; i < 240; i++ {
		fmt.Fprintf(&input, `{"tradeId":"t-%03d","fee_usdt":1,"notional":100}`+"\n", i)
	}
	if err := os.WriteFile(path, input.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	manifest := SourceManifest{CapturedAt: "frozen-time", ManifestHash: &h, Sources: []SourceEntry{e}}
	cfg := Config{RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 16, MaxBucketRows: 100, CheckpointEvery: 20}
	applyProjectionDefaults(&cfg)
	cfg.CheckpointPath = filepath.Join(out, "projection-checkpoint.json")

	baselineCP := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, baselineCP); err != nil {
		t.Fatal(err)
	}
	baselineStats, err := projectAll(context.Background(), cfg, manifest, &baselineCP, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, baselineSummary := buildArtifacts(manifest, baselineStats)
	baselineBytes := mustJSON(t, baselineSummary)
	if err := cleanupProjectionStaging(baselineStats.StagingDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.CheckpointPath); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cp := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		t.Fatal(err)
	}
	_, err = projectAll(ctx, cfg, manifest, &cp, func(source string, n int64) {
		if source == "exchange_fills" && n >= 40 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected projection cancellation, got %v", err)
	}
	saved, ok, err := loadCheckpoint(cfg.CheckpointPath)
	if err != nil || !ok {
		t.Fatalf("checkpoint load: %v", err)
	}
	offset := saved.SourceOffsets["exchange_fills"]
	if saved.Phase != "PROJECT" || offset.ByteOffset <= 0 || offset.ByteOffset >= e.BytePrefix || offset.CompleteLineOrdinal <= 0 {
		t.Fatalf("projection checkpoint did not advance: %#v", saved)
	}
	if err := verifyCheckpointBinding(saved, cfg, manifest); err != nil {
		t.Fatal(err)
	}
	resumedStats, err := projectAll(context.Background(), cfg, manifest, &saved, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resumedSummary := buildArtifacts(manifest, resumedStats)
	if !bytes.Equal(baselineBytes, mustJSON(t, resumedSummary)) {
		t.Fatal("projection resume artifacts differ from uninterrupted run")
	}
}

func TestAggregateCheckpointCancelResumeByteIdentical(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	var input bytes.Buffer
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&input, `{"tradeId":"a-%03d","fee_usdt":1,"notional":100}`+"\n", i)
	}
	if err := os.WriteFile(path, input.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	manifest := SourceManifest{CapturedAt: "frozen-time", ManifestHash: &h, Sources: []SourceEntry{e}}
	cfg := Config{RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 16, MaxBucketRows: 100, CheckpointEvery: 50}
	applyProjectionDefaults(&cfg)
	cfg.CheckpointPath = filepath.Join(out, "projection-checkpoint.json")

	baselineCP := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, baselineCP); err != nil {
		t.Fatal(err)
	}
	baselineStats, err := projectAll(context.Background(), cfg, manifest, &baselineCP, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, baselineSummary := buildArtifacts(manifest, baselineStats)
	baselineBytes := mustJSON(t, baselineSummary)
	if err := cleanupProjectionStaging(baselineStats.StagingDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.CheckpointPath); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cp := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		t.Fatal(err)
	}
	_, err = projectAll(ctx, cfg, manifest, &cp, func(source string, _ int64) {
		if strings.HasPrefix(source, "bucket-") {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected aggregate cancellation, got %v", err)
	}
	saved, ok, err := loadCheckpoint(cfg.CheckpointPath)
	if err != nil || !ok {
		t.Fatalf("checkpoint load: %v", err)
	}
	if saved.Phase != "AGGREGATE" || saved.CurrentBucket <= 0 || len(saved.AggregateFiles) == 0 {
		t.Fatalf("aggregate checkpoint did not advance: %#v", saved)
	}
	if err := verifyCheckpointBinding(saved, cfg, manifest); err != nil {
		t.Fatal(err)
	}
	resumedStats, err := projectAll(context.Background(), cfg, manifest, &saved, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, resumedSummary := buildArtifacts(manifest, resumedStats)
	if !bytes.Equal(baselineBytes, mustJSON(t, resumedSummary)) {
		t.Fatal("aggregate resume artifacts differ from uninterrupted run")
	}
}

func TestResumeRejectsStagingAndConfigDrift(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	var input bytes.Buffer
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&input, `{"tradeId":"d-%03d","fee_usdt":1,"notional":100}`+"\n", i)
	}
	if err := os.WriteFile(path, input.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	manifest := SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}
	cfg := Config{RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 4, MaxBucketRows: 100, CheckpointEvery: 10}
	applyProjectionDefaults(&cfg)
	cfg.CheckpointPath = filepath.Join(out, "projection-checkpoint.json")
	ctx, cancel := context.WithCancel(context.Background())
	cp := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		t.Fatal(err)
	}
	_, err = projectAll(ctx, cfg, manifest, &cp, func(source string, n int64) {
		if source == "exchange_fills" && n >= 10 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	saved, _, err := loadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		t.Fatal(err)
	}
	drifted := cfg
	drifted.ProjectBuckets++
	if err := verifyCheckpointBinding(saved, drifted, manifest); err == nil {
		t.Fatal("config drift accepted")
	}
	if len(saved.BucketFiles) == 0 {
		t.Fatal("checkpoint has no committed bucket files")
	}
	target := filepath.Join(saved.StagingDir, saved.BucketFiles[0].RelativePath)
	f, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{'X'}, 0); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := verifyCheckpointBinding(saved, cfg, manifest); err == nil {
		t.Fatal("staging mutation accepted")
	}
}

func TestCheckpointDigestDoesNotRehashCommittedBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	var input bytes.Buffer
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&input, `{"tradeId":"h-%04d","fee_usdt":1,"notional":100}`+"\n", i)
	}
	if err := os.WriteFile(path, input.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(every int) (projectionStats, Checkpoint) {
		out := t.TempDir()
		e, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
		if err != nil {
			t.Fatal(err)
		}
		h := "fixed-manifest"
		stats, cp, err := runProjectFixture(context.Background(), Config{
			RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 16,
			MaxBucketRows: 500, CheckpointEvery: every,
		}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return stats, cp
	}
	frequentStats, frequentCP := run(10)
	sparseStats, sparseCP := run(1000)
	if frequentCP.CheckpointHashedBytes != 0 || sparseCP.CheckpointHashedBytes != 0 ||
		frequentStats.CheckpointHashedBytes != 0 || sparseStats.CheckpointHashedBytes != 0 {
		t.Fatalf("normal checkpoints reread bytes: frequent=%d sparse=%d", frequentCP.CheckpointHashedBytes, sparseCP.CheckpointHashedBytes)
	}
	if frequentStats.Rows != sparseStats.Rows || frequentStats.Duplicates != sparseStats.Duplicates || frequentStats.StagingFilesHash != sparseStats.StagingFilesHash {
		t.Fatal("checkpoint cadence changed deterministic aggregate")
	}
}

func TestPresenceOnlyShadowRowsDoNotCollapseOrEnterBuckets(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "shadow.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 256*1024)
	const rows = 100000
	for i := 0; i < rows; i++ {
		fmt.Fprintln(w, `{"venue":"okx","symbol":"BTC-USDT","recorded_at":"2026-01-01T00:00:00Z"}`)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, out, SourceSpec{"executable_shadow", "shadow.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	stats, cp, err := runProjectFixture(context.Background(), Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 1,
		MaxBucketRows: 1, CheckpointEvery: 1000,
	}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProjectedTotal != rows || stats.Rows != rows || stats.Duplicates != 0 || stats.PeakBufferedRows != 0 || len(cp.BucketFiles) != 0 {
		t.Fatalf("presence-only rows collapsed or bucketed: stats=%#v cp=%#v", stats, cp)
	}
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	latency := combinedFieldCoverage(stats.Coverage, "latency_ms")
	maker := combinedFieldCoverage(stats.Coverage, "maker_fill_rate")
	instrument := combinedFieldCoverage(stats.Coverage, "instrument_lot")
	if fee.Denominator != 0 || maker.Denominator != 0 || instrument.Denominator != 0 ||
		latency.Denominator != rows || latency.Reasons["ABSENT_FIELD"] != rows {
		t.Fatalf("presence-only shadow polluted field denominators: %#v", stats.Coverage)
	}
}

func TestRealSchemaInstrumentAndExchangeCoverageReasons(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	metaPath := filepath.Join(root, "instrument_meta.json")
	instruments := map[string]any{}
	for i := 0; i < 394; i++ {
		symbol := fmt.Sprintf("S%03d-USDT", i)
		instruments[symbol] = map[string]any{"ct_val": "0.1", "lot": "0.01", "tick": "0.001"}
	}
	metaRaw, _ := json.Marshal(map[string]any{"fetched_at": "2026-01-01T00:00:00Z", "instruments": instruments})
	if err := os.WriteFile(metaPath, metaRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	fillPath := filepath.Join(root, "fills.jsonl")
	fills := `{"tradeId":"1","instId":"S000-USDT-SWAP","fee":"-0.01","feeCcy":"USDT","fillSz":"2","fillPx":"100"}` + "\n" +
		`{"tradeId":"2","instId":"MISS-USDT-SWAP","fee":"-0.01","feeCcy":"USDT","fillSz":"2","fillPx":"100"}` + "\n"
	if err := os.WriteFile(fillPath, []byte(fills), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := freezeOne(root, out, SourceSpec{"instrument_metadata", "instrument_meta.json", "JSON_MUTABLE_SNAPSHOT"})
	if err != nil {
		t.Fatal(err)
	}
	fillsEntry, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	stats, _, err := runProjectFixture(context.Background(), Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 8, MaxBucketRows: 10,
	}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{meta, fillsEntry}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lot := combinedFieldCoverage(stats.Coverage, "instrument_lot")
	ctVal := combinedFieldCoverage(stats.Coverage, "instrument_contract_value")
	ctType := combinedFieldCoverage(stats.Coverage, "instrument_contract_type")
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	dynamic := combinedFieldCoverage(stats.Coverage, "dynamic_min_notional_usdt")
	if lot.Numerator != 394 || lot.Denominator != 394 || ctVal.Numerator != 394 || ctVal.Denominator != 394 {
		t.Fatalf("real metadata aliases not counted: lot=%#v ctVal=%#v", lot, ctVal)
	}
	if ctType.Numerator != 0 || ctType.Denominator != 394 || ctType.Reasons["MISSING_CT_TYPE_DATA"] != 394 {
		t.Fatalf("ctType absence misclassified: %#v", ctType)
	}
	if fee.Denominator != 2 || fee.Numerator != 0 || fee.Reasons["MISSING_CT_TYPE_DATA"] != 1 || fee.Reasons["MISSING_INSTRUMENT_METADATA"] != 1 {
		t.Fatalf("exchange fee reasons collapsed: %#v", fee)
	}
	if dynamic.Denominator != 2 || dynamic.Numerator != 0 || dynamic.Reasons["MISSING_CT_TYPE_DATA"] != 1 || dynamic.Reasons["MISSING_INSTRUMENT_METADATA"] != 1 {
		t.Fatalf("dynamic min-notional incorrectly reconstructed: %#v", dynamic)
	}
}

func TestTruncatedSmokeCannotProduceFormalVerdict(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	cfg.MaxPrefixBytesPerSource = 120
	result, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ScopeStatus != "TRUNCATED_SMOKE" {
		t.Fatalf("scope=%q", result.Manifest.ScopeStatus)
	}
	var settlement SourceEntry
	for _, source := range result.Manifest.Sources {
		if source.SourceID == "settlements" {
			settlement = source
		}
	}
	if settlement.Status != "TRUNCATED_SMOKE" || settlement.BytePrefix > cfg.MaxPrefixBytesPerSource || settlement.OriginalBytePrefix <= settlement.BytePrefix {
		t.Fatalf("bad truncated source %#v", settlement)
	}
	route := result.Summary["classification_by_route"].(map[string]any)["g10_v1"].(map[string]any)
	if route["classification"] != nil || route["engineering_status"] != "NON_FORMAL_SMOKE" || result.Summary["not_formal_verdict"] != true {
		t.Fatalf("formal verdict escaped smoke: %#v", result.Summary)
	}
	var receipt map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(cfg.OutputDir, "audit_receipt.json")), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["not_formal_verdict"] != true || receipt["scope_status"] != "TRUNCATED_SMOKE" {
		t.Fatalf("bad receipt %#v", receipt)
	}
}

func protocolPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "research", "firstbattle", "g10_execution_evidence_audit_preregistration.json")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func fixtureConfig(t *testing.T) (Config, string) {
	t.Helper()
	root, out := t.TempDir(), t.TempDir()
	p := filepath.Join(root, "records", "okx_directional")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(p, "settlements.jsonl")
	data := "{\"position_id\":\"p1\",\"symbol\":\"BTC-USDT\",\"fee_usdt\":1,\"notional\":100}\n" +
		"{\"position_id\":\"p1\",\"symbol\":\"BTC-USDT\",\"fee_usdt\":1,\"notional\":100}\n" +
		"{\"position_id\":\"p2\",\"symbol\":\"ETH-USDT\",\"fee_usdt\":1}\n" +
		"not-json\npartial"
	if err := os.WriteFile(input, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return Config{RepoRoot: root, OutputDir: out, CheckpointPath: filepath.Join(out, "checkpoint.external.json"), ProtocolPath: protocolPath(t), AuditSalt: "test"}, input
}

func TestProtocolHashAndAllowedSourceContract(t *testing.T) {
	if err := VerifyProtocol(protocolPath(t)); err != nil {
		t.Fatal(err)
	}
	if len(AllowedSources()) != 12 || AllowedSources()[0].Path != "records/okx_directional/settlements.jsonl" {
		t.Fatal("allowed source drift")
	}
}

func TestFreezeAppendIgnoredPrefixMutationAndPartialTail(t *testing.T) {
	cfg, input := fixtureConfig(t)
	m, err := freezeAll(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var settlement SourceEntry
	for _, e := range m.Sources {
		if e.SourceID == "settlements" {
			settlement = e
		}
	}
	if settlement.PartialTrailingBytes != int64(len("partial")) {
		t.Fatalf("partial tail=%d", settlement.PartialTrailingBytes)
	}
	if err := os.WriteFile(input, append(mustRead(t, input), []byte("{\"later\":true}\npartial")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestInputs(cfg.RepoRoot, m); err != nil {
		t.Fatalf("append should be ignored: %v", err)
	}
	if err := os.WriteFile(input, []byte("{\"mutated\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestInputs(cfg.RepoRoot, m); err == nil {
		t.Fatal("prefix mutation must fail closed")
	}
}

func TestSnakeCaseAndNullSelfExclusion(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	m, err := freezeAll(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "SchemaVersion") || !strings.Contains(text, `"schema_version"`) || !strings.Contains(text, `"byte_prefix"`) {
		t.Fatalf("not snake_case: %s", text)
	}
	got := *m.ManifestHash
	m.ManifestHash = nil
	want, _, err := datatruth.HashJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.Contains(string(mustJSON(t, m)), `"manifest_hash":null`) {
		t.Fatal("manifest hash must self-exclude with null")
	}
}

func TestOutcomeFirewallUsesRealCellAndOpaqueWinnerBindings(t *testing.T) {
	var s projectionStats
	s.Known = map[string]int64{}
	root := t.TempDir()
	eventPath := filepath.Join(root, "events.json")
	event := `{"profile_id":"okx_1h","cells":{"x":{"key":{"event_family":"f","horizon":"1h","direction":"long","delay_bars":0},"stats":{"conservative_net_bps_mean":999,"event_outcome_count":50}}}}`
	if err := os.WriteFile(eventPath, []byte(event), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projectFirewallJSON(mustRead(t, eventPath), "validation_triggers_okx_1h", &s); err != nil {
		t.Fatal(err)
	}
	pbPath := filepath.Join(root, "playbooks.json")
	pb := `{"playbooks":[{"playbook_hash":"pb","source_winner_ids_hash":"opaque","profile_id":"okx_1h","event_family":"f","horizon":"1h","delay_bars":0}]}`
	if err := os.WriteFile(pbPath, []byte(pb), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projectFirewallJSON(mustRead(t, pbPath), "playbook_bindings", &s); err != nil {
		t.Fatal(err)
	}
	if len(s.SupportCells) != 1 || len(s.WinnerBindings) != 1 || s.FirewallRejectedFields != 1 {
		t.Fatalf("bad firewall projection %#v", s)
	}
	artifacts, _ := buildArtifacts(SourceManifest{CapturedAt: "x"}, s)
	support := artifacts["support_counts.json"].(map[string]any)
	if support["events"] != nil || support["status"] != "UNAVAILABLE_SOURCE_SCHEMA" {
		t.Fatal("aggregate outcomes must not become events")
	}
	bindings := artifacts["winner_control_bindings.json"].(map[string]any)
	if bindings["winner_seed_set_hash"] == hashStrings() || bindings["matched_control_set_hash"] != nil {
		t.Fatal("must bind real opaque seeds and refuse invented controls")
	}
	raw := string(mustJSON(t, artifacts))
	if strings.Contains(raw, "conservative_net_bps_mean") || strings.Contains(raw, "999") {
		t.Fatal("forbidden outcome leaked")
	}
}

func TestFrozenJSONProjectionUsesCapturedSnapshotAndRejectsDrift(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "events.json")
	original := `{
  "profile_id": "okx_1h",
  "cells": {
    "a": {
      "key": {"event_family":"f","horizon":"1h","direction":"long","delay_bars":0},
      "stats": {"future_outcome": 99}
    }
  }
}
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	entry, err := freezeOne(root, out, SourceSpec{"validation_triggers_okx_1h", "events.json", "FROZEN_JSON_LABEL_FIREWALL"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := readFrozenSource(out, entry)
	if err != nil {
		t.Fatal(err)
	}
	var before projectionStats
	if err := projectFirewallJSON(frozen, entry.SourceID, &before); err != nil {
		t.Fatal(err)
	}

	// Appended live bytes are outside the captured snapshot and cannot alter projection.
	live, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = live.WriteString("   \n")
	_ = live.Close()
	if err := verifyManifestInputs(root, SourceManifest{Sources: []SourceEntry{entry}}); err != nil {
		t.Fatalf("append after capture should be ignored: %v", err)
	}
	frozenAgain, err := readFrozenSource(out, entry)
	if err != nil {
		t.Fatal(err)
	}
	var after projectionStats
	if err := projectFirewallJSON(frozenAgain, entry.SourceID, &after); err != nil {
		t.Fatal(err)
	}
	if len(before.SupportCells) != 1 || !reflect.DeepEqual(before.SupportCells, after.SupportCells) || after.FirewallRejectedFields != 1 {
		t.Fatalf("outcome firewall/snapshot changed: before=%#v after=%#v", before, after)
	}

	// Prefix mutation is rejected even though the immutable snapshot still exists.
	mutated := append([]byte(nil), []byte(original)...)
	mutated[5] ^= 1
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifestInputs(root, SourceManifest{Sources: []SourceEntry{entry}}); err == nil {
		t.Fatal("live prefix mutation accepted")
	}

	snapshotPath := filepath.Join(out, entry.SnapshotPath)
	snapshot := mustRead(t, snapshotPath)
	snapshot[10] ^= 1
	if err := os.WriteFile(snapshotPath, snapshot, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrozenSource(out, entry); err == nil {
		t.Fatal("snapshot hash drift accepted")
	}
}

func TestExchangeFillDedupConflictAndDerivedFee(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fills.jsonl")
	data := `{"tradeId":"t1","fee_usdt":1,"notional":100,"fillPx":"10"}` + "\n" +
		`{"tradeId":"t1","fee_usdt":1,"notional":100,"fillPx":"10"}` + "\n" +
		`{"tradeId":"t1","fee_usdt":2,"notional":100,"fillPx":"10"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := freezeOne(root, t.TempDir(), SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	h := "manifest"
	stats, _, err := runProjectFixture(context.Background(), Config{RepoRoot: root, OutputDir: out, AuditSalt: "x", ProjectBuckets: 4, MaxBucketRows: 10}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{e}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rows != 1 || stats.Duplicates != 1 || stats.Conflicts != 1 || stats.Known["fee_bps"] != 0 {
		t.Fatalf("stats=%#v", stats)
	}
}

func TestCrossSourcePriorityProvenanceAndIdentityRules(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	settlementPath := filepath.Join(root, "settlements.jsonl")
	exchangePath := filepath.Join(root, "fills.jsonl")
	settlements := `{"ordId":"shared","position_id":"p1","fee_usdt":0.01,"notional":100}` + "\n" +
		`{"position_id":"separate","fee_usdt":0.01,"notional":100}` + "\n"
	exchange := `{"ordId":"shared","tradeId":"","fee_usdt":0.02,"notional":100}` + "\n" +
		`{"tradeId":"separate-fill","fee_usdt":0.02,"notional":100}` + "\n"
	if err := os.WriteFile(settlementPath, []byte(settlements), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exchangePath, []byte(exchange), 0o644); err != nil {
		t.Fatal(err)
	}
	settlement, err := freezeOne(root, out, SourceSpec{"settlements", "settlements.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	fill, err := freezeOne(root, out, SourceSpec{"exchange_fills", "fills.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	stats, _, err := runProjectFixture(context.Background(), Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 8, MaxBucketRows: 20,
	}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{settlement, fill}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	if fee.Denominator != 3 || fee.Numerator != 3 {
		t.Fatalf("shared identity did not suppress exactly one fact: %#v", fee)
	}
	if stats.SuppressedLowerPriorityFacts != 1 || stats.CrossSourceConflicts != 1 ||
		stats.ProvenanceCounts["fee_bps|settlements"] != 2 || stats.ProvenanceCounts["fee_bps|exchange_fills"] != 2 {
		t.Fatalf("priority/provenance mismatch %#v", stats)
	}
	if stats.UnresolvedCrossSourceIdentities != 2 {
		t.Fatalf("separate identities were silently merged: %d", stats.UnresolvedCrossSourceIdentities)
	}
	raw := map[string]any{"symbol": "BTC-USDT", "side": "long", "time": "t", "entry": 1.0, "exit": 2.0, "notional": 100.0}
	first, kind1, _ := stableIdentity("settlements", "okx", raw, 1, 10)
	second, kind2, _ := stableIdentity("settlements", "okx", raw, 999, 9999)
	if first != second || kind1 != "PROTOCOL_FALLBACK" || kind2 != kind1 {
		t.Fatal("protocol fallback identity is not deterministic")
	}
}

func TestSamePriorityConflictMakesFactUnknown(t *testing.T) {
	stats := projectionStats{Known: map[string]int64{}, Unknown: map[string]int64{}, Coverage: map[string]FieldCoverage{}, ProvenanceCounts: map[string]int64{}}
	metricA := stagedMetric{Applicable: true, Present: true, Value: 1, Status: "DIRECT", Scope: "exchange_fill_rows"}
	metricB := stagedMetric{Applicable: true, Present: true, Value: 2, Status: "DIRECT", Scope: "exchange_fill_rows"}
	aggregateIdentity([]stagedRecord{
		{SourceID: "exchange_fills", IdentityKind: "SHARED_STABLE_ID", FeeBPS: metricA},
		{SourceID: "exchange_fills", IdentityKind: "SHARED_STABLE_ID", FeeBPS: metricB},
	}, &stats)
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	if fee.Numerator != 0 || fee.Denominator != 1 || fee.Reasons["SOURCE_CONFLICT"] != 1 || stats.Conflicts != 1 {
		t.Fatalf("same-priority conflict did not become UNKNOWN: stats=%#v fee=%#v", stats, fee)
	}
}

func TestSettlementStreamsSharedIdentityPriorityComplementAndConflict(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	settlementPath := filepath.Join(root, "settlements.jsonl")
	fastPath := filepath.Join(root, "fast.jsonl")
	settlementFile, err := os.Create(settlementPath)
	if err != nil {
		t.Fatal(err)
	}
	fastFile, err := os.Create(fastPath)
	if err != nil {
		t.Fatal(err)
	}
	settlementWriter, fastWriter := bufio.NewWriter(settlementFile), bufio.NewWriter(fastFile)
	for i := 0; i < 729; i++ {
		fmt.Fprintf(settlementWriter, `{"position_id":"p-%03d","fee_usdt":0.01,"notional":100,"entry_slip_bps":1,"exit_slip_bps":2}`+"\n", i)
		fmt.Fprintf(fastWriter, `{"position_id":"p-%03d","fee_usdt":0.01,"notional":100,"entry_slip_bps":1,"exit_slip_bps":2}`+"\n", i)
	}
	fmt.Fprintln(settlementWriter, `{"position_id":"complement","fee_usdt":0.01,"notional":100}`)
	fmt.Fprintln(fastWriter, `{"position_id":"complement","entry_slip_bps":3,"exit_slip_bps":4}`)
	fmt.Fprintln(settlementWriter, `{"position_id":"conflict","fee_usdt":0.01,"notional":100,"entry_slip_bps":1,"exit_slip_bps":2}`)
	fmt.Fprintln(fastWriter, `{"position_id":"conflict","fee_usdt":0.02,"notional":100,"entry_slip_bps":1,"exit_slip_bps":2}`)
	_ = settlementWriter.Flush()
	_ = fastWriter.Flush()
	_ = settlementFile.Close()
	_ = fastFile.Close()
	settlement, err := freezeOne(root, out, SourceSpec{"settlements", "settlements.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := freezeOne(root, out, SourceSpec{"fast_v2_settlements", "fast.jsonl", "JSONL_APPEND"})
	if err != nil {
		t.Fatal(err)
	}
	h := "fixed-manifest"
	stats, _, err := runProjectFixture(context.Background(), Config{
		RepoRoot: root, OutputDir: out, AuditSalt: "salt", ProjectBuckets: 32, MaxBucketRows: 100,
	}, SourceManifest{ManifestHash: &h, Sources: []SourceEntry{settlement, fast}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fee := combinedFieldCoverage(stats.Coverage, "fee_bps")
	entry := combinedFieldCoverage(stats.Coverage, "entry_slip_bps")
	exit := combinedFieldCoverage(stats.Coverage, "exit_slip_bps")
	if stats.Rows != 731 || fee.Denominator != 731 || fee.Numerator != 730 || fee.Reasons["SOURCE_CONFLICT"] != 1 {
		t.Fatalf("overlap/conflict fee semantics wrong: rows=%d fee=%#v stats=%#v", stats.Rows, fee, stats)
	}
	if entry.Denominator != 731 || entry.Numerator != 731 || exit.Denominator != 731 || exit.Numerator != 731 {
		t.Fatalf("complementary fields were not filled once: entry=%#v exit=%#v", entry, exit)
	}
	if stats.SuppressedLowerPriorityFacts != 729*3+2 {
		// 729 exact overlaps suppress fast fee+entry+exit; conflict suppresses
		// only the two equal slip facts, while conflicting fee becomes UNKNOWN.
		t.Fatalf("unexpected suppressed fact count %d", stats.SuppressedLowerPriorityFacts)
	}
	if stats.CrossSourceConflicts != 1 || stats.UnresolvedCrossSourceIdentities != 0 {
		t.Fatalf("shared settlement identities unresolved/conflict mismatch: %#v", stats)
	}
}

func TestLargeFreezeUsesBoundedMemory(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "large.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(`{"symbol":"BTC-USDT","fee_usdt":1,"notional":100}` + "\n")
	for written := 0; written < 16*1024*1024; written += len(line) {
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := freezeOne(root, out, SourceSpec{"settlements", "large.jsonl", "JSONL_APPEND"}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+8*1024*1024 {
		t.Fatalf("freeze retained %d bytes for 16MiB input", after.HeapAlloc-before.HeapAlloc)
	}
}

func TestPublishFailureLeavesNoReceiptOrManifest(t *testing.T) {
	out := t.TempDir()
	err := publishArtifacts(out, map[string]any{
		"field_completeness.json": map[string]any{"bad": make(chan int)},
		"audit_receipt.json":      map[string]any{"complete": true},
	})
	if err == nil {
		t.Fatal("expected marshal failure")
	}
	for _, name := range []string{"audit_receipt.json", "source_manifest.json"} {
		if _, statErr := os.Stat(filepath.Join(out, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s unexpectedly published", name)
		}
	}
}

func TestRunQuarantinesMalformedDeduplicatesAndKeepsUnknown(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	result, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, filepath.Join(cfg.OutputDir, "conflict_matrix.json"))
	if !strings.Contains(string(raw), "\"exact_duplicates\": 1") || !strings.Contains(string(raw), "\"malformed_quarantined\": 1") {
		t.Fatalf("missing quarantine/dedupe: %s", raw)
	}
	comp := mustRead(t, filepath.Join(cfg.OutputDir, "field_completeness.json"))
	if !strings.Contains(string(comp), "entry_slip_bps") {
		t.Fatal("missing slip must remain UNKNOWN")
	}
	var completeness struct {
		ProjectedRows int64                    `json:"projected_rows"`
		Fields        map[string]FieldCoverage `json:"fields"`
	}
	if err := json.Unmarshal(comp, &completeness); err != nil {
		t.Fatal(err)
	}
	fee := completeness.Fields["fee_bps"]
	if completeness.ProjectedRows != 2 || fee.Numerator != 1 || fee.Denominator != 2 || fee.Reasons["MISSING_NOTIONAL"] != 1 {
		t.Fatalf("evidence/missing denominators collapsed: %#v", completeness)
	}
	class, _ := result.Summary["classification_by_route"].(map[string]any)
	route, _ := class["g10_v1"].(map[string]any)
	if route["classification"] != "DATA_BLIND" {
		t.Fatalf("got %#v", class)
	}
	if _, err := os.Stat(filepath.Join(cfg.OutputDir, ".evidenceaudit-staging")); !os.IsNotExist(err) {
		t.Fatal("successful run did not clean hidden staging")
	}
}

func TestCheckpointResumeByteIdenticalAndNoPartialManifest(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	if _, err := Run(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(cfg.OutputDir, "audit_summary.json"))
	if _, err := Run(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	after := mustRead(t, filepath.Join(cfg.OutputDir, "audit_summary.json"))
	if string(before) != string(after) {
		t.Fatal("resume changed completed artifact")
	}
	if _, err := os.Stat(filepath.Join(cfg.OutputDir, "source_manifest.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("partial manifest left behind")
	}
	if _, err := os.Stat(filepath.Join(cfg.OutputDir, "checkpoint.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWinnerBindingsAreOutcomeBlind(t *testing.T) {
	cfg, _ := fixtureConfig(t)
	if _, err := Run(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(cfg.OutputDir, "winner_control_bindings.json")), &v); err != nil {
		t.Fatal(err)
	}
	if v["labels_unblinded"] != false || v["winner_seed_set_hash"] == "" || v["matched_control_set_hash"] == "" {
		t.Fatalf("bad unblind binding %#v", v)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runProjectFixture(ctx context.Context, cfg Config, manifest SourceManifest, progress func(string, int64)) (projectionStats, Checkpoint, error) {
	applyProjectionDefaults(&cfg)
	if cfg.CheckpointPath == "" {
		cfg.CheckpointPath = filepath.Join(cfg.OutputDir, "projection-checkpoint.json")
	}
	cp := newProjectionCheckpoint(cfg, manifest)
	if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		return projectionStats{}, cp, err
	}
	stats, err := projectAll(ctx, cfg, manifest, &cp, progress)
	return stats, cp, err
}
