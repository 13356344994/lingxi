package personalvalidation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	g10constitution "lingxi/internal/routerg10/constitution"
	g10entryproof "lingxi/internal/routerg10/entryproof"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	g10exitproof "lingxi/internal/routerg10/exitproof"
	"lingxi/internal/routerg10/exclusionmap"
	"lingxi/internal/routerg10/personalbrain"
	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
)

func TestDefaultTriggerNotTautological(t *testing.T) {
	forbidden := map[string]bool{}
	for _, f := range personalplaybook.ForbiddenTriggerFields {
		forbidden[f] = true
	}
	if !forbidden["personal_tape_family"] || !forbidden["horizon"] {
		t.Fatal("personal_tape_family and horizon must be forbidden trigger fields")
	}
	for _, f := range personalplaybook.KnownPublicTriggerFields {
		if f == "personal_tape_family" || f == "horizon" {
			t.Fatalf("field %q must not be in known public trigger fields", f)
		}
	}
}

func TestBuildDecisionVisibleFeaturesNoPlaybookInjection(t *testing.T) {
	bars := []eventmap.Bar{
		{Open: 100, High: 101, Low: 99, Close: 100, OpenTime: time.Unix(0, 0)},
		{Open: 100, High: 105, Low: 99, Close: 104, OpenTime: time.Unix(3600, 0)},
		{Open: 104, High: 106, Low: 103, Close: 105, OpenTime: time.Unix(7200, 0)},
	}
	spec := eventmap.ProfileSpec{BarDuration: time.Hour}
	feat := BuildDecisionVisibleFeatures(DecisionFeatureInput{
		Bars: bars, BarIndex: 1, Spec: spec, WindowBars: 2, PrimaryBack: 1,
		Decision: bars[1].OpenTime, Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		CapitalUSDT: 30, MinNotionalUSDT: 5,
	})
	if feat.Symbol != "BTC-USDT-SWAP" || feat.ProfileID != eventmap.ProfileOKX1H {
		t.Fatalf("unexpected decision-visible features: %+v", feat)
	}
}

func TestDecideBarWithBoundProofCanEnter(t *testing.T) {
	constHash, _ := g10constitution.Hash()
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	eventID := CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	pb, proofs := proofBoundFixtures(constHash, eventID, decision)
	ev := TriggerEvent{
		EventID: eventID, Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		Direction: "long", EventFamily: eventmap.FamilyExtremeContinuation,
		DecisionTime: decision, DecisionBarIndex: 1,
		MakerFilled: true, FrozenAction: g10executionpolicy.ActionConditionalMarketableLimit,
		ConservativeBPS: 20, ReturnBPS: 5000, PriorReturnBPS: 4500,
		ExitProofOK: true, CrowdingPassed: true, ExclusionProofBound: true,
		EntryProofBound: true, ExecutionProofBound: true, ExitProofBound: true,
		MaxPriceCap: 103, MaxSlippageBPS: 30, MinRemainingEdgeBPS: 5,
		TimeoutMS: 3000, SpreadBPS: 3, FeeBPS: 2, LatencyMS: 50,
	}
	step := replayStep{
		time: decision, barIndex: 1,
		prices: map[string]float64{"BTC-USDT-SWAP": 100},
	}
	bar := buildBarInputFromTrigger(&ev, pb, step, 0, 1, 100, proofs)
	cfg := personalbrain.Config{
		InitialEquityUSDT: 100, ConstitutionHash: constHash,
		MinNotionalUSDT: 5, LotStep: 1, ContractValue: 5,
		ProtocolHash: personalbrain.ProtocolHash, TradeAllowed: true,
	}
	out, err := personalbrain.NewBrain(cfg).DecideBar(bar)
	if err != nil {
		t.Fatal(err)
	}
	if out.EnterGate.Status != personalbrain.EnterCanEnter {
		t.Fatalf("enter=%+v proofs=%+v", out.EnterGate, bar.Events[0].Proofs)
	}
	if out.AppliedAction != "LONG" && out.AppliedAction != "SHORT" {
		t.Fatalf("applied=%s exec=%+v", out.AppliedAction, out.Execution)
	}
}

func TestProofBoundEnterCanEnterReplay(t *testing.T) {
	constHash, _ := g10constitution.Hash()
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	eventID := CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	pb, proofs := proofBoundFixtures(constHash, eventID, decision)

	bars := []profitmap.Bar{
		{OpenTime: decision.Add(-time.Hour), Open: 100, High: 101, Low: 99, Close: 100},
		{OpenTime: decision, Open: 100, High: 101, Low: 99, Close: 100},
		{OpenTime: decision.Add(time.Hour), Open: 100, High: 102, Low: 99, Close: 101},
		{OpenTime: decision.Add(2 * time.Hour), Open: 101, High: 103, Low: 100, Close: 102},
	}
	barsBySymbol := map[string][]profitmap.Bar{"BTC-USDT-SWAP": bars}
	ev := TriggerEvent{
		EventID: eventID, Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		Direction: "long", EventFamily: eventmap.FamilyExtremeContinuation,
		DecisionTime: decision, DecisionBarIndex: 1, HorizonBarCount: 1, DelayBars: 0,
		MakerFilled: true, FrozenAction: g10executionpolicy.ActionConditionalMarketableLimit,
		ExitProofOK: true, CrowdingPassed: true,
		ConservativeBPS: 20, StressBPS: 10, ReferenceNotional: 50,
		ReturnBPS: 5000, PriorReturnBPS: 4500,
		EntryGrade: g10entryproof.GradeRealFillOrWAL, EntryProofBound: true,
		ExecutionProofBound: true, ExitProofBound: true, ExclusionProofBound: true,
		MaxPriceCap: 103, MaxSlippageBPS: 30, MinRemainingEdgeBPS: 5,
		TimeoutMS: 3000, SpreadBPS: 3, FeeBPS: 2, LatencyMS: 50,
	}
	scenarios, _, stats, _ := ReplayTierEvents(
		[]TriggerEvent{ev}, barsBySymbol, nil, 100,
		FrozenSizingInput{ConservativeMeanBPS: 20, ReferenceNotional: 50},
		pb, proofs,
	)
	if stats.TriggersTraded == 0 {
		t.Fatalf("expected traded trigger with bound A-grade proof, stats=%+v", stats)
	}
	if len(scenarios) == 0 {
		t.Fatal("expected tier scenarios")
	}
}

func TestMissingProofFailClosedEnterGate(t *testing.T) {
	constHash, _ := g10constitution.Hash()
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	eventID := CanonicalEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeContinuation)
	pb := proofBoundPlaybook(constHash, eventID, "ex")
	me := personalbrain.MatchedPersonalEvent{
		Playbook: pb, EventID: eventID, EventFamily: pb.EventFamily, Direction: "long",
		ThesisHealth: personalbrain.ThesisHealthy, CapacityUSDT: 500, MaxSizeUSDT: 200,
		GrossLongBPS: 80, GrossShortBPS: -30, CompletionBars: 4, DeadlineBar: 5,
	}
	me = enrichMatchedEvent(me, pb, ProofBundle{}, eventID, false, true, exclusionmap.CrowdingStress{}, g10executionpolicy.ActionBoundedMaker, true)
	gate := personalbrain.EvaluateEnterGate(me, personalbrain.BarInput{
		ExecutionSim: personalbrain.ExecutionSimState{
			ThesisStillValid: true, RemainingEdgeBPS: 20, MakerFilled: true,
			MaxPriceCap: 130, SpreadBPS: 3, FeeBPS: 2,
		},
	}, personalbrain.SolvencyDecision{State: personalbrain.SolvencyStateOK}, false, false)
	if gate.Status != personalbrain.EnterCannotEnter {
		t.Fatalf("missing proof must fail closed: %+v", gate)
	}
}

func TestValidateExclusionFromMetricProof(t *testing.T) {
	proof, err := exclusionmap.BuildProof(exclusionmap.EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event: residualmap.TriggeredEventEntry{
			EventID: "evt", Symbol: "BTC-USDT-SWAP", EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon: "1h", DecisionTime: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
		},
	}, exclusionmap.EvaluationResult{
		Accepted: true,
		Institution: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisInstitution, Excluded: true, PrimaryEvidence: "capacity_cliff",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"capacity_cliff": {Metric: "capacity_cliff_abs_profit", Passed: true, Value: 1, Threshold: 0.5},
			},
		},
		Retail: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisRetail, Excluded: true, PrimaryEvidence: "min_lot",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"min_lot_nonlinearity": {Metric: "pit_dynamic_min_notional_to_capital_ratio_30", Passed: true, Value: 0.33, Threshold: 0.2},
			},
		},
		Bot: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisBot, Excluded: true, PrimaryEvidence: "sparsity",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"event_sparsity": {Metric: "event_sparsity_score", Passed: true, Value: 1, Threshold: 0.8},
			},
		},
	}, "const")
	if err != nil {
		t.Fatal(err)
	}
	binding := personalplaybook.ProofBinding{
		ExclusionProofHash:  proof.ExclusionProofHash,
		InstitutionAxisHash: mustHashAxis(proof.Institution),
		RetailAxisHash:      mustHashAxis(proof.Retail),
		BotAxisHash:         mustHashAxis(proof.Bot),
	}
	if err := ValidateExclusionFromProof(binding, proof); err != nil {
		t.Fatalf("metric proof must validate: %v", err)
	}
}

func TestLoadExclusionProofsRequiresSealedHash(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"proofs":[{"schema_version":"` + exclusionmap.SchemaVersion + `","generation":"` + exclusionmap.Generation + `","event_id":"evt-1","accepted":true,"institution":{"axis":"institution","excluded":true,"primary_evidence":"x"},"retail":{"axis":"retail","excluded":true,"primary_evidence":"y"},"bot":{"axis":"bot","excluded":true,"primary_evidence":"z"}}]}`)
	if err := os.WriteFile(filepath.Join(dir, exclusionmap.OutputFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadExclusionProofs(dir); err == nil {
		t.Fatal("unsealed exclusion proof must fail validation")
	}
}

func proofBoundPlaybook(constHash, eventID, exclusionHash string) personalplaybook.PersonalWildPlaybook {
	narrative := personalplaybook.ExclusionNarrative{
		InstitutionExcludeReason: "capacity_cliff",
		RetailExcludeReason:      "min_lot_nonlinearity",
		BotExcludeReason:         "event_sparsity",
	}
	inst, retail, bot, _ := personalplaybook.BindExclusionAxisHashes(narrative)
	return personalplaybook.PersonalWildPlaybook{
		ProfileID: eventmap.ProfileOKX1H, EventFamily: eventmap.FamilyExtremeContinuation,
		Horizon: "1h", Direction: "long", DelayBars: 0, SourceEventID: eventID,
		ProofBinding: personalplaybook.ProofBinding{
			ConstitutionHash: constHash, ExclusionProofHash: exclusionHash,
			InstitutionAxisHash: inst, RetailAxisHash: retail, BotAxisHash: bot,
			EntryProofHash: "en", ExecutionPolicyHash: "exec", ExitProofHash: "exit",
		},
		ExclusionNarrative: narrative,
		EntryNarrative:     personalplaybook.EntryNarrative{DynamicMinNotionalUSDT: 5, ExecutableDepthUSDT: 500},
		RiskNarrative:      personalplaybook.RiskNarrative{MaxSizeUSDT: 200, MaxCapacityUSDT: 500},
		ExitNarrative:      personalplaybook.ExitNarrative{StopErrorRequired: true},
		StopNarrative:      personalplaybook.StopNarrative{CrowdingStopRule: "crowding_ok"},
		ExecutionNarrative: personalplaybook.ExecutionNarrative{MakerSelectionConclusion: "ok"},
		PublicTrigger: personalplaybook.PublicTriggerSpec{
			Conditions: []personalplaybook.PublicTriggerCondition{
				{Field: "event_family", Op: "eq", Note: eventmap.FamilyExtremeContinuation},
			},
		},
	}
}

func proofBoundFixtures(constHash, eventID string, decision time.Time) (personalplaybook.PersonalWildPlaybook, ProofBundle) {
	proofs := proofBoundBundle(eventID, decision)
	ex := proofs.ExclusionProofs[eventID]
	inst, _ := exclusionmap.HashAxis(ex.Institution)
	retail, _ := exclusionmap.HashAxis(ex.Retail)
	bot, _ := exclusionmap.HashAxis(ex.Bot)
	pb := personalplaybook.PersonalWildPlaybook{
		ProfileID: eventmap.ProfileOKX1H, EventFamily: eventmap.FamilyExtremeContinuation,
		Horizon: "1h", Direction: "long", DelayBars: 0, SourceEventID: eventID,
		ProofBinding: personalplaybook.ProofBinding{
			ConstitutionHash: constHash, ExclusionProofHash: ex.ExclusionProofHash,
			InstitutionAxisHash: inst, RetailAxisHash: retail, BotAxisHash: bot,
			EntryProofHash: proofs.EntryProofs[eventID].EntryProofHash,
			ExecutionPolicyHash: "exec", ExitProofHash: proofs.ExitProofs[eventID].ExitProofHash,
		},
		ExclusionNarrative: personalplaybook.ExclusionNarrative{
			InstitutionExcludeReason: "capacity_cliff",
			RetailExcludeReason:      "min_lot_nonlinearity",
			BotExcludeReason:         "event_sparsity",
		},
		EntryNarrative:     personalplaybook.EntryNarrative{DynamicMinNotionalUSDT: 5, ExecutableDepthUSDT: 500},
		RiskNarrative:      personalplaybook.RiskNarrative{MaxSizeUSDT: 200, MaxCapacityUSDT: 500},
		ExitNarrative:      personalplaybook.ExitNarrative{StopErrorRequired: true},
		StopNarrative:      personalplaybook.StopNarrative{CrowdingStopRule: "crowding_ok"},
		ExecutionNarrative: personalplaybook.ExecutionNarrative{MakerSelectionConclusion: "ok"},
		PublicTrigger: personalplaybook.PublicTriggerSpec{
			Conditions: []personalplaybook.PublicTriggerCondition{
				{Field: "event_family", Op: "eq", Note: eventmap.FamilyExtremeContinuation},
			},
		},
	}
	return pb, proofs
}

func proofBoundBundle(eventID string, decision time.Time) ProofBundle {
	ep, err := g10entryproof.BuildEntryProof(g10entryproof.TriggerRecord{
		TriggerID: eventID, DecisionTime: decision, Symbol: "BTC-USDT-SWAP", Venue: "okx",
		Side: "long", Price: 100, OrderType: "maker", DecisionVisibleInfo: "wal",
		MinSz: "1", LotSz: "0.01", CtVal: "0.01", CtType: "linear",
		ExecutableDepthUSDT: 5000, RealFill: true, MakerFilled: true, HasSettlement: true,
		ArtifactBinding: g10entryproof.ArtifactBinding{
			WALArtifactHash:        "a1b2c3d4e5f6789012345678abcdef01",
			SettlementArtifactHash: "b2c3d4e5f6789012345678abcdef0123",
			DepthArtifactHash:      "b2c3d4e5f6789012345678abcdef0123",
		},
	})
	if err != nil {
		panic(err)
	}
	exProof, err := exclusionmap.BuildProof(exclusionmap.EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event: residualmap.TriggeredEventEntry{
			EventID: eventID, Symbol: "BTC-USDT-SWAP", EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon: "1h", DecisionTime: decision,
		},
	}, exclusionmap.EvaluationResult{
		Accepted: true,
		Institution: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisInstitution, Excluded: true, PrimaryEvidence: "capacity_cliff",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"capacity_cliff": {Metric: "capacity_cliff_abs_profit", Passed: true, Value: 1, Threshold: 0.5},
			},
		},
		Retail: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisRetail, Excluded: true, PrimaryEvidence: "min_lot",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"min_lot_nonlinearity": {Metric: "pit_dynamic_min_notional_to_capital_ratio_30", Passed: true, Value: 0.33, Threshold: 0.2},
			},
		},
		Bot: exclusionmap.AxisExclusion{
			Axis: exclusionmap.AxisBot, Excluded: true, PrimaryEvidence: "sparsity",
			EvidenceBindings: map[string]exclusionmap.MetricBinding{
				"event_sparsity": {Metric: "event_sparsity_score", Passed: true, Value: 1, Threshold: 0.8},
			},
		},
	}, "const")
	if err != nil {
		panic(err)
	}
	fp := g10executionpolicy.FrozenPolicy{
		TriggerID: eventID, ExecutionPolicyHash: "exec", SelectedAction: g10executionpolicy.ActionConditionalMarketableLimit,
		MaxPriceCap: 103, MaxSlippageBPS: 30, MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
		SpreadBPS: 3, FeeBPS: 2, LatencyMS: 50, Side: "long", Price: 100,
		ActionsCompared: []g10executionpolicy.ActionEvaluation{
			{Action: g10executionpolicy.ActionConditionalMarketableLimit, ExpectedNetBPS: 50, ExpectedNetUSDT: 10, Allowed: true},
		},
	}
	xp, err := g10exitproof.BuildProof(g10exitproof.Input{
		TriggerID: eventID, DecisionTime: decision, ExitDepthUSDT: 5000,
		CanExitBeforeMaintenance: true, PartialCloseFeasible: true, PositionUSDT: 100,
	})
	if err != nil {
		panic(err)
	}
	return ProofBundle{
		EntryProofs:             map[string]g10entryproof.EntryProof{eventID: ep},
		ExecutionPolicies:       map[string]g10executionpolicy.FrozenPolicy{eventID: fp},
		ExitProofs:              map[string]g10exitproof.Proof{eventID: xp},
		ExclusionProofs:         map[string]exclusionmap.ExclusionProof{eventID: exProof},
	}
}

func TestExecutionPolicyCrossTriggerHashFallbackBlocked(t *testing.T) {
	eventA := "event-a"
	eventB := "event-b"
	fp := g10executionpolicy.FrozenPolicy{
		TriggerID: eventA, ExecutionPolicyHash: "shared",
		MaxPriceCap: 200, SelectedAction: g10executionpolicy.ActionConditionalMarketableLimit,
	}
	bundle := ProofBundle{
		ExecutionPolicies:       map[string]g10executionpolicy.FrozenPolicy{eventA: fp},
		ExecutionPoliciesByHash: map[string]g10executionpolicy.FrozenPolicy{"shared": fp},
	}
	if _, ok := bundle.executionPolicy(eventB); ok {
		t.Fatal("event B must not inherit event A policy via hash")
	}
	if !bundle.verifyExecutionPolicyHash(eventA, "shared") {
		t.Fatal("hash integrity check must pass for bound event")
	}
}

func mustHashAxis(axis exclusionmap.AxisExclusion) string {
	h, err := exclusionmap.HashAxis(axis)
	if err != nil {
		panic(err)
	}
	return h
}
