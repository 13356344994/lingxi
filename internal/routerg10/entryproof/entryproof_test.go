package entryproof

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDynamicMinNotionalLinear(t *testing.T) {
	got, ct, err := DynamicMinNotionalUSDT("1", "0.01", "linear", 100000)
	if err != nil || ct != CtTypeLinear {
		t.Fatalf("linear: got=%v ct=%s err=%v", got, ct, err)
	}
	if got != 1000 {
		t.Fatalf("dynamic min = %v want 1000", got)
	}
}

func TestDynamicMinNotionalInverseFailClosed(t *testing.T) {
	if _, _, err := DynamicMinNotionalUSDT("1", "100", "inverse", 100000); err == nil {
		t.Fatal("inverse must fail closed")
	}
}

func TestMakerTouchIsNotFill(t *testing.T) {
	tr := TriggerRecord{MakerTouched: true, MakerFilled: false}
	if !MakerTouchIsNotFill(tr) {
		t.Fatal("touch without fill must not count as fill")
	}
}

func TestBuildEntryProofGrades(t *testing.T) {
	for _, tr := range DefaultFixtureTriggers() {
		if tr.TriggerID == "inverse_fail_closed" {
			if _, err := BuildEntryProof(tr.TriggerRecord); err == nil {
				t.Fatal("inverse must fail closed")
			}
			continue
		}
		ep, err := BuildEntryProof(tr.TriggerRecord)
		if err != nil {
			t.Fatalf("%s: %v", tr.TriggerID, err)
		}
		if err := ValidateEntryProof(ep); err != nil {
			t.Fatalf("validate %s: %v", tr.TriggerID, err)
		}
		if ep.Venue != ExecutionVenueOKX {
			t.Fatalf("venue=%s", ep.Venue)
		}
		if ep.EvidenceGrade == GradeExecutableShadowOrTrade && ep.EntryStatus != EntryUnprovenStatus {
			t.Fatal("grade C must be ENTRY_UNPROVEN")
		}
	}
}

func TestRunnerWithFixtures(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		OutputDir:     out,
		Sources:       SourcePaths{Triggers: filepath.Join(root, "triggers.jsonl")},
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proofs) != 3 {
		t.Fatalf("proofs=%d", len(result.Proofs))
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFixturesBundleHash(t *testing.T) {
	bundle, err := WriteFixtures(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if bundle.BundleHash == "" || len(bundle.Proofs) < 2 {
		t.Fatalf("bundle: %+v", bundle)
	}
	if len(bundle.ExitProofs) != len(bundle.Proofs) {
		t.Fatal("exit proofs must pair with entry proofs")
	}
}

func TestExitDepthRequiredForCertification(t *testing.T) {
	tr := DefaultFixtureTriggers()[0]
	ep, err := BuildEntryProof(tr.TriggerRecord)
	if err != nil {
		t.Fatal(err)
	}
	if !ep.CertificationOK {
		t.Fatal("grade B should certify")
	}
	shadow := DefaultFixtureTriggers()[2]
	sep, err := BuildEntryProof(shadow.TriggerRecord)
	if err != nil {
		t.Fatal(err)
	}
	if sep.CertificationOK {
		t.Fatal("grade C must not certify")
	}
}
