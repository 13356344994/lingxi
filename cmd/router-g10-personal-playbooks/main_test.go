package main

import (
	"strings"
	"testing"

	"lingxi/internal/routerg10/personalplaybook"
)

func TestCLIRequiresReverseTapesDir(t *testing.T) {
	err := run([]string{
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--playbook-limit", "8",
	})
	if err == nil || !strings.Contains(err.Error(), "reverse-tapes-dir") {
		t.Fatalf("expected reverse-tapes-dir error, got %v", err)
	}
}

func TestCLIPlaybookLimit64(t *testing.T) {
	err := run([]string{
		"--reverse-tapes-dir", "/tmp/tapes", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json", "--playbook-limit", "65",
	})
	if err == nil || !strings.Contains(err.Error(), "playbook-limit") {
		t.Fatalf("expected playbook-limit error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--reverse-tapes-dir", "/tmp/tapes", "--output-dir", t.TempDir(),
		"--checkpoint", "/tmp/cp.json", "--playbook-limit", "8", "--low-cpu=false",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("low-cpu flag not recognized: %v", err)
	}
}

func TestCLIPackageCap64(t *testing.T) {
	if personalplaybook.MaxPlaybookCandidates != 64 {
		t.Fatalf("protocol cap drift: %d", personalplaybook.MaxPlaybookCandidates)
	}
}
