package profitmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
)

const checkpointSchemaVersion = "lingxi.router-g4-profit-map.checkpoint.v2"

// Checkpoint binds profile+symbol resume state with catalog/config integrity.
type Checkpoint struct {
	SchemaVersion      string            `json:"schema_version"`
	ProfileID          string            `json:"profile_id"`
	Generation         string            `json:"generation"`
	ProtocolHash       string            `json:"protocol_hash"`
	HistoryDataHash    string            `json:"history_data_hash"`
	CatalogHash        string            `json:"catalog_hash"`
	CatalogSymbolCount int               `json:"catalog_symbol_count"`
	SelectedFilesHash  string            `json:"selected_files_hash"`
	FullMarket         bool              `json:"full_market"`
	MapScope           string            `json:"map_scope"`
	ConfigHash         string            `json:"config_hash"`
	BoundariesHash     string            `json:"boundaries_hash"`
	CutpointsHash      string            `json:"cutpoints_hash"`
	SymbolClassHash    string            `json:"symbol_class_hash"`
	OutputDir          string            `json:"output_dir"`
	NextSymbolIndex    int               `json:"next_symbol_index"`
	CompletedSymbols   []string          `json:"completed_symbols"`
	CellHash           string            `json:"cell_hash"`
	Accumulator        StoreSnapshot     `json:"accumulator"`
	ArtifactHashes     map[string]string `json:"artifact_hashes,omitempty"`
	LinesProcessed     int64             `json:"lines_processed"`
	Done               bool              `json:"done"`
	SavedAt            time.Time         `json:"saved_at"`
	CheckpointHash     string            `json:"checkpoint_hash"`
}

// LoadCheckpoint reads a checkpoint file; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("profitmap: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("profitmap: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("profitmap: checkpoint hash mismatch (tamper/drift)")
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

// VerifyCheckpointBinding rejects drift across catalog/config/partitions.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, catalogHash string, symbolCount int, selectedFilesHash string, scope MapScope, boundariesHash, cutpointsHash, symbolClassHash string) error {
	if cp.ProfileID != cfg.ProfileID {
		return fmt.Errorf("profitmap: checkpoint profile %q != config %q", cp.ProfileID, cfg.ProfileID)
	}
	if cp.Generation != cfg.Generation || cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("profitmap: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("profitmap: checkpoint history data hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("profitmap: checkpoint catalog hash mismatch")
	}
	if cp.CatalogSymbolCount != symbolCount {
		return errors.New("profitmap: checkpoint catalog symbol count mismatch")
	}
	if cp.SelectedFilesHash != selectedFilesHash {
		return errors.New("profitmap: checkpoint selected file identities mismatch")
	}
	if cp.FullMarket != scope.FullMarket || cp.MapScope != scope.MapScope {
		return errors.New("profitmap: checkpoint map scope mismatch")
	}
	if cp.ConfigHash != mustConfigHash(cfg) {
		return errors.New("profitmap: checkpoint config hash mismatch")
	}
	if cp.BoundariesHash != boundariesHash {
		return errors.New("profitmap: checkpoint boundaries hash mismatch")
	}
	if cp.CutpointsHash != cutpointsHash {
		return errors.New("profitmap: checkpoint cutpoints hash mismatch")
	}
	if cp.SymbolClassHash != symbolClassHash {
		return errors.New("profitmap: checkpoint symbol class hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("profitmap: checkpoint output dir mismatch")
	}
	return nil
}

func mustConfigHash(cfg RunConfig) string {
	hash, err := ConfigFingerprint(cfg)
	if err != nil {
		panic(err)
	}
	return hash
}
