package personalvalidation

import (
	"math"
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
	layers := EvaluateLayers(nil, MakerPathStats{}, PITContaminationReport{}, DegeneracySummary{}, LeaveOneReport{}, false, nil, false)
	if len(layers) != 5 {
		t.Fatalf("layers=%d", len(layers))
	}
	if layers[0].Layer != LayerHistoricalPhenomenon || layers[0].Status != StatusInconclusive {
		t.Fatalf("empty historical layer=%+v", layers[0])
	}
	events := []TriggerEvent{
		{EventFamily: eventmap.FamilyExtremeContinuation, Day: "2024-01-01", ShockCluster: "shock-a"},
		{EventFamily: eventmap.FamilyExtremeContinuation, Day: "2024-01-02", ShockCluster: "shock-b"},
		{EventFamily: eventmap.FamilyExtremeContinuation, Day: "2024-01-03", ShockCluster: "shock-c"},
	}
	layers = EvaluateLayers(nil, MakerPathStats{}, PITContaminationReport{}, DegeneracySummary{}, LeaveOneReport{}, true, events, false)
	if layers[0].Status != StatusValidationPass {
		t.Fatalf("real gated historical layer=%+v", layers[0])
	}
	if layers[4].Layer != LayerWFPass || layers[4].Status != StatusInconclusive {
		t.Fatalf("wf layer=%+v", layers[4])
	}
}

func TestHeuristicOnlyLayersAllInconclusive(t *testing.T) {
	layers := heuristicOnlyLayers()
	if len(layers) != 5 {
		t.Fatalf("layers=%d", len(layers))
	}
	for _, lv := range layers {
		if lv.Status != StatusInconclusive || lv.FailReason != ReasonInsufficientEvidence {
			t.Fatalf("heuristic layer must be inconclusive: %+v", lv)
		}
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
		ExecutionPolicies: map[string]executionpolicy.FrozenPolicy{
			"evt": {
				TriggerID: "evt", ExecutionPolicyHash: "pol",
				MaxPriceCap: 100.3, MaxSlippageBPS: 30, TimeoutMS: 3000,
			},
		},
	}
	sim, tr := observeExecution(bars, 0, 0, "long", 5, 3, 100, "evt", pb, proofs, false)
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

func TestFormalValidationRequiresProofDirs(t *testing.T) {
	cfg := RunConfig{
		HistoryRoot: "h", PlaybookOutputDir: "p", OutputDir: "o",
		PlaybookManifestHash: "pm", ConstitutionHash: "c",
		ReverseTapesDir: "rt", ReverseTapesManifestHash: "rtm",
		AnnouncementDir: "ann", AnnouncementManifestHash: "am",
		MapDirs: []string{"m"}, MaxFiles: 1, MaxLines: 1, ProgressEvery: 1,
		AllowTruncatedSmoke: true,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("smoke validate: %v", err)
	}
	cfg.AllowTruncatedSmoke = false
	if err := cfg.ValidateFormalProofBindings(); err == nil {
		t.Fatal("formal validation must require proof dirs")
	}
}

func TestFormalValidationProofObservationsFailClosed(t *testing.T) {
	bars := []eventmap.Bar{
		{Open: 100, High: 101, Low: 99, Close: 100, OpenTime: time.Unix(0, 0)},
		{Open: 100, High: 101, Low: 99, Close: 100.5, OpenTime: time.Unix(3600, 0)},
		{Open: 100.5, High: 102, Low: 100, Close: 101, OpenTime: time.Unix(7200, 0)},
	}
	pb := personalplaybook.PersonalWildPlaybook{
		ProofBinding: personalplaybook.ProofBinding{ExecutionPolicyHash: "pol"},
	}
	sim, _ := observeExecution(bars, 0, 0, "long", 5, 3, 100, "missing-proof", pb, ProofBundle{}, true)
	if sim.MakerFilled {
		t.Fatal("formal path must not OHLC-fill without entry proof")
	}
}

func TestPrepositionTriggerRequiresPITMinLot(t *testing.T) {
	featLow := BuildDecisionVisibleFeatures(DecisionFeatureInput{
		Bars: []eventmap.Bar{{Close: 100, OpenTime: time.Unix(0, 0)}},
		BarIndex: 0, Spec: eventmap.ProfileSpec{BarDuration: time.Hour},
		Decision: time.Unix(0, 0), Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		CapitalUSDT: 30, MinNotionalUSDT: 3,
	})
	if featLow.PrepositionedCapitalWindow != 0 {
		t.Fatalf("low min lot must not trigger preposition: %+v", featLow)
	}
	featHigh := BuildDecisionVisibleFeatures(DecisionFeatureInput{
		Bars: []eventmap.Bar{{Close: 100, OpenTime: time.Unix(0, 0)}},
		BarIndex: 0, Spec: eventmap.ProfileSpec{BarDuration: time.Hour},
		Decision: time.Unix(0, 0), Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		CapitalUSDT: 30, MinNotionalUSDT: 5,
	})
	if featHigh.PrepositionedCapitalWindow != 1 {
		t.Fatalf("high min lot ratio should trigger preposition: %+v", featHigh)
	}
}

func TestCanonicalEventIDMatchesBrain(t *testing.T) {
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	id := personalbrain.CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	want := CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	if id != want {
		t.Fatalf("brain id=%q want %q", id, want)
	}
}

func TestBoundConservativeStressBPSUsesBPSNotUSDT(t *testing.T) {
	ref := 200.0
	bps := 23.4
	usdt := executionpolicy.BpsToUSDT(bps, ref)
	a := executionpolicy.ActionEvaluation{
		Action:          executionpolicy.ActionConditionalMarketableLimit,
		ExpectedNetBPS:  bps,
		ExpectedNetUSDT: usdt,
		Allowed:         true,
	}
	got := executionpolicy.ActionExpectedNetBPS(a, ref)
	if math.Abs(got-bps) > 1e-9 {
		t.Fatalf("got %v want %v", got, bps)
	}
	if math.Abs(usdt-bps) < 1 {
		t.Fatal("sanity: USDT must not equal raw BPS magnitude")
	}
}

func TestCanonicalEventIDMatchesEventmap(t *testing.T) {
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	id := CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	want := eventmap.BuildEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	if id != want {
		t.Fatalf("id=%q want %q", id, want)
	}
}

func TestTriggerEventFrozenParamsRoundTrip(t *testing.T) {
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ev := TriggerEvent{
		EventID: "evt-1", Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		Direction: "long", EventFamily: eventmap.FamilyExtremeContinuation,
		DecisionTime: decision, DecisionBarIndex: 1, ReturnBPS: 50, PriorReturnBPS: 40,
		MakerFilled: true, FrozenAction: executionpolicy.ActionConditionalMarketableLimit,
		ConservativeBPS: 20, ExitProofOK: true, CrowdingPassed: true,
		MaxPriceCap: 103, MaxSlippageBPS: 30, MinRemainingEdgeBPS: 5,
		TimeoutMS: 3000, SpreadBPS: 3, FeeBPS: 2, LatencyMS: 50,
	}
	pb := personalplaybook.PersonalWildPlaybook{
		ProfileID: eventmap.ProfileOKX1H, EventFamily: eventmap.FamilyExtremeContinuation,
		Horizon: "1h", Direction: "long",
		PublicTrigger: personalplaybook.PublicTriggerSpec{
			Conditions: []personalplaybook.PublicTriggerCondition{
				{Field: "event_family", Op: "eq", Note: eventmap.FamilyExtremeContinuation},
			},
		},
		RiskNarrative: personalplaybook.RiskNarrative{MaxSizeUSDT: 50, MaxCapacityUSDT: 500},
	}
	step := replayStep{time: decision, barIndex: 1, prices: map[string]float64{"BTC-USDT-SWAP": 100}}
	bar := buildBarInputFromTrigger(&ev, pb, step, 0, 1, 100, ProofBundle{})
	if bar.ExecutionSim.MaxPriceCap != ev.MaxPriceCap {
		t.Fatalf("cap=%v want %v", bar.ExecutionSim.MaxPriceCap, ev.MaxPriceCap)
	}
	if bar.ExecutionSim.TimeoutMS != ev.TimeoutMS {
		t.Fatalf("timeout=%v want %v", bar.ExecutionSim.TimeoutMS, ev.TimeoutMS)
	}
	if len(bar.Events) == 0 {
		t.Fatal("expected matched event from decision-visible trigger")
	}
}

func TestExecutionPolicyEventIDOnlyNoHashFallback(t *testing.T) {
	fp := executionpolicy.FrozenPolicy{
		TriggerID: "event-a", ExecutionPolicyHash: "shared-hash",
		MaxPriceCap: 105, SelectedAction: executionpolicy.ActionConditionalMarketableLimit,
	}
	bundle := ProofBundle{
		ExecutionPoliciesByHash: map[string]executionpolicy.FrozenPolicy{"shared-hash": fp},
	}
	if _, ok := bundle.executionPolicy("event-b"); ok {
		t.Fatal("hash fallback must not bind cross-trigger policy")
	}
	bundle.ExecutionPolicies = map[string]executionpolicy.FrozenPolicy{"event-a": fp}
	got, ok := bundle.executionPolicy("event-a")
	if !ok || got.MaxPriceCap != 105 {
		t.Fatalf("event-bound policy=%+v ok=%v", got, ok)
	}
}

func TestFreezeConditionalMarketableLimitShortFloor(t *testing.T) {
	tr := executionpolicy.TriggerRecord{
		Side: "short", Price: 3500, MaxPriceCap: 3495.8,
		SpreadBPS: 4, FeeBPS: 2, LatencyMS: 60,
		RemainingEdgeBPS: 30, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
	}
	ev := executionpolicy.FreezeConditionalMarketableLimit(tr)
	if !ev.Allowed {
		t.Fatalf("price above floor must allow CML: %+v", ev)
	}
	tr.Price = 3480
	ev = executionpolicy.FreezeConditionalMarketableLimit(tr)
	if ev.Allowed || ev.BlockReason != "below_min_price_floor" {
		t.Fatalf("price below floor must block: %+v", ev)
	}
}
