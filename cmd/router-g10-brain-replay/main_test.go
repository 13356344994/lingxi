package main

import (
	"os"
	"path/filepath"
	"testing"

	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/brain"
)

func TestReplayCLI(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "checkpoint.json")
	if err := run([]string{"--output-dir", dir, "--checkpoint", cp}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty checkpoint")
	}
}

func TestReplayDeterministicAcrossRuns(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	cp1 := filepath.Join(dir1, "cp.json")
	cp2 := filepath.Join(dir2, "cp.json")
	if err := run([]string{"--output-dir", dir1, "--checkpoint", cp1}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--output-dir", dir2, "--checkpoint", cp2}); err != nil {
		t.Fatal(err)
	}
	t1, _ := os.ReadFile(filepath.Join(dir1, "brain_trace.json"))
	t2, _ := os.ReadFile(filepath.Join(dir2, "brain_trace.json"))
	if string(t1) != string(t2) {
		t.Fatal("brain trace drift across replay runs")
	}
}

func TestPathMatrixCoverageInCLI(t *testing.T) {
	bundle := brain.BuildDefaultFixtures()
	tags := map[string]bool{}
	for _, sc := range bundle.Scenarios {
		_, pt, err := brain.ReplayScenario(sc)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range pt {
			tags[k] = tags[k] || v
		}
	}
	for _, path := range audit.PathMatrixRequired {
		if !tags[path] {
			t.Fatalf("path matrix missing %s", path)
		}
	}
}
