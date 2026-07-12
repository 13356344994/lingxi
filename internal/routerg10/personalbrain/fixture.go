package personalbrain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10executionpolicy "lingxi/internal/routerg10/executionpolicy"
	"lingxi/internal/routerg10/announcements"
	"lingxi/internal/routerg10/personalplaybook"
	"lingxi/internal/routerg6/eventmap"
)

const fixtureFileName = "personal_brain_fixtures.json"

// BuildDefaultFixtures returns deterministic G10P path-matrix scenarios.
func BuildDefaultFixtures() FixtureBundle {
	constHash, _ := g10constitution.Hash()
	cfg := defaultFixtureConfig(constHash)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	baseFeat := func(ret, prior float64, ood bool) BarFeatures {
		return BarFeatures{
			Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
			ReturnBPS: ret, PriorReturnBPS: prior, BTCRelativeBPS: 5,
			BarRangeBPS: 45, RollingVolumeUSDT: 5e5, Price: 100,
			OOD: ood,
		}
	}
	baseBar := func(idx int, feat BarFeatures, events []MatchedPersonalEvent, sim ExecutionSimState, anns []announcements.Announcement) BarInput {
		return BarInput{
			BarIndex: idx, DecisionTime: now.Add(time.Duration(idx) * time.Hour),
			Features: feat, Events: events,
			Announcements: anns,
			Prices: map[string]float64{feat.Symbol: feat.Price},
			ScenarioDelay: 0, CostStress: 1,
			ExecutionSim: sim,
		}
	}
	pitAnns := []announcements.Announcement{
		{
			ID: "ann-okx-1", Venue: announcements.VenueOKX, Symbol: "BTC-USDT-SWAP",
			Kind: announcements.KindListing,
			PublishedAt: now.Add(-2 * time.Hour), FirstSeenAt: now.Add(-2 * time.Hour),
			EffectiveAt: now.Add(-1 * time.Hour), SourceHash: "pit-hash-1",
			ExecutionVenue: "okx", InfoOnly: true,
		},
	}

	scenarios := []FixtureScenario{
		{
			Name: "can_enter_bounded_maker_long", WantPath: "can_enter_bounded_maker_long",
			WantEnterGate: EnterCanEnter, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(90, 80, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(90, 80, false), 1, g10executionpolicy.ActionBoundedMaker, constHash),
				}, defaultExecSim(), pitAnns),
			},
		},
		{
			Name: "cannot_enter_unproven", WantPath: "cannot_enter_unproven",
			WantEnterGate: EnterCannotEnter, WantAction: "NO_TRADE",
			WantReject: []string{"PLAYBOOK_NOT_CERTIFIED"},
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					unprovenEvent(baseFeat(50, 45, false), 1, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "missed_correctly_maker_timeout", WantPath: "missed_correctly_maker_timeout",
			WantEnterGate: EnterMissedCorrectly, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(60, 55, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(60, 55, false), 1, g10executionpolicy.ActionBoundedMaker, constHash),
				}, ExecutionSimState{
					MakerTouchedNotFilled: true, SubsequentlyProfitable: true, ThesisStillValid: true,
					MakerTimeoutBars: 1,
				}, nil),
			},
		},
		{
			Name: "conditional_marketable_limit_long", WantPath: "conditional_marketable_limit_long",
			WantEnterGate: EnterCanEnter, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(85, 75, false), []MatchedPersonalEvent{
					certifiedEventWithOptions(baseFeat(85, 75, false), 1, g10executionpolicy.ActionConditionalMarketableLimit, constHash, certifiedEventOpts{makerForbid: true}),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "execution_skip", WantPath: "execution_skip",
			WantEnterGate: EnterCanEnter, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(40, 35, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(40, 35, false), 1, g10executionpolicy.ActionSkip, constHash),
				}, ExecutionSimState{
					ThesisStillValid: true, RemainingEdgeBPS: 3, SpreadBPS: 3, FeeBPS: 2,
					MinRemainingEdgeBPS: 5, TimeoutMS: 3000,
				}, nil),
			},
		},
		{
			Name: "toxic_fill_revalidate", WantPath: "toxic_fill_revalidate",
			WantAction: "HOLD",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(50, 45, false), 1, g10executionpolicy.ActionBoundedMaker, constHash),
				}, ExecutionSimState{
					ToxicFastFullFill: true, ThesisStillValid: true,
				}, nil),
			},
		},
		{
			Name: "toxic_fill_exit", WantPath: "toxic_fill_exit",
			WantAction: "CLOSE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(50, 45, false), 1, g10executionpolicy.ActionBoundedMaker, constHash),
				}, ExecutionSimState{
					ToxicThroughPrice: true, ThesisStillValid: false,
				}, nil),
			},
		},
		{
			Name: "thesis_invalid_exit", WantPath: "thesis_invalid_exit",
			WantAction: "CLOSE",
			Bars: []BarInput{
				baseBar(2, baseFeat(-80, 70, false), []MatchedPersonalEvent{
					invalidatedEvent(baseFeat(-80, 70, false), 2, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "solvency_trim_independent", WantPath: "solvency_trim_independent",
			WantAction: "TRIM",
			Bars: []BarInput{
				baseBar(1, baseFeat(5, 3, false), nil, defaultExecSim(), nil),
			},
		},
		{
			Name: "deadline_stop_error_exit", WantPath: "deadline_stop_error_exit",
			WantAction: "CLOSE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					deadlineEvent(1, 3, constHash),
				}, defaultExecSim(), nil),
				baseBar(2, baseFeat(10, 8, false), nil, defaultExecSim(), nil),
				baseBar(3, baseFeat(8, 6, false), nil, defaultExecSim(), nil),
			},
		},
		{
			Name: "crowding_stop", WantPath: "crowding_stop",
			WantEnterGate: EnterCannotEnter, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					crowdingFailEvent(baseFeat(50, 45, false), 1, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "capital_expansion_stop", WantPath: "capital_expansion_stop",
			WantEnterGate: EnterCannotEnter, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					capitalStopEvent(baseFeat(50, 45, false), 1, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "terminal_risk_sizing_down", WantPath: "terminal_risk_sizing_down",
			WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					terminalCeilingEvent(baseFeat(50, 45, false), 1, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "announcement_pit_visible", WantPath: "announcement_pit_visible",
			WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(70, 65, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(70, 65, false), 1, g10executionpolicy.ActionSkip, constHash),
				}, defaultExecSim(), pitAnns),
			},
		},
		{
			Name: "below_min_notional_skip", WantPath: "below_min_notional_skip",
			WantAction: "NO_TRADE",
			WantReject: []string{RejectBelowMinNotional},
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedPersonalEvent{
					highMinNotionalEvent(baseFeat(50, 45, false), 1, constHash),
				}, defaultExecSim(), nil),
			},
		},
		{
			Name: "no_chase_after_miss", WantPath: "no_chase_after_miss",
			WantEnterGate: EnterMissedCorrectly, WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(55, 50, false), []MatchedPersonalEvent{
					certifiedEvent(baseFeat(55, 50, false), 1, g10executionpolicy.ActionBoundedMaker, constHash),
				}, ExecutionSimState{
					MakerTouchedNotFilled: true, SubsequentlyProfitable: true,
					ThesisStillValid: true, RemainingEdgeBPS: 30,
				}, nil),
			},
		},
	}

	for i := range scenarios {
		scenarios[i].SchemaVersion = SchemaVersionFixture
		scenarios[i].Config = cfg
		if scenarios[i].Name == "below_min_notional_skip" {
			scenarios[i].Config.InitialEquityUSDT = 30
			scenarios[i].Config.MinNotionalUSDT = 500
		}
		if scenarios[i].Name == "solvency_trim_independent" {
			scenarios[i].Config.InitialEquityUSDT = 30
			scenarios[i].Config.MinNotionalUSDT = 1
		}
		if scenarios[i].Name == "capital_expansion_stop" {
			scenarios[i].Bars[0].CapitalTierUSDT = 5000
		}
	}

	bundle := FixtureBundle{
		SchemaVersion: SchemaVersionFixture,
		Generation:    Generation,
		ProtocolHash:  ProtocolHash,
		Scenarios:     scenarios,
	}
	hash, _ := HashFixtureBundle(bundle)
	bundle.BundleHash = hash
	return bundle
}

// WriteFixtures writes default fixtures to outputDir.
func WriteFixtures(outputDir string) (FixtureBundle, error) {
	bundle := BuildDefaultFixtures()
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return FixtureBundle{}, err
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return FixtureBundle{}, err
	}
	path := filepath.Join(outputDir, fixtureFileName)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return FixtureBundle{}, err
	}
	return bundle, nil
}

// HashFixtureBundle returns canonical bundle hash.
func HashFixtureBundle(b FixtureBundle) (string, error) {
	payload := b
	payload.BundleHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func defaultFixtureConfig(constHash string) Config {
	return Config{
		InitialEquityUSDT: 100,
		ConstitutionHash:  constHash,
		MinNotionalUSDT:   5,
		LotStep:           0.01,
		ContractValue:     1,
		ProtocolHash:      ProtocolHash,
		TradeAllowed:      TradeAllowedDefault,
	}
}

func defaultExecSim() ExecutionSimState {
	return ExecutionSimState{
		ThesisStillValid:    true,
		RemainingEdgeBPS:    25,
		SpreadBPS:           3,
		FeeBPS:              2,
		LatencyMS:           45,
		MaxPriceCap:         105,
		MaxSlippageBPS:      10,
		MinRemainingEdgeBPS: 5,
		TimeoutMS:           3000,
	}
}

func syntheticPlaybook(constHash, execAction string, certified bool, makerForbid bool) personalplaybook.PersonalWildPlaybook {
	narrative := personalplaybook.ExclusionNarrative{
		InstitutionExcludeReason: "capacity_cliff",
		RetailExcludeReason:      "min_lot_nonlinearity",
		BotExcludeReason:         "event_sparsity",
	}
	instHash, retailHash, botHash, _ := personalplaybook.BindExclusionAxisHashes(narrative)
	makerConclusion := "ok"
	if makerForbid {
		makerConclusion = "adverse_selection_detected: forbid_maker"
	}
	pb := personalplaybook.PersonalWildPlaybook{
		SchemaVersion: personalplaybook.SchemaVersionPlaybook,
		Generation:    Generation,
		ProtocolHash:  ProtocolHash,
		PlaybookHash:  "fixture-playbook-hash",
		Symbol:        "BTC-USDT-SWAP",
		ProfileID:     eventmap.ProfileOKX1H,
		EventFamily:   eventmap.FamilyExtremeContinuation,
		Horizon:       "1h",
		Direction:     "long",
		DelayBars:     0,
		Certified:     certified,
		TradeAllowed:  TradeAllowedDefault,
		ProofBinding: personalplaybook.ProofBinding{
			ConstitutionHash:     constHash,
			ExclusionProofHash:   "fixture-exclusion-hash",
			InstitutionAxisHash:  instHash,
			RetailAxisHash:       retailHash,
			BotAxisHash:          botHash,
			EntryProofHash:       "fixture-entry-hash",
			ExecutionPolicyHash:  "fixture-exec-hash",
			ExitProofHash:        "fixture-exit-hash",
			AnnouncementTapeHash: "fixture-ann-hash",
		},
		ExclusionNarrative: narrative,
		EntryNarrative: personalplaybook.EntryNarrative{
			WhyUserCanEnter:        "fixture",
			DynamicMinNotionalUSDT: 5,
			ExecutableDepthUSDT:    500,
		},
		ExecutionNarrative: personalplaybook.ExecutionNarrative{
			MakerSelectionConclusion: makerConclusion,
			AllowedActions:           ExecutionActionsCompared,
			FrozenExecutionAction:    execAction,
			NoChaseAfterMiss:         true,
			NoLongLivedMaker:         true,
		},
		RiskNarrative: personalplaybook.RiskNarrative{
			MaxSizeUSDT:                   200,
			MaxCapacityUSDT:               500,
			EventTerminalBudgetFraction:   MaxEventTerminalLossFraction,
			AccountTerminalBudgetFraction: MaxAccountTerminalRiskFraction,
			RiskCeilingIsNotSizingTarget:  true,
		},
		ExitNarrative: personalplaybook.ExitNarrative{
			ExitRule:          "thesis_or_deadline",
			StopErrorRequired: true,
		},
		StopNarrative: personalplaybook.StopNarrative{
			CrowdingStopRule:         "crowding_ok",
			CapitalExpansionStopRule: "exceeds_certified_tier",
		},
		PublicTrigger: personalplaybook.PublicTriggerSpec{
			Conditions: []personalplaybook.PublicTriggerCondition{
				{Field: "abs_return_bps", Op: "gte", Value: 20},
			},
		},
	}
	return pb
}

func certifiedEvent(feat BarFeatures, idx int, execAction, constHash string) MatchedPersonalEvent {
	return certifiedEventWithOptions(feat, idx, execAction, constHash, certifiedEventOpts{})
}

type certifiedEventOpts struct {
	makerForbid bool
}

func certifiedEventWithOptions(feat BarFeatures, idx int, execAction, constHash string, opts certifiedEventOpts) MatchedPersonalEvent {
	pb := syntheticPlaybook(constHash, execAction, true, opts.makerForbid)
	evs := MatchPersonalPlaybooks([]personalplaybook.PersonalWildPlaybook{pb}, feat, idx)
	if len(evs) == 0 {
		return MatchedPersonalEvent{
			Playbook: pb, Proofs: proofsFromPlaybook(pb),
			EventID: "fixture-cert", EventFamily: pb.EventFamily,
			Direction: "long", ThesisHealth: ThesisHealthy,
			GrossLongBPS: 75, GrossShortBPS: -30, CompletionBars: 4, DeadlineBar: idx + 4,
			CapacityUSDT: 500, MaxSizeUSDT: 200,
		}
	}
	return evs[0]
}

func unprovenEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	pb := syntheticPlaybook(constHash, ActionSkip, false, false)
	ev := certifiedEvent(feat, idx, ActionSkip, constHash)
	ev.Playbook = pb
	ev.Proofs = proofsFromPlaybook(pb)
	ev.Proofs.EntryCertified = false
	ev.Proofs.EntryGrade = "C"
	return ev
}

func invalidatedEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionSkip, constHash)
	ev.ThesisHealth = ThesisInvalidated
	ev.InvalidatorBar = idx
	return ev
}

func deadlineEvent(triggerBar, deadlineBar int, constHash string) MatchedPersonalEvent {
	feat := BarFeatures{
		Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
		ReturnBPS: 50, PriorReturnBPS: 45, BarRangeBPS: 45, Price: 100,
	}
	ev := certifiedEvent(feat, triggerBar, ActionBoundedMaker, constHash)
	ev.DeadlineBar = deadlineBar
	ev.CompleteBar = deadlineBar
	return ev
}

func crowdingFailEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionBoundedMaker, constHash)
	ev.Playbook.StopNarrative.CrowdingStopRule = "crowding_failed"
	ev.Proofs.CrowdingPassed = false
	return ev
}

func capitalStopEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionBoundedMaker, constHash)
	ev.Playbook.RiskNarrative.MaxSizeUSDT = 50
	return ev
}

func terminalCeilingEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionBoundedMaker, constHash)
	ev.GrossLongBPS = 50000
	ev.GrossShortBPS = -50000
	return ev
}

func tinyCapacityEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionBoundedMaker, constHash)
	ev.CapacityUSDT = 0.5
	ev.MaxSizeUSDT = 0.5
	ev.Proofs.DynamicMinNotional = 5
	return ev
}

func highMinNotionalEvent(feat BarFeatures, idx int, constHash string) MatchedPersonalEvent {
	ev := certifiedEvent(feat, idx, ActionBoundedMaker, constHash)
	ev.Proofs.DynamicMinNotional = 500
	return ev
}
