package walkforward

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10protocol "lingxi/internal/routerg10/protocol"
	g10validation "lingxi/internal/routerg10/validation"
)

func hashJSON(payload any) (string, error) {
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func hashMechanismReport(r MechanismReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	return hashJSON(payload)
}

func hashWFReport(r WFReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	return hashJSON(payload)
}

func hashExecutionReport(r ExecutionReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	return hashJSON(payload)
}

func hashStageReceipt(r StageReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	return hashJSON(payload)
}

func hashWFSummary(s WFSummary) (string, error) {
	payload := s
	payload.SummaryHash = ""
	return hashJSON(payload)
}

// BuildWFReport constructs g10_wf_report.json.
func BuildWFReport(cfg RunConfig, survivors g10validation.ValidationSurvivors, results []PlaybookWFResult, overallStatus, overallReason string) (WFReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReport{}, err
	}
	pass, fail := 0, 0
	brainDegeneracyClear := true
	for _, r := range results {
		if r.BrainDegeneracy.Degenerate {
			brainDegeneracyClear = false
		}
		if r.Status == StatusWFPass {
			pass++
		} else if r.Status == StatusWFFail {
			fail++
		}
	}
	report := WFReport{
		SchemaVersion: SchemaVersionWFReport, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, PlaybookHash: cfg.PlaybookManifestHash,
		ValidationReportHash: cfg.ValidationReportHash, SurvivorsHash: cfg.SurvivorsHash,
		ConfigHash: cfgHash, Status: overallStatus, FailReason: overallReason,
		SurvivorCount: len(survivors.Survivors), PassCount: pass, FailCount: fail,
		Results: results, BrainDegeneracyClear: brainDegeneracyClear,
		ExamActions: g10protocol.ExamActions, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashWFReport(report)
	if err != nil {
		return WFReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

// BuildWFSummary constructs g10_wf_summary.json.
func BuildWFSummary(wf WFReport, mechanism MechanismReport, execution ExecutionReport) WFSummary {
	skipped := 0
	if wf.Status == StatusSkippedValidationFail {
		skipped = wf.SurvivorCount
	}
	summary := WFSummary{
		SchemaVersion: SchemaVersionWFSummary, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, WFReportHash: wf.ReportHash,
		MechanismReportHash: mechanism.ReportHash, ExecutionReportHash: execution.ReportHash,
		Status: wf.Status, MechanismStatus: mechanism.MechanismStatus,
		SurvivorCount: wf.SurvivorCount, PassCount: wf.PassCount, FailCount: wf.FailCount,
		SkippedCount: skipped, CapacityFailRate: execution.CapacityFailRate,
		ExamActions: g10protocol.ExamActions, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashWFSummary(summary)
	if err == nil {
		summary.SummaryHash = hash
	}
	return summary
}

// BuildExecutionReport aggregates execution feasibility counters.
func BuildExecutionReport(wf WFReport, stats ReplayStats) ExecutionReport {
	capRate := 0.0
	if stats.EventsSeen > 0 {
		capRate = float64(stats.SkipCapacity) / float64(stats.EventsSeen)
	}
	report := ExecutionReport{
		SchemaVersion: SchemaVersionExecutionReport, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, WFReportHash: wf.ReportHash,
		EventsSeen: stats.EventsSeen, EventsTraded: stats.EventsTraded, EventsSkipped: stats.EventsSkipped,
		SkipBelowMinNotional: stats.SkipBelowMinNotional, SkipBelowLotFloor: stats.SkipBelowLotFloor,
		SkipCapacity: stats.SkipCapacity, SkipConcurrencyCap: stats.SkipConcurrencyCap,
		CapacityFailRate: capRate, ExamActions: g10protocol.ExamActions, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashExecutionReport(report)
	if err == nil {
		report.ReportHash = hash
	}
	return report
}

// BuildStageReceipt binds provenance for one WF stage run.
func BuildStageReceipt(
	cfg RunConfig,
	manifestHash, playbookHash, experimentHash string,
	mapManifestHashes map[string]string,
	wf WFReport,
	mechanism MechanismReport,
	execution ExecutionReport,
	canonical map[string]string,
) (StageReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return StageReceipt{}, err
	}
	receipt := StageReceipt{
		SchemaVersion: SchemaVersionStageReceipt, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, HistoryDataHash: g10protocol.HistoryDataHash,
		PlaybookManifestHash: manifestHash, PlaybookHash: playbookHash, ExperimentHash: experimentHash,
		ValidationReportHash: cfg.ValidationReportHash, SurvivorsHash: cfg.SurvivorsHash,
		MapManifestHashes: cloneStringMap(mapManifestHashes), ConfigHash: cfgHash,
		WFReportHash: wf.ReportHash, MechanismReportHash: mechanism.ReportHash,
		ExecutionReportHash: execution.ReportHash, CanonicalDataHashes: cloneStringMap(canonical),
		ExamActions: g10protocol.ExamActions, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashStageReceipt(receipt)
	if err != nil {
		return StageReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

// WriteOutputs atomically writes WF artifacts.
func WriteOutputs(outputDir string, wf WFReport, summary WFSummary, mechanism MechanismReport, execution ExecutionReport, receipt StageReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "g10_wf_report.json"), wf); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "g10_wf_summary.json"), summary); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "g10_event_mechanism_report.json"), mechanism); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "g10_execution_report.json"), execution); err != nil {
		return err
	}
	return writeJSON(filepath.Join(outputDir, "g10_wf_stage_receipt.json"), receipt)
}

func writeJSON(path string, payload any) error {
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

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// BuildSkippedReport writes skip artifacts when validation did not pass.
func BuildSkippedReport(cfg RunConfig, reason string, validation g10validation.ValidationReport, survivors g10validation.ValidationSurvivors) (WFReport, WFSummary, MechanismReport, ExecutionReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReport{}, WFSummary{}, MechanismReport{}, ExecutionReport{}, err
	}
	wf := WFReport{
		SchemaVersion: SchemaVersionWFReport, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, PlaybookHash: validation.PlaybookHash,
		ExperimentHash: validation.ExperimentHash, ValidationReportHash: validation.ReportHash,
		SurvivorsHash: survivors.SurvivorsHash, Status: StatusSkippedValidationFail, FailReason: reason,
		SurvivorCount: len(survivors.Survivors), ConfigHash: cfgHash,
		ExamActions: g10protocol.ExamActions, CreatedAt: time.Now().UTC(),
	}
	hash, err := hashWFReport(wf)
	if err != nil {
		return WFReport{}, WFSummary{}, MechanismReport{}, ExecutionReport{}, err
	}
	wf.ReportHash = hash
	mechanism := BuildMechanismReport(wf)
	execution := BuildExecutionReport(wf, ReplayStats{})
	summary := BuildWFSummary(wf, mechanism, execution)
	return wf, summary, mechanism, execution, nil
}

// ValidateWFReport checks report hash integrity.
func ValidateWFReport(wf WFReport) error {
	if wf.SchemaVersion != SchemaVersionWFReport {
		return errors.New("wf report schema mismatch")
	}
	want, err := hashWFReport(wf)
	if err != nil {
		return err
	}
	if wf.ReportHash == "" || wf.ReportHash != want {
		return errors.New("wf report hash mismatch")
	}
	return nil
}

// ValidateMechanismReport checks mechanism report hash.
func ValidateMechanismReport(m MechanismReport) error {
	want, err := hashMechanismReport(m)
	if err != nil {
		return err
	}
	if m.ReportHash == "" || m.ReportHash != want {
		return errors.New("mechanism report hash mismatch")
	}
	return nil
}

// ValidateStageReceipt checks stage receipt hash integrity.
func ValidateStageReceipt(r StageReceipt) error {
	if r.SchemaVersion != SchemaVersionStageReceipt {
		return errors.New("stage receipt schema mismatch")
	}
	want, err := hashStageReceipt(r)
	if err != nil {
		return err
	}
	if r.ReceiptHash == "" || r.ReceiptHash != want {
		return errors.New("stage receipt hash mismatch")
	}
	return nil
}
