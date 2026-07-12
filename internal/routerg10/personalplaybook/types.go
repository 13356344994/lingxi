package personalplaybook

import (
	"time"

	"lingxi/internal/routerg10/reversetapes"
)

// ProofBinding carries the frozen hash chain for one personal wild playbook.
type ProofBinding struct {
	ConstitutionHash     string `json:"constitution_hash"`
	ExclusionProofHash   string `json:"exclusion_proof_hash"`
	InstitutionAxisHash  string `json:"institution_axis_hash"`
	RetailAxisHash       string `json:"retail_axis_hash"`
	BotAxisHash          string `json:"bot_axis_hash"`
	EntryProofHash       string `json:"entry_proof_hash"`
	ExecutionPolicyHash  string `json:"execution_policy_hash"`
	ExitProofHash        string `json:"exit_proof_hash"`
	AnnouncementTapeHash string `json:"announcement_tape_hash"`
}

// PublicTriggerCondition is one feature-only predicate; exclusion evidence is forbidden.
type PublicTriggerCondition struct {
	Field string  `json:"field"`
	Op    string  `json:"op"`
	Value float64 `json:"value,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// PublicTriggerSpec holds decision-visible trigger predicates only.
type PublicTriggerSpec struct {
	PersonalTapeFamily string                   `json:"personal_tape_family"`
	Conditions         []PublicTriggerCondition `json:"conditions"`
}

// ExclusionNarrative explains why institution, retail, and bots do not play.
type ExclusionNarrative struct {
	InstitutionExcludeReason string `json:"institution_exclude_reason"`
	RetailExcludeReason      string `json:"retail_exclude_reason"`
	BotExcludeReason         string `json:"bot_exclude_reason"`
}

// EntryNarrative explains why the user can enter at decision time.
type EntryNarrative struct {
	WhyUserCanEnter          string  `json:"why_user_can_enter"`
	DynamicMinNotionalUSDT   float64 `json:"dynamic_min_notional_usdt"`
	ExecutableDepthUSDT    float64 `json:"executable_depth_usdt"`
	DecisionVisibleInfo      string  `json:"decision_visible_info"`
	VisibleAnnouncementCount int     `json:"visible_announcement_count"`
}

// ExecutionNarrative freezes maker/taker/skip comparison conclusions.
type ExecutionNarrative struct {
	MakerSelectionConclusion string   `json:"maker_selection_conclusion"`
	AllowedActions           []string `json:"allowed_actions"`
	FrozenExecutionAction    string   `json:"frozen_execution_action"`
	MakerQuadrant            string   `json:"maker_quadrant,omitempty"`
	NoChaseAfterMiss         bool     `json:"no_chase_after_miss"`
	NoLongLivedMaker         bool     `json:"no_long_lived_maker"`
}

// RiskNarrative binds max size/capacity and 40%/70% terminal budget ceilings.
type RiskNarrative struct {
	MaxSizeUSDT                     float64 `json:"max_size_usdt"`
	MaxCapacityUSDT                 float64 `json:"max_capacity_usdt"`
	EventTerminalBudgetFraction       float64 `json:"event_terminal_budget_fraction"`
	AccountTerminalBudgetFraction   float64 `json:"account_terminal_budget_fraction"`
	EventTerminalRiskOccupancyPct   float64 `json:"event_terminal_risk_occupancy_pct"`
	AccountTerminalRiskOccupancyPct float64 `json:"account_terminal_risk_occupancy_pct"`
	RiskCeilingIsNotSizingTarget    bool    `json:"risk_ceiling_is_not_sizing_target"`
}

// ExitNarrative describes invalidator, deadline, and exit semantics.
type ExitNarrative struct {
	InvalidatorConditions []string `json:"invalidator_conditions"`
	DeadlineHorizon       string   `json:"deadline_horizon"`
	ExitRule              string   `json:"exit_rule"`
	StopErrorRequired     bool     `json:"stop_error_required"`
}

// StopNarrative binds crowding and capital-expansion stop rules.
type StopNarrative struct {
	CrowdingStopRule         string `json:"crowding_stop_rule"`
	CapitalExpansionStopRule string `json:"capital_expansion_stop_rule"`
}

// PersonalWildPlaybook is one sealed personal wild playbook from a certified tape.
type PersonalWildPlaybook struct {
	SchemaVersion   string `json:"schema_version"`
	Generation      string `json:"generation"`
	ProtocolHash    string `json:"protocol_hash"`
	PlaybookHash    string `json:"playbook_hash"`
	TapeHash        string `json:"tape_hash"`
	SourceEventID   string `json:"source_event_id"`
	ExperimentHash  string `json:"experiment_hash,omitempty"`
	Ordinal         int    `json:"ordinal"`

	ProofBinding ProofBinding `json:"proof_binding"`

	Symbol             string `json:"symbol"`
	ProfileID          string `json:"profile_id"`
	EventFamily        string `json:"event_family"`
	PersonalTapeFamily string `json:"personal_tape_family"`
	Horizon            string `json:"horizon"`
	DelayBars          int    `json:"delay_bars"`
	Direction          string `json:"direction"`
	ShockCluster       string `json:"shock_cluster"`

	ExclusionNarrative ExclusionNarrative `json:"exclusion_narrative"`
	EntryNarrative     EntryNarrative     `json:"entry_narrative"`
	ExecutionNarrative ExecutionNarrative `json:"execution_narrative"`
	RiskNarrative      RiskNarrative      `json:"risk_narrative"`
	ExitNarrative      ExitNarrative      `json:"exit_narrative"`
	StopNarrative      StopNarrative      `json:"stop_narrative"`
	PublicTrigger      PublicTriggerSpec  `json:"public_trigger"`

	Status              string   `json:"status"`
	NotPersonalReasons  []string `json:"not_personal_reasons,omitempty"`
	ValidationStage     string   `json:"validation_stage"`
	TradeAllowed        bool     `json:"trade_allowed"`
	ExamEligible        bool     `json:"exam_eligible"`
	Certified           bool     `json:"certified"`
	DiscoveryMode       string   `json:"discovery_mode"`
	ExamActions         int      `json:"exam_actions"`
}

// ExperimentAttempt records one append-only derivation attempt.
type ExperimentAttempt struct {
	AttemptOrdinal int       `json:"attempt_ordinal"`
	TapeHash       string    `json:"tape_hash,omitempty"`
	EventID        string    `json:"event_id"`
	Symbol         string    `json:"symbol"`
	ProfileID      string    `json:"profile_id"`
	PersonalTapeFamily string `json:"personal_tape_family"`
	Horizon        string    `json:"horizon"`
	Status         string    `json:"status"`
	StatusReason   string    `json:"status_reason"`
	LedgerStage    string    `json:"ledger_stage"`
	RejectReasons  []string  `json:"reject_reasons,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

// ExperimentLedger is the append-only personal-playbook ledger.
type ExperimentLedger struct {
	SchemaVersion          string              `json:"schema_version"`
	Generation             string              `json:"generation"`
	ProtocolHash           string              `json:"protocol_hash"`
	ConstitutionHash       string              `json:"constitution_hash"`
	ReverseTapesManifestHash string            `json:"reverse_tapes_manifest_hash"`
	ReverseTapesLedgerHash string              `json:"reverse_tapes_ledger_hash"`
	LedgerStage            string              `json:"ledger_stage"`
	DiscoveryMode          string              `json:"discovery_mode"`
	Attempts               []ExperimentAttempt `json:"attempts"`
	LedgerHash             string              `json:"ledger_hash"`
}

// PlaybookManifest seals inputs/outputs before validation replay.
type PlaybookManifest struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	DiscoveryMode            string            `json:"discovery_mode"`
	ConfigHash               string            `json:"config_hash"`
	ReverseTapesManifestHash string            `json:"reverse_tapes_manifest_hash"`
	ReverseTapesLedgerHash   string            `json:"reverse_tapes_ledger_hash"`
	ConstitutionHash         string            `json:"constitution_hash"`
	PlaybookLimit            int               `json:"playbook_limit"`
	CertifiedPlaybookCount   int               `json:"certified_playbook_count"`
	DiscoveryPlaybookCount   int               `json:"discovery_playbook_count"`
	NotPersonalCount         int               `json:"not_personal_count"`
	PlaybookHash             string            `json:"playbook_hash"`
	ExperimentHash           string            `json:"experiment_hash"`
	FreezeBeforeValidation   bool              `json:"freeze_before_validation"`
	ExamActions              int               `json:"exam_actions"`
	ArtifactHashes           map[string]string `json:"artifact_hashes"`
	CreatedAt                time.Time         `json:"created_at"`
	ManifestHash             string            `json:"manifest_hash"`
}

// RunSummary is the audit receipt for one personal-playbooks run.
type RunSummary struct {
	SchemaVersion            string         `json:"schema_version"`
	Generation               string         `json:"generation"`
	ProtocolHash             string         `json:"protocol_hash"`
	DiscoveryMode            string         `json:"discovery_mode"`
	CertifiedTapeCount       int            `json:"certified_tape_count"`
	DiscoveryTapeCount       int            `json:"discovery_tape_count"`
	AttemptCount             int            `json:"attempt_count"`
	CertifiedPlaybookCount   int            `json:"certified_playbook_count"`
	DiscoveryPlaybookCount   int            `json:"discovery_playbook_count"`
	NotPersonalCount         int            `json:"not_personal_count"`
	PlaybookLimit            int            `json:"playbook_limit"`
	LedgerStatusCounts       map[string]int `json:"ledger_status_counts"`
	PlaybookHash             string         `json:"playbook_hash"`
	ExperimentHash           string         `json:"experiment_hash"`
	ManifestHash             string         `json:"manifest_hash"`
	NoCandidatesReason       string         `json:"no_candidates_reason,omitempty"`
	ExamActions              int            `json:"exam_actions"`
}

// RunConfig binds CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion            string `json:"schema_version"`
	ReverseTapesDir          string `json:"reverse_tapes_dir"`
	ReverseTapesManifestHash string `json:"reverse_tapes_manifest_hash"`
	OutputDir                string `json:"output_dir"`
	PlaybookLimit            int    `json:"playbook_limit"`
	ProgressEvery            int    `json:"progress_every"`
	Generation               string `json:"generation"`
	ProtocolHash             string `json:"protocol_hash"`
	HistoryDataHash          string `json:"history_data_hash"`
}

// InputBundle holds verified reverse-tape artifacts.
type InputBundle struct {
	Manifest         reversetapes.TapeManifest
	Ledger           reversetapes.ExperimentLedger
	CertifiedTapes   []reversetapes.ExecutableProfitTape
	DiscoveryTapes   []reversetapes.ExecutableProfitTape
	ConstitutionHash string
}

// TapeSeed is one scheduled certified tape for derivation.
type TapeSeed struct {
	Tape     reversetapes.ExecutableProfitTape
	Priority int
}
