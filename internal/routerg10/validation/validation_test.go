package validation

import (
	"testing"

	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventaccount"
)

func TestEvaluateTierGatesSparseSampleInconclusive(t *testing.T) {
	replay := TierReplayStats{Count: 10, UniqueDays: 5, UniqueShockBlocks: 2}
	scenarios := []TierScenarioResult{{
		CapitalTier: 30, DelayBars: 0, CostStressMultiplier: 1.0,
		InitialEquityUSDT: 30, FinalEquityUSDT: 31, NetReturnNonNegative: true,
	}}
	status, checks, reason := EvaluateTierGates(replay, scenarios)
	if status != StatusInconclusive {
		t.Fatalf("expected INCONCLUSIVE, got %s", status)
	}
	if reason != ReasonCoverage {
		t.Fatalf("expected COVERAGE reason, got %s", reason)
	}
	if checks.SummaryNote != protocol.ValidationMinEventCountInconclusiveNote {
		t.Fatalf("expected inconclusive note")
	}
}

func TestMatchTriggerConditionsEmptyFails(t *testing.T) {
	features := FeatureSnapshot{ReturnBPS: 50, PriorReturnBPS: 40, RMSBPS: 100}
	spec := playbook.TriggerSpec{EventFamily: "test"}
	if MatchTriggerConditions(spec, features) {
		t.Fatal("empty conditions must not match")
	}
}

func TestCollapsePlaybookStatusPerTierPass(t *testing.T) {
	tiers := []TierResult{
		{CapitalTier: 30, Status: StatusValidationPass, FrozenConservativeMeanBPS: 2.5},
		{CapitalTier: 1000, Status: StatusValidationFail, FailReason: ReasonNoEdge},
	}
	status, _ := collapsePlaybookStatus(tiers)
	if status != StatusValidationPass {
		t.Fatalf("expected VALIDATION_PASS with partial tier pass, got %s", status)
	}
	eligible := eligibleTiersFromResults(tiers)
	if len(eligible) != 1 || eligible[0] != "30" {
		t.Fatalf("expected eligible [30], got %v", eligible)
	}
}

func TestFrozenSizingFromEventsUsesRealEdge(t *testing.T) {
	events := []ReplayEvent{
		{Direction: "long", ConservativeBPS: 3.0, StressBPS: 1.5},
		{Direction: "short", ConservativeBPS: 1.0, StressBPS: 0.5},
		{Direction: "flat", NoTrade: true},
	}
	sizing := FrozenSizingFromEvents(events, 30)
	if sizing.ConservativeMeanBPS != 2.0 {
		t.Fatalf("expected mean 2.0 bps, got %v", sizing.ConservativeMeanBPS)
	}
	if sizing.StressConservativeMeanBPS != 1.0 {
		t.Fatalf("expected mean stress 1.0 bps, got %v", sizing.StressConservativeMeanBPS)
	}
}

func TestFrozenSizingFromEventsNoFloorRollback(t *testing.T) {
	events := []ReplayEvent{{Direction: "long", ConservativeBPS: -1.0, StressBPS: 2.0}}
	sizing := FrozenSizingFromEvents(events, 30)
	if sizing.ConservativeMeanBPS != -1.0 {
		t.Fatalf("negative edge must not rollback to floor, got %v", sizing.ConservativeMeanBPS)
	}
}

func TestPickBrainDirectionRespectsSolvency(t *testing.T) {
	pb := playbook.FrozenEventPlaybook{EventFamily: "ohlcv_extreme_reversal", ProfileID: "p1"}
	feat := FeatureSnapshot{ReturnBPS: 50, PriorReturnBPS: 10, RMSBPS: 100}
	bar := toBarFeatures("BTC-USDT-SWAP", "p1", feat, 100)
	solvent := PickBrainDirection(pb, feat, bar, fingerprint.PersonalExecutionFingerprint{}, residualmap.CostBase{}, InstrumentConstraints{}, 100, 5, 3, 2, 1, true)
	insolvent := PickBrainDirection(pb, feat, bar, fingerprint.PersonalExecutionFingerprint{}, residualmap.CostBase{}, InstrumentConstraints{}, 100, 5, 3, 2, 1, false)
	if solvent.NoTrade && !insolvent.NoTrade {
		t.Fatalf("solvent=%+v insolvent=%+v", solvent, insolvent)
	}
}

func TestDirectionFromActionFlat(t *testing.T) {
	if directionFromAction("NO_TRADE") != "flat" {
		t.Fatal("NO_TRADE must map to flat")
	}
}

func TestCheckBrainDegeneracyEmptyEvents(t *testing.T) {
	summary, err := CheckBrainDegeneracy(nil, fingerprint.PersonalExecutionFingerprint{}, "okx_1h")
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Degenerate {
		t.Fatal("empty events must be degenerate")
	}
}

func TestBuildPlaybookResultsFailsOnDegeneracy(t *testing.T) {
	pb := playbook.FrozenEventPlaybook{PlaybookHash: "pb1", ProfileID: "okx_1h"}
	tiers := map[string][]TierResult{
		"pb1": {{CapitalTier: 30, Status: StatusValidationPass, FrozenConservativeMeanBPS: 2}},
	}
	deg := map[string]DegeneracySummary{"pb1": {Degenerate: true, Status: ReasonBrainDegeneracy}}
	results := BuildPlaybookResults([]playbook.FrozenEventPlaybook{pb}, tiers, playbook.ExperimentLedger{}, deg)
	if len(results) != 1 || results[0].Status != StatusValidationFail || results[0].FailReason != ReasonBrainDegeneracy {
		t.Fatalf("expected degeneracy fail, got %+v", results[0])
	}
}

func TestBenjaminiHochbergFDRConservativeFamilyDenominator(t *testing.T) {
	const family = 64
	pass, err := benjaminiHochbergFDRWithFamilyDenominator([]float64{0.001}, family, DiscoveryScreenFDRQ)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass) != 1 || !pass[0] {
		t.Fatalf("strong tested hypothesis must pass conservative denominator: %v", pass)
	}
	weak, err := benjaminiHochbergFDRWithFamilyDenominator([]float64{0.05}, family, DiscoveryScreenFDRQ)
	if err != nil {
		t.Fatal(err)
	}
	if len(weak) != 1 || weak[0] {
		t.Fatalf("weak tested hypothesis must fail under family=%d: %v", family, weak)
	}
}

func TestBHOrphanAttemptsUseConservativeDenominator(t *testing.T) {
	const family = 64
	pb := playbook.FrozenEventPlaybook{PlaybookHash: "pb1", ProfileID: "okx_1h"}
	tiers := map[string][]TierResult{
		"pb1": {{
			CapitalTier: 30, Status: StatusValidationPass, FrozenConservativeMeanBPS: 2,
			Replay: TierReplayStats{
				Count: 50, UniqueDays: 20, UniqueShockBlocks: 20,
				NonzeroShockBlocks: 20, PositiveShockBlocks: 20,
				MaxPositiveContributionFraction: 0.1, MaxDayPositiveContributionFraction: 0.1,
			},
		}},
	}
	ledger := playbook.ExperimentLedger{Attempts: make([]playbook.ExperimentAttempt, family)}
	for i := range ledger.Attempts {
		ledger.Attempts[i] = playbook.ExperimentAttempt{AttemptOrdinal: i + 1, Status: "DUPLICATE"}
	}
	ledger.Attempts[0] = playbook.ExperimentAttempt{
		AttemptOrdinal: 1, PlaybookHash: pb.PlaybookHash, Status: "UNEXPLAINED",
	}
	results := BuildPlaybookResults([]playbook.FrozenEventPlaybook{pb}, tiers, ledger, nil)
	if !results[0].BHPass || results[0].Status != StatusValidationPass {
		t.Fatalf("strong signal did not pass conservative ledger-family BH: %+v", results[0])
	}
	if results[0].SignP >= DiscoveryScreenFDRQ/float64(family) {
		t.Fatalf("strong p=%g does not establish ledger-family denominator", results[0].SignP)
	}
}

func TestAttributeBarDeltaSingleEvent(t *testing.T) {
	daily := DailyEquityReplay{
		DailyPnL: map[string]float64{}, ShockDayPnL: map[string]float64{},
		EventPnL: map[string]float64{}, SymbolPnL: map[string]float64{},
	}
	before := map[string]slotSnapshot{"e1": {eventID: "e1", symbol: "BTC", notional: 10}}
	after := map[string]slotSnapshot{"e1": {eventID: "e1", symbol: "BTC", notional: 10, closed: true}}
	candidates := []eventaccount.EventCandidate{{EventID: "e1", Symbol: "BTC", ReferenceNotional: 10}}
	events := []ReplayEvent{{EventID: "e1", Symbol: "BTC", ShockCluster: "shock-a"}}
	attributeBarDelta(5.0, before, after, candidates, events, &daily, "2024-01-01")
	if daily.EventPnL["e1"] != 5.0 {
		t.Fatalf("expected full delta on single closed event, got %v", daily.EventPnL["e1"])
	}
}
