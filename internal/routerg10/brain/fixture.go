package brain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/playbook"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventmap"
)

const fixtureFileName = "brain_fixtures.json"

// BuildDefaultFixtures returns deterministic path-matrix scenarios for brain replay tests.
func BuildDefaultFixtures() FixtureBundle {
	cfg := defaultFixtureConfig()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	baseFeat := func(ret, prior float64, ood bool) BarFeatures {
		return BarFeatures{
			Symbol: "BTC-USDT-SWAP", ProfileID: eventmap.ProfileOKX1H,
			ReturnBPS: ret, PriorReturnBPS: prior, BTCRelativeBPS: 5,
			BarRangeBPS: 45, RollingVolumeUSDT: 5e5, Price: 100,
			OOD: ood,
		}
	}
	baseBar := func(idx int, feat BarFeatures, events []MatchedEvent) BarInput {
		return BarInput{
			BarIndex: idx, DecisionTime: now.Add(time.Duration(idx) * time.Hour),
			Features: feat, Events: events, Prices: map[string]float64{feat.Symbol: feat.Price},
			ScenarioDelay: 0, CostStress: 1,
			CostBase: residualmap.CostBase{
				ProfileID: feat.ProfileID, RollingQuoteVolumeUSDT: feat.RollingVolumeUSDT,
				BarHigh: feat.Price * 1.0005, BarLow: feat.Price * 0.9995, BarMid: feat.Price,
			},
		}
	}
	contEv := matchedContinuationEvent

	scenarios := []FixtureScenario{
		{
			Name: "data_distortion_no_trade", WantPath: "data_distortion_no_trade", WantAction: "NO_TRADE",
			Bars: []BarInput{
				baseBar(1, baseFeat(40, 30, true), []MatchedEvent{contEv(baseFeat(40, 30, true), 1)}),
			},
		},
		{
			Name: "continuation_long", WantPath: "continuation_long", WantAction: "LONG",
			Bars: []BarInput{
				baseBar(1, baseFeat(90, 80, false), []MatchedEvent{contEv(baseFeat(90, 80, false), 1)}),
			},
		},
		{
			Name: "failed_breakout_short", WantPath: "failed_breakout_short", WantAction: "SHORT",
			Bars: []BarInput{
				baseBar(1, baseFeat(-80, 70, false), []MatchedEvent{
					func() MatchedEvent {
						f := baseFeat(-80, 70, false)
						ev := contEv(f, 1)
						ev.GrossLongBPS = 10
						ev.GrossShortBPS = 75
						return ev
					}(),
				}),
			},
		},
		{
			Name: "negative_memory_no_trade", WantPath: "negative_memory_no_trade", WantAction: "NO_TRADE",
			WantReject: []string{counterfactualRejectMemory()},
			Bars: []BarInput{
				baseBar(1, baseFeat(40, 35, false), []MatchedEvent{contEv(baseFeat(40, 35, false), 1)}),
			},
		},
		{
			Name: "event_complete_close", WantPath: "event_complete_close", WantAction: "CLOSE",
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedEvent{completeEvent(1, 5)}),
				baseBar(2, baseFeat(10, 8, false), nil),
				baseBar(3, baseFeat(8, 6, false), nil),
				baseBar(4, baseFeat(6, 4, false), nil),
				baseBar(5, baseFeat(4, 2, false), nil),
			},
		},
		{
			Name: "solvency_trim", WantPath: "solvency_trim", WantAction: "TRIM",
			Bars: []BarInput{
				baseBar(1, baseFeat(5, 3, false), nil),
			},
		},
		{
			Name: "concurrency_full_reject", WantPath: "concurrency_full_reject", WantAction: "NO_TRADE",
			WantReject: []string{RejectConcurrencyFull},
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedEvent{contEv(baseFeat(50, 45, false), 1)}),
				baseBar(2, baseFeat(45, 40, false), []MatchedEvent{
					{Playbook: syntheticPlaybook(), EventFamily: eventmap.FamilyExtremeContinuation, EventID: "e2", GrossLongBPS: 40, GrossShortBPS: -20, CompletionBars: 4, DeadlineBar: 6},
				}),
			},
		},
		{
			Name: "below_min_notional_skip", WantPath: "below_min_notional_skip", WantAction: "NO_TRADE",
			WantReject: []string{RejectBelowMinNotional},
			Bars: []BarInput{
				baseBar(1, baseFeat(50, 45, false), []MatchedEvent{contEv(baseFeat(50, 45, false), 1)}),
			},
		},
	}
	for i := range scenarios {
		scenarios[i].SchemaVersion = SchemaVersionFixture
		scenarios[i].Config = cfg
		if scenarios[i].Name == "negative_memory_no_trade" {
			scenarios[i].Config.Memory = episodic.NewStore(negativeMemoryEpisodes())
		}
		if scenarios[i].Name == "below_min_notional_skip" {
			scenarios[i].Config.MinNotionalUSDT = 500
			scenarios[i].Config.InitialEquityUSDT = 30
		}
		if scenarios[i].Name == "concurrency_full_reject" {
			scenarios[i].Config.InitialEquityUSDT = 30
			scenarios[i].Config.MinNotionalUSDT = 1
		}
		if scenarios[i].Name == "solvency_trim" {
			scenarios[i].Config.InitialEquityUSDT = 30
			scenarios[i].Config.MinNotionalUSDT = 1
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

func counterfactualRejectMemory() string { return "MEMORY_NEGATIVE_MAJORITY_VETO" }

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

func defaultFixtureConfig() Config {
	fp := syntheticFingerprint()
	return Config{
		InitialEquityUSDT: 100,
		MinNotionalUSDT:   5,
		LotStep:           0.01,
		ContractValue:     1,
		ProtocolHash:      ProtocolHash,
		Fingerprint:       fp,
		PersonalAdj:       residualmap.LookupPersonalAdjustments(fp, "BTC-USDT-SWAP", "long"),
		Memory:            episodic.NewStore(positiveMemoryEpisodes()),
	}
}

func positiveMemoryEpisodes() []episodic.FrozenEpisode {
	var out []episodic.FrozenEpisode
	for i := 0; i < 6; i++ {
		out = append(out, episodic.FrozenEpisode{
			SchemaVersion: episodic.SchemaVersionEpisode,
			EpisodeHash:   "win-" + string(rune('a'+i)),
			EventID:       "win-" + string(rune('a'+i)),
			Symbol:        "BTC-USDT-SWAP",
			ProfileID:     eventmap.ProfileOKX1H,
			EventFamily:   eventmap.FamilyExtremeContinuation,
			EventPhase:    0.5,
			State:         episodic.StateVector{ReturnBPS: 50, PriorReturnBPS: 40, BarRangeBPS: 45},
			Outcome:       episodic.ConservativeOutcome{NetBPS: 8, Win: true},
		})
	}
	return out
}

func syntheticPlaybook() playbook.FrozenEventPlaybook {
	pb, _ := playbook.BuildPlaybook(playbook.ResidualWinnerGroup{
		ProfileID:    eventmap.ProfileOKX1H,
		ManifestHash: "fixture-manifest",
		CellKey: playbook.ResidualCellKey{
			EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon:     "1h", DelayBars: 0,
		},
		ShockCluster: "fixture",
		Winners: []playbook.ResidualWinnerEntry{
			{EventID: "w1", EligibleCapitalTiers: []string{"30", "100", "1000"}, MaxCapacityUSDT: 1000},
		},
	}, eventmap.FrozenThresholds{
		ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 20, ExtremeRMSBPS: 30,
	}, 1)
	return pb
}

func syntheticFingerprint() fingerprint.PersonalExecutionFingerprint {
	cell := knownExecutionCell("long")
	shortCell := knownExecutionCell("short")
	return fingerprint.PersonalExecutionFingerprint{
		SchemaVersion: fingerprint.SchemaVersion, Generation: fingerprint.Generation,
		FingerprintHash: "fixture-fingerprint-known-hash-v1",
		SourceStatus: map[string]fingerprint.SourceStatus{
			"maker_attempts":    {Status: fingerprint.StatusAvailable, Decoded: 10},
			"executable_shadow": {Status: fingerprint.StatusAvailable, Decoded: 10},
		},
		Cells: []fingerprint.ExecutionCell{cell, shortCell},
	}
}

func knownExecutionCell(direction string) fingerprint.ExecutionCell {
	return fingerprint.ExecutionCell{
		CellKey: fingerprint.CellKey{Symbol: "BTC-USDT", Direction: direction, OrderType: "maker"},
		FeeBPS:         fingerprint.QuantileMetric{P90: 1, N: 10, Status: fingerprint.StatusAvailable},
		EntrySlipBPS:   fingerprint.QuantileMetric{P90: 0.3, N: 10, Status: fingerprint.StatusAvailable},
		ExitSlipBPS:    fingerprint.QuantileMetric{P90: 0.2, N: 10, Status: fingerprint.StatusAvailable},
		LatencyMS:      fingerprint.QuantileMetric{P90: 120, N: 10, Status: fingerprint.StatusAvailable},
		MakerFillRate:  fingerprint.RateMetric{Rate: 0.8, N: 10, Status: fingerprint.StatusAvailable},
		SampleCount:    10,
		Instrument: fingerprint.KnownLinearInstrument("0.01", "1", "0.1", 100),
	}
}

func matchedContinuationEvent(feat BarFeatures, idx int) MatchedEvent {
	pb := syntheticPlaybook()
	evs := MatchPlaybooks([]playbook.FrozenEventPlaybook{pb}, feat, idx)
	if len(evs) == 0 {
		return MatchedEvent{
			Playbook: pb, EventFamily: pb.EventFamily, EventID: "fixture-cont",
			GrossLongBPS: 75, GrossShortBPS: -30, CompletionBars: 4, DeadlineBar: idx + 4,
		}
	}
	return evs[0]
}

func completeEvent(triggerBar, completeBar int) MatchedEvent {
	pb := syntheticPlaybook()
	return MatchedEvent{
		Playbook: pb, EventFamily: pb.EventFamily,
		EventID: "complete-event", EventPhase: 0.5, ThesisHealth: ThesisHealthy,
		GrossLongBPS: 45, GrossShortBPS: -20, CompletionBars: completeBar - triggerBar,
		DeadlineBar: completeBar + 4, CompleteBar: completeBar,
	}
}

func negativeMemoryEpisodes() []episodic.FrozenEpisode {
	var out []episodic.FrozenEpisode
	for i := 0; i < 8; i++ {
		out = append(out, episodic.FrozenEpisode{
			SchemaVersion: episodic.SchemaVersionEpisode,
			EpisodeHash:   "loss-" + string(rune('a'+i)),
			EventID:       "loss-" + string(rune('a'+i)),
			Symbol:        "BTC-USDT-SWAP",
			ProfileID:     eventmap.ProfileOKX1H,
			EventFamily:   eventmap.FamilyExtremeContinuation,
			EventPhase:    0.5,
			State:         episodic.StateVector{ReturnBPS: 40, PriorReturnBPS: 35, BarRangeBPS: 45},
			Outcome:       episodic.ConservativeOutcome{NetBPS: -5, Win: false},
		})
	}
	return out
}
