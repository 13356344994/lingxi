package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
)

func checkpointTestEvent() g3reverse.Event {
	decision := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	return g3reverse.Event{
		ID: "BTCUSDT-1-1h-long", Symbol: "BTCUSDT", Cluster: "BTCUSDT",
		SkyState: "s", SorterDomain: "liquid", LiquidityBucket: "liq_6",
		Horizon: g3reverse.Horizon1h, DecisionTime: decision, FeatureAsOf: decision,
		EntryTime: decision, ExitTime: decision.Add(time.Hour), EntryPrice: 100, ExitPrice: 101,
		Direction: mechanism.DirectionLong, Features: map[string]float64{"symbol_return_1h": 1},
		NetOutcomeBPS: 88, OutcomeClass: g3reverse.OutcomeWinner,
	}
}

func checkpointTestBinding(root string) checkpointBinding {
	return checkpointBinding{
		CatalogDataHash: "data", CatalogSchemaHash: "schema", DatasetID: "dataset",
		SelectedFileHash: "selected", ConfigHash: "config", CutpointsHash: "cuts",
		BoundariesHash: "bounds", OutputDir: root,
	}
}

func TestCheckpointResumeRevalidatesEventFile(t *testing.T) {
	root := t.TempDir()
	eventsPath := filepath.Join("g3_induction", "events.jsonl")
	if err := os.MkdirAll(filepath.Join(root, "g3_induction"), 0o755); err != nil {
		t.Fatal(err)
	}
	events := []g3reverse.Event{checkpointTestEvent()}
	if err := writeJSONL(filepath.Join(root, eventsPath), events); err != nil {
		t.Fatal(err)
	}
	eventHash, err := g3reverse.HashEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	inputHash, err := partitionInputHash("induction", start, end, "selected", "config")
	if err != nil {
		t.Fatal(err)
	}
	binding := checkpointTestBinding(root)
	cp := newCheckpoint(binding)
	cp.Partitions["induction"] = partitionCheckpoint{
		Start: start, End: end, InputHash: inputHash, EventHash: eventHash,
		EventCount: 1, EventsPath: eventsPath, Rejections: map[string]int{"SKY_OOD": 2},
	}
	checkpointPath := filepath.Join(root, "checkpoint.json")
	if err := saveCheckpoint(checkpointPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := loadCheckpoint(checkpointPath)
	if err != nil || !ok {
		t.Fatalf("load checkpoint: ok=%v err=%v", ok, err)
	}
	if err := loaded.validateBinding(binding); err != nil {
		t.Fatal(err)
	}
	got, err := validatePartitionRecord(root, "induction", loaded.Partitions["induction"], "selected", "config")
	if err != nil || len(got) != 1 || got[0].ID != events[0].ID {
		t.Fatalf("resume validation failed: events=%+v err=%v", got, err)
	}
}

func TestCheckpointRejectsEventDriftAndBindingDrift(t *testing.T) {
	root := t.TempDir()
	eventsPath := filepath.Join("g3_induction", "events.jsonl")
	if err := os.MkdirAll(filepath.Join(root, "g3_induction"), 0o755); err != nil {
		t.Fatal(err)
	}
	events := []g3reverse.Event{checkpointTestEvent()}
	if err := writeJSONL(filepath.Join(root, eventsPath), events); err != nil {
		t.Fatal(err)
	}
	eventHash, _ := g3reverse.HashEvents(events)
	start := time.Now().UTC().Truncate(time.Hour)
	end := start.Add(time.Hour)
	inputHash, _ := partitionInputHash("induction", start, end, "selected", "config")
	record := partitionCheckpoint{
		Start: start, End: end, InputHash: inputHash, EventHash: eventHash,
		EventCount: 1, EventsPath: eventsPath, Rejections: map[string]int{},
	}
	events[0].EntryPrice = 999
	if err := writeJSONL(filepath.Join(root, eventsPath), events); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePartitionRecord(root, "induction", record, "selected", "config"); err == nil {
		t.Fatal("tampered resumed event file must be rejected")
	}
	cp := newCheckpoint(checkpointTestBinding(root))
	drift := checkpointTestBinding(root)
	drift.SelectedFileHash = "changed"
	if err := cp.validateBinding(drift); err == nil {
		t.Fatal("selected-file drift must be rejected")
	}
}

func TestCheckpointHashTamperRejected(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "checkpoint.json")
	if err := saveCheckpoint(path, newCheckpoint(checkpointTestBinding(root))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["config_hash"] = "tampered"
	raw, _ = json.Marshal(document)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCheckpoint(path); err == nil {
		t.Fatal("tampered checkpoint must be rejected")
	}
}

func TestCLIRequiresCheckpointAndNonemptyHMAC(t *testing.T) {
	t.Setenv("LINGXI_ROUTER_G3_DISCOVERY_HMAC_KEY", "")
	if err := run(context.Background(), []string{"--output-dir", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--checkpoint") {
		t.Fatalf("missing checkpoint should fail, got %v", err)
	}
	if err := run(context.Background(), []string{
		"--output-dir", t.TempDir(), "--checkpoint", filepath.Join(t.TempDir(), "cp.json"),
	}); err == nil || !strings.Contains(err.Error(), "HMAC") {
		t.Fatalf("empty HMAC should fail before generation, got %v", err)
	}
}
