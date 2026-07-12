package reverselearn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// WritePolicy atomically writes g5_policy.json.
func WritePolicy(outputDir string, policy FrozenPolicy) error {
	return writeJSONAtomic(filepath.Join(outputDir, "g5_policy.json"), policy)
}

// WriteLedger atomically writes g5_policy_ledger.json.
func WriteLedger(outputDir string, ledger ExperimentLedger) error {
	return writeJSONAtomic(filepath.Join(outputDir, "g5_policy_ledger.json"), ledger)
}

// WriteTrainingReport atomically writes g5_training_report.json.
func WriteTrainingReport(outputDir string, report TrainingReport) error {
	return writeJSONAtomic(filepath.Join(outputDir, "g5_training_report.json"), report)
}

// BuildPolicyManifest seals output manifest with artifact hashes.
func BuildPolicyManifest(
	cfg RunConfig,
	cube CubeBundle,
	feature FeatureBundle,
	policy FrozenPolicy,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
) (PolicyManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return PolicyManifest{}, err
	}
	manifest := PolicyManifest{
		SchemaVersion:       SchemaVersionManifest,
		Generation:          g5protocol.Generation,
		ProtocolHash:        g5protocol.RouterProtocolHash,
		HistoryDataHash:     g5protocol.HistoryDataHash,
		ProfileID:           cfg.ProfileID,
		FeatureHash:         policy.FeatureHash,
		PolicyHash:          policy.PolicyHash,
		ExperimentHash:      ledger.LedgerHash,
		CubeManifestHash:    cube.Manifest.ManifestHash,
		FeatureManifestHash: feature.Manifest.ManifestHash,
		TrainingLabelHash:   cube.Manifest.TrainingLabelHash,
		ConfigHash:          cfgHash,
		ArtifactHashes:      artifactHashes,
		ExamActions:         g5protocol.ExamActions,
		CreatedAt:           time.Now().UTC(),
	}
	hash, err := hashPolicyManifest(manifest)
	if err != nil {
		return PolicyManifest{}, err
	}
	manifest.ManifestHash = hash
	return manifest, nil
}

// BuildNoFrozenPolicyManifest seals a degenerate training manifest without scoreable policy.
func BuildNoFrozenPolicyManifest(
	cfg RunConfig,
	cube CubeBundle,
	feature FeatureBundle,
	ledger ExperimentLedger,
	artifactHashes map[string]string,
) (PolicyManifest, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return PolicyManifest{}, err
	}
	manifest := PolicyManifest{
		SchemaVersion:       SchemaVersionManifest,
		Generation:          g5protocol.Generation,
		ProtocolHash:        g5protocol.RouterProtocolHash,
		HistoryDataHash:     g5protocol.HistoryDataHash,
		ProfileID:           cfg.ProfileID,
		Status:              StatusNoFrozenPolicy,
		FeatureHash:         feature.Manifest.FeatureHash,
		PolicyHash:          "",
		ExperimentHash:      ledger.LedgerHash,
		CubeManifestHash:    cube.Manifest.ManifestHash,
		FeatureManifestHash: feature.Manifest.ManifestHash,
		TrainingLabelHash:   cube.Manifest.TrainingLabelHash,
		ConfigHash:          cfgHash,
		ArtifactHashes:      artifactHashes,
		ExamActions:         g5protocol.ExamActions,
		CreatedAt:           time.Now().UTC(),
	}
	hash, err := hashPolicyManifest(manifest)
	if err != nil {
		return PolicyManifest{}, err
	}
	manifest.ManifestHash = hash
	return manifest, nil
}

// IsNoFrozenPolicy reports whether validation/WF must skip replay for absent policy.
func IsNoFrozenPolicy(manifest PolicyManifest) bool {
	return manifest.Status == StatusNoFrozenPolicy
}

// WritePolicyManifest atomically writes policy_manifest.json.
func WritePolicyManifest(outputDir string, manifest PolicyManifest) error {
	return writeJSONAtomic(filepath.Join(outputDir, "policy_manifest.json"), manifest)
}

func hashPolicyManifest(m PolicyManifest) (string, error) {
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ConfigFingerprint hashes run config.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	payload := cfg
	payload.SchemaVersion = SchemaVersionConfig
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func writeJSONAtomic(path string, payload any) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
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

// HashFile returns sha256 hex via datatruth canonical JSON when possible.
func HashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash, _, err := datatruth.HashJSON(json.RawMessage(raw))
	return hash, err
}
