package residualmap

import (
	"time"

	"lingxi/internal/routerg10/fingerprint"
)

// TierCostBreakdown is one frozen tier evaluation with explicit cost components.
type TierCostBreakdown struct {
	TierLabel              string  `json:"tier_label"`
	CapitalUSDT            float64 `json:"capital_usdt"`
	GrossBPS               float64 `json:"gross_bps"`
	FeeBPS                 float64 `json:"fee_bps"`
	SlipBPS                float64 `json:"slip_bps"`
	ImpactBPS              float64 `json:"impact_bps"`
	FundingBPS             float64 `json:"funding_bps"`
	ConservativeBPS        float64 `json:"conservative_bps"`
	CapacityFail           bool    `json:"capacity_fail"`
	MarginTimeFail         bool    `json:"margin_time_fail"`
	ExecutionComplexityFail bool   `json:"execution_complexity_fail"`
}

// ResidualOpportunity is one accepted personal wild residual candidate.
type ResidualOpportunity struct {
	SchemaVersion        string                       `json:"schema_version"`
	Generation           string                       `json:"generation"`
	ProtocolHash         string                       `json:"protocol_hash"`
	ResidualHash         string                       `json:"residual_hash"`
	EventID              string                       `json:"event_id"`
	Symbol               string                       `json:"symbol"`
	ProfileID            string                       `json:"profile_id"`
	EventFamily          string                       `json:"event_family"`
	Horizon              string                       `json:"horizon"`
	Direction            string                       `json:"direction"`
	DelayBars            int                          `json:"delay_bars"`
	DecisionTime         time.Time                    `json:"decision_time"`
	ShockCluster         string                       `json:"shock_cluster"`
	Status               string                       `json:"status"`
	TierCosts            map[string]TierCostBreakdown `json:"tier_costs"`
	EligibleCapitalTiers []string                     `json:"eligible_capital_tiers"`
	MaxCapacityUSDT      float64                      `json:"max_capacity_usdt"`
	CapacityCliffUSDT    float64                      `json:"capacity_cliff_usdt"`
	CapacityCliffReason  string                       `json:"capacity_cliff_reason"`
	LargeTierFailReasons []string                     `json:"large_tier_fail_reasons,omitempty"`
	FingerprintHash      string                       `json:"fingerprint_hash"`
	MapManifestHash      string                       `json:"map_manifest_hash"`
}

// PersonalResidualMap is the frozen map artifact consumable by playbook generation.
type PersonalResidualMap struct {
	SchemaVersion   string                `json:"schema_version"`
	Generation      string                `json:"generation"`
	ProtocolHash    string                `json:"protocol_hash"`
	FingerprintHash string                `json:"fingerprint_hash"`
	BuiltAt         time.Time             `json:"built_at"`
	ConfigHash      string                `json:"config_hash"`
	Opportunities   []ResidualOpportunity `json:"opportunities"`
	MapHash         string                `json:"map_hash"`
}

// EvaluationResult holds tier costs plus acceptance/rejection metadata.
type EvaluationResult struct {
	EventID              string
	Direction            string
	Horizon              string
	DelayBars            int
	TierCosts            map[string]TierCostBreakdown
	EligibleCapitalTiers []string
	MaxCapacityUSDT      float64
	CapacityCliffUSDT    float64
	CapacityCliffReason  string
	LargeTierFailReasons []string
	RejectReasons        []string
	Accepted             bool
}

// RunConfig binds CLI knobs for bounded streaming evaluation.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	HistoryRoot         string `json:"history_root"`
	OutputDir           string `json:"output_dir"`
	FingerprintPath     string `json:"fingerprint_path"`
	MapDirs             []string `json:"map_dirs"`
	MaxFiles            int    `json:"max_files"`
	MaxLines            int    `json:"max_lines"`
	CandidateLimit      int    `json:"candidate_limit"`
	ProgressEvery       int    `json:"progress_every"`
	AllowTruncatedSmoke bool   `json:"allow_truncated_smoke"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
}

// FingerprintEnvelope carries the verified local execution portrait.
type FingerprintEnvelope struct {
	Fingerprint fingerprint.PersonalExecutionFingerprint
	Path        string
}

// EventWorkItem is one triggered event scheduled for personal residual evaluation.
type EventWorkItem struct {
	ProfileID    string
	ManifestHash string
	ShockCluster string
	Event        TriggeredEventEntry
}

// TriggeredEventEntry is one feature-only triggered event without winner labels.
type TriggeredEventEntry struct {
	EventID         string
	Symbol          string
	DecisionTime    time.Time
	EventFamily     string
	Horizon         string
	Direction       string
	DelayBars       int
	CapitalTier     int
	ConservativeBPS float64
	DecisionBarIdx  int
	ShockCluster    string
	MapManifestHash string
}

// WinnerWorkItem is kept for tests that still construct legacy winner-shaped items.
type WinnerWorkItem = EventWorkItem

// WinnerEntry aliases triggered event fields for legacy tests.
type WinnerEntry = TriggeredEventEntry

// ExperimentAttempt records one append-only evaluation attempt.
type ExperimentAttempt struct {
	AttemptOrdinal int       `json:"attempt_ordinal"`
	EventID        string    `json:"event_id"`
	ProfileID      string    `json:"profile_id"`
	EventFamily    string    `json:"event_family"`
	Horizon        string    `json:"horizon"`
	Direction      string    `json:"direction"`
	DelayBars      int       `json:"delay_bars"`
	Status         string    `json:"status"`
	StatusReason   string    `json:"status_reason"`
	RejectReasons  []string  `json:"reject_reasons,omitempty"`
	ResidualHash   string    `json:"residual_hash,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

// ExperimentLedger is the append-only residual evaluation ledger.
type ExperimentLedger struct {
	SchemaVersion     string              `json:"schema_version"`
	Generation        string              `json:"generation"`
	ProtocolHash      string              `json:"protocol_hash"`
	FingerprintHash   string              `json:"fingerprint_hash"`
	MapManifestHashes map[string]string   `json:"map_manifest_hashes"`
	Attempts          []ExperimentAttempt `json:"attempts"`
	LedgerHash        string              `json:"ledger_hash"`
}

// MapManifest binds G10 protocol, G6 maps, fingerprint, and output hashes.
type MapManifest struct {
	SchemaVersion       string            `json:"schema_version"`
	Generation          string            `json:"generation"`
	ProtocolHash        string            `json:"protocol_hash"`
	HistoryDataHash     string            `json:"history_data_hash"`
	ConfigHash          string            `json:"config_hash"`
	CatalogHash         string            `json:"catalog_hash"`
	FingerprintHash     string            `json:"fingerprint_hash"`
	FingerprintPath     string            `json:"fingerprint_path"`
	MapManifestHashes   map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes   map[string]string `json:"map_artifact_hashes"`
	CandidateLimit      int               `json:"candidate_limit"`
	AcceptedCount       int               `json:"accepted_count"`
	RejectedCount       int               `json:"rejected_count"`
	MapHash             string            `json:"map_hash"`
	LedgerHash          string            `json:"ledger_hash"`
	ArtifactHashes      map[string]string `json:"artifact_hashes"`
	CreatedAt           time.Time         `json:"created_at"`
	ManifestHash        string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one residual-map run.
type RunSummary struct {
	SchemaVersion      string         `json:"schema_version"`
	Generation         string         `json:"generation"`
	ProtocolHash       string         `json:"protocol_hash"`
	FingerprintHash    string         `json:"fingerprint_hash"`
	WinnerSeedCount    int            `json:"winner_seed_count"`
	TriggeredSeedCount int            `json:"triggered_seed_count"`
	AttemptCount       int            `json:"attempt_count"`
	AcceptedCount      int            `json:"accepted_count"`
	RejectedCount      int            `json:"rejected_count"`
	RejectReasonCounts map[string]int `json:"reject_reason_counts"`
	CandidateLimit     int            `json:"candidate_limit"`
	MapHash            string         `json:"map_hash"`
	LedgerHash         string         `json:"ledger_hash"`
	ManifestHash       string         `json:"manifest_hash"`
	FailClosedReason   string         `json:"fail_closed_reason,omitempty"`
}
