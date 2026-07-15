package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCLITruncatedSmokeJSONAndSaltResume(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	records := filepath.Join(root, "records", "okx_directional")
	if err := os.MkdirAll(records, 0o755); err != nil {
		t.Fatal(err)
	}
	input := "{\"position_id\":\"p1\",\"fee_usdt\":1,\"notional\":100}\n" +
		"{\"position_id\":\"p2\",\"fee_usdt\":1,\"notional\":100}\n"
	if err := os.WriteFile(filepath.Join(records, "settlements.jsonl"), []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	protocol, err := filepath.Abs(filepath.Join("..", "..", "research", "firstbattle", "g10_execution_evidence_audit_preregistration.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(out, "checkpoint.external.json")
	args := []string{
		"--repo-root", root, "--output-dir", out, "--checkpoint", checkpoint,
		"--protocol", protocol, "--max-prefix-bytes-per-source", "70", "--low-cpu=true",
	}
	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	runErr := run(args)
	_ = write.Close()
	os.Stdout = stdout
	if runErr != nil {
		t.Fatal(runErr)
	}
	var response map[string]any
	if err := json.NewDecoder(read).Decode(&response); err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	if response["scope_status"] != "TRUNCATED_SMOKE" {
		t.Fatalf("bad JSON response %#v", response)
	}
	raw, err := os.ReadFile(filepath.Join(out, "audit_summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsAny(string(raw), "audit_salt", "position_id", "p1") {
		t.Fatal("salt or raw identity leaked to final artifact")
	}
	// Completed resume must not require the cleaned staging salt.
	if err := run(args); err != nil {
		t.Fatalf("completed resume failed: %v", err)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) > 0 && len(value) >= len(needle) {
			for i := 0; i+len(needle) <= len(value); i++ {
				if value[i:i+len(needle)] == needle {
					return true
				}
			}
		}
	}
	return false
}
