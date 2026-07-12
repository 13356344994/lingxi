package portfoliosim_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg5/actionspace"
	"lingxi/internal/routerg5/features"
	"lingxi/internal/routerg5/outcome"
	"lingxi/internal/routerg5/policy"
	"lingxi/internal/routerg5/portfoliosim"
	"lingxi/internal/routerg5/reverselearn"
	"lingxi/internal/routerg5/walkforward"
	g5protocol "lingxi/internal/routerg5/protocol"
	"lingxi/internal/routerg4/profitmap"
)

func TestTopKClusterCap(t *testing.T) {
	proposals := []policy.Proposal{
		{ID: "a", Symbol: "A", Cluster: "btc_beta_high", Score: 3, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.4}},
		{ID: "b", Symbol: "B", Cluster: "btc_beta_high", Score: 2, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.4}},
		{ID: "c", Symbol: "C", Cluster: "btc_beta_low", Score: 1.5, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenShort, SizeFraction: 0.4}},
		{ID: "d", Symbol: "D", Cluster: "btc_beta_mid", Score: 1, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.4}},
		{ID: "e", Symbol: "E", Cluster: "btc_beta_mid", Score: 0.5, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.4}},
		{ID: "f", Symbol: "F", Cluster: "sector_ETH", Score: 0.4, NotionalUSDT: 400, CapacityHeadroom: 1, Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.4}},
	}
	selected := portfoliosim.SelectTopK(proposals, 1000)
	if len(selected) > g5protocol.TopK {
		t.Fatalf("selected=%d want <= %d", len(selected), g5protocol.TopK)
	}
	clusterUsed := map[string]float64{}
	for _, p := range selected {
		clusterUsed[p.Cluster] += p.NotionalUSDT
	}
	for cluster, used := range clusterUsed {
		if used > 1000*float64(g5protocol.MaxAccountLeverage)*0.4+1e-6 {
			t.Fatalf("cluster %s exceeded budget: %v", cluster, used)
		}
	}
}

func TestOverlapNoDoubleCount(t *testing.T) {
	account := portfoliosim.NewAccount(1000)
	long := actionspace.PositionState{Side: actionspace.SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: 1000}
	action := actionspace.Action{Verb: actionspace.VerbIncrease, SizeFraction: 0.25, TargetHorizon: "1h", DelayBars: 0}
	if err := account.ApplyExecution("BTC-USDT-SWAP", long, action, 101, 1, 0); err != nil {
		t.Fatal(err)
	}
	hold := actionspace.Action{Verb: actionspace.VerbHold, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", account.Positions["BTC-USDT-SWAP"], hold, 102, 0, 0); err != nil {
		t.Fatal(err)
	}
	if account.TurnoverUSDT <= 0 {
		t.Fatal("increase must incur turnover")
	}
	turnoverAfterHold := account.TurnoverUSDT
	account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 105})
	if account.EquityUSDT <= 1000 {
		t.Fatal("mark-to-market must move equity without double-counting hold turnover")
	}
	if account.TurnoverUSDT != turnoverAfterHold {
		t.Fatal("hold must not add turnover")
	}
}

func TestDynamicEarlyExitReverse(t *testing.T) {
	account := portfoliosim.NewAccount(1000)
	open := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h", DelayBars: 0}
	if err := account.ApplyExecution("ETH-USDT-SWAP", actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000}, open, 100, 2, 0); err != nil {
		t.Fatal(err)
	}
	trim := actionspace.Action{Verb: actionspace.VerbTrim, SizeFraction: 0.25, TargetHorizon: "1h", DelayBars: 0}
	pos := account.Positions["ETH-USDT-SWAP"]
	if err := account.ApplyExecution("ETH-USDT-SWAP", pos, trim, 102, 1, 0); err != nil {
		t.Fatal(err)
	}
	reverse := actionspace.Action{Verb: actionspace.VerbReverse, SizeFraction: 0.25, TargetHorizon: "1h", DelayBars: 0}
	pos = account.Positions["ETH-USDT-SWAP"]
	if err := account.ApplyExecution("ETH-USDT-SWAP", pos, reverse, 103, 1, 0); err != nil {
		t.Fatal(err)
	}
	if account.Positions["ETH-USDT-SWAP"].Side != actionspace.SideShort {
		t.Fatal("reverse must flip side")
	}
}

func TestDelayPseudoFixFailsGate(t *testing.T) {
	var scenarios []portfoliosim.ScenarioResult
	for _, equity := range g5protocol.ValidationInitialEquities {
		scenarios = append(scenarios, portfoliosim.ScenarioResult{
			InitialEquityUSDT: equity, DelayBars: 0, CostStressMultiplier: 1.0,
			FinalEquityUSDT: equity * 1.1, ActiveDays: 25, MaxDrawdownFraction: 0.1,
		})
		scenarios = append(scenarios, portfoliosim.ScenarioResult{
			InitialEquityUSDT: equity, DelayBars: 1, CostStressMultiplier: 1.0,
			FinalEquityUSDT: equity * 0.8, ActiveDays: 25, MaxDrawdownFraction: 0.1,
		})
		for _, delay := range []int{2, 3} {
			scenarios = append(scenarios, portfoliosim.ScenarioResult{
				InitialEquityUSDT: equity, DelayBars: delay, CostStressMultiplier: 1.0,
				FinalEquityUSDT: equity * 1.1, ActiveDays: 25, MaxDrawdownFraction: 0.1,
			})
		}
	}
	status, checks, reason := portfoliosim.EvaluateGates(scenarios)
	if status != portfoliosim.StatusValidationFail || reason != portfoliosim.ReasonDelay {
		t.Fatalf("delay failure want FAIL/DELAY got %s/%s checks=%+v", status, reason, checks)
	}
	if checks.AllDelayNetReturnPositive {
		t.Fatal("delay gate must fail")
	}
}

func TestCapacityEquity30_100_1000(t *testing.T) {
	for _, equity := range g5protocol.ValidationInitialEquities {
		account := portfoliosim.NewAccount(equity)
		action := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"}
		state := actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: equity}
		if err := account.ApplyExecution("BTC-USDT-SWAP", state, action, 100, equity*0.001, 0); err != nil {
			t.Fatalf("equity %v: %v", equity, err)
		}
		maxNotional := equity * float64(g5protocol.MaxAccountLeverage)
		if actionspace.NotionalUSDT(account.Positions["BTC-USDT-SWAP"]) > maxNotional+1e-6 {
			t.Fatalf("equity %v exceeded leverage cap", equity)
		}
	}
}

func TestCheckpointTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cp := portfoliosim.Checkpoint{
		Generation: g5protocol.Generation, ProtocolHash: g5protocol.RouterProtocolHash,
		HistoryDataHash: g5protocol.HistoryDataHash, Done: false,
	}
	if err := portfoliosim.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := portfoliosim.LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail")
	}
}

func TestPITValidationPartitionOnly(t *testing.T) {
	rows := []features.DecisionContext{
		{Partition: outcome.PartitionValidation, Symbol: "A", DecisionTime: time.Now().UTC(), Features: map[string]float64{"symbol_return": 1}},
		{Partition: outcome.PartitionDiscovery, Symbol: "B", DecisionTime: time.Now().UTC(), Features: map[string]float64{"symbol_return": 1}},
	}
	if err := portfoliosim.RejectDiscoveryPartition(rows); err == nil {
		t.Fatal("discovery partition must be rejected")
	}
	if err := portfoliosim.RejectWalkForward([]features.DecisionContext{{Partition: outcome.PartitionWalkFwd}}); err == nil {
		t.Fatal("walk_forward partition must be rejected")
	}
}

func TestValidationGatesPass(t *testing.T) {
	var scenarios []portfoliosim.ScenarioResult
	for _, equity := range g5protocol.ValidationInitialEquities {
		for _, delay := range g5protocol.ValidationDelayBars {
			scenarios = append(scenarios, portfoliosim.ScenarioResult{
				InitialEquityUSDT: equity, DelayBars: delay, CostStressMultiplier: 1.0,
				FinalEquityUSDT: equity * 1.1, ActiveDays: 25, MaxDrawdownFraction: 0.1,
				MaxSymbolContribution: 0.2,
			})
		}
	}
	status, _, _ := portfoliosim.EvaluateGates(scenarios)
	if status != portfoliosim.StatusValidationPass {
		t.Fatalf("want PASS got %s", status)
	}
}

func TestValidationReportHashStable(t *testing.T) {
	cfg := portfoliosim.RunConfig{
		SchemaVersion: portfoliosim.SchemaVersionConfig,
		ProfileID: "okx_1h", PolicyHash: "p", FeatureHash: "f", ExperimentHash: "e",
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
	}
	report, err := portfoliosim.BuildReport(cfg, nil, portfoliosim.StatusInconclusive, portfoliosim.ReasonCoverage, portfoliosim.GateChecks{})
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportHash == "" {
		t.Fatal("report hash required")
	}
}

func TestContinuousSizeMonotonic(t *testing.T) {
	low := policy.ContinuousSize(0.1, 0.5, map[string]float64{"capacity_headroom": 1, "sky_confidence": 0.5})
	high := policy.ContinuousSize(2.0, 0.9, map[string]float64{"capacity_headroom": 1, "sky_confidence": 0.9})
	if high <= low {
		t.Fatalf("higher edge should map to larger size: low=%v high=%v", low, high)
	}
	if math.IsNaN(high) || math.IsInf(high, 0) {
		t.Fatal("size must be finite")
	}
}

func TestMaxDrawdownGate(t *testing.T) {
	if !portfoliosim.MaxDrawdownOK(0.29) {
		t.Fatal("29% dd should pass")
	}
	if portfoliosim.MaxDrawdownOK(0.31) {
		t.Fatal("31% dd should fail")
	}
}

func writeFeatureShard(t *testing.T, dir string, rows []features.DecisionContext) features.ContextManifest {
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
	raw, _ := os.ReadFile(filepath.Join(dir, "feature_context_manifest.json"))
	var got features.ContextManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
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

func mustFeatureHash(t *testing.T) string {
	t.Helper()
	h, err := features.FeatureHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCloseEquityContinuousAtMark(t *testing.T) {
	account := portfoliosim.NewAccount(actionspace.BaseEquityUSDT)
	long := actionspace.PositionState{Side: actionspace.SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: actionspace.BaseEquityUSDT, FixedNotionalUSDT: actionspace.DeployNotionalUSDT(actionspace.BaseEquityUSDT, 0.5)}
	account.Positions["BTC-USDT-SWAP"] = long
	account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 105})
	marked := account.EquityUSDT
	closeAction := actionspace.Action{Verb: actionspace.VerbClose, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", long, closeAction, 105, 1, 0); err != nil {
		t.Fatal(err)
	}
	if math.Abs(marked-account.EquityUSDT) > 2.0 {
		t.Fatalf("close at mark should keep equity continuous: marked=%v closed=%v", marked, account.EquityUSDT)
	}
}

func TestSolvencyGateFailsNotInconclusive(t *testing.T) {
	scenarios := []portfoliosim.ScenarioResult{
		{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1.0, FinalEquityUSDT: 35, ActiveDays: 25, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 30, DelayBars: 1, CostStressMultiplier: 1.0, FinalEquityUSDT: 35, ActiveDays: 25, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 30, DelayBars: 2, CostStressMultiplier: 1.0, FinalEquityUSDT: 35, ActiveDays: 25, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 30, DelayBars: 3, CostStressMultiplier: 1.0, FinalEquityUSDT: 35, ActiveDays: 25, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 100, DelayBars: 0, CostStressMultiplier: 1.0, FinalEquityUSDT: 0, ActiveDays: 25, MaxDrawdownFraction: 0.9, SolvencyBreach: true},
	}
	status, _, reason := portfoliosim.EvaluateGates(scenarios)
	if status != portfoliosim.StatusValidationFail || reason != portfoliosim.ReasonSolvency {
		t.Fatalf("solvency want FAIL/SOLVENCY got %s/%s", status, reason)
	}
}

func TestFundingMissingNonblockingReported(t *testing.T) {
	bundle := portfoliosim.FundingBundle{BySymbol: map[string][]profitmap.FundingPoint{}, Missing: map[string]bool{"BTC-USDT-SWAP": true}}
	account := portfoliosim.NewAccount(1000)
	long := actionspace.PositionState{Side: actionspace.SideLong, SizeFraction: 0.5, BasisPrice: 100, EquityUSDT: 1000, FixedNotionalUSDT: actionspace.DeployNotionalUSDT(1000, 0.5)}
	account.Positions["BTC-USDT-SWAP"] = long
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(8 * time.Hour)
	account.ApplyHoldFunding(bundle, []string{"BTC-USDT-SWAP"}, start, end, 1.0)
	if !account.FundingMissing {
		t.Fatal("missing funding must be reported")
	}
	if account.FundingPaidUSDT != 0 {
		t.Fatalf("missing funding must not deduct: %v", account.FundingPaidUSDT)
	}
}

func TestEndToEndValidationRunner(t *testing.T) {
	policyDir := writePolicyDir(t)
	featureDir := t.TempDir()
	historyRoot := t.TempDir()
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	opens := make([]float64, 40)
	for i := range opens {
		opens[i] = 100 + float64(i)
	}
	writeTestBars(t, historyRoot, "BTC-USDT-SWAP", opens)
	catalog := testCatalog()
	var rows []features.DecisionContext
	for i := 0; i < 30; i++ {
		decision := base.Add(time.Duration(i) * time.Hour).Add(time.Hour)
		rows = append(rows, features.DecisionContext{
			ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP",
			DecisionTime: decision,
			Partition: outcome.PartitionValidation,
			Features: map[string]float64{
				"btc_return": 0.5, "symbol_return": 1.0, "btc_vol": 0.1, "btc_trend": 0.2,
				"btc_drawdown": 0.05, "btc_funding_state": 0, "sky_confidence": 0.9,
				"mask_btc_funding_missing": 0, "symbol_rv": 0.1, "symbol_beta": 1.0,
				"btc_relative_return": 0.01, "liquidity_percentile": 0.5, "quote_volume_rank": 0.5,
				"return_rank": 0.9, "vol_rank": 0.5, "liquidity_rank": 0.5, "beta_rank": 0.5,
				"btc_relative_strength_rank": 0.9, "liquidity_score": 0.8, "vol_cost_penalty": 0.1,
				"capacity_headroom": 0.9, "cost_estimate": 15.0,
			},
		})
	}
	writeFeatureShard(t, featureDir, rows)
	bundle, err := policy.LoadPolicyBundle(policyDir)
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	cfg := portfoliosim.DefaultRunConfig("okx_1h", historyRoot, policyDir, featureDir, outDir, bundle)
	runner := portfoliosim.Runner{Config: cfg, Catalog: catalog}
	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.ReportHash == "" || result.Receipt.ReceiptHash == "" {
		t.Fatal("hashes required")
	}
	if _, err := os.Stat(filepath.Join(outDir, "g5_validation_report.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMultiPositionEquityNoDoubleCount(t *testing.T) {
	account := portfoliosim.NewAccount(1000)
	openBTC := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.2, TargetHorizon: "1h"}
	openETH := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.2, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000}, openBTC, 100, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := account.ApplyExecution("ETH-USDT-SWAP", actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000}, openETH, 200, 1, 0); err != nil {
		t.Fatal(err)
	}
	account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 110, "ETH-USDT-SWAP": 220})
	if account.EquityUSDT >= 2000 {
		t.Fatalf("equity must not double-count full account per position: %v", account.EquityUSDT)
	}
	if account.EquityUSDT <= 1000 {
		t.Fatalf("equity must reflect both positions: %v", account.EquityUSDT)
	}
}

func TestDelayNotImmediateExecution(t *testing.T) {
	queue := &portfoliosim.PendingQueue{}
	queue.Enqueue(portfoliosim.PendingAction{ExecuteIndex: 2, Proposal: policy.Proposal{ID: "a"}})
	matured := queue.FlushMatured(1)
	if len(matured) != 0 {
		t.Fatal("delay action must not execute before execute index")
	}
	matured = queue.FlushMatured(2)
	if len(matured) != 1 {
		t.Fatal("matured action must execute at execute index")
	}
}

func TestDelayScenariosProduceDifferentReturns(t *testing.T) {
	opens := []float64{100, 110, 120, 130, 140}
	bundle := buildTestBundle(t, opens)
	runEntry := func(delay int) float64 {
		account := portfoliosim.NewAccount(1000)
		action := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h", DelayBars: delay}
		state := actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000}
		price, ok := portfoliosim.EntryOpenPrice(bundle, "BTC-USDT-SWAP", portfoliosim.ExecuteIndexForDelay(0, delay))
		if !ok {
			t.Fatalf("entry price missing for delay %d", delay)
		}
		costBPS := 5.0
		turnover := 1000 * 0.5 * float64(g5protocol.MaxAccountLeverage)
		cost := turnover * (g5protocol.RoundtripTakerFeeBPS + g5protocol.BaseSpreadSlippageFloorBPS + costBPS) / 10000
		if err := account.ApplyExecution("BTC-USDT-SWAP", state, action, price, cost, 0); err != nil {
			t.Fatal(err)
		}
		account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 140})
		return account.EquityUSDT
	}
	delay0 := runEntry(0)
	delay1 := runEntry(1)
	if math.Abs(delay0-delay1) < 1e-6 {
		t.Fatalf("delay scenarios must differ: delay0=%v delay1=%v", delay0, delay1)
	}
}

func TestPendingStaleActionRejected(t *testing.T) {
	account := portfoliosim.NewAccount(1000)
	open := actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000}, open, 100, 1, 0); err != nil {
		t.Fatal(err)
	}
	stale := portfoliosim.PendingAction{
		Proposal: policy.Proposal{
			ID: "stale", Symbol: "BTC-USDT-SWAP", CapacityHeadroom: 1, CostEstimateBPS: 5,
			Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"},
		},
		ExecuteIndex: 1,
	}
	_ = buildTestBundle(t, []float64{100, 110, 120})
	queue := &portfoliosim.PendingQueue{}
	queue.Enqueue(stale)
	matured := queue.FlushMatured(1)
	if len(matured) != 1 {
		t.Fatal("expected matured stale action")
	}
	before := account.StaleRejects
	for _, pa := range matured {
		current := account.CurrentPositionState(pa.Proposal.Symbol)
		if err := actionspace.ValidateTransition(current, pa.Proposal.Action); err == nil {
			t.Fatal("open_long from existing long should be invalid")
		}
		account.StaleRejects++
	}
	if account.StaleRejects != before+1 {
		t.Fatal("stale pending must increment stale rejects")
	}
}

func TestExecutionCostStressAndCapacity(t *testing.T) {
	turnover := 2500.0
	baseCost := turnover * (g5protocol.RoundtripTakerFeeBPS + g5protocol.BaseSpreadSlippageFloorBPS + 5) / 10000
	stressCost := turnover * (g5protocol.RoundtripTakerFeeBPS + g5protocol.BaseSpreadSlippageFloorBPS + 5) * g5protocol.CostStressMultiplier / 10000
	if stressCost <= baseCost {
		t.Fatal("stress cost must exceed base cost")
	}
	prop, ok := policy.BuildProposal(policy.ScoredAction{
		Symbol: "BTC-USDT-SWAP",
		Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"},
		Features: map[string]float64{
			"symbol_beta": 1.0, "capacity_headroom": 0, "cost_estimate": 20,
			"liquidity_score": 0.8, "vol_cost_penalty": 0.1,
		},
		Score: 1, StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000},
	}, 1000)
	if !ok || prop.CapacityHeadroom != 0 {
		t.Fatal("proposal must carry zero capacity headroom for replay rejection")
	}
	selected := portfoliosim.SelectTopK([]policy.Proposal{prop}, 1000)
	if len(selected) != 0 {
		t.Fatal("zero capacity must not pass Top-K selection")
	}
}

func TestCanonicalBarsLoadError(t *testing.T) {
	rows := map[string][]features.DecisionContext{
		"t": {{Symbol: "MISSING-USDT-SWAP", DecisionTime: time.Now().UTC()}},
	}
	catalog := testCatalog()
	_, err := portfoliosim.LoadCanonicalBars(catalog, t.TempDir(), "okx_1h", rows, 0)
	if err == nil {
		t.Fatal("missing canonical bars must fail")
	}
}

func TestPendingEOFNotCounted(t *testing.T) {
	queue := &portfoliosim.PendingQueue{}
	queue.Enqueue(portfoliosim.PendingAction{ExecuteIndex: 99, Proposal: policy.Proposal{ID: "tail"}})
	if got := queue.DiscardAll(); got != 1 {
		t.Fatalf("discard=%d want 1", got)
	}
	if len(queue.Actions) != 0 {
		t.Fatal("queue must be empty after discard")
	}
}

func writeTestBars(t *testing.T, historyRoot, symbol string, opens []float64) {
	t.Helper()
	dir := filepath.Join(historyRoot, "okx_1h")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
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

func buildTestBundle(t *testing.T, opens []float64) portfoliosim.BarsBundle {
	t.Helper()
	historyRoot := t.TempDir()
	writeTestBars(t, historyRoot, "BTC-USDT-SWAP", opens)
	rows := map[string][]features.DecisionContext{
		"t": {{Symbol: "BTC-USDT-SWAP", DecisionTime: time.Now().UTC()}},
	}
	bundle, err := portfoliosim.LoadCanonicalBars(testCatalog(), historyRoot, "okx_1h", rows, 0)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestMarkToMarketLeverageBreach(t *testing.T) {
	account := portfoliosim.NewAccount(100)
	longNotional := actionspace.DeployNotionalUSDT(100, 1.0)
	account.Positions["BTC-USDT-SWAP"] = actionspace.PositionState{
		Side: actionspace.SideLong, SizeFraction: 1.0, BasisPrice: 100, EquityUSDT: 100,
		FixedNotionalUSDT: longNotional,
	}
	account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 50})
	if !account.LeverageBreach {
		t.Fatalf("effective leverage=%v must breach after drawdown", account.EffectiveLeverage())
	}
	increase := actionspace.Action{Verb: actionspace.VerbIncrease, SizeFraction: 0.1, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", account.Positions["BTC-USDT-SWAP"], increase, 50, 0, 0); err == nil {
		t.Fatal("increase must be rejected after leverage breach")
	}
}

func TestPendingExecutionRescalesToApprovedNotional(t *testing.T) {
	account := portfoliosim.NewAccount(1000)
	prop, ok := policy.BuildProposal(policy.ScoredAction{
		Symbol: "BTC-USDT-SWAP",
		Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"},
		Score:  1,
		StartState: actionspace.PositionState{Side: actionspace.SideFlat, EquityUSDT: 1000},
	}, 1000)
	if !ok {
		t.Fatal("proposal required")
	}
	account.EquityUSDT = 500
	prop.NotionalUSDT = 1000
	pa := portfoliosim.PendingAction{Proposal: prop, ScenarioDelay: 0}
	action, ok := portfoliosim.PreparePendingExecution(pa, account)
	if !ok {
		t.Fatal("expected clipped execution")
	}
	got := action.SizeFraction * 500 * float64(g5protocol.MaxAccountLeverage)
	if got > prop.NotionalUSDT+1e-6 {
		t.Fatalf("executed notional=%v exceeds approved=%v", got, prop.NotionalUSDT)
	}
	if account.PendingClips == 0 {
		t.Fatal("expected pending clip counter")
	}
}

func TestTrimPortfolioMatchesOracleTurnover(t *testing.T) {
	account := portfoliosim.NewAccount(actionspace.BaseEquityUSDT)
	long := actionspace.StandardOracleStates(100)[1]
	trim := actionspace.Action{Verb: actionspace.VerbTrim, SizeFraction: 0.25, TargetHorizon: "1h"}
	priorNotional := actionspace.NotionalUSDT(long)
	if err := account.ApplyExecution("BTC-USDT-SWAP", long, trim, 101, 1, 0); err != nil {
		t.Fatal(err)
	}
	post := account.Positions["BTC-USDT-SWAP"]
	wantNotional := priorNotional * 0.5
	if math.Abs(actionspace.NotionalUSDT(post)-wantNotional) > 1.0 {
		t.Fatalf("trim notional=%v want=%v", actionspace.NotionalUSDT(post), wantNotional)
	}
}

func TestClusterPnLDeltaNotLifetimeDoubleCount(t *testing.T) {
	prev := map[string]float64{}
	cluster := map[string]float64{}
	clusterOf := func(sym string) string {
		beta := 1.5
		if sym == "ETH-USDT-SWAP" {
			beta = 0.5
		}
		return policy.ClusterFor(map[string]float64{"symbol_beta": beta}, sym)
	}
	bars := []struct {
		sym string
		pnl float64
	}{
		{"BTC-USDT-SWAP", 10},
		{"ETH-USDT-SWAP", 5},
		{"BTC-USDT-SWAP", 25},
		{"ETH-USDT-SWAP", 15},
	}
	for _, bar := range bars {
		delta := bar.pnl - prev[bar.sym]
		cluster[clusterOf(bar.sym)] += delta
		prev[bar.sym] = bar.pnl
	}
	if len(cluster) < 2 {
		t.Fatalf("need multiple clusters for LOO gate, got %v", cluster)
	}
	total := 0.0
	for _, pnl := range cluster {
		total += pnl
	}
	if math.Abs(total-40) > 1e-9 {
		t.Fatalf("incremental cluster total=%v want 40", total)
	}
	lifetimeBug := map[string]float64{}
	for _, bar := range bars {
		lifetimeBug[clusterOf(bar.sym)] += bar.pnl
	}
	if math.Abs(lifetimeBug[clusterOf("BTC-USDT-SWAP")]-cluster[clusterOf("BTC-USDT-SWAP")]) < 1e-9 &&
		math.Abs(lifetimeBug[clusterOf("ETH-USDT-SWAP")]-cluster[clusterOf("ETH-USDT-SWAP")]) < 1e-9 {
		t.Fatal("lifetime double-count must not match incremental cluster totals")
	}
	daily := portfoliosim.DailyEquityReplay{
		DailyPnL:   map[string]float64{"d1": 20, "d2": 20},
		ClusterPnL: cluster,
	}
	folds := []walkforward.FoldResult{{NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
	status, checks, _ := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if !checks.Robustness.SymbolClusterLOO_OK || status != walkforward.StatusWFPass {
		t.Fatalf("incremental cluster LOO should pass: status=%s checks=%+v", status, checks.Robustness)
	}
	doubleCounted := portfoliosim.DailyEquityReplay{
		DailyPnL: map[string]float64{"d1": 20, "d2": 20},
		ClusterPnL: map[string]float64{
			clusterOf("BTC-USDT-SWAP"): 65,
			clusterOf("ETH-USDT-SWAP"): -20,
		},
	}
	_, checks2, reason := walkforward.EvaluateWFGates(folds, nil, doubleCounted, nil)
	if checks2.Robustness.SymbolClusterLOO_OK || reason != walkforward.ReasonCluster {
		t.Fatalf("lifetime double-count LOO must fail cluster gate: checks=%+v reason=%s", checks2.Robustness, reason)
	}
}

func TestPendingBreachBlocksRiskActionsAllowsTrim(t *testing.T) {
	account := portfoliosim.NewAccount(100)
	longNotional := actionspace.DeployNotionalUSDT(100, 1.0)
	account.Positions["BTC-USDT-SWAP"] = actionspace.PositionState{
		Side: actionspace.SideLong, SizeFraction: 1.0, BasisPrice: 100, EquityUSDT: 100,
		FixedNotionalUSDT: longNotional,
	}
	account.MarkToMarket(map[string]float64{"BTC-USDT-SWAP": 50})
	if !account.LeverageBreach {
		t.Fatal("leverage breach required")
	}
	openPA := portfoliosim.PendingAction{Proposal: policy.Proposal{
		Symbol: "ETH-USDT-SWAP", NotionalUSDT: 100, CapacityHeadroom: 1,
		Action: actionspace.Action{Verb: actionspace.VerbOpenLong, SizeFraction: 0.5, TargetHorizon: "1h"},
	}}
	if _, ok := portfoliosim.PreparePendingExecution(openPA, account); ok {
		t.Fatal("open must fail after breach")
	}
	reversePA := portfoliosim.PendingAction{Proposal: policy.Proposal{
		Symbol: "BTC-USDT-SWAP", NotionalUSDT: 100, CapacityHeadroom: 1,
		Action: actionspace.Action{Verb: actionspace.VerbReverse, SizeFraction: 0.25, TargetHorizon: "1h"},
	}}
	if _, ok := portfoliosim.PreparePendingExecution(reversePA, account); ok {
		t.Fatal("reverse must fail after breach")
	}
	if account.PendingRejects < 2 {
		t.Fatalf("pending rejects=%d want >=2", account.PendingRejects)
	}
	trim := actionspace.Action{Verb: actionspace.VerbTrim, SizeFraction: 0.25, TargetHorizon: "1h"}
	if err := account.ApplyExecution("BTC-USDT-SWAP", account.Positions["BTC-USDT-SWAP"], trim, 50, 0, 0); err != nil {
		t.Fatalf("trim must remain allowed after breach: %v", err)
	}
}

func TestMinActiveDaysInconclusiveSummaryNotPass(t *testing.T) {
	scenarios := []portfoliosim.ScenarioResult{
		{InitialEquityUSDT: 30, DelayBars: 0, CostStressMultiplier: 1.0, FinalEquityUSDT: 35, ActiveDays: 10, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 100, DelayBars: 0, CostStressMultiplier: 1.0, FinalEquityUSDT: 110, ActiveDays: 10, MaxDrawdownFraction: 0.1},
		{InitialEquityUSDT: 1000, DelayBars: 0, CostStressMultiplier: 1.0, FinalEquityUSDT: 1100, ActiveDays: 10, MaxDrawdownFraction: 0.1},
	}
	for _, delay := range []int{1, 2, 3} {
		for _, equity := range g5protocol.ValidationInitialEquities {
			scenarios = append(scenarios, portfoliosim.ScenarioResult{
				InitialEquityUSDT: equity, DelayBars: delay, CostStressMultiplier: 1.0,
				FinalEquityUSDT: equity * 1.1, ActiveDays: 10, MaxDrawdownFraction: 0.1,
			})
		}
	}
	status, checks, reason := portfoliosim.EvaluateGates(scenarios)
	if status != portfoliosim.StatusInconclusive || reason != portfoliosim.ReasonCoverage {
		t.Fatalf("want INCONCLUSIVE/COVERAGE got %s/%s", status, reason)
	}
	if checks.SummaryNote == "" || !strings.Contains(checks.SummaryNote, "not VALIDATION_PASS") {
		t.Fatalf("summary must state non-pass: %q", checks.SummaryNote)
	}
}

func TestEquityTierMustPassSeparately(t *testing.T) {
	var scenarios []portfoliosim.ScenarioResult
	for _, equity := range g5protocol.ValidationInitialEquities {
		for _, delay := range g5protocol.ValidationDelayBars {
			scenarios = append(scenarios, portfoliosim.ScenarioResult{
				InitialEquityUSDT: equity, DelayBars: delay, CostStressMultiplier: 1.0,
				FinalEquityUSDT: equity * 1.1, ActiveDays: 25, MaxDrawdownFraction: 0.1,
				MaxSymbolContribution: 0.2,
			})
		}
		final := equity * 1.1
		if equity == 1000 {
			final = equity * 0.9
		}
		scenarios = append(scenarios, portfoliosim.ScenarioResult{
			InitialEquityUSDT: equity, DelayBars: 0, CostStressMultiplier: g5protocol.CostStressMultiplier,
			FinalEquityUSDT: final, ActiveDays: 25, MaxDrawdownFraction: 0.1,
			MaxSymbolContribution: 0.2,
		})
	}
	status, checks, reason := portfoliosim.EvaluateGates(scenarios)
	if status != portfoliosim.StatusValidationFail || reason != portfoliosim.ReasonCost {
		t.Fatalf("1000 tier cost-stress fail must block pass: %s/%s", status, reason)
	}
	if checks.AllEquityTiersPositive {
		t.Fatal("all_equity_tiers_positive must be false")
	}
}

func TestRunDailyTrackingNoFinalDecisionSkipsTail(t *testing.T) {
	engine := dailyTrackingEngine(t)
	equity := float64(g5protocol.ValidationInitialEquities[0])
	empty := engine.RunDailyTracking(map[string][]features.DecisionContext{}, nil, equity)
	if empty.TailAccrued {
		t.Fatal("empty timestamps must not accrue EOF tail")
	}
	if !empty.BalancesWithFinalEquity() {
		t.Fatalf("empty replay must balance: daily=%v final=%v", empty.DailyPnLSum(), empty.FinalEquityUSDT-empty.InitialEquityUSDT)
	}
	badTS := engine.RunDailyTracking(map[string][]features.DecisionContext{}, []string{"not-a-timestamp"}, equity)
	if badTS.TailAccrued {
		t.Fatal("unparsed timestamps must skip EOF tail explicitly")
	}
}

func TestRunDailyTrackingEOFTailBalancesFundingAndPnL(t *testing.T) {
	engine, featureRows, timestamps := dailyTrackingFixture(t)
	equity := float64(g5protocol.ValidationInitialEquities[0])
	scenarios := engine.RunScenarios(featureRows, timestamps)
	base := portfoliosim.BaseScenario(scenarios)
	if base == nil {
		t.Fatal("base scenario required")
	}
	daily := engine.RunDailyTracking(featureRows, timestamps, equity)
	if !daily.TailAccrued {
		t.Fatal("EOF tail accrual required when prior decision exists")
	}
	if !daily.BalancesWithFinalEquity() {
		t.Fatalf("daily PnL must reconcile: sum=%v want=%v daily=%v",
			daily.DailyPnLSum(), daily.FinalEquityUSDT-equity, daily.DailyPnL)
	}
	if math.Abs(daily.FinalEquityUSDT-base.FinalEquityUSDT) > 1e-3 {
		t.Fatalf("daily final=%v scenario final=%v", daily.FinalEquityUSDT, base.FinalEquityUSDT)
	}
	if len(daily.ClusterPnL) > 1 {
		folds := []walkforward.FoldResult{{NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
		status, checks, _ := walkforward.EvaluateWFGates(folds, nil, daily, nil)
		if daily.ClusterPnLSum() <= 0 || !checks.Robustness.SymbolClusterLOO_OK || status != walkforward.StatusWFPass {
			t.Fatalf("incremental cluster LOO must pass after EOF tail: status=%s cluster=%v checks=%+v",
				status, daily.ClusterPnL, checks.Robustness)
		}
	}
}

func TestRunDailyTrackingEOFTailMarksOpenSymbolsOutsideLastRows(t *testing.T) {
	engine, featureRows, timestamps := dailyTrackingTwoSymbolFixture(t)
	equity := float64(g5protocol.ValidationInitialEquities[0])
	daily := engine.RunDailyTracking(featureRows, timestamps, equity)
	if !daily.TailAccrued || !daily.BalancesWithFinalEquity() {
		t.Fatalf("two-symbol EOF tail must balance: tail=%v daily=%v final=%v",
			daily.TailAccrued, daily.DailyPnL, daily.FinalEquityUSDT)
	}
	base := portfoliosim.BaseScenario(engine.RunScenarios(featureRows, timestamps))
	if base == nil {
		t.Fatal("base scenario required")
	}
	if math.Abs(daily.FinalEquityUSDT-base.FinalEquityUSDT) > 1e-3 {
		t.Fatalf("daily final=%v scenario final=%v", daily.FinalEquityUSDT, base.FinalEquityUSDT)
	}
}

func dailyTrackingEngine(t *testing.T) portfoliosim.ScenarioEngine {
	t.Helper()
	engine, _, _ := dailyTrackingFixture(t)
	return engine
}

func dailyTrackingFixture(t *testing.T) (portfoliosim.ScenarioEngine, map[string][]features.DecisionContext, []string) {
	t.Helper()
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
	base := time.Date(2024, 4, 1, 8, 0, 0, 0, time.UTC)
	opens := make([]float64, 24)
	for i := range opens {
		opens[i] = 100 + float64(i)*2
	}
	writeTestBars(t, historyRoot, "BTC-USDT-SWAP", opens)
	writeTestFunding(t, historyRoot, "BTC-USDT-SWAP", base.Add(10*time.Hour), 0.0008)
	featureRows := map[string][]features.DecisionContext{}
	var timestamps []string
	for i := 0; i < 12; i++ {
		decision := base.Add(time.Duration(i) * time.Hour).Add(time.Hour)
		ts := decision.Format(time.RFC3339Nano)
		timestamps = append(timestamps, ts)
		featureRows[ts] = []features.DecisionContext{{
			ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP",
			DecisionTime: decision, Partition: outcome.PartitionValidation,
			Features: synthWalkFwdFeatures(1.0, 1.0),
		}}
	}
	barsBundle, err := portfoliosim.LoadCanonicalBars(testCatalogWithFunding(), historyRoot, "okx_1h", featureRows, 0)
	if err != nil {
		t.Fatal(err)
	}
	horizons := bundle.Policy.HorizonLabels
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	engine := portfoliosim.ScenarioEngine{
		Policy: bundle.Policy, Bars: barsBundle, Profile: profile,
		Horizons: horizons, Partition: outcome.PartitionValidation,
	}
	return engine, featureRows, timestamps
}

func dailyTrackingTwoSymbolFixture(t *testing.T) (portfoliosim.ScenarioEngine, map[string][]features.DecisionContext, []string) {
	t.Helper()
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
	base := time.Date(2024, 4, 2, 8, 0, 0, 0, time.UTC)
	for _, spec := range []struct {
		symbol string
		basePx float64
	}{
		{"BTC-USDT-SWAP", 100},
		{"ETH-USDT-SWAP", 200},
	} {
		opens := make([]float64, 16)
		for i := range opens {
			opens[i] = spec.basePx + float64(i)*2
		}
		writeTestBarsWithBase(t, historyRoot, spec.symbol, base, opens)
		writeTestFunding(t, historyRoot, spec.symbol, base.Add(10*time.Hour), 0.0005)
	}
	featureRows := map[string][]features.DecisionContext{}
	var timestamps []string
	for i := 0; i < 10; i++ {
		decision := base.Add(time.Duration(i) * time.Hour).Add(time.Hour)
		ts := decision.Format(time.RFC3339Nano)
		timestamps = append(timestamps, ts)
		rows := []features.DecisionContext{
			{
				ProfileID: "okx_1h", Symbol: "BTC-USDT-SWAP",
				DecisionTime: decision, Partition: outcome.PartitionValidation,
				Features: synthWalkFwdFeatures(1.0, 1.0),
			},
		}
		if i < 8 {
			rows = append(rows, features.DecisionContext{
				ProfileID: "okx_1h", Symbol: "ETH-USDT-SWAP",
				DecisionTime: decision, Partition: outcome.PartitionValidation,
				Features: synthWalkFwdFeatures(0.5, 0.5),
			})
		}
		featureRows[ts] = rows
	}
	catalog := testCatalogWithFunding()
	catalog.Families[0].Files = append(catalog.Families[0].Files,
		historydata.FileStats{Family: "okx_1h", Path: "okx_1h/ETH-USDT-SWAP.jsonl"})
	catalog.Families[1].Files = append(catalog.Families[1].Files,
		historydata.FileStats{Family: "okx_funding", Path: "okx_funding/ETH-USDT-SWAP.jsonl"})
	barsBundle, err := portfoliosim.LoadCanonicalBars(catalog, historyRoot, "okx_1h", featureRows, 0)
	if err != nil {
		t.Fatal(err)
	}
	horizons := bundle.Policy.HorizonLabels
	if len(horizons) == 0 {
		horizons = []string{"1h"}
	}
	engine := portfoliosim.ScenarioEngine{
		Policy: bundle.Policy, Bars: barsBundle, Profile: profile,
		Horizons: horizons, Partition: outcome.PartitionValidation,
	}
	return engine, featureRows, timestamps
}

func synthWalkFwdFeatures(symbolReturn, beta float64) map[string]float64 {
	return map[string]float64{
		"btc_return": 0.5, "symbol_return": symbolReturn, "btc_vol": 0.1, "btc_trend": 0.2,
		"btc_drawdown": 0.05, "btc_funding_state": 0, "sky_confidence": 0.9,
		"mask_btc_funding_missing": 0, "symbol_rv": 0.1, "symbol_beta": beta,
		"btc_relative_return": 0.01, "liquidity_percentile": 0.5, "quote_volume_rank": 0.5,
		"return_rank": 0.9, "vol_rank": 0.5, "liquidity_rank": 0.5, "beta_rank": 0.5,
		"btc_relative_strength_rank": 0.9, "liquidity_score": 0.8, "vol_cost_penalty": 0.1,
		"capacity_headroom": 0.9, "cost_estimate": 15.0,
	}
}

func writeTestBarsWithBase(t *testing.T, historyRoot, symbol string, base time.Time, opens []float64) {
	t.Helper()
	dir := filepath.Join(historyRoot, "okx_1h")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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

func writeTestFunding(t *testing.T, historyRoot, symbol string, settle time.Time, rate float64) {
	t.Helper()
	dir := filepath.Join(historyRoot, "okx_funding")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"data":{"fundingRate":"%v","fundingTime":"%d"}}`+"\n", rate, settle.UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, symbol+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testCatalogWithFunding() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{
		DataHash: g5protocol.HistoryDataHash,
		Families: []historydata.FamilyCatalog{
			{
				Name: "okx_1h",
				Files: []historydata.FileStats{{Family: "okx_1h", Path: "okx_1h/BTC-USDT-SWAP.jsonl"}},
			},
			{
				Name: "okx_funding",
				Files: []historydata.FileStats{{Family: "okx_funding", Path: "okx_funding/BTC-USDT-SWAP.jsonl"}},
			},
		},
	}
}

func TestNoFrozenPolicySkipsValidationReplay(t *testing.T) {
	dir := writeNoFrozenPolicyDir(t)
	bundle, err := policy.LoadPolicyBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reverselearn.IsNoFrozenPolicy(bundle.PolicyManifest) {
		t.Fatal("expected NO_FROZEN_POLICY manifest")
	}
	if _, err := os.Stat(filepath.Join(dir, "g5_policy.json")); !os.IsNotExist(err) {
		t.Fatal("degenerate bundle must not write scoreable policy")
	}
	outDir := filepath.Join(t.TempDir(), "validation")
	runner := portfoliosim.Runner{
		Config: portfoliosim.DefaultRunConfig("okx_1h", t.TempDir(), dir, t.TempDir(), outDir, bundle),
		Catalog: testCatalog(),
	}
	result, err := runner.Run()
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != portfoliosim.StatusSkippedPolicyUnavailable {
		t.Fatalf("status=%s", result.Report.Status)
	}
	if result.Report.FailReason != portfoliosim.ReasonNoGo {
		t.Fatalf("reason=%s", result.Report.FailReason)
	}
	if result.Report.ExamActions != 0 {
		t.Fatal("exam_actions must be 0")
	}
	if len(result.Report.Scenarios) != 0 {
		t.Fatal("skipped validation must not replay scenarios")
	}
	if result.Report.PolicyHash != "" {
		t.Fatal("skipped validation must not bind policy hash")
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
		SchemaVersion: reverselearn.SchemaVersionConfig,
		ProfileID:     "okx_1h",
		Generation:    g5protocol.Generation,
		ProtocolHash:  g5protocol.RouterProtocolHash,
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
