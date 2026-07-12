package brain

import (
	"time"

	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/counterfactual"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventaccount"
)

// BarFeatures holds decision-time market state without future labels.
type BarFeatures struct {
	Symbol            string  `json:"symbol"`
	ProfileID         string  `json:"profile_id"`
	ReturnBPS         float64 `json:"return_bps"`
	PriorReturnBPS    float64 `json:"prior_return_bps"`
	BTCRelativeBPS    float64 `json:"btc_relative_bps"`
	BarRangeBPS       float64 `json:"bar_range_bps"`
	RollingVolumeUSDT float64 `json:"rolling_volume_usdt"`
	SessionBucket     string  `json:"session_bucket,omitempty"`
	LiquidityBucket   string  `json:"liquidity_bucket,omitempty"`
	OOD               bool    `json:"ood"`
	Price             float64 `json:"price"`
}

// MatchedEvent binds one playbook match at decision time.
type MatchedEvent struct {
	Playbook       playbook.FrozenEventPlaybook `json:"playbook"`
	EventFamily    string                       `json:"event_family"`
	EventID        string                       `json:"event_id"`
	EventPhase     float64                      `json:"event_phase"`
	ThesisHealth   string                       `json:"thesis_health"`
	GrossLongBPS   float64                      `json:"gross_long_bps"`
	GrossShortBPS  float64                      `json:"gross_short_bps"`
	CompletionBars int                          `json:"completion_bars"`
	CapacityUSDT   float64                      `json:"capacity_usdt"`
	DeadlineBar    int                          `json:"deadline_bar"`
	InvalidatorBar int                          `json:"invalidator_bar"`
	CompleteBar    int                          `json:"complete_bar"`
	PayerGoneBar   int                          `json:"payer_gone_bar"`
	OODBar         int                          `json:"ood_bar"`
}

// BarInput is one decision bar for the G10 brain.
type BarInput struct {
	BarIndex      int                `json:"bar_index"`
	DecisionTime  time.Time          `json:"decision_time"`
	Features      BarFeatures        `json:"features"`
	Events        []MatchedEvent     `json:"events,omitempty"`
	Prices        map[string]float64 `json:"prices"`
	ScenarioDelay int                `json:"scenario_delay"`
	CostStress    float64            `json:"cost_stress"`
	CostBase      residualmap.CostBase `json:"cost_base"`
}

// Config binds frozen brain runtime inputs.
type Config struct {
	InitialEquityUSDT float64                                  `json:"initial_equity_usdt"`
	Fingerprint       fingerprint.PersonalExecutionFingerprint `json:"-"`
	PersonalAdj       residualmap.PersonalAdjustments            `json:"-"`
	Memory            *episodic.Store                          `json:"-"`
	MinNotionalUSDT   float64                                  `json:"min_notional_usdt"`
	LotStep           float64                                  `json:"lot_step"`
	ContractValue     float64                                  `json:"contract_value"`
	ProtocolHash      string                                   `json:"protocol_hash"`
}

// Brain orchestrates per-bar decisions with G5/G6 account semantics.
type Brain struct {
	Config  Config
	Account *eventaccount.Account
	Pending *eventaccount.PendingQueue
	Trace   *audit.TraceLog
}

// BarOutcome records one bar decision plus trace entry.
type BarOutcome struct {
	Trace           audit.BrainTraceEntry      `json:"trace"`
	Counterfactual  counterfactual.Decision    `json:"counterfactual"`
	AppliedAction   string                     `json:"applied_action"`
	PositionSize    float64                    `json:"position_size"`
	SolvencyForced  bool                       `json:"solvency_forced"`
	StopErrorForced bool                       `json:"stop_error_forced"`
}

// FixtureScenario is one deterministic path-matrix scenario.
type FixtureScenario struct {
	SchemaVersion string    `json:"schema_version"`
	Name          string    `json:"name"`
	Config        Config    `json:"config"`
	Bars          []BarInput `json:"bars"`
	WantAction    string    `json:"want_action"`
	WantPath      string    `json:"want_path"`
	WantReject    []string  `json:"want_reject,omitempty"`
}

// FixtureBundle seals deterministic brain path-matrix fixtures.
type FixtureBundle struct {
	SchemaVersion string            `json:"schema_version"`
	Generation    string            `json:"generation"`
	ProtocolHash  string            `json:"protocol_hash"`
	Scenarios     []FixtureScenario `json:"scenarios"`
	BundleHash    string            `json:"bundle_hash"`
}
