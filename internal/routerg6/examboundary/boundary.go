package examboundary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/walkforward"
)

// BoundaryReport is the public exam-boundary status visible to researchers.
type BoundaryReport struct {
	Generation      string `json:"generation"`
	ProtocolHash    string `json:"protocol_hash"`
	Status          string `json:"status"`
	ExamActions     int    `json:"exam_actions"`
	ResearchPass    bool   `json:"research_pass"`
	WFStatus        string `json:"wf_status,omitempty"`
	MechanismStatus string `json:"mechanism_status,omitempty"`
	ExamEligible    bool   `json:"exam_eligible"`
	CommitmentsOK   bool   `json:"commitments_ok"`
	PaperCount      int    `json:"paper_count"`
	Available       bool   `json:"available"`
	Reason          string `json:"reason,omitempty"`
}

// VerifyBoundaryInput binds public artifacts for boundary verification.
type VerifyBoundaryInput struct {
	WFReportPath        string
	MechanismReportPath string
	CommitmentsPath     string
	StatePath           string
}

// VerifyBoundary checks WF/mechanism eligibility and public commitments without
// reading hidden manifests, fixture bytes, or sealed exam material.
func VerifyBoundary(in VerifyBoundaryInput) (BoundaryReport, error) {
	report := BoundaryReport{
		Generation:   protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash,
		ExamActions:  protocol.ExamActions,
		Status:       protocol.ExamBoundaryNoGoStatus,
	}
	for _, path := range []string{in.WFReportPath, in.MechanismReportPath, in.CommitmentsPath, in.StatePath} {
		if strings.TrimSpace(path) != "" {
			if err := rejectHiddenManifestPath(path); err != nil {
				return report, err
			}
		}
	}

	wf, wfOK, err := loadWFReport(in.WFReportPath)
	if err != nil {
		return report, err
	}
	mech, mechOK, err := loadMechanismReport(in.MechanismReportPath)
	if err != nil {
		return report, err
	}
	if wfOK {
		report.WFStatus = wf.Status
	}
	if mechOK {
		report.MechanismStatus = mech.MechanismStatus
		report.ExamEligible = mech.ExamEligible
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
	if !mechOK {
		return false
	}
	if mech.MechanismStatus == walkforward.MechanismUnexplainedEventPass {
		return false
	}
	if !mech.ExamEligible {
		return false
	}
	if !mech.ForcedFlowEvidence || !mech.CounterpartyEvidence {
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
	if !mechOK {
		return "mechanism report missing"
	}
	if mech.MechanismStatus == walkforward.MechanismUnexplainedEventPass {
		return "mechanism unexplained"
	}
	if !mech.ExamEligible {
		return "event card not exam eligible"
	}
	if !mech.ForcedFlowEvidence || !mech.CounterpartyEvidence {
		return "mechanism evidence incomplete"
	}
	return "research pass requirements not met"
}

func loadWFReport(path string) (walkforward.WFReport, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return walkforward.WFReport{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return walkforward.WFReport{}, false, nil
		}
		return walkforward.WFReport{}, false, err
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		return walkforward.WFReport{}, false, err
	}
	var wf walkforward.WFReport
	if err := json.Unmarshal(raw, &wf); err != nil {
		return walkforward.WFReport{}, false, fmt.Errorf("wf report decode: %w", err)
	}
	if wf.ProtocolHash != "" && wf.ProtocolHash != protocol.RouterProtocolHash {
		return walkforward.WFReport{}, false, errors.New("wf report protocol hash mismatch")
	}
	return wf, true, nil
}

func loadMechanismReport(path string) (walkforward.MechanismReport, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return walkforward.MechanismReport{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return walkforward.MechanismReport{}, false, nil
		}
		return walkforward.MechanismReport{}, false, err
	}
	if err := AssertNoSchemaLeak(raw); err != nil {
		return walkforward.MechanismReport{}, false, err
	}
	var mech walkforward.MechanismReport
	if err := json.Unmarshal(raw, &mech); err != nil {
		return walkforward.MechanismReport{}, false, fmt.Errorf("mechanism report decode: %w", err)
	}
	if mech.ProtocolHash != "" && mech.ProtocolHash != protocol.RouterProtocolHash {
		return walkforward.MechanismReport{}, false, errors.New("mechanism report protocol hash mismatch")
	}
	return mech, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
