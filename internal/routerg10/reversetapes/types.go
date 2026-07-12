package reversetapes

import (
	"time"

	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/exclusionmap"
)

// RunConfig binds CLI knobs for bounded reverse-tape reconstruction.
type RunConfig struct {
	SchemaVersion       string   `json:"schema_version"`
	HistoryRoot         string   `json:"history_root"`
	OutputDir           string   `json:"output_dir"`
	ExclusionMapDir     string   `json:"exclusion_map_dir"`
	EntryProofDir       string   `json:"entry_proof_dir"`
	ExecutionPolicyDir  string   `json:"execution_policy_dir"`
	ExitProofPath       string   `json:"exit_proof_path"`
	AnnouncementDir     string   `json:"announcement_dir"`
	MapDirs             []string `json:"map_dirs"`
	MaxFiles            int      `json:"max_files"`
	MaxLines            int      `json:"max_lines"`
	CandidateLimit      int      `json:"candidate_limit"`
	ProgressEvery       int      `json:"progress_every"`
	AllowTruncatedSmoke bool     `json:"allow_truncated_smoke"`
	Generation          string   `json:"generation"`
	ProtocolHash        string   `json:"protocol_hash"`
	HistoryDataHash     string   `json:"history_data_hash"`
}

// ProofBinding carries verified upstream proof hashes for one event.
type ProofBinding struct {
	EventID              string `json:"event_id"`
	ExclusionProofHash   string `json:"exclusion_proof_hash"`
	EntryProofHash       string `json:"entry_proof_hash"`
	ExecutionPolicyHash  string `json:"execution_policy_hash"`
	ExitProofHash        string `json:"exit_proof_hash"`
	AnnouncementTapeHash string `json:"announcement_tape_hash"`
	ConstitutionHash     string `json:"constitution_hash"`
}

// DecisionBeforeState is the frozen pre-outcome decision snapshot.
type DecisionBeforeState struct {
	Direction              string   `json:"direction"`
	DelayBars              int      `json:"delay_bars"`
	FrozenExecutionAction  string   `json:"frozen_execution_action"`
	DecisionVisibleInfo    string   `json:"decision_visible_info"`
	DynamicMinNotionalUSDT float64  `json:"dynamic_min_notional_usdt"`
	ExecutableDepthUSDT    float64  `json:"executable_depth_usdt"`
	VisibleAnnouncementIDs []string `json:"visible_announcement_ids"`
	InstitutionExclude     string   `json:"institution_exclude_reason,omitempty"`
	RetailExclude          string   `json:"retail_exclude_reason,omitempty"`
	BotExclude             string   `json:"bot_exclude_reason,omitempty"`
	MakerQuadrant          string   `json:"maker_quadrant,omitempty"`
	NoChaseAfterMiss       bool     `json:"no_chase_after_miss"`
	NoLongLivedMaker       bool     `json:"no_long_lived_maker"`
}

// OutcomeLabel carries future-path labels that must not enter decision features.
type OutcomeLabel struct {
	ConservativeNetBPS     float64 `json:"conservative_net_bps"`
	GrossBPS               float64 `json:"gross_bps,omitempty"`
	MakerFilled            bool    `json:"maker_filled"`
	SubsequentlyProfitable bool    `json:"subsequently_profitable,omitempty"`
	MissedProfitableMove   bool    `json:"missed_profitable_move,omitempty"`
	ToxicFillStatus        string  `json:"toxic_fill_status,omitempty"`
	LabelOnly              bool    `json:"label_only"`
}

// ExecutableProfitTape is one sealed reverse-reconstructed profit tape.
type ExecutableProfitTape struct {
	SchemaVersion      string              `json:"schema_version"`
	Generation         string              `json:"generation"`
	ProtocolHash       string              `json:"protocol_hash"`
	TapeHash           string              `json:"tape_hash"`
	EventID            string              `json:"event_id"`
	Symbol             string              `json:"symbol"`
	ProfileID          string              `json:"profile_id"`
	EventFamily        string              `json:"event_family"`
	PersonalTapeFamily string              `json:"personal_tape_family"`
	Horizon            string              `json:"horizon"`
	DecisionTime       time.Time           `json:"decision_time"`
	ShockCluster       string              `json:"shock_cluster"`
	Status             string              `json:"status"`
	ProofBinding       ProofBinding        `json:"proof_binding"`
	DecisionBefore     DecisionBeforeState `json:"decision_before"`
	OutcomeLabel       OutcomeLabel        `json:"outcome_label"`
	AbsoluteProfitUSDT float64             `json:"absolute_profit_usdt"`
	MaxCapacityUSDT    float64             `json:"max_capacity_usdt"`
	MaxDrawdownUSDT    float64             `json:"max_drawdown_usdt"`
	TerminalLossUSDT   float64             `json:"terminal_loss_usdt"`
	RealizedFill       bool                `json:"realized_fill"`
	FillOrMissReason   string              `json:"fill_or_miss_reason,omitempty"`
	DiscoveryMode      string              `json:"discovery_mode"`
}

// AnnouncementContext carries PIT announcement inputs for evaluation.
type AnnouncementContext struct {
	TapeHash     string
	ManifestHash string
	Incomplete   bool
	Items        []announcements.Announcement
}

// FromExclusionEnvelope converts verified exclusion-map announcement envelope.
func FromExclusionEnvelope(env exclusionmap.AnnouncementEnvelope) AnnouncementContext {
	return AnnouncementContext{
		TapeHash:     env.TapeHash,
		ManifestHash: env.ManifestHash,
		Incomplete:   env.Incomplete,
		Items:        env.PITItems(),
	}
}

// EvaluationInput binds verified upstream artifacts for one exclusion proof.
type EvaluationInput struct {
	Exclusion        exclusionmap.ExclusionProof
	Entry            g10entryproof.EntryProof
	Execution        g10executionpolicy.FrozenPolicy
	Exit             g10exitproof.Proof
	Announcements    AnnouncementContext
	OutcomeLabel     OutcomeLabel
	ConstitutionHash string
	CapitalUSDT      float64
	AllTriggersDenominator int
}

// EvaluationResult holds reconstruction outcome before sealing.
type EvaluationResult struct {
	Accepted      bool
	Status        string
	RejectReasons []string
	Tape          ExecutableProfitTape
}

// ExperimentAttempt records one append-only reverse-tape attempt.
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
	TapeHash       string    `json:"tape_hash,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

// ExperimentLedger is the append-only reverse-tape ledger.
type ExperimentLedger struct {
	SchemaVersion            string              `json:"schema_version"`
	Generation               string              `json:"generation"`
	ProtocolHash             string              `json:"protocol_hash"`
	ConstitutionHash         string              `json:"constitution_hash"`
	ExclusionMapHash         string              `json:"exclusion_map_hash"`
	ExclusionManifestHash    string              `json:"exclusion_manifest_hash"`
	EntryProofManifestHash   string              `json:"entry_proof_manifest_hash"`
	ExecutionManifestHash    string              `json:"execution_manifest_hash"`
	AnnouncementTapeHash     string              `json:"announcement_tape_hash"`
	AnnouncementManifestHash string              `json:"announcement_manifest_hash"`
	Attempts                 []ExperimentAttempt `json:"attempts"`
	LedgerHash               string              `json:"ledger_hash"`
}

// TapeManifest binds upstream hashes and output artifacts.
type TapeManifest struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	ConfigHash               string            `json:"config_hash"`
	CatalogHash              string            `json:"catalog_hash"`
	ConstitutionHash         string            `json:"constitution_hash"`
	ExclusionMapHash         string            `json:"exclusion_map_hash"`
	ExclusionManifestHash    string            `json:"exclusion_manifest_hash"`
	EntryProofManifestHash   string            `json:"entry_proof_manifest_hash"`
	ExecutionManifestHash    string            `json:"execution_manifest_hash"`
	AnnouncementTapeHash     string            `json:"announcement_tape_hash"`
	AnnouncementManifestHash string            `json:"announcement_manifest_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	CandidateLimit           int               `json:"candidate_limit"`
	ExecutableCount          int               `json:"executable_count"`
	DiscoveryCount           int               `json:"discovery_count"`
	RejectedCount            int               `json:"rejected_count"`
	LedgerHash               string            `json:"ledger_hash"`
	ArtifactHashes           map[string]string `json:"artifact_hashes"`
	CreatedAt                time.Time         `json:"created_at"`
	ManifestHash             string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one reverse-tapes run.
type RunSummary struct {
	SchemaVersion      string         `json:"schema_version"`
	Generation         string         `json:"generation"`
	ProtocolHash       string         `json:"protocol_hash"`
	ConstitutionHash   string         `json:"constitution_hash"`
	SeedCount          int            `json:"seed_count"`
	AttemptCount       int            `json:"attempt_count"`
	ExecutableCount    int            `json:"executable_count"`
	DiscoveryCount     int            `json:"discovery_count"`
	RejectedCount      int            `json:"rejected_count"`
	RejectReasonCounts map[string]int `json:"reject_reason_counts"`
	CandidateLimit     int            `json:"candidate_limit"`
	LedgerHash         string         `json:"ledger_hash"`
	ManifestHash       string         `json:"manifest_hash"`
	FailClosedReason   string         `json:"fail_closed_reason,omitempty"`
}

// InputBundle holds verified upstream artifacts.
type InputBundle struct {
	ExclusionMap      exclusionmap.TripleExclusionMap
	ExclusionManifest exclusionmap.MapManifest
	EntryProofs       map[string]g10entryproof.EntryProof
	EntryManifest     g10entryproof.Manifest
	ExecutionPolicies map[string]g10executionpolicy.FrozenPolicy
	ExecutionManifest g10executionpolicy.Manifest
	ExitProofs        map[string]g10exitproof.Proof
	Announcements     AnnouncementContext
	OutcomeLabels     map[string]OutcomeLabel
	MapManifestHashes map[string]string
	ConstitutionHash  string
}
