package walkforward

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

// Runner orchestrates bounded G5 walk-forward replay.
type Runner struct {
	Config         RunConfig
	Catalog        historydata.HistoryCatalog
	CheckpointPath string
}

// RunResult summarizes one WF run.
type RunResult struct {
	WFReport         WFReport
	MechanismReport  MechanismReport
	ResearchSummary  ResearchSummary
	Receipt          WFReceipt
	ValidationReport portfoliosim.ValidationReport
}

// Run executes walk-forward folds and writes artifacts.
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
	cfg := r.Config
	if reverselearn.IsNoFrozenPolicy(policyBundle.PolicyManifest) {
		cfg.PolicyHash = ""
		cfg.FeatureHash = policyBundle.PolicyManifest.FeatureHash
		cfg.ExperimentHash = policyBundle.PolicyManifest.ExperimentHash
	} else {
		cfg.PolicyHash = policyBundle.Policy.PolicyHash
		cfg.FeatureHash = policyBundle.Policy.FeatureHash
		cfg.ExperimentHash = policyBundle.Policy.ExperimentHash
	}
	validationReport, validationErr := VerifyValidationReport(
		r.Config.ValidationDir,
		cfg.PolicyHash,
		cfg.FeatureHash,
		cfg.ExperimentHash,
	)
	if validationErr != nil {
		reason := ReasonValidationNotPass
		if validationReport.ReportHash != "" {
			switch validationReport.Status {
			case portfoliosim.StatusSkippedPolicyUnavailable:
				reason = ReasonPolicyUnavailable
			default:
				reason = ReasonValidationNotPass
			}
		} else if validationReport.ReportHash == "" {
			reason = ReasonValidationTamper
		}
		wf, mechanism, summary, err := BuildSkippedReport(cfg, reason, validationReport)
		if err != nil {
			return RunResult{}, err
		}
		receipt, err := BuildReceipt(cfg, wf, mechanism, summary, features.ContextManifest{}, policyBundle.PolicyManifest, map[string]string{
			"catalog_hash": r.Catalog.DataHash,
		})
		if err != nil {
			return RunResult{}, err
		}
		if err := writeArtifacts(cfg.OutputDir, wf, mechanism, summary, receipt); err != nil {
			return RunResult{}, err
		}
		return RunResult{WFReport: wf, MechanismReport: mechanism, ResearchSummary: summary, Receipt: receipt, ValidationReport: validationReport}, nil
	}
	cfg.ValidationReportHash = validationReport.ReportHash

	featureRows, featureManifest, err := LoadWalkForwardFeatures(r.Config.FeatureDir)
	if err != nil {
		return RunResult{}, err
	}
	if policyBundle.Policy.FeatureHash != featureManifest.FeatureHash {
		return RunResult{}, errors.New("walkforward: policy/feature hash mismatch")
	}
	timestamps := portfoliosim.FilterTimestamps(featureRows, nil)
	for _, ts := range timestamps {
		if err := RejectNonWalkForward(featureRows[ts]); err != nil {
			return RunResult{}, err
		}
	}
	profile, err := features.ProfileFor(r.Config.ProfileID)
	if err != nil {
		return RunResult{}, err
	}
	embargo := PurgeEmbargoDuration(profile)
	folds, err := BuildFolds(timestamps, g5protocol.WFFoldCount, embargo)
	if err != nil {
		return RunResult{}, err
	}
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
	barsBundle, err := portfoliosim.LoadCanonicalBars(r.Catalog, r.Config.HistoryRoot, r.Config.ProfileID, featureRows, r.Config.MaxLines)
	if err != nil {
		return RunResult{}, err
	}
	engine := portfoliosim.ScenarioEngine{
		Policy:    policyBundle.Policy,
		Bars:      barsBundle,
		Profile:   profile,
		Horizons:  horizons,
		Partition: outcome.PartitionWalkFwd,
	}
	var foldResults []FoldResult
	for _, fold := range folds {
		scenarios := engine.RunScenarios(featureRows, fold.Timestamps)
		foldResults = append(foldResults, SummarizeFoldResult(fold.Index, scenarios))
	}
	lofo := BuildLOFOResults(folds, foldResults)
	allTimestamps := append([]string(nil), timestamps...)
	sort.Strings(allTimestamps)
	daily := engine.RunDailyTracking(featureRows, allTimestamps, g5protocol.ValidationInitialEquities[0])
	attempts := BuildAttemptSignResults(policyBundle.Ledger, policyBundle.Policy.PolicyHash, daily)
	status, checks, reason := EvaluateWFGates(foldResults, lofo, daily, attempts)
	wfReport, err := BuildWFReport(cfg, folds, foldResults, lofo, checks, attempts, status, reason, embargo.Hours())
	if err != nil {
		return RunResult{}, err
	}
	mechanism := BuildMechanismReport(wfReport)
	summary, err := BuildResearchSummary(validationReport, wfReport, mechanism)
	if err != nil {
		return RunResult{}, err
	}
	receipt, err := BuildReceipt(cfg, wfReport, mechanism, summary, featureManifest, policyBundle.PolicyManifest, map[string]string{
		"catalog_hash":          r.Catalog.DataHash,
		"feature_manifest_hash": featureManifest.ManifestHash,
		"policy_manifest_hash":  policyBundle.PolicyManifest.ManifestHash,
	})
	if err != nil {
		return RunResult{}, err
	}
	if err := writeArtifacts(cfg.OutputDir, wfReport, mechanism, summary, receipt); err != nil {
		return RunResult{}, err
	}
	if r.CheckpointPath != "" {
		cfgHash, _ := ConfigFingerprint(cfg)
		cp := Checkpoint{
			Generation:           g5protocol.Generation,
			ProtocolHash:         g5protocol.RouterProtocolHash,
			HistoryDataHash:      g5protocol.HistoryDataHash,
			ConfigHash:           cfgHash,
			PolicyHash:           policyBundle.Policy.PolicyHash,
			FeatureHash:          policyBundle.Policy.FeatureHash,
			ExperimentHash:       policyBundle.Policy.ExperimentHash,
			ValidationReportHash: validationReport.ReportHash,
			FeatureManifestHash:  featureManifest.ManifestHash,
			PolicyManifestHash:   policyBundle.PolicyManifest.ManifestHash,
			OutputDir:            cfg.OutputDir,
			NextFoldIndex:        len(folds),
			Done:                 true,
		}
		if err := SaveCheckpoint(r.CheckpointPath, cp); err != nil {
			return RunResult{}, err
		}
	}
	return RunResult{
		WFReport:         wfReport,
		MechanismReport:  mechanism,
		ResearchSummary:  summary,
		Receipt:          receipt,
		ValidationReport: validationReport,
	}, nil
}

func (c RunConfig) Validate() error {
	if c.ProfileID == "" || c.PolicyDir == "" || c.FeatureDir == "" || c.ValidationDir == "" || c.OutputDir == "" {
		return fmt.Errorf("walkforward: profile, policy-dir, feature-dir, validation-dir, output-dir required")
	}
	if c.ProtocolHash != g5protocol.RouterProtocolHash {
		return errors.New("walkforward: protocol hash mismatch")
	}
	if c.HistoryDataHash != g5protocol.HistoryDataHash {
		return errors.New("walkforward: history data hash mismatch")
	}
	return nil
}

// DefaultRunConfig returns frozen WF config for one profile.
func DefaultRunConfig(profileID, historyRoot, policyDir, featureDir, validationDir, outputDir string, bundle policy.PolicyBundle, validationHash string) RunConfig {
	return RunConfig{
		SchemaVersion:        SchemaVersionConfig,
		ProfileID:            profileID,
		HistoryRoot:          historyRoot,
		PolicyDir:            policyDir,
		FeatureDir:           featureDir,
		ValidationDir:        validationDir,
		OutputDir:            outputDir,
		Generation:           g5protocol.Generation,
		ProtocolHash:         g5protocol.RouterProtocolHash,
		HistoryDataHash:      g5protocol.HistoryDataHash,
		PolicyHash:           bundle.Policy.PolicyHash,
		FeatureHash:          bundle.Policy.FeatureHash,
		ExperimentHash:       bundle.Policy.ExperimentHash,
		ValidationReportHash: validationHash,
	}
}
