package researchboundary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryRegimeContractIsOutcomeFreeAndFrozen(t *testing.T) {
	path := filepath.Clean("../../research/firstbattle/history_regime_contract.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var contract RegimeContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	want, err := contract.ComputedHash()
	if err != nil {
		t.Fatal(err)
	}
	if contract.ManifestHash == nil {
		t.Fatalf("manifest_hash is null; fill with %s", want)
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range contract.ForbiddenInputs {
		if forbidden == "forward_return" || forbidden == "realized_pnl" {
			return
		}
	}
	t.Fatal("regime contract must explicitly forbid outcomes")
}
