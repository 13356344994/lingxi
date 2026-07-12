package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h",
		"--validation-output", "/tmp/val", "--candidate-output", "/tmp/cand",
		"--output-dir", "/tmp/o", "--checkpoint", "/tmp/cp.json", "--map-dir", "/tmp/m",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresValidationOutput(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h",
		"--candidate-output", "/tmp/cand", "--output-dir", "/tmp/o",
		"--checkpoint", "/tmp/cp.json", "--map-dir", "/tmp/m",
		"--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "validation-output") {
		t.Fatalf("expected validation-output error, got %v", err)
	}
}
