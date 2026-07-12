package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/examboundary"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/walkforward"
)

func captureRun(t *testing.T, args ...string) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := run(args)
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("run %v failed: %v", args, runErr)
	}
	return string(out)
}

func TestCLIVerifyExamUnavailable(t *testing.T) {
	out := captureRun(t, "verify")
	if !strings.Contains(out, protocol.ExamUnavailableStatus) || !strings.Contains(out, `"exam_actions":0`) {
		t.Fatalf("expected EXAM_UNAVAILABLE exam_actions=0: %s", out)
	}
	if !strings.Contains(out, `"trade_allowed":false`) {
		t.Fatalf("trade_allowed must stay false: %s", out)
	}
}

func TestCLIVerifyNoGoWithoutResearchPass(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	doc := examboundary.Document{
		SchemaVersion: protocol.PublicCommitmentSchemaV1,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
	}
	for _, slot := range protocol.ExamScheduleTemplate {
		doc.Commitments = append(doc.Commitments, examboundary.PublicCommitment{
			ID: "cli-" + slot.Family, Queue: slot.Queue, Kind: slot.Kind, Family: slot.Family,
			AlphaSpend: slot.AlphaSpend, DataHash: "d" + slot.Family, ChallengeHash: "c" + slot.Family,
			ProtocolHash: protocol.RouterProtocolHash, Status: "sealed",
		})
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(commitments, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureRun(t, "verify", "--commitments", commitments)
	if !strings.Contains(out, protocol.ExamBoundaryNoGoStatus) {
		t.Fatalf("expected NO_GO: %s", out)
	}
}

func TestCLIRejectsHiddenPath(t *testing.T) {
	if err := run([]string{"verify", "--commitments", "/tmp/hidden-manifest.json"}); err == nil {
		t.Fatal("hidden path must be rejected")
	}
}

func TestCLIVerifyAvailableSynthetic(t *testing.T) {
	dir := t.TempDir()
	commitments := filepath.Join(dir, "commitments.json")
	wfPath := filepath.Join(dir, "wf.json")
	mechPath := filepath.Join(dir, "mech.json")
	doc := examboundary.Document{
		SchemaVersion: protocol.PublicCommitmentSchemaV1,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
	}
	for _, slot := range protocol.ExamScheduleTemplate {
		doc.Commitments = append(doc.Commitments, examboundary.PublicCommitment{
			ID: "ok-" + slot.Family, Queue: slot.Queue, Kind: slot.Kind, Family: slot.Family,
			AlphaSpend: slot.AlphaSpend, DataHash: "d-" + slot.Family, ChallengeHash: "c-" + slot.Family,
			ProtocolHash: protocol.RouterProtocolHash, Status: "sealed",
		})
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(commitments, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wf := sealWF(t, examboundaryDocumentWF())
	mech := sealMech(t, examboundaryDocumentMech(wf.ReportHash))
	wfRaw, _ := json.Marshal(wf)
	if err := os.WriteFile(wfPath, wfRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	mechRaw, _ := json.Marshal(mech)
	if err := os.WriteFile(mechPath, mechRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureRun(t, "verify", "--wf-report", wfPath, "--mechanism-report", mechPath, "--commitments", commitments)
	if !strings.Contains(out, protocol.ExamBoundaryAvailableStatus) || !strings.Contains(out, `"research_pass":true`) {
		t.Fatalf("expected AVAILABLE: %s", out)
	}
	if !strings.Contains(out, `"trade_allowed":false`) {
		t.Fatalf("trade_allowed must remain false: %s", out)
	}
}

func examboundaryDocumentWF() walkforward.WFReport {
	return walkforward.WFReport{
		SchemaVersion: walkforward.SchemaVersionWFReport, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, Status: walkforward.StatusWFPass,
		BrainDegeneracyClear: true, ExamActions: 0, CreatedAt: time.Now().UTC(),
	}
}

func examboundaryDocumentMech(wfHash string) walkforward.MechanismReport {
	return walkforward.MechanismReport{
		SchemaVersion: walkforward.SchemaVersionMechanismReport, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, WFReportHash: wfHash,
		MechanismStatus: "EXPLAINED_FORCED_FLOW", ForcedFlowEvidence: true, CounterpartyEvidence: true,
		ExamEligible: true, BrainDegeneracyClear: true, ExamActions: 0, CreatedAt: time.Now().UTC(),
	}
}

func sealWF(t *testing.T, wf walkforward.WFReport) walkforward.WFReport {
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

func sealMech(t *testing.T, mech walkforward.MechanismReport) walkforward.MechanismReport {
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
