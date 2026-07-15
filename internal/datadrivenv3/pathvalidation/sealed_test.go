package pathvalidation

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// realRepoRoot resolves the actual repository root from this package's
// location (internal/datadrivenv3/pathvalidation is three levels below it).
func realRepoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Skipf("cannot locate repository root from test working directory: %v", err)
	}
	return abs
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absTarget); err != nil {
		t.Skipf("fixture dependency missing: %v", err)
	}
	if err := os.Symlink(absTarget, link); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// buildSealedFixtureRoot assembles a fixture repository root that reuses the
// real repo's protocol/dataset-manifest/funding/model-submission files via
// symlink (read-only dependencies this test never tampers with) but makes a
// real, independent copy of the sealed 100k tournament directory so tests
// can safely corrupt individual files without touching the real sealed
// artifacts.
func buildSealedFixtureRoot(t *testing.T) string {
	t.Helper()
	real := realRepoRoot(t)
	tmp := t.TempDir()
	dvr := "research/firstbattle/data_driven_router_v3"

	for _, name := range []string{
		"protocol.json", "protocol_g2.json", "protocol_g3.json", "protocol_g4.json",
		"protocol_g5.json", "protocol_g6.json", "protocol_g7.json", "protocol_g8.json",
		"protocol_g9.json", "protocol_g10.json",
		"model_submission_terra.json", "model_submission_opus.json",
		"model_submission_episodic_sonnet.json", "model_submission_grok.json",
	} {
		mustSymlink(t, filepath.Join(real, dvr, name), filepath.Join(tmp, dvr, name))
	}
	mustSymlink(t,
		filepath.Join(real, dvr, "dataset_state_okx_1h_v3/dataset_manifest.json"),
		filepath.Join(tmp, dvr, "dataset_state_okx_1h_v3/dataset_manifest.json"))
	mustSymlink(t, filepath.Join(real, "records/history/okx_funding_v3"), filepath.Join(tmp, "records/history/okx_funding_v3"))
	for _, relative := range []string{
		"internal/datadrivenv3/counterfactual",
		"internal/datadrivenv3/neutralrunner",
		"internal/datadrivenv3/accountstate",
		"cmd/data-driven-v3-tournament",
	} {
		copyTree(t, filepath.Join(real, relative), filepath.Join(tmp, relative))
	}
	if err := copyFile(
		filepath.Join(real, "internal/datadrivenv3/reward_evaluator.go"),
		filepath.Join(tmp, "internal/datadrivenv3/reward_evaluator.go"),
	); err != nil {
		t.Fatal(err)
	}

	copyTree(t, filepath.Join(real, dvr, "sealed/discovery-tournament-100000"), filepath.Join(tmp, dvr, "sealed/discovery-tournament-100000"))
	return tmp
}

func TestVerifySealedAcceptsTheRealSealedTournament(t *testing.T) {
	root := buildSealedFixtureRoot(t)
	binding, err := VerifySealed(root)
	if err != nil {
		t.Fatalf("expected the real sealed tournament to verify cleanly: %v", err)
	}
	if len(binding.Policies) != len(OwnerIDs) {
		t.Fatalf("expected %d owner policies, got %d", len(OwnerIDs), len(binding.Policies))
	}
	for _, owner := range OwnerIDs {
		if _, ok := binding.Policies[owner]; !ok {
			t.Fatalf("missing policy for owner %s", owner)
		}
	}
}

func TestVerifySealedRejectsTamperedPolicy(t *testing.T) {
	root := buildSealedFixtureRoot(t)
	policyPath := filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000/policies/terra.json")
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["policy_id"] = "tampered-policy-id-for-hash-rejection-test"
	tampered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySealed(root); err == nil {
		t.Fatal("expected a tampered policy artifact to be rejected")
	}
}

func TestVerifySealedRejectsTamperedBundleBytes(t *testing.T) {
	root := buildSealedFixtureRoot(t)
	bundlePath := filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000/discovery_label_bundle.jsonl.gz")
	f, err := os.OpenFile(bundlePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("\x00tamper")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySealed(root); err == nil {
		t.Fatal("expected a tampered discovery label bundle to be rejected purely on its byte hash")
	}
}

func TestVerifySealedRejectsWrongExampleCount(t *testing.T) {
	root := buildSealedFixtureRoot(t)
	manifestPath := filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000/manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["examples"] = 99999
	tampered, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySealed(root); err == nil {
		t.Fatal("expected a non-100000 example count to be rejected")
	}
}

func TestVerifySealedRejectsBrokenLedgerChain(t *testing.T) {
	root := buildSealedFixtureRoot(t)
	ledgerPath := filepath.Join(root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000/attempt_ledgers/terra.json")
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Skip("ledger fixture too short for a chain-break test")
	}
	entries[1]["previous_hash"] = "0000000000000000000000000000000000000000000000000000000000000000"
	tampered, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySealed(root); err == nil {
		t.Fatal("expected a broken attempt-ledger hash chain to be rejected")
	}
}
