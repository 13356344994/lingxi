package outcome

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

const manifestSchemaVersion = "lingxi.router-g5-outcome-oracle.manifest.v1"

// CubeManifest binds catalog, protocol, config, partitions, and cube hashes.
type CubeManifest struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	ProfileID             string            `json:"profile_id"`
	ConfigHash            string            `json:"config_hash"`
	CatalogHash           string            `json:"catalog_hash"`
	CatalogSymbolCount    int               `json:"catalog_symbol_count"`
	AvailablePrimaryFiles int               `json:"available_primary_files"`
	SelectedPrimaryFiles  int               `json:"selected_primary_files"`
	SelectionTruncated    bool              `json:"selection_truncated"`
	FullMarket            bool              `json:"full_market"`
	CubeScope             string            `json:"cube_scope"`
	SelectedFilesHash     string            `json:"selected_files_hash"`
	SelectedSymbolsHash   string            `json:"selected_symbols_hash"`
	ActionSpaceHash       string            `json:"action_space_hash"`
	BoundariesHash        string            `json:"boundaries_hash"`
	CutpointsHash         string            `json:"cutpoints_hash"`
	SymbolClassHash       string            `json:"symbol_class_hash"`
	CellHash              string            `json:"cell_hash"`
	ReservoirRiskHash     string            `json:"reservoir_risk_hash"`
	TrainingLabelCount    int               `json:"training_label_count"`
	TrainingLabelHash     string            `json:"training_label_hash"`
	ArtifactHashes        map[string]string `json:"artifact_hashes"`
	Partitions            []string          `json:"partitions"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ManifestHash          string            `json:"manifest_hash"`
}

func BuildManifest(
	profileID, configHash, catalogHash, actionSpaceHash string,
	selection profitmap.Selection,
	symbols []string,
	boundaries profitmap.Boundaries,
	cutpoints sky.Cutpoints,
	symbolClass profitmap.SymbolClassCutpoints,
	cellHash, reservoirRiskHash string,
	trainingLabelCount int, trainingLabelHash string,
	artifactHashes map[string]string,
	partitions []string,
	scope CubeScope,
) (CubeManifest, error) {
	symbolsHash, _, err := datatruth.HashJSON(symbols)
	if err != nil {
		return CubeManifest{}, err
	}
	filesHash, _, err := datatruth.HashJSON(selection.Files)
	if err != nil {
		return CubeManifest{}, err
	}
	bHash, err := profitmap.HashBoundaries(boundaries)
	if err != nil {
		return CubeManifest{}, err
	}
	cHash, err := profitmap.HashCutpoints(cutpoints)
	if err != nil {
		return CubeManifest{}, err
	}
	scHash, err := symbolClass.Hash()
	if err != nil {
		return CubeManifest{}, err
	}
	manifest := CubeManifest{
		SchemaVersion:         manifestSchemaVersion,
		Generation:            g5protocol.Generation,
		ProtocolHash:          g5protocol.RouterProtocolHash,
		HistoryDataHash:       g5protocol.HistoryDataHash,
		ProfileID:             profileID,
		ConfigHash:            configHash,
		CatalogHash:           catalogHash,
		CatalogSymbolCount:    len(symbols),
		AvailablePrimaryFiles: scope.AvailablePrimaryFiles,
		SelectedPrimaryFiles:  scope.SelectedPrimaryFiles,
		SelectionTruncated:    scope.SelectionTruncated,
		FullMarket:            scope.FullMarket,
		CubeScope:             scope.CubeScope,
		SelectedFilesHash:     filesHash,
		SelectedSymbolsHash:   symbolsHash,
		ActionSpaceHash:       actionSpaceHash,
		BoundariesHash:        bHash,
		CutpointsHash:         cHash,
		SymbolClassHash:       scHash,
		CellHash:              cellHash,
		ReservoirRiskHash:     reservoirRiskHash,
		TrainingLabelCount:    trainingLabelCount,
		TrainingLabelHash:     trainingLabelHash,
		ArtifactHashes:        artifactHashes,
		Partitions:            partitions,
		ExamActions:           g5protocol.ExamActions,
		CreatedAt:             time.Now().UTC(),
	}
	hash, err := hashManifest(manifest)
	if err != nil {
		return CubeManifest{}, err
	}
	manifest.ManifestHash = hash
	return manifest, nil
}

func hashManifest(m CubeManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ValidateManifest verifies the sealed cube identity before downstream use.
func ValidateManifest(m CubeManifest) error {
	if m.Generation != g5protocol.Generation ||
		m.ProtocolHash != g5protocol.RouterProtocolHash ||
		m.HistoryDataHash != g5protocol.HistoryDataHash ||
		m.ExamActions != g5protocol.ExamActions {
		return fmt.Errorf("outcome: cube manifest protocol/history binding mismatch")
	}
	want, err := hashManifest(m)
	if err != nil {
		return err
	}
	if m.ManifestHash == "" || m.ManifestHash != want {
		return fmt.Errorf("outcome: cube manifest hash mismatch")
	}
	return nil
}

func WriteManifest(outputDir string, manifest CubeManifest) error {
	path := filepath.Join(outputDir, "cube_manifest.json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
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

func HorizonLabels(spec profitmap.ProfileSpec) []string {
	labels := make([]string, len(spec.Horizons))
	for i, h := range spec.Horizons {
		labels[i] = h.Label
	}
	return labels
}

func ActionSpaceHashForProfile(spec profitmap.ProfileSpec) (string, error) {
	return actionspace.ActionSpaceHash(HorizonLabels(spec))
}
