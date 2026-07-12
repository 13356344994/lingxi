package announcements

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint binds resume offsets with config/output integrity.
type Checkpoint struct {
	SchemaVersion  string              `json:"schema_version"`
	Generation     string              `json:"generation"`
	ConfigHash     string              `json:"config_hash"`
	OutputDir      string              `json:"output_dir"`
	SourceOffsets  map[string]int      `json:"source_offsets"`
	SourceStatus   map[string]SourceStatus `json:"source_status"`
	Accumulator    []Announcement      `json:"accumulator"`
	DedupeSkipped  int                 `json:"dedupe_skipped"`
	Done           bool                `json:"done"`
	SavedAt        time.Time           `json:"saved_at"`
	CheckpointHash string              `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("announcements: checkpoint path required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Checkpoint{}, false, nil
		}
		return Checkpoint{}, false, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Checkpoint{}, false, err
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return Checkpoint{}, false, errors.New("announcements: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("announcements: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = CheckpointSchemaVersion
	cp.SavedAt = time.Now().UTC()
	hash, err := hashCheckpoint(cp)
	if err != nil {
		return err
	}
	cp.CheckpointHash = hash
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

func hashCheckpoint(cp Checkpoint) (string, error) {
	payload := cp
	payload.CheckpointHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// VerifyCheckpointBinding rejects config/output drift on resume.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig) error {
	if cp.Generation != cfg.Generation {
		return fmt.Errorf("announcements: checkpoint generation %q != config %q", cp.Generation, cfg.Generation)
	}
	want, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != want {
		return errors.New("announcements: checkpoint config hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("announcements: checkpoint output dir mismatch")
	}
	return nil
}

// NewCheckpoint initializes a fresh checkpoint for one run.
func NewCheckpoint(cfg RunConfig, configHash string) Checkpoint {
	return Checkpoint{
		SchemaVersion: CheckpointSchemaVersion,
		Generation:    cfg.Generation,
		ConfigHash:    configHash,
		OutputDir:     cfg.OutputDir,
		SourceOffsets: map[string]int{},
		SourceStatus:  map[string]SourceStatus{},
	}
}
