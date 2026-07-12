package walkforward

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
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

// BuildWFSummary constructs g6_wf_summary.json from stage artifacts.
func BuildWFSummary(wf WFReport, mechanism MechanismReport, execution ExecutionReport) WFSummary {
	skipped := 0
	if wf.Status == StatusSkippedValidationFail {
		skipped = wf.SurvivorCount
	}
	summary := WFSummary{
		SchemaVersion:       SchemaVersionWFSummary,
		Generation:          protocol.Generation,
		ProtocolHash:        protocol.RouterProtocolHash,
		WFReportHash:        wf.ReportHash,
		MechanismReportHash: mechanism.ReportHash,
		ExecutionReportHash: execution.ReportHash,
		Status:              wf.Status,
		MechanismStatus:     mechanism.MechanismStatus,
		SurvivorCount:       wf.SurvivorCount,
		PassCount:           wf.PassCount,
		FailCount:           wf.FailCount,
		SkippedCount:        skipped,
		TradeAllowedCount:   0,
		ExamEligibleCount:   0,
		CapacityFailRate:    execution.CapacityFailRate,
		ExamActions:         protocol.ExamActions,
		CreatedAt:           time.Now().UTC(),
	}
	hash, err := hashWFSummary(summary)
	if err == nil {
		summary.SummaryHash = hash
	}
	return summary
}

// BuildWFReport constructs g6_wf_report.json for one WF run.
func BuildWFReport(cfg RunConfig, survivors eventvalidation.ValidationSurvivors, results []CardWFResult, status, reason string) (WFReport, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return WFReport{}, err
	}
	pass, fail := 0, 0
	for _, r := range results {
		switch r.Status {
		case StatusWFPass:
			pass++
		default:
			if r.Status == StatusWFFail || r.Status == StatusInconclusive {
				fail++
			}
		}
	}
	report := WFReport{
		SchemaVersion:        SchemaVersionWFReport,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		EventCardHash:        survivors.ReportHash,
		ValidationReportHash: cfg.ValidationReportHash,
		SurvivorsHash:        survivors.SurvivorsHash,
		ConfigHash:           cfgHash,
		Status:               status,
		FailReason:           reason,
		SurvivorCount:        len(survivors.Survivors),
		PassCount:            pass,
		FailCount:            fail,
		Results:              results,
		ExamActions:          protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashWFReport(report)
	if err != nil {
		return WFReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

// BuildExecutionReport aggregates execution feasibility across card results.
func BuildExecutionReport(wfReport WFReport, stats ReplayStats) ExecutionReport {
	capacityRate := 0.0
	if stats.EventsSeen > 0 {
		capacityRate = float64(stats.SkipCapacity+stats.SkipBelowMinNotional+stats.SkipBelowLotFloor) / float64(stats.EventsSeen)
	}
	report := ExecutionReport{
		SchemaVersion:        SchemaVersionExecutionReport,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		WFReportHash:         wfReport.ReportHash,
		LotNotionalProxyUSDT: LotNotionalProxyUSDT,
		MinNotionalProxyUSDT: MinNotionalProxyUSDT,
		EventsSeen:           stats.EventsSeen,
		EventsTraded:         stats.EventsTraded,
		EventsSkipped:        stats.EventsSkipped,
		SkipBelowMinNotional: stats.SkipBelowMinNotional,
		SkipBelowLotFloor:    stats.SkipBelowLotFloor,
		SkipCapacity:         stats.SkipCapacity,
		SkipConcurrencyCap:   stats.SkipConcurrencyCap,
		CapacityFailRate:     capacityRate,
		ExamActions:          protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashExecutionReport(report)
	if err == nil {
		report.ReportHash = hash
	}
	return report
}

// BuildStageReceipt constructs g6_wf_stage_receipt.json binding provenance.
func BuildStageReceipt(
	cfg RunConfig,
	bundle eventvalidation.CardBundle,
	mapManifestHashes map[string]string,
	wfReport WFReport,
	mechanism MechanismReport,
	execution ExecutionReport,
	canonical map[string]string,
) (StageReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return StageReceipt{}, err
	}
	eventCardHash := bundle.Manifest.EventCardHash
	experimentHash := bundle.Manifest.ExperimentHash
	cardManifestHash := bundle.Manifest.ManifestHash
	if cardManifestHash == "" {
		cardManifestHash = cfg.CardManifestHash
	}
	receipt := StageReceipt{
		SchemaVersion:        SchemaVersionStageReceipt,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		HistoryDataHash:      protocol.HistoryDataHash,
		CardManifestHash:     cardManifestHash,
		EventCardHash:        eventCardHash,
		ExperimentHash:       experimentHash,
		ValidationReportHash: cfg.ValidationReportHash,
		SurvivorsHash:        cfg.SurvivorsHash,
		MapManifestHashes:    cloneStringMap(mapManifestHashes),
		ConfigHash:           cfgHash,
		WFReportHash:         wfReport.ReportHash,
		MechanismReportHash:  mechanism.ReportHash,
		ExecutionReportHash:  execution.ReportHash,
		CanonicalDataHashes:  cloneStringMap(canonical),
		ExamActions:          protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashStageReceipt(receipt)
	if err != nil {
		return StageReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

// WriteOutputs atomically writes WF stage artifacts.
func WriteOutputs(outputDir string, wf WFReport, summary WFSummary, mechanism MechanismReport, execution ExecutionReport, receipt StageReceipt) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_wf_report.json"), wf); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_wf_summary.json"), summary); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_event_mechanism_report.json"), mechanism); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g6_execution_report.json"), execution); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(outputDir, "g6_wf_stage_receipt.json"), receipt)
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

// BuildSkippedReport writes a SKIPPED_VALIDATION_FAIL wf report without replay.
func BuildSkippedReport(
	cfg RunConfig,
	reason string,
	validation eventvalidation.ValidationReport,
	survivors eventvalidation.ValidationSurvivors,
) (WFReport, WFSummary, MechanismReport, ExecutionReport, error) {
	cfg.ValidationReportHash = validation.ReportHash
	cfg.SurvivorsHash = survivors.SurvivorsHash
	wf, err := BuildWFReport(cfg, survivors, nil, StatusSkippedValidationFail, reason)
	if err != nil {
		return WFReport{}, WFSummary{}, MechanismReport{}, ExecutionReport{}, err
	}
	mechanism := BuildMechanismReport(wf)
	execution := BuildExecutionReport(wf, ReplayStats{})
	summary := BuildWFSummary(wf, mechanism, execution)
	return wf, summary, mechanism, execution, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func readJSONFile(dir, name string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeJSON(raw []byte, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	return nil
}

// ValidateWFReport checks report self-hash integrity.
func ValidateWFReport(r WFReport) error {
	want, err := hashWFReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errors.New("walkforward: wf report hash mismatch")
	}
	if r.ExamActions != protocol.ExamActions {
		return errors.New("walkforward: exam_actions must be zero")
	}
	return nil
}

// ValidateMechanismReport checks mechanism report self-hash integrity.
func ValidateMechanismReport(r MechanismReport) error {
	want, err := hashMechanismReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errors.New("walkforward: mechanism report hash mismatch")
	}
	if r.TradeAllowed || r.ExamEligible || r.Certified {
		return errors.New("walkforward: mechanism must not allow trade/exam/certified")
	}
	return nil
}
