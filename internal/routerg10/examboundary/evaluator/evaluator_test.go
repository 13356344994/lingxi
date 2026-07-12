package evaluator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"lingxi/internal/gauntlet"
	"lingxi/internal/historyexam"
	"lingxi/internal/routerg10/examboundary/evaluator"
	g10protocol "lingxi/internal/routerg10/protocol"
)

func testCommitment() evaluator.BrainCommitment {
	c := evaluator.BrainCommitment{
		PlaybookHash:   "playbook-test-hash",
		ExperimentHash: "experiment-test-hash",
		ProtocolHash:   g10protocol.RouterProtocolHash,
	}
	c.ExpectedCommitmentHash = evaluator.ComputeExpectedCommitmentHash(c)
	return c
}

func resetRegistry(t *testing.T) {
	t.Helper()
	historyexam.ResetEvaluatorRegistry()
	evaluator.ResetRegistration()
	t.Cleanup(func() {
		historyexam.ResetEvaluatorRegistry()
		evaluator.ResetRegistration()
	})
}

func testReplay(commitment evaluator.BrainCommitment) evaluator.ReplayFunc {
	return evaluator.DefaultReplayFunc(commitment)
}

func registerTestReplay(t *testing.T, commitment evaluator.BrainCommitment) {
	t.Helper()
	if err := evaluator.RegisterReplayEvaluator(commitment, testReplay(commitment)); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRequiresExpectedHashes(t *testing.T) {
	resetRegistry(t)
	if err := evaluator.RegisterCommitment(evaluator.BrainCommitment{ProtocolHash: g10protocol.RouterProtocolHash}); err == nil {
		t.Fatal("incomplete commitment must fail registration")
	}
	commitment := testCommitment()
	registerTestReplay(t, commitment)
	ev, ok := historyexam.EvaluatorForProtocol(g10protocol.RouterProtocolHash)
	if !ok || ev.ProtocolHash() != g10protocol.RouterProtocolHash {
		t.Fatalf("registered evaluator missing: ok=%v", ok)
	}
}

func TestEvaluatorForProtocolUnavailableWithoutRegistration(t *testing.T) {
	resetRegistry(t)
	if _, ok := historyexam.EvaluatorForProtocol(g10protocol.RouterProtocolHash); ok {
		t.Fatal("g10 protocol must be unavailable without explicit registration")
	}
}

func TestEvaluatorUnavailableWithoutReplayCallback(t *testing.T) {
	resetRegistry(t)
	if err := evaluator.RegisterCommitment(testCommitment()); err != nil {
		t.Fatal(err)
	}
	if _, ok := historyexam.EvaluatorForProtocol(g10protocol.RouterProtocolHash); ok {
		t.Fatal("commitment-only registration must remain EXAM_UNAVAILABLE without replay")
	}
}

func TestFixtureSelfDeclarationRejected(t *testing.T) {
	raw := []byte(`{"schema_version":"lingxi.routerg10.exam-fixture.v1","playbook_hash":"evil","blocks":[]}`)
	if err := evaluator.AssertNoFixtureSelfDeclaration(raw); err == nil {
		t.Fatal("fixture self-declared playbook hash must be rejected")
	}
}

func TestFixtureBlockSelfScoreRejected(t *testing.T) {
	leaky, _ := json.Marshal(map[string]any{
		"schema_version":           evaluator.FixtureSchemaVersion,
		"expected_commitment_hash": "abc",
		"blocks": []map[string]any{{
			"initial_equity_usdt": 30,
			"account_metrics":     map[string]any{"final_equity_usdt": 999},
			"bars":                []any{},
		}},
	})
	if _, err := evaluator.ParseFixtureStream(bytes.NewReader(leaky)); err == nil {
		t.Fatal("fixture block self-declared account_metrics must be rejected")
	}
}

func TestEvaluatorPassAndGateFailureCoarseReason(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	registerTestReplay(t, commitment)
	ev := evaluator.NewEvaluator(commitment, testReplay(commitment))
	paper := testPaper(t, commitment)
	passRaw := mustFixture(t, commitment.ExpectedCommitmentHash, evaluator.PassingInputBlock())
	out, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(passRaw))
	if err != nil || !out.Pass || out.Reason != "PASS" {
		t.Fatalf("passing fixture: out=%+v err=%v", out, err)
	}
}

func TestBrainTraceDegeneracyGate(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	replay := func(r io.Reader) ([]evaluator.FixtureBlock, error) {
		blocks, err := evaluator.ReplayBlocksFromFixture(r, commitment)
		if err != nil || len(blocks) == 0 {
			return blocks, err
		}
		blocks[0].BrainTrace.ReplayMatchOK = false
		return blocks, nil
	}
	if err := evaluator.RegisterReplayEvaluator(commitment, replay); err != nil {
		t.Fatal(err)
	}
	ev, _ := historyexam.EvaluatorForProtocol(g10protocol.RouterProtocolHash)
	paper := testPaper(t, commitment)
	raw := mustFixture(t, commitment.ExpectedCommitmentHash, evaluator.PassingInputBlock())
	out, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(raw))
	if err != nil || out.Pass || out.Reason != g10protocol.BrainDegeneracyStatus {
		t.Fatalf("brain trace failure: out=%+v err=%v", out, err)
	}
}

func TestFakeFixtureMetricsRejected(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	registerTestReplay(t, commitment)
	ev, ok := historyexam.EvaluatorForProtocol(g10protocol.RouterProtocolHash)
	if !ok {
		t.Fatal("evaluator missing")
	}
	paper := testPaper(t, commitment)
	raw := mustFixture(t, commitment.ExpectedCommitmentHash, evaluator.PassingInputBlock())
	out, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(raw))
	if err != nil || !out.Pass {
		t.Fatalf("replay-graded fixture must pass with honest replay: out=%+v err=%v", out, err)
	}
}

func TestEvaluatorCommitmentHashMismatch(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	registerTestReplay(t, commitment)
	ev := evaluator.NewEvaluator(commitment, testReplay(commitment))
	paper := testPaper(t, commitment)
	raw, _ := evaluator.EncodeFixture("deadbeef", []evaluator.FixtureInputBlock{evaluator.PassingInputBlock()})
	if _, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(raw)); err == nil {
		t.Fatal("commitment hash mismatch must fail closed")
	}
}

func TestEvaluatorWrongProtocolRejected(t *testing.T) {
	resetRegistry(t)
	ev := evaluator.NewEvaluator(testCommitment(), testReplay(testCommitment()))
	paper, err := historyexam.NewHiddenPaper("other-protocol", historyexam.PaperSpec{
		ExamID: "x", Queue: 1, Kind: gauntlet.KindStandard, Family: "high_volatility", AlphaSpend: 1,
		Stratum: historyexam.StratumInput{Axes: map[string]string{"volatility": "high"}},
		ClusterIDs: []string{"c1"}, Start: day(2024, 5, 1), End: day(2024, 6, 1),
		Fixture: mustFixture(t, testCommitment().ExpectedCommitmentHash, evaluator.PassingInputBlock()), Nonce: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(mustFixture(t, testCommitment().ExpectedCommitmentHash, evaluator.PassingInputBlock()))); err != historyexam.ErrProtocolUnbound {
		t.Fatalf("wrong protocol must fail closed: %v", err)
	}
}

func TestHiddenLeakForbiddenTopLevelField(t *testing.T) {
	leaky, _ := json.Marshal(map[string]any{
		"schema_version":           evaluator.FixtureSchemaVersion,
		"expected_commitment_hash": "abc",
		"blocks":                   []evaluator.FixtureInputBlock{evaluator.PassingInputBlock()},
		"cluster_id":               "secret",
	})
	if _, err := evaluator.ParseFixtureStream(bytes.NewReader(leaky)); err == nil {
		t.Fatal("unexpected top-level field must fail")
	}
}

func TestEvaluateExamGatesEmptyBlocks(t *testing.T) {
	pass, reason, _ := evaluator.EvaluateExamGates(nil)
	if pass || reason != "ROBUSTNESS_GATE" {
		t.Fatalf("empty blocks: pass=%v reason=%q", pass, reason)
	}
}

func TestComputeExpectedCommitmentHashBinding(t *testing.T) {
	c := evaluator.BrainCommitment{
		PlaybookHash:   "playbook-a",
		ExperimentHash: "exp-b",
		ProtocolHash:   g10protocol.RouterProtocolHash,
	}
	got := evaluator.ComputeExpectedCommitmentHash(c)
	if got == "" {
		t.Fatal("hash must be non-empty")
	}
	c2 := c
	c2.PlaybookHash = "playbook-c"
	if evaluator.ComputeExpectedCommitmentHash(c2) == got {
		t.Fatal("hash must change when playbook hash changes")
	}
}

func TestReplayBlocksFromFixtureRecomputesMetrics(t *testing.T) {
	commitment := testCommitment()
	raw := mustFixture(t, commitment.ExpectedCommitmentHash, evaluator.PassingInputBlock())
	blocks, err := evaluator.ReplayBlocksFromFixture(bytes.NewReader(raw), commitment)
	if err != nil || len(blocks) == 0 {
		t.Fatalf("replay: err=%v blocks=%d", err, len(blocks))
	}
	if !blocks[0].BrainTrace.ReplayMatchOK {
		t.Fatal("brain trace replay must match on deterministic replay")
	}
}

func testPaper(t *testing.T, commitment evaluator.BrainCommitment) historyexam.HiddenPaper {
	t.Helper()
	paper, err := historyexam.NewHiddenPaper(g10protocol.RouterProtocolHash, historyexam.PaperSpec{
		ExamID: "g10-standard", Queue: 1, Kind: gauntlet.KindStandard, Family: "high_volatility", AlphaSpend: 1,
		Stratum: historyexam.StratumInput{Axes: map[string]string{"volatility": "high"}},
		ClusterIDs: []string{"c1"}, Start: day(2024, 5, 1), End: day(2024, 6, 1),
		Fixture: mustFixture(t, commitment.ExpectedCommitmentHash, evaluator.PassingInputBlock()), Nonce: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return paper
}

func mustFixture(t *testing.T, commitmentHash string, blocks ...evaluator.FixtureInputBlock) []byte {
	t.Helper()
	raw, err := evaluator.EncodeFixture(commitmentHash, blocks)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func day(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}
