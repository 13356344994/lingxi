package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/researchboundary"
)

func TestRunKeepsForcedLiquidationLeavesUntestable(t *testing.T) {
	output := filepath.Join(t.TempDir(), "verdicts.json")
	if err := run([]string{
		"--manifest", "../../research/firstbattle/history_data_requirements.json",
		"--available", "okx_1h,okx_5m,okx_funding,bybit_funding",
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Verdicts []researchboundary.TestabilityVerdict `json:"verdicts"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, verdict := range payload.Verdicts {
		if verdict.ID == "global_context.router_node" {
			if verdict.Status != researchboundary.Testable {
				t.Fatalf("global context should be testable: %+v", verdict)
			}
			continue
		}
		if verdict.Status != researchboundary.Untestable {
			t.Fatalf("forced liquidation leaf must remain UNTESTABLE without historical context: %+v", verdict)
		}
	}
}
