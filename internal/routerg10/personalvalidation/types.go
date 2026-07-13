package personalvalidation

import (
	"time"

	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/entryproof"
	"lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalplaybook"
)

// RunConfig binds explicit CLI knobs for checkpoint verification.
type RunConfig struct {
	SchemaVersion            string   `json:"schema_version"`
	HistoryRoot              string   `json:"history_root"`
	CatalogPath              string   `json:"catalog_path"`
	PlaybookOutputDir        string   `json:"playbook_output_dir"`
	ReverseTapesDir          string   `json:"reverse_tapes_dir"`
	ReverseTapesManifestHash string   `json:"reverse_tapes_manifest_hash"`
	AnnouncementDir          string   `json:"announcement_dir"`
	AnnouncementManifestHash string   `json:"announcement_manifest_hash"`
	EntryProofDir            string   `json:"entry_proof_dir"`
	ExecutionPolicyDir       string   `json:"execution_policy_dir"`
	ExitProofPath            string   `json:"exit_proof_path"`
	ExclusionMapDir          string   `json:"exclusion_map_dir"`
	OutputDir                string   `json:"output_dir"`
	MapDirs                  []string `json:"map_dirs"`
	MaxFiles                 int      `json:"max_files"`
	MaxLines                 int      `json:"max_lines"`
	ProgressEvery            int      `json:"progress_every"`
	Generation               string   `json:"generation"`
	ProtocolHash             string   `json:"protocol_hash"`
	HistoryDataHash          string   `json:"history_data_hash"`
	PlaybookManifestHash     string   `json:"playbook_manifest_hash"`
	ConstitutionHash         string   `json:"constitution_hash"`
	AllowTruncatedSmoke      bool     `json:"allow_truncated_smoke"`
}

// TriggerEvent is one validation-partition trigger with full execution path metadata.
// Unfilled/missed triggers carry NetPnLUSDT=0 — never winner-only or fill-only.
type TriggerEvent struct {
	EventID            string
	PlaybookHash       string
	Symbol             string
	ProfileID          string
	Direction          string
	EventFamily        string
	PersonalTapeFamily string
	DecisionTime       time.Time
	DecisionBarIndex   int
	Day                string
	ShockCluster       string
	HorizonBarCount    int
	DelayBars          int

	EnterGate          string
	FillOrMissReason   string
	FrozenAction       string
	MakerQuadrant      string
	NetPnLUSDT         float64
	ConservativeBPS    float64
	StressBPS          float64
	CapacityFail       bool
	CapacityCliffUSDT  float64
	ReferenceNotional  float64

	MakerFilled              bool
	MakerTouchedNotFilled    bool
	SubsequentlyProfitable   bool
	PartialFill              bool
	ThroughPrice             bool
	FastFill                 bool
	ToxicRevalidate          bool
	ToxicExit                bool
	ExitProofOK              bool

	PITPublishedContaminated bool
	PITFirstSeenContaminated bool
	CrowdingLevel            int
	CrowdingPassed           bool
	CapitalExpansionStop     bool

	ReturnBPS      float64
	PriorReturnBPS float64
	NoTrade        bool
	BrainAction    string

	EntryGrade       string
	EntryProofBound  bool
	ExecutionProofBound bool
	ExitProofBound   bool
	ExclusionProofBound bool

	// Frozen CML/maker execution params sealed at decision time for replay.
	MaxPriceCap         float64
	MaxSlippageBPS      float64
	MinRemainingEdgeBPS float64
	TimeoutMS           int64
	SpreadBPS           float64
	FeeBPS              float64
	LatencyMS           float64
}

// MakerPathStats aggregates maker quadrants over all triggers (full denominator).
type MakerPathStats struct {
	Quadrants              executionpolicy.MakerQuadrantCounts `json:"quadrants"`
	PFill                  float64                             `json:"p_fill"`
	ExpectedNetGivenFilled float64                             `json:"expected_net_given_filled"`
	FastFillCount          int                                 `json:"fast_fill_count"`
	SlowFillCount          int                                 `json:"slow_fill_count"`
	PartialFillCount       int                                 `json:"partial_fill_count"`
	ThroughPriceCount      int                                 `json:"through_price_count"`
	MakerGateForbid        bool                                `json:"maker_gate_forbid"`
	MakerGateReasons       []string                            `json:"maker_gate_reasons,omitempty"`
}

// PITContaminationReport records announcement published/first_seen pollution tests.
type PITContaminationReport struct {
	TriggersTested           int  `json:"triggers_tested"`
	PublishedLookaheadFails  int  `json:"published_lookahead_fails"`
	FirstSeenLookaheadFails  int  `json:"first_seen_lookahead_fails"`
	FutureEffectiveMisused   int  `json:"future_effective_misused"`
	ContaminationDetected    bool `json:"contamination_detected"`
}

// CrowdingReport records 1/3/5 micro-competitor stress and capacity expansion.
type CrowdingReport struct {
	LevelsTested       []int   `json:"levels_tested"`
	FailuresByLevel    []int   `json:"failures_by_level"`
	CapacityExpansionStops int `json:"capacity_expansion_stops"`
	AnyCrowdingFail    bool    `json:"any_crowding_fail"`
}

// LeaveOneReport records leave-one robustness slices.
type LeaveOneReport struct {
	AnnouncementLOOOK bool `json:"announcement_loo_ok"`
	ExchangeLOOOK     bool `json:"exchange_loo_ok"`
	SymbolLOOOK       bool `json:"symbol_loo_ok"`
	DayLOOOK          bool `json:"day_loo_ok"`
	ShockLOOOK        bool `json:"shock_loo_ok"`
}

// DailyEquityReplay tracks per-day equity deltas for sign and concentration tests.
type DailyEquityReplay struct {
	DailyPnL    map[string]float64
	ShockDayPnL map[string]float64
	EventPnL    map[string]float64
	SymbolPnL   map[string]float64
}

// ReplayStats aggregates execution feasibility counters over all triggers.
type ReplayStats struct {
	TriggersSeen         int
	TriggersTraded       int
	TriggersSkipped      int
	TriggersMissed       int
	SkipBelowMinNotional int
	SkipCapacity         int
	SkipConcurrencyCap   int
	CapacityFails        int
	MissedProfitableMove int
	ToxicRevalidateCount int
	ToxicExitCount       int
}

// TierScenarioResult is one capital tier × delay × cost-stress replay outcome.
type TierScenarioResult struct {
	CapitalTier          int     `json:"capital_tier"`
	DelayBars            int     `json:"delay_bars"`
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	P90SlippageStress    bool    `json:"p90_slippage_stress"`
	InitialEquityUSDT    float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT      float64 `json:"final_equity_usdt"`
	NetReturnFraction    float64 `json:"net_return_fraction"`
	NetReturnNonNegative bool    `json:"net_return_non_negative"`
	TriggersSeen         int     `json:"triggers_seen"`
	TriggersTraded       int     `json:"triggers_traded"`
	TriggersSkipped      int     `json:"triggers_skipped"`
	TriggersMissed       int     `json:"triggers_missed"`
	MaxDrawdownFraction  float64 `json:"max_drawdown_fraction"`
	LeverageBreach       bool    `json:"leverage_breach"`
	SolvencyBreach       bool    `json:"solvency_breach"`
	TerminalRiskCeilingHit bool  `json:"terminal_risk_ceiling_hit"`
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
	MinEventCountMet               bool   `json:"min_event_count_met"`
	MinIndependentDaysMet          bool   `json:"min_independent_days_met"`
	MinShockBlocksMet              bool   `json:"min_shock_blocks_met"`
	BaseNetReturnNonNegative       bool   `json:"base_net_return_non_negative"`
	AllDelayNetReturnNonNegative   bool   `json:"all_delay_net_return_non_negative"`
	CostStressNetReturnNonNegative bool   `json:"cost_stress_net_return_non_negative"`
	P90SlippageNetReturnNonNegative bool  `json:"p90_slippage_net_return_non_negative"`
	MaxContributionOK              bool   `json:"max_contribution_ok"`
	MaxDayContributionOK           bool   `json:"max_day_contribution_ok"`
	NoLeverageSolvencyBreach       bool   `json:"no_leverage_solvency_breach"`
	TerminalRiskCeilingNotTarget   bool   `json:"terminal_risk_ceiling_not_target"`
	PITContaminationClear          bool   `json:"pit_contamination_clear"`
	MakerSelectionBiasClear        bool   `json:"maker_selection_bias_clear"`
	ExitProofClear                 bool   `json:"exit_proof_clear"`
	SummaryNote                    string `json:"summary_note,omitempty"`
}

// LayerVerdict is one hierarchical validation layer outcome.
type LayerVerdict struct {
	Layer      string `json:"layer"`
	Status     string `json:"status"`
	FailReason string `json:"fail_reason,omitempty"`
	Note       string `json:"note,omitempty"`
}

// PlaybookResult is one personal wild playbook validation outcome.
type PlaybookResult struct {
	Ordinal              int                              `json:"ordinal"`
	PlaybookHash         string                           `json:"playbook_hash"`
	ExperimentHash       string                           `json:"experiment_hash,omitempty"`
	ProfileID            string                           `json:"profile_id"`
	EventFamily          string                           `json:"event_family"`
	PersonalTapeFamily   string                           `json:"personal_tape_family"`
	Horizon              string                           `json:"horizon"`
	Status               string                           `json:"status"`
	FailReason           string                           `json:"fail_reason,omitempty"`
	Layers               []LayerVerdict                   `json:"layers"`
	TierResults          []TierResult                     `json:"tier_results"`
	EligibleCapitalTiers []string                         `json:"eligible_capital_tiers"`
	MaxCapacityUSDT      float64                          `json:"max_capacity_usdt"`
	MakerStats           MakerPathStats                   `json:"maker_stats"`
	PITReport            PITContaminationReport           `json:"pit_report"`
	CrowdingReport       CrowdingReport                   `json:"crowding_report"`
	LeaveOne             LeaveOneReport                   `json:"leave_one"`
	BrainDegeneracy      DegeneracySummary                `json:"brain_degeneracy,omitempty"`
	TradeAllowed         bool                             `json:"trade_allowed"`
	ExamEligible         bool                             `json:"exam_eligible"`
	ExamActions          int                              `json:"exam_actions"`
	Playbook             personalplaybook.PersonalWildPlaybook `json:"playbook"`
}

// TierResult is one capital tier validation outcome for a playbook.
type TierResult struct {
	CapitalTier               int                  `json:"capital_tier"`
	Status                    string               `json:"status"`
	FailReason                string               `json:"fail_reason,omitempty"`
	FrozenConservativeMeanBPS float64              `json:"frozen_conservative_mean_bps"`
	StressConservativeMeanBPS float64              `json:"stress_conservative_mean_bps,omitempty"`
	Replay                    TierReplayStats      `json:"replay"`
	Scenarios                 []TierScenarioResult `json:"scenarios"`
	GateChecks                TierGateChecks       `json:"gate_checks"`
}

// ValidationReport is g10_personal_validation_report.json.
type ValidationReport struct {
	SchemaVersion        string            `json:"schema_version"`
	Generation           string            `json:"generation"`
	ProtocolHash         string            `json:"protocol_hash"`
	PlaybookManifestHash string            `json:"playbook_manifest_hash"`
	PlaybookHash         string            `json:"playbook_hash"`
	ExperimentHash       string            `json:"experiment_hash"`
	ConstitutionHash     string            `json:"constitution_hash"`
	MapManifestHashes    map[string]string            `json:"map_manifest_hashes"`
	HistoryScope           map[string]HistoryScopeSummary `json:"history_scope"`
	FormalValidation       bool                         `json:"formal_validation"`
	CatalogHash          string            `json:"catalog_hash"`
	ConfigHash           string            `json:"config_hash"`
	PlaybookCount        int               `json:"playbook_count"`
	PassCount            int               `json:"pass_count"`
	FailCount            int               `json:"fail_count"`
	InconclusiveCount    int               `json:"inconclusive_count"`
	Results              []PlaybookResult  `json:"results"`
	BrainDegeneracyClear bool              `json:"brain_degeneracy_clear"`
	ReportHash           string            `json:"report_hash"`
	ExamActions          int               `json:"exam_actions"`
	TradeAllowed         bool              `json:"trade_allowed"`
	CreatedAt            time.Time         `json:"created_at"`
}

// SurvivorEntry is one validation survivor marked UNEXPLAINED_PERSONAL_EDGE.
type SurvivorEntry struct {
	Ordinal                         int          `json:"ordinal"`
	PlaybookHash                    string       `json:"playbook_hash"`
	ProfileID                       string       `json:"profile_id"`
	EventFamily                     string       `json:"event_family"`
	PersonalTapeFamily              string       `json:"personal_tape_family"`
	Horizon                         string       `json:"horizon"`
	Status                          string       `json:"status"`
	Layers                          []LayerVerdict `json:"layers"`
	EligibleCapitalTiers            []string     `json:"eligible_capital_tiers"`
	MaxCapacityUSDT                 float64      `json:"max_capacity_usdt"`
	FrozenConservativeMeanBPS       float64      `json:"frozen_conservative_mean_bps"`
	FrozenStressConservativeMeanBPS float64      `json:"frozen_stress_conservative_mean_bps"`
	TierResults                     []TierResult `json:"tier_results"`
	TradeAllowed                    bool         `json:"trade_allowed"`
	ExamEligible                    bool         `json:"exam_eligible"`
	ExamActions                     int          `json:"exam_actions"`
}

// ValidationSurvivors is g10_personal_validation_survivors.json.
type ValidationSurvivors struct {
	SchemaVersion        string          `json:"schema_version"`
	Generation           string          `json:"generation"`
	ProtocolHash         string          `json:"protocol_hash"`
	PlaybookManifestHash string          `json:"playbook_manifest_hash"`
	ReportHash           string          `json:"report_hash"`
	Survivors            []SurvivorEntry `json:"survivors"`
	SurvivorsHash        string          `json:"survivors_hash"`
	ExamActions          int             `json:"exam_actions"`
	TradeAllowed         bool            `json:"trade_allowed"`
}

// ValidationReceipt binds provenance for one personal validation run.
type ValidationReceipt struct {
	SchemaVersion            string            `json:"schema_version"`
	Generation               string            `json:"generation"`
	ProtocolHash             string            `json:"protocol_hash"`
	HistoryDataHash          string            `json:"history_data_hash"`
	PlaybookManifestHash     string            `json:"playbook_manifest_hash"`
	PlaybookHash             string            `json:"playbook_hash"`
	ExperimentHash           string            `json:"experiment_hash"`
	ConstitutionHash         string            `json:"constitution_hash"`
	ReverseTapesManifestHash string            `json:"reverse_tapes_manifest_hash"`
	AnnouncementManifestHash string            `json:"announcement_manifest_hash"`
	MapManifestHashes        map[string]string `json:"map_manifest_hashes"`
	CatalogHash              string            `json:"catalog_hash"`
	ConfigHash               string            `json:"config_hash"`
	ReportHash               string            `json:"report_hash"`
	SurvivorsHash            string            `json:"survivors_hash"`
	CanonicalDataHashes      map[string]string `json:"canonical_data_hashes"`
	ExamActions              int               `json:"exam_actions"`
	TradeAllowed             bool              `json:"trade_allowed"`
	CreatedAt                time.Time         `json:"created_at"`
	ReceiptHash              string            `json:"receipt_hash"`
}

// LayerSummary is g10_personal_validation_layers.json rollup.
type LayerSummary struct {
	SchemaVersion string         `json:"schema_version"`
	Generation    string         `json:"generation"`
	ProtocolHash  string         `json:"protocol_hash"`
	ReportHash    string         `json:"report_hash"`
	ByLayer       map[string]int `json:"by_layer"`
	ExamActions   int            `json:"exam_actions"`
	TradeAllowed  bool           `json:"trade_allowed"`
	SummaryHash   string         `json:"summary_hash"`
}

// RunResult holds validation outputs.
type RunResult struct {
	Report    ValidationReport
	Survivors ValidationSurvivors
	Receipt   ValidationReceipt
	Layers    LayerSummary
}

// PlaybookBundle is sealed personal playbooks output consumed by validation.
type PlaybookBundle struct {
	Dir       string
	Manifest  personalplaybook.PlaybookManifest
	Playbooks []personalplaybook.PersonalWildPlaybook
	Ledger    personalplaybook.ExperimentLedger
}

// ProofBundle binds verified upstream proof artifacts for replay.
type ProofBundle struct {
	Announcements           []announcements.Announcement
	ConstitutionHash        string
	EntryProofs             map[string]entryproof.EntryProof
	ExecutionPolicies       map[string]executionpolicy.FrozenPolicy
	ExecutionPoliciesByHash map[string]executionpolicy.FrozenPolicy
	ExitProofs              map[string]exitproof.Proof
	ExclusionCrowding       map[string]exclusionmap.CrowdingStress
	ExclusionProofs         map[string]exclusionmap.ExclusionProof
	AnnouncementIncomplete  bool
}

// HistoryScopeSummary stamps formal catalog selection for red-team rt15.
type HistoryScopeSummary struct {
	Available  int    `json:"available"`
	Selected   int    `json:"selected"`
	Truncated  bool   `json:"truncated"`
	FullMarket bool   `json:"full_market"`
	Scope      string `json:"scope"`
}

// DegeneracySummary records personal Brain trace degeneration checks.
type DegeneracySummary struct {
	Status            string   `json:"status"`
	Degenerate        bool     `json:"degenerate"`
	DominantAction    string   `json:"dominant_action,omitempty"`
	DominantFraction  float64  `json:"dominant_fraction,omitempty"`
	BarsSinceLastExit int      `json:"bars_since_last_exit,omitempty"`
	Reasons           []string `json:"reasons,omitempty"`
}
