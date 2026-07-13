package executionpolicy

import "time"

// SourcePaths points at trigger streams for policy evaluation.
type SourcePaths struct {
	Triggers string `json:"triggers"`
}

// RunConfig binds CLI knobs for bounded execution-policy generation.
type RunConfig struct {
	SchemaVersion string      `json:"schema_version"`
	Generation    string      `json:"generation"`
	OutputDir     string      `json:"output_dir"`
	Sources       SourcePaths `json:"sources"`
	MaxLines      int         `json:"max_lines"`
	LowCPU        bool        `json:"low_cpu"`
}

// TriggerRecord is one trigger with frozen decision info and realized path labels.
type TriggerRecord struct {
	TriggerID    string    `json:"trigger_id"`
	DecisionTime time.Time `json:"decision_time"`
	Symbol       string    `json:"symbol"`
	Side         string    `json:"side"`
	Price        float64   `json:"price"`

	MakerTouched  bool `json:"maker_touched"`
	MakerFilled   bool `json:"maker_filled"`
	FillWaitMS    int  `json:"fill_wait_ms"`
	PartialFill   bool `json:"partial_fill"`
	ThroughPrice  bool `json:"through_price"`
	NetPnLUSDT    float64 `json:"net_pnl_usdt"`
	SubsequentlyProfitable bool `json:"subsequently_profitable"`

	MaxPriceCap       float64 `json:"max_price_cap"`
	MaxSlippageBPS    float64 `json:"max_slippage_bps"`
	MinRemainingEdgeBPS float64 `json:"min_remaining_edge_bps"`
	TimeoutMS         int64   `json:"timeout_ms"`
	RemainingEdgeBPS       float64 `json:"remaining_edge_bps"`
	ReferenceNotionalUSDT  float64 `json:"reference_notional_usdt,omitempty"`
	SpreadBPS              float64 `json:"spread_bps"`
	LatencyMS              float64 `json:"latency_ms"`
	FeeBPS                 float64 `json:"fee_bps"`

	ToxicFastFullFill bool `json:"toxic_fast_full_fill"`
	ToxicThroughPrice bool `json:"toxic_through_price"`
	ToxicBookSyncBad  bool `json:"toxic_book_sync_bad"`
	ThesisStillValid  bool `json:"thesis_still_valid"`
}

// ActionEvaluation is one frozen action path at decision time.
type ActionEvaluation struct {
	Action          string  `json:"action"`
	ExpectedNetBPS  float64 `json:"expected_net_bps"`
	ExpectedNetUSDT float64 `json:"expected_net_usdt"`
	PFill           float64 `json:"p_fill,omitempty"`
	Allowed         bool    `json:"allowed"`
	BlockReason     string  `json:"block_reason,omitempty"`
}

// MakerQuadrantCounts tracks maker fill selection bias over all triggers.
type MakerQuadrantCounts struct {
	FilledProfitable               int `json:"filled_profitable"`
	FilledUnprofitable             int `json:"filled_unprofitable"`
	UnfilledSubsequentlyProfitable int `json:"unfilled_subsequently_profitable"`
	UnfilledNotProfitable          int `json:"unfilled_not_profitable"`
	TotalTriggers                  int `json:"total_triggers"`
}

// MakerStats aggregates maker path metrics using all triggers as denominator.
type MakerStats struct {
	Quadrants              MakerQuadrantCounts `json:"quadrants"`
	PFill                  float64             `json:"p_fill"`
	ExpectedNetGivenFilled float64             `json:"expected_net_given_filled"`
	FastFillExpectedNet    float64             `json:"fast_fill_expected_net"`
	SlowFillExpectedNet    float64             `json:"slow_fill_expected_net"`
	PartialFillCount       int                 `json:"partial_fill_count"`
	ThroughPriceCount      int                 `json:"through_price_count"`
}

// MakerFillSelectionGate forbids maker when adverse selection dominates.
type MakerFillSelectionGate struct {
	ForbidMaker bool     `json:"forbid_maker"`
	Reasons     []string `json:"reasons,omitempty"`
}

// FrozenPolicy seals one trigger's simultaneous action comparison.
type FrozenPolicy struct {
	SchemaVersion           string             `json:"schema_version"`
	TriggerID               string             `json:"trigger_id"`
	DecisionTime            time.Time          `json:"decision_time"`
	FreezeBeforeOutcomeKnown bool              `json:"freeze_before_outcome_known"`
	ActionsCompared         []ActionEvaluation `json:"actions_compared"`
	MakerStats              MakerStats         `json:"maker_stats"`
	MakerQuadrant           string             `json:"maker_quadrant"`
	MakerGate               MakerFillSelectionGate `json:"maker_gate"`
	NoLongLivedMaker        bool               `json:"no_long_lived_maker"`
	NoChaseAfterMiss        bool               `json:"no_chase_after_miss"`
	ToxicFillStatus         string             `json:"toxic_fill_status,omitempty"`
	SelectedAction          string             `json:"selected_action"`
	ExecutionPolicyHash     string             `json:"execution_policy_hash"`

	// Frozen decision-visible execution bounds (LONG cap / SHORT floor).
	Side                string  `json:"side,omitempty"`
	Price               float64 `json:"price,omitempty"`
	MaxPriceCap         float64 `json:"max_price_cap,omitempty"`
	MaxSlippageBPS      float64 `json:"max_slippage_bps,omitempty"`
	MinRemainingEdgeBPS float64 `json:"min_remaining_edge_bps,omitempty"`
	TimeoutMS           int64   `json:"timeout_ms,omitempty"`
	SpreadBPS           float64 `json:"spread_bps,omitempty"`
	FeeBPS              float64 `json:"fee_bps,omitempty"`
	LatencyMS           float64 `json:"latency_ms,omitempty"`
}

// Manifest seals execution-policy run outputs.
type Manifest struct {
	SchemaVersion        string    `json:"schema_version"`
	Generation           string    `json:"generation"`
	ConfigHash           string    `json:"config_hash"`
	PolicyCount          int       `json:"policy_count"`
	AllTriggersDenominator int     `json:"all_triggers_denominator"`
	MakerGateForbidCount int       `json:"maker_gate_forbid_count"`
	PoliciesHash         string    `json:"policies_hash"`
	CreatedAt            time.Time `json:"created_at"`
	ManifestHash         string    `json:"manifest_hash"`
}

// RunResult holds sealed execution-policy outputs.
type RunResult struct {
	Policies   []FrozenPolicy
	Manifest   Manifest
	OutputPath string
}
