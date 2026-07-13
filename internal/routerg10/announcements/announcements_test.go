package announcements

import (
	"context"
	"os"
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
		Sources:       allVenueSources(root, "multi_venue.jsonl"),
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Tape.Incomplete {
		t.Fatalf("complete fixture must not be incomplete: %+v", result.Tape.IncompleteReasons)
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
		Sources:       allVenueSources(root, "incomplete_fetch.jsonl"),
		MaxLines:      100,
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
	st := result.Tape.SourceStatus["okx"]
	if st.Reason != ReasonFetchFailed {
		t.Fatalf("okx reason=%q want %q", st.Reason, ReasonFetchFailed)
	}
}

func TestRunnerFailClosedWhenAllRequiredArchivesMissing(t *testing.T) {
	out := t.TempDir()
	root := filepath.Join(out, "missing_archive_root")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     filepath.Join(out, "output"),
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Tape.Incomplete {
		t.Fatal("missing required venue archives must fail closed")
	}
	if result.Tape.Coverage.TotalAnnouncements != 0 {
		t.Fatalf("missing archives must not synthesize announcements, got %d", result.Tape.Coverage.TotalAnnouncements)
	}
	if len(result.Tape.IncompleteReasons) != len(RequiredSourceKeys()) {
		t.Fatalf("want %d incomplete reasons, got %+v", len(RequiredSourceKeys()), result.Tape.IncompleteReasons)
	}
	for _, key := range RequiredSourceKeys() {
		st := result.Tape.SourceStatus[key]
		if !st.Missing || st.Reason != ReasonRequiredVenueArchiveMissing {
			t.Fatalf("source %s must be missing fail-closed: %+v", key, st)
		}
	}
}

func TestRunnerFailClosedOnEmptyArchiveWithoutReceipt(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources:       allVenueSources(root, "empty_archive.jsonl"),
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Tape.Incomplete {
		t.Fatal("empty archive without coverage receipt must fail closed")
	}
	st := result.Tape.SourceStatus["okx"]
	if st.Reason != ReasonEmptyArchiveWithoutReceipt {
		t.Fatalf("reason=%q want %q", st.Reason, ReasonEmptyArchiveWithoutReceipt)
	}
}

func TestRunnerAllowsExplicitNoEventsCoverageReceipt(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources:       allVenueSources(root, "no_events_receipt.jsonl"),
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Tape.Incomplete {
		t.Fatalf("explicit no-events receipt must be complete: %+v", result.Tape.IncompleteReasons)
	}
	if result.Tape.Coverage.TotalAnnouncements != 0 {
		t.Fatalf("receipt-only archive must not synthesize announcements, got %d", result.Tape.Coverage.TotalAnnouncements)
	}
	for _, key := range RequiredSourceKeys() {
		st := result.Tape.SourceStatus[key]
		if st.Status != StatusAvailable || st.Reason != ReasonNoEventsCoverageReceipt {
			t.Fatalf("source %s must be receipt-complete: %+v", key, st)
		}
	}
}

func TestShouldFailClosedRequiresAllPreregisteredVenues(t *testing.T) {
	status := map[string]SourceStatus{
		"okx": {Status: StatusAvailable},
	}
	if !ShouldFailClosed(status) {
		t.Fatal("partial venue coverage must fail closed")
	}
	status = map[string]SourceStatus{
		"okx":     {Status: StatusIncomplete, Reason: ReasonRequiredVenueArchiveMissing, Missing: true},
		"binance": {Status: StatusIncomplete, Reason: ReasonRequiredVenueArchiveMissing, Missing: true},
		"bybit":   {Status: StatusIncomplete, Reason: ReasonRequiredVenueArchiveMissing, Missing: true},
		"gate":    {Status: StatusIncomplete, Reason: ReasonRequiredVenueArchiveMissing, Missing: true},
	}
	if !ShouldFailClosed(status) {
		t.Fatal("all missing required venues must fail closed")
	}
	status["binance"] = SourceStatus{Status: StatusIncomplete, FetchFailed: true, Reason: ReasonFetchFailed}
	if !ShouldFailClosed(status) {
		t.Fatal("network/archive failure must fail closed")
	}
	complete := map[string]SourceStatus{
		"okx":     {Status: StatusAvailable, Reason: ReasonNoEventsCoverageReceipt},
		"binance": {Status: StatusAvailable, Reason: ReasonNoEventsCoverageReceipt},
		"bybit":   {Status: StatusAvailable, Reason: ReasonNoEventsCoverageReceipt},
		"gate":    {Status: StatusAvailable, Reason: ReasonNoEventsCoverageReceipt},
	}
	if ShouldFailClosed(complete) {
		t.Fatal("explicit complete receipt coverage must not fail closed")
	}
}

func TestFormalRunMissingArchiveRootFailClosed(t *testing.T) {
	out := t.TempDir()
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   filepath.Join(out, "records", "announcement_history"),
		OutputDir:     filepath.Join(out, "g10_announcements"),
		MaxLines:      1000,
	}
	if err := ValidateRunConfig(cfg); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Tape.Incomplete || !result.Manifest.Incomplete {
		t.Fatalf("formal missing archive root must be incomplete: tape=%t manifest=%t reasons=%v",
			result.Tape.Incomplete, result.Manifest.Incomplete, result.Manifest.IncompleteReasons)
	}
	if result.Tape.Coverage.TotalAnnouncements != 0 {
		t.Fatalf("want announcements=0 without synthesis, got %d", result.Tape.Coverage.TotalAnnouncements)
	}
}
