package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMeasurementOutputAllowsOnlyFixedCanonicalPath(t *testing.T) {
	repo := testRepository(t)
	expected := filepath.Join(repo, filepath.FromSlash(measurementOutput))
	got, err := resolveMeasurementOutput(repo, expected)
	if err != nil || got != expected {
		t.Fatalf("fixed output rejected: %q %v", got, err)
	}
	for _, bad := range []string{
		filepath.Join(repo, "research", "firstbattle", "route_data_contracts.json"),
		filepath.Join(repo, "research", "firstbattle", "other.json"),
		filepath.Join(repo, "records", "receipt.json"),
	} {
		if _, err := resolveMeasurementOutput(repo, bad); err == nil {
			t.Fatalf("accepted forbidden output %q", bad)
		}
	}
}

func TestResolveMeasurementOutputRejectsSymlink(t *testing.T) {
	repo, outside := testRepository(t), t.TempDir()
	target := filepath.Join(repo, filepath.FromSlash(measurementOutput))
	if err := os.Symlink(filepath.Join(outside, "receipt.json"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMeasurementOutput(repo, target); err == nil {
		t.Fatal("accepted symlink measurement target")
	}
}

func TestResolveMeasurementOutputCWDChanges(t *testing.T) {
	repo := testRepository(t)
	nested := filepath.Join(repo, "cmd", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(repo, filepath.FromSlash(measurementOutput))
	if got, err := resolveMeasurementOutput(nested, expected); err != nil || got != expected {
		t.Fatalf("nested repository cwd failed: %q %v", got, err)
	}
	if _, err := resolveMeasurementOutput(t.TempDir(), expected); err == nil {
		t.Fatal("accepted cwd outside canonical repository")
	}
}

func testRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, dir := range []string{".git", "research/firstbattle"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.FromSlash(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module lingxi\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "research", "firstbattle", "route_data_contracts.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}
