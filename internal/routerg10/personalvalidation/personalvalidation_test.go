package personalvalidation

import (
	"testing"
	"time"

	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg10/executionpolicy"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
)

func TestProtocolNoDrift(t *testing.T) {
	if Generation != g10protocolv2.Generation {
		t.Fatalf("generation drift")
	}
	if ProtocolHash != g10protocolv2.RouterProtocolHash {
		t.Fatalf("protocol hash drift")
	}
	if SurvivorStatus != g10protocolv2.UnexplainedPersonalEdgeStatus {
		t.Fatal("survivor status drift")
	}
}

func TestEvaluateTierGatesSparseInconclusiveNotZeroEdge(t *testing.T) {
	replay := TierReplayStats{Count: 10, UniqueDays: 5, UniqueShockBlocks: 2}
	scenarios := []TierScenarioResult{{
		CapitalTier: 30, DelayBars: 0, InitialEquityUSDT: 30, FinalEquityUSDT: 31,
		NetReturnNonNegative: true,
	}}
	status, checks, reason := EvaluateTierGates(replay, scenarios, PITContaminationReport{}, MakerPathStats{}, true)
	if status != StatusInconclusive {
		t.Fatalf("expected INCONCLUSIVE, got %s", status)
	}
	if reason != ReasonInsufficientEvidence {
		t.Fatalf("expected INSUFFICIENT_EVIDENCE, got %s", reason)
	}
	if checks.SummaryNote != InconclusiveNotZeroEdgeNote {
		t.Fatal("missing inconclusive note")
	}
}

func TestMissedTriggerZeroPnL(t *testing.T) {
	tr := executionpolicy.TriggerRecord{
		MakerTouched: true, SubsequentlyProfitable: true, NetPnLUSDT: 99,
	}
	if executionpolicy.MissedProfitablePnL(tr) != 0 {
		t.Fatal("missed profitable must be zero pnl")
	}
}

func TestBuildMakerPathStatsFullDenominator(t *testing.T) {
	events := []TriggerEvent{
		{MakerFilled: true, NetPnLUSDT: 1, MakerQuadrant: executionpolicy.QuadrantFilledProfitable, FastFill: true},
		{MakerFilled: true, NetPnLUSDT: -1, MakerQuadrant: executionpolicy.QuadrantFilledUnprofitable},
		{MakerTouchedNotFilled: true, SubsequentlyProfitable: true, MakerQuadrant: executionpolicy.QuadrantUnfilledSubsequentlyProfitable},
		{MakerTouchedNotFilled: true, MakerQuadrant: executionpolicy.QuadrantUnfilledNotProfitable},
	}
	stats := BuildMakerPathStats(events)
	if stats.Quadrants.TotalTriggers != 4 {
		t.Fatalf("denominator=%d", stats.Quadrants.TotalTriggers)
	}
}

func TestEvaluateLayersHistoricalPhenomenon(t *testing.T) {
	layers := EvaluateLayers(nil, MakerPathStats{}, PITContaminationReport{}, DegeneracySummary{}, LeaveOneReport{}, true)
	if len(layers) != 5 {
		t.Fatalf("layers=%d", len(layers))
	}
	if layers[0].Layer != LayerHistoricalPhenomenon || layers[0].Status != StatusValidationPass {
		t.Fatalf("historical layer=%+v", layers[0])
	}
	if layers[4].Layer != LayerWFPass || layers[4].Status != StatusInconclusive {
		t.Fatalf("wf layer=%+v", layers[4])
	}
}

func TestPITSelfTestNoContamination(t *testing.T) {
	report := RunPITContaminationSelfTest()
	if report.ContaminationDetected {
		t.Fatalf("expected no contamination: %+v", report)
	}
}

func TestFrozenSizingIncludesNegativeEdge(t *testing.T) {
	events := []TriggerEvent{
		{ConservativeBPS: -2, StressBPS: -3, FrozenAction: executionpolicy.ActionBoundedMaker, MakerFilled: true},
		{ConservativeBPS: 4, StressBPS: 1, EnterGate: personalbrain.EnterMissedCorrectly, MakerTouchedNotFilled: true},
	}
	sizing := FrozenSizingFromEvents(events, 30)
	if sizing.ConservativeMeanBPS != -2 {
		t.Fatalf("mean=%v", sizing.ConservativeMeanBPS)
	}
}

func TestFrozenSizingMissedZeroBPS(t *testing.T) {
	events := []TriggerEvent{
		{ConservativeBPS: 40, EnterGate: personalbrain.EnterMissedCorrectly, MakerTouchedNotFilled: true},
		{ConservativeBPS: 30, NoTrade: true, FrozenAction: executionpolicy.ActionSkip},
	}
	sizing := FrozenSizingFromEvents(events, 30)
	if sizing.ConservativeMeanBPS != 0 {
		t.Fatalf("missed sizing bps must be 0, got %v", sizing.ConservativeMeanBPS)
	}
}

func TestObserveExecutionMaxPriceCapNot1e12(t *testing.T) {
	bars := []eventmap.Bar{
		{Open: 100, High: 101, Low: 99, Close: 100, OpenTime: time.Unix(0, 0)},
		{Open: 100, High: 101, Low: 99, Close: 100.5, OpenTime: time.Unix(3600, 0)},
		{Open: 100.5, High: 102, Low: 100, Close: 101, OpenTime: time.Unix(7200, 0)},
	}
	pb := personalplaybook.PersonalWildPlaybook{
		ProofBinding:  personalplaybook.ProofBinding{ExecutionPolicyHash: "pol"},
		RiskNarrative: personalplaybook.RiskNarrative{MaxCapacityUSDT: 100, MaxSizeUSDT: 50},
	}
	proofs := ProofBundle{
		ExecutionPoliciesByHash: map[string]executionpolicy.FrozenPolicy{"pol": {ExecutionPolicyHash: "pol"}},
	}
	sim, tr := observeExecution(bars, 0, 0, "long", 5, 3, 100, "evt", pb, proofs)
	if sim.MaxPriceCap >= 1e11 || tr.MaxPriceCap >= 1e11 {
		t.Fatalf("max price cap sim=%v tr=%v", sim.MaxPriceCap, tr.MaxPriceCap)
	}
}

func TestFormalValidationScopeBlocksTruncatedSmokePass(t *testing.T) {
	scope := map[string]HistoryScopeSummary{
		"okx_1h": {Available: 10, Selected: 3, Truncated: true, FullMarket: false, Scope: "TRUNCATED_SMOKE"},
	}
	if FormalValidationAllowed(true, scope) {
		t.Fatal("truncated smoke cannot be formal")
	}
	if FormalValidationAllowed(false, scope) {
		t.Fatal("truncated catalog cannot formal pass")
	}
}

func TestCrowdingLevels135(t *testing.T) {
	if len(CrowdingLevels) != 3 || CrowdingLevels[0] != 1 || CrowdingLevels[2] != 5 {
		t.Fatalf("crowding levels=%v", CrowdingLevels)
	}
}

func TestRunConfigValidateRequiredBindings(t *testing.T) {
	err := (RunConfig{}).Validate()
	if err == nil {
		t.Fatal("expected validate error")
	}
}
