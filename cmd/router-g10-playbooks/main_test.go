package main

import (
	"strings"
	"testing"

	"lingxi/internal/routerg10/playbook"
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

func TestCLIRequiresResidualMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--max-files", "10", "--max-lines", "100", "--playbook-limit", "8",
	})
	if err == nil || !strings.Contains(err.Error(), "residual-map-dir") {
		t.Fatalf("expected residual-map-dir error, got %v", err)
	}
}

func TestCLIPlaybookLimit64(t *testing.T) {
	if playbook.MaxPlaybookCandidates != 64 {
		t.Fatalf("protocol cap drift: %d", playbook.MaxPlaybookCandidates)
	}
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--max-files", "10", "--max-lines", "100", "--playbook-limit", "65",
		"--residual-map-dir", "/tmp/map",
	})
	if err == nil || !strings.Contains(err.Error(), "playbook-limit") {
		t.Fatalf("expected playbook-limit error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--catalog", "missing.json", "--history-root", "/tmp",
		"--output-dir", t.TempDir(), "--checkpoint", "/tmp/cp.json",
		"--max-files", "1", "--max-lines", "100", "--playbook-limit", "8",
		"--residual-map-dir", "/tmp/map", "--low-cpu=false",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("low-cpu flag not recognized: %v", err)
	}
}
