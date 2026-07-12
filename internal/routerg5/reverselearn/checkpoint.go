package reverselearn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

const checkpointSchemaVersion = "lingxi.router-g5-reverse-learn.checkpoint.v1"

// Checkpoint binds resume state with config and manifest integrity.
type Checkpoint struct {
	SchemaVersion       string            `json:"schema_version"`
	ProfileID           string            `json:"profile_id"`
	Generation          string            `json:"generation"`
	ProtocolHash        string            `json:"protocol_hash"`
	HistoryDataHash     string            `json:"history_data_hash"`
	ConfigHash          string            `json:"config_hash"`
	CubeManifestHash    string            `json:"cube_manifest_hash"`
	FeatureManifestHash string            `json:"feature_manifest_hash"`
	OutputDir           string            `json:"output_dir"`
	ArtifactHashes      map[string]string `json:"artifact_hashes,omitempty"`
	Done                bool              `json:"done"`
	SavedAt             time.Time         `json:"saved_at"`
	CheckpointHash      string            `json:"checkpoint_hash"`
}

func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("reverselearn: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("reverselearn: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("reverselearn: checkpoint hash mismatch (tamper/drift)")
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

func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, cube CubeBundle, feature FeatureBundle) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ProfileID != cfg.ProfileID || cp.Generation != cfg.Generation || cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("reverselearn: checkpoint profile/protocol mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("reverselearn: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("reverselearn: checkpoint config hash mismatch")
	}
	if cp.CubeManifestHash != cube.Manifest.ManifestHash || cp.FeatureManifestHash != feature.Manifest.ManifestHash {
		return errors.New("reverselearn: checkpoint manifest hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("reverselearn: checkpoint output dir mismatch")
	}
	return nil
}

func VerifyArtifactHashes(outputDir string, expected map[string]string) error {
	for name, want := range expected {
		got, err := HashFile(filepath.Join(outputDir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("reverselearn: artifact hash mismatch for %s", name)
		}
	}
	return nil
}
