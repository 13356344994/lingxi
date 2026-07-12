package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "playbook-manifest-hash") {
		t.Fatalf("expected playbook-manifest-hash error, got %v", err)
	}
}

func TestCLIRequiresResidualMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json", "--playbook-manifest-hash", "abc",
		"--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "residual-map-dir") {
		t.Fatalf("expected residual-map-dir error, got %v", err)
	}
}

func TestCLIRequiresFingerprintBinding(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json", "--playbook-manifest-hash", "abc",
		"--max-files", "10", "--max-lines", "100",
		"--residual-map-dir", "/tmp/map",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint-path") {
		t.Fatalf("expected fingerprint-path error, got %v", err)
	}
}

func TestCLIRecognizesLowCPU(t *testing.T) {
	err := run([]string{
		"--catalog", "missing.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--output-dir", t.TempDir(),
		"--checkpoint", "/tmp/cp.json", "--playbook-manifest-hash", "abc",
		"--max-files", "1", "--max-lines", "100",
		"--residual-map-dir", "/tmp/map", "--low-cpu=false",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("low-cpu flag not recognized: %v", err)
	}
}
