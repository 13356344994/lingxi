package main

import (
	"strings"
	"testing"
)

func TestRequiredFlags(t *testing.T) {
	if err := run([]string{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing flags should fail: %v", err)
	}
}

func TestPositiveBoundsRequired(t *testing.T) {
	args := []string{
		"--catalog", "c.json", "--history-root", "/h", "--profile", "okx_1h",
		"--output-dir", "/o", "--checkpoint", "/cp", "--max-files", "0",
		"--max-lines", "100", "--catalog-symbol-limit", "0",
	}
	if err := run(args); err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("max-files bound expected: %v", err)
	}
}
