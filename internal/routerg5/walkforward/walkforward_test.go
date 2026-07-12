package walkforward_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg5/reverselearn"
	"lingxi/internal/routerg5/walkforward"
	g5protocol "lingxi/internal/routerg5/protocol"
	"lingxi/internal/routerg4/profitmap"
)

func TestValidationFailSkipsWF(t *testing.T) {
	dir := t.TempDir()
	policyDir := writePolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	failReport, err := portfoliosim.BuildReport(portfoliosim.RunConfig{
		SchemaVersion: portfoliosim.SchemaVersionConfig,
		ProfileID: "okx_1h", ProtocolHash: g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash,
		PolicyHash: bundle.Policy.PolicyHash, FeatureHash: bundle.Policy.FeatureHash,
		ExperimentHash: bundle.Policy.ExperimentHash,
	}, nil, portfoliosim.StatusValidationFail, portfoliosim.ReasonNoEdge, portfoliosim.GateChecks{})
	if err != nil {
		t.Fatal(err)
	}
	valDir := filepath.Join(dir, "validation")
	if err := os.MkdirAll(valDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := portfoliosim.WriteJSONAtomic(filepath.Join(valDir, "g5_validation_report.json"), failReport); err != nil {
		t.Fatal(err)
	}
	cfg := walkforward.DefaultRunConfig("okx_1h", t.TempDir(), policyDir, t.TempDir(), valDir, filepath.Join(dir, "out"), bundle, "")
	runner := walkforward.Runner{Config: cfg, Catalog: testCatalog()}
	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.WFReport.Status != walkforward.StatusSkippedValidationFail {
		t.Fatalf("status=%s", result.WFReport.Status)
	}
}

func TestBuildFiveFolds(t *testing.T) {
	var ts []string
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		ts = append(ts, base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339Nano))
	}
	folds, err := walkforward.BuildFolds(ts, g5protocol.WFFoldCount, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(folds) != g5protocol.WFFoldCount {
		t.Fatalf("folds=%d", len(folds))
	}
}

func TestBuildFiveFoldsAppliesPurgeEmbargo(t *testing.T) {
	var ts []string
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		ts = append(ts, base.Add(time.Duration(i)*time.Hour).Format(time.RFC3339Nano))
	}
	embargo := 3 * time.Hour
	folds, err := walkforward.BuildFolds(ts, g5protocol.WFFoldCount, embargo)
	if err != nil {
		t.Fatal(err)
	}
	if len(folds) < 2 {
		t.Fatal("need multiple folds")
	}
	prevEnd := folds[0].EndTime
	for i := 1; i < len(folds); i++ {
		start, err := time.Parse(time.RFC3339Nano, folds[i].Timestamps[0])
		if err != nil {
			t.Fatal(err)
		}
		if !start.After(prevEnd.Add(embargo)) {
			t.Fatalf("fold %d must start after embargo gap: start=%s prevEnd=%s", i, start, prevEnd)
		}
		prevEnd = folds[i].EndTime
	}
	total := 0
	for _, fold := range folds {
		total += len(fold.Timestamps)
	}
	if total >= len(ts) {
		t.Fatalf("purge/embargo must drop timestamps: kept=%d total=%d", total, len(ts))
	}
}

func TestBadFoldFailsGate(t *testing.T) {
	folds := []walkforward.FoldResult{
		{FoldIndex: 0, BaseNetReturnFraction: 0.1, NetReturnPositive: true},
		{FoldIndex: 1, BaseNetReturnFraction: -0.1, NetReturnPositive: false},
	}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, portfoliosim.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonFoldNetReturn {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestLOFOFail(t *testing.T) {
	folds := []walkforward.FoldResult{
		{FoldIndex: 0, NetReturnPositive: true, BaseNetReturnFraction: 0.1, Scenarios: []portfoliosim.ScenarioResult{{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1, FinalEquityUSDT: 35}}},
		{FoldIndex: 1, NetReturnPositive: true, BaseNetReturnFraction: 0.1, Scenarios: []portfoliosim.ScenarioResult{{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1, FinalEquityUSDT: 35}}},
	}
	lofo := []walkforward.LOFOResult{{ExcludedFoldIndex: 0, NetReturnFraction: -0.1, NetReturnPositive: false}}
	status, _, reason := walkforward.EvaluateWFGates(folds, lofo, portfoliosim.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonLOFO {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestDelayFail(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []portfoliosim.ScenarioResult{
			{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1, FinalEquityUSDT: 35},
			{InitialEquityUSDT: 30, DelayBars: 1, CostStressMultiplier: 1, FinalEquityUSDT: 20},
		},
	}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, portfoliosim.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonDelay {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestCostStressFail(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []portfoliosim.ScenarioResult{
			{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1, FinalEquityUSDT: 35},
			{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1.5, FinalEquityUSDT: 20},
		},
	}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, portfoliosim.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonCostStress {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestClusterRobustnessFail(t *testing.T) {
	folds := []walkforward.FoldResult{{FoldIndex: 0, NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
	daily := portfoliosim.DailyEquityReplay{
		DailyPnL:    map[string]float64{"d1": 10, "d2": 10},
		ClusterPnL:  map[string]float64{"a": 100, "b": -50},
		ShockDayPnL: map[string]float64{"s": 1},
	}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonCluster {
		t.Fatalf("status=%s reason=%s", status, reason)
	}
}

func TestNoAutoMechanism(t *testing.T) {
	if got := walkforward.DecideMechanismFromFeatures([]string{"btc_return", "liquidity_score"}); got != walkforward.MechanismUnexplainedPolicyPass {
		t.Fatalf("got=%s", got)
	}
	wf := walkforward.WFReport{Status: walkforward.StatusWFPass, PolicyHash: "p", ReportHash: "r"}
	mech := walkforward.BuildMechanismReport(wf)
	if mech.TradeAllowed || mech.ExamEligible || mech.Certified || mech.AutoFromFeatures {
		t.Fatal("mechanism must forbid trade/exam/certified/auto")
	}
	if mech.MechanismStatus != walkforward.MechanismUnexplainedPolicyPass {
		t.Fatalf("status=%s", mech.MechanismStatus)
	}
}

func TestPartitionLeakRejected(t *testing.T) {
	rows := []features.DecisionContext{{Partition: outcome.PartitionValidation, Symbol: "A", DecisionTime: time.Now().UTC()}}
	if err := walkforward.RejectNonWalkForward(rows); err == nil {
		t.Fatal("validation partition must leak-detect")
	}
}

func TestCheckpointTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cp := walkforward.Checkpoint{
		Generation: g5protocol.Generation, ProtocolHash: g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash, Done: false,
	}
	if err := walkforward.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := walkforward.LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail")
	}
}

func TestHolmCoversLedgerFamily(t *testing.T) {
	ledger := reverselearn.ExperimentLedger{
		Attempts: []reverselearn.ExperimentAttempt{
			{AttemptOrdinal: 1, PolicyHash: "weak", RewardMeanBPS: -1},
			{AttemptOrdinal: 2, PolicyHash: "selected", RewardMeanBPS: 1},
		},
	}
	daily := portfoliosim.DailyEquityReplay{DailyPnL: map[string]float64{"d1": 1, "d2": 1, "d3": -1}}
	results := walkforward.BuildAttemptSignResults(ledger, "selected", daily)
	if len(results) != 2 {
		t.Fatalf("attempts=%d", len(results))
	}
}

func TestEndToEndWFRunner(t *testing.T) {
	policyDir := writePolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	featureDir := t.TempDir()
	historyRoot := t.TempDir()
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	opens := make([]float64, 200)
	for i := range opens {
		opens[i] = 100 + float64(i)*0.5
	}
	writeTestBars(t, historyRoot, "BTC-USDT-SWAP", opens)
	var rows []features.DecisionContext
	for i := 0; i < 180; i++ {
		decision := base.Add(time.Duration(i) * time.Hour).Add(time.Hour)
		rows = append(rows, features.DecisionContext{
			ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP",
			DecisionTime: decision, Partition: outcome.PartitionWalkFwd,
			Features: synthFeatures(i),
		})
	}
	writeFeatureShard(t, featureDir, rows)
	valDir := t.TempDir()
	passReport, err := portfoliosim.BuildReport(portfoliosim.RunConfig{
		SchemaVersion: portfoliosim.SchemaVersionConfig, ProfileID: "okx_1h",
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
		PolicyHash: bundle.Policy.PolicyHash, FeatureHash: bundle.Policy.FeatureHash,
		ExperimentHash: bundle.Policy.ExperimentHash,
	}, nil, portfoliosim.StatusValidationPass, "", portfoliosim.GateChecks{
		BaseNetReturnPositive: true, AllDelayNetReturnPositive: true, AllEquityTiersPositive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := portfoliosim.WriteJSONAtomic(filepath.Join(valDir, "g5_validation_report.json"), passReport); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	cfg := walkforward.DefaultRunConfig("okx_1h", historyRoot, policyDir, featureDir, valDir, outDir, bundle, passReport.ReportHash)
	runner := walkforward.Runner{Config: cfg, Catalog: testCatalog()}
	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.WFReport.ReportHash == "" || result.Receipt.ReceiptHash == "" {
		t.Fatal("hashes required")
	}
	for _, name := range []string{"g5_wf_report.json", "g5_policy_mechanism_report.json", "g5_research_summary.json", "wf_receipt.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if result.MechanismReport.ExamActions != 0 {
		t.Fatal("exam_actions must be 0")
	}
}

func writePolicyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	examples := synthTrainingExamples()
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
		Generation: g5protocol.Generation, ProtocolHash: g5protocol.RouterProtocolHash,
		FeatureHash: pol.FeatureHash, PolicyHash: pol.PolicyHash, ExperimentHash: pol.ExperimentHash,
		ExamActions: 0,
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "policy_manifest.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func synthTrainingExamples() []reverselearn.TrainingExample {
	return reverselearn.SyntheticDiverseExamples()
}

func synthFeatures(i int) map[string]float64 {
	return map[string]float64{
		"btc_return": 0.5, "symbol_return": float64(i%5), "btc_vol": 0.1, "btc_trend": 0.2,
		"btc_drawdown": 0.05, "btc_funding_state": 0, "sky_confidence": 0.9,
		"mask_btc_funding_missing": 0, "symbol_rv": 0.1, "symbol_beta": 1.0,
		"btc_relative_return": 0.01, "liquidity_percentile": 0.5, "quote_volume_rank": 0.5,
		"return_rank": 0.9, "vol_rank": 0.5, "liquidity_rank": 0.5, "beta_rank": 0.5,
		"btc_relative_strength_rank": 0.9, "liquidity_score": 0.8, "vol_cost_penalty": 0.1,
		"capacity_headroom": 0.9, "cost_estimate": 15.0,
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

func writeFeatureShard(t *testing.T, dir string, rows []features.DecisionContext) {
	t.Helper()
	schema, err := features.FrozenFeatureSchema()
	if err != nil {
		t.Fatal(err)
	}
	shard, err := features.WriteContextShard(dir, features.ShardManifest{
		ProfileID: "okx_1h", ShardIndex: 0, FileName: "shard_000.jsonl",
	}, rows)
	if err != nil {
		t.Fatal(err)
	}
	manifest := features.ContextManifest{
		Generation: g5protocol.Generation, ProtocolHash: g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash, ProfileID: "okx_1h",
		FeatureHash: schema.FeatureHash, Shards: []features.ShardManifest{shard},
		ExamActions: 0,
	}
	if err := features.WriteContextManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
}

func writeTestBars(t *testing.T, historyRoot, symbol string, opens []float64) {
	t.Helper()
	dir := filepath.Join(historyRoot, "okx_1h")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	var lines []string
	for i, open := range opens {
		ot := base.Add(time.Duration(i) * time.Hour)
		closePx := open * 1.01
		lines = append(lines, profitmap.FormatCandleLine(ot, open, open*1.02, open*0.99, closePx, 1e6))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, symbol+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testCatalog() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{
		DataHash: g5protocol.HistoryDataHash,
		Families: []historydata.FamilyCatalog{{
			Name: "okx_1h",
			Files: []historydata.FileStats{{Family: "okx_1h", Path: "okx_1h/BTC-USDT-SWAP.jsonl"}},
		}},
	}
}

func TestDailyReplayBalancesScenarioFinalEquity(t *testing.T) {
	policyDir := writePolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := features.ProfileFor("okx_1h")
	if err != nil {
		t.Fatal(err)
	}
	historyRoot := t.TempDir()
	base := time.Date(2024, 5, 2, 8, 0, 0, 0, time.UTC)
	opens := make([]float64, 20)
	for i := range opens {
		opens[i] = 100 + float64(i)
	}
	writeTestBars(t, historyRoot, "BTC-USDT-SWAP", opens)
	featureRows := map[string][]features.DecisionContext{}
	var timestamps []string
	for i := 0; i < 10; i++ {
		decision := base.Add(time.Duration(i) * time.Hour).Add(time.Hour)
		ts := decision.Format(time.RFC3339Nano)
		timestamps = append(timestamps, ts)
		featureRows[ts] = []features.DecisionContext{{
			ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP",
			DecisionTime: decision, Partition: outcome.PartitionWalkFwd,
			Features: synthFeatures(i),
		}}
	}
	barsBundle, err := portfoliosim.LoadCanonicalBars(testCatalog(), historyRoot, "okx_1h", featureRows, 0)
	if err != nil {
		t.Fatal(err)
	}
	horizons := bundle.Policy.HorizonLabels
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	engine := portfoliosim.ScenarioEngine{
		Policy: bundle.Policy, Bars: barsBundle, Profile: profile,
		Horizons: horizons, Partition: outcome.PartitionWalkFwd,
	}
	equity := float64(g5protocol.ValidationInitialEquities[0])
	baseScenario := portfoliosim.BaseScenario(engine.RunScenarios(featureRows, timestamps))
	if baseScenario == nil {
		t.Fatal("base scenario required")
	}
	daily := engine.RunDailyTracking(featureRows, timestamps, equity)
	if !daily.TailAccrued {
		t.Fatal("tail accrual required")
	}
	if !daily.BalancesWithFinalEquity() {
		t.Fatalf("daily replay must balance final equity: sum=%v delta=%v", daily.DailyPnLSum(), daily.FinalEquityUSDT-equity)
	}
	if daily.FinalEquityUSDT != baseScenario.FinalEquityUSDT {
		t.Fatalf("daily final=%v scenario final=%v", daily.FinalEquityUSDT, baseScenario.FinalEquityUSDT)
	}
}

func TestSkippedPolicyUnavailableSkipsWalkForward(t *testing.T) {
	dir := t.TempDir()
	policyDir := writeNoFrozenPolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	valReport, err := portfoliosim.BuildReport(portfoliosim.RunConfig{
		SchemaVersion: portfoliosim.SchemaVersionConfig,
		ProfileID:       "okx_1h",
		ProtocolHash:    g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash,
		FeatureHash:     bundle.PolicyManifest.FeatureHash,
		ExperimentHash:  bundle.PolicyManifest.ExperimentHash,
	}, nil, portfoliosim.StatusSkippedPolicyUnavailable, portfoliosim.ReasonNoGo, portfoliosim.GateChecks{})
	if err != nil {
		t.Fatal(err)
	}
	valDir := filepath.Join(dir, "validation")
	if err := os.MkdirAll(valDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := portfoliosim.WriteJSONAtomic(filepath.Join(valDir, "g5_validation_report.json"), valReport); err != nil {
		t.Fatal(err)
	}
	cfg := walkforward.DefaultRunConfig("okx_1h", t.TempDir(), policyDir, t.TempDir(), valDir, filepath.Join(dir, "out"), bundle, "")
	runner := walkforward.Runner{Config: cfg, Catalog: testCatalog()}
	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.WFReport.Status != walkforward.StatusSkippedValidationFail {
		t.Fatalf("wf status=%s", result.WFReport.Status)
	}
	if result.WFReport.FailReason != walkforward.ReasonPolicyUnavailable {
		t.Fatalf("reason=%s", result.WFReport.FailReason)
	}
	if len(result.WFReport.Folds) != 0 {
		t.Fatal("skipped WF must not replay folds")
	}
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
