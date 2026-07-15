package pathvalidation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func sampleReport() Report {
	return Report{
		SchemaVersion:          "lingxi.data-driven-router-v3.path-validation.v1",
		GeneratedAt:            "2026-07-15T00:00:00Z",
		SealedManifestPath:     "sealed/discovery-tournament-100000/manifest.json",
		SealedBundleByteHash:   "deadbeef",
		DatasetManifestHash:    "feedface",
		RouterHoldoutUnblinded: false,
		TradeAllowed:           false,
		ValidationFamilySize:   3,
		LeafWFFamilySize:       1,
		Owners: []OwnerReport{
			{
				Owner: "terra", PolicyID: "terra-bounded-tree-d4-b10-l16",
				ReplayFamily: "FULLY_REPLAYABLE_EXHAUSTIVE_PARTITION",
				Paths: []PathVerdict{
					{Owner: "terra", LeafID: "leaf-01", Verdict: VerdictValidatedPath, WFAttempted: true, WFPassed: true},
				},
			},
		},
	}
}

func TestWriteAtomicReportProducesAllFourArtifacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-1")
	if err := WriteAtomicReport(dir, sampleReport()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"validation_receipt.json", "owner_path_verdicts.json", "wf_confirmation.json", "hashes.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing artifact %s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hashes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hashes HashManifest
	if err := json.Unmarshal(raw, &hashes); err != nil {
		t.Fatal(err)
	}
	receiptRaw, err := os.ReadFile(filepath.Join(dir, "validation_receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hashBytes(receiptRaw); got != hashes.ReceiptHash {
		t.Fatalf("receipt hash mismatch: file=%s manifest=%s", got, hashes.ReceiptHash)
	}
	ownerRaw, err := os.ReadFile(filepath.Join(dir, "owner_path_verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hashBytes(ownerRaw); got != hashes.OwnerPathsHash {
		t.Fatalf("owner path verdicts hash mismatch: file=%s manifest=%s", got, hashes.OwnerPathsHash)
	}
}

func TestWriteAtomicReportRefusesToOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-1")
	if err := WriteAtomicReport(dir, sampleReport()); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomicReport(dir, sampleReport()); err == nil {
		t.Fatal("expected a second write to the same output directory to be refused")
	}
}

func TestWriteAtomicReportNeverLeavesAPartialDirOnSuccess(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "run-1")
	if err := WriteAtomicReport(dir, sampleReport()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "run-1" {
			t.Fatalf("unexpected leftover staging entry: %s", e.Name())
		}
	}
}
