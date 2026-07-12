package personalvalidation

import (
	"lingxi/internal/datatruth"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
)

// EvaluateLayers builds hierarchical layer verdicts for one playbook.
func EvaluateLayers(
	tiers []TierResult,
	maker MakerPathStats,
	pit PITContaminationReport,
	deg DegeneracySummary,
	leaveOne LeaveOneReport,
	hasTriggers bool,
	events []TriggerEvent,
) []LayerVerdict {
	layers := make([]LayerVerdict, 0, 5)

	histStatus := StatusInconclusive
	histReason := ReasonInsufficientEvidence
	if hasTriggers {
		histStatus = StatusValidationPass
		histReason = ""
	}
	layers = append(layers, LayerVerdict{Layer: LayerHistoricalPhenomenon, Status: histStatus, FailReason: histReason})

	entryStatus := StatusInconclusive
	entryReason := ReasonEntryUnproven
	for _, ev := range events {
		if ev.EntryProofBound && ev.EntryGrade != "" && g10entryproof.CertificationAllowed(ev.EntryGrade) {
			entryStatus = StatusValidationPass
			entryReason = ""
			break
		}
	}
	layers = append(layers, LayerVerdict{Layer: LayerEntryProven, Status: entryStatus, FailReason: entryReason})

	costStatus := StatusInconclusive
	costReason := ReasonInsufficientEvidence
	for _, tr := range tiers {
		if tr.Status == StatusValidationPass {
			costStatus = StatusValidationPass
			costReason = ""
			break
		}
		if tr.Status == StatusValidationFail && tr.FailReason == ReasonCost {
			costStatus = StatusValidationFail
			costReason = ReasonCost
		}
	}
	layers = append(layers, LayerVerdict{Layer: LayerCostBounded, Status: costStatus, FailReason: costReason})

	execStatus := StatusInconclusive
	execReason := ReasonInsufficientEvidence
	if deg.Degenerate {
		execStatus = StatusValidationFail
		execReason = ReasonBrainDegeneracy
	} else if maker.MakerGateForbid {
		execStatus = StatusValidationFail
		execReason = ReasonMakerSelectionBias
	} else if pit.ContaminationDetected {
		execStatus = StatusValidationFail
		execReason = ReasonPITContamination
	} else if !leaveOne.DayLOOOK || !leaveOne.ShockLOOOK {
		execStatus = StatusValidationFail
		execReason = ReasonCoverage
	} else if hasTriggers && executionProofVerified(events) {
		execStatus = StatusValidationPass
		execReason = ""
	} else if hasGradeCEntryOnly(events) {
		execStatus = StatusInconclusive
		execReason = ReasonEntryUnproven
	}
	layers = append(layers, LayerVerdict{
		Layer: LayerPersonalExecutionVerified, Status: execStatus, FailReason: execReason,
	})

	wfStatus := StatusInconclusive
	wfNote := "walk-forward stage pending"
	for _, tr := range tiers {
		if tr.Status == StatusValidationPass {
			wfStatus = StatusInconclusive
			wfNote = "validation pass; WF not yet run"
			break
		}
	}
	layers = append(layers, LayerVerdict{Layer: LayerWFPass, Status: wfStatus, Note: wfNote})
	return layers
}

func executionProofVerified(events []TriggerEvent) bool {
	for _, ev := range events {
		if !ev.EntryProofBound || !ev.ExecutionProofBound || !ev.ExitProofBound {
			continue
		}
		if g10entryproof.CertificationAllowed(ev.EntryGrade) {
			return true
		}
	}
	return false
}

func hasGradeCEntryOnly(events []TriggerEvent) bool {
	hasC := false
	for _, ev := range events {
		if !ev.EntryProofBound {
			continue
		}
		if ev.EntryGrade == g10entryproof.GradeExecutableShadowOrTrade {
			hasC = true
		}
		if g10entryproof.CertificationAllowed(ev.EntryGrade) {
			return false
		}
	}
	return hasC
}

// BuildLayerSummary aggregates layer counts across playbook results.
func BuildLayerSummary(report ValidationReport) LayerSummary {
	byLayer := map[string]int{}
	for _, r := range report.Results {
		for _, lv := range r.Layers {
			if lv.Status == StatusValidationPass {
				byLayer[lv.Layer]++
			}
		}
	}
	summary := LayerSummary{
		SchemaVersion: SchemaVersionLayer,
		Generation:    Generation,
		ProtocolHash:  ProtocolHash,
		ReportHash:    report.ReportHash,
		ByLayer:       byLayer,
		ExamActions:   g10protocolv2.ExamActions,
		TradeAllowed:  false,
	}
	hash, err := hashLayerSummary(summary)
	if err == nil {
		summary.SummaryHash = hash
	}
	return summary
}

func hashLayerSummary(s LayerSummary) (string, error) {
	payload := s
	payload.SummaryHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// UpdateWFLayer marks WF_PASS on validation survivors after walk-forward.
func UpdateWFLayer(layers []LayerVerdict, wfPass bool) []LayerVerdict {
	out := make([]LayerVerdict, len(layers))
	copy(out, layers)
	for i, lv := range out {
		if lv.Layer != LayerWFPass {
			continue
		}
		if wfPass {
			out[i] = LayerVerdict{Layer: LayerWFPass, Status: StatusValidationPass}
		} else {
			out[i] = LayerVerdict{Layer: LayerWFPass, Status: StatusValidationFail, FailReason: ReasonCoverage}
		}
	}
	return out
}
