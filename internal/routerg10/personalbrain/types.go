package personalbrain

import (
	"time"

	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/entryproof"
	"lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg6/eventaccount"
)

// BarFeatures holds decision-time market state without future labels.
type BarFeatures struct {
	Symbol                    string  `json:"symbol"`
	ProfileID                 string  `json:"profile_id"`
	ReturnBPS                 float64 `json:"return_bps"`
	PriorReturnBPS            float64 `json:"prior_return_bps"`
	BTCRelativeBPS            float64 `json:"btc_relative_bps"`
	BarRangeBPS               float64 `json:"bar_range_bps"`
	RollingVolumeUSDT         float64 `json:"rolling_volume_usdt"`
	SessionBucket             string  `json:"session_bucket,omitempty"`
	LiquidityBucket           string  `json:"liquidity_bucket,omitempty"`
	OOD                       bool    `json:"ood"`
	Price                     float64 `json:"price"`
	VisibleAnnouncementCount  int     `json:"visible_announcement_count,omitempty"`
	CrossVenueShockCluster    string  `json:"cross_venue_shock_cluster,omitempty"`
	EventFamily               string  `json:"event_family,omitempty"`
	DynamicMinNotionalUSDT    float64 `json:"dynamic_min_notional_usdt,omitempty"`
	MinLotNonlinearity        float64 `json:"min_lot_nonlinearity,omitempty"`
	MechanicalFlowExhaustion  float64 `json:"mechanical_flow_exhaustion,omitempty"`
	PrepositionedCapitalWindow float64 `json:"prepositioned_capital_window,omitempty"`
	CustodyAuthorizationLimit float64 `json:"custody_authorization_limit,omitempty"`
	EventSparsity             float64 `json:"event_sparsity,omitempty"`
	DynamicThresholdShift     float64 `json:"dynamic_threshold_shift,omitempty"`
	PersonalTapeFamily        string  `json:"personal_tape_family,omitempty"`
	Horizon                   string  `json:"horizon,omitempty"`
}

// ExecutionSimState carries bar-local execution simulation for fixtures/replay.
// Outcome labels (subsequently profitable, toxic signals) must not enter sizing.
type ExecutionSimState struct {
	MakerPending           bool    `json:"maker_pending,omitempty"`
	MakerTimeoutBars       int     `json:"maker_timeout_bars,omitempty"`
	MakerFilled            bool    `json:"maker_filled,omitempty"`
	MakerTouchedNotFilled  bool    `json:"maker_touched_not_filled,omitempty"`
	SubsequentlyProfitable bool    `json:"subsequently_profitable,omitempty"`
	ToxicFastFullFill      bool    `json:"toxic_fast_full_fill,omitempty"`
	ToxicThroughPrice      bool    `json:"toxic_through_price,omitempty"`
	ToxicBookSyncBad       bool    `json:"toxic_book_sync_bad,omitempty"`
	ThesisStillValid       bool    `json:"thesis_still_valid"`
	PartialFill            bool    `json:"partial_fill,omitempty"`
	RemainingEdgeBPS       float64 `json:"remaining_edge_bps,omitempty"`
	SpreadBPS              float64 `json:"spread_bps,omitempty"`
	FeeBPS                 float64 `json:"fee_bps,omitempty"`
	LatencyMS              float64 `json:"latency_ms,omitempty"`
	MaxPriceCap            float64 `json:"max_price_cap,omitempty"`
	MaxSlippageBPS         float64 `json:"max_slippage_bps,omitempty"`
	MinRemainingEdgeBPS    float64 `json:"min_remaining_edge_bps,omitempty"`
	TimeoutMS              int64   `json:"timeout_ms,omitempty"`
}

// VerifiedProofs binds frozen upstream proof artifacts for one decision.
type VerifiedProofs struct {
	ConstitutionHash     string                    `json:"constitution_hash"`
	ExclusionProofHash   string                    `json:"exclusion_proof_hash"`
	EntryProofHash       string                    `json:"entry_proof_hash"`
	ExecutionPolicyHash  string                    `json:"execution_policy_hash"`
	ExitProofHash        string                    `json:"exit_proof_hash"`
	AnnouncementTapeHash string                    `json:"announcement_tape_hash"`
	EntryGrade           string                    `json:"entry_grade"`
	EntryCertified       bool                      `json:"entry_certified"`
	ExitCertified        bool                      `json:"exit_certified"`
	ExclusionAccepted    bool                      `json:"exclusion_accepted"`
	InstitutionAxisHash  string                    `json:"institution_axis_hash,omitempty"`
	RetailAxisHash       string                    `json:"retail_axis_hash,omitempty"`
	BotAxisHash          string                    `json:"bot_axis_hash,omitempty"`
	CrowdingPassed       bool                      `json:"crowding_passed"`
	CrowdingStress       exclusionmap.CrowdingStress `json:"crowding_stress,omitempty"`
	MakerGateForbid      bool                      `json:"maker_gate_forbid,omitempty"`
	DynamicMinNotional   float64                   `json:"dynamic_min_notional_usdt"`
	ExecutableDepthUSDT  float64                   `json:"executable_depth_usdt"`
}

// MatchedPersonalEvent binds one verified personal playbook at decision time.
type MatchedPersonalEvent struct {
	Playbook       personalplaybook.PersonalWildPlaybook `json:"playbook"`
	Proofs         VerifiedProofs                        `json:"proofs"`
	EventID        string                                `json:"event_id"`
	EventFamily    string                                `json:"event_family"`
	EventPhase     float64                               `json:"event_phase"`
	ThesisHealth   string                                `json:"thesis_health"`
	Direction      string                                `json:"direction"`
	GrossLongBPS   float64                               `json:"gross_long_bps"`
	GrossShortBPS  float64                               `json:"gross_short_bps"`
	CompletionBars int                                   `json:"completion_bars"`
	CapacityUSDT   float64                               `json:"capacity_usdt"`
	MaxSizeUSDT    float64                               `json:"max_size_usdt"`
	DeadlineBar    int                                   `json:"deadline_bar"`
	InvalidatorBar int                                   `json:"invalidator_bar"`
	CompleteBar    int                                   `json:"complete_bar"`
	PayerGoneBar   int                                   `json:"payer_gone_bar"`
	OODBar         int                                   `json:"ood_bar"`
}

// BarInput is one decision bar for the G10P brain.
type BarInput struct {
	BarIndex       int                            `json:"bar_index"`
	DecisionTime   time.Time                      `json:"decision_time"`
	Features       BarFeatures                    `json:"features"`
	Events         []MatchedPersonalEvent         `json:"events,omitempty"`
	Announcements  []announcements.Announcement   `json:"announcements,omitempty"`
	Prices         map[string]float64             `json:"prices"`
	ScenarioDelay  int                            `json:"scenario_delay"`
	CostStress     float64                        `json:"cost_stress"`
	ExecutionSim   ExecutionSimState              `json:"execution_sim"`
	CapitalTierUSDT float64                       `json:"capital_tier_usdt,omitempty"`
	ShockCluster    string                         `json:"shock_cluster,omitempty"`
}

// Config binds frozen G10P brain runtime inputs.
type Config struct {
	InitialEquityUSDT float64 `json:"initial_equity_usdt"`
	ConstitutionHash  string  `json:"constitution_hash"`
	MinNotionalUSDT   float64 `json:"min_notional_usdt"`
	LotStep           float64 `json:"lot_step"`
	ContractValue     float64 `json:"contract_value"`
	ProtocolHash      string  `json:"protocol_hash"`
	TradeAllowed      bool    `json:"trade_allowed"`
}

// MakerOrderState tracks an in-flight bounded maker attempt.
type MakerOrderState struct {
	EventID      string
	Symbol       string
	Direction    string
	NotionalUSDT float64
	PlacedBar    int
	TimeoutBars  int
	FrozenAction string
}

// Brain orchestrates per-bar G10P decisions with G5/G6 account semantics.
type Brain struct {
	Config       Config
	Account      *eventaccount.Account
	Pending      *eventaccount.PendingQueue
	MakerOrder   *MakerOrderState
	ToxicBlocked bool
	Trace        *TraceLog
}

// EnterGateResult is the first per-bar gate before execution comparison.
type EnterGateResult struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

// ExecutionDecision freezes compared execution actions at decision time.
type ExecutionDecision struct {
	ActionsCompared []executionpolicy.ActionEvaluation `json:"actions_compared"`
	SelectedAction  string                               `json:"selected_action"`
	RejectReasons   []string                             `json:"reject_reasons,omitempty"`
}

// TerminalRiskUsage reports 40%/70% ceiling occupancy — not sizing targets.
type TerminalRiskUsage struct {
	EventTerminalBudgetUSDT   float64 `json:"event_terminal_budget_usdt"`
	AccountTerminalBudgetUSDT float64 `json:"account_terminal_budget_usdt"`
	EventOccupancyPct         float64 `json:"event_occupancy_pct"`
	AccountOccupancyPct       float64 `json:"account_occupancy_pct"`
	CeilingIsNotTarget        bool    `json:"ceiling_is_not_target"`
}

// AnnouncementVisibility records PIT-visible announcements at decision time.
type AnnouncementVisibility struct {
	VisibleCount int      `json:"visible_count"`
	VisibleIDs   []string `json:"visible_ids,omitempty"`
	TapeHash     string   `json:"tape_hash,omitempty"`
}

// BarOutcome records one bar decision plus trace entry.
type BarOutcome struct {
	Trace            BrainTraceEntry   `json:"trace"`
	EnterGate        EnterGateResult   `json:"enter_gate"`
	Execution        ExecutionDecision `json:"execution"`
	AppliedAction    string            `json:"applied_action"`
	PositionSize     float64           `json:"position_size"`
	SolvencyForced   bool              `json:"solvency_forced"`
	StopErrorForced  bool              `json:"stop_error_forced"`
	FillOrMissReason string            `json:"fill_or_miss_reason,omitempty"`
}

// FixtureScenario is one deterministic G10P path-matrix scenario.
type FixtureScenario struct {
	SchemaVersion string    `json:"schema_version"`
	Name          string    `json:"name"`
	Config        Config    `json:"config"`
	Bars          []BarInput `json:"bars"`
	WantAction    string    `json:"want_action"`
	WantPath      string    `json:"want_path"`
	WantEnterGate string    `json:"want_enter_gate,omitempty"`
	WantReject    []string  `json:"want_reject,omitempty"`
}

// FixtureBundle seals deterministic G10P brain path-matrix fixtures.
type FixtureBundle struct {
	SchemaVersion string            `json:"schema_version"`
	Generation    string            `json:"generation"`
	ProtocolHash  string            `json:"protocol_hash"`
	Scenarios     []FixtureScenario `json:"scenarios"`
	BundleHash    string            `json:"bundle_hash"`
}

// ReplayResult compares two trace replays for hash equality.
type ReplayResult struct {
	SchemaVersion   string `json:"schema_version"`
	Match           bool   `json:"match"`
	EntryCount      int    `json:"entry_count"`
	FirstMismatchAt int    `json:"first_mismatch_at,omitempty"`
	ExpectedHash    string `json:"expected_hash,omitempty"`
	ActualHash      string `json:"actual_hash,omitempty"`
}

// DegeneracyReport summarizes brain action distribution health.
type DegeneracyReport struct {
	SchemaVersion       string          `json:"schema_version"`
	Generation          string          `json:"generation"`
	ProtocolHash        string          `json:"protocol_hash"`
	TotalBars           int             `json:"total_bars"`
	ActionCounts        map[string]int  `json:"action_counts"`
	DominantAction      string          `json:"dominant_action,omitempty"`
	DominantFraction    float64         `json:"dominant_fraction"`
	DirectionalFraction float64         `json:"directional_fraction"`
	BarsSinceLastExit   int             `json:"bars_since_last_exit"`
	Degenerate          bool            `json:"degenerate"`
	DegeneracyReasons   []string        `json:"degeneracy_reasons,omitempty"`
	Status              string          `json:"status"`
	PathMatrixCovered   map[string]bool `json:"path_matrix_covered"`
	PathMatrixMissing   []string        `json:"path_matrix_missing,omitempty"`
	ReportHash          string          `json:"report_hash"`
}

// ProofBundle is a convenience aggregate for fixture construction.
type ProofBundle struct {
	Entry   entryproof.EntryProof
	Exit    exitproof.Proof
	Policy  executionpolicy.FrozenPolicy
	Exclude exclusionmap.ExclusionProof
}
