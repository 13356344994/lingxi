package exclusionmap

import (
	"time"

	"lingxi/internal/routerg10/residualmap"
)

// MetricBinding binds one exclusion claim to a calculable metric and threshold.
type MetricBinding struct {
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Passed    bool    `json:"passed"`
}

// AxisExclusion is one axis-specific exclusion proof with metric bindings.
type AxisExclusion struct {
	Axis             string                    `json:"axis"`
	Excluded         bool                      `json:"excluded"`
	PrimaryEvidence  string                    `json:"primary_evidence,omitempty"`
	EvidenceBindings map[string]MetricBinding  `json:"evidence_bindings"`
}

// CrowdingStressPoint is one micro-competitor stress evaluation.
type CrowdingStressPoint struct {
	Competitors           int     `json:"competitors"`
	BaselineConservative  float64 `json:"baseline_conservative_bps"`
	StressedConservative  float64 `json:"stressed_conservative_bps"`
	EdgeCollapseBPS       float64 `json:"edge_collapse_bps"`
	QueueCrowdingDetected bool    `json:"queue_crowding_detected"`
}

// CrowdingStress binds the frozen 1/3/5 competitor ladder.
type CrowdingStress struct {
	Levels []CrowdingStressPoint `json:"levels"`
	Passed bool                  `json:"passed"`
}

// Provenance binds upstream artifact hashes used to compute the proof.
type Provenance struct {
	MapManifestHash       string `json:"map_manifest_hash"`
	AnnouncementTapeHash  string `json:"announcement_tape_hash"`
	AnnouncementManifest  string `json:"announcement_manifest_hash"`
	ConstitutionHash      string `json:"constitution_hash"`
	CellKeyHash           string `json:"cell_key_hash,omitempty"`
	DecisionBarIndex      int    `json:"decision_bar_index"`
	RollingQuoteVolume    float64 `json:"rolling_quote_volume_usdt"`
}

// ExclusionProof is the sealed per-event triple-exclusion artifact.
type ExclusionProof struct {
	SchemaVersion      string         `json:"schema_version"`
	Generation         string         `json:"generation"`
	ProtocolHash       string         `json:"protocol_hash"`
	ConstitutionHash   string         `json:"constitution_hash"`
	ExclusionProofHash string         `json:"exclusion_proof_hash"`
	EventID            string         `json:"event_id"`
	Symbol             string         `json:"symbol"`
	ProfileID          string         `json:"profile_id"`
	EventFamily        string         `json:"event_family"`
	Horizon            string         `json:"horizon"`
	Direction          string         `json:"direction"`
	DelayBars          int            `json:"delay_bars"`
	DecisionTime       time.Time      `json:"decision_time"`
	ShockCluster       string         `json:"shock_cluster"`
	Institution        AxisExclusion  `json:"institution"`
	Retail             AxisExclusion  `json:"retail"`
	Bot                AxisExclusion  `json:"bot"`
	CrowdingStress     CrowdingStress `json:"crowding_stress"`
	ForbiddenHits      []string       `json:"forbidden_hits,omitempty"`
	Accepted           bool           `json:"accepted"`
	AcceptReason       string         `json:"accept_reason,omitempty"`
	RejectReasons      []string       `json:"reject_reasons,omitempty"`
	Provenance         Provenance     `json:"provenance"`
}

// TripleExclusionMap is the frozen exclusion map artifact.
type TripleExclusionMap struct {
	SchemaVersion    string           `json:"schema_version"`
	Generation       string           `json:"generation"`
	ProtocolHash     string           `json:"protocol_hash"`
	ConstitutionHash string           `json:"constitution_hash"`
	BuiltAt          time.Time        `json:"built_at"`
	ConfigHash       string           `json:"config_hash"`
	Proofs           []ExclusionProof `json:"proofs"`
	MapHash          string           `json:"map_hash"`
}

// EvaluationResult holds one event evaluation before sealing.
type EvaluationResult struct {
	EventID       string
	Accepted      bool
	AcceptReason  string
	RejectReasons []string
	ForbiddenHits []string
	Institution   AxisExclusion
	Retail        AxisExclusion
	Bot           AxisExclusion
	Crowding      CrowdingStress
	Provenance    Provenance
	Direction     string
	Horizon       string
	DelayBars     int
}

// EventWorkItem is one triggered event scheduled for exclusion evaluation.
type EventWorkItem struct {
	ProfileID    string
	ManifestHash string
	ShockCluster string
	Event        residualmap.TriggeredEventEntry
}

// RunConfig binds CLI knobs for bounded streaming evaluation.
type RunConfig struct {
	SchemaVersion       string   `json:"schema_version"`
	HistoryRoot         string   `json:"history_root"`
	OutputDir           string   `json:"output_dir"`
	MapDirs             []string `json:"map_dirs"`
	AnnouncementDir     string   `json:"announcement_dir"`
	MaxFiles            int      `json:"max_files"`
	MaxLines            int      `json:"max_lines"`
	CandidateLimit      int      `json:"candidate_limit"`
	ProgressEvery       int      `json:"progress_every"`
	AllowTruncatedSmoke bool     `json:"allow_truncated_smoke"`
	Generation          string   `json:"generation"`
	ProtocolHash        string   `json:"protocol_hash"`
	HistoryDataHash     string   `json:"history_data_hash"`
}

// AnnouncementEnvelope carries verified PIT announcement inputs.
type AnnouncementEnvelope struct {
	TapeHash        string
	ManifestHash    string
	Announcements   []announcementRecord
	Incomplete      bool
}

type announcementRecord struct {
	ID          string
	Symbol      string
	Kind        string
	PublishedAt time.Time
	FirstSeenAt time.Time
	EffectiveAt time.Time
	SourceHash  string
}

// ExperimentAttempt records one append-only exclusion evaluation attempt.
type ExperimentAttempt struct {
	AttemptOrdinal     int       `json:"attempt_ordinal"`
	EventID            string    `json:"event_id"`
	ProfileID          string    `json:"profile_id"`
	EventFamily        string    `json:"event_family"`
	Horizon            string    `json:"horizon"`
	Direction          string    `json:"direction"`
	DelayBars          int       `json:"delay_bars"`
	Status             string    `json:"status"`
	StatusReason       string    `json:"status_reason"`
	AcceptReason       string    `json:"accept_reason,omitempty"`
	RejectReasons      []string  `json:"reject_reasons,omitempty"`
	ExclusionProofHash string    `json:"exclusion_proof_hash,omitempty"`
	EvaluatedAt        time.Time `json:"evaluated_at"`
}

// ExperimentLedger is the append-only exclusion evaluation ledger.
type ExperimentLedger struct {
	SchemaVersion           string              `json:"schema_version"`
	Generation              string              `json:"generation"`
	ProtocolHash            string              `json:"protocol_hash"`
	ConstitutionHash        string              `json:"constitution_hash"`
	MapManifestHashes       map[string]string   `json:"map_manifest_hashes"`
	AnnouncementTapeHash    string              `json:"announcement_tape_hash"`
	AnnouncementManifestHash string              `json:"announcement_manifest_hash"`
	Attempts                []ExperimentAttempt `json:"attempts"`
	LedgerHash              string              `json:"ledger_hash"`
}

// MapManifest binds G10P protocol, G6 maps, announcements, and output hashes.
type MapManifest struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	ConfigHash               string            `json:"config_hash"`
	CatalogHash              string            `json:"catalog_hash"`
	ConstitutionHash         string            `json:"constitution_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes        map[string]string `json:"map_artifact_hashes"`
	AnnouncementTapeHash     string            `json:"announcement_tape_hash"`
	AnnouncementManifestHash string            `json:"announcement_manifest_hash"`
	CandidateLimit           int               `json:"candidate_limit"`
	AcceptedCount            int               `json:"accepted_count"`
	RejectedCount            int               `json:"rejected_count"`
	MapHash                  string            `json:"map_hash"`
	LedgerHash               string            `json:"ledger_hash"`
	ArtifactHashes           map[string]string `json:"artifact_hashes"`
	CreatedAt                time.Time         `json:"created_at"`
	ManifestHash             string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one exclusion-map run.
type RunSummary struct {
	SchemaVersion      string         `json:"schema_version"`
	Generation         string         `json:"generation"`
	ProtocolHash       string         `json:"protocol_hash"`
	ConstitutionHash   string         `json:"constitution_hash"`
	TriggeredSeedCount int            `json:"triggered_seed_count"`
	AttemptCount       int            `json:"attempt_count"`
	AcceptedCount      int            `json:"accepted_count"`
	RejectedCount      int            `json:"rejected_count"`
	RejectReasonCounts map[string]int `json:"reject_reason_counts"`
	AcceptReasonCounts map[string]int `json:"accept_reason_counts"`
	CandidateLimit     int            `json:"candidate_limit"`
	MapHash            string         `json:"map_hash"`
	LedgerHash         string         `json:"ledger_hash"`
	ManifestHash       string         `json:"manifest_hash"`
	FailClosedReason   string         `json:"fail_closed_reason,omitempty"`
}
