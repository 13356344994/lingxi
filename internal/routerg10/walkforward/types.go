package walkforward

import (
	"time"

	g10validation "lingxi/internal/routerg10/validation"
)

const (
	SchemaVersionWFReport        = "lingxi.router-g10-wf.report.v1"
	SchemaVersionWFSummary       = "lingxi.router-g10-wf.summary.v1"
	SchemaVersionMechanismReport = "lingxi.router-g10-wf.mechanism.v1"
	SchemaVersionExecutionReport = "lingxi.router-g10-wf.execution.v1"
	SchemaVersionStageReceipt    = "lingxi.router-g10-wf.stage-receipt.v1"
	SchemaVersionConfig          = "lingxi.router-g10-wf.config.v1"
	SchemaVersionCheckpoint      = "lingxi.router-g10-wf.checkpoint.v1"

	WFMaxDrawdownFraction       = 0.30
	WFMaxCapacityFailFraction = 0.50

	StatusSkippedValidationFail = "SKIPPED_VALIDATION_FAIL"
	StatusWFPass                = "WF_PASS"
	StatusWFFail                = "WF_FAIL"
	StatusInconclusive          = "INCONCLUSIVE"

	MechanismUnexplainedPersonalEdge = "UNEXPLAINED_PERSONAL_EDGE"

	ReasonValidationNotPass    = "VALIDATION_NOT_PASS"
	ReasonValidationTamper     = "VALIDATION_TAMPER"
	ReasonPlaybookUnavailable  = "PLAYBOOK_UNAVAILABLE"
	ReasonFoldNetReturn        = "FOLD_NET_RETURN"
	ReasonWorstFold            = "WORST_FOLD"
	ReasonLOFO                 = "LOFO"
	ReasonDelay                = "DELAY"
	ReasonCostStress           = "COST_STRESS"
	ReasonCluster              = "CLUSTER_ROBUSTNESS"
	ReasonDay                  = "DAY_ROBUSTNESS"
	ReasonShock                = "SHOCK_ROBUSTNESS"
	ReasonSymbol               = "SYMBOL_ROBUSTNESS"
	ReasonHolm                 = "HOLM_FAMILY"
	ReasonMaxDrawdown          = "MAX_DRAWDOWN"
	ReasonCapacity             = "CAPACITY"
	ReasonLeverageSolvency     = "LEVERAGE_SOLVENCY"
	ReasonInsufficientEvents   = "INSUFFICIENT_EVENTS"
	ReasonBrainDegeneracy      = "BRAIN_DEGENERATE"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion        string   `json:"schema_version"`
	HistoryRoot          string   `json:"history_root"`
	PlaybookOutputDir    string   `json:"playbook_output_dir"`
	ValidationDir        string   `json:"validation_dir"`
	OutputDir            string   `json:"output_dir"`
	ResidualMapDirs      []string `json:"residual_map_dirs"`
	MaxFiles             int      `json:"max_files"`
	MaxLines             int      `json:"max_lines"`
	ProgressEvery        int      `json:"progress_every"`
	Generation           string   `json:"generation"`
	ProtocolHash         string   `json:"protocol_hash"`
	HistoryDataHash      string   `json:"history_data_hash"`
	PlaybookManifestHash string   `json:"playbook_manifest_hash"`
	ValidationReportHash string   `json:"validation_report_hash"`
	SurvivorsHash        string   `json:"survivors_hash"`
	FingerprintPath      string   `json:"fingerprint_path"`
	FingerprintHash      string   `json:"fingerprint_hash"`
	AllowTruncatedSmoke  bool     `json:"allow_truncated_smoke"`
}

// Fold is one chronological walk_forward slice with embargo metadata.
type Fold struct {
	Index      int       `json:"index"`
	Timestamps []string  `json:"timestamps"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	EventCount int       `json:"event_count"`
}

// TierScenarioResult aliases validation tier scenario for WF reports.
type TierScenarioResult = g10validation.TierScenarioResult

// FoldResult aggregates one fold replay outcome for one capital tier.
type FoldResult struct {
	FoldIndex             int                  `json:"fold_index"`
	CapitalTier           int                  `json:"capital_tier"`
	Scenarios             []TierScenarioResult `json:"scenarios"`
	BaseNetReturnFraction float64              `json:"base_net_return_fraction"`
	NetReturnNonNegative  bool                 `json:"net_return_non_negative"`
}

// LOFOResult is leave-one-fold-out aggregate check.
type LOFOResult struct {
	CapitalTier           int     `json:"capital_tier"`
	ExcludedFoldIndex     int     `json:"excluded_fold_index"`
	NetReturnFraction     float64 `json:"net_return_fraction"`
	NetReturnNonNegative  bool    `json:"net_return_non_negative"`
}

// RobustnessChecks records day/shock/symbol leave-one-out gates.
type RobustnessChecks struct {
	DayLeaveOneOutOK    bool `json:"day_leave_one_out_ok"`
	ShockDayPositiveOK  bool `json:"shock_day_positive_ok"`
	SymbolClusterLOO_OK bool `json:"symbol_cluster_loo_ok"`
}

// AttemptSignResult is one Holm-family member sign test outcome.
type AttemptSignResult struct {
	AttemptOrdinal int     `json:"attempt_ordinal"`
	PlaybookHash   string  `json:"playbook_hash,omitempty"`
	Selected       bool    `json:"selected"`
	NonzeroDays    int     `json:"nonzero_days"`
	PositiveDays   int     `json:"positive_days"`
	SignPValue     float64 `json:"sign_p_value"`
	HolmLimit      float64 `json:"holm_limit,omitempty"`
	HolmPass       bool    `json:"holm_pass"`
}

// WFGateChecks records frozen walk-forward gate outcomes.
type WFGateChecks struct {
	AllFoldsNetReturnNonNegative   bool             `json:"all_folds_net_return_non_negative"`
	WorstFoldNetReturnNonNegative  bool             `json:"worst_fold_net_return_non_negative"`
	AllLOFONetReturnNonNegative    bool             `json:"all_lofo_net_return_non_negative"`
	AllDelayNetReturnNonNegative   bool             `json:"all_delay_net_return_non_negative"`
	CostStressNetReturnNonNegative bool             `json:"cost_stress_net_return_non_negative"`
	Robustness                     RobustnessChecks `json:"robustness"`
	MaxDrawdownOK                  bool             `json:"max_drawdown_ok"`
	CapacityFailRateOK             bool             `json:"capacity_fail_rate_ok"`
	NoLeverageSolvencyBreach       bool             `json:"no_leverage_solvency_breach"`
	HolmFamilyPass                 bool             `json:"holm_family_pass"`
}

// PlaybookWFResult is one survivor playbook WF outcome.
type PlaybookWFResult struct {
	Ordinal              int                 `json:"ordinal"`
	PlaybookHash         string              `json:"playbook_hash"`
	ExperimentHash       string              `json:"experiment_hash,omitempty"`
	ProfileID            string              `json:"profile_id"`
	EventFamily          string              `json:"event_family"`
	Horizon              string              `json:"horizon"`
	Status               string              `json:"status"`
	FailReason           string              `json:"fail_reason,omitempty"`
	FoldCount            int                 `json:"fold_count"`
	PurgeEmbargoHours    float64             `json:"purge_embargo_hours"`
	Folds                []Fold              `json:"folds"`
	FoldResults          []FoldResult        `json:"fold_results"`
	TierGateResults      []TierWFResult      `json:"tier_gate_results,omitempty"`
	LOFOResults          []LOFOResult        `json:"lofo_results"`
	GateChecks           WFGateChecks        `json:"gate_checks"`
	AttemptSignResults   []AttemptSignResult `json:"attempt_sign_results"`
	EligibleCapitalTiers []string            `json:"eligible_capital_tiers"`
	MaxCapacityUSDT      float64             `json:"max_capacity_usdt"`
	TradeAllowed         bool                `json:"trade_allowed"`
	ExamEligible         bool                `json:"exam_eligible"`
	PayerStatus          string              `json:"payer_status"`
	BrainDegeneracy      g10validation.DegeneracySummary `json:"brain_degeneracy,omitempty"`
}

// TierWFResult records per-capital-tier WF gate outcomes.
type TierWFResult struct {
	CapitalTier  int          `json:"capital_tier"`
	Status       string       `json:"status"`
	FailReason   string       `json:"fail_reason,omitempty"`
	GateChecks   WFGateChecks `json:"gate_checks"`
	LOFOResults  []LOFOResult `json:"lofo_results,omitempty"`
}

// WFReport is g10_wf_report.json.
type WFReport struct {
	SchemaVersion        string             `json:"schema_version"`
	Generation           string             `json:"generation"`
	ProtocolHash         string             `json:"protocol_hash"`
	PlaybookHash         string             `json:"playbook_hash"`
	ExperimentHash       string             `json:"experiment_hash"`
	ValidationReportHash string             `json:"validation_report_hash"`
	SurvivorsHash        string             `json:"survivors_hash"`
	ConfigHash           string             `json:"config_hash"`
	Status               string             `json:"status"`
	FailReason           string             `json:"fail_reason,omitempty"`
	SurvivorCount        int                `json:"survivor_count"`
	PassCount            int                `json:"pass_count"`
	FailCount            int                `json:"fail_count"`
	Results              []PlaybookWFResult `json:"results"`
	BrainDegeneracyClear bool               `json:"brain_degeneracy_clear"`
	ExamActions          int                `json:"exam_actions"`
	CreatedAt            time.Time          `json:"created_at"`
	ReportHash           string             `json:"report_hash"`
}

// MechanismReport is g10_event_mechanism_report.json.
type MechanismReport struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	PlaybookHash         string    `json:"playbook_hash"`
	WFReportHash         string    `json:"wf_report_hash"`
	MechanismStatus      string    `json:"mechanism_status"`
	ForcedFlowEvidence   bool      `json:"forced_flow_evidence"`
	CounterpartyEvidence bool      `json:"counterparty_evidence"`
	AutoFromFeatures     bool      `json:"auto_from_features"`
	TradeAllowed         bool      `json:"trade_allowed"`
	ExamEligible         bool      `json:"exam_eligible"`
	Certified            bool      `json:"certified"`
	BrainDegeneracyClear bool      `json:"brain_degeneracy_clear"`
	ExamActions          int       `json:"exam_actions"`
	CreatedAt            time.Time `json:"created_at"`
	ReportHash           string    `json:"report_hash"`
}

// ExecutionReport is g10_execution_report.json summarizing feasibility skips.
type ExecutionReport struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	WFReportHash         string    `json:"wf_report_hash"`
	EventsSeen           int       `json:"events_seen"`
	EventsTraded         int       `json:"events_traded"`
	EventsSkipped        int       `json:"events_skipped"`
	SkipBelowMinNotional int       `json:"skip_below_min_notional"`
	SkipBelowLotFloor    int       `json:"skip_below_lot_floor"`
	SkipCapacity         int       `json:"skip_capacity"`
	SkipConcurrencyCap   int       `json:"skip_concurrency_cap"`
	CapacityFailRate     float64   `json:"capacity_fail_rate"`
	ExamActions          int       `json:"exam_actions"`
	CreatedAt            time.Time `json:"created_at"`
	ReportHash           string    `json:"report_hash"`
}

// StageReceipt binds provenance for one WF stage run.
type StageReceipt struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	HistoryDataHash      string            `json:"history_data_hash"`
	PlaybookManifestHash string            `json:"playbook_manifest_hash"`
	PlaybookHash         string            `json:"playbook_hash"`
	ExperimentHash       string            `json:"experiment_hash"`
	ValidationReportHash string            `json:"validation_report_hash"`
	SurvivorsHash        string            `json:"survivors_hash"`
	MapManifestHashes    map[string]string `json:"map_manifest_hashes"`
	ConfigHash           string            `json:"config_hash"`
	WFReportHash         string            `json:"wf_report_hash"`
	MechanismReportHash  string            `json:"mechanism_report_hash"`
	ExecutionReportHash  string            `json:"execution_report_hash"`
	CanonicalDataHashes  map[string]string `json:"canonical_data_hashes"`
	ExamActions          int               `json:"exam_actions"`
	CreatedAt            time.Time         `json:"created_at"`
	ReceiptHash          string            `json:"receipt_hash"`
}

// Checkpoint binds resume state with validation/survivor integrity.
type Checkpoint struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	HistoryDataHash      string            `json:"history_data_hash"`
	ConfigHash           string            `json:"config_hash"`
	PlaybookManifestHash string            `json:"playbook_manifest_hash"`
	PlaybookHash         string            `json:"playbook_hash"`
	ExperimentHash       string            `json:"experiment_hash"`
	ValidationReportHash string            `json:"validation_report_hash"`
	SurvivorsHash        string            `json:"survivors_hash"`
	MapManifestHashes    map[string]string `json:"map_manifest_hashes"`
	OutputDir            string            `json:"output_dir"`
	NextSurvivorIndex    int               `json:"next_survivor_index"`
	Done                 bool              `json:"done"`
	SavedAt              time.Time         `json:"saved_at"`
	CheckpointHash       string            `json:"checkpoint_hash"`
}

// WFSummary is g10_wf_summary.json.
type WFSummary struct {
	SchemaVersion       string    `json:"schema_version"`
	Generation          string    `json:"generation"`
	ProtocolHash        string    `json:"protocol_hash"`
	WFReportHash        string    `json:"wf_report_hash"`
	MechanismReportHash string    `json:"mechanism_report_hash"`
	ExecutionReportHash string    `json:"execution_report_hash"`
	Status              string    `json:"status"`
	MechanismStatus     string    `json:"mechanism_status"`
	SurvivorCount       int       `json:"survivor_count"`
	PassCount           int       `json:"pass_count"`
	FailCount           int       `json:"fail_count"`
	SkippedCount        int       `json:"skipped_count"`
	CapacityFailRate    float64   `json:"capacity_fail_rate"`
	ExamActions         int       `json:"exam_actions"`
	CreatedAt           time.Time `json:"created_at"`
	SummaryHash         string    `json:"summary_hash"`
}

// RunResult holds WF stage outputs.
type RunResult struct {
	WFReport        WFReport
	Summary         WFSummary
	MechanismReport MechanismReport
	ExecutionReport ExecutionReport
	Receipt         StageReceipt
	Validation      g10validation.ValidationReport
	Survivors       g10validation.ValidationSurvivors
}

// DailyEquityReplay aliases validation daily replay tracker.
type DailyEquityReplay = g10validation.DailyEquityReplay

// ReplayStats aliases validation replay stats.
type ReplayStats = g10validation.ReplayStats

// WFEvent aliases validation replay event on walk_forward partition.
type WFEvent = g10validation.ReplayEvent

// FrozenSizingInput aliases validation sizing input.
type FrozenSizingInput = g10validation.FrozenSizingInput
