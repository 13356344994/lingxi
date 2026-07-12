package reversemine

import (
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

const (
	SchemaVersionLedger    = "lingxi.router-g4-reverse-mine.ledger.v1"
	SchemaVersionCandidate = "lingxi.router-g4-reverse-mine.candidate.v1"
	SchemaVersionManifest  = "lingxi.router-g4-reverse-mine.manifest.v1"
	SchemaVersionSummary   = "lingxi.router-g4-reverse-mine.summary.v1"
	SchemaVersionConfig    = "lingxi.router-g4-reverse-mine.config.v1"

	// Compatibility aliases are protocol-owned; no local version literals.
	SearchSpaceID          = protocol.ReverseMineSearchSpaceID
	DerivationAlgorithmVer = protocol.DerivationAlgorithmVersion
	CandidateStatus        = "UNEXPLAINED_CANDIDATE"
	WinnerSeedMode         = "top_winner_biased_hypothesis_seed"

	LedgerStageDiscovery = "discovery_reverse_mine"
)

// RuleOp is the comparison operator for one pre-decision feature threshold.
type RuleOp string

const (
	OpLTE RuleOp = "lte_quantile"
	OpGTE RuleOp = "gte_quantile"
)

// Condition is one frozen quantile threshold predicate.
type Condition struct {
	FeatureKey string  `json:"feature_key"`
	Op         RuleOp  `json:"op"`
	Threshold  float64 `json:"threshold"`
	Quantile   float64 `json:"quantile"`
}

// Candidate is one frozen reverse-mined rule hypothesis.
type Candidate struct {
	Ordinal             int               `json:"ordinal"`
	ProfileID           string            `json:"profile_id"`
	CellKey             profitmap.CellKey `json:"cell_key"`
	Conditions          []Condition       `json:"conditions"`
	CanonicalHash       string            `json:"canonical_hash"`
	Status              string            `json:"status"`
	SourceWinnerCount   int               `json:"source_winner_count"`
	SourceWinnerIDsHash string            `json:"source_winner_ids_hash"`
	MapManifestHash     string            `json:"map_manifest_hash"`
	WinnerSeedMode      string            `json:"winner_seed_mode"`
	SearchSpaceID       string            `json:"search_space_id"`
	DerivationAlgorithm string            `json:"derivation_algorithm_version"`
	PhenomenonID        string            `json:"phenomenon_id"`
}

// ValidationEvent is one decision-point feature vector for rule firing tests.
// Only decision-before features and exact cell dimensions are allowed.
type ValidationEvent struct {
	ProfileID string             `json:"profile_id"`
	CellKey   profitmap.CellKey  `json:"cell_key"`
	Features  map[string]float64 `json:"features"`
	Decision  time.Time          `json:"decision_time,omitempty"`
}

// LedgerEntry records one append-only derivation attempt.
type LedgerEntry struct {
	AttemptOrdinal      int               `json:"attempt_ordinal"`
	EnumeratorCursor    string            `json:"enumerator_cursor"`
	CanonicalRuleHash   string            `json:"canonical_rule_hash"`
	ProfileID           string            `json:"profile_id"`
	CellKey             profitmap.CellKey `json:"cell_key"`
	Status              string            `json:"status"`
	StatusReason        string            `json:"status_reason"`
	LedgerStage         string            `json:"ledger_stage"`
	RuleKind            string            `json:"rule_kind,omitempty"`
	Conditions          []Condition       `json:"conditions,omitempty"`
	SourceWinnerCount   int               `json:"source_winner_count,omitempty"`
	SourceWinnerIDsHash string            `json:"source_winner_ids_hash,omitempty"`
	PriorityTier        int               `json:"priority_tier,omitempty"`
}

// CandidateLedger is the sealed append-only discovery ledger.
type CandidateLedger struct {
	SchemaVersion       string        `json:"schema_version"`
	Generation          string        `json:"generation"`
	ProtocolHash        string        `json:"protocol_hash"`
	SearchSpaceID       string        `json:"search_space_id"`
	DerivationAlgorithm string        `json:"derivation_algorithm_version"`
	WinnerSeedMode      string        `json:"winner_seed_mode"`
	Entries             []LedgerEntry `json:"entries"`
	LedgerHash          string        `json:"ledger_hash"`
}

// CandidateManifest seals all inputs and outputs before validation.
type CandidateManifest struct {
	SchemaVersion          string            `json:"schema_version"`
	Generation             string            `json:"generation"`
	ProtocolHash           string            `json:"protocol_hash"`
	HistoryDataHash        string            `json:"history_data_hash"`
	SearchSpaceID          string            `json:"search_space_id"`
	DerivationAlgorithm    string            `json:"derivation_algorithm_version"`
	ConfigHash             string            `json:"config_hash"`
	CatalogHash            string            `json:"catalog_hash"`
	MapManifestHashes      map[string]string `json:"map_manifest_hashes"`
	MapArtifactHashes      map[string]string `json:"map_artifact_hashes"`
	CandidateLimit         int               `json:"candidate_limit"`
	ActualCandidateCount   int               `json:"actual_candidate_count"`
	CandidatesHash         string            `json:"candidates_hash"`
	LedgerHash             string            `json:"ledger_hash"`
	CoverageHash           string            `json:"coverage_hash"`
	Coverage               CoverageSummary   `json:"coverage"`
	FreezeBeforeValidation bool              `json:"freeze_before_validation"`
	ExamActions            int               `json:"exam_actions"`
	CreatedAt              time.Time         `json:"created_at"`
	ManifestHash           string            `json:"manifest_hash"`
}

// MineSummary is the audit receipt for one reverse-mine run.
type MineSummary struct {
	SchemaVersion                string              `json:"schema_version"`
	Generation                   string              `json:"generation"`
	ProtocolHash                 string              `json:"protocol_hash"`
	SearchSpaceID                string              `json:"search_space_id"`
	DerivationAlgorithm          string              `json:"derivation_algorithm_version"`
	ProfileIDs                   []string            `json:"profile_ids"`
	DiscoveryCellCount           int                 `json:"discovery_cell_count"`
	TotalCells                   int                 `json:"total_cells"`
	CoveredCells                 int                 `json:"covered_cells"`
	CoverageByProfile            map[string]int      `json:"coverage_by_profile"`
	CoverageByHorizon            map[string]int      `json:"coverage_by_horizon"`
	CoverageByDirection          map[string]int      `json:"coverage_by_direction"`
	CoverageByCapital            map[string]int      `json:"coverage_by_capital"`
	CoverageByPriority           map[string]int      `json:"coverage_by_priority"`
	CandidatesByFeature          map[string]int      `json:"candidates_by_feature"`
	CandidatesByProfile          map[string]int      `json:"candidates_by_profile"`
	CandidatesByHorizon          map[string]int      `json:"candidates_by_horizon"`
	CandidatesByDirection        map[string]int      `json:"candidates_by_direction"`
	CandidatesByCapital          map[string]int      `json:"candidates_by_capital"`
	DeclaredDistinctFeatureNames []string            `json:"declared_distinct_feature_names"`
	AvailableProfileFeatureSlots map[string][]string `json:"available_profile_feature_slots"`
	CoveredDistinctFeatureNames  []string            `json:"covered_distinct_feature_names"`
	MissingDeclaredFeatureNames  []string            `json:"missing_declared_feature_names"`
	CoverageHash                 string              `json:"coverage_hash"`
	WinnerSeedCount              int                 `json:"winner_seed_count"`
	AttemptCount                 int                 `json:"attempt_count"`
	ActualCandidateCount         int                 `json:"actual_candidate_count"`
	CandidateCount               int                 `json:"candidate_count"`
	UniquePhenomenonCount        int                 `json:"unique_phenomenon_count"`
	CapitalTiersByPhenomenon     map[string][]int    `json:"capital_tiers_by_phenomenon"`
	SubpathCount                 int                 `json:"subpath_count"`
	CandidateLimit               int                 `json:"candidate_limit"`
	SingleFeatureCount           int                 `json:"single_feature_count"`
	DualFeatureCount             int                 `json:"dual_feature_count"`
	LedgerStatusCounts           map[string]int      `json:"ledger_status_counts"`
	CandidatesHash               string              `json:"candidates_hash"`
	LedgerHash                   string              `json:"ledger_hash"`
	CandidateManifestHash        string              `json:"candidate_manifest_hash"`
	RoundRobinDimensions         []string            `json:"round_robin_dimensions"`
	ExamActions                  int                 `json:"exam_actions"`
}

// RunConfig binds CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion   string   `json:"schema_version"`
	HistoryRoot     string   `json:"history_root"`
	OutputDir       string   `json:"output_dir"`
	MapDirs         []string `json:"map_dirs"`
	MaxFiles        int      `json:"max_files"`
	MaxLines        int      `json:"max_lines"`
	CandidateLimit  int      `json:"candidate_limit"`
	ProgressEvery   int      `json:"progress_every"`
	Generation      string   `json:"generation"`
	ProtocolHash    string   `json:"protocol_hash"`
	HistoryDataHash string   `json:"history_data_hash"`
}

// MapBundle is one verified profit-map output directory.
type MapBundle struct {
	Dir        string
	Manifest   profitmap.MapManifest
	Discovery  profitmap.PartitionMap
	FullMarket bool
	Scope      string
}

// CellWorkItem is one discovery cell scheduled for reverse mining.
type CellWorkItem struct {
	ProfileID    string
	ManifestHash string
	Key          profitmap.CellKey
	Stats        profitmap.CellStats
	Winners      []profitmap.WinnerEntry
	PriorityTier int
}

// WinnerFeatureMatrix preserves winner identity so dual thresholds can only
// use winners where both features are simultaneously available.
type WinnerFeatureMatrix map[string]map[string]map[string]float64

// CoverageSummary commits candidate coverage over discovery cells.
type CoverageSummary struct {
	TotalCells                   int                 `json:"total_cells"`
	CoveredCells                 int                 `json:"covered_cells"`
	CoverageByProfile            map[string]int      `json:"coverage_by_profile"`
	CoverageByHorizon            map[string]int      `json:"coverage_by_horizon"`
	CoverageByDirection          map[string]int      `json:"coverage_by_direction"`
	CoverageByCapital            map[string]int      `json:"coverage_by_capital"`
	CoverageByPriority           map[string]int      `json:"coverage_by_priority"`
	CandidatesByFeature          map[string]int      `json:"candidates_by_feature"`
	CandidatesByProfile          map[string]int      `json:"candidates_by_profile"`
	CandidatesByHorizon          map[string]int      `json:"candidates_by_horizon"`
	CandidatesByDirection        map[string]int      `json:"candidates_by_direction"`
	CandidatesByCapital          map[string]int      `json:"candidates_by_capital"`
	DeclaredDistinctFeatureNames []string            `json:"declared_distinct_feature_names"`
	AvailableProfileFeatureSlots map[string][]string `json:"available_profile_feature_slots"`
	CoveredDistinctFeatureNames  []string            `json:"covered_distinct_feature_names"`
	MissingDeclaredFeatureNames  []string            `json:"missing_declared_feature_names"`
}
