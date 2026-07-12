package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunMaterializeShowsProgressAndWritesPartition(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mids.jsonl")
	if err := os.WriteFile(input, []byte(
		`{"recorded_at":"2026-07-11T00:00:00Z","mids":{"BTC-USDT":100}}`+"\n"+
			`{"recorded_at":"2026-07-11T00:01:00Z","mids":{"BTC-USDT":101}}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := run(context.Background(), []string{
		"--mode", "materialize",
		"--kind", "mids",
		"--input", input,
		"--output-dir", filepath.Join(dir, "out"),
		"--date", "2026-07-11",
		"--progress-every", "1",
	}, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Len() == 0 {
		t.Fatal("expected visible progress")
	}
	for _, name := range []string{"mids.jsonl", "mids.manifest.json", "mids.checkpoint.json"} {
		if _, err := os.Stat(filepath.Join(dir, "out", "2026-07-11", name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
}

func TestRunValidateFailsClosedWhenRequiredSourcesMissing(t *testing.T) {
	dir := t.TempDir()
	verdict := filepath.Join(dir, "verdict.json")
	var output bytes.Buffer
	err := run(context.Background(), []string{
		"--mode", "validate",
		"--day-dir", dir,
		"--date", "2026-07-11",
		"--verdict-out", verdict,
	}, &output)
	if err == nil {
		t.Fatal("missing required context sources must fail")
	}
	if _, statErr := os.Stat(verdict); statErr != nil {
		t.Fatalf("fail verdict should still be written: %v", statErr)
	}
}
