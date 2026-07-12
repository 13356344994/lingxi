package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIRequiresBounds(t *testing.T) {
	err := run([]string{"--output-dir", t.TempDir(), "--checkpoint", filepath.Join(t.TempDir(), "cp.json")})
	if err == nil {
		t.Fatal("expected max-lines error")
	}
}

func TestCLIFixturesOnly(t *testing.T) {
	out := t.TempDir()
	if err := run([]string{"--fixtures-only", "--output-dir", out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "entry_proof_fixtures.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCLIRunsWithFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "routerg10", "entryproof", "testdata")
	out := t.TempDir()
	err := run([]string{
		"--output-dir", out,
		"--checkpoint", filepath.Join(out, "cp.json"),
		"--max-lines", "100",
		"--triggers", filepath.Join(root, "triggers.jsonl"),
		"--low-cpu=false",
	})
	if err != nil {
		t.Fatal(err)
	}
}
