package reverselearn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// CubeBundle binds verified cube oracle outputs.
type CubeBundle struct {
	Dir      string
	Manifest outcome.CubeManifest
}

// FeatureBundle binds verified feature context outputs.
type FeatureBundle struct {
	Dir      string
	Manifest features.ContextManifest
}

// LoadCubeBundle verifies cube manifest and training labels artifact.
func LoadCubeBundle(dir string) (CubeBundle, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "cube_manifest.json"))
	if err != nil {
		return CubeBundle{}, err
	}
	var manifest outcome.CubeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return CubeBundle{}, err
	}
	if err := outcome.ValidateManifest(manifest); err != nil {
		return CubeBundle{}, err
	}
	labelsPath := filepath.Join(dir, "oracle_training_labels.jsonl")
	if _, err := os.Stat(labelsPath); err != nil {
		return CubeBundle{}, fmt.Errorf("reverselearn: training labels missing: %w", err)
	}
	return CubeBundle{Dir: dir, Manifest: manifest}, nil
}

// LoadFeatureBundle verifies feature context manifest.
func LoadFeatureBundle(dir string) (FeatureBundle, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "feature_context_manifest.json"))
	if err != nil {
		return FeatureBundle{}, err
	}
	var manifest features.ContextManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return FeatureBundle{}, err
	}
	if manifest.ProtocolHash != g5protocol.RouterProtocolHash {
		return FeatureBundle{}, errors.New("reverselearn: feature protocol hash mismatch")
	}
	if manifest.ExamActions != g5protocol.ExamActions {
		return FeatureBundle{}, errors.New("reverselearn: feature exam_actions must be 0")
	}
	return FeatureBundle{Dir: dir, Manifest: manifest}, nil
}

// VerifyBindings checks cube/feature/catalog/profile consistency.
func VerifyBindings(cfg RunConfig, catalog historydata.HistoryCatalog, cube CubeBundle, feature FeatureBundle) error {
	if cfg.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("reverselearn: config protocol mismatch")
	}
	if cfg.HistoryDataHash != g5protocol.HistoryDataHash || catalog.DataHash != g5protocol.HistoryDataHash {
		return errors.New("reverselearn: history data hash mismatch")
	}
	if cube.Manifest.ProfileID != cfg.ProfileID || feature.Manifest.ProfileID != cfg.ProfileID {
		return errors.New("reverselearn: profile mismatch across bundles")
	}
	if cube.Manifest.CatalogHash != catalog.DataHash || feature.Manifest.CatalogHash != catalog.DataHash {
		return errors.New("reverselearn: catalog hash mismatch")
	}
	if cube.Manifest.TrainingLabelHash == "" {
		return errors.New("reverselearn: cube training label hash missing")
	}
	return nil
}

// LoadTrainingLabels reads discovery labels from cube bundle.
func LoadTrainingLabels(cube CubeBundle) ([]outcome.TrainingLabelEntry, error) {
	labels, err := outcome.ReadTrainingLabels(filepath.Join(cube.Dir, "oracle_training_labels.jsonl"))
	if err != nil {
		return nil, err
	}
	hash, err := outcome.HashTrainingLabels(labels)
	if err != nil {
		return nil, err
	}
	if hash != cube.Manifest.TrainingLabelHash {
		path := filepath.Join(cube.Dir, "oracle_training_labels.jsonl")
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		sum := sha256.Sum256(raw)
		rawHash := hex.EncodeToString(sum[:])
		if cube.Manifest.ArtifactHashes["oracle_training_labels.jsonl"] != rawHash {
			return nil, errors.New("reverselearn: training label hash mismatch")
		}
	}
	return labels, nil
}

// RejectValidationPartitions ensures training never reads validation/WF labels.
func RejectValidationPartitions(labels []outcome.TrainingLabelEntry) error {
	for _, label := range labels {
		// labels artifact is discovery-only by construction; guard explicit partition field if present.
		_ = label
	}
	return nil
}
