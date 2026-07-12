package walkforward_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg6/eventaccount"
	"lingxi/internal/routerg6/eventvalidation"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
	"lingxi/internal/routerg6/walkforward"
)

func TestValidationFailSkipsWF(t *testing.T) {
	dir := t.TempDir()
	valDir := filepath.Join(dir, "validation")
	if err := os.MkdirAll(valDir, 0o755); err != nil {
		t.Fatal(err)
	}
	failReport := eventvalidation.ValidationReport{
		SchemaVersion:    eventvalidation.SchemaVersionReport,
		Generation:       protocol.Generation,
		ProtocolHash:     protocol.RouterProtocolHash,
		CardManifestHash: "manifest",
		PassCount:        0,
		ExamActions:      0,
		CreatedAt:        time.Now().UTC(),
	}
	raw, _ := json.Marshal(failReport)
	if err := os.WriteFile(filepath.Join(valDir, "g6_validation_report.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := walkforward.DefaultRunConfig(t.TempDir(), t.TempDir(), valDir, filepath.Join(dir, "out"), []string{t.TempDir()})
	cfg.CardManifestHash = "manifest"
	cfg.MaxFiles = 1
	cfg.MaxLines = 100
	runner := walkforward.Runner{
		Config: cfg, Catalog: testCatalog(),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
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
	folds, err := walkforward.BuildFolds(ts, protocol.WFFoldCount, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(folds) != protocol.WFFoldCount {
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
	folds, err := walkforward.BuildFolds(ts, protocol.WFFoldCount, embargo)
	if err != nil {
		t.Fatal(err)
	}
	prevEnd := folds[0].EndTime
	for i := 1; i < len(folds); i++ {
		start, err := time.Parse(time.RFC3339Nano, folds[i].Timestamps[0])
		if err != nil {
			t.Fatal(err)
		}
		if !start.After(prevEnd.Add(embargo)) {
			t.Fatalf("fold %d must start after embargo gap", i)
		}
		prevEnd = folds[i].EndTime
	}
}

func TestFoldNetReturnGate(t *testing.T) {
	folds := []walkforward.FoldResult{
		{FoldIndex: 0, CapitalTier: 100, NetReturnPositive: false, BaseNetReturnFraction: -0.1},
	}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonFoldNetReturn {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestLOFOGate(t *testing.T) {
	folds := []walkforward.FoldResult{
		{FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1},
	}
	lofo := []walkforward.LOFOResult{{CapitalTier: 100, ExcludedFoldIndex: 0, NetReturnFraction: -0.1, NetReturnPositive: false}}
	status, _, reason := walkforward.EvaluateWFGates(folds, lofo, walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonLOFO {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestDelayGate(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []walkforward.TierScenarioResult{
			{DelayBars: 1, CostStressMultiplier: 1.0, InitialEquityUSDT: 100, FinalEquityUSDT: 90},
		},
	}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonDelay {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestCostStressGate(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []walkforward.TierScenarioResult{
			{DelayBars: 0, CostStressMultiplier: 1.5, InitialEquityUSDT: 100, FinalEquityUSDT: 90},
		},
	}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"d": 1}}, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonCostStress {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestDayLOOGate(t *testing.T) {
	folds := []walkforward.FoldResult{{FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
	daily := walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"a": 10, "b": -20}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonDay {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestMechanismUnexplainedEvenOnPass(t *testing.T) {
	if got := walkforward.DecideMechanismFromFeatures([]string{"btc_return"}); got != walkforward.MechanismUnexplainedEventPass {
		t.Fatalf("got %q", got)
	}
	wf := walkforward.WFReport{Status: walkforward.StatusWFPass, ReportHash: "r"}
	mech := walkforward.BuildMechanismReport(wf)
	if mech.MechanismStatus != walkforward.MechanismUnexplainedEventPass {
		t.Fatalf("mechanism=%q", mech.MechanismStatus)
	}
	if mech.TradeAllowed || mech.ExamEligible {
		t.Fatal("must not allow trade or exam")
	}
}

func TestHolmFamilyGate(t *testing.T) {
	folds := []walkforward.FoldResult{{FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
	daily := walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"a": 5, "b": 5}}
	attempts := []walkforward.AttemptSignResult{
		{Selected: true, HolmPass: false, SignPValue: 0.5},
	}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, attempts)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonHolm {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestBuildAttemptSignResultsHolm(t *testing.T) {
	ledger := reverseevent.ExperimentLedger{
		Attempts: []reverseevent.ExperimentAttempt{
			{AttemptOrdinal: 1, EventCardHash: "selected", Status: protocol.UnexplainedCandidateStatus},
			{AttemptOrdinal: 2, EventCardHash: "other", Status: protocol.UnexplainedCandidateStatus, SourceWinnerCount: 1},
		},
	}
	daily := walkforward.DailyEquityReplay{
		DailyPnL: map[string]float64{"d1": 1, "d2": 1, "d3": 1, "d4": 1, "d5": 1, "d6": 1, "d7": 1, "d8": 1, "d9": 1, "d10": 1},
	}
	results := walkforward.BuildAttemptSignResults(ledger, "selected", daily)
	if len(results) != 2 {
		t.Fatalf("results=%d", len(results))
	}
	if !results[0].Selected || results[0].SignPValue >= 0.01 {
		t.Fatalf("selected p=%g", results[0].SignPValue)
	}
}

func TestMinNotionalProxySkips30U(t *testing.T) {
	acct := eventaccount.NewAccount(30)
	c := eventaccount.EventCandidate{
		EventID: "e1", Symbol: "BTC-USDT-SWAP", Direction: "long",
		ConservativeEdgeUSDT: 1, StressLossUSDT: 0.5, ReferenceNotional: 30,
		MarginTimeSec: 3600, MinNotionalUSDT: 100,
		LotNotionalUSDT: walkforward.LotNotionalProxyUSDT,
	}
	_, rejected := eventaccount.QueueAdmission{MaxConcurrent: 1}.Select(acct, []eventaccount.EventCandidate{c})
	if len(rejected) != 1 {
		t.Fatalf("want min-notional skip got accepted=%d rejected=%d", 0, len(rejected))
	}
}

func TestCheckpointBinding(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := walkforward.DefaultRunConfig(dir, dir, dir, dir, []string{dir})
	cfg.CardManifestHash = "manifest"
	cfg.MaxFiles = 1
	cfg.MaxLines = 100
	cfg.ValidationReportHash = "val"
	cfg.SurvivorsHash = "surv"
	cfgHash, err := walkforward.ConfigFingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cp := walkforward.Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		CardManifestHash: "manifest", ValidationReportHash: "val", SurvivorsHash: "surv", OutputDir: dir,
	}
	if err := walkforward.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := walkforward.LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if err := walkforward.VerifyCheckpointBinding(loaded, cfg, "manifest", "surv", nil); err != nil {
		t.Fatal(err)
	}
	cfg.SurvivorsHash = "tampered"
	if err := walkforward.VerifyCheckpointBinding(loaded, cfg, "manifest", "surv", nil); err == nil {
		t.Fatal("tampered survivors hash must fail")
	}
}

func TestExecutionReportFeasibility(t *testing.T) {
	wf := walkforward.WFReport{ReportHash: "wf", ExamActions: 0}
	stats := walkforward.ReplayStats{EventsSeen: 10, EventsSkipped: 3, SkipBelowMinNotional: 2, SkipCapacity: 1}
	report := walkforward.BuildExecutionReport(wf, stats)
	if report.SkipBelowMinNotional != 2 || report.CapacityFailRate <= 0 {
		t.Fatalf("execution report=%+v", report)
	}
	if report.LotNotionalProxyUSDT != walkforward.LotNotionalProxyUSDT {
		t.Fatalf("lot proxy=%v", report.LotNotionalProxyUSDT)
	}
}

func TestMaxDrawdownGate(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []walkforward.TierScenarioResult{
			{DelayBars: 0, CostStressMultiplier: 1.0, InitialEquityUSDT: 100, FinalEquityUSDT: 110, MaxDrawdownFraction: 0.35, EventsSeen: 5},
		},
	}}
	daily := walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"a": 5, "b": 5}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonMaxDrawdown {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestCapacityGate(t *testing.T) {
	folds := []walkforward.FoldResult{{
		FoldIndex: 0, CapitalTier: 30, NetReturnPositive: true, BaseNetReturnFraction: 0.1,
		Scenarios: []walkforward.TierScenarioResult{
			{DelayBars: 0, CostStressMultiplier: 1.0, InitialEquityUSDT: 30, FinalEquityUSDT: 35, EventsSeen: 10, SkipBelowMinNotional: 6},
		},
	}}
	daily := walkforward.DailyEquityReplay{DailyPnL: map[string]float64{"a": 5, "b": 5}}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonCapacity {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestShockLOOGate(t *testing.T) {
	folds := []walkforward.FoldResult{{FoldIndex: 0, CapitalTier: 100, NetReturnPositive: true, BaseNetReturnFraction: 0.1}}
	daily := walkforward.DailyEquityReplay{
		DailyPnL:    map[string]float64{"a": 5, "b": 5},
		ShockDayPnL: map[string]float64{"s1": 10, "s2": -20},
	}
	status, _, reason := walkforward.EvaluateWFGates(folds, nil, daily, nil)
	if status != walkforward.StatusWFFail || reason != walkforward.ReasonShock {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestStopErrorExitPriority(t *testing.T) {
	sig := eventaccount.ExitSignal{BarIndex: 5, EventComplete: true, PayerGone: true}
	_, reason, exit := eventaccount.EvaluateStopError(eventaccount.StopErrorHolding, sig)
	if !exit || reason != eventaccount.ExitEventComplete {
		t.Fatalf("stop=%q exit=%v", reason, exit)
	}
}

func TestThreeTierConcurrentCaps(t *testing.T) {
	for _, tc := range []struct {
		equity float64
		want   int
	}{
		{30, 1}, {100, 2}, {1000, 3},
	} {
		if got := walkforward.MaxConcurrentForTier(tc.equity); got != tc.want {
			t.Fatalf("equity %v cap=%d want %d", tc.equity, got, tc.want)
		}
	}
}

func TestZeroEventsInconclusive(t *testing.T) {
	folds, err := walkforward.BuildFolds([]string{
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}, protocol.WFFoldCount, time.Hour)
	if err == nil {
		t.Fatal("expected insufficient timestamps error")
	}
	_ = folds
	status, _, reason := walkforward.EvaluateWFGates(nil, nil, walkforward.DailyEquityReplay{}, nil)
	if status != walkforward.StatusInconclusive || reason != walkforward.ReasonFoldNetReturn {
		t.Fatalf("got %s/%s", status, reason)
	}
}

func TestBuildWFSummaryExamZero(t *testing.T) {
	wf := walkforward.WFReport{Status: walkforward.StatusWFPass, ReportHash: "wf", PassCount: 1, ExamActions: 0}
	mech := walkforward.BuildMechanismReport(wf)
	exec := walkforward.BuildExecutionReport(wf, walkforward.ReplayStats{})
	summary := walkforward.BuildWFSummary(wf, mech, exec)
	if summary.ExamActions != 0 || summary.ExamEligibleCount != 0 || summary.TradeAllowedCount != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.MechanismStatus != walkforward.MechanismUnexplainedEventPass {
		t.Fatalf("mechanism=%q", summary.MechanismStatus)
	}
}

func testCatalog() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{DataHash: protocol.HistoryDataHash}
}
