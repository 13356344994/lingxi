package walkforward

import (
	"time"

	"lingxi/internal/routerg6/eventvalidation"
)

const (
	SchemaVersionWFReport        = "lingxi.router-g6-wf.report.v1"
	SchemaVersionWFSummary       = "lingxi.router-g6-wf.summary.v1"
	SchemaVersionMechanismReport = "lingxi.router-g6-wf.mechanism.v1"
	SchemaVersionExecutionReport = "lingxi.router-g6-wf.execution.v1"
	SchemaVersionStageReceipt    = "lingxi.router-g6-wf.stage-receipt.v1"
	SchemaVersionConfig          = "lingxi.router-g6-wf.config.v1"
	SchemaVersionCheckpoint      = "lingxi.router-g6-wf.checkpoint.v1"

	// WFMaxDrawdownFraction mirrors G5 validation max-DD gate for event replay.
	WFMaxDrawdownFraction = 0.30
	// WFMaxCapacityFailFraction mirrors G4 strict WF capacity skip ceiling.
	WFMaxCapacityFailFraction = 0.50

	StatusSkippedValidationFail = "SKIPPED_VALIDATION_FAIL"
	StatusWFPass                = "WF_PASS"
	StatusWFFail                = "WF_FAIL"
	StatusInconclusive          = "INCONCLUSIVE"

	MechanismUnexplainedEventPass = "UNEXPLAINED_EVENT_PASS"

	ReasonValidationNotPass   = "VALIDATION_NOT_PASS"
	ReasonValidationTamper    = "VALIDATION_TAMPER"
	ReasonEventCardUnavailable = "EVENT_CARD_UNAVAILABLE"
	ReasonFoldNetReturn       = "FOLD_NET_RETURN"
	ReasonWorstFold           = "WORST_FOLD"
	ReasonLOFO                = "LOFO"
	ReasonDelay               = "DELAY"
	ReasonCostStress          = "COST_STRESS"
	ReasonCluster             = "CLUSTER_ROBUSTNESS"
	ReasonDay                 = "DAY_ROBUSTNESS"
	ReasonShock               = "SHOCK_ROBUSTNESS"
	ReasonHolm                = "HOLM_FAMILY"
	ReasonMaxDrawdown         = "MAX_DRAWDOWN"
	ReasonCapacity            = "CAPACITY"
	ReasonLeverageSolvency    = "LEVERAGE_SOLVENCY"
	ReasonPartitionLeak       = "PARTITION_LEAK"
	ReasonInsufficientEvents  = "INSUFFICIENT_EVENTS"

	// LotNotionalProxyUSDT is the frozen 5U/step lot floor proxy.
	LotNotionalProxyUSDT = 5.0
	// MinNotionalProxyUSDT is the frozen exchange min-notional proxy.
	MinNotionalProxyUSDT = 5.0
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion        string   `json:"schema_version"`
	HistoryRoot          string   `json:"history_root"`
	CardOutputDir        string   `json:"card_output_dir"`
	ValidationDir        string   `json:"validation_dir"`
	OutputDir            string   `json:"output_dir"`
	MapDirs              []string `json:"map_dirs"`
	MaxFiles             int      `json:"max_files"`
	MaxLines             int      `json:"max_lines"`
	ProgressEvery        int      `json:"progress_every"`
	Generation           string   `json:"generation"`
	ProtocolHash         string   `json:"protocol_hash"`
	HistoryDataHash      string   `json:"history_data_hash"`
	CardManifestHash     string   `json:"card_manifest_hash"`
	SurvivorsHash        string   `json:"survivors_hash"`
	ValidationReportHash string   `json:"validation_report_hash"`
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

// TierScenarioResult is one capital tier × delay × cost-stress replay outcome.
type TierScenarioResult struct {
	CapitalTier          int     `json:"capital_tier"`
	DelayBars            int     `json:"delay_bars"`
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	InitialEquityUSDT    float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT      float64 `json:"final_equity_usdt"`
	NetReturnFraction    float64 `json:"net_return_fraction"`
	NetReturnPositive    bool    `json:"net_return_positive"`
	EventsSeen           int     `json:"events_seen"`
	EventsTraded         int     `json:"events_traded"`
	EventsSkipped        int     `json:"events_skipped"`
	SkipBelowMinNotional int     `json:"skip_below_min_notional"`
	SkipBelowLotFloor    int     `json:"skip_below_lot_floor"`
	SkipCapacity         int     `json:"skip_capacity"`
	SkipConcurrencyCap   int     `json:"skip_concurrency_cap"`
	MaxDrawdownFraction  float64 `json:"max_drawdown_fraction"`
	LeverageBreach       bool    `json:"leverage_breach"`
	SolvencyBreach       bool    `json:"solvency_breach"`
}

// FoldResult aggregates one fold replay outcome for one capital tier.
type FoldResult struct {
	FoldIndex             int                  `json:"fold_index"`
	CapitalTier           int                  `json:"capital_tier"`
	Scenarios             []TierScenarioResult `json:"scenarios"`
	BaseNetReturnFraction float64              `json:"base_net_return_fraction"`
	NetReturnPositive     bool                 `json:"net_return_positive"`
}

// LOFOResult is leave-one-fold-out aggregate check.
type LOFOResult struct {
	CapitalTier           int     `json:"capital_tier"`
	ExcludedFoldIndex     int     `json:"excluded_fold_index"`
	NetReturnFraction     float64 `json:"net_return_fraction"`
	NetReturnPositive     bool    `json:"net_return_positive"`
}

// RobustnessChecks records day/shock/symbol cluster leave-one-out gates.
type RobustnessChecks struct {
	DayLeaveOneOutOK    bool `json:"day_leave_one_out_ok"`
	ShockDayPositiveOK  bool `json:"shock_day_positive_ok"`
	SymbolClusterLOO_OK bool `json:"symbol_cluster_loo_ok"`
}

// AttemptSignResult is one Holm-family member sign test outcome.
type AttemptSignResult struct {
	AttemptOrdinal int     `json:"attempt_ordinal"`
	EventCardHash  string  `json:"event_card_hash,omitempty"`
	Selected       bool    `json:"selected"`
	NonzeroDays    int     `json:"nonzero_days"`
	PositiveDays   int     `json:"positive_days"`
	SignPValue     float64 `json:"sign_p_value"`
	HolmLimit      float64 `json:"holm_limit,omitempty"`
	HolmPass       bool    `json:"holm_pass"`
}

// WFGateChecks records frozen walk-forward gate outcomes.
type WFGateChecks struct {
	AllFoldsNetReturnPositive   bool             `json:"all_folds_net_return_positive"`
	WorstFoldNetReturnPositive  bool             `json:"worst_fold_net_return_positive"`
	AllLOFONetReturnPositive    bool             `json:"all_lofo_net_return_positive"`
	AllDelayNetReturnPositive   bool             `json:"all_delay_net_return_positive"`
	CostStressNetReturnPositive bool             `json:"cost_stress_net_return_positive"`
	Robustness                  RobustnessChecks `json:"robustness"`
	MaxDrawdownOK               bool             `json:"max_drawdown_ok"`
	CapacityFailRateOK          bool             `json:"capacity_fail_rate_ok"`
	NoLeverageSolvencyBreach    bool             `json:"no_leverage_solvency_breach"`
	HolmFamilyPass              bool             `json:"holm_family_pass"`
}

// CardWFResult is one survivor EventCard WF outcome.
type CardWFResult struct {
	Ordinal              int                 `json:"ordinal"`
	EventCardHash        string              `json:"event_card_hash"`
	ExperimentHash       string              `json:"experiment_hash,omitempty"`
	ProfileID            string              `json:"profile_id"`
	EventFamily          string              `json:"event_family"`
	Horizon              string              `json:"horizon"`
	Direction            string              `json:"direction"`
	Status               string              `json:"status"`
	FailReason           string              `json:"fail_reason,omitempty"`
	FoldCount            int                 `json:"fold_count"`
	PurgeEmbargoHours    float64             `json:"purge_embargo_hours"`
	Folds                []Fold              `json:"folds"`
	FoldResults          []FoldResult        `json:"fold_results"`
	LOFOResults          []LOFOResult        `json:"lofo_results"`
	GateChecks           WFGateChecks        `json:"gate_checks"`
	AttemptSignResults   []AttemptSignResult `json:"attempt_sign_results"`
	TradeAllowed         bool                `json:"trade_allowed"`
	ExamEligible         bool                `json:"exam_eligible"`
}

// WFReport is g6_wf_report.json.
type WFReport struct {
	SchemaVersion        string         `json:"schema_version"`
	Generation           string         `json:"generation"`
	ProtocolHash         string         `json:"protocol_hash"`
	EventCardHash        string         `json:"event_card_hash"`
	ExperimentHash       string         `json:"experiment_hash"`
	ValidationReportHash string         `json:"validation_report_hash"`
	SurvivorsHash        string         `json:"survivors_hash"`
	ConfigHash           string         `json:"config_hash"`
	Status               string         `json:"status"`
	FailReason           string         `json:"fail_reason,omitempty"`
	SurvivorCount        int            `json:"survivor_count"`
	PassCount            int            `json:"pass_count"`
	FailCount            int            `json:"fail_count"`
	Results              []CardWFResult `json:"results"`
	ExamActions          int            `json:"exam_actions"`
	CreatedAt            time.Time      `json:"created_at"`
	ReportHash           string         `json:"report_hash"`
}

// MechanismReport is g6_event_mechanism_report.json.
type MechanismReport struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	EventCardHash        string    `json:"event_card_hash"`
	WFReportHash         string    `json:"wf_report_hash"`
	MechanismStatus      string    `json:"mechanism_status"`
	ForcedFlowEvidence   bool      `json:"forced_flow_evidence"`
	CounterpartyEvidence bool      `json:"counterparty_evidence"`
	AutoFromFeatures     bool      `json:"auto_from_features"`
	TradeAllowed         bool      `json:"trade_allowed"`
	ExamEligible         bool      `json:"exam_eligible"`
	Certified            bool      `json:"certified"`
	ExamActions          int       `json:"exam_actions"`
	CreatedAt            time.Time `json:"created_at"`
	ReportHash           string    `json:"report_hash"`
}

// ExecutionReport is g6_execution_report.json summarizing feasibility skips.
type ExecutionReport struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	WFReportHash         string    `json:"wf_report_hash"`
	LotNotionalProxyUSDT float64   `json:"lot_notional_proxy_usdt"`
	MinNotionalProxyUSDT float64   `json:"min_notional_proxy_usdt"`
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
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	CardManifestHash      string            `json:"card_manifest_hash"`
	EventCardHash         string            `json:"event_card_hash"`
	ExperimentHash        string            `json:"experiment_hash"`
	ValidationReportHash  string            `json:"validation_report_hash"`
	SurvivorsHash         string            `json:"survivors_hash"`
	MapManifestHashes     map[string]string `json:"map_manifest_hashes"`
	ConfigHash            string            `json:"config_hash"`
	WFReportHash          string            `json:"wf_report_hash"`
	MechanismReportHash   string            `json:"mechanism_report_hash"`
	ExecutionReportHash   string            `json:"execution_report_hash"`
	CanonicalDataHashes   map[string]string `json:"canonical_data_hashes"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ReceiptHash           string            `json:"receipt_hash"`
}

// Checkpoint binds resume state with validation/survivor integrity.
type Checkpoint struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	ConfigHash            string            `json:"config_hash"`
	CardManifestHash      string            `json:"card_manifest_hash"`
	EventCardHash         string            `json:"event_card_hash"`
	ExperimentHash        string            `json:"experiment_hash"`
	ValidationReportHash  string            `json:"validation_report_hash"`
	SurvivorsHash         string            `json:"survivors_hash"`
	MapManifestHashes     map[string]string `json:"map_manifest_hashes"`
	OutputDir             string            `json:"output_dir"`
	NextSurvivorIndex     int               `json:"next_survivor_index"`
	Done                  bool              `json:"done"`
	SavedAt               time.Time         `json:"saved_at"`
	CheckpointHash        string            `json:"checkpoint_hash"`
}

// WFSummary is g6_wf_summary.json — stage-level rollup without per-card detail.
type WFSummary struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	WFReportHash         string    `json:"wf_report_hash"`
	MechanismReportHash  string    `json:"mechanism_report_hash"`
	ExecutionReportHash  string    `json:"execution_report_hash"`
	Status               string    `json:"status"`
	MechanismStatus      string    `json:"mechanism_status"`
	SurvivorCount        int       `json:"survivor_count"`
	PassCount            int       `json:"pass_count"`
	FailCount            int       `json:"fail_count"`
	SkippedCount         int       `json:"skipped_count"`
	TradeAllowedCount    int       `json:"trade_allowed_count"`
	ExamEligibleCount    int       `json:"exam_eligible_count"`
	CapacityFailRate     float64   `json:"capacity_fail_rate"`
	ExamActions          int       `json:"exam_actions"`
	CreatedAt            time.Time `json:"created_at"`
	SummaryHash          string    `json:"summary_hash"`
}

// RunResult holds WF stage outputs.
type RunResult struct {
	WFReport        WFReport
	Summary         WFSummary
	MechanismReport MechanismReport
	ExecutionReport ExecutionReport
	Receipt         StageReceipt
	Validation      eventvalidation.ValidationReport
	Survivors       eventvalidation.ValidationSurvivors
}
