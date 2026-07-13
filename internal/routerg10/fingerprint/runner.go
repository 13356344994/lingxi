package fingerprint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

// RunResult summarizes one fingerprint build.
type RunResult struct {
	Fingerprint  PersonalExecutionFingerprint
	Coverage     CoverageReport
	OutputPath   string
	CoveragePath string
	Checkpointed bool
}

// Runner executes bounded streaming aggregation with optional resume.
type Runner struct {
	Config         RunConfig
	CheckpointPath string
	OnProgress     func(source string, lines int)
}

func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	cfg := r.Config
	if cfg.SchemaVersion == "" {
		cfg.SchemaVersion = ConfigSchema
	}
	if cfg.Generation == "" {
		cfg.Generation = Generation
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}
	configHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return RunResult{}, err
	}

	store := newStore()
	sourceStatus := map[string]SourceStatus{}
	offsets := map[string]int{}
	equity := EquitySummary{Status: StatusUnknown}

	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, cfg); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			return loadCompletedResult(cfg.OutputDir)
		}
		store = restoreStore(cp.Accumulator)
		sourceStatus = cp.SourceStatus
		offsets = cp.SourceOffsets
		equity = cp.Equity
	}

	defaultVenue := defaultVenueFromRoot(cfg.RecordRoot)
	ingest := ingestContext{store: store, defaultVenue: defaultVenue}

	if path := cfg.Sources.InstrumentMeta; fileExists(path) {
		cat, err := loadInstrumentCatalog(path)
		if err != nil {
			sourceStatus[sourceInstrumentMeta] = SourceStatus{Path: path, Status: StatusUnknown}
		} else {
			ingest.catalog = cat
			ingest.hasCatalog = true
			sourceStatus[sourceInstrumentMeta] = SourceStatus{Path: path, Status: StatusAvailable}
		}
	} else {
		sourceStatus[sourceInstrumentMeta] = unknownStatus(cfg.Sources.InstrumentMeta)
	}

	if err := r.ingestSource(ctx, sourceSettlements, cfg.Sources.Settlements, cfg.MaxLines, offsets, ingest, func(path string, max, skip int, ctx ingestContext) (streamStats, error) {
		return ingestSettlements(path, max, skip, ctx)
	}, sourceStatus); err != nil {
		return RunResult{}, err
	}
	if err := r.ingestSource(ctx, sourceMakerAttempts, cfg.Sources.MakerAttempts, cfg.MaxLines, offsets, ingest, ingestMakerAttempts, sourceStatus); err != nil {
		return RunResult{}, err
	}
	if err := r.ingestSource(ctx, sourceOpenDecisions, cfg.Sources.OpenDecisions, cfg.MaxLines, offsets, ingest, ingestOpenDecisions, sourceStatus); err != nil {
		return RunResult{}, err
	}
	if err := r.ingestSource(ctx, sourceExecutableShadow, cfg.Sources.ExecutableShadow, cfg.MaxLines, offsets, ingest, ingestExecutableShadow, sourceStatus); err != nil {
		return RunResult{}, err
	}

	if path := cfg.Sources.EquitySnapshots; fileExists(path) {
		eq, stats, err := ingestEquitySnapshots(path, cfg.MaxLines, offsets[sourceEquitySnapshots])
		if err != nil {
			sourceStatus[sourceEquitySnapshots] = SourceStatus{Path: path, Status: StatusUnknown}
		} else {
			equity = eq
			sourceStatus[sourceEquitySnapshots] = buildSourceStatus(path, stats, eq.Status)
			offsets[sourceEquitySnapshots] += stats.Decoded
			if r.OnProgress != nil {
				r.OnProgress(sourceEquitySnapshots, offsets[sourceEquitySnapshots])
			}
		}
	} else {
		sourceStatus[sourceEquitySnapshots] = unknownStatus(cfg.Sources.EquitySnapshots)
	}

	fp := PersonalExecutionFingerprint{
		SchemaVersion: SchemaVersion,
		Generation:    cfg.Generation,
		BuiltAt:       time.Now().UTC(),
		ConfigHash:    configHash,
		SourceStatus:  sourceStatus,
		Equity:        equity,
		Cells:         store.sortedCells(),
	}
	hash, err := hashFingerprint(fp)
	if err != nil {
		return RunResult{}, err
	}
	fp.FingerprintHash = hash
	coverage, err := BuildCoverageReport(fp)
	if err != nil {
		return RunResult{}, err
	}

	outPath := filepath.Join(cfg.OutputDir, OutputFileName)
	if err := writeFingerprint(outPath, fp); err != nil {
		return RunResult{}, err
	}
	coveragePath := filepath.Join(cfg.OutputDir, CoverageFileName)
	if err := writeCoverageReport(coveragePath, coverage); err != nil {
		return RunResult{}, err
	}

	cp := Checkpoint{
		Generation:    cfg.Generation,
		ConfigHash:    configHash,
		OutputDir:     cfg.OutputDir,
		SourceOffsets: offsets,
		SourceStatus:  sourceStatus,
		Equity:        equity,
		Accumulator:   snapshotStore(store),
		Done:          true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}

	return RunResult{
		Fingerprint:  fp,
		Coverage:     coverage,
		OutputPath:   outPath,
		CoveragePath: coveragePath,
		Checkpointed: true,
	}, nil
}

type ingestFn func(path string, maxLines, skipLines int, ctx ingestContext) (streamStats, error)

func (r *Runner) ingestSource(
	ctx context.Context,
	key, path string,
	maxLines int,
	offsets map[string]int,
	ingest ingestContext,
	fn ingestFn,
	sourceStatus map[string]SourceStatus,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !fileExists(path) {
		sourceStatus[key] = unknownStatus(path)
		return nil
	}
	stats, err := fn(path, maxLines, offsets[key], ingest)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	sourceStatus[key] = buildSourceStatus(path, stats, StatusAvailable)
	offsets[key] += stats.Decoded
	if r.OnProgress != nil {
		r.OnProgress(key, offsets[key])
	}
	return nil
}

func buildSourceStatus(path string, stats streamStats, baseStatus string) SourceStatus {
	status := SourceStatus{
		Path:      path,
		Status:    baseStatus,
		Lines:     stats.Lines,
		Decoded:   stats.Decoded,
		Skipped:   stats.Skipped,
		Malformed: stats.Malformed,
		Truncated: stats.Truncated,
	}
	if stats.Truncated {
		status.Status = StatusTruncated
	}
	if baseStatus == StatusUnknown {
		status.Status = StatusUnknown
	}
	return status
}

func hashFingerprint(fp PersonalExecutionFingerprint) (string, error) {
	payload := fp
	payload.FingerprintHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func writeFingerprint(path string, fp PersonalExecutionFingerprint) error {
	return writeRedactedJSON(path, fp)
}

func writeCoverageReport(path string, report CoverageReport) error {
	return writeRedactedJSON(path, report)
}

func writeRedactedJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := AssertNoForbiddenFields(raw); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadCompletedResult(outputDir string) (RunResult, error) {
	outPath := filepath.Join(outputDir, OutputFileName)
	coveragePath := filepath.Join(outputDir, CoverageFileName)
	var fp PersonalExecutionFingerprint
	if err := readJSONArtifact(outPath, &fp); err != nil {
		return RunResult{}, err
	}
	if err := ValidateFingerprintSchema(fp); err != nil {
		return RunResult{}, err
	}
	var coverage CoverageReport
	if err := readJSONArtifact(coveragePath, &coverage); err != nil {
		return RunResult{}, err
	}
	if err := ValidateCoverageReport(coverage, fp.FingerprintHash); err != nil {
		return RunResult{}, err
	}
	return RunResult{
		Fingerprint:  fp,
		Coverage:     coverage,
		OutputPath:   outPath,
		CoveragePath: coveragePath,
		Checkpointed: true,
	}, nil
}

func readJSONArtifact(path string, dst any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	return nil
}

// ValidateFingerprintSchema checks required envelope fields.
func ValidateFingerprintSchema(fp PersonalExecutionFingerprint) error {
	if fp.SchemaVersion != SchemaVersion {
		return fmt.Errorf("fingerprint: schema_version=%q", fp.SchemaVersion)
	}
	if fp.Generation != Generation {
		return fmt.Errorf("fingerprint: generation=%q", fp.Generation)
	}
	if fp.FingerprintHash == "" {
		return errors.New("fingerprint: missing fingerprint_hash")
	}
	want, err := hashFingerprint(fp)
	if err != nil {
		return err
	}
	if fp.FingerprintHash != want {
		return errors.New("fingerprint: fingerprint hash mismatch (tamper/drift)")
	}
	return nil
}
