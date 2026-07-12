package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--card-output-dir", "/tmp/cards", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "card-manifest-hash") {
		t.Fatalf("expected card-manifest-hash error, got %v", err)
	}
}

func TestCLIRequiresMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--card-output-dir", "/tmp/cards", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json", "--card-manifest-hash", "abc",
		"--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "map-dir") {
		t.Fatalf("expected map-dir error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--catalog", "missing.json", "--history-root", "/tmp",
		"--card-output-dir", "/tmp/cards", "--output-dir", t.TempDir(),
		"--checkpoint", "/tmp/cp.json", "--card-manifest-hash", "abc",
		"--max-files", "1", "--max-lines", "100",
		"--map-dir", "/tmp/map", "--low-cpu=false",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("low-cpu flag not recognized: %v", err)
	}
}
