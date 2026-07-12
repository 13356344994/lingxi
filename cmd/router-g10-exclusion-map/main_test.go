package main

import (
	"strings"
	"testing"
)

func TestCLIRequiresExplicitBounds(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--announcement-dir", "/tmp/ann",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--map-dir", "/tmp/map",
	})
	if err == nil || !strings.Contains(err.Error(), "max-files") {
		t.Fatalf("expected max-files error, got %v", err)
	}
}

func TestCLIRequiresAnnouncementDir(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--map-dir", "/tmp/map",
		"--max-files", "10",
		"--max-lines", "100",
		"--candidate-limit", "5",
	})
	if err == nil || !strings.Contains(err.Error(), "announcement-dir") {
		t.Fatalf("expected announcement-dir error, got %v", err)
	}
}

func TestCLIRequiresMapDir(t *testing.T) {
	err := run([]string{
		"--catalog", "/tmp/catalog.json",
		"--history-root", "/tmp/history",
		"--announcement-dir", "/tmp/ann",
		"--output-dir", "/tmp/out",
		"--checkpoint", "/tmp/cp.json",
		"--max-files", "10",
		"--max-lines", "100",
		"--candidate-limit", "5",
	})
	if err == nil || !strings.Contains(err.Error(), "map-dir") {
		t.Fatalf("expected map-dir error, got %v", err)
	}
}
