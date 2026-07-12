package walkforward

import (
	"time"

	g5protocol "lingxi/internal/routerg5/protocol"
)

// BuildMechanismReport constructs g5_policy_mechanism_report.json.
// G5 has no independent forced-flow/counterparty evidence path; WF pass caps at UNEXPLAINED_POLICY_PASS.
func BuildMechanismReport(wfReport WFReport) MechanismReport {
	status := MechanismUnexplainedPolicyPass
	if wfReport.Status != StatusWFPass {
		status = wfReport.Status
	}
	report := MechanismReport{
		SchemaVersion:        SchemaVersionMechanismReport,
		Generation:           g5protocol.Generation,
		ProtocolHash:         g5protocol.RouterProtocolHash,
		PolicyHash:           wfReport.PolicyHash,
		WFReportHash:         wfReport.ReportHash,
		MechanismStatus:      status,
		ForcedFlowEvidence:   false,
		CounterpartyEvidence: false,
		AutoFromFeatures:     false,
		TradeAllowed:         false,
		ExamEligible:         false,
		Certified:            false,
		ExamActions:          g5protocol.ExamActions,
		CreatedAt:            time.Now().UTC(),
	}
	hash, err := hashMechanismReport(report)
	if err == nil {
		report.ReportHash = hash
	}
	return report
}

// DecideMechanismFromFeatures is forbidden — always returns unexplained without auto attribution.
func DecideMechanismFromFeatures(featureKeys []string) string {
	_ = featureKeys
	return MechanismUnexplainedPolicyPass
}

func hashMechanismReport(r MechanismReport) (string, error) {
	payload := r
	payload.ReportHash = ""
	hash, err := hashJSON(payload)
	return hash, err
}
