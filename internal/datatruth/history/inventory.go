package history

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	RootDir       string
	Families      []string
	MaxFiles      int
	MaxLines      int64
	ProgressEvery int64
	Checkpoint    string
	Now           func() time.Time
	OnProgress    func(Progress)
}

type Progress struct {
	Stage      string    `json:"stage"`
	Family     string    `json:"family,omitempty"`
	File       string    `json:"file,omitempty"`
	FilesDone  int       `json:"files_done"`
	FilesTotal int       `json:"files_total"`
	Lines      int64     `json:"lines"`
	Completed  bool      `json:"completed"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Result struct {
	Catalog    HistoryCatalog `json:"catalog"`
	Completed  bool           `json:"completed"`
	Checkpoint string         `json:"checkpoint"`
}

type fileAccumulator struct {
	lines  int64
	fields map[string]struct{}
	minT   time.Time
	maxT   time.Time
}

func Inventory(ctx context.Context, cfg Config) (Result, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return Result{}, err
	}
	files, _, err := discoverInputFiles(cfg.RootDir, cfg.Families, cfg.MaxFiles)
	if err != nil {
		return Result{}, err
	}
	configHash, err := fingerprintConfig(cfg.RootDir, cfg.Families, cfg.MaxFiles)
	if err != nil {
		return Result{}, err
	}

	donePath := checkpointDonePath(cfg.Checkpoint)
	state, hasCheckpoint, err := loadCheckpoint(cfg.Checkpoint)
	if err != nil {
		return Result{}, err
	}
	doneExists := fileExists(donePath)
	if doneExists {
		if !hasCheckpoint || !state.Done {
			return Result{}, errors.New("history: .done exists but checkpoint is not completed")
		}
		if err := verifyInputBinding(state, files, configHash); err != nil {
			return Result{}, err
		}
		if err := verifyDoneMarker(donePath, state.Catalog, configHash); err != nil {
			return Result{}, err
		}
		return Result{Catalog: state.Catalog, Completed: true, Checkpoint: cfg.Checkpoint}, nil
	}

	if hasCheckpoint {
		if state.Done {
			return Result{}, errors.New("history: completed checkpoint exists but .done marker missing")
		}
		if err := verifyInputBinding(state, files, configHash); err != nil {
			return Result{}, err
		}
	} else {
		state = checkpointState{
			Version:      checkpointVersion,
			ConfigHash:   configHash,
			RootDir:      cfg.RootDir,
			Files:        files,
			SeenFileHash: map[string]string{},
		}
	}
	if len(state.Files) == 0 {
		state.Files = files
	}
	if state.SeenFileHash == nil {
		state.SeenFileHash = map[string]string{}
		for _, completed := range state.Completed {
			if !completed.Empty {
				state.SeenFileHash[completed.FileHash] = completed.Path
			}
		}
	}

	save := func() error { return saveCheckpoint(cfg.Checkpoint, state) }
	if err := save(); err != nil {
		return Result{}, err
	}

	processedThisRun := int64(0)
	for state.NextFileIndex < len(state.Files) {
		select {
		case <-ctx.Done():
			if err := save(); err != nil {
				return Result{}, err
			}
			reportProgress(cfg, state, false)
			return Result{Completed: false, Checkpoint: cfg.Checkpoint}, ctx.Err()
		default:
		}
		entry := state.Files[state.NextFileIndex]
		beforeLineOffset := state.LineOffset
		stats, interrupted, err := processOneFile(ctx, cfg, &state, entry, processedThisRun)
		if err != nil {
			return Result{}, err
		}
		processedThisRun += stats.lines - beforeLineOffset
		if interrupted {
			if err := save(); err != nil {
				return Result{}, err
			}
			reportProgress(cfg, state, false)
			if ctx.Err() != nil {
				return Result{Completed: false, Checkpoint: cfg.Checkpoint}, ctx.Err()
			}
			return Result{Completed: false, Checkpoint: cfg.Checkpoint}, nil
		}
		complete, err := finalizeFile(entry, stats)
		if err != nil {
			return Result{}, err
		}
		if !complete.Empty {
			if previousPath, exists := state.SeenFileHash[complete.FileHash]; exists {
				return Result{}, fmt.Errorf("history: duplicate file hash %s (%s and %s)", complete.FileHash, previousPath, complete.Path)
			}
			state.SeenFileHash[complete.FileHash] = complete.Path
		}
		state.Completed = append(state.Completed, complete)
		state.NextFileIndex++
		state.ByteOffset = 0
		state.LineOffset = 0
		state.LineChain = ""
		state.CurrentFields = nil
		state.CurrentMin = time.Time{}
		state.CurrentMax = time.Time{}
		if err := save(); err != nil {
			return Result{}, err
		}
		if len(state.Completed)%100 == 0 {
			reportProgress(cfg, state, false)
		}
		if cfg.MaxLines > 0 && processedThisRun >= cfg.MaxLines {
			return Result{Completed: false, Checkpoint: cfg.Checkpoint}, nil
		}
	}

	if len(state.Completed) != len(state.Files) {
		return Result{}, errors.New("history: incomplete scan state")
	}
	catalog, err := buildCatalog(cfg.RootDir, append([]FileStats(nil), state.Completed...), cfg.Now())
	if err != nil {
		return Result{}, err
	}
	state.Catalog = catalog
	state.Done = true
	if err := save(); err != nil {
		return Result{}, err
	}
	if err := saveDoneMarker(donePath, catalog, configHash); err != nil {
		return Result{}, err
	}
	reportProgress(cfg, state, true)
	return Result{Catalog: catalog, Completed: true, Checkpoint: cfg.Checkpoint}, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	var err error
	if cfg.RootDir == "" {
		cfg.RootDir = filepath.Join("records", "history")
	}
	if cfg.RootDir, err = filepath.Abs(cfg.RootDir); err != nil {
		return Config{}, err
	}
	if cfg.Checkpoint == "" {
		cfg.Checkpoint = filepath.Join(cfg.RootDir, ".history-inventory.checkpoint.json")
	}
	if cfg.Checkpoint, err = filepath.Abs(cfg.Checkpoint); err != nil {
		return Config{}, err
	}
	cleanFamilies := make([]string, 0, len(cfg.Families))
	seen := map[string]struct{}{}
	for _, family := range cfg.Families {
		family = strings.TrimSpace(family)
		if family == "" {
			continue
		}
		if _, exists := seen[family]; exists {
			continue
		}
		seen[family] = struct{}{}
		cleanFamilies = append(cleanFamilies, family)
	}
	sort.Strings(cleanFamilies)
	cfg.Families = cleanFamilies
	if cfg.ProgressEvery <= 0 {
		cfg.ProgressEvery = 1000
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return cfg, nil
}

func processOneFile(ctx context.Context, cfg Config, state *checkpointState, file inputFile, alreadyProcessed int64) (fileAccumulator, bool, error) {
	f, err := os.Open(file.AbsPath)
	if err != nil {
		return fileAccumulator{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fileAccumulator{}, false, err
	}
	if info.Size() < state.ByteOffset {
		return fileAccumulator{}, false, fmt.Errorf("history: file truncated during resume: %s", file.RelPath)
	}
	if _, err := f.Seek(state.ByteOffset, io.SeekStart); err != nil {
		return fileAccumulator{}, false, err
	}
	reader := bufio.NewReaderSize(f, 256*1024)
	acc := fileAccumulator{
		lines:  state.LineOffset,
		fields: map[string]struct{}{},
		minT:   state.CurrentMin,
		maxT:   state.CurrentMax,
	}
	for _, field := range state.CurrentFields {
		acc.fields[field] = struct{}{}
	}
	initialLineOffset := state.LineOffset

	for {
		select {
		case <-ctx.Done():
			return acc, true, nil
		default:
		}
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr == io.EOF {
			return acc, false, nil
		}
		if readErr != nil && readErr != io.EOF {
			return fileAccumulator{}, false, readErr
		}
		if readErr == io.EOF && !bytes.HasSuffix(line, []byte{'\n'}) {
			return fileAccumulator{}, false, fmt.Errorf("history: truncated final line in %s", file.RelPath)
		}
		raw := bytes.TrimSpace(line)
		if len(raw) == 0 {
			state.ByteOffset += int64(len(line))
			if readErr == io.EOF {
				return acc, false, nil
			}
			continue
		}
		fields, minT, maxT, err := parseLine(raw)
		if err != nil {
			return fileAccumulator{}, false, fmt.Errorf("history: %s line %d: %w", file.RelPath, acc.lines+1, err)
		}
		for _, field := range fields {
			acc.fields[field] = struct{}{}
		}
		mergeMinMax(&acc.minT, &acc.maxT, minT)
		mergeMinMax(&acc.minT, &acc.maxT, maxT)
		acc.lines++

		state.ByteOffset += int64(len(line))
		state.LineOffset = acc.lines
		state.TotalLines++
		state.LineChain = extendChain(state.LineChain, line)
		state.CurrentFields = sortedKeys(acc.fields)
		state.CurrentMin = acc.minT
		state.CurrentMax = acc.maxT

		linesThisRun := alreadyProcessed + (acc.lines - initialLineOffset)
		if cfg.ProgressEvery > 0 && state.TotalLines%cfg.ProgressEvery == 0 {
			if err := saveCheckpoint(cfg.Checkpoint, *state); err != nil {
				return fileAccumulator{}, false, err
			}
			reportProgress(cfg, *state, false)
		}
		if cfg.MaxLines > 0 && linesThisRun >= cfg.MaxLines {
			return acc, true, nil
		}
		if readErr == io.EOF {
			return acc, false, nil
		}
	}
}

func finalizeFile(file inputFile, stats fileAccumulator) (FileStats, error) {
	fileHash, bytesHashed, err := hashFile(file.AbsPath)
	if err != nil {
		return FileStats{}, err
	}
	fields := sortedKeys(stats.fields)
	return FileStats{
		Family:      file.Family,
		Path:        file.RelPath,
		Size:        file.Size,
		ModTime:     file.ModTime,
		Lines:       stats.lines,
		Fields:      fields,
		MinTime:     stats.minT,
		MaxTime:     stats.maxT,
		FileHash:    fileHash,
		BytesHashed: bytesHashed,
		Empty:       stats.lines == 0 || file.Size == 0,
	}, nil
}

func parseLine(raw []byte) ([]string, time.Time, time.Time, error) {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	fields := map[string]struct{}{}
	var minT, maxT time.Time
	visitValue("", decoded, fields, &minT, &maxT)
	return sortedKeys(fields), minT, maxT, nil
}

func visitValue(path string, value any, fields map[string]struct{}, minT, maxT *time.Time) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			fields[next] = struct{}{}
			if t, ok := parseTimeCandidate(key, v[key]); ok {
				mergeMinMax(minT, maxT, t)
			}
			visitValue(next, v[key], fields, minT, maxT)
		}
	case []any:
		arrayPath := path
		if arrayPath == "" {
			arrayPath = "[]"
		} else {
			arrayPath = arrayPath + "[]"
		}
		fields[arrayPath] = struct{}{}
		if len(v) > 0 {
			if t, ok := parseTimeCandidate("", v[0]); ok {
				mergeMinMax(minT, maxT, t)
			}
		}
		for _, child := range v {
			visitValue(arrayPath, child, fields, minT, maxT)
		}
	}
}

func parseTimeCandidate(key string, value any) (time.Time, bool) {
	if key != "" && !looksLikeTimeField(key) {
		return time.Time{}, false
	}
	return parseAnyTime(value)
}

func parseAnyTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return parseUnixTimestamp(i)
		}
		if f, err := v.Float64(); err == nil {
			return parseUnixTimestamp(int64(f))
		}
	case float64:
		return parseUnixTimestamp(int64(v))
	case int64:
		return parseUnixTimestamp(v)
	case int:
		return parseUnixTimestamp(int64(v))
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{
			time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02",
		} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return parseUnixTimestamp(i)
		}
	}
	return time.Time{}, false
}

func parseUnixTimestamp(v int64) (time.Time, bool) {
	if v <= 0 {
		return time.Time{}, false
	}
	var ts time.Time
	switch {
	case v >= 1_000_000_000_000_000_000:
		ts = time.Unix(0, v)
	case v >= 1_000_000_000_000_000:
		ts = time.UnixMicro(v)
	case v >= 1_000_000_000_000:
		ts = time.UnixMilli(v)
	case v >= 946_684_800:
		ts = time.Unix(v, 0)
	default:
		return time.Time{}, false
	}
	ts = ts.UTC()
	if ts.Year() < 2000 || ts.Year() > 2100 {
		return time.Time{}, false
	}
	return ts, true
}

func looksLikeTimeField(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "_", ""), "-", ""))
	switch normalized {
	case "ts", "timestamp", "time", "eventtime", "recordedat", "createdat", "updatedat",
		"fetchedat", "fundingtime", "fundingratetimestamp", "nextfundingtime", "opentime", "closetime":
		return true
	default:
		return false
	}
}

func reportProgress(cfg Config, state checkpointState, completed bool) {
	if cfg.OnProgress == nil {
		return
	}
	family, file := "", ""
	if state.NextFileIndex < len(state.Files) {
		family = state.Files[state.NextFileIndex].Family
		file = state.Files[state.NextFileIndex].RelPath
	}
	cfg.OnProgress(Progress{
		Stage:      "inventory",
		Family:     family,
		File:       file,
		FilesDone:  len(state.Completed),
		FilesTotal: len(state.Files),
		Lines:      state.TotalLines,
		Completed:  completed,
		UpdatedAt:  cfg.Now().UTC(),
	})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
