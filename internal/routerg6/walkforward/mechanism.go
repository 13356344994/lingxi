package walkforward

import (
	"time"

	"lingxi/internal/routerg6/protocol"
)

// BuildMechanismReport constructs g6_event_mechanism_report.json.
// G6 has no independent forced-flow/counterparty evidence; WF pass caps at UNEXPLAINED_EVENT_PASS.
func BuildMechanismReport(wfReport WFReport) MechanismReport {
	status := MechanismUnexplainedEventPass
	if wfReport.Status != StatusWFPass {
		status = wfReport.Status
	}
	report := MechanismReport{
		SchemaVersion:        SchemaVersionMechanismReport,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		EventCardHash:        wfReport.EventCardHash,
		WFReportHash:         wfReport.ReportHash,
		MechanismStatus:      status,
		ForcedFlowEvidence:   false,
		CounterpartyEvidence: false,
		AutoFromFeatures:     false,
		TradeAllowed:         false,
		ExamEligible:         false,
		Certified:            false,
		ExamActions:          protocol.ExamActions,
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
	return MechanismUnexplainedEventPass
}
