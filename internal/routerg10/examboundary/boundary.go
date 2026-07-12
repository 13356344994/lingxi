package examboundary

import (
	"fmt"
	"strings"

	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/walkforward"
)

// BoundaryReport is the public exam-boundary status visible to researchers.
type BoundaryReport struct {
	Generation           string `json:"generation"`
	ProtocolHash         string `json:"protocol_hash"`
	Status               string `json:"status"`
	ExamActions          int    `json:"exam_actions"`
	ResearchPass         bool   `json:"research_pass"`
	WFStatus             string `json:"wf_status,omitempty"`
	MechanismStatus      string `json:"mechanism_status,omitempty"`
	ExamEligible         bool   `json:"exam_eligible"`
	BrainDegeneracyClear bool   `json:"brain_degeneracy_clear"`
	CommitmentsOK        bool   `json:"commitments_ok"`
	PaperCount           int    `json:"paper_count"`
	Available            bool   `json:"available"`
	TradeAllowed         bool   `json:"trade_allowed"`
	Reason               string `json:"reason,omitempty"`
}

// VerifyBoundary checks WF/mechanism eligibility and public commitments without
// reading hidden manifests, fixture bytes, or sealed exam material.
func VerifyBoundary(in VerifyBoundaryInput) (BoundaryReport, error) {
	report := BoundaryReport{
		Generation:   protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash,
		ExamActions:  protocol.ExamActions,
		Status:       protocol.ExamBoundaryNoGoStatus,
		TradeAllowed: protocol.MechanismTradeAllowedDefault,
	}
	for _, path := range []string{in.WFReportPath, in.MechanismReportPath, in.StageReceiptPath, in.CommitmentsPath, in.StatePath} {
		if strings.TrimSpace(path) != "" {
			if err := rejectHiddenManifestPath(path); err != nil {
				return report, err
			}
		}
	}

	wf, wfOK, err := loadWFReportFull(in.WFReportPath)
	if err != nil {
		return report, err
	}
	mech, mechOK, err := loadMechanismReportFull(in.MechanismReportPath)
	if err != nil {
		return report, err
	}
	receipt, receiptOK, err := loadStageReceipt(in.StageReceiptPath)
	if err != nil {
		return report, err
	}
	if wfOK || mechOK || receiptOK {
		if !wfOK || !mechOK {
			return report, fmt.Errorf("%w: wf and mechanism reports required together", ErrArtifactBinding)
		}
		if err := validateResearchArtifacts(wf, mech, receipt, receiptOK); err != nil {
			return report, err
		}
	}
	if wfOK {
		report.WFStatus = wf.Status
		report.BrainDegeneracyClear = wf.BrainDegeneracyClear
	}
	if mechOK {
		report.MechanismStatus = mech.MechanismStatus
		report.ExamEligible = mech.ExamEligible
		if mech.BrainDegeneracyClear {
			report.BrainDegeneracyClear = mech.BrainDegeneracyClear
		}
	}
	report.ResearchPass = researchPass(wfOK, wf, mechOK, mech)

	commitReport, err := Verify(in.CommitmentsPath, in.StatePath)
	if err != nil {
		return report, err
	}
	report.CommitmentsOK = commitReport.Available
	report.PaperCount = commitReport.PaperCount

	if !commitReport.Available {
		report.Status = protocol.ExamUnavailableStatus
		report.Reason = firstNonEmpty(commitReport.Reason, "commitments unavailable")
		return report, nil
	}
	if !report.ResearchPass {
		report.Status = protocol.ExamBoundaryNoGoStatus
		report.Reason = researchFailReason(wfOK, wf, mechOK, mech)
		return report, nil
	}
	report.Status = protocol.ExamBoundaryAvailableStatus
	report.Available = true
	return report, nil
}

func researchPass(wfOK bool, wf walkforward.WFReport, mechOK bool, mech walkforward.MechanismReport) bool {
	if !wfOK || wf.Status != walkforward.StatusWFPass {
		return false
	}
	if !wf.BrainDegeneracyClear {
		return false
	}
	if !mechOK {
		return false
	}
	if mech.MechanismStatus == walkforward.MechanismUnexplainedPersonalEdge {
		return false
	}
	if !mech.ExamEligible {
		return false
	}
	if !mech.ForcedFlowEvidence || !mech.CounterpartyEvidence {
		return false
	}
	if !mech.BrainDegeneracyClear {
		return false
	}
	return true
}

func researchFailReason(wfOK bool, wf walkforward.WFReport, mechOK bool, mech walkforward.MechanismReport) string {
	if !wfOK {
		return "wf report missing"
	}
	if wf.Status != walkforward.StatusWFPass {
		return fmt.Sprintf("wf status %q requires %q for %s", wf.Status, walkforward.StatusWFPass, protocol.ExamBoundaryResearchPassStatus)
	}
	if !wf.BrainDegeneracyClear {
		return "brain degeneracy not clear"
	}
	if !mechOK {
		return "mechanism report missing"
	}
	if mech.MechanismStatus == walkforward.MechanismUnexplainedPersonalEdge {
		return "mechanism unexplained personal edge"
	}
	if !mech.ExamEligible {
		return "brain not exam eligible"
	}
	if !mech.ForcedFlowEvidence || !mech.CounterpartyEvidence {
		return "mechanism evidence incomplete"
	}
	if !mech.BrainDegeneracyClear {
		return "brain degeneracy not clear"
	}
	return "research pass requirements not met"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
