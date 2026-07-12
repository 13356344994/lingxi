package examevaluator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"lingxi/internal/gauntlet"
	"lingxi/internal/historyexam"
	"lingxi/internal/routerg5/examevaluator"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func testCommitment() examevaluator.PolicyCommitment {
	c := examevaluator.PolicyCommitment{
		PolicyHash:     "policy-test-hash",
		FeatureHash:    "feature-test-hash",
		ExperimentHash: "experiment-test-hash",
		ProtocolHash:   g5protocol.RouterProtocolHash,
	}
	c.ExpectedCommitmentHash = examevaluator.ComputeExpectedCommitmentHash(c)
	return c
}

func resetRegistry(t *testing.T) {
	t.Helper()
	historyexam.ResetEvaluatorRegistry()
	examevaluator.ResetRegistration()
	t.Cleanup(func() {
		historyexam.ResetEvaluatorRegistry()
		examevaluator.ResetRegistration()
	})
}

func testReplay(blocks []examevaluator.FixtureBlock) examevaluator.ReplayFunc {
	return func(r io.Reader) ([]examevaluator.FixtureBlock, error) {
		if _, err := examevaluator.ParseFixtureStream(r); err != nil {
			return nil, err
		}
		return blocks, nil
	}
}

func registerTestReplay(t *testing.T, blocks ...examevaluator.FixtureBlock) {
	t.Helper()
	if err := examevaluator.RegisterReplayEvaluator(testCommitment(), testReplay(blocks)); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRequiresExpectedHashes(t *testing.T) {
	resetRegistry(t)
	if err := examevaluator.RegisterCommitment(examevaluator.PolicyCommitment{ProtocolHash: g5protocol.RouterProtocolHash}); err == nil {
		t.Fatal("incomplete commitment must fail registration")
	}
	if err := examevaluator.RegisterReplayEvaluator(testCommitment(), testReplay([]examevaluator.FixtureBlock{examevaluator.PassingBlock()})); err != nil {
		t.Fatal(err)
	}
	ev, ok := historyexam.EvaluatorForProtocol(g5protocol.RouterProtocolHash)
	if !ok || ev.ProtocolHash() != g5protocol.RouterProtocolHash {
		t.Fatalf("registered evaluator missing: ok=%v", ok)
	}
}

func TestEvaluatorForProtocolUnavailableWithoutRegistration(t *testing.T) {
	resetRegistry(t)
	if _, ok := historyexam.EvaluatorForProtocol(g5protocol.RouterProtocolHash); ok {
		t.Fatal("g5 protocol must be unavailable without explicit registration")
	}
}

func TestEvaluatorUnavailableWithoutReplayCallback(t *testing.T) {
	resetRegistry(t)
	if err := examevaluator.RegisterCommitment(testCommitment()); err != nil {
		t.Fatal(err)
	}
	if _, ok := historyexam.EvaluatorForProtocol(g5protocol.RouterProtocolHash); ok {
		t.Fatal("commitment-only registration must remain EXAM_UNAVAILABLE without replay")
	}
}

func TestFixtureSelfDeclarationRejected(t *testing.T) {
	raw := []byte(`{"schema_version":"lingxi.routerg5.exam-fixture.v1","policy_hash":"evil","blocks":[]}`)
	if err := examevaluator.AssertNoFixtureSelfDeclaration(raw); err == nil {
		t.Fatal("fixture self-declared policy hash must be rejected")
	}
}

func TestEvaluatorPassAndGateFailureCoarseReason(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	registerTestReplay(t, examevaluator.PassingBlock())
	ev := examevaluator.NewEvaluator(commitment, testReplay([]examevaluator.FixtureBlock{examevaluator.PassingBlock()}))
	paper := testPaper(t, examevaluator.PassingBlock())
	passRaw := mustFixture(t, commitment.ExpectedCommitmentHash, examevaluator.PassingBlock())
	out, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(passRaw))
	if err != nil || !out.Pass || out.Reason != "PASS" {
		t.Fatalf("passing fixture: out=%+v err=%v", out, err)
	}
	failBlock := examevaluator.PassingBlock()
	failBlock.AccountMetrics.FinalEquityUSDT = 1
	failRaw, _ := examevaluator.EncodeFixture(commitment.ExpectedCommitmentHash, []examevaluator.FixtureBlock{failBlock})
	evFail := examevaluator.NewEvaluator(commitment, testReplay([]examevaluator.FixtureBlock{failBlock}))
	out, err = evFail.Evaluate(context.Background(), paper, bytes.NewReader(failRaw))
	if err != nil || out.Pass || out.Reason != "ROBUSTNESS_GATE" {
		t.Fatalf("failing net return: out=%+v err=%v", out, err)
	}
}

func TestFakeFixtureMetricsRejected(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	replay := func(r io.Reader) ([]examevaluator.FixtureBlock, error) {
		if _, err := examevaluator.ParseFixtureStream(r); err != nil {
			return nil, err
		}
		fail := examevaluator.PassingBlock()
		fail.AccountMetrics.FinalEquityUSDT = 1
		return []examevaluator.FixtureBlock{fail}, nil
	}
	if err := examevaluator.RegisterReplayEvaluator(commitment, replay); err != nil {
		t.Fatal(err)
	}
	ev, ok := historyexam.EvaluatorForProtocol(g5protocol.RouterProtocolHash)
	if !ok {
		t.Fatal("evaluator missing")
	}
	paper := testPaper(t, examevaluator.PassingBlock())
	good := examevaluator.PassingBlock()
	good.AccountMetrics.FinalEquityUSDT = float64(g5protocol.ValidationInitialEquities[0]) * 100
	raw, _ := examevaluator.EncodeFixture(commitment.ExpectedCommitmentHash, []examevaluator.FixtureBlock{good})
	out, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(raw))
	if err != nil || out.Pass {
		t.Fatalf("fake fixture metrics must not grade pass: out=%+v err=%v", out, err)
	}
}

func TestEvaluatorCommitmentHashMismatch(t *testing.T) {
	resetRegistry(t)
	commitment := testCommitment()
	registerTestReplay(t, examevaluator.PassingBlock())
	ev := examevaluator.NewEvaluator(commitment, testReplay([]examevaluator.FixtureBlock{examevaluator.PassingBlock()}))
	paper := testPaper(t, examevaluator.PassingBlock())
	raw, _ := examevaluator.EncodeFixture("deadbeef", []examevaluator.FixtureBlock{examevaluator.PassingBlock()})
	if _, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(raw)); err == nil {
		t.Fatal("commitment hash mismatch must fail closed")
	}
}

func TestEvaluatorWrongProtocolRejected(t *testing.T) {
	resetRegistry(t)
	ev := examevaluator.NewEvaluator(testCommitment(), testReplay([]examevaluator.FixtureBlock{examevaluator.PassingBlock()}))
	paper, err := historyexam.NewHiddenPaper("other-protocol", historyexam.PaperSpec{
		ExamID: "x", Queue: 1, Kind: gauntlet.KindStandard, Family: "high_volatility", AlphaSpend: 1,
		Stratum: historyexam.StratumInput{Axes: map[string]string{"volatility": "high"}},
		ClusterIDs: []string{"c1"}, Start: day(2024, 5, 1), End: day(2024, 6, 1),
		Fixture: mustFixture(t, testCommitment().ExpectedCommitmentHash, examevaluator.PassingBlock()), Nonce: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Evaluate(context.Background(), paper, bytes.NewReader(mustFixture(t, testCommitment().ExpectedCommitmentHash, examevaluator.PassingBlock()))); err != historyexam.ErrProtocolUnbound {
		t.Fatalf("wrong protocol must fail closed: %v", err)
	}
}

func TestHiddenLeakForbiddenTopLevelField(t *testing.T) {
	leaky, _ := json.Marshal(map[string]any{
		"schema_version":           examevaluator.FixtureSchemaVersion,
		"expected_commitment_hash": "abc",
		"blocks":                   []examevaluator.FixtureBlock{examevaluator.PassingBlock()},
		"cluster_id":               "secret",
	})
	if _, err := examevaluator.ParseFixtureStream(bytes.NewReader(leaky)); err == nil {
		t.Fatal("unexpected top-level field must fail")
	}
}

func TestEvaluateExamGatesEmptyBlocks(t *testing.T) {
	pass, reason, _ := examevaluator.EvaluateExamGates(nil)
	if pass || reason != "ROBUSTNESS_GATE" {
		t.Fatalf("empty blocks: pass=%v reason=%q", pass, reason)
	}
}

func testPaper(t *testing.T, block examevaluator.FixtureBlock) historyexam.HiddenPaper {
	t.Helper()
	commitment := testCommitment()
	paper, err := historyexam.NewHiddenPaper(g5protocol.RouterProtocolHash, historyexam.PaperSpec{
		ExamID: "g5-standard", Queue: 1, Kind: gauntlet.KindStandard, Family: "high_volatility", AlphaSpend: 1,
		Stratum: historyexam.StratumInput{Axes: map[string]string{"volatility": "high"}},
		ClusterIDs: []string{"c1"}, Start: day(2024, 5, 1), End: day(2024, 6, 1),
		Fixture: mustFixture(t, commitment.ExpectedCommitmentHash, block), Nonce: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return paper
}

func mustFixture(t *testing.T, commitmentHash string, blocks ...examevaluator.FixtureBlock) []byte {
	t.Helper()
	raw, err := examevaluator.EncodeFixture(commitmentHash, blocks)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func day(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
}
