// Package evidenceaudit implements the outcome-blind Phase A G10 execution
// evidence freeze. It intentionally does not calculate returns or verdicts
// requiring future labels.
package evidenceaudit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"lingxi/internal/datatruth"
)

const (
	ProtocolHash = "dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3"
	Schema       = "lingxi.g10-execution-evidence-audit.v1"
	maxLineBytes = 4 * 1024 * 1024
)

var sources = []SourceSpec{
	{"settlements", "records/okx_directional/settlements.jsonl", "JSONL_APPEND"},
	{"fast_v2_settlements", "records/okx_directional/fast_v2_settlements.jsonl", "JSONL_APPEND"},
	{"exchange_fills", "records/fast_exchange_fills_cache.jsonl", "JSONL_APPEND"},
	{"maker_attempts", "records/okx_directional/fast_maker_attempts.jsonl", "JSONL_APPEND"},
	{"executable_shadow", "records/okx_directional/fast_executable_shadow_events.jsonl", "JSONL_APPEND"},
	{"open_decisions", "records/okx_directional/open_decision_events.jsonl", "JSONL_APPEND"},
	{"instrument_metadata", "records/okx_directional/instrument_meta.json", "JSON_MUTABLE_SNAPSHOT"},
	{"equity_snapshots", "records/crossvenue/equity_snapshots.jsonl", "JSONL_APPEND"},
	{"validation_triggers_okx_1h", "research/firstbattle/g10_wild_brain/event_map_okx_1h/event_map_validation_okx_1h.json", "FROZEN_JSON_LABEL_FIREWALL"},
	{"validation_triggers_okx_5m", "research/firstbattle/g10_wild_brain/event_map_okx_5m/event_map_validation_okx_5m.json", "FROZEN_JSON_LABEL_FIREWALL"},
	{"playbook_bindings", "research/firstbattle/g10_wild_brain/playbooks/g10_event_playbooks.json", "FROZEN_JSON_LABEL_FIREWALL"},
	{"playbook_manifest", "research/firstbattle/g10_wild_brain/playbooks/event_playbook_manifest.json", "FROZEN_JSON"},
}

type SourceSpec struct{ ID, Path, Format string }
type Config struct {
	RepoRoot                string `json:"repo_root"`
	OutputDir               string `json:"output_dir"`
	CheckpointPath          string `json:"checkpoint_path"`
	ProtocolPath            string `json:"protocol_path"`
	ProgressEvery           int    `json:"progress_every"`
	AuditSalt               string `json:"audit_salt"`
	ProjectBuckets          int    `json:"project_buckets"`
	MaxBucketRows           int    `json:"max_bucket_rows"`
	CheckpointEvery         int    `json:"checkpoint_every"`
	MaxPrefixBytesPerSource int64  `json:"max_prefix_bytes_per_source"`
}
type SourceEntry struct {
	SourceID                string `json:"source_id"`
	RelativePath            string `json:"relative_path"`
	Format                  string `json:"format"`
	Device                  int64  `json:"device"`
	Inode                   int64  `json:"inode"`
	CapturedAt              string `json:"captured_at"`
	BytePrefix              int64  `json:"byte_prefix"`
	OriginalFileSize        int64  `json:"original_file_size"`
	OriginalBytePrefix      int64  `json:"original_byte_prefix"`
	CompleteLineCount       int64  `json:"complete_line_count"`
	PartialTrailingBytes    int64  `json:"partial_trailing_bytes"`
	PrefixSHA256            string `json:"prefix_sha256"`
	WholeFileSHA256         string `json:"whole_file_sha256"`
	FirstCompleteLineSHA256 string `json:"first_complete_line_sha256"`
	LastCompleteLineSHA256  string `json:"last_complete_line_sha256"`
	Status                  string `json:"status"`
	MalformedCompleteLines  int64  `json:"malformed_complete_lines"`
	EmptyLines              int64  `json:"empty_lines"`
	SnapshotPath            string `json:"snapshot_path,omitempty"`
}
type SourceManifest struct {
	SchemaVersion string        `json:"schema_version"`
	Generation    string        `json:"generation"`
	ProtocolHash  string        `json:"protocol_hash"`
	CapturedAt    string        `json:"captured_at"`
	Sources       []SourceEntry `json:"sources"`
	ManifestHash  *string       `json:"manifest_hash"`
	ScopeStatus   string        `json:"scope_status"`
}
type SourceOffset struct {
	ByteOffset          int64 `json:"byte_offset"`
	CompleteLineOrdinal int64 `json:"complete_line_ordinal"`
}
type Checkpoint struct {
	SchemaVersion         string                   `json:"schema_version"`
	ProtocolHash          string                   `json:"protocol_hash"`
	SourceManifestHash    string                   `json:"source_manifest_hash"`
	ProjectionSchemaHash  string                   `json:"projection_schema_hash"`
	MatchingConfigHash    string                   `json:"matching_config_hash"`
	OutputDir             string                   `json:"output_dir"`
	SourceOffsets         map[string]SourceOffset  `json:"source_offsets"`
	Phase                 string                   `json:"phase"`
	CurrentSource         string                   `json:"current_source,omitempty"`
	CurrentBucket         int                      `json:"current_bucket"`
	StagingDir            string                   `json:"staging_dir"`
	StagingBindingHash    string                   `json:"staging_binding_hash"`
	BucketCount           int                      `json:"bucket_count"`
	MaxBucketRows         int                      `json:"max_bucket_rows"`
	BucketFiles           []StagingFile            `json:"bucket_files"`
	AggregateFiles        []StagingFile            `json:"aggregate_files"`
	ProjectedTotal        int64                    `json:"projected_total"`
	MalformedRows         int64                    `json:"malformed_rows"`
	PresenceOnlyRows      int64                    `json:"presence_only_rows"`
	PresenceCoverage      map[string]FieldCoverage `json:"presence_coverage"`
	SourceRows            map[string]int64         `json:"source_rows"`
	CheckpointHashedBytes int64                    `json:"checkpoint_hashed_bytes"`
	Done                  bool                     `json:"done"`
	CheckpointHash        *string                  `json:"checkpoint_hash"`
}
type StagingFile struct {
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256,omitempty"`
	ChainDigest  string `json:"chain_digest,omitempty"`
}
type Result struct {
	Manifest SourceManifest
	Summary  map[string]any
}

func AllowedSources() []SourceSpec { return append([]SourceSpec(nil), sources...) }

func VerifyProtocol(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	audit, ok := doc["audit"].(map[string]any)
	if !ok {
		return errors.New("protocol missing audit object")
	}
	audit["protocol_hash"] = nil
	got, _, err := datatruth.HashJSON(doc)
	if err != nil {
		return err
	}
	if got != ProtocolHash {
		return fmt.Errorf("protocol hash drift: got %s", got)
	}
	return nil
}

func Run(ctx context.Context, cfg Config, progress func(string, int64)) (Result, error) {
	applyProjectionDefaults(&cfg)
	if cfg.ProtocolPath == "" || cfg.OutputDir == "" || cfg.RepoRoot == "" || cfg.CheckpointPath == "" {
		return Result{}, errors.New("repo root, output dir, protocol path, and checkpoint path required")
	}
	if err := VerifyProtocol(cfg.ProtocolPath); err != nil {
		return Result{}, err
	}
	if filepath.Clean(cfg.OutputDir) == filepath.Clean(cfg.RepoRoot) || !filepath.IsAbs(cfg.OutputDir) {
		return Result{}, errors.New("output directory must be explicit absolute new research directory")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return Result{}, err
	}
	cp, cpOK, err := loadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		return Result{}, err
	}
	if !cpOK || !cp.Done {
		if err := resolveAuditSalt(&cfg, cpOK); err != nil {
			return Result{}, err
		}
	}
	var manifest SourceManifest
	if cpOK {
		raw, err := os.ReadFile(filepath.Join(cfg.OutputDir, ".source_manifest.staging.json"))
		if os.IsNotExist(err) {
			raw, err = os.ReadFile(filepath.Join(cfg.OutputDir, "source_manifest.json"))
		}
		if err != nil {
			return Result{}, errors.New("checkpoint exists without source manifest")
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return Result{}, err
		}
		if manifest.ManifestHash == nil || cp.ProtocolHash != ProtocolHash || cp.OutputDir != cfg.OutputDir || cp.SourceManifestHash != *manifest.ManifestHash {
			return Result{}, errors.New("checkpoint binding drift")
		}
	} else {
		manifest, err = freezeAll(ctx, cfg, progress)
		if err != nil {
			return Result{}, err
		}
		if err := writeJSONAtomic(filepath.Join(cfg.OutputDir, ".source_manifest.staging.json"), manifest); err != nil {
			return Result{}, err
		}
		cp = newProjectionCheckpoint(cfg, manifest)
		if err := saveCheckpoint(cfg.CheckpointPath, cp); err != nil {
			return Result{}, err
		}
		cpOK = true
	}
	if err := verifyManifestInputs(cfg.RepoRoot, manifest); err != nil {
		return Result{}, err
	}
	if cpOK && cp.Done {
		if _, err := os.Stat(filepath.Join(cfg.OutputDir, "source_manifest.json")); os.IsNotExist(err) {
			if err := os.Rename(filepath.Join(cfg.OutputDir, ".source_manifest.staging.json"), filepath.Join(cfg.OutputDir, "source_manifest.json")); err != nil {
				return Result{}, err
			}
		}
		return loadResult(cfg.OutputDir)
	}
	if err := verifyCheckpointBinding(cp, cfg, manifest); err != nil {
		return Result{}, err
	}
	stats, err := projectAll(ctx, cfg, manifest, &cp, progress)
	if err != nil {
		return Result{}, err
	}
	completed := cp
	completed.Done = true
	completed.Phase = "PUBLISH"
	completed.CheckpointHash = nil
	completedHash, _, err := datatruth.HashJSON(completed)
	if err != nil {
		return Result{}, err
	}
	completed.CheckpointHash = &completedHash
	artifacts, summary := buildArtifacts(manifest, stats)
	artifacts["checkpoint.json"] = completed
	finalizeArtifactChain(artifacts, summary, manifest)
	progressCheckpoint := completed
	progressCheckpoint.Done = false
	progressCheckpoint.CheckpointHash = nil
	if err := saveCheckpoint(cfg.CheckpointPath, progressCheckpoint); err != nil {
		return Result{}, err
	}
	if err := publishArtifacts(cfg.OutputDir, artifacts); err != nil {
		return Result{}, err
	}
	if err := saveCheckpoint(cfg.CheckpointPath, completed); err != nil {
		return Result{}, err
	}
	if err := os.Rename(filepath.Join(cfg.OutputDir, ".source_manifest.staging.json"), filepath.Join(cfg.OutputDir, "source_manifest.json")); err != nil {
		return Result{}, err
	}
	if err := cleanupProjectionStaging(stats.StagingDir); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Summary: summary}, nil
}

func freezeAll(ctx context.Context, cfg Config, progress func(string, int64)) (SourceManifest, error) {
	scope := "FULL_PREFIX"
	if cfg.MaxPrefixBytesPerSource > 0 {
		scope = "TRUNCATED_SMOKE"
	}
	m := SourceManifest{SchemaVersion: "lingxi.g10-execution-evidence-source-manifest.v1", Generation: "firstbattle-g10-execution-evidence-audit-v1", ProtocolHash: ProtocolHash, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), ScopeStatus: scope}
	for _, spec := range sources {
		if ctx.Err() != nil {
			return m, ctx.Err()
		}
		entry, err := freezeOneBounded(cfg.RepoRoot, cfg.OutputDir, spec, cfg.MaxPrefixBytesPerSource)
		if err != nil {
			return m, fmt.Errorf("%s: %w", spec.ID, err)
		}
		m.Sources = append(m.Sources, entry)
		if progress != nil {
			progress(spec.ID, entry.CompleteLineCount)
		}
	}
	sort.Slice(m.Sources, func(i, j int) bool { return m.Sources[i].SourceID < m.Sources[j].SourceID })
	copyM := m
	copyM.ManifestHash = nil
	h, _, err := datatruth.HashJSON(copyM)
	if err != nil {
		return m, err
	}
	m.ManifestHash = &h
	return m, nil
}

func freezeOne(root, out string, spec SourceSpec) (SourceEntry, error) {
	return freezeOneBounded(root, out, spec, 0)
}

func freezeOneBounded(root, out string, spec SourceSpec, maxPrefix int64) (SourceEntry, error) {
	p := filepath.Join(root, spec.Path)
	e := SourceEntry{SourceID: spec.ID, RelativePath: spec.Path, Format: spec.Format, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "UNKNOWN"}
	before, err := os.Stat(p)
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	e.Device, e.Inode = statID(before)
	initialSize := before.Size()
	e.OriginalFileSize = initialSize
	f, err := os.Open(p)
	if err != nil {
		return e, err
	}
	defer f.Close()
	readSize := initialSize
	truncated := false
	if spec.Format == "JSONL_APPEND" && maxPrefix > 0 && readSize > maxPrefix {
		readSize = maxPrefix
		truncated = true
	}
	whole := sha256.New()
	buf := make([]byte, 256*1024)
	var read, lastLF int64
	for read < readSize {
		n, readErr := f.Read(buf[:min64(int64(len(buf)), readSize-read)])
		if n > 0 {
			chunk := buf[:n]
			_, _ = whole.Write(chunk)
			if i := bytes.LastIndexByte(chunk, '\n'); i >= 0 {
				lastLF = read + int64(i) + 1
			}
			read += int64(n)
		}
		if readErr != nil && readErr != io.EOF {
			return e, readErr
		}
		if n == 0 {
			break
		}
	}
	if read != readSize {
		return e, errors.New("source truncated during capture")
	}
	after, err := os.Stat(p)
	if err != nil {
		return e, err
	}
	d, ino := statID(after)
	if d != e.Device || ino != e.Inode || after.Size() < initialSize {
		return e, errors.New("source inode/truncation changed during capture")
	}
	e.WholeFileSHA256 = hex.EncodeToString(whole.Sum(nil))
	if spec.Format != "JSONL_APPEND" {
		e.SnapshotPath = filepath.Join("input_snapshots", spec.ID+".json")
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return e, err
		}
		if err := copyNAtomic(filepath.Join(out, e.SnapshotPath), f, initialSize); err != nil {
			return e, err
		}
		e.BytePrefix, e.OriginalBytePrefix, e.Status = initialSize, initialSize, "FROZEN"
		e.PrefixSHA256 = e.WholeFileSHA256
		return e, nil
	}
	originalPrefix, err := lastLFOffset(f, initialSize)
	if err != nil {
		return e, err
	}
	e.OriginalBytePrefix = originalPrefix
	e.BytePrefix, e.PartialTrailingBytes = lastLF, readSize-lastLF
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return e, err
	}
	h := sha256.New()
	sc := bufio.NewScanner(io.TeeReader(io.LimitReader(f, lastLF), h))
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			e.EmptyLines++
			continue
		}
		e.CompleteLineCount++
		if e.FirstCompleteLineSHA256 == "" {
			e.FirstCompleteLineSHA256 = sha(line)
		}
		e.LastCompleteLineSHA256 = sha(line)
	}
	if err := sc.Err(); err != nil {
		return e, err
	}
	e.PrefixSHA256, e.Status = hex.EncodeToString(h.Sum(nil)), "FROZEN"
	if truncated && e.BytePrefix < e.OriginalBytePrefix {
		e.Status = "TRUNCATED_SMOKE"
	}
	return e, nil
}

func lastLFOffset(f *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	const chunk = int64(256 * 1024)
	for end := size; end > 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

func verifyManifestInputs(root string, m SourceManifest) error {
	for _, e := range m.Sources {
		p := filepath.Join(root, e.RelativePath)
		if e.Status == "UNKNOWN" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		d, ino := statID(info)
		if d != e.Device || ino != e.Inode || info.Size() < e.BytePrefix {
			return fmt.Errorf("%s: inode/truncation drift", e.SourceID)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.CopyN(h, f, e.BytePrefix)
		_ = f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != e.PrefixSHA256 {
			return fmt.Errorf("%s: prefix mutation", e.SourceID)
		}
	}
	return nil
}

func readFrozenSource(outputDir string, source SourceEntry) ([]byte, error) {
	if source.SnapshotPath == "" {
		return nil, fmt.Errorf("%s: frozen source snapshot missing", source.SourceID)
	}
	path := filepath.Join(outputDir, source.SnapshotPath)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() != source.BytePrefix {
		return nil, fmt.Errorf("%s: snapshot size drift", source.SourceID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hash := sha(raw)
	if hash != source.PrefixSHA256 || (source.WholeFileSHA256 != "" && hash != source.WholeFileSHA256) {
		return nil, fmt.Errorf("%s: snapshot hash drift", source.SourceID)
	}
	return raw, nil
}

type projectionStats struct {
	Rows, Duplicates, Conflicts, Malformed int64
	Unknown                                map[string]int64
	Known                                  map[string]int64
	SupportCells                           []string
	WinnerBindings                         []string
	FirewallRejectedFields                 int64
	ProjectedTotal                         int64
	PeakBufferedRows                       int64
	PeakBufferedBytes                      int64
	StagingBindingHash                     string
	StagingFilesHash                       string
	StagingDir                             string
	CheckpointHashedBytes                  int64
	Coverage                               map[string]FieldCoverage
	SourceRows                             map[string]int64
	ProvenanceCounts                       map[string]int64
	SuppressedLowerPriorityFacts           int64
	UnresolvedCrossSourceIdentities        int64
	CrossSourceConflicts                   int64
}
type FieldCoverage struct {
	Numerator   int64            `json:"numerator"`
	Denominator int64            `json:"denominator"`
	Reasons     map[string]int64 `json:"missing_reason_counts"`
}

func projectInstrumentSnapshot(raw []byte, s *projectionStats) error {
	if s.Coverage == nil {
		s.Coverage = map[string]FieldCoverage{}
	}
	var envelope struct {
		Instruments map[string]map[string]any `json:"instruments"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	for _, fields := range envelope.Instruments {
		_, lot := firstNumber(fields, "lot", "lotSz", "lot_size")
		_, ctVal := firstNumber(fields, "ct_val", "ctVal", "contract_value")
		ctType := false
		for _, key := range []string{"ct_type", "ctType", "contract_type"} {
			if value := strings.TrimSpace(fmt.Sprint(fields[key])); value != "" && value != "<nil>" {
				ctType = true
				break
			}
		}
		addFieldCoverage(s.Coverage, "instrument_lot", "metadata_entries", lot, chooseReason(lot, "INVALID_NUMERIC"))
		addFieldCoverage(s.Coverage, "instrument_contract_value", "metadata_entries", ctVal, chooseReason(ctVal, "INVALID_NUMERIC"))
		addFieldCoverage(s.Coverage, "instrument_contract_type", "metadata_entries", ctType, chooseReason(ctType, "MISSING_CT_TYPE_DATA"))
	}
	return nil
}

func chooseReason(known bool, missing string) string {
	if known {
		return ""
	}
	return missing
}

func number(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok
}

func projectFirewallJSON(raw []byte, source string, s *projectionStats) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if source == "playbook_bindings" {
		var envelope struct {
			Playbooks []struct {
				PlaybookHash        string `json:"playbook_hash"`
				ExperimentHash      string `json:"experiment_hash"`
				ProfileID           string `json:"profile_id"`
				EventFamily         string `json:"event_family"`
				Horizon             string `json:"horizon"`
				SourceWinnerIDsHash string `json:"source_winner_ids_hash"`
				DelayBars           int    `json:"delay_bars"`
			} `json:"playbooks"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		for _, p := range envelope.Playbooks {
			s.WinnerBindings = append(s.WinnerBindings, strings.Join([]string{p.PlaybookHash, p.SourceWinnerIDsHash, p.ProfileID, p.EventFamily, p.Horizon, fmt.Sprint(p.DelayBars)}, "\x00"))
		}
		sort.Strings(s.WinnerBindings)
		return nil
	}
	var envelope struct {
		ProfileID string `json:"profile_id"`
		Cells     map[string]struct {
			Key struct {
				EventFamily string `json:"event_family"`
				Horizon     string `json:"horizon"`
				Direction   string `json:"direction"`
				DelayBars   int    `json:"delay_bars"`
			} `json:"key"`
			Stats json.RawMessage `json:"stats"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	for _, cell := range envelope.Cells {
		s.SupportCells = append(s.SupportCells, strings.Join([]string{envelope.ProfileID, cell.Key.EventFamily, cell.Key.Horizon, cell.Key.Direction, fmt.Sprint(cell.Key.DelayBars)}, "\x00"))
		if len(cell.Stats) > 0 {
			s.FirewallRejectedFields++
		}
	}
	sort.Strings(s.SupportCells)
	return nil
}

func buildArtifacts(m SourceManifest, s projectionStats) (map[string]any, map[string]any) {
	required := []string{"fee_bps", "entry_slip_bps", "exit_slip_bps", "maker_fill_rate", "latency_ms", "instrument_lot", "instrument_contract_value", "instrument_contract_type", "dynamic_min_notional_usdt"}
	unknown := map[string]any{}
	completeness := map[string]any{}
	fitnessRows := make([]map[string]any, 0, len(required))
	observabilityOK := true
	for _, field := range required {
		coverage := combinedFieldCoverage(s.Coverage, field)
		known := coverage.Numerator
		missing := coverage.Denominator - known
		status := "MISSING"
		availability := "UNKNOWN"
		proxy := ""
		if known > 0 {
			status, availability = "DIRECT", "KNOWN_AFTER_DECISION"
		}
		if field == "latency_ms" && known == 0 && coverage.Reasons["WEAK_PROXY_ONLY"] > 0 {
			status, proxy = "WEAK_PROXY", "eval_rest_ms cannot establish signal-to-ack-fill latency"
		}
		if field == "dynamic_min_notional_usdt" {
			availability = "PIT_UNPROVEN"
			if known > 0 {
				status = "RECONSTRUCTABLE"
			}
		}
		if known < 3 {
			observabilityOK = false
		}
		unknown[field] = missing
		completeness[field] = map[string]any{"numerator": known, "denominator": coverage.Denominator, "missing_reason_counts": coverage.Reasons, "scopes": coverageScopes(s.Coverage, field)}
		evidencePayload := map[string]any{"field": field, "coverage": completeness[field], "status": status}
		evidenceHash, _, _ := datatruth.HashJSON(evidencePayload)
		fitnessRows = append(fitnessRows, map[string]any{
			"route_id": "g10_v1", "required_field": field, "semantic_definition": field,
			"required_for_layer": "observability", "source_ids": sourceIDsForField(field),
			"fitness_status": status, "decision_time_availability": availability,
			"reconstruction_rule": reconstructionRule(field), "weak_proxy_limit": proxy,
			"coverage_numerator": known, "coverage_denominator": coverage.Denominator,
			"missing_reason_counts": coverage.Reasons,
			"false_negative_risk":   "missing execution evidence can hide executable personal edge",
			"leakage_risk":          leakageRisk(field), "cross_source_duplicate_risk": s.UnresolvedCrossSourceIdentities > 0, "evidence_hash": evidenceHash,
		})
	}
	className, blocker, reason := "UNIDENTIFIABLE", "identifiability", "allowed frozen schemas expose aggregate cells and opaque winner hashes, not event identities required for controls"
	if !observabilityOK {
		className, blocker, reason = "DATA_BLIND", "observability", "one or more critical-known metrics have fewer than 3 semantically valid samples"
	}
	classification := map[string]any{"classification": className, "earliest_blocker": blocker, "phase": "A", "reason": reason}
	if m.ScopeStatus == "TRUNCATED_SMOKE" {
		blocker = "engineering_scope"
		classification = map[string]any{"classification": nil, "engineering_status": "NON_FORMAL_SMOKE", "phase": "A", "reason": "truncated prefix smoke cannot produce a formal seven-class verdict"}
	}
	winnerHash := hashStrings(s.WinnerBindings...)
	supportCellHash := hashStrings(s.SupportCells...)
	artifacts := map[string]any{
		"data_fitness_matrix.json":     map[string]any{"schema_version": Schema, "fitness_status_enum": []string{"DIRECT", "RECONSTRUCTABLE", "WEAK_PROXY", "MISSING"}, "rows": fitnessRows},
		"field_completeness.json":      map[string]any{"schema_version": Schema, "source_rows": s.SourceRows, "projected_rows": s.Rows, "projected_total_before_dedup": s.ProjectedTotal, "fields": completeness, "presence_bits_required": true},
		"critical_reasons.json":        map[string]any{"schema_version": Schema, "fields": completeness, "per_metric_min_samples": 3},
		"support_counts.json":          map[string]any{"schema_version": Schema, "events": nil, "independent_days": nil, "shock_blocks": nil, "outcome_blind_cell_count": len(s.SupportCells), "outcome_blind_cell_set_hash": supportCellHash, "status": "UNAVAILABLE_SOURCE_SCHEMA", "thresholds": map[string]int{"events": 50, "independent_days": 20, "shock_blocks": 10}},
		"winner_control_bindings.json": map[string]any{"schema_version": Schema, "outcome_blind": true, "winner_seed_set_hash": winnerHash, "winner_binding_kind": "PLAYBOOK_HASH_PLUS_OPAQUE_SOURCE_WINNER_IDS_HASH", "matched_control_set_hash": nil, "baseline_support_set_hash": nil, "matching_config_hash": hashStrings("outcome_blind_matching_v1"), "status": "UNAVAILABLE_SOURCE_SCHEMA", "labels_unblinded": false},
		"falsification_chain.json":     map[string]any{"schema_version": Schema, "layers": []string{"observability", "identifiability", "ex_ante_predictability", "personal_executability", "independent_support", "net_edge"}, "stopped_at": blocker},
		"classification_evidence.json": map[string]any{"schema_version": Schema, "classification_by_route": map[string]any{"g10_v1": classification}},
		"conflict_matrix.json":         map[string]any{"schema_version": Schema, "exact_duplicates": s.Duplicates, "conflicts": s.Conflicts, "cross_source_conflicts": s.CrossSourceConflicts, "suppressed_lower_priority_facts": s.SuppressedLowerPriorityFacts, "unresolved_cross_source_identities": s.UnresolvedCrossSourceIdentities, "provenance_counts": s.ProvenanceCounts, "unresolved_reason": "CROSS_SOURCE_IDENTITY_UNAVAILABLE", "malformed_quarantined": s.Malformed, "firewall_rejected_outcome_sections": s.FirewallRejectedFields, "severity": "CRITICAL_UNKNOWN"},
		"decision_journal.json":        map[string]any{"schema_version": Schema, "entries": []map[string]any{{"sequence": 1, "recorded_at": m.CapturedAt, "owner": "routerg10/evidenceaudit", "observation": "validation maps contain aggregate outcome stats but no event identities", "hypothesis_before": "allowed maps expose outcome-blind event rows", "hypothesis_after": "event-level matching is impossible from allowed schemas", "decision": "STOP_AT_EARLIEST_BLOCKER", "rejected_alternatives": []string{"treat aggregate event_outcome_count as events", "invent seed identities"}, "unresolved_uncertainty": "event-level support and matching", "budget_spent": "fixture-only", "next_stop_condition": "new preregistration with legal event identity source"}}},
	}
	hashes := map[string]string{}
	for n, v := range artifacts {
		h := addArtifactHash(v)
		hashes[n] = h
	}
	systemStatus := "NO_GO"
	if m.ScopeStatus == "TRUNCATED_SMOKE" {
		systemStatus = "TRUNCATED_SMOKE"
	}
	summary := map[string]any{"schema_version": Schema, "generation": m.Generation, "protocol_hash": ProtocolHash, "source_manifest_hash": m.ManifestHash, "scope_status": m.ScopeStatus, "not_formal_verdict": m.ScopeStatus == "TRUNCATED_SMOKE", "g10_bindings": "frozen_by_protocol", "field_completeness": completeness, "critical_reason_counts": completeness, "support_counts_by_playbook_symbol_day_direction": nil, "data_blind_denominator": s.Rows, "observed_failure_denominator": 0, "falsification_chain_by_route": map[string]any{"g10_v1": blocker}, "classification_by_route": map[string]any{"g10_v1": classification}, "conflict_matrix_hash": hashes["conflict_matrix.json"], "decision_journal_hash": hashes["decision_journal.json"], "bounded_memory": map[string]any{"projected_total": s.ProjectedTotal, "peak_buffered_rows": s.PeakBufferedRows, "peak_buffered_bytes": s.PeakBufferedBytes, "checkpoint_hashed_bytes": s.CheckpointHashedBytes, "staging_binding_hash": s.StagingBindingHash, "staging_files_hash": s.StagingFilesHash}, "output_hashes": hashes, "exam_actions": 0, "trade_allowed": false, "system_status": systemStatus}
	summaryHash := addArtifactHash(summary)
	artifacts["audit_summary.json"] = summary
	hashes["audit_summary.json"] = summaryHash
	receipt := map[string]any{"schema_version": Schema, "protocol_hash": ProtocolHash, "source_manifest_hash": m.ManifestHash, "scope_status": m.ScopeStatus, "not_formal_verdict": m.ScopeStatus == "TRUNCATED_SMOKE", "artifact_hashes": hashes, "complete": true}
	addArtifactHash(receipt)
	artifacts["audit_receipt.json"] = receipt
	return artifacts, summary
}

func combinedFieldCoverage(all map[string]FieldCoverage, field string) FieldCoverage {
	out := FieldCoverage{Reasons: map[string]int64{}}
	for key, coverage := range all {
		if !strings.HasPrefix(key, field+"|") {
			continue
		}
		out.Numerator += coverage.Numerator
		out.Denominator += coverage.Denominator
		for reason, count := range coverage.Reasons {
			out.Reasons[reason] += count
		}
	}
	return out
}

func coverageScopes(all map[string]FieldCoverage, field string) map[string]FieldCoverage {
	out := map[string]FieldCoverage{}
	for key, coverage := range all {
		if !strings.HasPrefix(key, field+"|") {
			continue
		}
		out[strings.TrimPrefix(key, field+"|")] = coverage
	}
	return out
}

func addArtifactHash(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		h, _, _ := datatruth.HashJSON(v)
		return h
	}
	m["artifact_hash"] = nil
	h, _, _ := datatruth.HashJSON(m)
	m["artifact_hash"] = h
	return h
}
func finalizeArtifactChain(artifacts map[string]any, summary map[string]any, manifest SourceManifest) {
	hashes := map[string]string{}
	for name, value := range artifacts {
		if name == "audit_summary.json" || name == "audit_receipt.json" {
			continue
		}
		if cp, ok := value.(Checkpoint); ok && cp.CheckpointHash != nil {
			hashes[name] = *cp.CheckpointHash
			continue
		}
		hashes[name] = addArtifactHash(value)
	}
	summary["output_hashes"] = cloneHashes(hashes)
	delete(summary, "artifact_hash")
	hashes["audit_summary.json"] = addArtifactHash(summary)
	receipt := map[string]any{
		"schema_version": Schema, "protocol_hash": ProtocolHash,
		"source_manifest_hash": manifest.ManifestHash, "artifact_hashes": cloneHashes(hashes),
		"scope_status": manifest.ScopeStatus, "not_formal_verdict": manifest.ScopeStatus == "TRUNCATED_SMOKE", "complete": true,
	}
	addArtifactHash(receipt)
	artifacts["audit_receipt.json"] = receipt
}
func cloneHashes(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func sourceIDsForField(field string) []string {
	switch field {
	case "fee_bps":
		return []string{"exchange_fills", "settlements", "fast_v2_settlements"}
	case "entry_slip_bps", "exit_slip_bps":
		return []string{"settlements", "fast_v2_settlements"}
	case "maker_fill_rate":
		return []string{"maker_attempts"}
	case "latency_ms":
		return []string{"maker_attempts", "executable_shadow"}
	default:
		return []string{"instrument_metadata"}
	}
}
func reconstructionRule(field string) string {
	if field == "fee_bps" {
		return "abs(fee_usdt)/notional_usdt*10000 when currency semantics and notional are valid"
	}
	if field == "dynamic_min_notional_usdt" {
		return "min_sz*ctVal*price only for linear contracts with PIT-valid metadata"
	}
	return "none; direct presence required"
}
func leakageRisk(field string) string {
	if strings.Contains(field, "instrument") || field == "dynamic_min_notional_usdt" {
		return "current mutable snapshot may be PIT_UNPROVEN"
	}
	return "KNOWN_AFTER_DECISION is execution evidence only, never a prediction feature"
}

func loadResult(out string) (Result, error) {
	raw, err := os.ReadFile(filepath.Join(out, "audit_summary.json"))
	if err != nil {
		return Result{}, err
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		return Result{}, err
	}
	return Result{Summary: s}, nil
}
func loadCheckpoint(path string) (Checkpoint, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, false, err
	}
	want := c
	want.CheckpointHash = nil
	h, _, _ := datatruth.HashJSON(want)
	if c.CheckpointHash == nil || *c.CheckpointHash != h {
		return c, false, errors.New("checkpoint hash drift")
	}
	return c, true, nil
}
func saveCheckpoint(path string, c Checkpoint) error {
	c.CheckpointHash = nil
	h, _, err := datatruth.HashJSON(c)
	if err != nil {
		return err
	}
	c.CheckpointHash = &h
	return writeJSONAtomic(path, c)
}
func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, append(raw, '\n'))
}
func publishArtifacts(outputDir string, artifacts map[string]any) error {
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		if name != "audit_receipt.json" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	names = append(names, "audit_receipt.json")
	staged := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(outputDir, "."+name+".staging")
		if err := writeJSONAtomic(path, artifacts[name]); err != nil {
			for _, p := range staged {
				_ = os.Remove(p)
			}
			return err
		}
		staged = append(staged, path)
	}
	for i, name := range names {
		if err := os.Rename(staged[i], filepath.Join(outputDir, name)); err != nil {
			return err
		}
	}
	return nil
}
func writeBytesAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func copyNAtomic(path string, src io.Reader, n int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = io.CopyN(tmp, src, n); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func min64(a, b int64) int {
	if a < b {
		return int(a)
	}
	return int(b)
}
func statID(info os.FileInfo) (int64, int64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Dev), int64(st.Ino)
	}
	return 0, 0
}
func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func hashStrings(s ...string) string {
	h := sha256.New()
	for _, x := range s {
		_, _ = h.Write([]byte(x))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func token(salt, value string) string {
	h := hmac.New(sha256.New, []byte(salt))
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func resolveAuditSalt(cfg *Config, resume bool) error {
	if cfg.AuditSalt != "" {
		return nil
	}
	dir := filepath.Join(cfg.OutputDir, ".evidenceaudit-staging")
	path := filepath.Join(dir, "audit_salt.key")
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) != 32 {
			return errors.New("invalid staged audit salt")
		}
		cfg.AuditSalt = hex.EncodeToString(raw)
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if resume {
		return errors.New("checkpoint exists but staged audit salt is missing")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw = make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	cfg.AuditSalt = hex.EncodeToString(raw)
	return nil
}
func dedupKey(source string, raw map[string]any) string {
	for _, k := range []string{"tradeId", "billId", "position_id", "fast_event_id"} {
		if v, ok := raw[k]; ok && fmt.Sprint(v) != "" {
			return source + ":" + k + ":" + fmt.Sprint(v)
		}
	}
	b, _ := json.Marshal(raw)
	return source + ":fallback:" + sha(b)
}
