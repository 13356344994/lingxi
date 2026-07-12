package portfoliosim

import (
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
)

const (
	SchemaVersionReport   = "lingxi.router-g5-validation.report.v1"
	SchemaVersionReceipt  = "lingxi.router-g5-validation.receipt.v1"
	SchemaVersionConfig   = "lingxi.router-g5-validation.config.v1"
	SchemaVersionCheckpoint = "lingxi.router-g5-validation.checkpoint.v1"

	StatusValidationPass       = "VALIDATION_PASS"
	StatusValidationFail       = "VALIDATION_FAIL"
	StatusInconclusive         = "INCONCLUSIVE"
	StatusSkippedPolicyUnavailable = "SKIPPED_POLICY_UNAVAILABLE"

	ReasonCoverage       = "COVERAGE_INCONCLUSIVE"
	ReasonNoEdge         = "NO_EDGE"
	ReasonDrawdown       = "DRAWDOWN"
	ReasonConcentration  = "CONCENTRATION"
	ReasonLeverage       = "LEVERAGE_SOLVENCY"
	ReasonSolvency       = "SOLVENCY"
	ReasonDelay          = "DELAY"
	ReasonCost           = "COST_STRESS"
	ReasonCapacity       = "CAPACITY"
	ReasonFixedAction    = "FIXED_ACTION"
	ReasonNoGo           = "NO_GO"
)

// RunConfig binds one validation replay run.
type RunConfig struct {
	SchemaVersion       string  `json:"schema_version"`
	ProfileID           string  `json:"profile_id"`
	HistoryRoot         string  `json:"history_root"`
	PolicyDir           string  `json:"policy_dir"`
	FeatureDir          string  `json:"feature_dir"`
	OutputDir           string  `json:"output_dir"`
	MaxFiles            int     `json:"max_files"`
	MaxLines            int     `json:"max_lines"`
	CatalogSymbolLimit  int     `json:"catalog_symbol_limit"`
	InitialEquityUSDT   float64 `json:"initial_equity_usdt"`
	ScenarioDelayBars   int     `json:"scenario_delay_bars"`
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	Generation          string  `json:"generation"`
	ProtocolHash        string  `json:"protocol_hash"`
	HistoryDataHash     string  `json:"history_data_hash"`
	PolicyHash          string  `json:"policy_hash"`
	FeatureHash         string  `json:"feature_hash"`
	ExperimentHash      string  `json:"experiment_hash"`
}

// ScenarioResult is one equity/delay/stress replay outcome.
type ScenarioResult struct {
	InitialEquityUSDT    float64 `json:"initial_equity_usdt"`
	DelayBars            int     `json:"delay_bars"`
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	FinalEquityUSDT      float64 `json:"final_equity_usdt"`
	NetReturnFraction    float64 `json:"net_return_fraction"`
	MaxDrawdownFraction  float64 `json:"max_drawdown_fraction"`
	ActiveDays           int     `json:"active_days"`
	TurnoverUSDT         float64 `json:"turnover_usdt"`
	FundingPaidUSDT      float64 `json:"funding_paid_usdt"`
	FundingMissing       bool    `json:"funding_missing"`
	NoTradeFraction      float64 `json:"no_trade_fraction"`
	ActionCoverage       map[string]int `json:"action_coverage"`
	CapacityRejectRate   float64 `json:"capacity_reject_rate"`
	LeverageBreach       bool    `json:"leverage_breach"`
	SolvencyBreach       bool    `json:"solvency_breach"`
	MaxSymbolContribution float64 `json:"max_symbol_positive_contribution_fraction"`
	EquityCurveSummary   EquityCurveSummary `json:"equity_curve_summary"`
}

// EquityCurveSummary is a compact equity path digest.
type EquityCurveSummary struct {
	StartEquity float64 `json:"start_equity_usdt"`
	EndEquity   float64 `json:"end_equity_usdt"`
	PeakEquity  float64 `json:"peak_equity_usdt"`
	TroughEquity float64 `json:"trough_equity_usdt"`
	Points      int     `json:"points"`
}

// GateChecks records frozen validation gate outcomes.
type GateChecks struct {
	BaseNetReturnPositive      bool   `json:"base_net_return_positive"`
	AllDelayNetReturnPositive  bool   `json:"all_delay_net_return_positive"`
	AllEquityTiersPositive     bool   `json:"all_equity_tiers_positive"`
	MaxDrawdownOK              bool   `json:"max_drawdown_ok"`
	NoLeverageSolvencyBreach   bool   `json:"no_leverage_solvency_breach"`
	MinActiveDaysMet           bool   `json:"min_active_days_met"`
	MaxSymbolContributionOK    bool   `json:"max_symbol_contribution_ok"`
	SummaryNote                string `json:"summary_note,omitempty"`
}

// ValidationReport is g5_validation_report.json.
type ValidationReport struct {
	SchemaVersion    string           `json:"schema_version"`
	Generation       string           `json:"generation"`
	ProtocolHash     string           `json:"protocol_hash"`
	HistoryDataHash  string           `json:"history_data_hash"`
	ProfileID        string           `json:"profile_id"`
	PolicyHash       string           `json:"policy_hash"`
	FeatureHash      string           `json:"feature_hash"`
	ExperimentHash   string           `json:"experiment_hash"`
	ConfigHash       string           `json:"config_hash"`
	Status           string           `json:"status"`
	FailReason       string           `json:"fail_reason,omitempty"`
	SummaryNote      string           `json:"summary_note,omitempty"`
	GateChecks       GateChecks       `json:"gate_checks"`
	Scenarios        []ScenarioResult `json:"scenarios"`
	ActionCoverage   map[string]int   `json:"action_coverage"`
	NoTradeFraction  float64          `json:"no_trade_fraction"`
	TurnoverUSDT     float64          `json:"turnover_usdt"`
	FundingPaidUSDT  float64          `json:"funding_paid_usdt"`
	FundingMissing   bool             `json:"funding_missing"`
	CapacityRejectRate float64        `json:"capacity_reject_rate"`
	ExamActions      int              `json:"exam_actions"`
	CreatedAt        time.Time        `json:"created_at"`
	ReportHash       string           `json:"report_hash"`
}

// ValidationReceipt binds provenance for tamper detection.
type ValidationReceipt struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	PolicyHash            string            `json:"policy_hash"`
	FeatureHash           string            `json:"feature_hash"`
	ExperimentHash        string            `json:"experiment_hash"`
	FeatureManifestHash   string            `json:"feature_manifest_hash"`
	PolicyManifestHash    string            `json:"policy_manifest_hash"`
	ConfigHash            string            `json:"config_hash"`
	ReportHash            string            `json:"report_hash"`
	FundingPaidUSDT       float64           `json:"funding_paid_usdt"`
	FundingMissing        bool              `json:"funding_missing"`
	CanonicalDataHashes   map[string]string `json:"canonical_data_hashes"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ReceiptHash           string            `json:"receipt_hash"`
}

// Account tracks concurrent portfolio state.
type Account struct {
	CashUSDT      float64
	EquityUSDT    float64
	InitialEquity float64
	Positions     map[string]actionspace.PositionState
	PeakEquity    float64
	MaxDrawdown   float64
	TurnoverUSDT  float64
	ActiveDays    map[string]bool
	SymbolPnL     map[string]float64
	ActionCounts  map[string]int
	NoTradeBars   int
	TotalBars     int
	LeverageBreach bool
	SolvencyBreach bool
	CapacityRejects int
	TotalProposals  int
	StaleRejects    int
	PendingClips    int
	PendingRejects  int
	FundingPaidUSDT float64
	FundingMissing  bool
}

// ReplayInput binds one bar decision replay slice.
type ReplayInput struct {
	Policy      reverselearn.FrozenPolicy
	Horizons    []string
	DecisionTime time.Time
	Rows        []policy.DecisionInput
	Prices      map[string]float64
}
