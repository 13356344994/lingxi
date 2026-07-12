package features

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

const checkpointSchemaVersion = "lingxi.routerg5.features.checkpoint.v1"

// Checkpoint binds resume state for bounded feature context generation.
type Checkpoint struct {
	SchemaVersion     string            `json:"schema_version"`
	ProfileID         string            `json:"profile_id"`
	Generation        string            `json:"generation"`
	ProtocolHash      string            `json:"protocol_hash"`
	HistoryDataHash   string            `json:"history_data_hash"`
	CatalogHash       string            `json:"catalog_hash"`
	ConfigHash        string            `json:"config_hash"`
	BoundariesHash    string            `json:"boundaries_hash"`
	SelectedFilesHash string            `json:"selected_files_hash"`
	FeatureHash       string            `json:"feature_hash"`
	OutputDir         string            `json:"output_dir"`
	NextShardIndex  int             `json:"next_shard_index"`
	CompletedShards []int           `json:"completed_shards"`
	ShardManifests    []ShardManifest   `json:"shard_manifests"`
	TotalRows         int               `json:"total_rows"`
	LinesProcessed    int64             `json:"lines_processed"`
	Done              bool              `json:"done"`
	SavedAt           time.Time         `json:"saved_at"`
	CheckpointHash    string            `json:"checkpoint_hash"`
}

func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("features: checkpoint path required")
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
	if cp.SchemaVersion != checkpointSchemaVersion {
		return Checkpoint{}, false, errors.New("features: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("features: checkpoint hash mismatch")
	}
	return cp, true, nil
}

func SaveCheckpoint(path string, cp Checkpoint) error {
	cp.SchemaVersion = checkpointSchemaVersion
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

func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, catalogHash, boundariesHash, selectedFilesHash, featureHash string) error {
	if cp.ProfileID != cfg.ProfileID {
		return fmt.Errorf("features: checkpoint profile %q != %q", cp.ProfileID, cfg.ProfileID)
	}
	if cp.Generation != cfg.Generation || cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("features: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("features: checkpoint history data hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("features: checkpoint catalog hash mismatch")
	}
	if cp.ConfigHash != cfg.ConfigHash() {
		return errors.New("features: checkpoint config hash mismatch")
	}
	if cp.BoundariesHash != boundariesHash {
		return errors.New("features: checkpoint boundaries hash mismatch")
	}
	if cp.SelectedFilesHash != selectedFilesHash {
		return errors.New("features: checkpoint selected files hash mismatch")
	}
	if cp.FeatureHash != featureHash {
		return errors.New("features: checkpoint feature hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("features: checkpoint output dir mismatch")
	}
	return nil
}
