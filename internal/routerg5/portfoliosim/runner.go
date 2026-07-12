package portfoliosim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// Checkpoint binds resume state with policy/feature integrity.
type Checkpoint struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	HistoryDataHash      string    `json:"history_data_hash"`
	ConfigHash           string    `json:"config_hash"`
	PolicyHash           string    `json:"policy_hash"`
	FeatureHash          string    `json:"feature_hash"`
	ExperimentHash       string    `json:"experiment_hash"`
	FeatureManifestHash  string    `json:"feature_manifest_hash"`
	PolicyManifestHash   string    `json:"policy_manifest_hash"`
	OutputDir            string    `json:"output_dir"`
	NextScenarioIndex    int       `json:"next_scenario_index"`
	Done                 bool      `json:"done"`
	SavedAt              time.Time `json:"saved_at"`
	CheckpointHash       string    `json:"checkpoint_hash"`
}

// LoadCheckpoint reads checkpoint; missing file returns zero,false,nil.
func LoadCheckpoint(path string) (Checkpoint, bool, error) {
	if path == "" {
		return Checkpoint{}, false, errors.New("portfoliosim: checkpoint path required")
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
		return Checkpoint{}, false, errors.New("portfoliosim: checkpoint schema mismatch")
	}
	want, err := hashCheckpoint(cp)
	if err != nil {
		return Checkpoint{}, false, err
	}
	if cp.CheckpointHash == "" || cp.CheckpointHash != want {
		return Checkpoint{}, false, errors.New("portfoliosim: checkpoint hash mismatch (tamper/drift)")
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

// VerifyCheckpointBinding rejects drift across policy/feature/config.
func VerifyCheckpointBinding(cp Checkpoint, cfg RunConfig, featureManifest features.ContextManifest, policyBundle policy.PolicyBundle) error {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return err
	}
	if cp.Generation != g5protocol.Generation || cp.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("portfoliosim: checkpoint protocol mismatch")
	}
	if cp.HistoryDataHash != g5protocol.HistoryDataHash {
		return errors.New("portfoliosim: checkpoint history hash mismatch")
	}
	if cp.ConfigHash != cfgHash {
		return errors.New("portfoliosim: checkpoint config hash mismatch")
	}
	if cp.PolicyHash != policyBundle.Policy.PolicyHash ||
		cp.FeatureHash != policyBundle.Policy.FeatureHash ||
		cp.ExperimentHash != policyBundle.Policy.ExperimentHash {
		return errors.New("portfoliosim: checkpoint policy hash mismatch")
	}
	if cp.FeatureManifestHash != featureManifest.ManifestHash {
		return errors.New("portfoliosim: checkpoint feature manifest hash mismatch")
	}
	if cp.PolicyManifestHash != policyBundle.PolicyManifest.ManifestHash {
		return errors.New("portfoliosim: checkpoint policy manifest hash mismatch")
	}
	if cp.OutputDir != cfg.OutputDir {
		return errors.New("portfoliosim: checkpoint output dir mismatch")
	}
	return nil
}

// Runner orchestrates bounded G5 validation replay.
type Runner struct {
	Config         RunConfig
	Catalog        historydata.HistoryCatalog
	CheckpointPath string
}

// RunResult summarizes one validation run.
type RunResult struct {
	Report  ValidationReport
	Receipt ValidationReceipt
}

// Run executes validation scenarios and writes artifacts.
func (r *Runner) Run() (RunResult, error) {
	if err := r.Config.Validate(); err != nil {
		return RunResult{}, err
	}
	if err := policy.RejectDiscoveryLabels(filepath.Join(r.Config.PolicyDir, "oracle_training_labels.jsonl")); err != nil {
		return RunResult{}, err
	}
	policyBundle, err := policy.LoadPolicyBundle(r.Config.PolicyDir)
	if err != nil {
		return RunResult{}, err
	}
	if reverselearn.IsNoFrozenPolicy(policyBundle.PolicyManifest) {
		return r.runSkippedNoFrozenPolicy(policyBundle)
	}
	featureRows, featureManifest, err := LoadValidationFeatures(r.Config.FeatureDir)
	if err != nil {
		return RunResult{}, err
	}
	if policyBundle.Policy.FeatureHash != featureManifest.FeatureHash {
		return RunResult{}, errors.New("portfoliosim: policy/feature hash mismatch")
	}
	if policy.IsDegeneratePolicy(policyBundle.Policy) {
		checks := GateChecks{}
		report, err := BuildReport(r.Config, nil, StatusValidationFail, ReasonFixedAction, checks)
		if err != nil {
			return RunResult{}, err
		}
		receipt, err := BuildReceipt(r.Config, report, featureManifest, policyBundle.PolicyManifest, map[string]string{
			"catalog_hash": r.Catalog.DataHash,
		})
		if err != nil {
			return RunResult{}, err
		}
		if err := writeArtifacts(r.Config.OutputDir, report, receipt); err != nil {
			return RunResult{}, err
		}
		return RunResult{Report: report, Receipt: receipt}, nil
	}
	timestamps := sortedTimestamps(featureRows)
	horizons := policyBundle.Policy.HorizonLabels
	if len(horizons) == 0 {
		spec, err := profitmap.ProfileSpecFor(r.Config.ProfileID)
		if err != nil {
			return RunResult{}, err
		}
		for _, h := range spec.Horizons {
			horizons = append(horizons, h.Label)
		}
	}
	profile, err := features.ProfileFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	barsBundle, err := LoadCanonicalBars(r.Catalog, r.Config.HistoryRoot, r.Config.ProfileID, featureRows, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	engine := ScenarioEngine{
		Policy:    policyBundle.Policy,
		Bars:      barsBundle,
		Profile:   profile,
		Horizons:  horizons,
		Partition: outcome.PartitionValidation,
	}
	scenarios := engine.RunScenarios(featureRows, timestamps)
	status, checks, reason := EvaluateGates(scenarios)
	report, err := BuildReport(r.Config, scenarios, status, reason, checks)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(r.Config, report, featureManifest, policyBundle.PolicyManifest, map[string]string{
		"catalog_hash":          r.Catalog.DataHash,
		"feature_manifest_hash": featureManifest.ManifestHash,
		"policy_manifest_hash":  policyBundle.PolicyManifest.ManifestHash,
	})
	if err != nil {
		return RunResult{}, err
	}
	if err := writeArtifacts(r.Config.OutputDir, report, receipt); err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		Generation:          g5protocol.Generation,
		ProtocolHash:        g5protocol.RouterProtocolHash,
		HistoryDataHash:     g5protocol.HistoryDataHash,
		ConfigHash:          report.ConfigHash,
		PolicyHash:          policyBundle.Policy.PolicyHash,
		FeatureHash:         policyBundle.Policy.FeatureHash,
		ExperimentHash:      policyBundle.Policy.ExperimentHash,
		FeatureManifestHash: featureManifest.ManifestHash,
		PolicyManifestHash:  policyBundle.PolicyManifest.ManifestHash,
		OutputDir:           r.Config.OutputDir,
		NextScenarioIndex:   len(scenarios),
		Done:                true,
	}
	if r.CheckpointPath != "" {
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
	}
	return RunResult{Report: report, Receipt: receipt}, nil
}

func (r *Runner) runSkippedNoFrozenPolicy(policyBundle policy.PolicyBundle) (RunResult, error) {
	manifest := policyBundle.PolicyManifest
	cfg := r.Config
	cfg.PolicyHash = ""
	cfg.FeatureHash = manifest.FeatureHash
	cfg.ExperimentHash = manifest.ExperimentHash
	checks := GateChecks{}
	report, err := BuildReport(cfg, nil, StatusSkippedPolicyUnavailable, ReasonNoGo, checks)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(cfg, report, features.ContextManifest{}, manifest, map[string]string{
		"catalog_hash": r.Catalog.DataHash,
	})
	if err != nil {
		return RunResult{}, err
	}
	if err := writeArtifacts(r.Config.OutputDir, report, receipt); err != nil {
		return RunResult{}, err
	}
	cp := Checkpoint{
		Generation:         g5protocol.Generation,
		ProtocolHash:       g5protocol.RouterProtocolHash,
		HistoryDataHash:    g5protocol.HistoryDataHash,
		ConfigHash:         report.ConfigHash,
		PolicyHash:         "",
		FeatureHash:        manifest.FeatureHash,
		ExperimentHash:     manifest.ExperimentHash,
		PolicyManifestHash: manifest.ManifestHash,
		OutputDir:          r.Config.OutputDir,
		Done:               true,
	}
	if r.CheckpointPath != "" {
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
	}
	return RunResult{Report: report, Receipt: receipt}, nil
}

func (c RunConfig) Validate() error {
	if c.ProfileID == "" || c.PolicyDir == "" || c.FeatureDir == "" || c.OutputDir == "" {
		return errors.New("portfoliosim: profile, policy-dir, feature-dir, output-dir required")
	}
	if c.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("portfoliosim: protocol hash mismatch")
	}
	if c.HistoryDataHash != g5protocol.HistoryDataHash {
		return errors.New("portfoliosim: history data hash mismatch")
	}
	return nil
}

func writeArtifacts(outputDir string, report ValidationReport, receipt ValidationReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := WriteJSONAtomic(filepath.Join(outputDir, "g5_validation_report.json"), report); err != nil {
		return err
	}
	return WriteJSONAtomic(filepath.Join(outputDir, "validation_receipt.json"), receipt)
}

func sortedTimestamps(rows map[string][]features.DecisionContext) []string {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DefaultRunConfig returns frozen validation config for one profile.
func DefaultRunConfig(profileID, historyRoot, policyDir, featureDir, outputDir string, bundle policy.PolicyBundle) RunConfig {
	return RunConfig{
		SchemaVersion:    SchemaVersionConfig,
		ProfileID:        profileID,
		HistoryRoot:      historyRoot,
		PolicyDir:        policyDir,
		FeatureDir:       featureDir,
		OutputDir:        outputDir,
		Generation:       g5protocol.Generation,
		ProtocolHash:     g5protocol.RouterProtocolHash,
		HistoryDataHash:  g5protocol.HistoryDataHash,
		PolicyHash:       bundle.Policy.PolicyHash,
		FeatureHash:      bundle.Policy.FeatureHash,
		ExperimentHash:   bundle.Policy.ExperimentHash,
	}
}

// Ensure partition constants are referenced.
var _ = outcome.PartitionValidation
