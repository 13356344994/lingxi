package evaluator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	g10protocol "lingxi/internal/routerg10/protocol"
)

var (
	ErrFixtureSchema      = errors.New("invalid exam fixture schema")
	ErrFixtureSelfDeclare = errors.New("fixture must not self-declare brain hashes")
	ErrFixtureFieldLeak   = errors.New("fixture contains forbidden field")
)

var forbiddenFixtureFields = []string{
	"playbook_hash",
	"experiment_hash",
	"protocol_hash",
	"pass",
	"pass_threshold",
	"threshold",
}

var forbiddenBlockFields = []string{
	"account_metrics",
	"delay",
	"cost",
	"shock",
	"concurrent",
	"brain_trace",
	"pass",
	"score",
	"pnl",
	"hidden_range",
	"cluster_id",
}

// ParseFixtureStream decodes a hidden fixture from a streaming JSON reader.
// It rejects self-declared component hashes, grading fields, and unknown top-level fields.
func ParseFixtureStream(r io.Reader) (ParseResult, error) {
	decoder := json.NewDecoder(r)
	token, err := decoder.Token()
	if err != nil {
		return ParseResult{}, fmt.Errorf("%w: %v", ErrFixtureSchema, err)
	}
	if token != json.Delim('{') {
		return ParseResult{}, fmt.Errorf("%w: root must be object", ErrFixtureSchema)
	}
	var blocks []FixtureInputBlock
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
				var raw json.RawMessage
				if err := decoder.Decode(&raw); err != nil {
					return ParseResult{}, fmt.Errorf("%w: block decode: %v", ErrFixtureSchema, err)
				}
				if err := assertNoForbiddenBlockFields(raw); err != nil {
					return ParseResult{}, err
				}
				var block FixtureInputBlock
				if err := json.Unmarshal(raw, &block); err != nil {
					return ParseResult{}, fmt.Errorf("%w: block decode: %v", ErrFixtureSchema, err)
				}
				if err := validateInputBlock(block); err != nil {
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

func assertNoForbiddenBlockFields(raw json.RawMessage) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("%w: %v", ErrFixtureSchema, err)
	}
	for _, key := range forbiddenBlockFields {
		if _, ok := probe[key]; ok {
			return fmt.Errorf("%w: block field %q", ErrFixtureFieldLeak, key)
		}
	}
	return nil
}

func validateInputBlock(block FixtureInputBlock) error {
	if len(block.Bars) == 0 {
		return fmt.Errorf("%w: block requires bars", ErrFixtureSchema)
	}
	for _, bar := range block.Bars {
		if bar.Features.Symbol == "" {
			return fmt.Errorf("%w: bar features.symbol required", ErrFixtureSchema)
		}
	}
	return nil
}

// EncodeFixture builds a canonical input-only fixture envelope for synthetic tests.
func EncodeFixture(commitmentHash string, blocks []FixtureInputBlock) ([]byte, error) {
	return EncodeInputFixture(commitmentHash, blocks)
}

// PassingBlock returns replay-computed metrics for a synthetic passing input block.
func PassingBlock() FixtureBlock {
	commitment := BrainCommitment{
		PlaybookHash:   "test-playbook",
		ExperimentHash: "test-experiment",
		ProtocolHash:   g10protocol.RouterProtocolHash,
	}
	commitment.ExpectedCommitmentHash = ComputeExpectedCommitmentHash(commitment)
	block, err := PassingBlockFromReplay(commitment)
	if err != nil {
		return FixtureBlock{
			AccountMetrics: BlockAccountMetrics{
				InitialEquityUSDT: float64(g10protocol.ValidationInitialEquities[0]),
				FinalEquityUSDT:   float64(g10protocol.ValidationInitialEquities[0]) * 1.1,
				IndependentDays:   g10protocol.ExamMinIndependentDays,
			},
			Delay: BlockDelay{NetReturnPositiveByDelay: map[string]bool{"0": true, "1": true, "2": true, "3": true}},
			Cost: BlockCost{CostStressMultiplier: g10protocol.ExamCostStressMultiplier, NetReturnPositive: true},
			Shock: BlockShock{ShockBlockLOO_OK: true},
			Concurrent: BlockConcurrent{MaxConcurrentByCapitalOK: true},
			BrainTrace: BlockBrainTrace{ReplayMatchOK: true, DegeneracyClear: true},
		}
	}
	return block
}

func delayKey(delay int) string {
	return fmt.Sprintf("%d", delay)
}
