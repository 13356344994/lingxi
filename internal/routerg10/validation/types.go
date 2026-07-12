package validation

import (
	"time"

	"lingxi/internal/routerg10/playbook"
)

const (
	SchemaVersionReport    = "lingxi.router-g10-validation.report.v1"
	SchemaVersionSurvivors = "lingxi.router-g10-validation.survivors.v1"
	SchemaVersionReceipt   = "lingxi.router-g10-validation.receipt.v1"
	SchemaVersionConfig    = "lingxi.router-g10-validation.config.v1"
	SchemaVersionCheckpoint = "lingxi.router-g10-validation.checkpoint.v1"

	StatusInconclusive   = "INCONCLUSIVE"
	StatusValidationPass = "VALIDATION_PASS"
	StatusValidationFail = "VALIDATION_FAIL"

	ReasonCost            = "COST"
	ReasonDelay           = "DELAY"
	ReasonConcentration   = "CONCENTRATION"
	ReasonCoverage        = "COVERAGE"
	ReasonNoEdge          = "NO_EDGE"
	ReasonMultipleTesting = "MULTIPLE_TESTING"
	ReasonSolvency        = "SOLVENCY"
	ReasonCapacity        = "CAPACITY"
	ReasonBrainDegeneracy = "BRAIN_DEGENERATE"

	SurvivorStatus = "UNEXPLAINED_PERSONAL_EDGE"

	DiscoveryScreenFDRQ = 0.10
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion         string   `json:"schema_version"`
	HistoryRoot           string   `json:"history_root"`
	PlaybookOutputDir     string   `json:"playbook_output_dir"`
	OutputDir             string   `json:"output_dir"`
	ResidualMapDirs       []string `json:"residual_map_dirs"`
	MaxFiles              int      `json:"max_files"`
	MaxLines              int      `json:"max_lines"`
	ProgressEvery         int      `json:"progress_every"`
	Generation            string   `json:"generation"`
	ProtocolHash          string   `json:"protocol_hash"`
	HistoryDataHash       string   `json:"history_data_hash"`
	PlaybookManifestHash  string   `json:"playbook_manifest_hash"`
	FingerprintPath       string   `json:"fingerprint_path"`
	FingerprintHash       string   `json:"fingerprint_hash"`
	AllowTruncatedSmoke   bool     `json:"allow_truncated_smoke"`
}

// ReplayEvent is one validation-partition trigger prepared for account replay.
type ReplayEvent struct {
	EventID           string
	Symbol            string
	Direction         string
	EventFamily       string
	DecisionTime      time.Time
	DecisionBarIndex  int
	Day               string
	ShockCluster      string
	DeadlineBarIndex  int
	CompleteAtBar     int
	HorizonBarCount   int
	PlaybookDelayBars int
	ConservativeBPS   float64
	StressBPS         float64
	CapacityFail      bool
	CapacityCliffUSDT float64
	ReferenceNotional float64
	NoTrade           bool
	BrainAction       string
	MinNotionalUSDT   float64
	LotNotionalUSDT   float64
	ReturnBPS         float64
	PriorReturnBPS    float64
	DirectionDeferred bool
	LongConservativeBPS  float64
	ShortConservativeBPS float64
	LongStressBPS        float64
	ShortStressBPS       float64
}

// DailyEquityReplay tracks per-day equity deltas for sign and concentration tests.
type DailyEquityReplay struct {
	DailyPnL    map[string]float64
	ShockDayPnL map[string]float64
	EventPnL    map[string]float64
	SymbolPnL   map[string]float64
}

// ReplayStats aggregates execution feasibility counters.
type ReplayStats struct {
	EventsSeen           int
	EventsTraded         int
	EventsSkipped        int
	SkipBelowMinNotional int
	SkipBelowLotFloor    int
	SkipCapacity         int
	SkipConcurrencyCap   int
	CapacityFails        int
}

// FrozenSizingInput carries conservative edge for replay sizing.
type FrozenSizingInput struct {
	ConservativeMeanBPS       float64
	StressConservativeMeanBPS float64
	ReferenceNotional         float64
}

// TierScenarioResult is one capital tier × delay × cost-stress replay outcome.
type TierScenarioResult struct {
	CapitalTier          int     `json:"capital_tier"`
	DelayBars            int     `json:"delay_bars"`
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	InitialEquityUSDT    float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT      float64 `json:"final_equity_usdt"`
	NetReturnFraction    float64 `json:"net_return_fraction"`
	NetReturnNonNegative bool    `json:"net_return_non_negative"`
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

// TierReplayStats aggregates validation replay counters for one tier.
type TierReplayStats struct {
	Count                              int     `json:"count"`
	UniqueDays                         int     `json:"unique_days"`
	UniqueSymbols                      int     `json:"unique_symbols"`
	UniqueShockBlocks                  int     `json:"unique_shock_blocks"`
	MaxPositiveContributionFraction    float64 `json:"max_positive_contribution_fraction"`
	MaxDayPositiveContributionFraction float64 `json:"max_day_positive_contribution_fraction"`
	CapacityFailRate                   float64 `json:"capacity_fail_rate"`
	CapacityCliffUSDT                  float64 `json:"capacity_cliff_usdt"`
	NonzeroShockBlocks                 int     `json:"nonzero_shock_blocks"`
	PositiveShockBlocks                int     `json:"positive_shock_blocks"`
}

// TierGateChecks records protocol validation gate outcomes for one capital tier.
type TierGateChecks struct {
	MinEventCountMet              bool   `json:"min_event_count_met"`
	MinIndependentDaysMet         bool   `json:"min_independent_days_met"`
	MinShockBlocksMet             bool   `json:"min_shock_blocks_met"`
	BaseNetReturnNonNegative      bool   `json:"base_net_return_non_negative"`
	AllDelayNetReturnNonNegative  bool   `json:"all_delay_net_return_non_negative"`
	CostStressNetReturnNonNegative bool  `json:"cost_stress_net_return_non_negative"`
	MaxContributionOK             bool   `json:"max_contribution_ok"`
	MaxDayContributionOK          bool   `json:"max_day_contribution_ok"`
	NoLeverageSolvencyBreach      bool   `json:"no_leverage_solvency_breach"`
	ShockSignBlocksMet            bool   `json:"shock_sign_blocks_met"`
	SummaryNote                   string `json:"summary_note,omitempty"`
}

// TierResult is one capital tier validation outcome for a playbook.
type TierResult struct {
	CapitalTier int              `json:"capital_tier"`
	Status      string           `json:"status"`
	FailReason  string           `json:"fail_reason,omitempty"`
	FrozenConservativeMeanBPS       float64      `json:"frozen_conservative_mean_bps"`
	StressConservativeMeanBPS       float64      `json:"stress_conservative_mean_bps,omitempty"`
	Replay      TierReplayStats  `json:"replay"`
	Scenarios   []TierScenarioResult `json:"scenarios"`
	GateChecks  TierGateChecks   `json:"gate_checks"`
}

// PlaybookResult is one Event Playbook validation outcome.
type PlaybookResult struct {
	Ordinal              int                        `json:"ordinal"`
	PlaybookHash         string                     `json:"playbook_hash"`
	ExperimentHash       string                     `json:"experiment_hash,omitempty"`
	ProfileID            string                     `json:"profile_id"`
	EventFamily          string                     `json:"event_family"`
	Horizon              string                     `json:"horizon"`
	Status               string                     `json:"status"`
	FailReason           string                     `json:"fail_reason,omitempty"`
	TierResults          []TierResult               `json:"tier_results"`
	EligibleCapitalTiers []string                   `json:"eligible_capital_tiers"`
	MaxCapacityUSDT      float64                    `json:"max_capacity_usdt"`
	FrozenConservativeMeanBPS       float64         `json:"frozen_conservative_mean_bps,omitempty"`
	FrozenStressConservativeMeanBPS float64         `json:"frozen_stress_conservative_mean_bps,omitempty"`
	SignP                float64                    `json:"sign_p"`
	BHLimit              float64                    `json:"bh_limit"`
	BHPass               bool                       `json:"bh_pass"`
	TradeAllowed         bool                       `json:"trade_allowed"`
	ExamEligible         bool                       `json:"exam_eligible"`
	PayerStatus          string                     `json:"payer_status"`
	BrainDegeneracy      DegeneracySummary          `json:"brain_degeneracy,omitempty"`
	Playbook             playbook.FrozenEventPlaybook `json:"playbook"`
}

// ValidationReport is g10_validation_report.json.
type ValidationReport struct {
	SchemaVersion             string            `json:"schema_version"`
	Generation                string            `json:"generation"`
	ProtocolHash              string            `json:"protocol_hash"`
	PlaybookManifestHash      string            `json:"playbook_manifest_hash"`
	PlaybookHash              string            `json:"playbook_hash"`
	ExperimentHash            string            `json:"experiment_hash"`
	MapManifestHashes         map[string]string `json:"map_manifest_hashes"`
	CatalogHash               string            `json:"catalog_hash"`
	ConfigHash                string            `json:"config_hash"`
	PlaybookCount             int               `json:"playbook_count"`
	PassCount                 int               `json:"pass_count"`
	FailCount                 int               `json:"fail_count"`
	InconclusiveCount         int               `json:"inconclusive_count"`
	DiscoveryScreenFamily     string            `json:"discovery_screen_family"`
	DiscoveryScreenFamilySize int               `json:"discovery_screen_family_size"`
	Results                   []PlaybookResult  `json:"results"`
	BrainDegeneracyClear      bool              `json:"brain_degeneracy_clear"`
	ReportHash                string            `json:"report_hash"`
	ExamActions               int               `json:"exam_actions"`
	CreatedAt                 time.Time         `json:"created_at"`
}

// SurvivorEntry is one validation survivor still marked UNEXPLAINED_PERSONAL_EDGE.
type SurvivorEntry struct {
	Ordinal                         int          `json:"ordinal"`
	PlaybookHash                    string       `json:"playbook_hash"`
	ProfileID                       string       `json:"profile_id"`
	EventFamily                     string       `json:"event_family"`
	Horizon                         string       `json:"horizon"`
	Status                          string       `json:"status"`
	PayerStatus                     string       `json:"payer_status"`
	TradeAllowed                    bool         `json:"trade_allowed"`
	ExamEligible                    bool         `json:"exam_eligible"`
	EligibleCapitalTiers            []string     `json:"eligible_capital_tiers"`
	MaxCapacityUSDT                 float64      `json:"max_capacity_usdt"`
	FrozenConservativeMeanBPS       float64      `json:"frozen_conservative_mean_bps"`
	FrozenStressConservativeMeanBPS float64      `json:"frozen_stress_conservative_mean_bps"`
	TierResults                     []TierResult `json:"tier_results"`
}

// ValidationSurvivors is g10_validation_survivors.json.
type ValidationSurvivors struct {
	SchemaVersion        string          `json:"schema_version"`
	Generation           string          `json:"generation"`
	ProtocolHash         string          `json:"protocol_hash"`
	PlaybookManifestHash string          `json:"playbook_manifest_hash"`
	ReportHash           string          `json:"report_hash"`
	Survivors            []SurvivorEntry `json:"survivors"`
	SurvivorsHash        string          `json:"survivors_hash"`
	ExamActions          int             `json:"exam_actions"`
}

// ValidationReceipt binds provenance for one validation run.
type ValidationReceipt struct {
	SchemaVersion             string            `json:"schema_version"`
	Generation                string            `json:"generation"`
	ProtocolHash              string            `json:"protocol_hash"`
	HistoryDataHash           string            `json:"history_data_hash"`
	PlaybookManifestHash      string            `json:"playbook_manifest_hash"`
	PlaybookHash              string            `json:"playbook_hash"`
	ExperimentHash            string            `json:"experiment_hash"`
	MapManifestHashes         map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes         map[string]string `json:"map_artifact_hashes"`
	CatalogHash               string            `json:"catalog_hash"`
	ConfigHash                string            `json:"config_hash"`
	ReportHash                string            `json:"report_hash"`
	SurvivorsHash             string            `json:"survivors_hash"`
	DiscoveryScreenFamily     string            `json:"discovery_screen_family"`
	DiscoveryScreenFamilySize int               `json:"discovery_screen_family_size"`
	CanonicalDataHashes       map[string]string `json:"canonical_data_hashes"`
	ExamActions               int               `json:"exam_actions"`
	CreatedAt                 time.Time         `json:"created_at"`
	ReceiptHash               string            `json:"receipt_hash"`
}

// RunResult holds validation outputs.
type RunResult struct {
	Report    ValidationReport
	Survivors ValidationSurvivors
	Receipt   ValidationReceipt
}

// PlaybookBundle is sealed playbooks output consumed by validation.
type PlaybookBundle struct {
	Dir      string
	Manifest playbook.PlaybookManifest
	Playbooks []playbook.FrozenEventPlaybook
	Ledger   playbook.ExperimentLedger
}
