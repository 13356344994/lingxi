package announcements

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// DedupeKey returns the stable append-only identity for one announcement row.
func DedupeKey(a Announcement) string {
	parts := []string{
		string(a.Venue),
		normalizeSymbol(a.Symbol),
		string(a.Kind),
		a.SourceHash,
		a.SourceURL,
		a.PublishedAt.UTC().Format(timeRFC3339Nano),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

const timeRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func normalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func sortAnnouncements(in []Announcement) {
	sort.Slice(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if !a.FirstSeenAt.Equal(b.FirstSeenAt) {
			return a.FirstSeenAt.Before(b.FirstSeenAt)
		}
		if !a.PublishedAt.Equal(b.PublishedAt) {
			return a.PublishedAt.Before(b.PublishedAt)
		}
		if a.Venue != b.Venue {
			return a.Venue < b.Venue
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.ID < b.ID
	})
}

// Store is an append-only in-memory announcement ledger with dedupe.
type Store struct {
	items  []Announcement
	seen   map[string]struct{}
	dupes  int
}

// NewStore creates an empty append-only store.
func NewStore() *Store {
	return &Store{seen: make(map[string]struct{})}
}

// Append adds announcements; duplicates are skipped without mutation.
func (s *Store) Append(items ...Announcement) int {
	added := 0
	for _, a := range items {
		if a.ID == "" {
			a.ID = DedupeKey(a)
		}
		a.Symbol = normalizeSymbol(a.Symbol)
		a.ExecutionVenue = ExecutionVenue
		a.InfoOnly = a.Venue != VenueOKX
		if _, ok := s.seen[a.ID]; ok {
			s.dupes++
			continue
		}
		s.seen[a.ID] = struct{}{}
		s.items = append(s.items, a)
		added++
	}
	return added
}

// Items returns a sorted copy of stored announcements.
func (s *Store) Items() []Announcement {
	out := append([]Announcement(nil), s.items...)
	sortAnnouncements(out)
	return out
}

// DedupeSkipped returns duplicate rows ignored during ingestion.
func (s *Store) DedupeSkipped() int {
	return s.dupes
}

// Coverage builds summary counts from the current store.
func (s *Store) Coverage() CoverageSummary {
	items := s.Items()
	byVenue := map[string]int{}
	byKind := map[string]int{}
	symbols := map[string]struct{}{}
	for _, a := range items {
		byVenue[string(a.Venue)]++
		byKind[string(a.Kind)]++
		symbols[a.Symbol] = struct{}{}
	}
	return CoverageSummary{
		TotalAnnouncements: len(items),
		ByVenue:            byVenue,
		ByKind:             byKind,
		SymbolCount:        len(symbols),
		DedupeSkipped:      s.dupes,
	}
}

// RestoreStore rebuilds a store from checkpoint snapshot items.
func RestoreStore(items []Announcement) *Store {
	s := NewStore()
	s.Append(items...)
	return s
}

// ValidateAnnouncement rejects incomplete normalized rows.
func ValidateAnnouncement(a Announcement) error {
	if a.Venue == "" {
		return fmt.Errorf("announcements: venue required")
	}
	if a.Symbol == "" {
		return fmt.Errorf("announcements: symbol required")
	}
	if a.Kind == "" {
		return fmt.Errorf("announcements: kind required")
	}
	if a.PublishedAt.IsZero() {
		return fmt.Errorf("announcements: published_at required")
	}
	if a.FirstSeenAt.IsZero() {
		return fmt.Errorf("announcements: first_seen_at required")
	}
	if a.SourceHash == "" {
		return fmt.Errorf("announcements: source_hash required")
	}
	if a.ExecutionVenue != ExecutionVenue {
		return fmt.Errorf("announcements: execution_venue must be %q", ExecutionVenue)
	}
	return nil
}
