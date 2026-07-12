package personalwalkforward

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

// Checkpoint binds resume state with validation/survivor integrity.
type Checkpoint struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	HistoryDataHash      string    `json:"history_data_hash"`
	ConfigHash           string    `json:"config_hash"`
	PlaybookManifestHash string    `json:"playbook_manifest_hash"`
	ValidationReportHash string    `json:"validation_report_hash"`
	SurvivorsHash        string    `json:"survivors_hash"`
	OutputDir            string    `json:"output_dir"`
	NextSurvivorIndex    int       `json:"next_survivor_index"`
	Done                 bool      `json:"done"`
	SavedAt              time.Time `json:"saved_at"`
	CheckpointHash       string    `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("personalwalkforward: checkpoint path required")
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
	if cp.SchemaVersion != SchemaVersionCheckpoint {
		return Checkpoint{}, false, errors.New("personalwalkforward: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("personalwalkforward: checkpoint hash mismatch")
	}
	return cp, true, nil
}

func saveCheckpoint(path string, nextSurvivor int, done bool, cfg RunConfig, bundle g10personalvalidation.PlaybookBundle, survivors g10personalvalidation.ValidationSurvivors) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	cp := Checkpoint{
		Generation: Generation, ProtocolHash: ProtocolHash, HistoryDataHash: g10protocolv2.HistoryDataHash,
		ConfigHash: cfgHash, PlaybookManifestHash: bundle.Manifest.ManifestHash,
		ValidationReportHash: cfg.ValidationReportHash, SurvivorsHash: survivors.SurvivorsHash,
		OutputDir: cfg.OutputDir, NextSurvivorIndex: nextSurvivor, Done: done,
	}
	return SaveCheckpoint(path, cp)
}

// SaveCheckpoint atomically persists checkpoint state.
func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = SchemaVersionCheckpoint
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

// VerifyCheckpointBinding ensures resume state matches current run bindings.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, playbookManifestHash, survivorsHash string) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != cfgHash || cp.PlaybookManifestHash != playbookManifestHash || cp.SurvivorsHash != survivorsHash {
		return errors.New("personalwalkforward: checkpoint binding mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("personalwalkforward: checkpoint output dir mismatch")
	}
	return nil
}
