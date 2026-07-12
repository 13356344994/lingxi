package reverseevent

import (
	"time"

	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

const (
	SchemaVersionLedger   = "lingxi.router-g6-reverse-events.ledger.v1"
	SchemaVersionCard     = "lingxi.router-g6-reverse-events.card.v1"
	SchemaVersionManifest = "lingxi.router-g6-reverse-events.manifest.v1"
	SchemaVersionSummary  = "lingxi.router-g6-reverse-events.summary.v1"
	SchemaVersionConfig   = "lingxi.router-g6-reverse-events.config.v1"

	LedgerStageDiscovery = "discovery_reverse_events"

	// MaxCardCandidates is the frozen upper bound for reverse EventCard derivation.
	MaxCardCandidates = 256

	ValidationStagePending = "full_event_verification_pending"

	EventCompletionStopError = "event_complete_stop_error"
	PayerStatusUnexplained   = "UNEXPLAINED"
)

// TriggerCondition is one feature-only trigger predicate reverse-derived from winners.
type TriggerCondition struct {
	Field string  `json:"field"`
	Op    string  `json:"op"`
	Value float64 `json:"value,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// TriggerSpec holds reverse-derived trigger_conditions for one EventCard.
type TriggerSpec struct {
	EventFamily string             `json:"event_family"`
	Conditions  []TriggerCondition `json:"conditions"`
}

// DynamicDeadlineSpec describes horizon-based fallback deadline semantics.
type DynamicDeadlineSpec struct {
	Horizon           string `json:"horizon"`
	FallbackOnly      bool   `json:"fallback_only"`
	StopErrorRequired bool   `json:"stop_error_required"`
}

// FrozenEventCard is one sealed reverse-derived event card awaiting full verification.
type FrozenEventCard struct {
	SchemaVersion   string              `json:"schema_version"`
	Generation      string              `json:"generation"`
	ProtocolHash    string              `json:"protocol_hash"`
	EventCardHash   string              `json:"event_card_hash"`
	ExperimentHash  string              `json:"experiment_hash,omitempty"`
	ProfileID       string              `json:"profile_id"`
	Ordinal         int                 `json:"ordinal"`
	EventFamily     string              `json:"event_family"`
	Horizon         string              `json:"horizon"`
	Direction       string              `json:"direction"`
	DelayBars       int                 `json:"delay_bars"`
	ShockCluster       string              `json:"shock_cluster"`
	TriggerConditions TriggerSpec       `json:"trigger_conditions"`
	DirectionRule   string              `json:"direction_rule"`
	MaxWaitTime     string              `json:"max_wait_time"`
	DynamicDeadline DynamicDeadlineSpec `json:"dynamic_deadline"`
	InvalidatorConditions []string      `json:"invalidator_conditions"`
	CapacityBand    string              `json:"capacity_band"`
	CapitalTiers    []int               `json:"capital_tiers"`
	DataRequirements []string           `json:"data_requirements"`
	EventCompletion string              `json:"event_completion"`
	PayerStatus     string              `json:"payer_status"`
	Status          string              `json:"status"`
	ValidationStage string              `json:"validation_stage"`
	TradeAllowed    bool                `json:"trade_allowed"`
	ExamEligible    bool                `json:"exam_eligible"`
	SourceWinnerCount   int             `json:"source_winner_count"`
	SourceWinnerIDsHash string          `json:"source_winner_ids_hash"`
	MapManifestHash     string          `json:"map_manifest_hash"`
	DiscoveryMode       string          `json:"discovery_mode"`
	ExamActions         int             `json:"exam_actions"`
}

// ExperimentAttempt records one append-only reverse derivation attempt.
type ExperimentAttempt struct {
	AttemptOrdinal      int    `json:"attempt_ordinal"`
	EnumeratorCursor    string `json:"enumerator_cursor"`
	EventCardHash       string `json:"event_card_hash,omitempty"`
	ProfileID           string `json:"profile_id"`
	EventFamily         string `json:"event_family"`
	Horizon             string `json:"horizon"`
	Direction           string `json:"direction"`
	ShockCluster        string `json:"shock_cluster"`
	Status              string `json:"status"`
	StatusReason        string `json:"status_reason"`
	LedgerStage         string `json:"ledger_stage"`
	SourceWinnerCount   int    `json:"source_winner_count,omitempty"`
	SourceWinnerIDsHash string `json:"source_winner_ids_hash,omitempty"`
}

// ExperimentLedger is the append-only discovery experiment ledger sealed as ExperimentHash.
type ExperimentLedger struct {
	SchemaVersion     string              `json:"schema_version"`
	Generation        string              `json:"generation"`
	ProtocolHash      string              `json:"protocol_hash"`
	MapManifestHashes map[string]string   `json:"map_manifest_hashes"`
	LedgerStage       string              `json:"ledger_stage"`
	DiscoveryMode     string              `json:"discovery_mode"`
	Attempts          []ExperimentAttempt `json:"attempts"`
	LedgerHash        string              `json:"ledger_hash"`
}

// EventCardManifest seals inputs/outputs before validation replay.
type EventCardManifest struct {
	SchemaVersion          string            `json:"schema_version"`
	Generation             string            `json:"generation"`
	ProtocolHash           string            `json:"protocol_hash"`
	HistoryDataHash        string            `json:"history_data_hash"`
	DiscoveryMode          string            `json:"discovery_mode"`
	ConfigHash             string            `json:"config_hash"`
	CatalogHash            string            `json:"catalog_hash"`
	MapManifestHashes      map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes      map[string]string `json:"map_artifact_hashes"`
	CardLimit              int               `json:"card_limit"`
	ActualCardCount        int               `json:"actual_card_count"`
	EventCardHash          string            `json:"event_card_hash"`
	ExperimentHash         string            `json:"experiment_hash"`
	CoverageHash           string            `json:"coverage_hash"`
	Coverage               CoverageSummary   `json:"coverage"`
	FreezeBeforeValidation bool              `json:"freeze_before_validation"`
	ExamActions            int               `json:"exam_actions"`
	CreatedAt              time.Time         `json:"created_at"`
	ManifestHash           string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one reverse-events run.
type RunSummary struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	DiscoveryMode        string            `json:"discovery_mode"`
	ProfileIDs           []string          `json:"profile_ids"`
	WinnerSeedCount      int               `json:"winner_seed_count"`
	AttemptCount         int               `json:"attempt_count"`
	ActualCardCount      int               `json:"actual_card_count"`
	CardLimit            int               `json:"card_limit"`
	CoverageByFamily     map[string]int    `json:"coverage_by_family"`
	CoverageByDirection  map[string]int    `json:"coverage_by_direction"`
	CoverageByHorizon    map[string]int    `json:"coverage_by_horizon"`
	CoverageByProfile    map[string]int    `json:"coverage_by_profile"`
	LedgerStatusCounts   map[string]int    `json:"ledger_status_counts"`
	EventCardHash        string            `json:"event_card_hash"`
	ExperimentHash       string            `json:"experiment_hash"`
	CoverageHash         string            `json:"coverage_hash"`
	ManifestHash         string            `json:"manifest_hash"`
	RoundRobinDimensions []string          `json:"round_robin_dimensions"`
	ExamActions          int               `json:"exam_actions"`
}

// RunConfig binds CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion       string   `json:"schema_version"`
	HistoryRoot         string   `json:"history_root"`
	OutputDir           string   `json:"output_dir"`
	MapDirs             []string `json:"map_dirs"`
	MaxFiles            int      `json:"max_files"`
	MaxLines            int      `json:"max_lines"`
	CardLimit           int      `json:"card_limit"`
	ProgressEvery       int      `json:"progress_every"`
	AllowTruncatedSmoke bool     `json:"allow_truncated_smoke"`
	Generation          string   `json:"generation"`
	ProtocolHash        string   `json:"protocol_hash"`
	HistoryDataHash     string   `json:"history_data_hash"`
}

// MapBundle is one verified G6 event-map output directory.
type MapBundle struct {
	Dir        string
	Manifest   eventmap.MapManifest
	Discovery  eventmap.PartitionMap
	Thresholds eventmap.FrozenThresholds
}

// WinnerWorkItem is one discovery winner scheduled for reverse derivation.
type WinnerWorkItem struct {
	ProfileID    string
	ManifestHash string
	CellKey      eventmap.CellKey
	Winner       eventmap.WinnerEntry
	ShockCluster string
	PriorityTier int
}

// WinnerGroup aggregates winners sharing one reverse card bucket.
type WinnerGroup struct {
	Key          string
	ProfileID    string
	ManifestHash string
	CellKey      eventmap.CellKey
	ShockCluster string
	Winners      []eventmap.WinnerEntry
	PriorityTier int
}

// CoverageSummary commits card coverage over winner buckets.
type CoverageSummary struct {
	TotalBuckets         int            `json:"total_buckets"`
	CoveredBuckets       int            `json:"covered_buckets"`
	CoverageByProfile    map[string]int `json:"coverage_by_profile"`
	CoverageByFamily     map[string]int `json:"coverage_by_family"`
	CoverageByDirection  map[string]int `json:"coverage_by_direction"`
	CoverageByHorizon    map[string]int `json:"coverage_by_horizon"`
}

// DefaultDiscoveryMode returns the frozen G6 reverse discovery mode.
func DefaultDiscoveryMode() string { return protocol.DiscoveryMode }

// DefaultUnexplainedStatus returns the frozen unexplained candidate status.
func DefaultUnexplainedStatus() string { return protocol.UnexplainedCandidateStatus }
