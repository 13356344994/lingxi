package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--exclusion-map-dir", "/tmp/exclusion",
		"--entry-proof-dir", "/tmp/entry",
		"--execution-policy-dir", "/tmp/exec",
		"--announcement-dir", "/tmp/ann",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "candidate-limit") {
		t.Fatalf("expected candidate-limit error, got %v", err)
	}
}

func TestCLIRequiresUpstreamDirs(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--candidate-limit", "5",
	})
	if err == nil || !strings.Contains(err.Error(), "exclusion-map-dir") {
		t.Fatalf("expected exclusion-map-dir error, got %v", err)
	}
}
