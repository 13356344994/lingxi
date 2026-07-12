package contextdata

import (
	"bufio"
	"bytes"
	"context"
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
	"time"

	"lingxi/internal/datatruth"
)

const (
	checkpointVersion   = 2
	defaultDedupeWindow = 4096
	identityPrefixBytes = 64 * 1024
)

type Config struct {
	Kind             Kind
	InputPath        string
	OutputDir        string
	Date             string
	BaseCatalogPath  string
	UniverseID       string
	CheckpointPath   string
	MaxLines         int64
	ProgressEvery    int64
	LateAfter        time.Duration
	MaxDedupeEntries int
	Now              func() time.Time
	OnProgress       func(Progress)
}

type Progress struct {
	Stage        string    `json:"stage"`
	Kind         Kind      `json:"kind"`
	LinesScanned int64     `json:"lines_scanned"`
	RowsEmitted  int64     `json:"rows_emitted"`
	ByteOffset   int64     `json:"byte_offset"`
	BytesHashed  int64     `json:"bytes_hashed,omitempty"`
	EventTime    time.Time `json:"event_time,omitempty"`
	Completed    bool      `json:"completed"`
}

type checkpoint struct {
	Version        int                   `json:"version"`
	Input          InputIdentity         `json:"input"`
	ConfigHash     string                `json:"config_hash"`
	ByteOffset     int64                 `json:"byte_offset"`
	LinesScanned   int64                 `json:"lines_scanned"`
	RowsEmitted    int64                 `json:"rows_emitted"`
	OutputBytes    int64                 `json:"output_bytes"`
	ConsumedChain  string                `json:"consumed_chain"`
	StreamState    datatruth.StreamState `json:"stream_state"`
	Quality        QualityCounts         `json:"quality"`
	Clocks         ClockRange            `json:"clocks"`
	LastEventTimes map[string]time.Time  `json:"last_event_times,omitempty"`
	FinalizeReady  bool                  `json:"finalize_ready"`
	Completed      bool                  `json:"completed"`
	ManifestPath   string                `json:"manifest_path,omitempty"`
	SavedAt        time.Time             `json:"saved_at"`
}

type Result struct {
	Manifest   PartitionManifest `json:"manifest"`
	ReachedEOF bool              `json:"reached_eof"`
	Checkpoint string            `json:"checkpoint"`
}

func Materialize(ctx context.Context, cfg Config) (Result, error) {
	if err := normalizeConfig(&cfg); err != nil {
		return Result{}, err
	}
	if cfg.Kind == KindInstrument || cfg.Kind == KindUniverse {
		return materializeSnapshot(cfg)
	}
	return materializeStream(ctx, cfg)
}

func normalizeConfig(cfg *Config) error {
	if _, err := ParseKind(string(cfg.Kind)); err != nil {
		return err
	}
	if cfg.InputPath == "" || cfg.OutputDir == "" {
		return errors.New("contextdata: input and output-dir are required")
	}
	var err error
	if cfg.InputPath, err = filepath.Abs(cfg.InputPath); err != nil {
		return err
	}
	if cfg.OutputDir, err = filepath.Abs(cfg.OutputDir); err != nil {
		return err
	}
	if cfg.BaseCatalogPath != "" {
		if cfg.BaseCatalogPath, err = filepath.Abs(cfg.BaseCatalogPath); err != nil {
			return err
		}
	}
	if cfg.CheckpointPath != "" {
		if cfg.CheckpointPath, err = filepath.Abs(cfg.CheckpointPath); err != nil {
			return err
		}
	}
	if _, err := time.Parse("2006-01-02", cfg.Date); err != nil {
		return fmt.Errorf("contextdata: invalid UTC date %q: %w", cfg.Date, err)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxDedupeEntries <= 0 {
		cfg.MaxDedupeEntries = defaultDedupeWindow
	}
	if cfg.ProgressEvery <= 0 {
		cfg.ProgressEvery = 100000
	}
	if cfg.Kind != KindInstrument && cfg.Kind != KindUniverse {
		finalPath := filepath.Join(cfg.OutputDir, cfg.Date, string(cfg.Kind)+".jsonl")
		if filepath.Clean(finalPath) == filepath.Clean(cfg.InputPath) {
			return errors.New("contextdata: output partition must not overwrite input")
		}
	}
	return nil
}

func materializeStream(ctx context.Context, cfg Config) (Result, error) {
	dayDir := filepath.Join(cfg.OutputDir, cfg.Date)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return Result{}, err
	}
	finalPath := filepath.Join(dayDir, string(cfg.Kind)+".jsonl")
	partialPath := finalPath + ".partial"
	manifestPath := filepath.Join(dayDir, string(cfg.Kind)+".manifest.json")
	checkpointPath := cfg.CheckpointPath
	if checkpointPath == "" {
		checkpointPath = filepath.Join(dayDir, string(cfg.Kind)+".checkpoint.json")
	}
	configHash, err := configFingerprint(cfg)
	if err != nil {
		return Result{}, err
	}
	currentIdentity, err := computeInputIdentity(cfg.InputPath, 0)
	if err != nil {
		return Result{}, err
	}

	cp, haveCheckpoint, err := loadCheckpoint(checkpointPath)
	if err != nil {
		return Result{}, err
	}
	if haveCheckpoint {
		if err := verifyResume(cp, currentIdentity, configHash, cfg); err != nil {
			return Result{}, err
		}
		if cp.Completed {
			manifest, err := loadManifest(cp.ManifestPath)
			if err != nil {
				return Result{}, fmt.Errorf("contextdata: completed checkpoint has invalid manifest: %w", err)
			}
			return Result{Manifest: manifest, ReachedEOF: true, Checkpoint: checkpointPath}, nil
		}
		if recovered, manifest, err := recoverRenamedFinal(cfg, &cp, finalPath, partialPath, manifestPath, checkpointPath); err != nil {
			return Result{}, err
		} else if recovered {
			return Result{Manifest: manifest, ReachedEOF: true, Checkpoint: checkpointPath}, nil
		}
	} else {
		if _, err := os.Stat(finalPath); err == nil {
			return Result{}, fmt.Errorf("contextdata: refusing to overwrite existing final partition %s without checkpoint", finalPath)
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
		cp = checkpoint{
			Version:     checkpointVersion,
			Input:       currentIdentity,
			ConfigHash:  configHash,
			StreamState: datatruth.StreamState{},
		}
	}

	input, err := os.Open(cfg.InputPath)
	if err != nil {
		return Result{}, err
	}
	defer input.Close()
	if _, err := input.Seek(cp.ByteOffset, io.SeekStart); err != nil {
		return Result{}, err
	}
	if cp.OutputBytes > 0 {
		info, err := os.Stat(partialPath)
		if err != nil {
			return Result{}, fmt.Errorf("contextdata: partial output missing for checkpoint with %d confirmed bytes: %w", cp.OutputBytes, err)
		}
		if info.Size() < cp.OutputBytes {
			return Result{}, fmt.Errorf("contextdata: partial output truncated: size=%d checkpoint=%d", info.Size(), cp.OutputBytes)
		}
	}
	output, err := os.OpenFile(partialPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer output.Close()
	if err := output.Truncate(cp.OutputBytes); err != nil {
		return Result{}, err
	}
	if _, err := output.Seek(cp.OutputBytes, io.SeekStart); err != nil {
		return Result{}, err
	}
	writer := bufio.NewWriterSize(output, 64*1024)
	reader := bufio.NewReaderSize(input, 256*1024)
	dayStart, _ := time.Parse("2006-01-02", cfg.Date)
	dayStart = dayStart.UTC()
	dayEnd := dayStart.Add(24 * time.Hour)
	var runLines int64
	reachedEOF := false

	save := func() error {
		if err := writer.Flush(); err != nil {
			return err
		}
		if err := output.Sync(); err != nil {
			return err
		}
		identity, err := computeInputIdentity(cfg.InputPath, 0)
		if err != nil {
			return err
		}
		cp.Input = identity
		cp.StreamState = trimState(cp.StreamState, cfg.MaxDedupeEntries)
		cp.SavedAt = cfg.Now().UTC()
		return saveJSONAtomic(checkpointPath, cp)
	}

	for {
		select {
		case <-ctx.Done():
			if err := save(); err != nil {
				return Result{}, err
			}
			return Result{Manifest: incompleteManifest(cfg, cp), Checkpoint: checkpointPath}, ctx.Err()
		default:
		}

		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr == io.EOF {
			reachedEOF = true
			break
		}
		if readErr != nil && readErr != io.EOF {
			return Result{}, readErr
		}
		if readErr == io.EOF && !bytes.HasSuffix(line, []byte{'\n'}) {
			// A live append-only file may expose its final JSON object mid-write. Do not consume
			// or checkpoint a partial line; resume from the same byte offset once it is complete.
			break
		}
		cp.ConsumedChain = extendChain(cp.ConsumedChain, line)
		cp.ByteOffset += int64(len(line))
		cp.LinesScanned++
		runLines++
		raw := bytes.TrimSpace(line)
		if len(raw) > 0 {
			record, missing, err := DecodeLegacy(cfg.Kind, raw, cp.LinesScanned)
			if err != nil {
				return Result{}, fmt.Errorf("contextdata: %s line %d: %w", cfg.Kind, cp.LinesScanned, err)
			}
			assessment := observeBounded(&cp.StreamState, record.Event, datatruth.ObserveOptions{LateAfter: cfg.LateAfter})
			if cp.LastEventTimes == nil {
				cp.LastEventTimes = map[string]time.Time{}
			}
			orderKey := record.Event.Source + "|" + record.Event.Stream + "|" + record.Event.Instrument
			lastEventTime := cp.LastEventTimes[orderKey]
			if !lastEventTime.IsZero() && record.Event.EventTime.Before(lastEventTime) {
				assessment.OutOfOrder = true
				assessment.Quality = appendQualityMark(assessment.Quality, datatruth.QualityOutOfOrder)
				assessment.Event.Quality = appendQualityMark(assessment.Event.Quality, datatruth.QualityOutOfOrder)
			}
			if record.Event.EventTime.After(lastEventTime) {
				cp.LastEventTimes[orderKey] = record.Event.EventTime
			}
			cp.StreamState = compactState(cp.StreamState, cfg.MaxDedupeEntries)
			record.Event = assessment.Event
			if !record.Event.EventTime.Before(dayStart) && record.Event.EventTime.Before(dayEnd) {
				countQuality(&cp.Quality, assessment, missing)
				updateClockRange(&cp.Clocks, record.Event)
				if assessment.Accepted {
					encoded, err := json.Marshal(record)
					if err != nil {
						return Result{}, err
					}
					encoded = append(encoded, '\n')
					if _, err := writer.Write(encoded); err != nil {
						return Result{}, err
					}
					cp.OutputBytes += int64(len(encoded))
					cp.RowsEmitted++
				}
			}
		}
		if cfg.ProgressEvery > 0 && cp.LinesScanned%cfg.ProgressEvery == 0 {
			if err := save(); err != nil {
				return Result{}, err
			}
			reportProgress(cfg, cp, false)
		}
		if cfg.MaxLines > 0 && runLines >= cfg.MaxLines {
			break
		}
		if readErr == io.EOF {
			reachedEOF = true
			break
		}
	}

	if err := save(); err != nil {
		return Result{}, err
	}
	if !reachedEOF {
		reportProgress(cfg, cp, false)
		return Result{Manifest: incompleteManifest(cfg, cp), Checkpoint: checkpointPath}, nil
	}
	cp.FinalizeReady = true
	if err := save(); err != nil {
		return Result{}, err
	}
	if err := writer.Flush(); err != nil {
		return Result{}, err
	}
	if err := output.Sync(); err != nil {
		return Result{}, err
	}
	if err := output.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(partialPath, finalPath); err != nil {
		return Result{}, err
	}
	outputHash, err := hashFileProgress(finalPath, func(bytesHashed int64) {
		if cfg.OnProgress != nil {
			cfg.OnProgress(Progress{
				Stage: "hashing", Kind: cfg.Kind, LinesScanned: cp.LinesScanned,
				RowsEmitted: cp.RowsEmitted, ByteOffset: cp.ByteOffset, BytesHashed: bytesHashed,
			})
		}
	})
	if err != nil {
		return Result{}, err
	}
	finalIdentity, err := computeInputIdentity(cfg.InputPath, 0)
	if err != nil {
		return Result{}, err
	}
	manifest := completedManifest(cfg, cp, finalIdentity, finalPath, outputHash)
	if err := manifest.Validate(); err != nil {
		return Result{}, err
	}
	if err := saveJSONAtomic(manifestPath, manifest); err != nil {
		return Result{}, err
	}
	cp.Completed = true
	cp.ManifestPath = manifestPath
	cp.Input = finalIdentity
	cp.SavedAt = cfg.Now().UTC()
	if err := saveJSONAtomic(checkpointPath, cp); err != nil {
		return Result{}, err
	}
	reportProgress(cfg, cp, true)
	return Result{Manifest: manifest, ReachedEOF: true, Checkpoint: checkpointPath}, nil
}

func recoverRenamedFinal(cfg Config, cp *checkpoint, finalPath, partialPath, manifestPath, checkpointPath string) (bool, PartitionManifest, error) {
	if !cp.FinalizeReady {
		return false, PartitionManifest{}, nil
	}
	if _, err := os.Stat(partialPath); err == nil {
		return false, PartitionManifest{}, nil
	} else if !os.IsNotExist(err) {
		return false, PartitionManifest{}, err
	}
	info, err := os.Stat(finalPath)
	if os.IsNotExist(err) {
		return false, PartitionManifest{}, nil
	}
	if err != nil {
		return false, PartitionManifest{}, err
	}
	if info.Size() != cp.OutputBytes {
		return false, PartitionManifest{}, fmt.Errorf(
			"contextdata: renamed final size %d does not match checkpoint %d", info.Size(), cp.OutputBytes,
		)
	}
	outputHash, err := hashFileProgress(finalPath, func(bytesHashed int64) {
		if cfg.OnProgress != nil {
			cfg.OnProgress(Progress{Stage: "recover_final", Kind: cfg.Kind, BytesHashed: bytesHashed})
		}
	})
	if err != nil {
		return false, PartitionManifest{}, err
	}
	identity, err := computeInputIdentity(cfg.InputPath, 0)
	if err != nil {
		return false, PartitionManifest{}, err
	}
	manifest := completedManifest(cfg, *cp, identity, finalPath, outputHash)
	if err := manifest.Validate(); err != nil {
		return false, PartitionManifest{}, err
	}
	if err := saveJSONAtomic(manifestPath, manifest); err != nil {
		return false, PartitionManifest{}, err
	}
	cp.Completed = true
	cp.ManifestPath = manifestPath
	cp.Input = identity
	cp.SavedAt = cfg.Now().UTC()
	if err := saveJSONAtomic(checkpointPath, *cp); err != nil {
		return false, PartitionManifest{}, err
	}
	return true, manifest, nil
}

func appendQualityMark(marks []datatruth.QualityMark, mark datatruth.QualityMark) []datatruth.QualityMark {
	for _, existing := range marks {
		if existing == mark {
			return marks
		}
	}
	return append(marks, mark)
}

func materializeSnapshot(cfg Config) (Result, error) {
	dayDir := filepath.Join(cfg.OutputDir, cfg.Date)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(cfg.InputPath)
	if err != nil {
		return Result{}, err
	}
	configHash, err := configFingerprint(cfg)
	if err != nil {
		return Result{}, err
	}
	identity, err := computeInputIdentity(cfg.InputPath, 0)
	if err != nil {
		return Result{}, err
	}
	var (
		outputPath string
		versions   int64
		output     any
	)
	switch cfg.Kind {
	case KindInstrument:
		outputPath = filepath.Join(dayDir, "instrument.catalog.json")
		incoming, err := ConvertInstrumentSnapshot(raw)
		if err != nil {
			return Result{}, err
		}
		base := datatruth.InstrumentCatalog{}
		basePath := existingCatalogPath(cfg.BaseCatalogPath, outputPath)
		if basePath != "" {
			if err := readJSON(basePath, &base); err != nil {
				return Result{}, err
			}
		}
		merged, err := MergeInstrumentCatalog(base, incoming)
		if err != nil {
			return Result{}, err
		}
		output, versions = merged, int64(len(merged.Versions))
	case KindUniverse:
		outputPath = filepath.Join(dayDir, "universe.catalog.json")
		incoming, err := ConvertUniverseSnapshot(raw, cfg.UniverseID)
		if err != nil {
			return Result{}, err
		}
		base := datatruth.UniverseCatalog{}
		basePath := existingCatalogPath(cfg.BaseCatalogPath, outputPath)
		if basePath != "" {
			if err := readJSON(basePath, &base); err != nil {
				return Result{}, err
			}
		}
		merged, err := MergeUniverseCatalog(base, incoming)
		if err != nil {
			return Result{}, err
		}
		output, versions = merged, int64(len(merged.Versions))
	default:
		return Result{}, fmt.Errorf("contextdata: invalid snapshot kind %q", cfg.Kind)
	}
	if err := saveJSONAtomic(outputPath, output); err != nil {
		return Result{}, err
	}
	outputHash, err := hashFile(outputPath)
	if err != nil {
		return Result{}, err
	}
	manifest := PartitionManifest{
		SchemaVersion: SchemaVersion,
		DatasetID:     datasetID(cfg),
		Kind:          cfg.Kind,
		Date:          cfg.Date,
		Input:         identity,
		OutputFile:    outputPath,
		OutputHash:    outputHash,
		ConfigHash:    configHash,
		RowsScanned:   1,
		RowsEmitted:   1,
		PITVersions:   versions,
		Verdict:       VerdictPass,
		Completed:     true,
		CreatedAt:     cfg.Now().UTC(),
	}
	if versions == 0 {
		manifest.Verdict = VerdictFail
		manifest.Reasons = []string{ReasonCatalogInvalid}
	}
	manifest.Normalize()
	if err := manifest.Validate(); err != nil {
		return Result{}, err
	}
	manifestPath := filepath.Join(dayDir, string(cfg.Kind)+".manifest.json")
	if err := saveJSONAtomic(manifestPath, manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, ReachedEOF: true}, nil
}

func existingCatalogPath(explicit, outputPath string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat(outputPath); err == nil {
		return outputPath
	}
	return ""
}

func completedManifest(cfg Config, cp checkpoint, input InputIdentity, outputPath, outputHash string) PartitionManifest {
	m := incompleteManifest(cfg, cp)
	m.Input = input
	m.OutputFile = outputPath
	m.OutputHash = outputHash
	m.Completed = true
	m.CreatedAt = cfg.Now().UTC()
	m.Verdict = VerdictPass
	if m.RowsEmitted == 0 {
		m.Verdict = VerdictFail
		m.Reasons = append(m.Reasons, ReasonPartitionEmpty)
	}
	if m.Quality.MissingRequired > 0 || m.Quality.ClockInvalid > 0 ||
		m.Quality.Correction > 0 || m.Quality.OutOfOrder > 0 {
		m.Verdict = VerdictFail
		m.Reasons = append(m.Reasons, ReasonQualityInvalid)
	}
	m.Normalize()
	return m
}

func incompleteManifest(cfg Config, cp checkpoint) PartitionManifest {
	return PartitionManifest{
		SchemaVersion: SchemaVersion,
		DatasetID:     datasetID(cfg),
		Kind:          cfg.Kind,
		Date:          cfg.Date,
		Input:         cp.Input,
		ConfigHash:    cp.ConfigHash,
		RowsScanned:   cp.LinesScanned,
		RowsEmitted:   cp.RowsEmitted,
		Quality:       cp.Quality,
		Clocks:        cp.Clocks,
		Verdict:       VerdictFail,
		Reasons:       []string{ReasonIncompleteDay},
		Completed:     false,
		CreatedAt:     cfg.Now().UTC(),
	}
}

func datasetID(cfg Config) string {
	return fmt.Sprintf("context.%s.%s", cfg.Kind, cfg.Date)
}

func reportProgress(cfg Config, cp checkpoint, completed bool) {
	if cfg.OnProgress != nil {
		cfg.OnProgress(Progress{
			Stage: "materialize", Kind: cfg.Kind, LinesScanned: cp.LinesScanned, RowsEmitted: cp.RowsEmitted,
			ByteOffset: cp.ByteOffset, EventTime: cp.Clocks.MaxEventTime, Completed: completed,
		})
	}
}

func countQuality(counts *QualityCounts, assessment datatruth.EventAssessment, missing bool) {
	if assessment.Accepted {
		counts.Accepted++
	}
	if assessment.Duplicate {
		counts.Duplicate++
	}
	if assessment.OutOfOrder {
		counts.OutOfOrder++
	}
	if assessment.Gap {
		counts.Gap++
	}
	if assessment.ClockInvalid {
		counts.ClockInvalid++
	}
	if assessment.Correction {
		counts.Correction++
	}
	if assessment.MissingHash {
		counts.MissingPayloadHash++
	}
	for _, mark := range assessment.Quality {
		if mark == datatruth.QualityLate {
			counts.Late++
		}
	}
	if missing {
		counts.MissingRequired++
	}
}

func updateClockRange(r *ClockRange, event datatruth.EventEnvelope) {
	updateMinMax(&r.MinEventTime, &r.MaxEventTime, event.EventTime)
	updateMinMax(&r.MinReceiveTime, &r.MaxReceiveTime, event.ReceiveTime)
	updateMinMax(&r.MinProcessTime, &r.MaxProcessTime, event.ProcessTime)
}

func updateMinMax(minimum, maximum *time.Time, value time.Time) {
	if value.IsZero() {
		return
	}
	if minimum.IsZero() || value.Before(*minimum) {
		*minimum = value
	}
	if maximum.IsZero() || value.After(*maximum) {
		*maximum = value
	}
}

func observeBounded(state *datatruth.StreamState, env datatruth.EventEnvelope, opts datatruth.ObserveOptions) datatruth.EventAssessment {
	if state.SeenSequences == nil {
		state.SeenSequences = map[int64]string{}
	}
	if state.SeenHashes == nil {
		state.SeenHashes = map[string]string{}
	}
	assessment := datatruth.EventAssessment{Event: env, Accepted: true}
	add := func(mark datatruth.QualityMark) {
		assessment.Quality = appendQualityMark(assessment.Quality, mark)
	}
	if env.EventTime.IsZero() || env.ReceiveTime.IsZero() || env.ProcessTime.IsZero() ||
		env.ProcessTime.Before(env.ReceiveTime) || env.EventTime.After(env.ReceiveTime.Add(time.Second)) {
		assessment.ClockInvalid = true
		add(datatruth.QualityClockInvalid)
	}
	if opts.LateAfter > 0 && !env.EventTime.IsZero() && env.ReceiveTime.Sub(env.EventTime) > opts.LateAfter {
		add(datatruth.QualityLate)
	}
	if strings.TrimSpace(env.PayloadHash) == "" {
		assessment.MissingHash = true
		add(datatruth.QualityPayloadHashMissing)
	}
	if strings.TrimSpace(env.RevisionOf) != "" {
		assessment.Correction = true
		add(datatruth.QualityCorrection)
	}
	if oldHash, ok := state.SeenSequences[env.Sequence]; ok && oldHash == env.PayloadHash {
		assessment.Duplicate = true
		assessment.Accepted = false
		add(datatruth.QualityDuplicate)
	}
	if oldID, ok := state.SeenHashes[env.PayloadHash]; ok && oldID != "" {
		assessment.Duplicate = true
		assessment.Accepted = false
		add(datatruth.QualityDuplicate)
	}
	if env.Sequence < state.LastSequence {
		assessment.OutOfOrder = true
		add(datatruth.QualityOutOfOrder)
	}
	if env.Sequence > state.LastSequence+1 && state.LastSequence != 0 {
		assessment.Gap = true
		assessment.GapFrom = state.LastSequence + 1
		assessment.GapTo = env.Sequence - 1
		add(datatruth.QualityGap)
	}
	if len(assessment.Quality) == 0 {
		add(datatruth.QualityOK)
	}
	env.Quality = append([]datatruth.QualityMark(nil), assessment.Quality...)
	assessment.Event = env
	if assessment.Accepted {
		state.SeenSequences[env.Sequence] = env.PayloadHash
		if env.PayloadHash != "" {
			state.SeenHashes[env.PayloadHash] = env.EventID
		}
		if env.Sequence > state.LastSequence {
			state.LastSequence = env.Sequence
		}
	}
	return assessment
}

func compactState(state datatruth.StreamState, maxEntries int) datatruth.StreamState {
	if maxEntries <= 0 {
		return state
	}
	batch := maxEntries / 4
	if batch < 1 {
		batch = 1
	}
	if len(state.SeenSequences) <= maxEntries+batch {
		return state
	}
	return trimState(state, maxEntries)
}

func trimState(state datatruth.StreamState, maxEntries int) datatruth.StreamState {
	if maxEntries <= 0 || len(state.SeenSequences) <= maxEntries {
		return state
	}
	keys := make([]int64, 0, len(state.SeenSequences))
	for sequence := range state.SeenSequences {
		keys = append(keys, sequence)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, sequence := range keys[:len(keys)-maxEntries] {
		hash := state.SeenSequences[sequence]
		delete(state.SeenSequences, sequence)
		delete(state.SeenHashes, hash)
	}
	return state
}

func extendChain(previous string, line []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(previous))
	_, _ = hash.Write(line)
	return hex.EncodeToString(hash.Sum(nil))
}

func configFingerprint(cfg Config) (string, error) {
	value := struct {
		SchemaVersion    string        `json:"schema_version"`
		Kind             Kind          `json:"kind"`
		InputPath        string        `json:"input_path"`
		OutputDir        string        `json:"output_dir"`
		Date             string        `json:"date"`
		BaseCatalogPath  string        `json:"base_catalog_path,omitempty"`
		UniverseID       string        `json:"universe_id,omitempty"`
		LateAfter        time.Duration `json:"late_after"`
		MaxDedupeEntries int           `json:"max_dedupe_entries"`
	}{
		SchemaVersion: SchemaVersion, Kind: cfg.Kind, InputPath: cfg.InputPath,
		OutputDir: cfg.OutputDir, Date: cfg.Date, BaseCatalogPath: cfg.BaseCatalogPath,
		UniverseID: cfg.UniverseID, LateAfter: cfg.LateAfter, MaxDedupeEntries: cfg.MaxDedupeEntries,
	}
	hash, _, err := datatruth.HashJSON(value)
	return hash, err
}

func computeInputIdentity(path string, prefixSize int64) (InputIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return InputIdentity{}, err
	}
	if prefixSize <= 0 || prefixSize > info.Size() {
		prefixSize = info.Size()
		if prefixSize > identityPrefixBytes {
			prefixSize = identityPrefixBytes
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return InputIdentity{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyN(hash, file, prefixSize); err != nil && err != io.EOF {
		return InputIdentity{}, err
	}
	return InputIdentity{
		Path: path, Size: info.Size(), ModTime: info.ModTime().UTC(),
		PrefixSize: prefixSize, PrefixHash: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func verifyResume(cp checkpoint, current InputIdentity, configHash string, cfg Config) error {
	if cp.Version != checkpointVersion {
		return fmt.Errorf("%s: checkpoint version %d", ReasonInputConfigChanged, cp.Version)
	}
	if cp.ConfigHash != configHash || cp.Input.Path != current.Path || current.Size < cp.Input.Size {
		return fmt.Errorf("%s: input or config changed", ReasonInputConfigChanged)
	}
	rehash, err := computeInputIdentity(current.Path, cp.Input.PrefixSize)
	if err != nil {
		return err
	}
	if rehash.PrefixHash != cp.Input.PrefixHash {
		return fmt.Errorf("%s: input prefix changed", ReasonSourceIdentity)
	}
	chain, err := hashConsumedPrefix(current.Path, cp.ByteOffset, func(bytesHashed int64) {
		if cfg.OnProgress != nil {
			cfg.OnProgress(Progress{Stage: "verify_input", Kind: cfg.Kind, BytesHashed: bytesHashed})
		}
	})
	if err != nil {
		return err
	}
	if chain != cp.ConsumedChain {
		return fmt.Errorf("%s: consumed input range changed", ReasonSourceIdentity)
	}
	return nil
}

func hashConsumedPrefix(path string, byteOffset int64, onProgress func(int64)) (string, error) {
	if byteOffset == 0 {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256*1024)
	var total, nextReport int64
	nextReport = 64 * 1024 * 1024
	chain := ""
	for total < byteOffset {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return "", fmt.Errorf("%s: cannot verify consumed prefix at byte %d: %w", ReasonSourceIdentity, total, err)
		}
		total += int64(len(line))
		if total > byteOffset {
			return "", fmt.Errorf("%s: checkpoint byte offset is not a line boundary", ReasonSourceIdentity)
		}
		chain = extendChain(chain, line)
		if onProgress != nil && total >= nextReport {
			onProgress(total)
			nextReport += 64 * 1024 * 1024
		}
	}
	if onProgress != nil {
		onProgress(total)
	}
	return chain, nil
}

func loadCheckpoint(path string) (checkpoint, bool, error) {
	var cp checkpoint
	err := readJSON(path, &cp)
	if os.IsNotExist(err) {
		return checkpoint{}, false, nil
	}
	if err != nil {
		return checkpoint{}, false, err
	}
	return cp, true, nil
}

func loadManifest(path string) (PartitionManifest, error) {
	var manifest PartitionManifest
	if err := readJSON(path, &manifest); err != nil {
		return PartitionManifest{}, err
	}
	return manifest, manifest.Validate()
}

func readJSON(path string, output any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}

func saveJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func hashFile(path string) (string, error) {
	return hashFileProgress(path, nil)
}

func hashFileProgress(path string, onProgress func(int64)) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 8*1024*1024)
	var total, nextReport int64
	nextReport = 64 * 1024 * 1024
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
			total += int64(n)
			if onProgress != nil && total >= nextReport {
				onProgress(total)
				nextReport += 64 * 1024 * 1024
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if onProgress != nil {
		onProgress(total)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
