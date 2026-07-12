package entryproof

import "testing"

func TestSelfDeclaredGradeIgnored(t *testing.T) {
	tr := DefaultFixtureTriggers()[0].TriggerRecord
	tr.EvidenceGrade = GradeRealFillOrWAL
	tr.ArtifactBinding = ArtifactBinding{MakerArtifactHash: "c3d4e5f6789012345678abcdef012345", DepthArtifactHash: "c3d4e5f6789012345678abcdef012345"}
	ep, err := BuildEntryProof(tr)
	if err != nil {
		t.Fatal(err)
	}
	if ep.EvidenceGrade != GradeMakerAttemptAckCancel {
		t.Fatalf("grade=%s want B from artifact not self-declared A", ep.EvidenceGrade)
	}
}

func TestGradeARequiresSettlementArtifact(t *testing.T) {
	tr := DefaultFixtureTriggers()[1].TriggerRecord
	tr.ArtifactBinding = ArtifactBinding{}
	tr.ExecutableDepthUSDT = 0
	_, err := BuildEntryProof(tr)
	if err == nil {
		t.Fatal("grade A must require verifiable WAL/settlement artifact")
	}
}

func TestGradeCRequiresShadowSource(t *testing.T) {
	tr := DefaultFixtureTriggers()[2].TriggerRecord
	tr.ShadowSourceID = ""
	_, err := BuildEntryProof(tr)
	if err == nil {
		t.Fatal("grade C must bind shadow source")
	}
}

func TestGradeDNotCertified(t *testing.T) {
	tr := TriggerRecord{
		TriggerID: "public_d", DecisionTime: parseTime("2026-07-01T12:00:00Z"),
		Symbol: "BTC-USDT", Venue: "okx", Price: 100000,
		DecisionVisibleInfo: "public_boundary",
		MinSz: "1", LotSz: "0.01", CtVal: "0.01", CtType: "linear",
		ExecutableDepthUSDT: 100,
	}
	ep, err := BuildEntryProof(tr)
	if err != nil {
		t.Fatal(err)
	}
	if ep.CertificationOK {
		t.Fatal("grade D must not certify")
	}
}

func TestCertifiedRequiresArtifactHash(t *testing.T) {
	ep, err := BuildEntryProof(DefaultFixtureTriggers()[1].TriggerRecord)
	if err != nil {
		t.Fatal(err)
	}
	if ep.EvidenceArtifactHash == "" {
		t.Fatal("certified entry must bind artifact hash")
	}
}
