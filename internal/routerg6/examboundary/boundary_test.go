package examboundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/walkforward"
)

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
}

func TestVerifyBoundaryNoGoWhenMechanismNotEligible(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath := filepath.Join(dir, "wf.json")
	writeJSON(t, wfPath, walkforward.WFReport{
		SchemaVersion: walkforward.SchemaVersionWFReport,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Status:        walkforward.StatusWFPass,
		ExamActions:   0,
		CreatedAt:     time.Now().UTC(),
	})
	mechPath := filepath.Join(dir, "mech.json")
	writeJSON(t, mechPath, walkforward.MechanismReport{
		SchemaVersion:   walkforward.SchemaVersionMechanismReport,
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		MechanismStatus: walkforward.MechanismUnexplainedEventPass,
		ExamEligible:    false,
		ExamActions:     0,
		CreatedAt:       time.Now().UTC(),
	})
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
	if report.ExamActions != 0 {
		t.Fatalf("exam_actions must stay 0: %+v", report)
	}
}

func TestVerifyBoundaryNoGoWhenSkippedValidationFail(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath := filepath.Join(dir, "wf.json")
	writeJSON(t, wfPath, walkforward.WFReport{
		SchemaVersion: walkforward.SchemaVersionWFReport,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Status:        walkforward.StatusSkippedValidationFail,
		FailReason:    walkforward.ReasonEventCardUnavailable,
		ExamActions:   0,
		CreatedAt:     time.Now().UTC(),
	})
	mechPath := filepath.Join(dir, "mech.json")
	writeJSON(t, mechPath, walkforward.MechanismReport{
		SchemaVersion:   walkforward.SchemaVersionMechanismReport,
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		MechanismStatus: walkforward.MechanismUnexplainedEventPass,
		ExamEligible:    false,
		ExamActions:     0,
		CreatedAt:       time.Now().UTC(),
	})
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
	if report.ExamActions != 0 {
		t.Fatalf("exam_actions must stay 0: %+v", report)
	}
}

func TestVerifyBoundaryAvailableWhenResearchPass(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	writeCommitmentsDoc(t, commitments)
	wfPath := filepath.Join(dir, "wf.json")
	writeJSON(t, wfPath, walkforward.WFReport{
		SchemaVersion: walkforward.SchemaVersionWFReport,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		Status:        walkforward.StatusWFPass,
		ExamActions:   0,
		CreatedAt:     time.Now().UTC(),
	})
	mechPath := filepath.Join(dir, "mech.json")
	writeJSON(t, mechPath, walkforward.MechanismReport{
		SchemaVersion:        walkforward.SchemaVersionMechanismReport,
		Generation:           protocol.Generation,
		ProtocolHash:         protocol.RouterProtocolHash,
		MechanismStatus:      "EXPLAINED_FORCED_FLOW",
		ForcedFlowEvidence:   true,
		CounterpartyEvidence: true,
		ExamEligible:         true,
		ExamActions:          0,
		CreatedAt:            time.Now().UTC(),
	})
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
