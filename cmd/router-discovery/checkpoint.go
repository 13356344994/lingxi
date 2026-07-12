package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerdiscovery/backward"
)

const checkpointSchemaVersion = "lingxi.router-discovery.checkpoint.v1"

// runConfig is every knob that changes what a run computes. It is hashed
// into ConfigHash so a checkpoint can never be silently reused across a
// different configuration (input identity/config binding).
type runConfig struct {
	RootDir             string
	Families            []string
	MaxFiles            int
	MaxLines            int
	SearchSpacePath     string
	SearchSpaceHash     string
	EventManifestHash   string
	MatchWindowMS       int64
	VolatilityTolerance float64
	LiquidityTolerance  float64
	NeutralBandBPS      float64
	PermutationDraws    int
	BootstrapSamples    int
	Seed                int64
	FamilyWiseAlpha     float64
	Generation          string
	ProtocolHash        string
}

func configFingerprint(cfg runConfig) (string, error) {
	families := append([]string(nil), cfg.Families...)
	sort.Strings(families)
	payload := struct {
		SchemaVersion       string   `json:"schema_version"`
		RootDir             string   `json:"root_dir"`
		Families            []string `json:"families"`
		MaxFiles            int      `json:"max_files"`
		MaxLines            int      `json:"max_lines"`
		SearchSpacePath     string   `json:"search_space_path"`
		SearchSpaceHash     string   `json:"search_space_hash"`
		EventManifestHash   string   `json:"event_manifest_hash"`
		MatchWindowMS       int64    `json:"match_window_ms"`
		VolatilityTolerance float64  `json:"volatility_tolerance"`
		LiquidityTolerance  float64  `json:"liquidity_tolerance"`
		NeutralBandBPS      float64  `json:"neutral_band_bps"`
		PermutationDraws    int      `json:"permutation_draws"`
		BootstrapSamples    int      `json:"bootstrap_samples"`
		Seed                int64    `json:"seed"`
		FamilyWiseAlpha     float64  `json:"family_wise_alpha"`
		Generation          string   `json:"generation"`
		ProtocolHash        string   `json:"protocol_hash"`
	}{
		SchemaVersion: checkpointSchemaVersion, RootDir: cfg.RootDir, Families: families,
		MaxFiles: cfg.MaxFiles, MaxLines: cfg.MaxLines,
		SearchSpacePath: cfg.SearchSpacePath, SearchSpaceHash: cfg.SearchSpaceHash,
		EventManifestHash: cfg.EventManifestHash,
		MatchWindowMS:     cfg.MatchWindowMS, VolatilityTolerance: cfg.VolatilityTolerance,
		LiquidityTolerance: cfg.LiquidityTolerance, NeutralBandBPS: cfg.NeutralBandBPS,
		PermutationDraws: cfg.PermutationDraws, BootstrapSamples: cfg.BootstrapSamples,
		Seed: cfg.Seed, FamilyWiseAlpha: cfg.FamilyWiseAlpha,
		Generation: cfg.Generation, ProtocolHash: cfg.ProtocolHash,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// inputFingerprint binds the exact discovered file identities and contents
// so a checkpoint can detect drift (a file added, removed, reordered, or
// edited) and refuse to reuse a now-stale cached report.
func inputFingerprint(files []eventFile) (string, error) {
	sorted := append([]eventFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RelPath < sorted[j].RelPath })
	type entry struct {
		RelPath     string    `json:"rel_path"`
		Size        int64     `json:"size"`
		ModTime     time.Time `json:"mod_time"`
		ContentHash string    `json:"content_hash"`
	}
	entries := make([]entry, 0, len(sorted))
	for _, f := range sorted {
		h, err := hashFileContents(f.AbsPath)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{RelPath: f.RelPath, Size: f.Size, ModTime: f.ModTime, ContentHash: h})
	}
	hash, _, err := datatruth.HashJSON(entries)
	return hash, err
}

func hashFileContents(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, file, make([]byte, 4*1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type checkpointFile struct {
	SchemaVersion string          `json:"schema_version"`
	ConfigHash    string          `json:"config_hash"`
	InputHash     string          `json:"input_hash"`
	Done          bool            `json:"done"`
	Report        backward.Report `json:"report"`
	ReportHash    string          `json:"report_hash"`
	SavedAt       time.Time       `json:"saved_at"`
}

func loadCheckpoint(path string) (checkpointFile, bool, error) {
	if path == "" {
		return checkpointFile{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkpointFile{}, false, nil
		}
		return checkpointFile{}, false, err
	}
	var cp checkpointFile
	if err := json.Unmarshal(raw, &cp); err != nil {
		return checkpointFile{}, false, err
	}
	if cp.SchemaVersion != checkpointSchemaVersion {
		return checkpointFile{}, false, errors.New("router-discovery: checkpoint schema mismatch")
	}
	hash, _, err := datatruth.HashJSON(cp.Report)
	if err != nil {
		return checkpointFile{}, false, err
	}
	if cp.ReportHash == "" || cp.ReportHash != hash {
		return checkpointFile{}, false, errors.New("router-discovery: checkpoint report hash mismatch")
	}
	return cp, true, nil
}

func saveCheckpoint(path string, cp checkpointFile) error {
	if path == "" {
		return nil
	}
	cp.SchemaVersion = checkpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	reportHash, _, err := datatruth.HashJSON(cp.Report)
	if err != nil {
		return err
	}
	cp.ReportHash = reportHash
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
