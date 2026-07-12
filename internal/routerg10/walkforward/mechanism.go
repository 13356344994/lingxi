package walkforward

import (
	"time"

	g10protocol "lingxi/internal/routerg10/protocol"
)

// BuildMechanismReport constructs g10_event_mechanism_report.json.
func BuildMechanismReport(wfReport WFReport) MechanismReport {
	status := MechanismUnexplainedPersonalEdge
	if wfReport.Status != StatusWFPass {
		status = wfReport.Status
	}
	report := MechanismReport{
		SchemaVersion: SchemaVersionMechanismReport, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, PlaybookHash: wfReport.PlaybookHash,
		WFReportHash: wfReport.ReportHash, MechanismStatus: status,
		ForcedFlowEvidence: false, CounterpartyEvidence: false, AutoFromFeatures: false,
		TradeAllowed: false, ExamEligible: false, Certified: false,
		BrainDegeneracyClear: wfReport.BrainDegeneracyClear,
		ExamActions: g10protocol.ExamActions,
		CreatedAt: time.Now().UTC(),
	}
	hash, err := hashMechanismReport(report)
	if err == nil {
		report.ReportHash = hash
	}
	return report
}
