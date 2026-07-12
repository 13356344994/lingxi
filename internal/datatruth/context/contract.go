// Package contextdata materializes legacy record-only feeds into deterministic,
// point-in-time daily context partitions. It never reads settlements, PnL, or exams.
package contextdata

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

const SchemaVersion = "lingxi.context.v1"

type Kind string

const (
	KindInstrument Kind = "instrument"
	KindUniverse   Kind = "universe"
	KindMids       Kind = "mids"
	KindOI         Kind = "oi"
	KindXVenue     Kind = "xvenue"
	KindDepth      Kind = "depth"
)

var RequiredKinds = []Kind{
	KindInstrument,
	KindUniverse,
	KindMids,
	KindOI,
	KindXVenue,
	KindDepth,
}

func ParseKind(raw string) (Kind, error) {
	k := Kind(strings.ToLower(strings.TrimSpace(raw)))
	switch k {
	case KindInstrument, KindUniverse, KindMids, KindOI, KindXVenue, KindDepth:
		return k, nil
	default:
		return "", fmt.Errorf("contextdata: unsupported kind %q", raw)
	}
}

type ClockProvenance struct {
	EventTime   string `json:"event_time"`
	ReceiveTime string `json:"receive_time"`
	ProcessTime string `json:"process_time"`
}

type Record struct {
	SchemaVersion string                  `json:"schema_version"`
	Kind          Kind                    `json:"kind"`
	Event         datatruth.EventEnvelope `json:"event"`
	Clocks        ClockProvenance         `json:"clocks"`
}

type InputIdentity struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	PrefixSize int64     `json:"prefix_size"`
	PrefixHash string    `json:"prefix_hash"`
}

type QualityCounts struct {
	Accepted           int64 `json:"accepted"`
	Duplicate          int64 `json:"duplicate"`
	Late               int64 `json:"late"`
	OutOfOrder         int64 `json:"out_of_order"`
	Gap                int64 `json:"gap"`
	ClockInvalid       int64 `json:"clock_invalid"`
	Correction         int64 `json:"correction"`
	MissingPayloadHash int64 `json:"missing_payload_hash"`
	MissingRequired    int64 `json:"missing_required"`
}

type ClockRange struct {
	MinEventTime   time.Time `json:"min_event_time,omitempty"`
	MaxEventTime   time.Time `json:"max_event_time,omitempty"`
	MinReceiveTime time.Time `json:"min_receive_time,omitempty"`
	MaxReceiveTime time.Time `json:"max_receive_time,omitempty"`
	MinProcessTime time.Time `json:"min_process_time,omitempty"`
	MaxProcessTime time.Time `json:"max_process_time,omitempty"`
}

type Verdict string

const (
	VerdictPass Verdict = "PASS"
	VerdictFail Verdict = "FAIL"
)

const (
	ReasonSchemaInvalid      = "SCHEMA_INVALID"
	ReasonPITMissing         = "PIT_MISSING"
	ReasonUniverseMissing    = "UNIVERSE_MISSING"
	ReasonMidsMissing        = "MIDS_MISSING"
	ReasonOIMissing          = "OI_MISSING"
	ReasonOIStale            = "OI_STALE"
	ReasonXVenueMissing      = "XVENUE_MISSING"
	ReasonDepthMissing       = "DEPTH_MISSING"
	ReasonClockInvalid       = "CLOCK_INVALID"
	ReasonQualityInvalid     = "QUALITY_INVALID"
	ReasonPartitionEmpty     = "PARTITION_EMPTY"
	ReasonSourceIdentity     = "SOURCE_IDENTITY_INVALID"
	ReasonIncompleteDay      = "INCOMPLETE_DAY"
	ReasonCatalogInvalid     = "CATALOG_INVALID"
	ReasonInputConfigChanged = "INPUT_OR_CONFIG_CHANGED"
)

type PartitionManifest struct {
	SchemaVersion string         `json:"schema_version"`
	DatasetID     string         `json:"dataset_id"`
	Kind          Kind           `json:"kind"`
	Date          string         `json:"date"`
	Input         InputIdentity  `json:"input"`
	OutputFile    string         `json:"output_file"`
	OutputHash    string         `json:"output_hash"`
	ConfigHash    string         `json:"config_hash"`
	RowsScanned   int64          `json:"rows_scanned"`
	RowsEmitted   int64          `json:"rows_emitted"`
	PITVersions   int64          `json:"pit_versions,omitempty"`
	Quality       QualityCounts  `json:"quality"`
	Clocks        ClockRange     `json:"clocks"`
	Verdict       Verdict        `json:"verdict"`
	Reasons       []string       `json:"reasons,omitempty"`
	Completed     bool           `json:"completed"`
	CreatedAt     time.Time      `json:"created_at"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func (m *PartitionManifest) Normalize() {
	m.Reasons = uniqueSorted(m.Reasons)
}

func (m PartitionManifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%s: got %q want %q", ReasonSchemaInvalid, m.SchemaVersion, SchemaVersion)
	}
	if _, err := ParseKind(string(m.Kind)); err != nil {
		return err
	}
	if _, err := time.Parse("2006-01-02", m.Date); err != nil {
		return fmt.Errorf("contextdata: invalid UTC date %q: %w", m.Date, err)
	}
	if strings.TrimSpace(m.DatasetID) == "" || strings.TrimSpace(m.ConfigHash) == "" {
		return errors.New("contextdata: dataset_id and config_hash are required")
	}
	if strings.TrimSpace(m.Input.Path) == "" || m.Input.Size < 0 || strings.TrimSpace(m.Input.PrefixHash) == "" {
		return errors.New("contextdata: valid input identity is required")
	}
	if m.RowsScanned < 0 || m.RowsEmitted < 0 || m.RowsEmitted > m.RowsScanned && m.Kind != KindInstrument && m.Kind != KindUniverse {
		return errors.New("contextdata: invalid row counts")
	}
	if m.Completed && (strings.TrimSpace(m.OutputFile) == "" || strings.TrimSpace(m.OutputHash) == "") {
		return errors.New("contextdata: completed manifest requires output file and hash")
	}
	return nil
}

type DailyVerdict struct {
	SchemaVersion string                     `json:"schema_version"`
	Date          string                     `json:"date"`
	Verdict       Verdict                    `json:"verdict"`
	Reasons       []string                   `json:"reasons,omitempty"`
	Manifests     map[Kind]PartitionManifest `json:"manifests"`
	CreatedAt     time.Time                  `json:"created_at"`
}

func (v *DailyVerdict) Normalize() {
	v.Reasons = uniqueSorted(v.Reasons)
}

func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			seen[s] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
