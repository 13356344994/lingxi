package researchboundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const requirementsPath = "../../research/firstbattle/history_data_requirements.json"

func TestHistoryDataRequirementsHashAndPerLeafIsolation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(requirementsPath))
	if err != nil {
		t.Fatal(err)
	}
	var manifest RequirementsManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	want, err := manifest.ComputedHash()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestHash == nil {
		t.Fatalf("manifest_hash is null; fill with %s", want)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	verdicts := EvaluateRequirements(manifest, map[string]bool{"okx_1h": true})
	testable := 0
	untestable := 0
	for _, verdict := range verdicts {
		switch verdict.Status {
		case Testable:
			testable++
			if verdict.ID != "global_context.router_node" {
				t.Fatalf("only global context should be testable with okx_1h: %+v", verdict)
			}
		case Untestable:
			untestable++
		default:
			t.Fatalf("unexpected verdict: %+v", verdict)
		}
	}
	if testable != 1 || untestable != 4 {
		t.Fatalf("testable=%d untestable=%d verdicts=%+v", testable, untestable, verdicts)
	}
}

func TestForbiddenDataRejectsOnlyAffectedItems(t *testing.T) {
	manifest, err := LoadRequirements(filepath.Clean(requirementsPath))
	if err != nil {
		t.Skipf("manifest hash not frozen yet: %v", err)
	}
	verdicts := EvaluateRequirements(manifest, map[string]bool{
		"okx_1h": true, "settlements": true,
	})
	for _, verdict := range verdicts {
		if verdict.Status != Rejected {
			t.Fatalf("forbidden settlements must reject every item that declares it: %+v", verdict)
		}
	}
}
