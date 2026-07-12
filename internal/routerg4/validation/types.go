package validation

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/reversemine"
)

const (
	SchemaVersionReport    = "lingxi.router-g4-validation.report.v1"
	SchemaVersionSurvivors = "lingxi.router-g4-validation.survivors.v1"
	SchemaVersionReceipt   = "lingxi.router-g4-validation.receipt.v1"
	SchemaVersionConfig    = "lingxi.router-g4-validation.config.v1"

	StatusInconclusive   = "INCONCLUSIVE"
	StatusValidationPass = "VALIDATION_PASS"
	StatusValidationFail = "VALIDATION_FAIL"

	ReasonCost            = "COST"
	ReasonDelay           = "DELAY"
	ReasonConcentration   = "CONCENTRATION"
	ReasonCoverage        = "COVERAGE"
	ReasonNoEdge          = "NO_EDGE"
	ReasonCapacity        = "CAPACITY"
	ReasonMultipleTesting = "MULTIPLE_TESTING"

	SurvivorStatus = "UNEXPLAINED"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion         string   `json:"schema_version"`
	HistoryRoot           string   `json:"history_root"`
	CandidateOutputDir    string   `json:"candidate_output_dir"`
	OutputDir             string   `json:"output_dir"`
	MapDirs               []string `json:"map_dirs"`
	MaxFiles              int      `json:"max_files"`
	MaxLines              int      `json:"max_lines"`
	ProgressEvery         int      `json:"progress_every"`
	Generation            string   `json:"generation"`
	ProtocolHash          string   `json:"protocol_hash"`
	HistoryDataHash       string   `json:"history_data_hash"`
	CandidateManifestHash string   `json:"candidate_manifest_hash"`
}

// FiringSample is one candidate firing on validation partition.
type FiringSample struct {
	Symbol                string    `json:"symbol"`
	DecisionTime          time.Time `json:"decision_time"`
	Day                   string    `json:"day"`
	GrossBPS              float64   `json:"gross_bps"`
	FeeNetBPS             float64   `json:"fee_net_bps"`
	ConservativeBPS       float64   `json:"conservative_bps"`
	DelayConservativeBPS  float64   `json:"delay_conservative_bps"`
	StressConservativeBPS float64   `json:"stress_conservative_bps"`
	CapacityFail          bool      `json:"capacity_fail"`
}

// FiringStats aggregates validation firings for one candidate.
type FiringStats struct {
	Count                              int     `json:"count"`
	UniqueDays                         int     `json:"unique_days"`
	UniqueSymbols                      int     `json:"unique_symbols"`
	GrossMeanBPS                       float64 `json:"gross_mean_bps"`
	FeeNetMeanBPS                      float64 `json:"fee_net_mean_bps"`
	ConservativeMeanBPS                float64 `json:"conservative_mean_bps"`
	ConservativePositiveRate           float64 `json:"conservative_positive_rate"`
	ConservativeP10BPS                 float64 `json:"conservative_p10_bps"`
	WorstConservativeBPS               float64 `json:"worst_conservative_bps"`
	DelayConservativeMeanBPS           float64 `json:"delay_conservative_mean_bps"`
	StressConservativeMeanBPS          float64 `json:"stress_conservative_mean_bps"`
	MaxPositiveContributionFraction    float64 `json:"max_positive_contribution_fraction"`
	MaxDayPositiveContributionFraction float64 `json:"max_day_positive_contribution_fraction"`
	CapacityFailRate                   float64 `json:"capacity_fail_rate"`
	NonzeroBlocks                      int     `json:"-"`
	PositiveBlocks                     int     `json:"-"`
}

// CandidateResult is one frozen candidate validation outcome.
type CandidateResult struct {
	Ordinal            int                   `json:"ordinal"`
	CanonicalHash      string                `json:"canonical_hash"`
	PhenomenonID       string                `json:"phenomenon_id"`
	ProfileID          string                `json:"profile_id"`
	CellKey            profitmap.CellKey     `json:"cell_key"`
	Status             string                `json:"status"`
	FailReason         string                `json:"fail_reason,omitempty"`
	Firing             FiringStats           `json:"firing"`
	GateChecks         GateChecks            `json:"gate_checks"`
	NonzeroBlocks      int                   `json:"nonzero_blocks"`
	PositiveBlocks     int                   `json:"positive_blocks"`
	SignP              float64               `json:"sign_p"`
	BHLimit            float64               `json:"bh_limit"`
	BHPass             bool                  `json:"bh_pass"`
	MaxDayContribution float64               `json:"max_day_contribution"`
	Candidate          reversemine.Candidate `json:"candidate"`
}

// GateChecks records protocol validation gate outcomes.
type GateChecks struct {
	MinOpportunitiesMet      bool `json:"min_opportunities_met"`
	MinUniqueDaysMet         bool `json:"min_unique_days_met"`
	MinUniqueSymbolsMet      bool `json:"min_unique_symbols_met"`
	FeeNetMeanPositive       bool `json:"fee_net_mean_positive"`
	ConservativeMeanPositive bool `json:"conservative_mean_positive"`
	MaxContributionOK        bool `json:"max_contribution_ok"`
	MaxDayContributionOK     bool `json:"max_day_contribution_ok"`
	MinIndependentBlocksMet  bool `json:"min_independent_blocks_met"`
	DelayMeanPositive        bool `json:"delay_mean_positive"`
	StressMeanNonNegative    bool `json:"stress_mean_non_negative"`
}

// ValidationReport is g4_validation_report.json.
type ValidationReport struct {
	SchemaVersion             string            `json:"schema_version"`
	Generation                string            `json:"generation"`
	ProtocolHash              string            `json:"protocol_hash"`
	CandidateManifestHash     string            `json:"candidate_manifest_hash"`
	CandidatesHash            string            `json:"candidates_hash"`
	LedgerHash                string            `json:"ledger_hash"`
	MapManifestHashes         map[string]string `json:"map_manifest_hashes"`
	CatalogHash               string            `json:"catalog_hash"`
	ConfigHash                string            `json:"config_hash"`
	CandidateCount            int               `json:"candidate_count"`
	PassCount                 int               `json:"pass_count"`
	CandidatePassCount        int               `json:"candidate_pass_count"`
	FailCount                 int               `json:"fail_count"`
	InconclusiveCount         int               `json:"inconclusive_count"`
	UniquePhenomenonPassCount int               `json:"unique_phenomenon_pass_count"`
	Results                   []CandidateResult `json:"results"`
	ReportHash                string            `json:"report_hash"`
	ExamActions               int               `json:"exam_actions"`
	CreatedAt                 time.Time         `json:"created_at"`
}

// ValidationSurvivors is validation_survivors.json.
type ValidationSurvivors struct {
	SchemaVersion         string          `json:"schema_version"`
	Generation            string          `json:"generation"`
	ProtocolHash          string          `json:"protocol_hash"`
	CandidateManifestHash string          `json:"candidate_manifest_hash"`
	ReportHash            string          `json:"report_hash"`
	Survivors             []SurvivorEntry `json:"survivors"`
	SurvivorsHash         string          `json:"survivors_hash"`
	ExamActions           int             `json:"exam_actions"`
}

// SurvivorEntry is one validation survivor still marked UNEXPLAINED.
type SurvivorEntry struct {
	Ordinal       int                     `json:"ordinal"`
	CanonicalHash string                  `json:"canonical_hash"`
	PhenomenonID  string                  `json:"phenomenon_id"`
	ProfileID     string                  `json:"profile_id"`
	CellKey       profitmap.CellKey       `json:"cell_key"`
	Status        string                  `json:"status"`
	Conditions    []reversemine.Condition `json:"conditions"`
	Firing        FiringStats             `json:"firing"`
}

// ValidationReceipt is validation_receipt.json binding provenance.
type ValidationReceipt struct {
	SchemaVersion         string            `json:"schema_version"`
	Generation            string            `json:"generation"`
	ProtocolHash          string            `json:"protocol_hash"`
	HistoryDataHash       string            `json:"history_data_hash"`
	CandidateManifestHash string            `json:"candidate_manifest_hash"`
	CandidatesHash        string            `json:"candidates_hash"`
	LedgerHash            string            `json:"ledger_hash"`
	MapManifestHashes     map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes     map[string]string `json:"map_artifact_hashes"`
	CatalogHash           string            `json:"catalog_hash"`
	ConfigHash            string            `json:"config_hash"`
	ReportHash            string            `json:"report_hash"`
	SurvivorsHash         string            `json:"survivors_hash"`
	CanonicalDataHashes   map[string]string `json:"canonical_data_hashes"`
	ExamActions           int               `json:"exam_actions"`
	CreatedAt             time.Time         `json:"created_at"`
	ReceiptHash           string            `json:"receipt_hash"`
}

// RunResult holds validation outputs.
type RunResult struct {
	Report    ValidationReport
	Survivors ValidationSurvivors
	Receipt   ValidationReceipt
}

// MapBundle is one verified profit-map output directory.
type MapBundle struct {
	Dir        string
	Manifest   profitmap.MapManifest
	FullMarket bool
	MapScope   string
}

// CandidateBundle is sealed discovery output consumed by validation.
type CandidateBundle struct {
	Dir        string
	Manifest   reversemine.CandidateManifest
	Candidates []reversemine.Candidate
	Ledger     reversemine.CandidateLedger
}

// EventContext is one validation-partition decision point with returns.
type EventContext struct {
	ProfileID             string
	Symbol                string
	DecisionTime          time.Time
	CellKey               profitmap.CellKey
	Features              map[string]float64
	GrossBPS              float64
	FeeNetBPS             float64
	ConservativeBPS       float64
	DelayConservativeBPS  float64
	StressConservativeBPS float64
	CapacityFail          bool
}
