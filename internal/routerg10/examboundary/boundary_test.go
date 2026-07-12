package examboundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/walkforward"
)

func sealWFReport(t *testing.T, wf walkforward.WFReport) walkforward.WFReport {
	t.Helper()
	payload := wf
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	wf.ReportHash = hash
	return wf
}

func sealMechanismReport(t *testing.T, mech walkforward.MechanismReport) walkforward.MechanismReport {
	t.Helper()
	payload := mech
	payload.ReportHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	mech.ReportHash = hash
	return mech
}

func writeSealedResearchReports(t *testing.T, dir string, wf walkforward.WFReport, mech walkforward.MechanismReport) (wfPath, mechPath string) {
	t.Helper()
	wf = sealWFReport(t, wf)
	mech.WFReportHash = wf.ReportHash
	mech = sealMechanismReport(t, mech)
	wfPath = filepath.Join(dir, "wf.json")
	mechPath = filepath.Join(dir, "mech.json")
	writeJSON(t, wfPath, wf)
	writeJSON(t, mechPath, mech)
	return wfPath, mechPath
}

func baseWF(status string, brainClear bool) walkforward.WFReport {
	return walkforward.WFReport{
		SchemaVersion: walkforward.SchemaVersionWFReport, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, Status: status,
		BrainDegeneracyClear: brainClear, ExamActions: 0, CreatedAt: time.Now().UTC(),
	}
}

func baseMechanism(status string, eligible, brainClear bool) walkforward.MechanismReport {
	return walkforward.MechanismReport{
		SchemaVersion: walkforward.SchemaVersionMechanismReport, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, MechanismStatus: status,
		ForcedFlowEvidence: status == "EXPLAINED_FORCED_FLOW", CounterpartyEvidence: status == "EXPLAINED_FORCED_FLOW",
		ExamEligible: eligible, BrainDegeneracyClear: brainClear, ExamActions: 0, CreatedAt: time.Now().UTC(),
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBoundaryExamUnavailableWithoutCommitments(t *testing.T) {
	report, err := VerifyBoundary(VerifyBoundaryInput{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != protocol.ExamUnavailableStatus || report.ExamActions != 0 || report.Available {
		t.Fatalf("expected EXAM_UNAVAILABLE: %+v", report)
	}
	if report.TradeAllowed {
		t.Fatalf("trade_allowed must stay false: %+v", report)
	}
}

func TestVerifyBoundaryNoGoWhenMechanismNotEligible(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath, mechPath := writeSealedResearchReports(t, dir,
		baseWF(walkforward.StatusWFPass, true),
		baseMechanism(walkforward.MechanismUnexplainedPersonalEdge, false, true),
	)
	report, err := VerifyBoundary(VerifyBoundaryInput{
		WFReportPath:        wfPath,
		MechanismReportPath: mechPath,
		CommitmentsPath:     commitments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != protocol.ExamBoundaryNoGoStatus || report.ResearchPass || report.Available {
		t.Fatalf("expected NO_GO: %+v", report)
	}
	if report.ExamActions != 0 || report.TradeAllowed {
		t.Fatalf("exam_actions=0 trade_allowed=false required: %+v", report)
	}
}

func TestVerifyBoundaryNoGoWhenBrainDegenerate(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath, mechPath := writeSealedResearchReports(t, dir,
		baseWF(walkforward.StatusWFPass, false),
		baseMechanism("EXPLAINED_FORCED_FLOW", true, true),
	)
	report, err := VerifyBoundary(VerifyBoundaryInput{
		WFReportPath:        wfPath,
		MechanismReportPath: mechPath,
		CommitmentsPath:     commitments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != protocol.ExamBoundaryNoGoStatus || report.ResearchPass {
		t.Fatalf("expected NO_GO for brain degeneracy: %+v", report)
	}
}

func TestVerifyBoundaryAvailableWhenResearchPass(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath, mechPath := writeSealedResearchReports(t, dir,
		baseWF(walkforward.StatusWFPass, true),
		baseMechanism("EXPLAINED_FORCED_FLOW", true, true),
	)
	report, err := VerifyBoundary(VerifyBoundaryInput{
		WFReportPath:        wfPath,
		MechanismReportPath: mechPath,
		CommitmentsPath:     commitments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.ResearchPass || report.Status != protocol.ExamBoundaryAvailableStatus || !report.Available {
		t.Fatalf("expected AVAILABLE research pass: %+v", report)
	}
	if report.TradeAllowed {
		t.Fatal("trade_allowed must remain false even when AVAILABLE")
	}
}

func TestVerifyBoundaryFailsTamperedReportHash(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath, mechPath := writeSealedResearchReports(t, dir,
		baseWF(walkforward.StatusWFPass, true),
		baseMechanism("EXPLAINED_FORCED_FLOW", true, true),
	)
	raw, err := os.ReadFile(wfPath)
	if err != nil {
		t.Fatal(err)
	}
	var wf walkforward.WFReport
	if err := json.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	wf.Status = walkforward.StatusWFFail
	wfRaw, _ := json.Marshal(wf)
	if err := os.WriteFile(wfPath, wfRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyBoundary(VerifyBoundaryInput{
		WFReportPath: wfPath, MechanismReportPath: mechPath, CommitmentsPath: commitments,
	})
	if err == nil {
		t.Fatal("tampered wf report hash must fail closed")
	}
}

func writeCommitmentsDoc(t *testing.T, path string) {
	t.Helper()
	doc := Document{
		SchemaVersion: protocol.PublicCommitmentSchemaV1,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Commitments:   syntheticCommitments("boundary"),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBoundaryRejectsHiddenPath(t *testing.T) {
	_, err := VerifyBoundary(VerifyBoundaryInput{CommitmentsPath: "/tmp/hidden-manifest.json"})
	if err == nil {
		t.Fatal("hidden path must be rejected")
	}
}

func syntheticCommitments(prefix string) []PublicCommitment {
	out := make([]PublicCommitment, len(protocol.ExamScheduleTemplate))
	for i, slot := range protocol.ExamScheduleTemplate {
		out[i] = PublicCommitment{
			ID:            prefix + "-paper-" + slot.Family,
			Queue:         slot.Queue,
			Kind:          slot.Kind,
			Family:        slot.Family,
			AlphaSpend:    slot.AlphaSpend,
			DataHash:      prefix + "-data-" + slot.Family,
			ChallengeHash: prefix + "-challenge-" + slot.Family,
			ProtocolHash:  protocol.RouterProtocolHash,
			Status:        "sealed",
		}
	}
	return out
}
