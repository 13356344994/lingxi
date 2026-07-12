package main

import (
	"strings"
	"testing"

	"lingxi/internal/routerg4/protocol"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h", "--output-dir", "/tmp/o",
		"--checkpoint", "/tmp/cp.json", "--map-dir", "/tmp/m",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRejectsCandidateLimitAbove1024(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp/h", "--output-dir", "/tmp/o",
		"--checkpoint", "/tmp/cp.json", "--map-dir", "/tmp/m",
		"--max-files", "10", "--max-lines", "100", "--candidate-limit", "2048",
	})
	if err == nil || !strings.Contains(err.Error(), "candidate-limit") {
		t.Fatalf("expected candidate-limit error, got %v", err)
	}
	if protocol.MaxTotalCandidates != 1024 {
		t.Fatalf("protocol cap drift: %d", protocol.MaxTotalCandidates)
	}
}
