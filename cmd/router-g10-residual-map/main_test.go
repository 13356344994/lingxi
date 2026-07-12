package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--fingerprint", "/tmp/fp.json",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--map-dir", "/tmp/map",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresFingerprint(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--map-dir", "/tmp/map",
		"--max-files", "10",
		"--max-lines", "100",
		"--candidate-limit", "8",
	})
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected fingerprint error, got %v", err)
	}
}

func TestCLIRequiresMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--fingerprint", "/tmp/fp.json",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--max-files", "10",
		"--max-lines", "100",
		"--candidate-limit", "8",
	})
	if err == nil || !strings.Contains(err.Error(), "map-dir") {
		t.Fatalf("expected map-dir error, got %v", err)
	}
}
