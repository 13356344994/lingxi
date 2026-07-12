package personalwalkforward

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

// BuildOutputs constructs WF report, summary, and receipt.
func BuildOutputs(
	cfg RunConfig,
	bundle g10personalvalidation.PlaybookBundle,
	survivors g10personalvalidation.ValidationSurvivors,
	validation g10personalvalidation.ValidationReport,
	results []PlaybookWFResult,
	overallStatus, overallReason string,
) (WFReport, WFSummary, StageReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReport{}, WFSummary{}, StageReceipt{}, err
	}
	pass, fail := 0, 0
	brainClear := true
	for _, r := range results {
		if r.BrainDegeneracy.Degenerate {
			brainClear = false
		}
		switch r.Status {
		case StatusWFPass:
			pass++
		default:
			if r.Status == StatusWFFail {
				fail++
			}
		}
	}
	wf := WFReport{
		SchemaVersion: SchemaVersionWFReport, Generation: Generation, ProtocolHash: ProtocolHash,
		PlaybookHash: bundle.Manifest.PlaybookHash, ExperimentHash: bundle.Manifest.ExperimentHash,
		ValidationReportHash: validation.ReportHash, SurvivorsHash: survivors.SurvivorsHash,
		ConfigHash: cfgHash, Status: overallStatus, FailReason: overallReason,
		SurvivorCount: len(survivors.Survivors), PassCount: pass, FailCount: fail,
		Results: results, BrainDegeneracyClear: brainClear,
		ExamActions: g10protocolv2.ExamActions, TradeAllowed: false, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashWFReport(wf)
	if err != nil {
		return WFReport{}, WFSummary{}, StageReceipt{}, err
	}
	wf.ReportHash = hash
	summary := WFSummary{
		SchemaVersion: SchemaVersionWFSummary, Generation: Generation, ProtocolHash: ProtocolHash,
		WFReportHash: wf.ReportHash, Status: overallStatus, SurvivorCount: len(survivors.Survivors),
		PassCount: pass, FailCount: fail, ExamActions: g10protocolv2.ExamActions,
		TradeAllowed: false, CreatedAt: time.Now().UTC(),
	}
	sumHash, err := hashWFSummary(summary)
	if err != nil {
		return WFReport{}, WFSummary{}, StageReceipt{}, err
	}
	summary.SummaryHash = sumHash
	receipt := StageReceipt{
		SchemaVersion: SchemaVersionStageReceipt, Generation: Generation, ProtocolHash: ProtocolHash,
		HistoryDataHash: cfg.HistoryDataHash, PlaybookManifestHash: cfg.PlaybookManifestHash,
		ValidationReportHash: validation.ReportHash, SurvivorsHash: survivors.SurvivorsHash,
		ConfigHash: cfgHash, WFReportHash: wf.ReportHash,
		CanonicalDataHashes: map[string]string{"catalog_hash": cfg.HistoryDataHash},
		ExamActions: g10protocolv2.ExamActions, TradeAllowed: false, CreatedAt: time.Now().UTC(),
	}
	recHash, err := hashReceipt(receipt)
	if err != nil {
		return WFReport{}, WFSummary{}, StageReceipt{}, err
	}
	receipt.ReceiptHash = recHash
	return wf, summary, receipt, nil
}

// BuildSkippedReport constructs skipped WF artifacts when validation did not pass.
func BuildSkippedReport(
	cfg RunConfig,
	reason string,
	validation g10personalvalidation.ValidationReport,
	survivors g10personalvalidation.ValidationSurvivors,
) (WFReport, WFSummary, StageReceipt, error) {
	status := StatusSkippedValidationFail
	if reason == ReasonValidationTamper {
		status = StatusSkippedValidationFail
	}
	return BuildOutputs(cfg, g10personalvalidation.PlaybookBundle{}, survivors, validation, nil, status, reason)
}

func hashWFReport(r WFReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func hashWFSummary(s WFSummary) (string, error) {
	payload := s
	payload.SummaryHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func hashReceipt(r StageReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

// WriteOutputs atomically writes personal WF artifacts.
func WriteOutputs(outputDir string, wf WFReport, summary WFSummary, receipt StageReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputWFReportFile), wf); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, OutputWFSummaryFile), summary); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(outputDir, OutputReceiptFile), receipt)
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

func loadCompleted(outputDir string, validation g10personalvalidation.ValidationReport, survivors g10personalvalidation.ValidationSurvivors) (RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(outputDir, OutputWFReportFile))
	if err != nil {
		return RunResult{}, err
	}
	var wf WFReport
	if err := json.Unmarshal(raw, &wf); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(outputDir, OutputWFSummaryFile))
	if err != nil {
		return RunResult{}, err
	}
	var summary WFSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return RunResult{}, err
	}
	raw, err = os.ReadFile(filepath.Join(outputDir, OutputReceiptFile))
	if err != nil {
		return RunResult{}, err
	}
	var receipt StageReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return RunResult{}, err
	}
	return RunResult{WFReport: wf, Summary: summary, Receipt: receipt, Validation: validation, Survivors: survivors}, nil
}

// ValidateWFReport checks report hash integrity.
func ValidateWFReport(r WFReport) error {
	if r.SchemaVersion != SchemaVersionWFReport {
		return errors.New("wf report schema mismatch")
	}
	want, err := hashWFReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errors.New("wf report hash mismatch")
	}
	if r.TradeAllowed || r.ExamActions != 0 {
		return errors.New("exam/live must be false")
	}
	return nil
}
