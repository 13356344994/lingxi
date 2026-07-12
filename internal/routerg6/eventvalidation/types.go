package eventvalidation

import (
	"time"

	"lingxi/internal/routerg6/reverseevent"
)

const (
	SchemaVersionReport    = "lingxi.router-g6-validation.report.v1"
	SchemaVersionSurvivors = "lingxi.router-g6-validation.survivors.v1"
	SchemaVersionReceipt   = "lingxi.router-g6-validation.receipt.v1"
	SchemaVersionConfig    = "lingxi.router-g6-validation.config.v1"

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

	SurvivorStatus = "UNEXPLAINED"

	// DiscoveryScreenFDRQ is the frozen BH-FDR q for validation discovery-screen.
	DiscoveryScreenFDRQ = 0.10

	OutcomeWinner  = "winner"
	OutcomeLoss    = "loss"
	OutcomeNeutral = "neutral"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion       string   `json:"schema_version"`
	HistoryRoot         string   `json:"history_root"`
	CardOutputDir       string   `json:"card_output_dir"`
	OutputDir           string   `json:"output_dir"`
	MapDirs             []string `json:"map_dirs"`
	MaxFiles            int      `json:"max_files"`
	MaxLines            int      `json:"max_lines"`
	ProgressEvery       int      `json:"progress_every"`
	Generation          string   `json:"generation"`
	ProtocolHash        string   `json:"protocol_hash"`
	HistoryDataHash     string   `json:"history_data_hash"`
	CardManifestHash    string   `json:"card_manifest_hash"`
	AllowTruncatedSmoke bool     `json:"allow_truncated_smoke"`
}

// FiringSample is one matched validation trigger with evaluated returns.
type FiringSample struct {
	Symbol                string    `json:"symbol"`
	DecisionTime          time.Time `json:"decision_time"`
	Day                   string    `json:"day"`
	ShockCluster          string    `json:"shock_cluster"`
	DelayBars             int       `json:"delay_bars"`
	CapitalTier           int       `json:"capital_tier"`
	OutcomeClass          string    `json:"outcome_class"`
	GrossBPS              float64   `json:"gross_bps"`
	FeeNetBPS             float64   `json:"fee_net_bps"`
	ConservativeBPS       float64   `json:"conservative_bps"`
	StressConservativeBPS float64   `json:"stress_conservative_bps"`
	CapacityFail          bool      `json:"capacity_fail"`
}

// FiringStats aggregates validation firings for one card tier.
type FiringStats struct {
	Count                              int     `json:"count"`
	UniqueDays                         int     `json:"unique_days"`
	UniqueSymbols                      int     `json:"unique_symbols"`
	UniqueShockBlocks                  int     `json:"unique_shock_blocks"`
	GrossMeanBPS                       float64 `json:"gross_mean_bps"`
	FeeNetMeanBPS                      float64 `json:"fee_net_mean_bps"`
	ConservativeMeanBPS                float64 `json:"conservative_mean_bps"`
	StressConservativeMeanBPS          float64 `json:"stress_conservative_mean_bps"`
	MaxPositiveContributionFraction    float64 `json:"max_positive_contribution_fraction"`
	MaxDayPositiveContributionFraction float64 `json:"max_day_positive_contribution_fraction"`
	CapacityFailRate                   float64 `json:"capacity_fail_rate"`
	WinnerCount                        int     `json:"winner_count"`
	LossCount                          int     `json:"loss_count"`
	NeutralCount                       int     `json:"neutral_count"`
	DelayMeanBPS                       map[int]float64 `json:"delay_mean_bps,omitempty"`
	NonzeroShockBlocks                 int     `json:"nonzero_shock_blocks"`
	PositiveShockBlocks                int     `json:"positive_shock_blocks"`
}

// TierGateChecks records protocol gate outcomes for one capital tier.
type TierGateChecks struct {
	MinEventCountMet         bool `json:"min_event_count_met"`
	MinIndependentDaysMet    bool `json:"min_independent_days_met"`
	MinShockBlocksMet        bool `json:"min_shock_blocks_met"`
	BaseNetReturnPositive    bool `json:"base_net_return_positive"`
	AllDelayNetReturnPositive bool `json:"all_delay_net_return_positive"`
	CostStressNetReturnPositive bool `json:"cost_stress_net_return_positive"`
	MaxContributionOK        bool `json:"max_contribution_ok"`
	MaxDayContributionOK     bool `json:"max_day_contribution_ok"`
	ShockSignBlocksMet       bool `json:"shock_sign_blocks_met"`
	SummaryNote              string `json:"summary_note,omitempty"`
}

// TierResult is one capital tier validation outcome for a card.
type TierResult struct {
	CapitalTier int            `json:"capital_tier"`
	Status      string         `json:"status"`
	FailReason  string         `json:"fail_reason,omitempty"`
	Firing      FiringStats    `json:"firing"`
	GateChecks  TierGateChecks `json:"gate_checks"`
}

// CardResult is one EventCard validation outcome.
type CardResult struct {
	Ordinal       int                        `json:"ordinal"`
	EventCardHash string                     `json:"event_card_hash"`
	ExperimentHash string                    `json:"experiment_hash,omitempty"`
	ProfileID     string                     `json:"profile_id"`
	EventFamily   string                     `json:"event_family"`
	Horizon       string                     `json:"horizon"`
	Direction     string                     `json:"direction"`
	Status        string                     `json:"status"`
	FailReason    string                     `json:"fail_reason,omitempty"`
	TierResults   []TierResult               `json:"tier_results"`
	SignP         float64                    `json:"sign_p"`
	BHLimit       float64                    `json:"bh_limit"`
	BHPass        bool                       `json:"bh_pass"`
	TradeAllowed  bool                       `json:"trade_allowed"`
	ExamEligible  bool                       `json:"exam_eligible"`
	PayerStatus   string                     `json:"payer_status"`
	Card          reverseevent.FrozenEventCard `json:"card"`
}

// ValidationReport is g6_validation_report.json.
type ValidationReport struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	CardManifestHash      string            `json:"card_manifest_hash"`
	EventCardHash         string            `json:"event_card_hash"`
	ExperimentHash        string            `json:"experiment_hash"`
	MapManifestHashes     map[string]string `json:"map_manifest_hashes"`
	CatalogHash           string            `json:"catalog_hash"`
	ConfigHash            string            `json:"config_hash"`
	CardCount             int               `json:"card_count"`
	PassCount             int               `json:"pass_count"`
	FailCount             int               `json:"fail_count"`
	InconclusiveCount     int               `json:"inconclusive_count"`
	DiscoveryScreenFamily     string        `json:"discovery_screen_family"`
	DiscoveryScreenFamilySize int           `json:"discovery_screen_family_size"`
	Results               []CardResult      `json:"results"`
	ReportHash            string            `json:"report_hash"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
}

// SurvivorEntry is one validation survivor still marked UNEXPLAINED.
type SurvivorEntry struct {
	Ordinal                         int          `json:"ordinal"`
	EventCardHash                   string       `json:"event_card_hash"`
	ProfileID                       string       `json:"profile_id"`
	EventFamily                     string       `json:"event_family"`
	Horizon                         string       `json:"horizon"`
	Direction                       string       `json:"direction"`
	Status                          string       `json:"status"`
	PayerStatus                     string       `json:"payer_status"`
	TradeAllowed                    bool         `json:"trade_allowed"`
	ExamEligible                    bool         `json:"exam_eligible"`
	FrozenConservativeMeanBPS       float64      `json:"frozen_conservative_mean_bps"`
	FrozenStressConservativeMeanBPS float64      `json:"frozen_stress_conservative_mean_bps"`
	TierResults                     []TierResult `json:"tier_results"`
}

// ValidationSurvivors is g6_validation_survivors.json.
type ValidationSurvivors struct {
	SchemaVersion    string          `json:"schema_version"`
	Generation       string          `json:"generation"`
	ProtocolHash     string          `json:"protocol_hash"`
	CardManifestHash string          `json:"card_manifest_hash"`
	ReportHash       string          `json:"report_hash"`
	Survivors        []SurvivorEntry `json:"survivors"`
	SurvivorsHash    string          `json:"survivors_hash"`
	ExamActions      int             `json:"exam_actions"`
}

// ValidationReceipt binds provenance for one validation run.
type ValidationReceipt struct {
	SchemaVersion       string            `json:"schema_version"`
	Generation          string            `json:"generation"`
	ProtocolHash        string            `json:"protocol_hash"`
	HistoryDataHash     string            `json:"history_data_hash"`
	CardManifestHash    string            `json:"card_manifest_hash"`
	EventCardHash       string            `json:"event_card_hash"`
	ExperimentHash      string            `json:"experiment_hash"`
	MapManifestHashes   map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes   map[string]string `json:"map_artifact_hashes"`
	CatalogHash         string            `json:"catalog_hash"`
	ConfigHash          string            `json:"config_hash"`
	ReportHash          string            `json:"report_hash"`
	SurvivorsHash       string            `json:"survivors_hash"`
	DiscoveryScreenFamily      string     `json:"discovery_screen_family"`
	DiscoveryScreenFamilySize  int        `json:"discovery_screen_family_size"`
	CanonicalDataHashes map[string]string `json:"canonical_data_hashes"`
	ExamActions         int               `json:"exam_actions"`
	CreatedAt           time.Time         `json:"created_at"`
	ReceiptHash         string            `json:"receipt_hash"`
}

// RunResult holds validation outputs.
type RunResult struct {
	Report    ValidationReport
	Survivors ValidationSurvivors
	Receipt   ValidationReceipt
}

// CardBundle is sealed reverse-events output consumed by validation.
type CardBundle struct {
	Dir      string
	Manifest reverseevent.EventCardManifest
	Cards    []reverseevent.FrozenEventCard
	Ledger   reverseevent.ExperimentLedger
}
