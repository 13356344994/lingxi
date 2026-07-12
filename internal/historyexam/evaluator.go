package historyexam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrExamUnavailable = errors.New("EXAM_UNAVAILABLE")

// EvaluatorRegistration binds a protocol hash to an evaluator factory. Expected
// policy hashes and grading rules are fixed inside the factory implementation.
type EvaluatorRegistration struct {
	Protocol string
	Factory  func() (Evaluator, error)
}

var protocolEvaluators = map[string]EvaluatorRegistration{}

// RegisterEvaluator installs a reviewed protocol evaluator. Callers cannot inject
// thresholds or pass/fail at runtime through this API.
func RegisterEvaluator(reg EvaluatorRegistration) {
	if reg.Protocol == "" || reg.Factory == nil {
		return
	}
	protocolEvaluators[reg.Protocol] = reg
}

// UnregisterEvaluator removes a protocol registration (tests only).
func UnregisterEvaluator(protocolHash string) {
	delete(protocolEvaluators, protocolHash)
}

// ResetEvaluatorRegistry clears all dynamic registrations (tests only).
func ResetEvaluatorRegistry() {
	for k := range protocolEvaluators {
		delete(protocolEvaluators, k)
	}
}

// SyntheticFixture is the shape of the synthetic exam material this package
// grades. It carries no real records: observations are decision-time-adjusted
// synthetic net-edge samples produced by a fixture generator, and the examiner
// side is allowed to see them because grading inherently evaluates performance.
type SyntheticFixture struct {
	Observations []float64 `json:"observations"`
}

// ThresholdEvaluator is a concrete protocol-bound grader. It encodes a frozen
// grading rule: the mean synthetic net-edge observation must meet a threshold
// that is fixed at construction time (from the protocol), never tuned per exam.
type ThresholdEvaluator struct {
	Protocol  string
	MinMean   float64
	MinSample int
}

// ProtocolHash reports the protocol this evaluator is bound to.
func (e ThresholdEvaluator) ProtocolHash() string { return e.Protocol }

// Evaluate applies the frozen rule. It re-checks protocol binding defensively,
// requires a minimum sample size (else it fails as inconclusive/robustness),
// and returns only a coarse pass/fail with a standardized reason.
func (e ThresholdEvaluator) Evaluate(_ context.Context, paper HiddenPaper, fixture io.Reader) (Outcome, error) {
	if e.Protocol == "" {
		return Outcome{}, errors.New("evaluator has no bound protocol")
	}
	if paper.ProtocolHash != e.Protocol {
		return Outcome{}, ErrProtocolUnbound
	}
	decoder := json.NewDecoder(fixture)
	token, err := decoder.Token()
	if err != nil {
		return Outcome{}, fmt.Errorf("cannot parse synthetic fixture object: %w", err)
	}
	if token != json.Delim('{') {
		return Outcome{}, errors.New("synthetic fixture must be a JSON object")
	}
	var sum float64
	count := 0
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return Outcome{}, err
		}
		key, ok := keyToken.(string)
		if !ok || key != "observations" {
			return Outcome{}, fmt.Errorf("synthetic fixture contains unsupported field %v", keyToken)
		}
		arrayToken, err := decoder.Token()
		if err != nil || arrayToken != json.Delim('[') {
			return Outcome{}, errors.New("observations must be an array")
		}
		for decoder.More() {
			var value float64
			if err := decoder.Decode(&value); err != nil {
				return Outcome{}, fmt.Errorf("cannot parse observation: %w", err)
			}
			sum += value
			count++
		}
		if _, err := decoder.Token(); err != nil {
			return Outcome{}, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Outcome{}, err
	}
	if count < e.MinSample {
		return Outcome{Pass: false, Reason: "ROBUSTNESS_GATE"}, nil
	}
	mean := sum / float64(count)
	if mean >= e.MinMean {
		return Outcome{Pass: true, Reason: "PASS"}, nil
	}
	return Outcome{Pass: false, Reason: "ROBUSTNESS_GATE"}, nil
}

const SyntheticTestProtocolHash = "synthetic-historyexam-v1"

// EvaluatorForProtocol is the only evaluator registry used by the custodian CLI.
// Real protocol evaluators must be added via RegisterEvaluator in reviewed code;
// CLI callers cannot supply thresholds or inject pass/fail. When no registration
// exists the caller must treat the result as EXAM_UNAVAILABLE.
func EvaluatorForProtocol(protocolHash string) (Evaluator, bool) {
	if protocolHash == SyntheticTestProtocolHash {
		return ThresholdEvaluator{Protocol: protocolHash, MinMean: 1.5, MinSample: 1}, true
	}
	reg, ok := protocolEvaluators[protocolHash]
	if !ok {
		return nil, false
	}
	ev, err := reg.Factory()
	if err != nil || ev == nil {
		return nil, false
	}
	return ev, true
}
