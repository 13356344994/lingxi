package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g4-validation.checkpoint.v1"

// Checkpoint binds resume state with catalog/config/map/candidate integrity.
type Checkpoint struct {
	SchemaVersion            string                     `json:"schema_version"`
	Generation               string                     `json:"generation"`
	ProtocolHash             string                     `json:"protocol_hash"`
	HistoryDataHash          string                     `json:"history_data_hash"`
	ConfigHash               string                     `json:"config_hash"`
	CatalogHash              string                     `json:"catalog_hash"`
	CandidateManifestHash    string                     `json:"candidate_manifest_hash"`
	CandidatesHash           string                     `json:"candidates_hash"`
	LedgerHash               string                     `json:"ledger_hash"`
	MapManifestHashes        map[string]string          `json:"map_manifest_hashes"`
	OutputDir                string                     `json:"output_dir"`
	NextSymbolIndexByProfile map[string]int             `json:"next_symbol_index_by_profile"`
	CandidateStats           []checkpointCandidateStats `json:"candidate_stats"`
	LinesProcessed           int64                      `json:"lines_processed"`
	Done                     bool                       `json:"done"`
	SavedAt                  time.Time                  `json:"saved_at"`
	CheckpointHash           string                     `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("validation: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("validation: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("validation: checkpoint hash mismatch (tamper/drift)")
	}
	return cp, true, nil
}

// SaveCheckpoint atomically persists checkpoint state.
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

// VerifyCheckpointBinding rejects drift across catalog/config/maps/candidates.
func VerifyCheckpointBinding(
	cp Checkpoint,
	cfg RunConfig,
	catalogHash string,
	candidatesHash, ledgerHash string,
	mapManifestHashes map[string]string,
) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != protocol.Generation || cp.ProtocolHash != protocol.RouterProtocolHash {
		return errors.New("validation: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != protocol.HistoryDataHash {
		return errors.New("validation: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("validation: checkpoint config hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("validation: checkpoint catalog hash mismatch")
	}
	if cp.CandidateManifestHash != cfg.CandidateManifestHash {
		return errors.New("validation: checkpoint candidate manifest hash mismatch")
	}
	if cp.CandidatesHash != candidatesHash {
		return errors.New("validation: checkpoint candidates hash mismatch")
	}
	if cp.LedgerHash != ledgerHash {
		return errors.New("validation: checkpoint ledger hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("validation: checkpoint output dir mismatch")
	}
	for profile, want := range mapManifestHashes {
		if got := cp.MapManifestHashes[profile]; got != want {
			return fmt.Errorf("validation: checkpoint map manifest hash mismatch for %s", profile)
		}
	}
	return nil
}
