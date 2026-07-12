package main

import (
	"strings"
	"testing"

	"lingxi/internal/routerg6/reverseevent"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--max-files", "10", "--max-lines", "100", "--card-limit", "8",
	})
	if err == nil || !strings.Contains(err.Error(), "map-dir") {
		t.Fatalf("expected map-dir error, got %v", err)
	}
}

func TestCLICardLimit256(t *testing.T) {
	if reverseevent.MaxCardCandidates != 256 {
		t.Fatalf("protocol cap drift: %d", reverseevent.MaxCardCandidates)
	}
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--max-files", "10", "--max-lines", "100", "--card-limit", "257",
		"--map-dir", "/tmp/map",
	})
	if err == nil || !strings.Contains(err.Error(), "card-limit") {
		t.Fatalf("expected card-limit error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--catalog", "missing.json", "--history-root", "/tmp",
		"--output-dir", t.TempDir(), "--checkpoint", "/tmp/cp.json",
		"--max-files", "1", "--max-lines", "100", "--card-limit", "8",
		"--map-dir", "/tmp/map", "--low-cpu=false",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("low-cpu flag not recognized: %v", err)
	}
}
