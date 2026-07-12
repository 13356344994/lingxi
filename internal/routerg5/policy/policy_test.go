package policy_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/reverselearn"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestContinuousSizeSigmoidRange(t *testing.T) {
	size := policy.ContinuousSize(2.0, 0.8, map[string]float64{
		"capacity_headroom": 1.0, "sky_confidence": 0.9,
	})
	if size <= 0 || size > 1 {
		t.Fatalf("size=%v want (0,1]", size)
	}
	zero := policy.ContinuousSize(-10, 0.1, map[string]float64{"capacity_headroom": 0})
	if zero != 0 {
		t.Fatalf("low edge should map to zero size, got %v", zero)
	}
}

func TestDifferentActionsDifferentScores(t *testing.T) {
	pol := mustPolicy(t)
	input := policy.DecisionInput{
		Symbol: "BTC-USDT-SWAP", DecisionTime: time.Now().UTC(),
		Partition: outcome.PartitionValidation,
		Features:  synthFeatures(1.0), StateLabel: actionspace.SideFlat,
		StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 100},
	}
	horizons := []string{"1h", "4h"}
	scored, err := policy.ScoreActions(pol, input, horizons)
	if err != nil || len(scored) < 2 {
		t.Fatal("expected multiple scored actions")
	}
	if math.Abs(scored[0].Score-scored[len(scored)-1].Score) < 1e-9 {
		t.Fatal("different actions must produce different scores")
	}
}

func TestTopKClusterAssignment(t *testing.T) {
	features := synthFeatures(1.2)
	clusterHigh := policy.ClusterFor(features, "ETH-USDT-SWAP")
	features["symbol_beta"] = 0.5
	clusterLow := policy.ClusterFor(features, "SOL-USDT-SWAP")
	if clusterHigh == clusterLow {
		t.Fatalf("beta clusters must differ: %s vs %s", clusterHigh, clusterLow)
	}
}

func TestRejectDiscoveryLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oracle_training_labels.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := policy.RejectDiscoveryLabels(path); err == nil {
		t.Fatal("discovery labels must be rejected in validation")
	}
}

func TestPolicyHashChainTamper(t *testing.T) {
	dir := writePolicyBundle(t)
	raw, _ := os.ReadFile(filepath.Join(dir, "g5_policy.json"))
	tampered := string(raw)
	tampered = tampered[:len(tampered)-2] + " "
	if err := os.WriteFile(filepath.Join(dir, "g5_policy.json"), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := policy.LoadPolicyBundle(dir); err == nil {
		t.Fatal("tampered policy must fail verification")
	}
}

func TestZeroPolicyRejected(t *testing.T) {
	pol := mustPolicy(t)
	for i := range pol.Weights {
		pol.Weights[i] = 0
	}
	input := policy.DecisionInput{
		Symbol: "BTC-USDT-SWAP", Features: synthFeatures(0),
		StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 100},
		StateLabel: actionspace.SideFlat,
	}
	scored, err := policy.ScoreActions(pol, input, []string{"1h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) == 0 {
		t.Fatal("zero weights should still enumerate actions")
	}
	for _, s := range scored {
		if s.Score != 0 {
			t.Fatalf("zero policy should score 0, got %v", s.Score)
		}
	}
}

func TestLoadNoFrozenPolicyBundle(t *testing.T) {
	dir := writeNoFrozenPolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reverselearn.IsNoFrozenPolicy(bundle.PolicyManifest) {
		t.Fatal("expected NO_FROZEN_POLICY")
	}
	if bundle.Policy.PolicyHash != "" {
		t.Fatal("bundle must not expose scoreable policy")
	}
}

func mustPolicy(t *testing.T) reverselearn.FrozenPolicy {
	t.Helper()
	examples := synthExamples()
	outcome, err := reverselearn.RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	return outcome.Policy
}

func writePolicyBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	examples := synthExamples()
	outcome, err := reverselearn.RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil {
		t.Fatal(err)
	}
	pol := outcome.Policy
	ledger := outcome.Ledger
	if err := reverselearn.WritePolicy(dir, pol); err != nil {
		t.Fatal(err)
	}
	if err := reverselearn.WriteLedger(dir, ledger); err != nil {
		t.Fatal(err)
	}
	manifest := reverselearn.PolicyManifest{
		SchemaVersion: reverselearn.SchemaVersionManifest,
		Generation:    g5protocol.Generation,
		ProtocolHash:    g5protocol.RouterProtocolHash,
		FeatureHash:     pol.FeatureHash,
		PolicyHash:      pol.PolicyHash,
		ExperimentHash:  pol.ExperimentHash,
		ExamActions:     0,
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "policy_manifest.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func synthExamples() []reverselearn.TrainingExample {
	return reverselearn.SyntheticDiverseExamples()
}

func synthFeatures(btcReturn float64) map[string]float64 {
	return map[string]float64{
		"btc_return": btcReturn, "symbol_return": btcReturn * 2,
		"btc_vol": 0.1, "btc_trend": 0.2, "btc_drawdown": 0.05, "btc_funding_state": 0,
		"sky_confidence": 0.8, "mask_btc_funding_missing": 0, "symbol_rv": 0.1,
		"symbol_beta": 1.0, "btc_relative_return": 0.01, "liquidity_percentile": 0.5,
		"quote_volume_rank": 0.5, "return_rank": 0.5, "vol_rank": 0.5,
		"liquidity_rank": 0.5, "beta_rank": 0.5, "btc_relative_strength_rank": 0.5,
		"liquidity_score": 0.5, "vol_cost_penalty": 0.1, "capacity_headroom": 0.8,
		"cost_estimate": 0.05,
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

func writeNoFrozenPolicyDir(t *testing.T) string {
	t.Helper()
	examples := reverselearn.SyntheticFixedDirectionExamples()
	trainOutcome, err := reverselearn.RunTraining(examples, "okx_1h", mustFeatureHash(t))
	if err != nil || !trainOutcome.AllAttemptsRejected {
		t.Fatalf("training: err=%v rejected=%v", err, trainOutcome.AllAttemptsRejected)
	}
	dir := t.TempDir()
	if err := reverselearn.WriteLedger(dir, trainOutcome.Ledger); err != nil {
		t.Fatal(err)
	}
	if err := reverselearn.WriteTrainingReport(dir, trainOutcome.Report); err != nil {
		t.Fatal(err)
	}
	artifactHashes := map[string]string{}
	for _, name := range []string{"g5_policy_ledger.json", "g5_training_report.json"} {
		hash, err := reverselearn.HashFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		artifactHashes[name] = hash
	}
	cfg := reverselearn.RunConfig{
		SchemaVersion:   reverselearn.SchemaVersionConfig,
		ProfileID:       "okx_1h",
		Generation:      g5protocol.Generation,
		ProtocolHash:    g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash,
	}
	manifest, err := reverselearn.BuildNoFrozenPolicyManifest(cfg, reverselearn.CubeBundle{
		Manifest: outcome.CubeManifest{ManifestHash: "cube", TrainingLabelHash: "labels"},
	}, reverselearn.FeatureBundle{
		Manifest: features.ContextManifest{FeatureHash: trainOutcome.Report.FeatureHash, ManifestHash: "feature"},
	}, trainOutcome.Ledger, artifactHashes)
	if err != nil {
		t.Fatal(err)
	}
	if err := reverselearn.WritePolicyManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	return dir
}
