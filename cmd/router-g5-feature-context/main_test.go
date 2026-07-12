package main

import (
	"strings"
	"testing"
)

func TestCLIRequiredFlags(t *testing.T) {
	err := run([]string{})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required flag error, got %v", err)
	}
}

func TestCLIUnsupportedProfile(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json",
		"--history-root", "/tmp",
		"--profile", "bad",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--max-files", "1",
		"--max-lines", "1",
		"--catalog-symbol-limit", "0",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported profile") {
		t.Fatalf("expected unsupported profile, got %v", err)
	}
}
