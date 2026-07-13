package announcements

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunResult holds sealed announcement outputs.
type RunResult struct {
	Tape     AnnouncementTape
	Chain    EventChain
	Manifest Manifest
}

// Runner executes bounded archive ingestion with optional checkpoint resume.
type Runner struct {
	Config         RunConfig
	CheckpointPath string
	OnProgress     func(source string, lines int)
}

type sourceSpec struct {
	key   string
	path  string
	venue Venue
}

func (r *Runner) sources() []sourceSpec {
	s := r.Config.Sources
	root := r.Config.ArchiveRoot
	return []sourceSpec{
		{key: "okx", path: firstNonEmpty(s.OKX, filepath.Join(root, "okx_announcements.jsonl")), venue: VenueOKX},
		{key: "binance", path: firstNonEmpty(s.Binance, filepath.Join(root, "binance_announcements.jsonl")), venue: VenueBinance},
		{key: "bybit", path: firstNonEmpty(s.Bybit, filepath.Join(root, "bybit_announcements.jsonl")), venue: VenueBybit},
		{key: "gate", path: firstNonEmpty(s.Gate, filepath.Join(root, "gate_announcements.jsonl")), venue: VenueGate},
	}
}

// Run ingests historical archives and writes tape, chain, and manifest.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	cfg := r.Config
	if cfg.SchemaVersion == "" {
		cfg.SchemaVersion = ConfigSchema
	}
	if cfg.Generation == "" {
		cfg.Generation = Generation
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.MaxLines <= 0 {
		return RunResult{}, errors.New("announcements: max_lines must be positive")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}
	configHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return RunResult{}, err
	}

	store := NewStore()
	offsets := map[string]int{}
	sourceStatus := map[string]SourceStatus{}

	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, cfg); err != nil {
			return RunResult{}, err
		}
		store = RestoreStore(cp.Accumulator)
		offsets = cp.SourceOffsets
		sourceStatus = cp.SourceStatus
	}

	for _, spec := range r.sources() {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		default:
		}
		if !fileExists(spec.path) {
			sourceStatus[spec.key] = MissingSourceStatus(spec.path)
			continue
		}
		stats, err := ingestArchiveFile(spec.path, cfg.MaxLines, offsets[spec.key], cfg.PageSize, spec.venue, store)
		if err != nil {
			sourceStatus[spec.key] = SourceStatus{
				Path: spec.path, Status: StatusIncomplete, FetchFailed: true, Reason: ReasonFetchFailed,
			}
			continue
		}
		offsets[spec.key] += stats.Decoded + stats.Malformed
		st := BuildSourceStatus(spec.path, stats)
		sourceStatus[spec.key] = st
		if r.OnProgress != nil {
			r.OnProgress(spec.key, offsets[spec.key])
		}
	}

	incomplete := ShouldFailClosed(sourceStatus)
	incompleteReasons := CollectIncompleteReasons(sourceStatus)
	items := store.Items()
	tapeHash, err := HashTape(items)
	if err != nil {
		return RunResult{}, err
	}
	tape := AnnouncementTape{
		SchemaVersion:  SchemaVersion,
		Generation:     cfg.Generation,
		ExecutionVenue: ExecutionVenue,
		ConfigHash:     configHash,
		TapeHash:       tapeHash,
		BuiltAt:        time.Now().UTC(),
		Incomplete:        incomplete,
		IncompleteReasons: incompleteReasons,
		SourceStatus:      sourceStatus,
		Coverage:       store.Coverage(),
		Announcements:  items,
	}
	chain, err := BuildEventChain(items, cfg.Generation)
	if err != nil {
		return RunResult{}, err
	}
	if err := ValidateEventChain(chain); err != nil {
		return RunResult{}, err
	}
	manifest, err := BuildManifest(cfg, tape, chain)
	if err != nil {
		return RunResult{}, err
	}
	if err := WriteOutputs(cfg.OutputDir, tape, chain, manifest); err != nil {
		return RunResult{}, err
	}

	cp := NewCheckpoint(cfg, configHash)
	cp.SourceOffsets = offsets
	cp.SourceStatus = sourceStatus
	cp.Accumulator = items
	cp.DedupeSkipped = store.DedupeSkipped()
	cp.Done = true
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}

	return RunResult{Tape: tape, Chain: chain, Manifest: manifest}, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// DefaultSourcePaths resolves archive paths under archiveRoot.
func DefaultSourcePaths(archiveRoot string) SourcePaths {
	return SourcePaths{
		OKX:     filepath.Join(archiveRoot, "okx_announcements.jsonl"),
		Binance: filepath.Join(archiveRoot, "binance_announcements.jsonl"),
		Bybit:   filepath.Join(archiveRoot, "bybit_announcements.jsonl"),
		Gate:    filepath.Join(archiveRoot, "gate_announcements.jsonl"),
	}
}

// ValidateRunConfig checks required bounded knobs.
func ValidateRunConfig(cfg RunConfig) error {
	if cfg.ArchiveRoot == "" && cfg.Sources.OKX == "" && cfg.Sources.Binance == "" &&
		cfg.Sources.Bybit == "" && cfg.Sources.Gate == "" {
		return fmt.Errorf("announcements: archive root or explicit sources required")
	}
	if cfg.OutputDir == "" {
		return errors.New("announcements: output_dir required")
	}
	if cfg.MaxLines <= 0 {
		return errors.New("announcements: max_lines must be positive")
	}
	return nil
}
