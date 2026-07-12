package playbook

import (
	"time"

	"lingxi/internal/routerg10/protocol"
)

const (
	SchemaVersionLedger   = "lingxi.router-g10-playbooks.ledger.v1"
	SchemaVersionPlaybook = "lingxi.router-g10-playbooks.playbook.v1"
	SchemaVersionManifest = "lingxi.router-g10-playbooks.manifest.v1"
	SchemaVersionSummary  = "lingxi.router-g10-playbooks.summary.v1"
	SchemaVersionConfig   = "lingxi.router-g10-playbooks.config.v1"

	LedgerStageDiscovery = "discovery_reverse_playbooks"

	// MaxPlaybookCandidates is the frozen upper bound for outcome-first reverse derivation.
	MaxPlaybookCandidates = 64

	ValidationStagePending = "full_playbook_verification_pending"

	EventCompletionStopError = "event_complete_stop_error"
	PayerStatusUnexplained   = "UNEXPLAINED_PERSONAL_EDGE"
)

// TriggerCondition is one feature-only trigger predicate reverse-derived from residual winners.
type TriggerCondition struct {
	Field string  `json:"field"`
	Op    string  `json:"op"`
	Value float64 `json:"value,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// TriggerSpec holds reverse-derived trigger_conditions for one Event Playbook.
type TriggerSpec struct {
	EventFamily string             `json:"event_family"`
	Conditions  []TriggerCondition `json:"conditions"`
}

// DirectionBranch describes one long/short/flat path; direction is not strategy identity.
type DirectionBranch struct {
	Action    string `json:"action"`
	Thesis    string `json:"thesis"`
	EntryRule string `json:"entry_rule"`
	ExitRule  string `json:"exit_rule"`
}

// DynamicDeadlineSpec describes horizon-based fallback deadline semantics.
type DynamicDeadlineSpec struct {
	Horizon           string `json:"horizon"`
	FallbackOnly      bool   `json:"fallback_only"`
	StopErrorRequired bool   `json:"stop_error_required"`
}

// FrozenEventPlaybook is one sealed reverse-derived event playbook awaiting full verification.
type FrozenEventPlaybook struct {
	SchemaVersion   string              `json:"schema_version"`
	Generation      string              `json:"generation"`
	ProtocolHash    string              `json:"protocol_hash"`
	PlaybookHash    string              `json:"playbook_hash"`
	ExperimentHash  string              `json:"experiment_hash,omitempty"`
	ProfileID       string              `json:"profile_id"`
	Ordinal         int                 `json:"ordinal"`
	EventFamily     string              `json:"event_family"`
	Horizon         string              `json:"horizon"`
	DelayBars       int                 `json:"delay_bars"`
	ShockCluster    string              `json:"shock_cluster"`
	TriggerConditions TriggerSpec       `json:"trigger_conditions"`
	LongBranch      DirectionBranch     `json:"long_branch"`
	ShortBranch     DirectionBranch     `json:"short_branch"`
	FlatBranch      DirectionBranch     `json:"flat_branch"`
	MaxWaitTime     string              `json:"max_wait_time"`
	DynamicDeadline DynamicDeadlineSpec `json:"dynamic_deadline"`
	InvalidatorConditions []string      `json:"invalidator_conditions"`
	CapacityBand    string              `json:"capacity_band"`
	EligibleCapitalTiers []string       `json:"eligible_capital_tiers"`
	PersonalExecutionRequirements []string `json:"personal_execution_requirements"`
	StopConditions  []string            `json:"stop_conditions"`
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
	PlaybookHash        string `json:"playbook_hash,omitempty"`
	ProfileID           string `json:"profile_id"`
	EventFamily         string `json:"event_family"`
	Horizon             string `json:"horizon"`
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

// PlaybookManifest seals inputs/outputs before validation replay.
type PlaybookManifest struct {
	SchemaVersion          string            `json:"schema_version"`
	Generation             string            `json:"generation"`
	ProtocolHash           string            `json:"protocol_hash"`
	HistoryDataHash        string            `json:"history_data_hash"`
	DiscoveryMode          string            `json:"discovery_mode"`
	ConfigHash             string            `json:"config_hash"`
	CatalogHash            string            `json:"catalog_hash"`
	MapManifestHashes      map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes      map[string]string `json:"map_artifact_hashes"`
	PlaybookLimit          int               `json:"playbook_limit"`
	ActualPlaybookCount    int               `json:"actual_playbook_count"`
	PlaybookHash           string            `json:"playbook_hash"`
	ExperimentHash         string            `json:"experiment_hash"`
	CoverageHash           string            `json:"coverage_hash"`
	Coverage               CoverageSummary   `json:"coverage"`
	FreezeBeforeValidation bool              `json:"freeze_before_validation"`
	ExamActions            int               `json:"exam_actions"`
	CreatedAt              time.Time         `json:"created_at"`
	ManifestHash           string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one playbooks run.
type RunSummary struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	DiscoveryMode        string            `json:"discovery_mode"`
	ProfileIDs           []string          `json:"profile_ids"`
	WinnerSeedCount      int               `json:"winner_seed_count"`
	AttemptCount         int               `json:"attempt_count"`
	ActualPlaybookCount  int               `json:"actual_playbook_count"`
	PlaybookLimit        int               `json:"playbook_limit"`
	CoverageByFamily     map[string]int    `json:"coverage_by_family"`
	CoverageByHorizon    map[string]int    `json:"coverage_by_horizon"`
	CoverageByProfile    map[string]int    `json:"coverage_by_profile"`
	LedgerStatusCounts   map[string]int    `json:"ledger_status_counts"`
	PlaybookHash         string            `json:"playbook_hash"`
	ExperimentHash       string            `json:"experiment_hash"`
	CoverageHash         string            `json:"coverage_hash"`
	ManifestHash         string            `json:"manifest_hash"`
	NoCandidatesReason   string            `json:"no_candidates_reason,omitempty"`
	ResidualFailClosedReason string        `json:"residual_fail_closed_reason,omitempty"`
	RoundRobinDimensions []string          `json:"round_robin_dimensions"`
	ExamActions          int               `json:"exam_actions"`
}

// RunConfig binds CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion       string   `json:"schema_version"`
	HistoryRoot         string   `json:"history_root"`
	OutputDir           string   `json:"output_dir"`
	ResidualMapDirs     []string `json:"residual_map_dirs"`
	MaxFiles            int      `json:"max_files"`
	MaxLines            int      `json:"max_lines"`
	PlaybookLimit       int      `json:"playbook_limit"`
	ProgressEvery       int      `json:"progress_every"`
	AllowTruncatedSmoke bool     `json:"allow_truncated_smoke"`
	Generation          string   `json:"generation"`
	ProtocolHash        string   `json:"protocol_hash"`
	HistoryDataHash     string   `json:"history_data_hash"`
}

// CoverageSummary commits playbook coverage over winner buckets.
type CoverageSummary struct {
	TotalBuckets       int            `json:"total_buckets"`
	CoveredBuckets     int            `json:"covered_buckets"`
	CoverageByProfile  map[string]int `json:"coverage_by_profile"`
	CoverageByFamily   map[string]int `json:"coverage_by_family"`
	CoverageByHorizon  map[string]int `json:"coverage_by_horizon"`
}

// DefaultDiscoveryMode returns the frozen G10 reverse discovery mode.
func DefaultDiscoveryMode() string { return protocol.DiscoveryMode }

// DefaultCandidateStatus returns the frozen personal wild candidate status.
func DefaultCandidateStatus() string { return protocol.PersonalWildCandidateStatus }
