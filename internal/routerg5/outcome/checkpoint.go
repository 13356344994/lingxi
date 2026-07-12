package outcome

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
	g4protocol "lingxi/internal/routerg4/protocol"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const checkpointSchemaVersion = "lingxi.router-g5-outcome-oracle.checkpoint.v1"

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
	ActionSpaceHash    string            `json:"action_space_hash"`
	FullMarket         bool              `json:"full_market"`
	CubeScope          string            `json:"cube_scope"`
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

func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("outcome: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("outcome: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("outcome: checkpoint hash mismatch (tamper/drift)")
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

func VerifyCheckpointBinding(
	cp Checkpoint,
	cfg RunConfig,
	catalogHash string,
	symbolCount int,
	selectedFilesHash, actionSpaceHash string,
	scope CubeScope,
	boundariesHash, cutpointsHash, symbolClassHash string,
) error {
	if cp.ProfileID != cfg.ProfileID {
		return fmt.Errorf("outcome: checkpoint profile %q != config %q", cp.ProfileID, cfg.ProfileID)
	}
	if cp.Generation != cfg.Generation || cp.ProtocolHash != cfg.ProtocolHash {
		return errors.New("outcome: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != cfg.HistoryDataHash {
		return errors.New("outcome: checkpoint history data hash mismatch")
	}
	if cp.CatalogHash != catalogHash {
		return errors.New("outcome: checkpoint catalog hash mismatch")
	}
	if cp.CatalogSymbolCount != symbolCount {
		return errors.New("outcome: checkpoint catalog symbol count mismatch")
	}
	if cp.SelectedFilesHash != selectedFilesHash {
		return errors.New("outcome: checkpoint selected file identities mismatch")
	}
	if cp.ActionSpaceHash != actionSpaceHash {
		return errors.New("outcome: checkpoint action space hash mismatch")
	}
	if cp.FullMarket != scope.FullMarket || cp.CubeScope != scope.CubeScope {
		return errors.New("outcome: checkpoint cube scope mismatch")
	}
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("outcome: checkpoint config hash mismatch")
	}
	if cp.BoundariesHash != boundariesHash {
		return errors.New("outcome: checkpoint boundaries hash mismatch")
	}
	if cp.CutpointsHash != cutpointsHash {
		return errors.New("outcome: checkpoint cutpoints hash mismatch")
	}
	if cp.SymbolClassHash != symbolClassHash {
		return errors.New("outcome: checkpoint symbol class hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("outcome: checkpoint output dir mismatch")
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
			return fmt.Errorf("outcome: artifact hash mismatch for %s", name)
		}
	}
	return nil
}

// HashFile returns sha256 hex for one file.
func HashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash, _, err := datatruth.HashJSON(json.RawMessage(raw))
	return hash, err
}

// ScopeForSelection maps a G4 catalog selection into cube scope metadata.
func ScopeForSelection(selection profitmap.Selection) CubeScope {
	scope := CubeScope{
		AvailablePrimaryFiles: selection.Available,
		SelectedPrimaryFiles:  len(selection.Files),
		SelectionTruncated:    selection.Truncated,
		FullMarket:            !selection.Truncated,
		CubeScope:             FullMarketScope,
	}
	if selection.Truncated {
		scope.CubeScope = TruncatedSmokeScope
	}
	return scope
}

func ValidateSelectionScope(selection profitmap.Selection, catalogSymbolLimit int, allowTruncatedSmoke bool) (CubeScope, error) {
	scope := ScopeForSelection(selection)
	err := g4protocol.ValidateFullMarketScope(g4protocol.FullMarketSummary{
		Available: scope.AvailablePrimaryFiles,
		Selected:  scope.SelectedPrimaryFiles,
		Truncated: scope.SelectionTruncated,
		FullMarket: scope.FullMarket,
		Scope:     scope.CubeScope,
	}, catalogSymbolLimit, allowTruncatedSmoke)
	return scope, err
}

func validateOutputScope(available, selected int, truncated, fullMarket bool, cubeScope string, expected CubeScope) error {
	if available != expected.AvailablePrimaryFiles || selected != expected.SelectedPrimaryFiles ||
		truncated != expected.SelectionTruncated || fullMarket != expected.FullMarket || cubeScope != expected.CubeScope {
		return fmt.Errorf("scope fields mismatch")
	}
	return nil
}

// DefaultProgressEvery returns the frozen progress cadence.
func DefaultProgressEvery() int {
	return g4protocol.ScanProgressEveryLines
}

// DefaultGeneration returns the frozen G5 generation id.
func DefaultGeneration() string {
	return g5protocol.Generation
}

// DefaultProtocolHash returns the frozen G5 protocol hash.
func DefaultProtocolHash() string {
	return g5protocol.RouterProtocolHash
}

// DefaultHistoryDataHash returns the frozen history data hash.
func DefaultHistoryDataHash() string {
	return g5protocol.HistoryDataHash
}
