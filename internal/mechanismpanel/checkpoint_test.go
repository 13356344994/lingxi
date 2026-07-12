package mechanismpanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

func TestCheckpointSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")

	cfg := testAggregatorConfig()
	agg := NewAggregator(cfg, nil, nil)
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	states := map[string]datatruth.StreamState{}
	ingest := func(rec Record) {
		key := streamKeyForKind(rec.Kind)
		next, assess := datatruth.Observe(states[key], rec.Envelope, datatruth.ObserveOptions{})
		states[key] = next
		rec.Envelope = assess.Event
		if err := agg.Ingest(rec, assess); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	// Drive some state: one still-open episode (BTC-USDT gap threshold not yet exceeded).
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base), Liquidation: &LiquidationRecord{RecordedAt: base, Symbol: "BTC-USDT", PosSide: "long", Qty: 1, BkPx: 60000}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(2 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(2 * time.Second), Mids: map[string]float64{"BTC-USDT": 59990}}})

	cp := Checkpoint{
		SourceOffsets:   map[string]int64{"okx_liquidation": 1, "market_mids": 1},
		StreamStates:    states,
		LinesProcessed:  2,
		FeaturesEmitted: 0,
		LabelsEmitted:   0,
		Aggregator:      agg.Snapshot(),
	}
	if err := SaveCheckpoint(path, cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	loaded, ok, err := LoadCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("LoadCheckpoint: ok=%v err=%v", ok, err)
	}
	if loaded.SourceOffsets["okx_liquidation"] != 1 || loaded.SourceOffsets["market_mids"] != 1 {
		t.Fatalf("unexpected offsets: %+v", loaded.SourceOffsets)
	}
	if len(loaded.Aggregator.Open) != 1 || loaded.Aggregator.Open[0].Instrument != "BTC-USDT" {
		t.Fatalf("expected 1 open episode for BTC-USDT in snapshot, got %+v", loaded.Aggregator.Open)
	}

	restored := RestoreAggregator(cfg, loaded.Aggregator, nil, nil)
	if len(restored.open) != 1 {
		t.Fatalf("restored aggregator should have 1 open episode, got %d", len(restored.open))
	}
	ep := restored.open["BTC-USDT"]
	if ep == nil || len(ep.ForcingEvents) != 1 {
		t.Fatalf("restored episode missing forcing events: %+v", ep)
	}
	if ep.Mids[0].V != 59990 {
		t.Fatalf("restored episode missing buffered mid point: %+v", ep.Mids)
	}
}

func TestLoadCheckpointMissingFileReturnsFalse(t *testing.T) {
	_, ok, err := LoadCheckpoint(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("expected no error for missing checkpoint, got %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for missing checkpoint")
	}
}

// TestPendingLabelSnapshotRoundTripPreservesTouchedState guards against a real bug class:
// if `touched` were not part of the checkpoint, a resumed run would treat the next future
// sample within an already-touched horizon window as the *first* sample and overwrite
// (rather than min/max-compare) the running excursion extremes.
func TestPendingLabelSnapshotRoundTripPreservesTouchedState(t *testing.T) {
	decision := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	pl := newPendingLabel("ep-1", "BTC-USDT", decision, 59900, []time.Duration{10 * time.Second})
	pl.absorbFuture(decision.Add(2*time.Second), 59850) // touches but does not freeze the 10s horizon

	snap := pl.snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var roundTripped pendingLabelSnapshot
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	restored := restorePendingLabel(roundTripped)

	// Feed a second sample within the same horizon window; without correctly restoring
	// `touched`, this would overwrite (not compare against) the first sample's extremes.
	restored.absorbFuture(decision.Add(6*time.Second), 59950)

	wantAdverse := (59850.0/59900 - 1) * 10000
	wantFavorable := (59950.0/59900 - 1) * 10000
	if !almostEqual(restored.results[0].MaxAdverseBps, wantAdverse, 1e-6) {
		t.Errorf("MaxAdverseBps regressed after restore: %+v, want %v", restored.results[0], wantAdverse)
	}
	if !almostEqual(restored.results[0].MaxFavorableBps, wantFavorable, 1e-6) {
		t.Errorf("MaxFavorableBps regressed after restore: %+v, want %v", restored.results[0], wantFavorable)
	}
}

// ===== 红队#12:checkpoint 绑定输入文件身份 + 完整配置身份,resume 时不一致必须
// fail closed。以下测试只用临时小 fixture(t.TempDir()),不触碰任何 testdata/records。

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func identityTestConfig(t *testing.T, dir string) Config {
	t.Helper()
	liq := writeTempFile(t, dir, "liq.jsonl", `{"recorded_at":"2026-07-11T00:00:00Z","symbol":"BTC-USDT","pos_side":"long","side":"sell","qty":1,"bk_px":100}`+"\n")
	return Config{
		Sources:    SourcePaths{Liquidation: liq},
		Aggregator: testAggregatorConfig(),
	}
}

// TestHashInputPrefixStableAcrossAppendOnlyGrowth pins down the actual invariant
// VerifyCheckpointIdentity relies on: re-hashing only up to the size recorded at
// checkpoint time is stable even after the (append-only) file has grown well past
// that size, and even though the file is far smaller than inputIdentityPrefixBytes
// (the common case for small fixtures/tests, where a naive "hash whatever is
// available now, capped at 64KiB" approach would incorrectly change on every append).
func TestHashInputPrefixStableAcrossAppendOnlyGrowth(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "growing.jsonl", "line-one\n")
	recordedSize := int64(len("line-one\n"))
	first, err := hashInputPrefix(path, recordedSize)
	if err != nil {
		t.Fatalf("hashInputPrefix: %v", err)
	}
	if err := os.WriteFile(path, []byte("line-one\nline-two\n"), 0o600); err != nil {
		t.Fatalf("append: %v", err)
	}
	second, err := hashInputPrefix(path, recordedSize)
	if err != nil {
		t.Fatalf("hashInputPrefix after growth: %v", err)
	}
	if first != second {
		t.Fatalf("hash of the recorded-size prefix should stay stable for append-only growth: %s vs %s", first, second)
	}

	// Sanity: computeInputIdentity's own PrefixHash (bounded by *current* size) is,
	// by contrast, expected to change when the file is smaller than the prefix window
	// and grows — that's exactly why VerifyCheckpointIdentity re-hashes at the recorded
	// size instead of comparing two independently-sized snapshots.
	beforeGrowth, err := computeInputIdentity(path)
	if err != nil {
		t.Fatalf("computeInputIdentity: %v", err)
	}
	if err := os.WriteFile(path, []byte("line-one\nline-two\nline-three\n"), 0o600); err != nil {
		t.Fatalf("append again: %v", err)
	}
	afterGrowth, err := computeInputIdentity(path)
	if err != nil {
		t.Fatalf("computeInputIdentity after growth: %v", err)
	}
	if beforeGrowth.PrefixHash == afterGrowth.PrefixHash {
		t.Fatalf("expected computeInputIdentity's current-size-bounded hash to change when the file grows past the previous size")
	}
}

func TestVerifyCheckpointIdentityAcceptsAppendOnlyGrowthAndUnchangedConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	identities, err := computeInputIdentities(cfg.Sources)
	if err != nil {
		t.Fatalf("computeInputIdentities: %v", err)
	}
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{Version: checkpointVersion, InputIdentities: identities, ConfigFingerprint: fp, ConfigHash: fp.hash()}

	// The input file grows (more lines appended) before resume — must still be accepted.
	liqPath := cfg.Sources.Liquidation
	f, err := os.OpenFile(liqPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(`{"recorded_at":"2026-07-11T00:00:30Z","symbol":"BTC-USDT","pos_side":"long","side":"sell","qty":1,"bk_px":99}` + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	f.Close()

	if err := VerifyCheckpointIdentity(cfg, cp); err != nil {
		t.Fatalf("expected append-only growth to be accepted, got: %v", err)
	}
}

func TestVerifyCheckpointIdentityFailsClosedOnFileReplaced(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	identities, err := computeInputIdentities(cfg.Sources)
	if err != nil {
		t.Fatalf("computeInputIdentities: %v", err)
	}
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{Version: checkpointVersion, InputIdentities: identities, ConfigFingerprint: fp, ConfigHash: fp.hash()}

	// Same path, but the file got replaced with unrelated content of the same size
	// (simulates a different generation's fixture being dropped into the same path).
	if err := os.WriteFile(cfg.Sources.Liquidation, []byte(`{"recorded_at":"2026-07-11T00:00:00Z","symbol":"ETH-USDT","pos_side":"short","side":"buy","qty":9,"bk_px":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("replace file: %v", err)
	}

	err = VerifyCheckpointIdentity(cfg, cp)
	if err == nil {
		t.Fatalf("expected fail-closed error for replaced input file")
	}
	if !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected ErrCheckpointMismatch, got %v", err)
	}
}

func TestVerifyCheckpointIdentityFailsClosedOnShrunkFile(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	// Pretend the checkpoint was saved when the file was already larger than it is now
	// (e.g. someone replaced the growing log with a fresh, shorter one).
	recorded, err := computeInputIdentity(cfg.Sources.Liquidation)
	if err != nil {
		t.Fatalf("computeInputIdentity: %v", err)
	}
	recorded.SizeBytes += 1024
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{
		Version:           checkpointVersion,
		InputIdentities:   map[string]InputIdentity{"okx_liquidation": recorded},
		ConfigFingerprint: fp,
		ConfigHash:        fp.hash(),
	}

	err = VerifyCheckpointIdentity(cfg, cp)
	if err == nil || !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected ErrCheckpointMismatch for a shrunk file, got %v", err)
	}
}

func TestVerifyCheckpointIdentityFailsClosedOnSourceSetChange(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	identities, err := computeInputIdentities(cfg.Sources)
	if err != nil {
		t.Fatalf("computeInputIdentities: %v", err)
	}
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{Version: checkpointVersion, InputIdentities: identities, ConfigFingerprint: fp, ConfigHash: fp.hash()}

	// A new source (depth) gets turned on for the resumed run.
	cfg.Sources.Depth = writeTempFile(t, dir, "depth.jsonl", `{"recorded_at":"2026-07-11T00:00:00Z","symbol":"BTC-USDT","bid_px":100,"bid_qty":1,"ask_px":100.1,"ask_qty":1}`+"\n")

	err = VerifyCheckpointIdentity(cfg, cp)
	if err == nil || !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected ErrCheckpointMismatch when a source is added, got %v", err)
	}
}

func TestVerifyCheckpointIdentityFailsClosedOnConfigDrift(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	identities, err := computeInputIdentities(cfg.Sources)
	if err != nil {
		t.Fatalf("computeInputIdentities: %v", err)
	}
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{Version: checkpointVersion, InputIdentities: identities, ConfigFingerprint: fp, ConfigHash: fp.hash()}

	// A semantic threshold changed — must be treated as a different generation.
	cfg.Aggregator.GapThreshold = cfg.Aggregator.GapThreshold * 2

	err = VerifyCheckpointIdentity(cfg, cp)
	if err == nil || !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected ErrCheckpointMismatch on GapThreshold drift, got %v", err)
	}
}

func TestVerifyCheckpointIdentityAllowsOperationalKnobDrift(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	identities, err := computeInputIdentities(cfg.Sources)
	if err != nil {
		t.Fatalf("computeInputIdentities: %v", err)
	}
	fp := newConfigFingerprint(cfg)
	cp := Checkpoint{Version: checkpointVersion, InputIdentities: identities, ConfigFingerprint: fp, ConfigHash: fp.hash()}

	// Purely operational knobs may legitimately differ between resumes of the same
	// generation (e.g. finishing a streaming run with --finalize at the very end).
	cfg.MaxLines = 42
	cfg.ProgressEveryN = 7
	cfg.FinalizeAtEOF = true

	if err := VerifyCheckpointIdentity(cfg, cp); err != nil {
		t.Fatalf("expected operational knob drift to be accepted, got: %v", err)
	}
}

func TestVerifyCheckpointIdentityFailsClosedOnOldCheckpointVersion(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	// A pre-red-team-#12 checkpoint (version 1) never recorded identities/fingerprint.
	cp := Checkpoint{Version: 1, SourceOffsets: map[string]int64{"okx_liquidation": 1}}

	err := VerifyCheckpointIdentity(cfg, cp)
	if err == nil || !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected ErrCheckpointMismatch for stale checkpoint version, got %v", err)
	}
}

// TestNewPipelineRefusesResumeOnIdentityMismatch is the integration-level guarantee:
// NewPipeline itself must fail closed (return an error, not construct a Pipeline) when
// asked to resume from a checkpoint whose identity no longer matches — here, the input
// file got replaced with different content between the checkpointed run and the resume
// attempt.
func TestNewPipelineRefusesResumeOnIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	cfg := identityTestConfig(t, dir)
	cpPath := filepath.Join(dir, "checkpoint.json")
	cfg.CheckpointPath = cpPath
	cfg.MaxLines = 1

	p1, err := NewPipeline(cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewPipeline (initial): %v", err)
	}
	res1, err := p1.Run(context.Background())
	if err != nil {
		t.Fatalf("Run (initial): %v", err)
	}
	p1.Close()
	if !res1.Checkpointed {
		t.Fatalf("expected initial run to checkpoint")
	}

	// Swap the input file for a different one before attempting to resume.
	if err := os.WriteFile(cfg.Sources.Liquidation, []byte(`{"recorded_at":"2026-07-11T00:05:00Z","symbol":"ETH-USDT","pos_side":"short","side":"buy","qty":5,"bk_px":2000}`+"\n"), 0o600); err != nil {
		t.Fatalf("replace input file: %v", err)
	}

	if _, err := NewPipeline(cfg, nil, nil); err == nil {
		t.Fatalf("expected NewPipeline to refuse resume after input file was replaced")
	} else if !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("expected error wrapping ErrCheckpointMismatch, got: %v", err)
	}

	// The checkpoint on disk must be left untouched (fail closed, not silently reset).
	stillThere, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("expected checkpoint file to remain readable: ok=%v err=%v", ok, err)
	}
	if stillThere.LinesProcessed != res1.Stats.LinesProcessed {
		t.Fatalf("checkpoint on disk must be unmodified by the refused resume attempt")
	}
}
