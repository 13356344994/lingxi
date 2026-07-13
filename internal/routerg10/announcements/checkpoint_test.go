package announcements

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointTamperRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   filepath.Join("testdata"),
		OutputDir:     dir,
		Sources:       allVenueSources("testdata", "multi_venue.jsonl"),
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: path}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["dedupe_skipped"] = 999
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadCheckpoint(path); err == nil || ok {
		t.Fatal("tampered checkpoint must fail closed")
	}
}

func TestManifestTamperRejected(t *testing.T) {
	dir := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   filepath.Join("testdata"),
		OutputDir:     dir,
		Sources:       allVenueSources("testdata", "multi_venue.jsonl"),
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(dir, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, OutputManifestFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["tape_hash"] = "tampered"
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(manifestPath); err == nil {
		t.Fatal("tampered manifest must fail")
	}
	if err := VerifyOutputArtifacts(dir, result.Manifest); err == nil {
		// VerifyOutputArtifacts uses original manifest; tamper file only affects LoadManifest
	}
}
