package examevaluator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	g5protocol "lingxi/internal/routerg5/protocol"
)

var (
	ErrFixtureSchema      = errors.New("invalid exam fixture schema")
	ErrFixtureSelfDeclare = errors.New("fixture must not self-declare policy hashes")
	ErrFixtureFieldLeak   = errors.New("fixture contains forbidden field")
)

var forbiddenFixtureFields = []string{
	"policy_hash",
	"feature_hash",
	"experiment_hash",
	"protocol_hash",
	"pass",
	"pass_threshold",
	"threshold",
}

// ParseFixtureStream decodes a hidden fixture from a streaming JSON reader.
// It rejects self-declared component hashes and unknown top-level fields.
func ParseFixtureStream(r io.Reader) (ParseResult, error) {
	decoder := json.NewDecoder(r)
	token, err := decoder.Token()
	if err != nil {
		return ParseResult{}, fmt.Errorf("%w: %v", ErrFixtureSchema, err)
	}
	if token != json.Delim('{') {
		return ParseResult{}, fmt.Errorf("%w: root must be object", ErrFixtureSchema)
	}
	var blocks []FixtureBlock
	var commitmentHash string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return ParseResult{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return ParseResult{}, fmt.Errorf("%w: non-string key", ErrFixtureSchema)
		}
		if slices.Contains(forbiddenFixtureFields, key) {
			return ParseResult{}, fmt.Errorf("%w: %q", ErrFixtureSelfDeclare, key)
		}
		switch key {
		case "schema_version":
			var version string
			if err := decoder.Decode(&version); err != nil {
				return ParseResult{}, err
			}
			if version != FixtureSchemaVersion {
				return ParseResult{}, fmt.Errorf("%w: schema_version=%q", ErrFixtureSchema, version)
			}
		case "expected_commitment_hash":
			if err := decoder.Decode(&commitmentHash); err != nil {
				return ParseResult{}, err
			}
			if commitmentHash == "" {
				return ParseResult{}, fmt.Errorf("%w: expected_commitment_hash required", ErrFixtureSchema)
			}
		case "blocks":
			arrayToken, err := decoder.Token()
			if err != nil || arrayToken != json.Delim('[') {
				return ParseResult{}, fmt.Errorf("%w: blocks must be array", ErrFixtureSchema)
			}
			for decoder.More() {
				var block FixtureBlock
				if err := decoder.Decode(&block); err != nil {
					return ParseResult{}, fmt.Errorf("%w: block decode: %v", ErrFixtureSchema, err)
				}
				if err := validateBlock(block); err != nil {
					return ParseResult{}, err
				}
				blocks = append(blocks, block)
			}
			if _, err := decoder.Token(); err != nil {
				return ParseResult{}, err
			}
		default:
			return ParseResult{}, fmt.Errorf("%w: %q", ErrFixtureFieldLeak, key)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return ParseResult{}, err
	}
	if commitmentHash == "" {
		return ParseResult{}, fmt.Errorf("%w: expected_commitment_hash required", ErrFixtureSchema)
	}
	if len(blocks) == 0 {
		return ParseResult{}, fmt.Errorf("%w: empty blocks", ErrFixtureSchema)
	}
	return ParseResult{ExpectedCommitmentHash: commitmentHash, Blocks: blocks}, nil
}

func validateBlock(block FixtureBlock) error {
	if block.Cost.CostStressMultiplier != 0 &&
		block.Cost.CostStressMultiplier != g5protocol.ExamCostStressMultiplier {
		return fmt.Errorf("%w: cost_stress_multiplier drift", ErrFixtureSchema)
	}
	return nil
}

// EncodeFixture builds a canonical fixture envelope for synthetic tests.
func EncodeFixture(commitmentHash string, blocks []FixtureBlock) ([]byte, error) {
	return json.Marshal(FixtureEnvelope{
		SchemaVersion:          FixtureSchemaVersion,
		ExpectedCommitmentHash: commitmentHash,
		Blocks:                 blocks,
	})
}

// PassingBlock returns a synthetic block that satisfies frozen exam gates.
func PassingBlock() FixtureBlock {
	delay := map[string]bool{}
	for _, d := range g5protocol.ValidationDelayBars {
		delay[delayKey(d)] = true
	}
	return FixtureBlock{
		AccountMetrics: BlockAccountMetrics{
			InitialEquityUSDT:                     float64(g5protocol.ValidationInitialEquities[0]),
			FinalEquityUSDT:                       float64(g5protocol.ValidationInitialEquities[0]) * 1.1,
			MaxDrawdownFraction:                   0.10,
			ActiveDays:                            g5protocol.ExamMinActiveDays,
			MaxSymbolPositiveContributionFraction: 0.20,
		},
		Actions: BlockActions{ActionCount: 10, TopKOK: true},
		Delay:   BlockDelay{NetReturnPositiveByDelay: delay},
		Cost: BlockCost{
			CostStressMultiplier: g5protocol.ExamCostStressMultiplier,
			NetReturnPositive:    true,
		},
		Cluster: BlockCluster{ClusterLOO_OK: true},
	}
}
