package walkforward

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func hashJSON(payload any) (string, error) {
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildWFReport constructs g5_wf_report.json.
func BuildWFReport(cfg RunConfig, folds []Fold, foldResults []FoldResult, lofo []LOFOResult, checks WFGateChecks, attempts []AttemptSignResult, status, reason string, embargoHours float64) (WFReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReport{}, err
	}
	report := WFReport{
		SchemaVersion:        SchemaVersionWFReport,
		Generation:           g5protocol.Generation,
		ProtocolHash:         g5protocol.RouterProtocolHash,
		HistoryDataHash:      g5protocol.HistoryDataHash,
		ProfileID:            cfg.ProfileID,
		PolicyHash:           cfg.PolicyHash,
		FeatureHash:          cfg.FeatureHash,
		ExperimentHash:       cfg.ExperimentHash,
		ValidationReportHash: cfg.ValidationReportHash,
		ConfigHash:           cfgHash,
		Status:               status,
		FailReason:           reason,
		FoldCount:            len(folds),
		PurgeEmbargoHours:    embargoHours,
		Folds:                folds,
		FoldResults:          foldResults,
		LOFOResults:          lofo,
		GateChecks:           checks,
		AttemptSignResults:   attempts,
		ExamActions:          g5protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashWFReport(report)
	if err != nil {
		return WFReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func hashWFReport(r WFReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	return hashJSON(payload)
}

// BuildResearchSummary constructs g5_research_summary.json.
func BuildResearchSummary(validation portfoliosim.ValidationReport, wf WFReport, mechanism MechanismReport) (ResearchSummary, error) {
	summary := ResearchSummary{
		SchemaVersion:        SchemaVersionResearchSummary,
		Generation:           g5protocol.Generation,
		ProtocolHash:         g5protocol.RouterProtocolHash,
		PolicyHash:           wf.PolicyHash,
		ValidationStatus:     validation.Status,
		ValidationReportHash: validation.ReportHash,
		WFStatus:             wf.Status,
		WFReportHash:         wf.ReportHash,
		MechanismStatus:      mechanism.MechanismStatus,
		MechanismReportHash:  mechanism.ReportHash,
		TradeAllowed:         mechanism.TradeAllowed,
		ExamEligible:         mechanism.ExamEligible,
		Certified:            mechanism.Certified,
		ExamActions:          g5protocol.ExamActions,
	}
	hash, err := hashResearchSummary(summary)
	if err != nil {
		return ResearchSummary{}, err
	}
	summary.SummaryHash = hash
	return summary, nil
}

func hashResearchSummary(s ResearchSummary) (string, error) {
	payload := s
	payload.SummaryHash = ""
	return hashJSON(payload)
}

// BuildReceipt constructs wf receipt with provenance hashes.
func BuildReceipt(
	cfg RunConfig,
	wfReport WFReport,
	mechanism MechanismReport,
	summary ResearchSummary,
	featureManifest features.ContextManifest,
	policyManifest reverselearn.PolicyManifest,
	canonical map[string]string,
) (WFReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReceipt{}, err
	}
	receipt := WFReceipt{
		SchemaVersion:        SchemaVersionWFReceipt,
		Generation:           g5protocol.Generation,
		ProtocolHash:         g5protocol.RouterProtocolHash,
		HistoryDataHash:      g5protocol.HistoryDataHash,
		PolicyHash:           cfg.PolicyHash,
		FeatureHash:          cfg.FeatureHash,
		ExperimentHash:       cfg.ExperimentHash,
		ValidationReportHash: cfg.ValidationReportHash,
		FeatureManifestHash:  featureManifest.ManifestHash,
		PolicyManifestHash:   policyManifest.ManifestHash,
		ConfigHash:           cfgHash,
		WFReportHash:         wfReport.ReportHash,
		MechanismReportHash:  mechanism.ReportHash,
		ResearchSummaryHash:  summary.SummaryHash,
		CanonicalDataHashes:  canonical,
		ExamActions:          g5protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashReceipt(receipt)
	if err != nil {
		return WFReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

func hashReceipt(r WFReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	return hashJSON(payload)
}

// WriteJSONAtomic writes one json artifact atomically.
func WriteJSONAtomic(path string, payload any) error {
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

func writeArtifacts(outputDir string, wf WFReport, mechanism MechanismReport, summary ResearchSummary, receipt WFReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := WriteJSONAtomic(filepath.Join(outputDir, "g5_wf_report.json"), wf); err != nil {
		return err
	}
	if err := WriteJSONAtomic(filepath.Join(outputDir, "g5_policy_mechanism_report.json"), mechanism); err != nil {
		return err
	}
	if err := WriteJSONAtomic(filepath.Join(outputDir, "g5_research_summary.json"), summary); err != nil {
		return err
	}
	return WriteJSONAtomic(filepath.Join(outputDir, "wf_receipt.json"), receipt)
}

// BuildSkippedReport writes a SKIPPED_VALIDATION_FAIL wf report without replay.
func BuildSkippedReport(cfg RunConfig, reason string, validation portfoliosim.ValidationReport) (WFReport, MechanismReport, ResearchSummary, error) {
	cfg.ValidationReportHash = validation.ReportHash
	cfg.PolicyHash = validation.PolicyHash
	cfg.FeatureHash = validation.FeatureHash
	cfg.ExperimentHash = validation.ExperimentHash
	wf, err := BuildWFReport(cfg, nil, nil, nil, WFGateChecks{}, nil, StatusSkippedValidationFail, reason, 0)
	if err != nil {
		return WFReport{}, MechanismReport{}, ResearchSummary{}, err
	}
	mechanism := BuildMechanismReport(wf)
	summary, err := BuildResearchSummary(validation, wf, mechanism)
	if err != nil {
		return WFReport{}, MechanismReport{}, ResearchSummary{}, err
	}
	return wf, mechanism, summary, nil
}
