package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--archive-root", "/tmp/archive",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "max-lines") {
		t.Fatalf("expected max-lines error, got %v", err)
	}
}

func TestCLIRequiresArchiveRoot(t *testing.T) {
	err := run([]string{
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "archive-root") {
		t.Fatalf("expected archive-root error, got %v", err)
	}
}

func TestCLIRunsWithFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "routerg10", "announcements", "testdata")
	out := t.TempDir()
	err := run([]string{
		"--archive-root", root,
		"--output-dir", out,
		"--checkpoint", filepath.Join(out, "cp.json"),
		"--max-lines", "100",
		"--okx-archive", filepath.Join(root, "multi_venue.jsonl"),
		"--low-cpu=false",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
