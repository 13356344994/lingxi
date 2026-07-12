package historyexam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
)

// coarseReasons mirrors the standardized codes the gauntlet accepts. The grader
// validates the evaluator's reason against this set before it ever reaches the
// signed receipt, so a stray internal reason cannot leak to the researcher.
var coarseReasons = map[string]struct{}{
	"PASS":                    {},
	"ALPHA_BUDGET":            {},
	"FAMILY_MULTIPLE_TESTING": {},
	"RISK_GATE":               {},
	"ROBUSTNESS_GATE":         {},
	"EXECUTION_GATE":          {},
	"CRASH":                   {},
}

// Outcome is the verdict a protocol-bound Evaluator returns. It is deliberately
// the only channel through which pass/fail enters grading; there is no API that
// lets a caller inject a raw boolean.
type Outcome struct {
	Pass   bool
	Reason string
}

// Evaluator is the protocol-bound grader. An implementation encodes a frozen
// grading protocol and must reject any paper whose ProtocolHash does not match
// its bound protocol. The evaluator alone decides pass/fail from the hidden
// fixture; no external caller can override it.
type Evaluator interface {
	// ProtocolHash returns the protocol this evaluator is bound to.
	ProtocolHash() string
	// Evaluate grades a hidden paper against its verified fixture bytes.
	Evaluate(ctx context.Context, paper HiddenPaper, fixture io.Reader) (Outcome, error)
}

var (
	// ErrNoRunningPaper indicates there is no unsealed paper to grade.
	ErrNoRunningPaper = errors.New("no unsealed paper to grade")
	// ErrCommitmentMismatch indicates the unsealed exam's commitments do not
	// match the hidden paper store; grading fails closed.
	ErrCommitmentMismatch = errors.New("running exam commitments do not match the hidden paper store")
	// ErrProtocolUnbound indicates the evaluator is not bound to the paper's
	// protocol.
	ErrProtocolUnbound = errors.New("evaluator is not bound to the paper protocol")
	// ErrFixtureCorrupt indicates the fixture bytes no longer match DataHash.
	ErrFixtureCorrupt = errors.New("fixture bytes do not match the committed data hash")
)

// runningExam returns the currently unsealed exam for a generation.
func runningExam(state gauntlet.State, generationID string) (exam.Exam, bool) {
	for _, g := range state.Generations {
		if g.ID != generationID {
			continue
		}
		for _, e := range g.Exams {
			if e.Status == exam.StatusRunning {
				return e, true
			}
		}
	}
	return exam.Exam{}, false
}

// Grade is the real grader-binding API. Unlike gauntlet.Grade, it accepts no
// pass boolean. It:
//  1. locates the currently unsealed exam in the gauntlet generation;
//  2. looks up the matching hidden paper and re-verifies the running exam's
//     DataHash, ChallengeHash, and ProtocolHash against the hidden store;
//  3. checks the evaluator is bound to that protocol;
//  4. re-verifies the fixture bytes against the committed DataHash;
//  5. invokes the protocol-bound Evaluator to obtain the verdict;
//  6. consumes the paper through the gauntlet and returns only a SignedResult.
//
// The manifest, evaluator, and fixture never cross to the researcher. The
// researcher receives nothing but the returned SignedResult.
func Grade(
	ctx context.Context,
	state gauntlet.State,
	access gauntlet.AccessController,
	role gauntlet.Role,
	generationID string,
	manifest HiddenManifest,
	evaluator Evaluator,
	now time.Time,
	signer exam.ReceiptSigner,
) (gauntlet.State, gauntlet.SignedResult, error) {
	if evaluator == nil {
		return state, gauntlet.SignedResult{}, errors.New("a protocol-bound evaluator is required")
	}
	if manifest.Generation != generationID {
		return state, gauntlet.SignedResult{}, fmt.Errorf("hidden manifest is for generation %q, not %q", manifest.Generation, generationID)
	}
	running, ok := runningExam(state, generationID)
	if !ok {
		return state, gauntlet.SignedResult{}, ErrNoRunningPaper
	}
	paper, ok := manifest.paperByExamID(running.ExamID)
	if !ok {
		return state, gauntlet.SignedResult{}, ErrCommitmentMismatch
	}
	if running.ChallengeHash != paper.ChallengeHash ||
		running.DataHash != paper.DataHash ||
		running.ProtocolHash != paper.ProtocolHash {
		return state, gauntlet.SignedResult{}, ErrCommitmentMismatch
	}
	if evaluator.ProtocolHash() != paper.ProtocolHash {
		return state, gauntlet.SignedResult{}, ErrProtocolUnbound
	}
	fixture, err := openPaperData(paper)
	if err != nil {
		return state, gauntlet.SignedResult{}, err
	}
	defer fixture.Close()
	hash := sha256.New()
	counter := &byteCounter{}
	verifiedStream := io.TeeReader(fixture, io.MultiWriter(hash, counter))
	outcome, err := evaluator.Evaluate(ctx, paper, verifiedStream)
	if err != nil {
		return state, gauntlet.SignedResult{}, err
	}
	if _, err := io.Copy(io.Discard, verifiedStream); err != nil {
		return state, gauntlet.SignedResult{}, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != paper.DataHash || counter.n != paper.FixtureSize {
		return state, gauntlet.SignedResult{}, ErrFixtureCorrupt
	}
	if _, ok := coarseReasons[outcome.Reason]; !ok {
		return state, gauntlet.SignedResult{}, fmt.Errorf("evaluator returned non-standard reason %q", outcome.Reason)
	}
	if outcome.Pass && outcome.Reason != "PASS" {
		return state, gauntlet.SignedResult{}, errors.New("a passing verdict must carry reason PASS")
	}
	if !outcome.Pass && outcome.Reason == "PASS" {
		return state, gauntlet.SignedResult{}, errors.New("a failing verdict must not carry reason PASS")
	}
	return gauntlet.Grade(ctx, state, access, role, generationID, outcome.Pass, outcome.Reason, now, signer)
}

type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
