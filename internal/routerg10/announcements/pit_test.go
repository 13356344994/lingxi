package announcements

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPITLookaheadRejectsFutureFirstSeen(t *testing.T) {
	pub := time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	a := Announcement{
		Venue:         VenueOKX,
		Symbol:        "FUTR-USDT",
		Kind:          KindListing,
		PublishedAt:   pub,
		FirstSeenAt:   firstSeen,
		SourceHash:    "lookahead_v1",
		ExecutionVenue: ExecutionVenue,
	}
	decision := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)
	if VisibleAt(a, decision) {
		t.Fatal("must reject announcement whose first_seen is after decision")
	}
	if IsEffectiveAt(a, decision) {
		t.Fatal("must not treat future-first-seen as effective")
	}
}

func TestPITFutureEffectiveVisibleButNotEffective(t *testing.T) {
	pub := time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
	effective := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)
	a := Announcement{
		Venue:         VenueOKX,
		Symbol:        "BTC-USDT",
		Kind:          KindDelisting,
		PublishedAt:   pub,
		FirstSeenAt:   firstSeen,
		EffectiveAt:   effective,
		SourceHash:    "okx_delist_btc_v1",
		ExecutionVenue: ExecutionVenue,
	}
	decision := time.Date(2024, 6, 5, 0, 0, 0, 0, time.UTC)
	if !VisibleAt(a, decision) {
		t.Fatal("announcement must be visible before effective date")
	}
	if IsEffectiveAt(a, decision) {
		t.Fatal("future effective must not count as already effective")
	}
	if !HasFutureEffective(a, decision) {
		t.Fatal("must flag future effective as announced-but-not-effective")
	}
}

func TestPITPublishedAfterDecisionHidden(t *testing.T) {
	pub := time.Date(2024, 6, 20, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 20, 9, 0, 0, 0, time.UTC)
	a := Announcement{
		Venue:         VenueBinance,
		Symbol:        "ETH-USDT",
		Kind:          KindListing,
		PublishedAt:   pub,
		FirstSeenAt:   firstSeen,
		SourceHash:    "late_pub",
		ExecutionVenue: ExecutionVenue,
		InfoOnly:      true,
	}
	decision := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)
	if VisibleAt(a, decision) {
		t.Fatal("published after decision must stay hidden")
	}
}

func TestVisibleAnnouncementsSorted(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []Announcement{
		{Venue: VenueGate, Symbol: "A-USDT", Kind: KindSuspension, PublishedAt: base, FirstSeenAt: base.Add(2 * time.Hour), SourceHash: "a", ExecutionVenue: ExecutionVenue, InfoOnly: true},
		{Venue: VenueOKX, Symbol: "B-USDT", Kind: KindDelisting, PublishedAt: base, FirstSeenAt: base.Add(1 * time.Hour), SourceHash: "b", ExecutionVenue: ExecutionVenue},
	}
	decision := base.Add(24 * time.Hour)
	visible := VisibleAnnouncements(items, decision)
	if len(visible) != 2 {
		t.Fatalf("expected 2 visible, got %d", len(visible))
	}
	if visible[0].Symbol != "B-USDT" {
		t.Fatalf("expected first_seen order, got %s", visible[0].Symbol)
	}
}

func TestRunnerLookaheadFixture(t *testing.T) {
	root := filepath.Join("testdata")
	out := t.TempDir()
	cp := filepath.Join(out, "cp.json")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		ArchiveRoot:   root,
		OutputDir:     out,
		Sources:       allVenueSources(root, "lookahead.jsonl"),
		MaxLines: 100,
	}
	runner := Runner{Config: cfg, CheckpointPath: cp}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decision := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)
	for _, a := range result.Tape.Announcements {
		if VisibleAt(a, decision) {
			t.Fatalf("lookahead fixture must stay hidden at %v: %+v", decision, a)
		}
	}
}
