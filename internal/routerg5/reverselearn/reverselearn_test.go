package reverselearn

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestTrainingLabelReservoirUnbiasedAllVerbs(t *testing.T) {
	res := outcome.NewTrainingLabelReservoir(100)
	verbs := map[string]bool{}
	var pos, neg, flat int
	for i := 0; i < 300; i++ {
		verb := g5protocol.ActionVerbs[i%len(g5protocol.ActionVerbs)]
		reward := float64(i%50 - 25)
		rec := outcome.RewardRecord{
			Symbol: "ETH-USDT-SWAP", Partition: outcome.PartitionDiscovery,
			DecisionTime: time.Unix(int64(i), 0).UTC(),
			StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: actionspace.BaseEquityUSDT},
			StateLabel: actionspace.SideFlat,
			Action: actionspace.Action{
				Verb: verb, Direction: actionspace.DirectionFlat,
				TargetHorizon: "1h", SizeFraction: 0.25, DelayBars: 0,
			},
			Label:                      "l",
			ConservativeEquityDeltaBPS: reward,
		}
		res.Consider(rec)
		verbs[verb] = true
		if reward > 0 {
			pos++
		} else if reward < 0 {
			neg++
		} else {
			flat++
		}
	}
	entries := res.Entries()
	if len(entries) != 100 {
		t.Fatalf("cap = %d, want 100", len(entries))
	}
	gotPos, gotNeg, gotFlat := 0, 0, 0
	gotVerbs := map[string]bool{}
	for _, e := range entries {
		gotVerbs[e.Action.Verb] = true
		if e.ConservativeEquityDeltaBPS > 0 {
			gotPos++
		} else if e.ConservativeEquityDeltaBPS < 0 {
			gotNeg++
		} else {
			gotFlat++
		}
	}
	if gotPos == 0 || gotNeg == 0 {
		t.Fatal("reservoir must retain positive and negative samples")
	}
	if len(gotVerbs) < 3 {
		t.Fatalf("expected multiple verbs, got %d", len(gotVerbs))
	}
	_ = flat
	_ = verbs
}

func TestJoinDiscoveryOnly(t *testing.T) {
	decision := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	labels := []outcome.TrainingLabelEntry{{
		Symbol: "BTC-USDT-SWAP", DecisionTime: decision,
		StateLabel: actionspace.SideFlat,
		Action: actionspace.Action{Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong, TargetHorizon: "1h"},
		Label: "x", ConservativeEquityDeltaBPS: 5,
	}}
	schema, _ := features.FrozenFeatureSchema()
	ctx := features.DecisionContext{
		SchemaVersion: schema.SchemaVersion, FeatureHash: schema.FeatureHash,
		ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP", DecisionTime: decision,
		Partition: outcome.PartitionDiscovery,
		Features:  map[string]float64{"btc_return": 0.1, "symbol_return": 0.2},
	}
	bad := features.DecisionContext{
		ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP", DecisionTime: decision,
		Partition: outcome.PartitionValidation,
		Features:  map[string]float64{"btc_return": 0.1},
	}
	joined, err := JoinDiscoveryExamples(labels, map[string]features.DecisionContext{
		JoinKey(ctx.Symbol, ctx.DecisionTime): ctx,
		JoinKey(bad.Symbol, bad.DecisionTime): bad,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(joined) != 1 {
		t.Fatalf("want 1 joined row, got %d", len(joined))
	}
}

func TestNormalizationNoHoldoutLeak(t *testing.T) {
	featureKeys := []string{"btc_return"}
	train := []TrainingExample{
		{Features: map[string]float64{"btc_return": 1}},
		{Features: map[string]float64{"btc_return": 1}},
		{Features: map[string]float64{"btc_return": 1}},
	}
	holdout := []TrainingExample{{Features: map[string]float64{"btc_return": 100}}}
	norm := FitNormalization(featureKeys, train)
	actionBlock := []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	x := BuildFeatureVector(featureKeys, norm, holdout[0].Features, actionBlock)
	leakedNorm := FitNormalization(featureKeys, append(train, holdout...))
	xLeak := BuildFeatureVector(featureKeys, leakedNorm, holdout[0].Features, actionBlock)
	if math.Abs(x[0]-xLeak[0]) < 1e-3 {
		t.Fatal("normalization must not incorporate holdout distribution")
	}
}

func TestLambdaSelectionDeterministicWeights(t *testing.T) {
	examples := syntheticExamples(t)
	out1, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	out2, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	p1 := out1.Policy
	p2 := out2.Policy
	if p1.PolicyHash != p2.PolicyHash {
		t.Fatalf("policy hash not deterministic: %s vs %s", p1.PolicyHash, p2.PolicyHash)
	}
	if len(p1.Weights) != len(p2.Weights) {
		t.Fatal("weight length mismatch")
	}
	for i := range p1.Weights {
		if math.Abs(p1.Weights[i]-p2.Weights[i]) > 1e-9 {
			t.Fatalf("weights[%d] drift", i)
		}
	}
}

func TestDifferentActionsDifferentScores(t *testing.T) {
	examples := syntheticExamples(t)
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	policy := outcome.Policy
	actionSpec := policy.ActionEncoding
	norm := policy.Normalization
	base := examples[0]
	long := base
	long.Action.Verb = actionspace.VerbOpenLong
	long.Action.Direction = actionspace.DirectionLong
	short := base
	short.Action.Verb = actionspace.VerbOpenShort
	short.Action.Direction = actionspace.DirectionShort
	sLong := PredictScore(long, policy.Weights, policy.FeatureKeys, actionSpec, policy.HorizonLabels, norm)
	sShort := PredictScore(short, policy.Weights, policy.FeatureKeys, actionSpec, policy.HorizonLabels, norm)
	if math.Abs(sLong-sShort) < 1e-6 {
		t.Fatal("different directions must produce different scores")
	}
}

func TestCheckpointTamperAtomic(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cp := Checkpoint{
		ProfileID: "okx_1h", Generation: g5protocol.Generation,
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
		OutputDir: dir, Done: false,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail")
	}
}

func syntheticExamples(t *testing.T) []TrainingExample {
	t.Helper()
	return SyntheticDiverseExamples()
}

func TestContextDirectionInteractionVariesByRegime(t *testing.T) {
	examples := SyntheticDiverseExamples()
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	policy := outcome.Policy
	bull := syntheticFeatureRegime(0, 0)
	bear := syntheticFeatureRegime(1, 0)
	neutral := syntheticFeatureRegime(2, 0)
	pick := func(features map[string]float64) string {
		bestScore := math.Inf(-1)
		bestDir := ""
		for _, horizon := range []string{"1h", "4h"} {
			for _, spec := range []struct {
				verb string
				dir  string
			}{
				{actionspace.VerbOpenLong, actionspace.DirectionLong},
				{actionspace.VerbOpenShort, actionspace.DirectionShort},
				{actionspace.VerbFlat, actionspace.DirectionFlat},
			} {
				ex := TrainingExample{
					StateLabel: actionspace.SideFlat,
					Action: actionspace.Action{
						Verb: spec.verb, Direction: spec.dir, TargetHorizon: horizon, SizeFraction: 0.25,
					},
					Features: features,
				}
				score := PredictScore(ex, policy.Weights, policy.FeatureKeys, policy.ActionEncoding, policy.HorizonLabels, policy.Normalization)
				if score > bestScore {
					bestScore = score
					bestDir = spec.dir
				}
			}
		}
		return bestDir
	}
	bullDir := pick(bull)
	bearDir := pick(bear)
	neutralDir := pick(neutral)
	if bullDir == bearDir {
		t.Fatalf("policy must vary direction between bull and bear: bull=%s bear=%s neutral=%s", bullDir, bearDir, neutralDir)
	}
	if bullDir != actionspace.DirectionLong {
		t.Fatalf("bull regime should prefer long, got %s", bullDir)
	}
}

func TestExperimentLedgerAppendOnly(t *testing.T) {
	examples := syntheticExamples(t)
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	ledger := outcome.Ledger
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Attempts) != len(g5protocol.LambdaGrid) {
		t.Fatalf("attempts = %d, want %d", len(ledger.Attempts), len(g5protocol.LambdaGrid))
	}
	for i, attempt := range ledger.Attempts {
		if attempt.AttemptOrdinal != i+1 {
			t.Fatal("ledger must be append-only with contiguous ordinals")
		}
	}
}

func TestExamActionsZero(t *testing.T) {
	examples := syntheticExamples(t)
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	policy := outcome.Policy
	ledger := outcome.Ledger
	report := outcome.Report
	if policy.ExamActions != 0 || ledger.Attempts[0].Epochs != g5protocol.SGDEpochs || report.ExamActions != 0 {
		t.Fatal("exam_actions must remain 0 in research stage outputs")
	}
}

func mustFeatureHash(t *testing.T) string {
	t.Helper()
	h, err := features.FeatureHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDiverseTrainedPolicyNotDegenerate(t *testing.T) {
	examples := syntheticExamples(t)
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	pol := outcome.Policy
	if IsDegeneratePolicyCandidate(pol.Weights, pol.FeatureKeys, pol.ActionEncoding, pol.Normalization, pol.HorizonLabels) {
		t.Fatal("diverse training must not seal degenerate policy")
	}
}

func TestAllLambdaAttemptsRejectedWritesDegenerateArtifacts(t *testing.T) {
	examples := SyntheticFixedDirectionExamples()
	outcome, err := RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.AllAttemptsRejected {
		t.Fatal("expected all lambda attempts rejected")
	}
	if outcome.Report.Status != StatusTrainingRejectedDegenerate {
		t.Fatalf("report status=%q", outcome.Report.Status)
	}
	if outcome.Report.PolicyHash != "" {
		t.Fatal("degenerate report must not bind policy hash")
	}
	if outcome.Report.ExperimentHash != outcome.Ledger.LedgerHash {
		t.Fatal("experiment hash must bind sealed ledger")
	}
	if err := ValidateLedger(outcome.Ledger); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range outcome.Ledger.Attempts {
		if attempt.Status != "rejected_fixed_action" {
			t.Fatalf("attempt %d status=%q", attempt.AttemptOrdinal, attempt.Status)
		}
	}
}
