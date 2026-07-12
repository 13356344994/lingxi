package examevaluator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"lingxi/internal/historyexam"
)

// Evaluator grades G6 hidden exam fixtures against a registration-bound event-card
// commitment. Thresholds and pass/fail cannot be supplied at runtime.
type Evaluator struct {
	Commitment EventCardCommitment
	Replay     ReplayFunc
}

// NewEvaluator constructs a protocol-bound G6 evaluator with a custodian replay callback.
func NewEvaluator(commitment EventCardCommitment, replay ReplayFunc) Evaluator {
	return Evaluator{Commitment: commitment, Replay: replay}
}

// ProtocolHash reports the bound G6 protocol hash.
func (e Evaluator) ProtocolHash() string {
	return e.Commitment.ProtocolHash
}

// Evaluate streams the fixture, verifies the registered event-card commitment hash
// chain, replays blocks via the registered callback, and applies frozen exam gates.
func (e Evaluator) Evaluate(_ context.Context, paper historyexam.HiddenPaper, fixture io.Reader) (historyexam.Outcome, error) {
	if !e.Commitment.Valid() || e.Replay == nil {
		return historyexam.Outcome{}, historyexam.ErrExamUnavailable
	}
	if paper.ProtocolHash != e.Commitment.ProtocolHash {
		return historyexam.Outcome{}, historyexam.ErrProtocolUnbound
	}
	raw, err := io.ReadAll(fixture)
	if err != nil {
		return historyexam.Outcome{}, err
	}
	if err := AssertNoFixtureSelfDeclaration(raw); err != nil {
		return historyexam.Outcome{}, err
	}
	parsed, err := ParseFixtureStream(bytes.NewReader(raw))
	if err != nil {
		return historyexam.Outcome{}, err
	}
	if parsed.ExpectedCommitmentHash != e.Commitment.ExpectedCommitmentHash {
		return historyexam.Outcome{}, fmt.Errorf("%w: got %q want %q", ErrCommitmentHashMismatch, parsed.ExpectedCommitmentHash, e.Commitment.ExpectedCommitmentHash)
	}
	blocks, err := e.Replay(bytes.NewReader(raw))
	if err != nil {
		return historyexam.Outcome{}, err
	}
	pass, reason, _ := EvaluateExamGates(blocks)
	return historyexam.Outcome{Pass: pass, Reason: reason}, nil
}

// ErrHashMismatch indicates fixture-carried hashes disagree with registration.
var ErrHashMismatch = errors.New("event card commitment hash mismatch")

// AssertNoFixtureSelfDeclaration rejects raw fixture bytes that declare forbidden hashes.
func AssertNoFixtureSelfDeclaration(raw []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("%w: malformed json", ErrFixtureSelfDeclare)
	}
	for _, key := range forbiddenFixtureFields {
		if _, ok := probe[key]; ok {
			return fmt.Errorf("%w: %q", ErrFixtureSelfDeclare, key)
		}
	}
	return nil
}
