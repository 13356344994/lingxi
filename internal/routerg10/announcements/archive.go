package announcements

import (
	"encoding/json"
	"time"

	"lingxi/internal/datatruth"
)

// ArchiveLine is one append-only historical archive JSONL record.
type ArchiveLine struct {
	SchemaVersion string          `json:"schema_version,omitempty"`
	Venue         Venue           `json:"venue"`
	Format        string          `json:"format,omitempty"`
	Page          int             `json:"page,omitempty"`
	TotalPages    int             `json:"total_pages,omitempty"`
	FetchStatus   string          `json:"fetch_status,omitempty"`
	FetchedAt     time.Time       `json:"fetched_at,omitempty"`
	SourceURL     string          `json:"source_url,omitempty"`
	SourceHash    string          `json:"source_hash,omitempty"`
	BodyHash      string          `json:"body_hash,omitempty"`
	Body          json.RawMessage `json:"body,omitempty"`
	// Normalized shortcut fields (format=normalized_v1).
	PublishedAt time.Time `json:"published_at,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at,omitempty"`
	EffectiveAt time.Time `json:"effective_at,omitempty"`
	Kind        Kind      `json:"kind,omitempty"`
	Title       string    `json:"title,omitempty"`
	Symbols     []string  `json:"symbols,omitempty"`
}

// ParseArchiveLine decodes one JSONL archive record.
func ParseArchiveLine(raw []byte) (ArchiveLine, error) {
	var line ArchiveLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return ArchiveLine{}, err
	}
	if line.SchemaVersion == "" {
		line.SchemaVersion = ArchiveLineSchema
	}
	return line, nil
}

// HashArchiveLine fingerprints one archive line for tamper detection.
func HashArchiveLine(line ArchiveLine) (string, error) {
	hash, _, err := datatruth.HashJSON(line)
	return hash, err
}

// streamStats counts one archive pass.
type streamStats struct {
	Lines         int
	Decoded       int
	Skipped       int
	Malformed     int
	Truncated     bool
	PagesComplete int
	PagesExpected int
	FetchFailed   bool
}

func ingestArchiveFile(
	path string,
	maxLines, skipLines, pageSize int,
	venue Venue,
	store *Store,
) (streamStats, error) {
	return streamArchiveJSONL(path, maxLines, skipLines, func(raw []byte) error {
		line, err := ParseArchiveLine(raw)
		if err != nil {
			return err
		}
		if line.Venue != "" && line.Venue != venue {
			return nil
		}
		items, err := AdaptArchiveLine(line, venue)
		if err != nil {
			return err
		}
		store.Append(items...)
		return nil
	}, func(line ArchiveLine, stats *streamStats) {
		if line.FetchStatus == "failed" {
			stats.FetchFailed = true
		}
		if line.TotalPages > 0 {
			if stats.PagesExpected < line.TotalPages {
				stats.PagesExpected = line.TotalPages
			}
		}
		if line.Page > 0 {
			stats.PagesComplete++
		}
		_ = pageSize
	})
}
