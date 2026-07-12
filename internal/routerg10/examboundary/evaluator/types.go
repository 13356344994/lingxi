package evaluator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"lingxi/internal/routerg10/brain"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// ComputeExpectedCommitmentHash returns the frozen registration hash chain.
func ComputeExpectedCommitmentHash(c BrainCommitment) string {
	sum := sha256.Sum256([]byte(
		c.PlaybookHash + "\n" + c.ExperimentHash + "\n" + c.ProtocolHash,
	))
	return hex.EncodeToString(sum[:])
}

// BrainCommitment is the researcher-visible frozen brain/playbook binding carried
// into grading. PlaybookHash seals the frozen brain manifest; hashes are supplied
// only via explicit registration, never from fixture bytes.
type BrainCommitment struct {
	PlaybookHash           string `json:"playbook_hash"`
	ExperimentHash         string `json:"experiment_hash"`
	ProtocolHash           string `json:"protocol_hash"`
	ExpectedCommitmentHash string `json:"expected_commitment_hash"`
}

// Valid reports whether all commitment fields are present and protocol-bound.
func (c BrainCommitment) Valid() bool {
	return c.PlaybookHash != "" &&
		c.ExperimentHash != "" &&
		c.ProtocolHash == g10protocol.RouterProtocolHash &&
		c.ExpectedCommitmentHash != "" &&
		c.ExpectedCommitmentHash == ComputeExpectedCommitmentHash(c)
}

// FixtureSchemaVersion is the frozen hidden exam fixture envelope version.
const FixtureSchemaVersion = g10protocol.ExamFixtureSchemaV1

// FixtureBarInput is one decision bar supplied by the custodian; no grading fields.
type FixtureBarInput struct {
	BarIndex      int                 `json:"bar_index"`
	DecisionTime  time.Time           `json:"decision_time"`
	Features      brain.BarFeatures   `json:"features"`
	Events        []brain.MatchedEvent `json:"events,omitempty"`
	Prices        map[string]float64  `json:"prices"`
	ScenarioDelay int                 `json:"scenario_delay,omitempty"`
	CostStress    float64             `json:"cost_stress,omitempty"`
}

// FixtureInputBlock is one replay input block. Grading metrics are computed by Replay().
type FixtureInputBlock struct {
	InitialEquityUSDT float64           `json:"initial_equity_usdt"`
	Bars              []FixtureBarInput   `json:"bars"`
}

// BlockAccountMetrics carries account-level replay metrics for one exam block.
type BlockAccountMetrics struct {
	InitialEquityUSDT float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT   float64 `json:"final_equity_usdt"`
	IndependentDays   int     `json:"independent_days"`
	LeverageBreach    bool    `json:"leverage_breach"`
	SolvencyBreach    bool    `json:"solvency_breach"`
}

// BlockDelay records per-delay net-return positivity for one block.
type BlockDelay struct {
	NetReturnPositiveByDelay map[string]bool `json:"net_return_positive_by_delay"`
}

// BlockCost records cost-stress replay outcome for one block.
type BlockCost struct {
	CostStressMultiplier float64 `json:"cost_stress_multiplier"`
	NetReturnPositive    bool    `json:"net_return_positive"`
}

// BlockShock records shock-block leave-one-out robustness for one block.
type BlockShock struct {
	ShockBlockLOO_OK bool `json:"shock_block_loo_ok"`
}

// BlockConcurrent records max-concurrent-by-capital gate for one block.
type BlockConcurrent struct {
	MaxConcurrentByCapitalOK bool `json:"max_concurrent_by_capital_ok"`
}

// BlockBrainTrace records deterministic brain trace replay for one block.
type BlockBrainTrace struct {
	ReplayMatchOK   bool `json:"replay_match_ok"`
	DegeneracyClear bool `json:"degeneracy_clear"`
}

// FixtureBlock is one replay-computed exam block returned by Replay().
type FixtureBlock struct {
	AccountMetrics BlockAccountMetrics `json:"account_metrics"`
	Delay          BlockDelay          `json:"delay"`
	Cost           BlockCost           `json:"cost"`
	Shock          BlockShock          `json:"shock"`
	Concurrent     BlockConcurrent     `json:"concurrent"`
	BrainTrace     BlockBrainTrace     `json:"brain_trace"`
}

// FixtureEnvelope is the hidden fixture root object written by the custodian side.
type FixtureEnvelope struct {
	SchemaVersion          string              `json:"schema_version"`
	ExpectedCommitmentHash string              `json:"expected_commitment_hash"`
	Blocks                 []FixtureInputBlock `json:"blocks"`
}

// ParseResult binds decoded fixture blocks with the carried commitment hash.
type ParseResult struct {
	ExpectedCommitmentHash string
	Blocks                 []FixtureInputBlock
}

// ErrCommitmentHashMismatch indicates fixture hash disagrees with registration.
var ErrCommitmentHashMismatch = fmt.Errorf("brain commitment hash mismatch")
