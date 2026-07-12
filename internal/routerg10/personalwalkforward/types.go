package personalwalkforward

import (
	"time"

	g10personalvalidation "lingxi/internal/routerg10/personalvalidation"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion        string   `json:"schema_version"`
	HistoryRoot          string   `json:"history_root"`
	PlaybookOutputDir    string   `json:"playbook_output_dir"`
	ValidationDir        string   `json:"validation_dir"`
	OutputDir            string   `json:"output_dir"`
	MapDirs              []string `json:"map_dirs"`
	ReverseTapesDir          string `json:"reverse_tapes_dir"`
	ReverseTapesManifestHash string `json:"reverse_tapes_manifest_hash"`
	AnnouncementDir          string `json:"announcement_dir"`
	AnnouncementManifestHash string `json:"announcement_manifest_hash"`
	ConstitutionHash         string `json:"constitution_hash"`
	EntryProofDir            string `json:"entry_proof_dir"`
	ExecutionPolicyDir       string `json:"execution_policy_dir"`
	ExitProofPath            string `json:"exit_proof_path"`
	ExclusionMapDir          string `json:"exclusion_map_dir"`
	MaxFiles             int      `json:"max_files"`
	MaxLines             int      `json:"max_lines"`
	ProgressEvery        int      `json:"progress_every"`
	Generation           string   `json:"generation"`
	ProtocolHash         string   `json:"protocol_hash"`
	HistoryDataHash      string   `json:"history_data_hash"`
	PlaybookManifestHash string   `json:"playbook_manifest_hash"`
	ValidationReportHash string   `json:"validation_report_hash"`
	SurvivorsHash        string   `json:"survivors_hash"`
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

type TierScenarioResult = g10personalvalidation.TierScenarioResult

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
	CapitalTier          int     `json:"capital_tier"`
	ExcludedFoldIndex    int     `json:"excluded_fold_index"`
	NetReturnFraction    float64 `json:"net_return_fraction"`
	NetReturnNonNegative bool    `json:"net_return_non_negative"`
}

// RobustnessChecks records day/shock/symbol/announcement leave-one-out gates.
type RobustnessChecks struct {
	DayLeaveOneOutOK          bool `json:"day_leave_one_out_ok"`
	ShockDayPositiveOK        bool `json:"shock_day_positive_ok"`
	SymbolClusterLOO_OK       bool `json:"symbol_cluster_loo_ok"`
	AnnouncementLeaveOneOutOK bool `json:"announcement_leave_one_out_ok"`
	ExchangeLeaveOneOutOK     bool `json:"exchange_leave_one_out_ok"`
}

// WFGateChecks records frozen walk-forward gate outcomes.
type WFGateChecks struct {
	AllFoldsNetReturnNonNegative   bool             `json:"all_folds_net_return_non_negative"`
	WorstFoldNetReturnNonNegative  bool             `json:"worst_fold_net_return_non_negative"`
	AllLOFONetReturnNonNegative    bool             `json:"all_lofo_net_return_non_negative"`
	AllDelayNetReturnNonNegative   bool             `json:"all_delay_net_return_non_negative"`
	CostStressNetReturnNonNegative bool             `json:"cost_stress_net_return_non_negative"`
	P90SlippageNetReturnNonNegative bool            `json:"p90_slippage_net_return_non_negative"`
	Robustness                     RobustnessChecks `json:"robustness"`
	MaxDrawdownOK                  bool             `json:"max_drawdown_ok"`
	CapacityFailRateOK             bool             `json:"capacity_fail_rate_ok"`
	NoLeverageSolvencyBreach       bool             `json:"no_leverage_solvency_breach"`
}

// PlaybookWFResult is one survivor playbook WF outcome.
type PlaybookWFResult struct {
	Ordinal              int                              `json:"ordinal"`
	PlaybookHash         string                           `json:"playbook_hash"`
	ExperimentHash       string                           `json:"experiment_hash,omitempty"`
	ProfileID            string                           `json:"profile_id"`
	EventFamily          string                           `json:"event_family"`
	PersonalTapeFamily   string                           `json:"personal_tape_family"`
	Horizon              string                           `json:"horizon"`
	Status               string                           `json:"status"`
	FailReason           string                           `json:"fail_reason,omitempty"`
	Layers               []g10personalvalidation.LayerVerdict `json:"layers"`
	FoldCount            int                              `json:"fold_count"`
	PurgeEmbargoHours    float64                          `json:"purge_embargo_hours"`
	Folds                []Fold                           `json:"folds"`
	FoldResults          []FoldResult                     `json:"fold_results"`
	LOFOResults          []LOFOResult                     `json:"lofo_results"`
	GateChecks           WFGateChecks                     `json:"gate_checks"`
	EligibleCapitalTiers []string                         `json:"eligible_capital_tiers"`
	MaxCapacityUSDT      float64                          `json:"max_capacity_usdt"`
	TradeAllowed         bool                             `json:"trade_allowed"`
	ExamEligible         bool                             `json:"exam_eligible"`
	ExamActions          int                              `json:"exam_actions"`
	BrainDegeneracy      g10personalvalidation.DegeneracySummary `json:"brain_degeneracy,omitempty"`
}

// WFReport is g10_personal_wf_report.json.
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
	TradeAllowed         bool               `json:"trade_allowed"`
	CreatedAt            time.Time          `json:"created_at"`
	ReportHash           string             `json:"report_hash"`
}

// WFSummary is g10_personal_wf_summary.json.
type WFSummary struct {
	SchemaVersion  string    `json:"schema_version"`
	Generation     string    `json:"generation"`
	ProtocolHash   string    `json:"protocol_hash"`
	WFReportHash   string    `json:"wf_report_hash"`
	Status         string    `json:"status"`
	SurvivorCount  int       `json:"survivor_count"`
	PassCount      int       `json:"pass_count"`
	FailCount      int       `json:"fail_count"`
	SkippedCount   int       `json:"skipped_count"`
	ExamActions    int       `json:"exam_actions"`
	TradeAllowed   bool      `json:"trade_allowed"`
	CreatedAt      time.Time `json:"created_at"`
	SummaryHash    string    `json:"summary_hash"`
}

// StageReceipt binds provenance for one personal WF stage run.
type StageReceipt struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	HistoryDataHash      string            `json:"history_data_hash"`
	PlaybookManifestHash string            `json:"playbook_manifest_hash"`
	ValidationReportHash string            `json:"validation_report_hash"`
	SurvivorsHash        string            `json:"survivors_hash"`
	ConfigHash           string            `json:"config_hash"`
	WFReportHash         string            `json:"wf_report_hash"`
	CanonicalDataHashes  map[string]string `json:"canonical_data_hashes"`
	ExamActions          int               `json:"exam_actions"`
	TradeAllowed         bool              `json:"trade_allowed"`
	CreatedAt            time.Time         `json:"created_at"`
	ReceiptHash          string            `json:"receipt_hash"`
}

// RunResult holds WF stage outputs.
type RunResult struct {
	WFReport   WFReport
	Summary    WFSummary
	Receipt    StageReceipt
	Validation g10personalvalidation.ValidationReport
	Survivors  g10personalvalidation.ValidationSurvivors
}

type WFEvent = g10personalvalidation.TriggerEvent
type DailyEquityReplay = g10personalvalidation.DailyEquityReplay
type ReplayStats = g10personalvalidation.ReplayStats
