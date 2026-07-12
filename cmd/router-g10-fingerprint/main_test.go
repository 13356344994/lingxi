package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--record-root", "/tmp/records",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "max-lines") {
		t.Fatalf("expected max-lines error, got %v", err)
	}
}

func TestCLIRequiresRecordRoot(t *testing.T) {
	err := run([]string{
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "record-root") {
		t.Fatalf("expected record-root error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--record-root", t.TempDir(),
		"--output-dir", t.TempDir(),
		"--checkpoint", filepath.Join(t.TempDir(), "cp.json"),
		"--max-lines", "100",
		"--low-cpu=false",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
