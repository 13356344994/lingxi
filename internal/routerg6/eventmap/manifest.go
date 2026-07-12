package eventmap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/protocol"
)

const manifestSchemaVersion = "lingxi.router-g6-event-map.manifest.v1"

// MapManifest binds catalog, protocol, config, partitions, and cell hashes.
type MapManifest struct {
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
	MapScope              string            `json:"map_scope"`
	SelectedFilesHash     string            `json:"selected_files_hash"`
	SelectedSymbolsHash   string            `json:"selected_symbols_hash"`
	BoundariesHash        string            `json:"boundaries_hash"`
	ThresholdsHash        string            `json:"thresholds_hash"`
	CellHash              string            `json:"cell_hash"`
	ReservoirRiskHash     string            `json:"reservoir_risk_hash"`
	ArtifactHashes        map[string]string `json:"artifact_hashes"`
	Partitions            []string          `json:"partitions"`
	EventFamilies         []string          `json:"event_families"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ManifestHash          string            `json:"manifest_hash"`
}

// BuildManifest constructs a manifest from run outputs.
func BuildManifest(
	profileID string,
	configHash string,
	catalogHash string,
	selection profitmap.Selection,
	symbols []string,
	boundaries Boundaries,
	thresholds FrozenThresholds,
	cellHash string,
	reservoirRiskHash string,
	artifactHashes map[string]string,
	partitions []string,
) (MapManifest, error) {
	symbolsHash, _, err := datatruth.HashJSON(symbols)
	if err != nil {
		return MapManifest{}, err
	}
	filesHash, _, err := datatruth.HashJSON(selection.Files)
	if err != nil {
		return MapManifest{}, err
	}
	bHash, err := HashBoundaries(boundaries)
	if err != nil {
		return MapManifest{}, err
	}
	tHash, err := HashThresholds(thresholds)
	if err != nil {
		return MapManifest{}, err
	}
	scope := ScopeForSelection(selection)
	boundArtifacts := make(map[string]string, len(artifactHashes))
	for name, hash := range artifactHashes {
		boundArtifacts[name] = hash
	}
	m := MapManifest{
		SchemaVersion:         manifestSchemaVersion,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		ProfileID:             profileID,
		ConfigHash:            configHash,
		CatalogHash:           catalogHash,
		CatalogSymbolCount:    len(selection.Files),
		AvailablePrimaryFiles: selection.Available,
		SelectedPrimaryFiles:  len(selection.Files),
		SelectionTruncated:    selection.Truncated,
		FullMarket:            scope.FullMarket,
		MapScope:              scope.MapScope,
		SelectedFilesHash:     filesHash,
		SelectedSymbolsHash:   symbolsHash,
		BoundariesHash:        bHash,
		ThresholdsHash:        tHash,
		CellHash:              cellHash,
		ReservoirRiskHash:     reservoirRiskHash,
		ArtifactHashes:        boundArtifacts,
		Partitions:            append([]string(nil), partitions...),
		EventFamilies:         PrimaryEventFamilies(),
		ExamActions:           protocol.ExamActions,
		CreatedAt:             time.Now().UTC(),
	}
	hash, err := hashManifest(m)
	if err != nil {
		return MapManifest{}, err
	}
	m.ManifestHash = hash
	return m, nil
}

func hashManifest(m MapManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// WriteManifest atomically writes event_map_manifest.json.
func WriteManifest(outputDir string, manifest MapManifest) error {
	path := filepath.Join(outputDir, "event_map_manifest.json")
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

// WritePartitionMap atomically writes one partition map file.
func WritePartitionMap(outputDir string, pm PartitionMap) error {
	name := fmt.Sprintf("event_map_%s_%s.json", pm.Partition, pm.ProfileID)
	path := filepath.Join(outputDir, name)
	raw, err := json.MarshalIndent(pm, "", "  ")
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

// HashFile returns sha256 hex of one file.
func HashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// WriteThresholds atomically writes frozen thresholds artifact.
func WriteThresholds(outputDir string, th FrozenThresholds) error {
	path := filepath.Join(outputDir, "event_map_thresholds.json")
	raw, err := json.MarshalIndent(th, "", "  ")
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
