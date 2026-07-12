package policy

import (
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/reverselearn"
)

const (
	SchemaVersionBundle = "lingxi.router-g5-policy.bundle.v1"
)

// ScoredAction is one policy-ranked legal action at a decision point.
type ScoredAction struct {
	Symbol       string
	Action       actionspace.Action
	StateLabel   string
	StartState   actionspace.PositionState
	Features     map[string]float64
	Score        float64
	Edge         float64
	Confidence   float64
	SizeFraction float64
}

// Proposal is one account-level candidate after policy scoring and sizing.
type Proposal struct {
	ID               string
	Symbol           string
	Cluster          string
	Action           actionspace.Action
	StateLabel       string
	StartState       actionspace.PositionState
	Score            float64
	SizeFraction     float64
	NotionalUSDT     float64
	StressLossUSDT   float64
	MarginUSDT       float64
	MarginTimeSec    int64
	CapacityHeadroom float64
	CostEstimateBPS  float64
	LiquidityScore   float64
}

// PolicyBundle binds verified frozen policy inputs for validation.
type PolicyBundle struct {
	Dir            string
	Policy         reverselearn.FrozenPolicy
	Ledger         reverselearn.ExperimentLedger
	PolicyManifest reverselearn.PolicyManifest
}

// DecisionInput is one timestamp+symbol validation context row.
type DecisionInput struct {
	Symbol       string
	DecisionTime time.Time
	Partition    string
	Features     map[string]float64
	Missing      []string
	StartState   actionspace.PositionState
	StateLabel   string
}
