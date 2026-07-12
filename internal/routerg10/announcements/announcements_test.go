package announcements

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRunnerMultiVenueOutputsChainAndManifest(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources: SourcePaths{
			OKX:     filepath.Join(root, "multi_venue.jsonl"),
			Binance: filepath.Join(root, "multi_venue.jsonl"),
			Bybit:   filepath.Join(root, "multi_venue.jsonl"),
			Gate:    filepath.Join(root, "multi_venue.jsonl"),
		},
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Tape.ExecutionVenue != ExecutionVenue {
		t.Fatal("tape execution venue must be OKX")
	}
	if result.Chain.ExecutionVenue != ExecutionVenue {
		t.Fatal("chain execution venue must be OKX")
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEventChain(result.Chain); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputArtifacts(out, result.Manifest); err != nil {
		t.Fatal(err)
	}
	for _, a := range result.Tape.Announcements {
		if a.Venue != VenueOKX && !a.InfoOnly {
			t.Fatalf("non-OKX must be info-only: %+v", a)
		}
	}
}

func TestRunnerFailClosedOnIncompleteFetch(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources: SourcePaths{
			OKX: filepath.Join(root, "incomplete_fetch.jsonl"),
		},
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Tape.Incomplete {
		t.Fatal("fetch failure must mark tape incomplete (fail-closed)")
	}
	if !result.Manifest.Incomplete {
		t.Fatal("manifest must propagate incomplete flag")
	}
	if !ShouldFailClosed(result.Tape.SourceStatus) {
		t.Fatal("source status must fail closed")
	}
}

func TestShouldFailClosedDoesNotBackfillFromUniverse(t *testing.T) {
	// Missing archives stay UNKNOWN; incomplete fetch stays fail-closed without
	// synthesizing listing/delisting history from current instrument universe.
	status := map[string]SourceStatus{
		"okx": {Status: StatusUnknown},
	}
	if ShouldFailClosed(status) {
		t.Fatal("unknown-only sources must not alone trigger incomplete fail-closed")
	}
	status["binance"] = SourceStatus{Status: StatusIncomplete, FetchFailed: true}
	if !ShouldFailClosed(status) {
		t.Fatal("network/archive failure must fail closed")
	}
}
