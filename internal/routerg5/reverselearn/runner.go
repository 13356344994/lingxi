package reverselearn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	historydata "lingxi/internal/datatruth/history"
)

// Runner orchestrates discovery reverse policy learning.
type Runner struct {
	Config           RunConfig
	Catalog          historydata.HistoryCatalog
	CheckpointPath   string
	CatalogValidator func(historydata.HistoryCatalog) error
}

// RunResult holds sealed policy outputs.
type RunResult struct {
	Policy   FrozenPolicy
	Ledger   ExperimentLedger
	Report   TrainingReport
	Manifest PolicyManifest
}

func (c RunConfig) Validate() error {
	if c.ProfileID == "" || c.CubeDir == "" || c.FeatureDir == "" || c.OutputDir == "" {
		return errors.New("reverselearn: profile, cube-dir, feature-dir, output-dir required")
	}
	return nil
}

// Run executes bounded reverse policy learning with checkpoint resume.
func (r *Runner) Run(ctx context.Context) (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	cube, err := LoadCubeBundle(r.Config.CubeDir)
	if err != nil {
		return RunResult{}, err
	}
	feature, err := LoadFeatureBundle(r.Config.FeatureDir)
	if err != nil {
		return RunResult{}, err
	}
	validator := r.CatalogValidator
	if validator == nil {
		validator = historydata.ValidateCatalog
	}
	if err := validator(r.Catalog); err != nil {
		return RunResult{}, err
	}
	if err := VerifyBindings(r.Config, r.Catalog, cube, feature); err != nil {
		return RunResult{}, err
	}
	if cp, ok, err := LoadCheckpoint(r.CheckpointPath); err != nil {
		return RunResult{}, err
	} else if ok {
		if err := VerifyCheckpointBinding(cp, r.Config, cube, feature); err != nil {
			return RunResult{}, err
		}
		if cp.Done {
			if err := VerifyArtifactHashes(r.Config.OutputDir, cp.ArtifactHashes); err != nil {
				return RunResult{}, err
			}
			return r.loadCompleted()
		}
	}
	if err := os.MkdirAll(r.Config.OutputDir, 0o755); err != nil {
		return RunResult{}, err
	}
	labels, err := LoadTrainingLabels(cube)
	if err != nil {
		return RunResult{}, err
	}
	if err := RejectValidationPartitions(labels); err != nil {
		return RunResult{}, err
	}
	contexts, err := LoadDiscoveryContexts(r.Config.FeatureDir)
	if err != nil {
		return RunResult{}, err
	}
	examples, err := JoinDiscoveryExamples(labels, contexts)
	if err != nil {
		return RunResult{}, err
	}
	outcome, err := RunTraining(examples, r.Config.ProfileID, feature.Manifest.FeatureHash)
	if err != nil {
		return RunResult{}, err
	}
	if outcome.AllAttemptsRejected {
		return r.writeRejectedOutcome(cube, feature, outcome)
	}
	policy := outcome.Policy
	ledger := outcome.Ledger
	report := outcome.Report
	if err := WritePolicy(r.Config.OutputDir, policy); err != nil {
		return RunResult{}, err
	}
	if err := WriteLedger(r.Config.OutputDir, ledger); err != nil {
		return RunResult{}, err
	}
	if err := WriteTrainingReport(r.Config.OutputDir, report); err != nil {
		return RunResult{}, err
	}
	artifactHashes := map[string]string{}
	for _, name := range []string{"g5_policy.json", "g5_policy_ledger.json", "g5_training_report.json"} {
		hash, err := HashFile(filepath.Join(r.Config.OutputDir, name))
		if err != nil {
			return RunResult{}, err
		}
		artifactHashes[name] = hash
	}
	manifest, err := BuildPolicyManifest(r.Config, cube, feature, policy, ledger, artifactHashes)
	if err != nil {
		return RunResult{}, err
	}
	if err := WritePolicyManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	artifactHashes["policy_manifest.json"], err = HashFile(filepath.Join(r.Config.OutputDir, "policy_manifest.json"))
	if err != nil {
		return RunResult{}, err
	}
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		ProfileID:           r.Config.ProfileID,
		Generation:          r.Config.Generation,
		ProtocolHash:        r.Config.ProtocolHash,
		HistoryDataHash:     r.Config.HistoryDataHash,
		ConfigHash:          cfgHash,
		CubeManifestHash:    cube.Manifest.ManifestHash,
		FeatureManifestHash: feature.Manifest.ManifestHash,
		OutputDir:           r.Config.OutputDir,
		ArtifactHashes:      artifactHashes,
		Done:                true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Policy: policy, Ledger: ledger, Report: report, Manifest: manifest}, nil
}

func (r *Runner) writeRejectedOutcome(cube CubeBundle, feature FeatureBundle, outcome TrainingOutcome) (RunResult, error) {
	if err := WriteLedger(r.Config.OutputDir, outcome.Ledger); err != nil {
		return RunResult{}, err
	}
	if err := WriteTrainingReport(r.Config.OutputDir, outcome.Report); err != nil {
		return RunResult{}, err
	}
	artifactHashes := map[string]string{}
	for _, name := range []string{"g5_policy_ledger.json", "g5_training_report.json"} {
		hash, err := HashFile(filepath.Join(r.Config.OutputDir, name))
		if err != nil {
			return RunResult{}, err
		}
		artifactHashes[name] = hash
	}
	manifest, err := BuildNoFrozenPolicyManifest(r.Config, cube, feature, outcome.Ledger, artifactHashes)
	if err != nil {
		return RunResult{}, err
	}
	if err := WritePolicyManifest(r.Config.OutputDir, manifest); err != nil {
		return RunResult{}, err
	}
	artifactHashes["policy_manifest.json"], err = HashFile(filepath.Join(r.Config.OutputDir, "policy_manifest.json"))
	if err != nil {
		return RunResult{}, err
	}
	cfgHash, err := ConfigFingerprint(r.Config)
	if err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		ProfileID:           r.Config.ProfileID,
		Generation:          r.Config.Generation,
		ProtocolHash:        r.Config.ProtocolHash,
		HistoryDataHash:     r.Config.HistoryDataHash,
		ConfigHash:          cfgHash,
		CubeManifestHash:    cube.Manifest.ManifestHash,
		FeatureManifestHash: feature.Manifest.ManifestHash,
		OutputDir:           r.Config.OutputDir,
		ArtifactHashes:      artifactHashes,
		Done:                true,
	}
	if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
		return RunResult{}, err
	}
	return RunResult{Ledger: outcome.Ledger, Report: outcome.Report, Manifest: manifest}, nil
}

func (r *Runner) loadCompleted() (RunResult, error) {
	var policy FrozenPolicy
	var ledger ExperimentLedger
	var report TrainingReport
	var manifest PolicyManifest
	for _, tc := range []struct {
		name string
		dst  any
	}{
		{"g5_policy_ledger.json", &ledger},
		{"g5_training_report.json", &report},
		{"policy_manifest.json", &manifest},
	} {
		raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, tc.name))
		if err != nil {
			return RunResult{}, err
		}
		if err := json.Unmarshal(raw, tc.dst); err != nil {
			return RunResult{}, fmt.Errorf("reverselearn: decode %s: %w", tc.name, err)
		}
	}
	if !IsNoFrozenPolicy(manifest) {
		raw, err := os.ReadFile(filepath.Join(r.Config.OutputDir, "g5_policy.json"))
		if err != nil {
			return RunResult{}, err
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			return RunResult{}, fmt.Errorf("reverselearn: decode g5_policy.json: %w", err)
		}
	}
	return RunResult{Policy: policy, Ledger: ledger, Report: report, Manifest: manifest}, nil
}
