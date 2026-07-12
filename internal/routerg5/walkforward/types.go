package walkforward

import (
	"time"

	"lingxi/internal/routerg5/portfoliosim"
)

const (
	SchemaVersionWFReport       = "lingxi.router-g5-wf.report.v1"
	SchemaVersionWFReceipt      = "lingxi.router-g5-wf.receipt.v1"
	SchemaVersionMechanismReport = "lingxi.router-g5-wf.mechanism.v1"
	SchemaVersionResearchSummary = "lingxi.router-g5-wf.research-summary.v1"
	SchemaVersionConfig         = "lingxi.router-g5-wf.config.v1"
	SchemaVersionCheckpoint     = "lingxi.router-g5-wf.checkpoint.v1"

	StatusSkippedValidationFail = "SKIPPED_VALIDATION_FAIL"
	StatusWFPass                = "WF_PASS"
	StatusWFFail                = "WF_FAIL"
	StatusInconclusive          = "INCONCLUSIVE"

	MechanismUnexplainedPolicyPass = "UNEXPLAINED_POLICY_PASS"

	ReasonValidationNotPass = "VALIDATION_NOT_PASS"
	ReasonValidationTamper  = "VALIDATION_TAMPER"
	ReasonPolicyUnavailable = "POLICY_UNAVAILABLE"
	ReasonFoldNetReturn     = "FOLD_NET_RETURN"
	ReasonWorstFold         = "WORST_FOLD"
	ReasonLOFO              = "LOFO"
	ReasonDelay             = "DELAY"
	ReasonCostStress        = "COST_STRESS"
	ReasonCluster           = "CLUSTER_ROBUSTNESS"
	ReasonDay               = "DAY_ROBUSTNESS"
	ReasonShock             = "SHOCK_ROBUSTNESS"
	ReasonHolm              = "HOLM_FAMILY"
	ReasonPartitionLeak     = "PARTITION_LEAK"
)

// RunConfig binds one WF replay run.
type RunConfig struct {
	SchemaVersion       string `json:"schema_version"`
	ProfileID           string `json:"profile_id"`
	HistoryRoot         string `json:"history_root"`
	PolicyDir           string `json:"policy_dir"`
	FeatureDir          string `json:"feature_dir"`
	ValidationDir       string `json:"validation_dir"`
	OutputDir           string `json:"output_dir"`
	MaxLines            int    `json:"max_lines"`
	Generation          string `json:"generation"`
	ProtocolHash        string `json:"protocol_hash"`
	HistoryDataHash     string `json:"history_data_hash"`
	PolicyHash          string `json:"policy_hash"`
	FeatureHash         string `json:"feature_hash"`
	ExperimentHash      string `json:"experiment_hash"`
	ValidationReportHash string `json:"validation_report_hash"`
}

// Fold is one chronological walk_forward slice with embargo metadata.
type Fold struct {
	Index      int       `json:"index"`
	Timestamps []string  `json:"timestamps"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	RowCount   int       `json:"row_count"`
}

// FoldResult aggregates one fold replay outcome.
type FoldResult struct {
	FoldIndex             int                           `json:"fold_index"`
	Scenarios             []portfoliosim.ScenarioResult `json:"scenarios"`
	BaseNetReturnFraction float64                       `json:"base_net_return_fraction"`
	NetReturnPositive     bool                          `json:"net_return_positive"`
}

// LOFOResult is leave-one-fold-out aggregate check.
type LOFOResult struct {
	ExcludedFoldIndex     int     `json:"excluded_fold_index"`
	NetReturnFraction     float64 `json:"net_return_fraction"`
	NetReturnPositive     bool    `json:"net_return_positive"`
}

// RobustnessChecks records day/shock/symbol cluster leave-one-out gates.
type RobustnessChecks struct {
	DayLeaveOneOutOK     bool `json:"day_leave_one_out_ok"`
	ShockDayPositiveOK   bool `json:"shock_day_positive_ok"`
	SymbolClusterLOO_OK  bool `json:"symbol_cluster_loo_ok"`
}

// AttemptSignResult is one Holm-family member sign test outcome.
type AttemptSignResult struct {
	AttemptOrdinal int     `json:"attempt_ordinal"`
	PolicyHash     string  `json:"policy_hash"`
	Selected       bool    `json:"selected"`
	NonzeroDays    int     `json:"nonzero_days"`
	PositiveDays   int     `json:"positive_days"`
	SignPValue     float64 `json:"sign_p_value"`
	HolmLimit      float64 `json:"holm_limit,omitempty"`
	HolmPass       bool    `json:"holm_pass"`
}

// WFGateChecks records frozen walk-forward gate outcomes.
type WFGateChecks struct {
	AllFoldsNetReturnPositive   bool `json:"all_folds_net_return_positive"`
	WorstFoldNetReturnPositive  bool `json:"worst_fold_net_return_positive"`
	AllLOFONetReturnPositive    bool `json:"all_lofo_net_return_positive"`
	AllDelayNetReturnPositive   bool `json:"all_delay_net_return_positive"`
	CostStressNetReturnPositive bool `json:"cost_stress_net_return_positive"`
	Robustness                  RobustnessChecks `json:"robustness"`
	HolmFamilyPass              bool `json:"holm_family_pass"`
}

// WFReport is g5_wf_report.json.
type WFReport struct {
	SchemaVersion         string                `json:"schema_version"`
	Generation            string                `json:"generation"`
	ProtocolHash          string                `json:"protocol_hash"`
	HistoryDataHash       string                `json:"history_data_hash"`
	ProfileID             string                `json:"profile_id"`
	PolicyHash            string                `json:"policy_hash"`
	FeatureHash           string                `json:"feature_hash"`
	ExperimentHash        string                `json:"experiment_hash"`
	ValidationReportHash  string                `json:"validation_report_hash"`
	ConfigHash            string                `json:"config_hash"`
	Status                string                `json:"status"`
	FailReason            string                `json:"fail_reason,omitempty"`
	FoldCount             int                   `json:"fold_count"`
	PurgeEmbargoHours     float64               `json:"purge_embargo_hours"`
	Folds                 []Fold                `json:"folds"`
	FoldResults           []FoldResult          `json:"fold_results"`
	LOFOResults           []LOFOResult          `json:"lofo_results"`
	GateChecks            WFGateChecks          `json:"gate_checks"`
	AttemptSignResults    []AttemptSignResult   `json:"attempt_sign_results"`
	ExamActions           int                   `json:"exam_actions"`
	CreatedAt             time.Time             `json:"created_at"`
	ReportHash            string                `json:"report_hash"`
}

// MechanismReport is g5_policy_mechanism_report.json.
type MechanismReport struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	PolicyHash           string    `json:"policy_hash"`
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

// ResearchSummary is g5_research_summary.json.
type ResearchSummary struct {
	SchemaVersion        string `json:"schema_version"`
	Generation           string `json:"generation"`
	ProtocolHash         string `json:"protocol_hash"`
	PolicyHash           string `json:"policy_hash"`
	ValidationStatus     string `json:"validation_status"`
	ValidationReportHash string `json:"validation_report_hash"`
	WFStatus             string `json:"wf_status"`
	WFReportHash         string `json:"wf_report_hash"`
	MechanismStatus      string `json:"mechanism_status"`
	MechanismReportHash  string `json:"mechanism_report_hash"`
	TradeAllowed         bool   `json:"trade_allowed"`
	ExamEligible         bool   `json:"exam_eligible"`
	Certified            bool   `json:"certified"`
	ExamActions          int    `json:"exam_actions"`
	SummaryHash          string `json:"summary_hash"`
}

// WFReceipt binds provenance for tamper detection.
type WFReceipt struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	PolicyHash            string            `json:"policy_hash"`
	FeatureHash           string            `json:"feature_hash"`
	ExperimentHash        string            `json:"experiment_hash"`
	ValidationReportHash  string            `json:"validation_report_hash"`
	FeatureManifestHash   string            `json:"feature_manifest_hash"`
	PolicyManifestHash    string            `json:"policy_manifest_hash"`
	ConfigHash            string            `json:"config_hash"`
	WFReportHash          string            `json:"wf_report_hash"`
	MechanismReportHash   string            `json:"mechanism_report_hash"`
	ResearchSummaryHash   string            `json:"research_summary_hash"`
	CanonicalDataHashes   map[string]string `json:"canonical_data_hashes"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ReceiptHash           string            `json:"receipt_hash"`
}

// Checkpoint binds resume state with policy/feature/validation integrity.
type Checkpoint struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ProtocolHash         string    `json:"protocol_hash"`
	HistoryDataHash      string    `json:"history_data_hash"`
	ConfigHash           string    `json:"config_hash"`
	PolicyHash           string    `json:"policy_hash"`
	FeatureHash          string    `json:"feature_hash"`
	ExperimentHash       string    `json:"experiment_hash"`
	ValidationReportHash string    `json:"validation_report_hash"`
	FeatureManifestHash  string    `json:"feature_manifest_hash"`
	PolicyManifestHash   string    `json:"policy_manifest_hash"`
	OutputDir            string    `json:"output_dir"`
	NextFoldIndex        int       `json:"next_fold_index"`
	Done                 bool      `json:"done"`
	SavedAt              time.Time `json:"saved_at"`
	CheckpointHash       string    `json:"checkpoint_hash"`
}
