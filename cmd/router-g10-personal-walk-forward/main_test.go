package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresValidationDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "validation-dir") {
		t.Fatalf("expected validation-dir error, got %v", err)
	}
}

func TestCLIRequiresPlaybookManifestHash(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--validation-dir", "/tmp/val",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
	})
	if err == nil || !strings.Contains(err.Error(), "playbook-manifest-hash") {
		t.Fatalf("expected playbook-manifest-hash error, got %v", err)
	}
}

func TestCLIRequiresMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "c.json", "--history-root", "/tmp",
		"--playbook-output-dir", "/tmp/playbooks", "--validation-dir", "/tmp/val",
		"--output-dir", "/tmp/out", "--checkpoint", "/tmp/cp.json",
		"--playbook-manifest-hash", "abc", "--constitution-hash", "def",
		"--reverse-tapes-dir", "/tmp/tapes", "--reverse-tapes-manifest-hash", "rt",
		"--announcement-dir", "/tmp/ann", "--announcement-manifest-hash", "am",
		"--max-files", "10", "--max-lines", "100",
	})
	if err == nil || !strings.Contains(err.Error(), "map-dir") {
		t.Fatalf("expected map-dir error, got %v", err)
	}
}
