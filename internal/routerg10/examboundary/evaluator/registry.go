package evaluator

import (
	"fmt"
	"io"
	"sync"

	"lingxi/internal/historyexam"
	g10protocol "lingxi/internal/routerg10/protocol"
)

// ReplayFunc recomputes exam blocks from a hidden fixture stream.
// Fixture-embedded account metrics must never be trusted for grading.
type ReplayFunc func(io.Reader) ([]FixtureBlock, error)

var (
	regMu         sync.RWMutex
	regCommitment BrainCommitment
	regReplay     ReplayFunc
	regActive     bool
)

// RegisterCommitment explicitly binds expected playbook hashes for the G10 protocol.
// Fixture bytes must never self-declare component hashes; only expected_commitment_hash is allowed.
func RegisterCommitment(c BrainCommitment) error {
	if c.ProtocolHash != g10protocol.RouterProtocolHash {
		return fmt.Errorf("protocol hash mismatch: got %q want %q", c.ProtocolHash, g10protocol.RouterProtocolHash)
	}
	c.ExpectedCommitmentHash = ComputeExpectedCommitmentHash(c)
	if !c.Valid() {
		return fmt.Errorf("incomplete brain commitment")
	}
	regMu.Lock()
	defer regMu.Unlock()
	regCommitment = c
	regActive = true
	historyexam.RegisterEvaluator(historyexam.EvaluatorRegistration{
		Protocol: g10protocol.RouterProtocolHash,
		Factory:  buildRegisteredEvaluator,
	})
	return nil
}

// RegisterReplayEvaluator binds commitment hashes and a custodian replay callback.
// Grading uses only replay-computed blocks; without replay the protocol is EXAM_UNAVAILABLE.
func RegisterReplayEvaluator(c BrainCommitment, replay ReplayFunc) error {
	if replay == nil {
		return fmt.Errorf("evaluator: replay callback required")
	}
	if err := RegisterCommitment(c); err != nil {
		return err
	}
	regMu.Lock()
	defer regMu.Unlock()
	regReplay = replay
	return nil
}

// ResetRegistration clears G10 evaluator registration (tests only).
func ResetRegistration() {
	regMu.Lock()
	defer regMu.Unlock()
	regActive = false
	regCommitment = BrainCommitment{}
	regReplay = nil
	historyexam.UnregisterEvaluator(g10protocol.RouterProtocolHash)
}

func buildRegisteredEvaluator() (historyexam.Evaluator, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	if !regActive || !regCommitment.Valid() || regReplay == nil {
		return nil, historyexam.ErrExamUnavailable
	}
	ev := NewEvaluator(regCommitment, regReplay)
	return ev, nil
}

// RegisteredCommitment returns the active registration for tests.
func RegisteredCommitment() (BrainCommitment, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if !regActive {
		return BrainCommitment{}, false
	}
	return regCommitment, true
}

// RegisteredReplay returns the active replay callback for tests.
func RegisteredReplay() (ReplayFunc, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if regReplay == nil {
		return nil, false
	}
	return regReplay, true
}
