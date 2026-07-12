package announcements

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDedupeSkipsIdenticalRows(t *testing.T) {
	pub := time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
	a := Announcement{
		Venue:          VenueOKX,
		Symbol:         "BTC-USDT",
		Kind:           KindDelisting,
		PublishedAt:    pub,
		FirstSeenAt:    firstSeen,
		SourceURL:      "https://www.okx.com/help/okx-to-delist-btc-usdt",
		SourceHash:     "okx_delist_btc_v1",
		ExecutionVenue: ExecutionVenue,
	}
	store := NewStore()
	if got := store.Append(a, a); got != 1 {
		t.Fatalf("expected 1 added, got %d", got)
	}
	if store.DedupeSkipped() != 1 {
		t.Fatalf("expected 1 dedupe skip, got %d", store.DedupeSkipped())
	}
	if len(store.Items()) != 1 {
		t.Fatalf("expected 1 item, got %d", len(store.Items()))
	}
}

func TestDedupeAllowsDistinctSourceHash(t *testing.T) {
	pub := time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
	base := Announcement{
		Venue:          VenueOKX,
		Symbol:         "BTC-USDT",
		Kind:           KindDelisting,
		PublishedAt:    pub,
		FirstSeenAt:    firstSeen,
		SourceURL:      "https://example.com/a",
		ExecutionVenue: ExecutionVenue,
	}
	a := base
	a.SourceHash = "hash_a"
	b := base
	b.SourceHash = "hash_b"
	store := NewStore()
	if got := store.Append(a, b); got != 2 {
		t.Fatalf("expected 2 added, got %d", got)
	}
}

func TestRunnerDedupeFixture(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	multi := filepath.Join(root, "multi_venue.jsonl")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources: SourcePaths{
			OKX:     multi,
			Binance: multi,
			Bybit:   multi,
			Gate:    multi,
		},
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: filepath.Join(out, "cp.json")}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Tape.Coverage.DedupeSkipped < 1 {
		t.Fatalf("expected duplicate skip, got %d", result.Tape.Coverage.DedupeSkipped)
	}
	if result.Tape.Coverage.TotalAnnouncements != 4 {
		t.Fatalf("expected 4 unique announcements, got %d", result.Tape.Coverage.TotalAnnouncements)
	}
}
