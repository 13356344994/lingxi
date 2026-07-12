package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp", "--profile", "okx_1h",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresCatalogSymbolLimit(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp", "--profile", "okx_1h",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "catalog-symbol-limit") {
		t.Fatalf("expected catalog-symbol-limit error, got %v", err)
	}
}

func TestCLIRecognizesAllowTruncatedSmoke(t *testing.T) {
	err := run([]string{
		"--catalog", "missing.json", "--history-root", "/tmp", "--profile", "okx_1h",
		"--output-dir", t.TempDir(), "--checkpoint", "/tmp/cp.json",
		"--max-files", "1", "--max-lines", "100", "--catalog-symbol-limit", "0",
		"--allow-truncated-smoke",
	})
	if err == nil || strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("allow-truncated-smoke flag not recognized: %v", err)
	}
}
