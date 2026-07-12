package mechanism

import (
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/validation"
)

// BuildMechanismReport constructs g4_mechanism_report.json.
func BuildMechanismReport(
	cfg RunConfig,
	validationReport validation.ValidationReport,
	survivors validation.ValidationSurvivors,
	decisions []AttributionDecision,
) (MechanismReport, error) {
	explained, hypothesis, unexplained, ambiguous, graveyard := 0, 0, 0, 0, 0
	for _, d := range decisions {
		switch d.AttributionStatus {
		case protocol.AttributionExplained:
			explained++
		case protocol.AttributionMechanismHypothesis:
			hypothesis++
		case protocol.AttributionUnexplained:
			unexplained++
		case protocol.AttributionAmbiguous:
			ambiguous++
		case protocol.AttributionGraveyard:
			graveyard++
		}
	}
	report := MechanismReport{
		SchemaVersion:           SchemaVersionMechanismReport,
		Generation:              protocol.Generation,
		ProtocolHash:            protocol.RouterProtocolHash,
		ValidationReportHash:    validationReport.ReportHash,
		ValidationSurvivorsHash: survivors.SurvivorsHash,
		CandidateManifestHash:   validationReport.CandidateManifestHash,
		SurvivorCount:           len(decisions),
		ExplainedCount:          explained,
		MechanismHypothesisCount: hypothesis,
		UnexplainedCount:        unexplained,
		AmbiguousCount:          ambiguous,
		GraveyardCount:          graveyard,
		Decisions:               decisions,
		ExamActions:             protocol.ExamActions,
		CreatedAt:               time.Now().UTC(),
	}
	hash, err := hashMechanismReport(report)
	if err != nil {
		return MechanismReport{}, err
	}
	report.ReportHash = hash
	_ = cfg
	return report, nil
}

func hashMechanismReport(r MechanismReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildStrictWFReport constructs g4_strict_wf_report.json.
func BuildStrictWFReport(mechanismReport MechanismReport, results []WFResult) (StrictWFReport, error) {
	pass, closed, inconclusive := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case protocol.StrictWFPassState:
			pass++
		case protocol.StrictWFInsufficientSampleState:
			inconclusive++
		default:
			closed++
		}
	}
	report := StrictWFReport{
		SchemaVersion:         SchemaVersionStrictWFReport,
		Generation:            protocol.Generation,
		ProtocolHash:          protocol.RouterProtocolHash,
		MechanismReportHash:   mechanismReport.ReportHash,
		ValidationReportHash:  mechanismReport.ValidationReportHash,
		CandidateManifestHash: mechanismReport.CandidateManifestHash,
		ExplainedCount:        mechanismReport.ExplainedCount,
		ResearchPassCount:     pass,
		ClosedCount:           closed,
		InconclusiveCount:     inconclusive,
		FamilyAlpha:           protocol.StrictWFFamilyAlpha,
		Results:               results,
		ExamActions:           protocol.ExamActions,
		CreatedAt:             time.Now().UTC(),
	}
	hash, err := hashStrictWFReport(report)
	if err != nil {
		return StrictWFReport{}, err
	}
	report.ReportHash = hash
	return report, nil
}

func hashStrictWFReport(r StrictWFReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildFinalLeaves constructs g4_final_leaves.json.
func BuildFinalLeaves(
	mechanismReport MechanismReport,
	wfReport StrictWFReport,
	items []SurvivorWorkItem,
) (FinalLeaves, error) {
	wfByHash := map[string]WFResult{}
	for _, result := range wfReport.Results {
		wfByHash[result.CanonicalHash] = result
	}
	leaves := make([]FinalLeaf, 0, len(items))
	for _, item := range items {
		leaf := FinalLeaf{
			Ordinal:           item.Survivor.Ordinal,
			CanonicalHash:     item.Survivor.CanonicalHash,
			ProfileID:         item.Survivor.ProfileID,
			CellKey:           item.Survivor.CellKey,
			AttributionStatus: item.Decision.AttributionStatus,
			TemplateID:        item.Decision.TemplateID,
			Mechanism:         item.Decision.Mechanism,
			Payer:             item.Decision.Payer,
			ForcedBehavior:    item.Decision.ForcedBehavior,
			CannotWait:        item.Decision.CannotWait,
			CompetitionDefense: item.Decision.CompetitionDefense,
			Deadline:          item.Decision.Deadline,
			Invalidators:      append([]string(nil), item.Decision.Invalidators...),
			Conditions:        item.Survivor.Conditions,
		}
		switch item.Decision.AttributionStatus {
		case protocol.AttributionExplained:
			wf, ok := wfByHash[item.Survivor.CanonicalHash]
			if ok {
				leaf.WF = &wf
				leaf.Status = wf.Status
				leaf.Reason = wf.Reason
			} else {
				leaf.Status = protocol.StrictWFInsufficientSampleState
				leaf.Reason = "missing_wf_result"
			}
			leaf.TradeAllowed = false
			leaf.ExamAllowed = false
			leaf.Certified = false
		case protocol.AttributionUnexplained:
			leaf.Status = FinalStatusUnexplainedResearchPass
			leaf.Reason = item.Decision.Reason
			leaf.TradeAllowed = false
			leaf.ExamAllowed = false
			leaf.Certified = false
		case protocol.AttributionMechanismHypothesis:
			leaf.Status = protocol.HypothesisResearchResultStatus
			leaf.Reason = item.Decision.Reason
			leaf.TradeAllowed = false
			leaf.ExamAllowed = false
			leaf.Certified = false
		default:
			leaf.Status = FinalStatusClosed
			leaf.Reason = item.Decision.Reason
			leaf.TradeAllowed = false
			leaf.ExamAllowed = false
			leaf.Certified = false
		}
		leaves = append(leaves, leaf)
	}
	out := FinalLeaves{
		SchemaVersion:        SchemaVersionFinalLeaves,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		MechanismReportHash:  mechanismReport.ReportHash,
		StrictWFReportHash:   wfReport.ReportHash,
		ValidationReportHash: mechanismReport.ValidationReportHash,
		Leaves:               leaves,
		ExamActions:          protocol.ExamActions,
	}
	hash, err := hashFinalLeaves(out)
	if err != nil {
		return FinalLeaves{}, err
	}
	out.LeavesHash = hash
	return out, nil
}

func hashFinalLeaves(l FinalLeaves) (string, error) {
	payload := l
	payload.LeavesHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// BuildStageSummary constructs g4_stage_summary.json.
func BuildStageSummary(
	validationBundle ValidationBundle,
	mechanismReport MechanismReport,
	wfReport StrictWFReport,
	finalLeaves FinalLeaves,
) StageSummary {
	unexplainedPass, hypothesisResult, researchPass, closed, inconclusive := 0, 0, 0, 0, 0
	for _, leaf := range finalLeaves.Leaves {
		switch leaf.Status {
		case FinalStatusUnexplainedResearchPass:
			unexplainedPass++
		case protocol.HypothesisResearchResultStatus:
			hypothesisResult++
		case FinalStatusResearchPass:
			researchPass++
		case FinalStatusInconclusive:
			inconclusive++
		default:
			closed++
		}
	}
	systemStatus := "NO_GO"
	if researchPass > 0 {
		systemStatus = "RESEARCH_PASS"
	}
	return StageSummary{
		SchemaVersion:           SchemaVersionStageSummary,
		Generation:              protocol.Generation,
		ProtocolHash:            protocol.RouterProtocolHash,
		ValidationReportHash:    validationBundle.Report.ReportHash,
		ValidationSurvivorsHash: validationBundle.Survivors.SurvivorsHash,
		ValidationReceiptHash:   validationBundle.Receipt.ReceiptHash,
		MechanismReportHash:     mechanismReport.ReportHash,
		StrictWFReportHash:      wfReport.ReportHash,
		FinalLeavesHash:         finalLeaves.LeavesHash,
		SurvivorCount:           mechanismReport.SurvivorCount,
		ExplainedCount:          mechanismReport.ExplainedCount,
		UnexplainedResearchPass: unexplainedPass,
		MechanismHypothesisResult: hypothesisResult,
		ResearchPassLeaves:      researchPass,
		ClosedCount:             closed,
		InconclusiveCount:       inconclusive,
		SystemStatus:            systemStatus,
		ExamActions:             protocol.ExamActions,
	}
}

// BuildReceipt constructs mechanism_receipt.json binding all provenance.
func BuildReceipt(
	cfg RunConfig,
	input InputBundle,
	mechanismReport MechanismReport,
	wfReport StrictWFReport,
	finalLeaves FinalLeaves,
	stageSummary StageSummary,
) (MechanismReceipt, error) {
	cfgHash, err := ConfigFingerprint(cfg)
	if err != nil {
		return MechanismReceipt{}, err
	}
	stageHash, _, err := datatruth.HashJSON(stageSummary)
	if err != nil {
		return MechanismReceipt{}, err
	}
	receipt := MechanismReceipt{
		SchemaVersion:           SchemaVersionReceipt,
		Generation:              protocol.Generation,
		ProtocolHash:            protocol.RouterProtocolHash,
		HistoryDataHash:         protocol.HistoryDataHash,
		ValidationReportHash:    input.Validation.Report.ReportHash,
		ValidationSurvivorsHash: input.Validation.Survivors.SurvivorsHash,
		ValidationReceiptHash:   input.Validation.Receipt.ReceiptHash,
		CandidateManifestHash:   input.Candidates.Manifest.ManifestHash,
		CandidatesHash:          input.Candidates.Manifest.CandidatesHash,
		LedgerHash:              input.Candidates.Manifest.LedgerHash,
		MapManifestHashes:       cloneStringMap(input.Validation.Receipt.MapManifestHashes),
		MapArtifactHashes:       cloneStringMap(input.Validation.Receipt.MapArtifactHashes),
		CatalogHash:             input.Validation.Receipt.CatalogHash,
		ConfigHash:              cfgHash,
		MechanismReportHash:     mechanismReport.ReportHash,
		StrictWFReportHash:      wfReport.ReportHash,
		FinalLeavesHash:         finalLeaves.LeavesHash,
		StageSummaryHash:        stageHash,
		CanonicalDataHashes: map[string]string{
			"catalog_hash":              input.Validation.Receipt.CatalogHash,
			"candidates_hash":           input.Candidates.Manifest.CandidatesHash,
			"ledger_hash":               input.Candidates.Manifest.LedgerHash,
			"candidate_manifest_hash":   input.Candidates.Manifest.ManifestHash,
			"validation_report_hash":    input.Validation.Report.ReportHash,
			"validation_survivors_hash": input.Validation.Survivors.SurvivorsHash,
			"validation_receipt_hash":   input.Validation.Receipt.ReceiptHash,
		},
		ExamActions: protocol.ExamActions,
		CreatedAt:   time.Now().UTC(),
	}
	hash, err := hashReceipt(receipt)
	if err != nil {
		return MechanismReceipt{}, err
	}
	receipt.ReceiptHash = hash
	return receipt, nil
}

func hashReceipt(r MechanismReceipt) (string, error) {
	payload := r
	payload.ReceiptHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// WriteOutputs atomically writes mechanism stage artifacts.
func WriteOutputs(
	outputDir string,
	mechanismReport MechanismReport,
	wfReport StrictWFReport,
	finalLeaves FinalLeaves,
	stageSummary StageSummary,
	receipt MechanismReceipt,
) error {
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_mechanism_report.json"), mechanismReport); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_strict_wf_report.json"), wfReport); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_final_leaves.json"), finalLeaves); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_stage_summary.json"), stageSummary); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(outputDir, "g4_mechanism_receipt.json"), receipt); err != nil {
		return err
	}
	return nil
}

// ValidateMechanismReport checks report self-hash integrity.
func ValidateMechanismReport(r MechanismReport) error {
	want, err := hashMechanismReport(r)
	if err != nil {
		return err
	}
	if r.ReportHash == "" || r.ReportHash != want {
		return errMechanismReportHashMismatch
	}
	if r.ExamActions != protocol.ExamActions {
		return errExamActions
	}
	return nil
}

// ValidateReceipt checks receipt self-hash integrity.
func ValidateReceipt(r MechanismReceipt) error {
	want, err := hashReceipt(r)
	if err != nil {
		return err
	}
	if r.ReceiptHash == "" || r.ReceiptHash != want {
		return errReceiptHashMismatch
	}
	if r.ExamActions != protocol.ExamActions {
		return errExamActions
	}
	return nil
}

var (
	errMechanismReportHashMismatch = datatruthError("mechanism: report hash mismatch")
	errReceiptHashMismatch         = datatruthError("mechanism: receipt hash mismatch")
	errExamActions                   = datatruthError("mechanism: exam_actions must be zero")
)

type datatruthError string

func (e datatruthError) Error() string { return string(e) }

// BuildWFResults evaluates preliminary gates and Holm for explained survivors.
func BuildWFResults(items []SurvivorWorkItem, accumulators map[string]*wfAccumulator) []WFResult {
	results := make([]WFResult, 0)
	for _, item := range items {
		if item.Decision.AttributionStatus != protocol.AttributionExplained {
			continue
		}
		acc := accumulators[item.Survivor.CanonicalHash]
		var stats validation.FiringStats
		var blocks map[string]float64
		if acc != nil {
			stats = acc.finalize()
			blocks = acc.blockMeans()
		}
		checks, preliminary := evaluatePreliminaryWFGates(stats, len(blocks))
		nonzeroBlocks, positiveBlocks := nonzeroBlockSignStats(blocks)
		result := WFResult{
			Ordinal:          item.Survivor.Ordinal,
			CanonicalHash:    item.Survivor.CanonicalHash,
			ProfileID:        item.Survivor.ProfileID,
			Firing:           stats,
			GateChecks:       checks,
			NonzeroBlockCount:  nonzeroBlocks,
			PositiveBlockCount: positiveBlocks,
			TemplateID:       item.Decision.TemplateID,
			Mechanism:        item.Decision.Mechanism,
		}
		if preliminary != "" {
			result.Status = preliminary
			if preliminary == protocol.StrictWFInsufficientSampleState {
				result.Reason = "INSUFFICIENT_WF_SAMPLE"
			} else {
				result.Reason = "WF_GATE_FAILURE"
			}
			results = append(results, result)
			continue
		}
		result.SignPValue = twoSidedBinomialSignPValue(nonzeroBlocks, positiveBlocks)
		results = append(results, result)
	}
	applyHolm(results, protocol.StrictWFFamilyAlpha)
	return results
}
