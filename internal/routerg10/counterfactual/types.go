package counterfactual

import (
	"time"

	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/residualmap"
)

// EventContext describes the matched event playbook branch for counterfactual scoring.
type EventContext struct {
	HasEvent      bool    `json:"has_event"`
	EventFamily   string  `json:"event_family,omitempty"`
	PlaybookHash  string  `json:"playbook_hash,omitempty"`
	Horizon       string  `json:"horizon,omitempty"`
	Direction     string  `json:"direction,omitempty"`
	GrossLongBPS  float64 `json:"gross_long_bps"`
	GrossShortBPS float64 `json:"gross_short_bps"`
	DelayBars     int     `json:"delay_bars"`
}

// PositionContext describes the current account position state.
type PositionContext struct {
	HasPosition       bool    `json:"has_position"`
	Direction         string  `json:"direction,omitempty"`
	NotionalUSDT      float64 `json:"notional_usdt"`
	ConcurrentEvents  int     `json:"concurrent_events"`
	MaxConcurrent     int     `json:"max_concurrent"`
}

// DecisionContext binds one bar decision inputs.
type DecisionContext struct {
	Symbol          string                    `json:"symbol"`
	ProfileID       string                    `json:"profile_id"`
	DecisionTime    time.Time                 `json:"decision_time"`
	EquityUSDT      float64                   `json:"equity_usdt"`
	Event           EventContext              `json:"event"`
	Position        PositionContext           `json:"position"`
	Query           episodic.QueryVector      `json:"query"`
	Memory          episodic.MemoryAdvice     `json:"memory"`
	Fingerprint     fingerprint.PersonalExecutionFingerprint `json:"-"`
	CostBase        residualmap.CostBase      `json:"cost_base"`
	PersonalAdj     residualmap.PersonalAdjustments `json:"-"`
	MinNotionalUSDT float64                   `json:"min_notional_usdt"`
	LotStep         float64                   `json:"lot_step"`
	ContractValue   float64                   `json:"contract_value"`
	OOD             bool                      `json:"ood"`
	Solvent         bool                      `json:"solvent"`
	StressBudgetBPS float64                   `json:"stress_budget_bps"`
	CompletionBars  int                       `json:"completion_bars"`
	CapacityUSDT    float64                   `json:"capacity_usdt"`
}

// ActionMetric is one row in the local eight-action counterfactual table.
type ActionMetric struct {
	Action                     string   `json:"action"`
	PersonalConservativeNetBPS float64  `json:"personal_conservative_net_bps"`
	StressLossBPS              float64  `json:"stress_loss_bps"`
	MarginOccupationUSDT       float64  `json:"margin_occupation_usdt"`
	ExpectedCompletionBars     int      `json:"expected_completion_bars"`
	DelayBars                  int      `json:"delay_bars"`
	MinLotFeasible             bool     `json:"min_lot_feasible"`
	CapacityUSDT               float64  `json:"capacity_usdt"`
	RankScore                  float64  `json:"rank_score"`
	Allowed                    bool     `json:"allowed"`
	SizeScale                  float64  `json:"size_scale"`
	RejectReasons              []string `json:"reject_reasons,omitempty"`
}

// CounterfactualTable compares all frozen Brain actions for one decision.
type CounterfactualTable struct {
	SchemaVersion string         `json:"schema_version"`
	Generation    string         `json:"generation"`
	ProtocolHash  string         `json:"protocol_hash"`
	DecisionTime  time.Time      `json:"decision_time"`
	Symbol        string         `json:"symbol"`
	Actions       []ActionMetric `json:"actions"`
	TableHash     string         `json:"table_hash"`
}

// Decision is the auditable counterfactual outcome for one bar.
type Decision struct {
	SchemaVersion string              `json:"schema_version"`
	FinalAction   string              `json:"final_action"`
	DelayBars     int                 `json:"delay_bars"`
	SizeScale     float64             `json:"size_scale"`
	RejectReasons []string            `json:"reject_reasons"`
	Table         CounterfactualTable `json:"table"`
	DecisionHash  string              `json:"decision_hash"`
}

// FixtureScenario is one deterministic path-matrix scenario for tests.
type FixtureScenario struct {
	SchemaVersion string          `json:"schema_version"`
	Name          string          `json:"name"`
	Context       DecisionContext `json:"context"`
	WantAction    string          `json:"want_action"`
	WantReject    []string        `json:"want_reject,omitempty"`
}
