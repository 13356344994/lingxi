package evidenceaudit

// Milestone A design decision:
//  1. In-memory rows+seen was rejected because memory grows with the input.
//  2. One external sort file was rejected because it needs a platform sort
//     dependency and can expose unstable locale/order behavior.
//  3. Stable hash buckets were selected: projection writes only redacted facts,
//     then each bucket is aggregated independently under a hard row cap. A
//     skewed bucket fails closed instead of silently exceeding that cap.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"lingxi/internal/datatruth"
)

const (
	defaultProjectBuckets  = 256
	defaultMaxBucketRows   = 50000
	defaultCheckpointEvery = 10000
	projectionSchema       = "phase_a_redacted_bucket_record.v1"
)

type stagedMetric struct {
	Applicable bool    `json:"applicable"`
	Present    bool    `json:"present"`
	Value      float64 `json:"value,omitempty"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason,omitempty"`
	Scope      string  `json:"scope"`
}

type stagedRecord struct {
	KeyToken       string       `json:"key_token"`
	PayloadHash    string       `json:"payload_hash"`
	SourceID       string       `json:"source_id"`
	FeeBPS         stagedMetric `json:"fee_bps"`
	EntrySlip      stagedMetric `json:"entry_slip_bps"`
	ExitSlip       stagedMetric `json:"exit_slip_bps"`
	LatencyMS      stagedMetric `json:"latency_ms"`
	DynamicMin     stagedMetric `json:"dynamic_min_notional_usdt"`
	MakerFill      stagedMetric `json:"maker_fill_rate"`
	MakerFilled    *bool        `json:"maker_filled,omitempty"`
	IdentityKind   string       `json:"identity_kind"`
	IdentityReason string       `json:"identity_reason,omitempty"`
}

type instrumentFact struct {
	CTVal, Lot, Tick          float64
	HasCTVal, HasLot, HasTick bool
	CTType                    string
}

type bucketObservation struct {
	Records         []stagedRecord
	SeenPayloads    map[string]struct{}
	ExactDuplicates int64
	ByteCount       int64
}

type bucketWriter struct {
	File  *os.File
	Size  int64
	Chain string
}

type bucketAggregate struct {
	Bucket                          int                      `json:"bucket"`
	Rows                            int64                    `json:"rows"`
	Duplicates                      int64                    `json:"duplicates"`
	Conflicts                       int64                    `json:"conflicts"`
	Known                           map[string]int64         `json:"known"`
	Unknown                         map[string]int64         `json:"unknown"`
	Coverage                        map[string]FieldCoverage `json:"coverage"`
	SourceRows                      map[string]int64         `json:"source_rows"`
	ProvenanceCounts                map[string]int64         `json:"provenance_counts"`
	SuppressedLowerPriorityFacts    int64                    `json:"suppressed_lower_priority_facts"`
	UnresolvedCrossSourceIdentities int64                    `json:"unresolved_cross_source_identities"`
	CrossSourceConflicts            int64                    `json:"cross_source_conflicts"`
	PeakBufferedRows                int64                    `json:"peak_buffered_rows"`
	PeakBufferedBytes               int64                    `json:"peak_buffered_bytes"`
	AggregateHash                   *string                  `json:"aggregate_hash"`
}

func applyProjectionDefaults(cfg *Config) {
	if cfg.ProjectBuckets <= 0 {
		cfg.ProjectBuckets = defaultProjectBuckets
	}
	if cfg.MaxBucketRows <= 0 {
		cfg.MaxBucketRows = defaultMaxBucketRows
	}
	if cfg.CheckpointEvery <= 0 {
		cfg.CheckpointEvery = defaultCheckpointEvery
	}
}

func newProjectionCheckpoint(cfg Config, manifest SourceManifest) Checkpoint {
	stagingDir := filepath.Join(cfg.OutputDir, ".evidenceaudit-staging")
	binding := hashStrings(ProtocolHash, *manifest.ManifestHash, hashStrings(projectionSchema), hashStrings("outcome_blind_matching_v1"), cfg.OutputDir, fmt.Sprint(cfg.ProjectBuckets), fmt.Sprint(cfg.MaxBucketRows), hashStrings(cfg.AuditSalt))
	return Checkpoint{
		SchemaVersion: Schema, ProtocolHash: ProtocolHash, SourceManifestHash: *manifest.ManifestHash,
		ProjectionSchemaHash: hashStrings(projectionSchema), MatchingConfigHash: hashStrings("outcome_blind_matching_v1"),
		OutputDir: cfg.OutputDir, SourceOffsets: map[string]SourceOffset{}, Phase: "PROJECT",
		CurrentBucket: 0, StagingDir: stagingDir, StagingBindingHash: binding,
		BucketCount: cfg.ProjectBuckets, MaxBucketRows: cfg.MaxBucketRows,
		PresenceCoverage: map[string]FieldCoverage{}, SourceRows: map[string]int64{},
	}
}

func verifyCheckpointBinding(cp Checkpoint, cfg Config, manifest SourceManifest) error {
	want := newProjectionCheckpoint(cfg, manifest)
	if cp.ProtocolHash != want.ProtocolHash || cp.SourceManifestHash != want.SourceManifestHash ||
		cp.ProjectionSchemaHash != want.ProjectionSchemaHash || cp.MatchingConfigHash != want.MatchingConfigHash ||
		cp.OutputDir != want.OutputDir || cp.StagingBindingHash != want.StagingBindingHash ||
		cp.BucketCount != want.BucketCount || cp.MaxBucketRows != want.MaxBucketRows || cp.StagingDir != want.StagingDir {
		return errors.New("checkpoint projection/staging binding drift")
	}
	return verifyCheckpointStaging(cp)
}

func projectAll(ctx context.Context, cfg Config, manifest SourceManifest, cp *Checkpoint, progress func(string, int64)) (projectionStats, error) {
	if manifest.ManifestHash == nil {
		return projectionStats{}, errors.New("projection requires frozen manifest hash")
	}
	stagingDir := cp.StagingDir
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return projectionStats{}, err
	}
	binding := expectedStagingBinding(*cp)
	if _, err := os.Stat(filepath.Join(stagingDir, "binding.json")); os.IsNotExist(err) {
		if err := writeJSONAtomic(filepath.Join(stagingDir, "binding.json"), binding); err != nil {
			return projectionStats{}, err
		}
	} else if err != nil {
		return projectionStats{}, err
	}
	stats := projectionStats{Unknown: map[string]int64{}, Known: map[string]int64{}, Coverage: map[string]FieldCoverage{}, SourceRows: map[string]int64{}, StagingBindingHash: cp.StagingBindingHash, StagingDir: stagingDir}
	instruments, err := loadInstrumentFacts(cfg, manifest)
	if err != nil {
		return stats, err
	}
	if cp.Phase == "AGGREGATE" || cp.Phase == "PUBLISH" {
		return aggregateStaging(ctx, cfg, manifest, cp, stats, progress)
	}
	writers := map[int]*bucketWriter{}
	closeWriters := func() error {
		var first error
		for _, writer := range writers {
			if err := writer.File.Sync(); err != nil && first == nil {
				first = err
			}
			if err := writer.File.Close(); err != nil && first == nil {
				first = err
			}
		}
		writers = map[int]*bucketWriter{}
		return first
	}
	defer closeWriters()

	for _, source := range manifest.Sources {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if source.Status == "UNKNOWN" {
			continue
		}
		switch source.Format {
		case "FROZEN_JSON_LABEL_FIREWALL":
			continue
		case "JSON_MUTABLE_SNAPSHOT":
			continue
		case "JSONL_APPEND":
		default:
			continue
		}
		offset := cp.SourceOffsets[source.SourceID]
		if offset.ByteOffset >= source.BytePrefix {
			continue
		}
		cp.CurrentSource = source.SourceID
		f, err := os.Open(filepath.Join(cfg.RepoRoot, source.RelativePath))
		if err != nil {
			return stats, err
		}
		if _, err := f.Seek(offset.ByteOffset, io.SeekStart); err != nil {
			_ = f.Close()
			return stats, err
		}
		scan := bufio.NewScanner(io.LimitReader(f, source.BytePrefix-offset.ByteOffset))
		scan.Buffer(make([]byte, 64*1024), maxLineBytes)
		var sourceRows int64
		for scan.Scan() {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return stats, err
			}
			line := scan.Bytes()
			lineStart := offset.ByteOffset
			offset.ByteOffset += int64(len(line) + 1)
			offset.CompleteLineOrdinal++
			cp.SourceOffsets[source.SourceID] = offset
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var raw map[string]any
			if err := json.Unmarshal(line, &raw); err != nil {
				stats.Malformed++
				cp.MalformedRows++
				continue
			}
			record, hasEvidence := redactRecord(source.SourceID, raw, cfg.AuditSalt, offset.CompleteLineOrdinal, lineStart, instruments)
			cp.ProjectedTotal++
			cp.SourceRows[source.SourceID]++
			sourceRows++
			if !hasEvidence {
				cp.PresenceOnlyRows++
				if source.SourceID == "executable_shadow" {
					addFieldCoverage(cp.PresenceCoverage, "latency_ms", "shadow_rows", false, "ABSENT_FIELD")
				}
				if sourceRows%int64(cfg.CheckpointEvery) == 0 {
					if err := checkpointProjection(writers, cfg.CheckpointPath, cp); err != nil {
						_ = f.Close()
						return stats, err
					}
					if progress != nil {
						progress(source.SourceID, offset.CompleteLineOrdinal)
					}
				}
				continue
			}
			bucket := stableBucket(record.KeyToken, cp.BucketCount)
			writer := writers[bucket]
			if writer == nil {
				file, openErr := os.OpenFile(bucketFile(stagingDir, bucket), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				err = openErr
				if err != nil {
					_ = f.Close()
					return stats, err
				}
				committed := committedBucket(*cp, filepath.Base(bucketFile(stagingDir, bucket)))
				writer = &bucketWriter{File: file, Size: committed.Size, Chain: committed.ChainDigest}
				writers[bucket] = writer
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				_ = f.Close()
				return stats, err
			}
			lineBytes := append(encoded, '\n')
			if _, err := writer.File.Write(lineBytes); err != nil {
				_ = f.Close()
				return stats, err
			}
			writer.Chain = nextRecordChain(writer.Chain, encoded)
			writer.Size += int64(len(lineBytes))
			if sourceRows%int64(cfg.CheckpointEvery) == 0 {
				if err := checkpointProjection(writers, cfg.CheckpointPath, cp); err != nil {
					_ = f.Close()
					return stats, err
				}
				if progress != nil {
					progress(source.SourceID, offset.CompleteLineOrdinal)
				}
			}
		}
		if err := scan.Err(); err != nil {
			_ = f.Close()
			return stats, err
		}
		_ = f.Close()
		if err := checkpointProjection(writers, cfg.CheckpointPath, cp); err != nil {
			return stats, err
		}
	}
	if err := closeWriters(); err != nil {
		return stats, err
	}
	if err := verifyCheckpointStaging(*cp); err != nil {
		return stats, err
	}
	cp.Phase, cp.CurrentSource, cp.CurrentBucket = "AGGREGATE", "", 0
	if err := checkpointProjection(writers, cfg.CheckpointPath, cp); err != nil {
		return stats, err
	}
	return aggregateStaging(ctx, cfg, manifest, cp, stats, progress)
}

func checkpointProjection(writers map[int]*bucketWriter, checkpointPath string, cp *Checkpoint) error {
	for _, writer := range writers {
		if err := writer.File.Sync(); err != nil {
			return err
		}
	}
	files := map[string]StagingFile{}
	for _, file := range cp.BucketFiles {
		files[file.RelativePath] = file
	}
	for bucket, writer := range writers {
		name := filepath.Base(bucketFile(cp.StagingDir, bucket))
		files[name] = StagingFile{RelativePath: name, Size: writer.Size, ChainDigest: writer.Chain}
	}
	cp.BucketFiles = cp.BucketFiles[:0]
	for _, file := range files {
		cp.BucketFiles = append(cp.BucketFiles, file)
	}
	sort.Slice(cp.BucketFiles, func(i, j int) bool { return cp.BucketFiles[i].RelativePath < cp.BucketFiles[j].RelativePath })
	return saveCheckpoint(checkpointPath, *cp)
}

func committedBucket(cp Checkpoint, name string) StagingFile {
	for _, file := range cp.BucketFiles {
		if file.RelativePath == name {
			return file
		}
	}
	return StagingFile{}
}

func nextRecordChain(previous string, encoded []byte) string {
	recordHash := sha256.Sum256(encoded)
	h := sha256.New()
	_, _ = h.Write([]byte(previous))
	_, _ = h.Write(recordHash[:])
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(encoded)+1))
	_, _ = h.Write(length[:])
	return hex.EncodeToString(h.Sum(nil))
}

func verifyCheckpointStaging(cp Checkpoint) error {
	if cp.Done {
		return nil
	}
	if cp.StagingDir == "" {
		return errors.New("checkpoint missing staging dir")
	}
	raw, err := os.ReadFile(filepath.Join(cp.StagingDir, "binding.json"))
	if err != nil {
		hasOffset := false
		for _, offset := range cp.SourceOffsets {
			if offset.ByteOffset > 0 {
				hasOffset = true
				break
			}
		}
		if cp.Phase == "PROJECT" && len(cp.BucketFiles) == 0 && !hasOffset && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var binding map[string]any
	if err := json.Unmarshal(raw, &binding); err != nil {
		return err
	}
	got, _, err := datatruth.HashJSON(binding)
	if err != nil {
		return err
	}
	want, _, err := datatruth.HashJSON(expectedStagingBinding(cp))
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("staging binding drift")
	}
	expected := map[string]StagingFile{}
	for _, file := range cp.BucketFiles {
		expected[file.RelativePath] = file
	}
	entries, err := os.ReadDir(cp.StagingDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "binding.json" || entry.Name() == "audit_salt.key" {
			continue
		}
		file, ok := expected[entry.Name()]
		path := filepath.Join(cp.StagingDir, entry.Name())
		if !ok {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() < file.Size {
			return fmt.Errorf("staging file truncated: %s", entry.Name())
		}
		chain, err := recomputeRecordChain(path, file.Size)
		if err != nil || chain != file.ChainDigest {
			return fmt.Errorf("staging file mutation: %s", entry.Name())
		}
		if info.Size() > file.Size {
			if err := os.Truncate(path, file.Size); err != nil {
				return err
			}
		}
		delete(expected, entry.Name())
	}
	if len(expected) != 0 {
		return errors.New("checkpoint staging file missing")
	}
	for _, file := range cp.AggregateFiles {
		path := filepath.Join(cp.StagingDir, file.RelativePath)
		info, err := os.Stat(path)
		if err != nil || info.Size() != file.Size {
			return fmt.Errorf("aggregate staging drift: %s", file.RelativePath)
		}
		hash, err := hashFileChunked(path)
		if err != nil || hash != file.SHA256 {
			return fmt.Errorf("aggregate staging mutation: %s", file.RelativePath)
		}
	}
	return nil
}

func recomputeRecordChain(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scan := bufio.NewScanner(io.LimitReader(f, size))
	scan.Buffer(make([]byte, 64*1024), maxLineBytes)
	chain := ""
	var consumed int64
	for scan.Scan() {
		encoded := scan.Bytes()
		consumed += int64(len(encoded) + 1)
		chain = nextRecordChain(chain, encoded)
	}
	if err := scan.Err(); err != nil {
		return "", err
	}
	if consumed != size {
		return "", errors.New("committed bucket does not end on LF")
	}
	return chain, nil
}

func expectedStagingBinding(cp Checkpoint) map[string]any {
	return map[string]any{
		"schema_version": projectionSchema, "protocol_hash": ProtocolHash,
		"source_manifest_hash": cp.SourceManifestHash, "bucket_count": cp.BucketCount,
		"max_bucket_rows": cp.MaxBucketRows, "projection_schema_hash": cp.ProjectionSchemaHash,
		"staging_binding_hash": cp.StagingBindingHash,
	}
}

func mustHashFile(path string) string {
	hash, _ := hashFileChunked(path)
	return hash
}

func redactRecord(source string, raw map[string]any, salt string, ordinal, byteOffset int64, instruments map[string]instrumentFact) (stagedRecord, bool) {
	record := stagedRecord{SourceID: source}
	switch source {
	case "settlements", "fast_v2_settlements":
		record.FeeBPS = stagedMetric{Applicable: true, Status: "MISSING", Reason: "ABSENT_FIELD", Scope: "settlement_rows"}
		record.EntrySlip = stagedMetric{Applicable: true, Status: "MISSING", Reason: "MISSING_REFERENCE_QUOTE", Scope: "settlement_rows"}
		record.ExitSlip = stagedMetric{Applicable: true, Status: "MISSING", Reason: "MISSING_REFERENCE_QUOTE", Scope: "settlement_rows"}
	case "exchange_fills":
		record.FeeBPS = stagedMetric{Applicable: true, Status: "MISSING", Reason: "ABSENT_FIELD", Scope: "exchange_fill_rows"}
		record.DynamicMin = stagedMetric{Applicable: true, Status: "MISSING", Reason: "MISSING_INSTRUMENT_METADATA", Scope: "execution_rows"}
	case "maker_attempts":
		record.MakerFill = stagedMetric{Applicable: true, Status: "MISSING", Reason: "ABSENT_FIELD", Scope: "maker_attempt_rows"}
		record.LatencyMS = stagedMetric{Applicable: true, Status: "MISSING", Reason: "ABSENT_FIELD", Scope: "maker_attempt_rows"}
	case "executable_shadow":
		record.LatencyMS = stagedMetric{Applicable: true, Status: "MISSING", Reason: "ABSENT_FIELD", Scope: "shadow_rows"}
	}
	if fee, ok := number(raw["fee_usdt"]); ok && fee >= 0 {
		if notional, ok := number(raw["notional"]); ok && notional > 0 {
			record.FeeBPS = stagedMetric{Applicable: true, Present: true, Value: fee / notional * 10000, Status: "DIRECT", Scope: "settlement_rows"}
		} else if record.FeeBPS.Applicable {
			record.FeeBPS.Reason = "MISSING_NOTIONAL"
		}
	} else if _, exists := raw["fee_usdt"]; exists && record.FeeBPS.Applicable {
		record.FeeBPS.Reason = "INVALID_NUMERIC"
	}
	if source == "exchange_fills" {
		fee, feeOK := flexibleNumber(raw["fee"])
		size, sizeOK := flexibleNumber(raw["fillSz"])
		price, priceOK := flexibleNumber(raw["fillPx"])
		symbol := normalizeFillSymbol(fmt.Sprint(raw["instId"]))
		instrument := instruments[symbol]
		if feeOK && strings.EqualFold(strings.TrimSpace(fmt.Sprint(raw["feeCcy"])), "USDT") {
			if sizeOK && priceOK && instrument.HasCTVal && strings.EqualFold(instrument.CTType, "linear") {
				notional := math.Abs(size) * instrument.CTVal * price
				if notional > 0 {
					record.FeeBPS = stagedMetric{Applicable: true, Present: true, Value: math.Abs(fee) / notional * 10000, Status: "RECONSTRUCTABLE", Scope: "exchange_fill_rows"}
				}
			} else if sizeOK && priceOK && instrument.HasCTVal && instrument.CTType == "" {
				record.FeeBPS = stagedMetric{Applicable: true, Status: "MISSING", Reason: "MISSING_CT_TYPE_DATA", Scope: "exchange_fill_rows"}
			} else if !instrument.HasCTVal {
				record.FeeBPS.Reason = "MISSING_INSTRUMENT_METADATA"
			} else if !sizeOK || !priceOK {
				record.FeeBPS.Reason = "INVALID_NUMERIC"
			}
		}
		if priceOK && instrument.HasLot && instrument.HasCTVal && strings.EqualFold(instrument.CTType, "linear") {
			record.DynamicMin = stagedMetric{Applicable: true, Present: true, Value: instrument.Lot * instrument.CTVal * price, Status: "RECONSTRUCTABLE", Scope: "execution_rows"}
		} else if priceOK && instrument.HasLot && instrument.HasCTVal && instrument.CTType == "" {
			record.DynamicMin = stagedMetric{Applicable: true, Status: "MISSING", Reason: "MISSING_CT_TYPE_DATA", Scope: "execution_rows"}
		} else if !instrument.HasLot || !instrument.HasCTVal {
			record.DynamicMin.Reason = "MISSING_INSTRUMENT_METADATA"
		} else if !priceOK {
			record.DynamicMin.Reason = "INVALID_NUMERIC"
		}
	}
	if value, ok := firstNumber(raw, "entry_slip_bps", "entry_slippage_bps"); ok {
		record.EntrySlip = stagedMetric{Applicable: true, Present: true, Value: value, Status: "DIRECT", Scope: "settlement_rows"}
	} else if hasAnyField(raw, "entry_slip_bps", "entry_slippage_bps") {
		record.EntrySlip.Reason = "INVALID_NUMERIC"
	}
	if value, ok := firstNumber(raw, "exit_slip_bps", "exit_slippage_bps"); ok {
		record.ExitSlip = stagedMetric{Applicable: true, Present: true, Value: value, Status: "DIRECT", Scope: "settlement_rows"}
	} else if hasAnyField(raw, "exit_slip_bps", "exit_slippage_bps") {
		record.ExitSlip.Reason = "INVALID_NUMERIC"
	}
	if value, ok := number(raw["maker_wait_ms"]); ok && value >= 0 {
		record.LatencyMS = stagedMetric{Applicable: true, Present: true, Value: value, Status: "DIRECT_MAKER_WAIT", Scope: "maker_attempt_rows"}
	} else if value, ok := number(raw["eval_rest_ms"]); ok && value >= 0 {
		record.LatencyMS = stagedMetric{Applicable: true, Status: "WEAK_PROXY", Reason: "WEAK_PROXY_ONLY", Scope: "shadow_rows"}
	} else if hasAnyField(raw, "maker_wait_ms", "eval_rest_ms") {
		record.LatencyMS.Reason = "INVALID_NUMERIC"
	}
	if value, ok := raw["maker_filled"].(bool); ok {
		record.MakerFilled = &value
		record.MakerFill = stagedMetric{Applicable: true, Present: true, Value: boolFloat(value), Status: "DIRECT", Scope: "maker_attempt_rows"}
	}
	venue := strings.TrimSpace(fmt.Sprint(raw["venue"]))
	if venue == "" && (source == "exchange_fills" || source == "settlements" || source == "fast_v2_settlements" || source == "maker_attempts" || source == "open_decisions" || source == "executable_shadow") {
		venue = "okx"
	}
	identity, kind, reason := stableIdentity(source, venue, raw, ordinal, byteOffset)
	record.IdentityKind, record.IdentityReason = kind, reason
	record.KeyToken = token(salt, identity)
	payload := record
	payload.KeyToken, payload.PayloadHash = "", ""
	record.PayloadHash, _, _ = datatruth.HashJSON(payload)
	hasEvidence := record.FeeBPS.Applicable || record.EntrySlip.Applicable || record.ExitSlip.Applicable || record.LatencyMS.Applicable || record.DynamicMin.Applicable || record.MakerFill.Applicable
	if source == "executable_shadow" && !record.LatencyMS.Present && record.LatencyMS.Reason == "ABSENT_FIELD" {
		hasEvidence = false
	}
	return record, hasEvidence
}

func stableIdentity(source, venue string, raw map[string]any, ordinal, byteOffset int64) (string, string, string) {
	for _, field := range []string{"tradeId", "billId", "ordId", "clOrdId"} {
		if value := strings.TrimSpace(fmt.Sprint(raw[field])); value != "" && value != "<nil>" {
			return "shared\x00" + venue + "\x00" + field + "\x00" + value, "SHARED_STABLE_ID", ""
		}
	}
	if source == "settlements" || source == "fast_v2_settlements" {
		for _, field := range []string{"position_id", "fast_event_id"} {
			if value := strings.TrimSpace(fmt.Sprint(raw[field])); value != "" && value != "<nil>" {
				return "settlement_shared\x00" + venue + "\x00" + field + "\x00" + value, "SHARED_SETTLEMENT_ID", ""
			}
		}
		if identity, ok := compositeIdentity(source, raw, []string{"symbol", "side", "time", "entry", "exit", "notional"}); ok {
			return identity, "PROTOCOL_FALLBACK", "CROSS_SOURCE_IDENTITY_UNAVAILABLE"
		}
	}
	keys := map[string][]string{
		"exchange_fills":    {"instId", "fillTime", "side", "posSide", "fillPx", "fillSz", "fee"},
		"maker_attempts":    {"venue", "symbol", "side", "recorded_at", "maker_order_price", "maker_wait_ms"},
		"open_decisions":    {"venue", "symbol", "side", "recorded_at", "position_id", "decision_result"},
		"executable_shadow": {"venue", "symbol", "recorded_at", "snapshot_id"},
		"equity_snapshots":  {"venue", "ts"},
	}
	if identity, ok := compositeIdentity(source, raw, keys[source]); ok {
		return identity, "PROTOCOL_FALLBACK", "CROSS_SOURCE_IDENTITY_UNAVAILABLE"
	}
	return fmt.Sprintf("%s\x00%s\x00line\x00%d\x00%d", source, venue, ordinal, byteOffset), "LINE_IDENTITY", "CROSS_SOURCE_IDENTITY_UNAVAILABLE"
}

func compositeIdentity(source string, raw map[string]any, fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	parts := []string{source, "fallback"}
	for _, field := range fields {
		value := strings.TrimSpace(fmt.Sprint(raw[field]))
		if value == "" || value == "<nil>" {
			return "", false
		}
		parts = append(parts, field, value)
	}
	return strings.Join(parts, "\x00"), true
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func flexibleNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func firstNumber(raw map[string]any, fields ...string) (float64, bool) {
	for _, field := range fields {
		if value, ok := flexibleNumber(raw[field]); ok {
			return value, true
		}
	}
	return 0, false
}

func hasAnyField(raw map[string]any, fields ...string) bool {
	for _, field := range fields {
		if _, ok := raw[field]; ok {
			return true
		}
	}
	return false
}

func normalizeFillSymbol(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), "-SWAP")
}

func loadInstrumentFacts(cfg Config, manifest SourceManifest) (map[string]instrumentFact, error) {
	facts := map[string]instrumentFact{}
	for _, source := range manifest.Sources {
		if source.SourceID != "instrument_metadata" || source.Status == "UNKNOWN" {
			continue
		}
		raw, err := readFrozenSource(cfg.OutputDir, source)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Instruments map[string]map[string]any `json:"instruments"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
		for symbol, fields := range envelope.Instruments {
			fact := instrumentFact{}
			fact.CTVal, fact.HasCTVal = firstNumber(fields, "ct_val", "ctVal", "contract_value")
			fact.Lot, fact.HasLot = firstNumber(fields, "lot", "lotSz", "lot_size")
			fact.Tick, fact.HasTick = firstNumber(fields, "tick", "tickSz", "tick_size")
			for _, key := range []string{"ct_type", "ctType", "contract_type"} {
				if value := strings.TrimSpace(fmt.Sprint(fields[key])); value != "" && value != "<nil>" {
					fact.CTType = value
					break
				}
			}
			facts[symbol] = fact
		}
	}
	return facts, nil
}

func aggregateStaging(ctx context.Context, cfg Config, manifest SourceManifest, cp *Checkpoint, stats projectionStats, progress func(string, int64)) (projectionStats, error) {
	dir, buckets, maxRows := cp.StagingDir, cp.BucketCount, cp.MaxBucketRows
	fileBindings := make([]string, 0, len(cp.BucketFiles))
	for _, file := range cp.BucketFiles {
		fileBindings = append(fileBindings, file.RelativePath+":"+fmt.Sprint(file.Size)+":"+file.ChainDigest)
	}
	aggregateDir := filepath.Join(dir, "aggregates")
	if err := os.MkdirAll(aggregateDir, 0o700); err != nil {
		return stats, err
	}
	for bucket := cp.CurrentBucket; bucket < buckets; bucket++ {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		path := bucketFile(dir, bucket)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			cp.CurrentBucket = bucket + 1
			if err := saveCheckpoint(cfg.CheckpointPath, *cp); err != nil {
				return stats, err
			}
			continue
		}
		if err != nil {
			return stats, err
		}
		f, err := os.Open(path)
		if err != nil {
			return stats, err
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 64*1024), maxLineBytes)
		seen := map[string]*bucketObservation{}
		var rows, bufferedBytes int64
		for scan.Scan() {
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return stats, err
			}
			rows++
			if rows > int64(maxRows) {
				_ = f.Close()
				return stats, fmt.Errorf("bucket %d exceeds frozen max_bucket_rows=%d", bucket, maxRows)
			}
			var record stagedRecord
			if err := json.Unmarshal(scan.Bytes(), &record); err != nil {
				_ = f.Close()
				return stats, err
			}
			observation := seen[record.KeyToken]
			if observation == nil {
				size := int64(len(scan.Bytes()))
				seen[record.KeyToken] = &bucketObservation{Records: []stagedRecord{record}, SeenPayloads: map[string]struct{}{record.PayloadHash: {}}, ByteCount: size}
				bufferedBytes += size
			} else {
				if _, duplicate := observation.SeenPayloads[record.PayloadHash]; duplicate {
					observation.ExactDuplicates++
				} else {
					observation.SeenPayloads[record.PayloadHash] = struct{}{}
					observation.Records = append(observation.Records, record)
				}
			}
		}
		if err := scan.Err(); err != nil {
			_ = f.Close()
			return stats, err
		}
		_ = f.Close()
		part := projectionStats{Known: map[string]int64{}, Unknown: map[string]int64{}, Coverage: map[string]FieldCoverage{}, SourceRows: map[string]int64{}, ProvenanceCounts: map[string]int64{}, PeakBufferedRows: int64(len(seen)), PeakBufferedBytes: bufferedBytes}
		for _, observation := range seen {
			part.Duplicates += observation.ExactDuplicates
			aggregateIdentity(observation.Records, &part)
		}
		aggregate := bucketAggregate{Bucket: bucket, Rows: part.Rows, Duplicates: part.Duplicates, Conflicts: part.Conflicts, Known: part.Known, Unknown: part.Unknown, Coverage: part.Coverage, SourceRows: part.SourceRows, ProvenanceCounts: part.ProvenanceCounts, SuppressedLowerPriorityFacts: part.SuppressedLowerPriorityFacts, UnresolvedCrossSourceIdentities: part.UnresolvedCrossSourceIdentities, CrossSourceConflicts: part.CrossSourceConflicts, PeakBufferedRows: part.PeakBufferedRows, PeakBufferedBytes: part.PeakBufferedBytes}
		hashPayload := aggregate
		hashPayload.AggregateHash = nil
		hash, _, err := datatruth.HashJSON(hashPayload)
		if err != nil {
			return stats, err
		}
		aggregate.AggregateHash = &hash
		aggregatePath := filepath.Join(aggregateDir, fmt.Sprintf("bucket-%04d.json", bucket))
		if err := writeJSONAtomic(aggregatePath, aggregate); err != nil {
			return stats, err
		}
		info, err = os.Stat(aggregatePath)
		if err != nil {
			return stats, err
		}
		cp.AggregateFiles = append(cp.AggregateFiles, StagingFile{RelativePath: filepath.ToSlash(filepath.Join("aggregates", filepath.Base(aggregatePath))), Size: info.Size(), SHA256: mustHashFile(aggregatePath)})
		cp.CurrentBucket = bucket + 1
		if err := saveCheckpoint(cfg.CheckpointPath, *cp); err != nil {
			return stats, err
		}
		if progress != nil {
			progress(fmt.Sprintf("bucket-%04d", bucket), rows)
		}
	}
	sort.Strings(fileBindings)
	stats = projectionStats{Unknown: map[string]int64{}, Known: map[string]int64{}, Coverage: map[string]FieldCoverage{}, SourceRows: map[string]int64{}, ProvenanceCounts: map[string]int64{}, ProjectedTotal: cp.ProjectedTotal, Malformed: cp.MalformedRows, StagingBindingHash: cp.StagingBindingHash, StagingDir: cp.StagingDir, StagingFilesHash: hashStrings(fileBindings...)}
	for _, file := range cp.AggregateFiles {
		raw, err := os.ReadFile(filepath.Join(dir, file.RelativePath))
		if err != nil {
			return stats, err
		}
		var aggregate bucketAggregate
		if err := json.Unmarshal(raw, &aggregate); err != nil {
			return stats, err
		}
		stats.Rows += aggregate.Rows
		stats.Duplicates += aggregate.Duplicates
		stats.Conflicts += aggregate.Conflicts
		for k, v := range aggregate.Known {
			stats.Known[k] += v
		}
		for k, v := range aggregate.Unknown {
			stats.Unknown[k] += v
		}
		mergeCoverage(stats.Coverage, aggregate.Coverage)
		for key, count := range aggregate.ProvenanceCounts {
			stats.ProvenanceCounts[key] += count
		}
		stats.SuppressedLowerPriorityFacts += aggregate.SuppressedLowerPriorityFacts
		stats.UnresolvedCrossSourceIdentities += aggregate.UnresolvedCrossSourceIdentities
		stats.CrossSourceConflicts += aggregate.CrossSourceConflicts
		if aggregate.PeakBufferedRows > stats.PeakBufferedRows {
			stats.PeakBufferedRows = aggregate.PeakBufferedRows
		}
		if aggregate.PeakBufferedBytes > stats.PeakBufferedBytes {
			stats.PeakBufferedBytes = aggregate.PeakBufferedBytes
		}
	}
	stats.Rows += cp.PresenceOnlyRows
	mergeCoverage(stats.Coverage, cp.PresenceCoverage)
	for source, rows := range cp.SourceRows {
		stats.SourceRows[source] += rows
	}
	stats.CheckpointHashedBytes = cp.CheckpointHashedBytes
	for _, source := range manifest.Sources {
		if source.Status == "UNKNOWN" {
			continue
		}
		if source.Format == "FROZEN_JSON_LABEL_FIREWALL" {
			raw, err := readFrozenSource(cfg.OutputDir, source)
			if err != nil {
				return stats, err
			}
			if err := projectFirewallJSON(raw, source.SourceID, &stats); err != nil {
				return stats, err
			}
		}
		if source.Format == "JSON_MUTABLE_SNAPSHOT" {
			raw, err := readFrozenSource(cfg.OutputDir, source)
			if err != nil {
				return stats, err
			}
			if err := projectInstrumentSnapshot(raw, &stats); err != nil {
				return stats, err
			}
		}
		if source.SourceID == "playbook_manifest" && source.Format == "FROZEN_JSON" {
			raw, err := readFrozenSource(cfg.OutputDir, source)
			if err != nil {
				return stats, err
			}
			if err := validatePlaybookManifest(raw); err != nil {
				return stats, err
			}
		}
	}
	cp.Phase = "PUBLISH"
	if err := saveCheckpoint(cfg.CheckpointPath, *cp); err != nil {
		return stats, err
	}
	return stats, nil
}

func validatePlaybookManifest(raw []byte) error {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	stored, _ := doc["manifest_hash"].(string)
	doc["manifest_hash"] = nil
	computed, _, err := datatruth.HashJSON(doc)
	if err != nil {
		return err
	}
	if computed != stored {
		// The frozen G10 v1 manifest predates this audit protocol and used an
		// empty string, not JSON null, for its own self-exclusion.
		doc["manifest_hash"] = ""
		computed, _, err = datatruth.HashJSON(doc)
		if err != nil {
			return err
		}
	}
	if stored != "ef82da8421a9c6eb02b9854eebaf47673e94365918a058b610077d35064ce0c5" || computed != stored {
		return errors.New("frozen playbook manifest hash drift")
	}
	if doc["protocol_hash"] != "9f86ce54c24b00de41d1a6735a4f8190b00d49dce2a66371242646ffecd10750" ||
		doc["playbook_hash"] != "8ceae12715dc9cc885fcaf77c230b7c44dc3becfe3644372ad1f276bd2d8a8f0" ||
		doc["experiment_hash"] != "a471569db03b5667815a944506a935cac8c5f2b74371a434807cd2c306995d19" {
		return errors.New("frozen playbook manifest binding drift")
	}
	return nil
}

func aggregateIdentity(records []stagedRecord, stats *projectionStats) {
	if len(records) == 0 {
		return
	}
	stats.Rows++
	shared := false
	sources := map[string]struct{}{}
	for _, record := range records {
		sources[record.SourceID] = struct{}{}
		if record.IdentityKind == "SHARED_STABLE_ID" || record.IdentityKind == "SHARED_SETTLEMENT_ID" {
			shared = true
		}
	}
	// A shared-capable ID is only resolved when it is actually observed in
	// more than one source. A lone trade/order ID remains an unmatched
	// cross-source identity fact; it must not be silently treated as joined.
	if !shared || len(sources) < 2 {
		stats.UnresolvedCrossSourceIdentities++
	}
	for _, field := range []string{"fee_bps", "entry_slip_bps", "exit_slip_bps", "latency_ms", "dynamic_min_notional_usdt", "maker_fill_rate"} {
		type candidate struct {
			record   stagedRecord
			metric   stagedMetric
			priority int
			hash     string
		}
		var candidates []candidate
		for _, record := range records {
			metric := recordMetric(record, field)
			if !metric.Applicable {
				continue
			}
			stats.ProvenanceCounts[field+"|"+record.SourceID]++
			hash, _, _ := datatruth.HashJSON(metric)
			candidates = append(candidates, candidate{record: record, metric: metric, priority: factPriority(field, record.SourceID), hash: hash})
		}
		if len(candidates) == 0 {
			continue
		}
		present := candidates[:0]
		for _, candidate := range candidates {
			if candidate.metric.Present {
				present = append(present, candidate)
			}
		}
		selection := candidates
		if len(present) > 0 {
			selection = present
		}
		sort.SliceStable(selection, func(i, j int) bool {
			if selection[i].priority != selection[j].priority {
				return selection[i].priority > selection[j].priority
			}
			return selection[i].record.SourceID < selection[j].record.SourceID
		})
		topPriority := selection[0].priority
		topHash := selection[0].hash
		topConflict := false
		suppressed := int64(0)
		lowerConflicts := int64(0)
		settlementPair := false
		_, hasSettlement := sources["settlements"]
		_, hasFast := sources["fast_v2_settlements"]
		if hasSettlement && hasFast && records[0].IdentityKind == "SHARED_SETTLEMENT_ID" {
			settlementPair = true
		}
		for _, candidate := range selection[1:] {
			if candidate.priority == topPriority && candidate.hash != topHash {
				topConflict = true
			}
			if settlementPair && candidate.metric.Present && candidate.hash != topHash {
				topConflict = true
			}
			if candidate.priority < topPriority && candidate.metric.Present {
				suppressed++
				if candidate.hash != topHash {
					lowerConflicts++
				}
			}
		}
		if topConflict {
			stats.Conflicts++
			stats.CrossSourceConflicts++
			metric := selection[0].metric
			addFieldCoverage(stats.Coverage, field, metric.Scope, false, "SOURCE_CONFLICT")
			stats.Unknown[field]++
			continue
		}
		stats.SuppressedLowerPriorityFacts += suppressed
		stats.CrossSourceConflicts += lowerConflicts
		aggregateSelectedMetric(field, selection[0].metric, stats)
	}
}

func recordMetric(record stagedRecord, field string) stagedMetric {
	switch field {
	case "fee_bps":
		return record.FeeBPS
	case "entry_slip_bps":
		return record.EntrySlip
	case "exit_slip_bps":
		return record.ExitSlip
	case "latency_ms":
		return record.LatencyMS
	case "dynamic_min_notional_usdt":
		return record.DynamicMin
	case "maker_fill_rate":
		return record.MakerFill
	default:
		return stagedMetric{}
	}
}

func aggregateSelectedMetric(field string, metric stagedMetric, stats *projectionStats) {
	addFieldCoverage(stats.Coverage, field, metric.Scope, metric.Present, metric.Reason)
	if metric.Present {
		stats.Known[field]++
	} else {
		stats.Unknown[field]++
	}
}

func factPriority(field, source string) int {
	if field == "fee_bps" {
		switch source {
		case "exchange_fills":
			return 30
		case "settlements":
			return 20
		case "fast_v2_settlements":
			return 10
		}
	}
	if field == "entry_slip_bps" || field == "exit_slip_bps" {
		switch source {
		case "settlements":
			return 30
		case "fast_v2_settlements":
			return 20
		case "exchange_fills":
			return 10
		}
	}
	return 10
}

func coverageKey(field, scope string) string { return field + "|" + scope }
func addFieldCoverage(target map[string]FieldCoverage, field, scope string, known bool, reason string) {
	key := coverageKey(field, scope)
	value := target[key]
	value.Denominator++
	if value.Reasons == nil {
		value.Reasons = map[string]int64{}
	}
	if known {
		value.Numerator++
	} else {
		if reason == "" {
			reason = "ABSENT_FIELD"
		}
		value.Reasons[reason]++
	}
	target[key] = value
}
func mergeCoverage(target, source map[string]FieldCoverage) {
	for key, incoming := range source {
		value := target[key]
		value.Numerator += incoming.Numerator
		value.Denominator += incoming.Denominator
		if value.Reasons == nil {
			value.Reasons = map[string]int64{}
		}
		for reason, count := range incoming.Reasons {
			value.Reasons[reason] += count
		}
		target[key] = value
	}
}

func stableBucket(token string, count int) int {
	sum := sha256.Sum256([]byte(token))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(count))
}

func bucketFile(dir string, bucket int) string {
	return filepath.Join(dir, fmt.Sprintf("bucket-%04d.jsonl", bucket))
}

func hashFileChunked(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func cleanupProjectionStaging(dir string) error {
	if dir == "" {
		return nil
	}
	base := filepath.Base(filepath.Clean(dir))
	if base != ".evidenceaudit-staging" {
		return errors.New("refusing to remove unexpected staging directory")
	}
	return os.RemoveAll(dir)
}
