package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h",
		"--candidate-output", "/tmp/cand", "--output-dir", "/tmp/o",
		"--checkpoint", "/tmp/cp.json", "--map-dir", "/tmp/m",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresCandidateOutput(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h",
		"--output-dir", "/tmp/o", "--checkpoint", "/tmp/cp.json",
		"--map-dir", "/tmp/m", "--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "candidate-output") {
		t.Fatalf("expected candidate-output error, got %v", err)
	}
}
