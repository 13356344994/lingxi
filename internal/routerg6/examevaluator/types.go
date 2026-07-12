package examevaluator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	g6protocol "lingxi/internal/routerg6/protocol"
)

// ComputeExpectedCommitmentHash returns the frozen registration hash chain.
func ComputeExpectedCommitmentHash(c EventCardCommitment) string {
	sum := sha256.Sum256([]byte(
		c.EventCardHash + "\n" + c.ExperimentHash + "\n" + c.ProtocolHash,
	))
	return hex.EncodeToString(sum[:])
}

// EventCardCommitment is the researcher-visible frozen event-card binding carried
// into grading. Hashes are supplied only via explicit registration, never from fixture.
type EventCardCommitment struct {
	EventCardHash          string `json:"event_card_hash"`
	ExperimentHash         string `json:"experiment_hash"`
	ProtocolHash           string `json:"protocol_hash"`
	ExpectedCommitmentHash string `json:"expected_commitment_hash"`
}

// Valid reports whether all commitment fields are present and protocol-bound.
func (c EventCardCommitment) Valid() bool {
	return c.EventCardHash != "" &&
		c.ExperimentHash != "" &&
		c.ProtocolHash == g6protocol.RouterProtocolHash &&
		c.ExpectedCommitmentHash != "" &&
		c.ExpectedCommitmentHash == ComputeExpectedCommitmentHash(c)
}

// FixtureSchemaVersion is the frozen hidden exam fixture envelope version.
const FixtureSchemaVersion = "lingxi.routerg6.exam-fixture.v1"

// BlockAccountMetrics carries account-level replay metrics for one exam block.
type BlockAccountMetrics struct {
	InitialEquityUSDT  float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT    float64 `json:"final_equity_usdt"`
	IndependentDays    int     `json:"independent_days"`
	LeverageBreach     bool    `json:"leverage_breach"`
	SolvencyBreach     bool    `json:"solvency_breach"`
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

// FixtureBlock is one hidden exam replay block. Blocks stream in array order.
type FixtureBlock struct {
	AccountMetrics BlockAccountMetrics `json:"account_metrics"`
	Delay          BlockDelay          `json:"delay"`
	Cost           BlockCost           `json:"cost"`
	Shock          BlockShock          `json:"shock"`
	Concurrent     BlockConcurrent     `json:"concurrent"`
}

// FixtureEnvelope is the hidden fixture root object written by the custodian side.
type FixtureEnvelope struct {
	SchemaVersion          string         `json:"schema_version"`
	ExpectedCommitmentHash string         `json:"expected_commitment_hash"`
	Blocks                 []FixtureBlock `json:"blocks"`
}

// ParseResult binds decoded fixture blocks with the carried commitment hash.
type ParseResult struct {
	ExpectedCommitmentHash string
	Blocks                 []FixtureBlock
}

// ErrCommitmentHashMismatch indicates fixture hash disagrees with registration.
var ErrCommitmentHashMismatch = fmt.Errorf("event card commitment hash mismatch")
