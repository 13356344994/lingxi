package announcements

import "time"

// Venue identifies the exchange that published the announcement.
type Venue string

const (
	VenueOKX     Venue = "OKX"
	VenueBinance Venue = "BINANCE"
	VenueBybit   Venue = "BYBIT"
	VenueGate    Venue = "GATE"
)

// Kind is the normalized announcement category.
type Kind string

const (
	KindListing      Kind = "listing"
	KindDelisting    Kind = "delisting"
	KindMaintenance  Kind = "maintenance"
	KindSuspension   Kind = "suspension"
)

// Announcement is the normalized cross-venue announcement record.
type Announcement struct {
	ID           string    `json:"id"`
	Venue        Venue     `json:"venue"`
	Symbol       string    `json:"symbol"`
	Kind         Kind      `json:"kind"`
	PublishedAt  time.Time `json:"published_at"`
	FirstSeenAt  time.Time `json:"first_seen_at"`
	EffectiveAt  time.Time `json:"effective_at,omitempty"`
	SourceURL    string    `json:"source_url"`
	SourceHash   string    `json:"source_hash"`
	Title        string    `json:"title,omitempty"`
	InfoOnly     bool      `json:"info_only"`
	ExecutionVenue string  `json:"execution_venue"`
}

// ChainEvent is one ordered cross-venue timeline row for a symbol.
type ChainEvent struct {
	Sequence       int       `json:"sequence"`
	Symbol         string    `json:"symbol"`
	AnnouncementID string    `json:"announcement_id"`
	Venue          Venue     `json:"venue"`
	Kind           Kind      `json:"kind"`
	PublishedAt    time.Time `json:"published_at"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	EffectiveAt    time.Time `json:"effective_at,omitempty"`
	InfoOnly       bool      `json:"info_only"`
	ExecutionVenue string    `json:"execution_venue"`
}

// EventChain groups ordered events per symbol.
type EventChain struct {
	SchemaVersion  string       `json:"schema_version"`
	Generation     string       `json:"generation"`
	ExecutionVenue string       `json:"execution_venue"`
	ChainHash      string       `json:"chain_hash"`
	Symbols        []SymbolChain `json:"symbols"`
}

// SymbolChain is the append-only event sequence for one symbol.
type SymbolChain struct {
	Symbol string       `json:"symbol"`
	Events []ChainEvent `json:"events"`
}

// SourcePaths points at per-venue historical archive JSONL inputs.
type SourcePaths struct {
	OKX     string `json:"okx"`
	Binance string `json:"binance"`
	Bybit   string `json:"bybit"`
	Gate    string `json:"gate"`
}

// RunConfig binds CLI knobs for bounded archive ingestion.
type RunConfig struct {
	SchemaVersion string      `json:"schema_version"`
	Generation    string      `json:"generation"`
	ArchiveRoot   string      `json:"archive_root"`
	OutputDir     string      `json:"output_dir"`
	Sources       SourcePaths `json:"sources"`
	MaxLines      int         `json:"max_lines"`
	PageSize      int         `json:"page_size"`
	LowCPU        bool        `json:"low_cpu"`
}

// SourceStatus reports per-archive availability without leaking raw bodies.
type SourceStatus struct {
	Path          string `json:"path,omitempty"`
	Status        string `json:"status"`
	Lines         int    `json:"lines,omitempty"`
	Decoded       int    `json:"decoded,omitempty"`
	Skipped       int    `json:"skipped,omitempty"`
	Malformed     int    `json:"malformed,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	PagesComplete int    `json:"pages_complete,omitempty"`
	PagesExpected int    `json:"pages_expected,omitempty"`
	FetchFailed   bool   `json:"fetch_failed,omitempty"`
}

const (
	StatusAvailable  = "AVAILABLE"
	StatusUnknown    = "UNKNOWN"
	StatusTruncated  = "TRUNCATED"
	StatusIncomplete = "INCOMPLETE"
)

// CoverageSummary counts ingested announcements by venue/kind.
type CoverageSummary struct {
	TotalAnnouncements int            `json:"total_announcements"`
	ByVenue            map[string]int `json:"by_venue"`
	ByKind             map[string]int `json:"by_kind"`
	SymbolCount        int            `json:"symbol_count"`
	DedupeSkipped      int            `json:"dedupe_skipped"`
}

// AnnouncementTape is the sealed append-only announcement ledger.
type AnnouncementTape struct {
	SchemaVersion   string              `json:"schema_version"`
	Generation      string              `json:"generation"`
	ExecutionVenue  string              `json:"execution_venue"`
	ConfigHash      string              `json:"config_hash"`
	TapeHash        string              `json:"tape_hash"`
	BuiltAt         time.Time           `json:"built_at"`
	Incomplete      bool                `json:"incomplete"`
	SourceStatus    map[string]SourceStatus `json:"source_status"`
	Coverage        CoverageSummary     `json:"coverage"`
	Announcements   []Announcement      `json:"announcements"`
}
