package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFixturesCLI(t *testing.T) {
	out := t.TempDir()
	cp := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := run([]string{"--output-dir", out, "--checkpoint", cp}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "counterfactual_fixtures.json")); err != nil {
		t.Fatalf("fixture file missing: %v", err)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}
}
