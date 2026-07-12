package personalvalidation

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"lingxi/internal/datatruth"
)

// Checkpoint binds resume state with catalog/config/playbook integrity.
type Checkpoint struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	ConfigHash               string            `json:"config_hash"`
	CatalogHash              string            `json:"catalog_hash"`
	PlaybookManifestHash     string            `json:"playbook_manifest_hash"`
	PlaybookHash             string            `json:"playbook_hash"`
	ExperimentHash           string            `json:"experiment_hash"`
	ConstitutionHash         string            `json:"constitution_hash"`
	ReverseTapesManifestHash string            `json:"reverse_tapes_manifest_hash"`
	AnnouncementManifestHash string            `json:"announcement_manifest_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	OutputDir                string            `json:"output_dir"`
	NextPlaybookIndex        int               `json:"next_playbook_index"`
	NextSymbolIndexByPlaybook map[string]int   `json:"next_symbol_index_by_playbook"`
	LinesProcessed           int64             `json:"lines_processed"`
	Done                     bool              `json:"done"`
	SavedAt                  time.Time         `json:"saved_at"`
	CheckpointHash           string            `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("personalvalidation: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("personalvalidation: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("personalvalidation: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
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
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, catalogHash string, bundle PlaybookBundle, mapHashes map[string]string) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != cfgHash || cp.CatalogHash != catalogHash {
		return errors.New("personalvalidation: checkpoint catalog/config mismatch")
	}
	if cp.PlaybookManifestHash != bundle.Manifest.ManifestHash ||
		cp.PlaybookHash != bundle.Manifest.PlaybookHash ||
		cp.ExperimentHash != bundle.Manifest.ExperimentHash {
		return errors.New("personalvalidation: checkpoint playbook mismatch")
	}
	if cp.ConstitutionHash != cfg.ConstitutionHash ||
		cp.ReverseTapesManifestHash != cfg.ReverseTapesManifestHash ||
		cp.AnnouncementManifestHash != cfg.AnnouncementManifestHash {
		return errors.New("personalvalidation: checkpoint proof binding mismatch")
	}
	for k, v := range mapHashes {
		if cp.MapManifestHashes[k] != v {
			return errors.New("personalvalidation: checkpoint map manifest mismatch")
		}
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("personalvalidation: checkpoint output dir mismatch")
	}
	return nil
}
