package examevaluator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	g5protocol "lingxi/internal/routerg5/protocol"
)

// ComputeExpectedCommitmentHash returns the frozen registration hash chain.
func ComputeExpectedCommitmentHash(c PolicyCommitment) string {
	sum := sha256.Sum256([]byte(
		c.PolicyHash + "\n" + c.FeatureHash + "\n" + c.ExperimentHash + "\n" + c.ProtocolHash,
	))
	return hex.EncodeToString(sum[:])
}

// PolicyCommitment is the researcher-visible frozen policy binding carried into
// grading. Hashes are supplied only via explicit registration, never from fixture.
type PolicyCommitment struct {
	PolicyHash               string `json:"policy_hash"`
	FeatureHash              string `json:"feature_hash"`
	ExperimentHash           string `json:"experiment_hash"`
	ProtocolHash             string `json:"protocol_hash"`
	ExpectedCommitmentHash   string `json:"expected_commitment_hash"`
}

// Valid reports whether all commitment fields are present and protocol-bound.
func (c PolicyCommitment) Valid() bool {
	return c.PolicyHash != "" &&
		c.FeatureHash != "" &&
		c.ExperimentHash != "" &&
		c.ProtocolHash == g5protocol.RouterProtocolHash &&
		c.ExpectedCommitmentHash != "" &&
		c.ExpectedCommitmentHash == ComputeExpectedCommitmentHash(c)
}

// FixtureSchemaVersion is the frozen hidden exam fixture envelope version.
const FixtureSchemaVersion = "lingxi.routerg5.exam-fixture.v1"

// BlockAccountMetrics carries account-level replay metrics for one exam block.
type BlockAccountMetrics struct {
	InitialEquityUSDT                     float64 `json:"initial_equity_usdt"`
	FinalEquityUSDT                       float64 `json:"final_equity_usdt"`
	MaxDrawdownFraction                   float64 `json:"max_drawdown_fraction"`
	ActiveDays                            int     `json:"active_days"`
	LeverageBreach                        bool    `json:"leverage_breach"`
	SolvencyBreach                        bool    `json:"solvency_breach"`
	MaxSymbolPositiveContributionFraction float64 `json:"max_symbol_positive_contribution_fraction"`
}

// BlockActions records action coverage constraints for one block.
type BlockActions struct {
	ActionCount int  `json:"action_count"`
	TopKOK      bool `json:"top_k_ok"`
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

// BlockCluster records cluster robustness for one block.
type BlockCluster struct {
	ClusterLOO_OK bool `json:"cluster_loo_ok"`
}

// FixtureBlock is one hidden exam replay block. Blocks stream in array order.
type FixtureBlock struct {
	AccountMetrics BlockAccountMetrics `json:"account_metrics"`
	Actions        BlockActions        `json:"actions"`
	Delay          BlockDelay          `json:"delay"`
	Cost           BlockCost           `json:"cost"`
	Cluster        BlockCluster        `json:"cluster"`
}

// FixtureEnvelope is the hidden fixture root object written by the custodian side.
type FixtureEnvelope struct {
	SchemaVersion            string         `json:"schema_version"`
	ExpectedCommitmentHash   string         `json:"expected_commitment_hash"`
	Blocks                   []FixtureBlock `json:"blocks"`
}

// ParseResult binds decoded fixture blocks with the carried commitment hash.
type ParseResult struct {
	ExpectedCommitmentHash string
	Blocks                 []FixtureBlock
}

// ErrCommitmentHashMismatch indicates fixture hash disagrees with registration.
var ErrCommitmentHashMismatch = fmt.Errorf("policy commitment hash mismatch")
