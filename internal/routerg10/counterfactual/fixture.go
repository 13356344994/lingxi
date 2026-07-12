package counterfactual

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/fingerprint"
	"lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventmap"
)

const fixtureFileName = "counterfactual_fixtures.json"

// FixtureBundle is the frozen counterfactual path-matrix fixture set.
type FixtureBundle struct {
	SchemaVersion string            `json:"schema_version"`
	Generation    string            `json:"generation"`
	ProtocolHash  string            `json:"protocol_hash"`
	Scenarios     []FixtureScenario `json:"scenarios"`
	BundleHash    string            `json:"bundle_hash"`
}

// BuildDefaultFixtures returns deterministic path-matrix scenarios for tests and CLI.
func BuildDefaultFixtures() FixtureBundle {
	proto := protocol.RouterProtocolHash
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	baseCost := residualmap.CostBase{
		RollingQuoteVolumeUSDT: 5e5,
		BarHigh:                100.05,
		BarLow:                 99.95,
		BarMid:                 100,
		FundingBPS:             0.5,
		ProfileID:              eventmap.ProfileOKX1H,
		Horizon:                "1h",
		DelayBars:              0,
	}
	fp := syntheticFingerprint()
	adj := residualmap.LookupPersonalAdjustments(fp, "BTC-USDT-SWAP", "long")

	scenarios := []FixtureScenario{
		{
			Name:       "continuation_long",
			Context:    baseContext(now, baseCost, fp, adj, eventContext(true, 75, -40), false),
			WantAction: "LONG",
		},
		{
			Name:       "failed_breakout_short",
			Context:    baseContext(now, baseCost, fp, adj, eventContext(true, -35, 75), false),
			WantAction: "SHORT",
		},
		{
			Name:       "data_distortion_no_trade",
			Context:    withOOD(baseContext(now, baseCost, fp, adj, eventContext(true, 30, -20), false)),
			WantAction: "NO_TRADE",
			WantReject: []string{RejectOOD},
		},
		{
			Name:       "negative_memory_no_trade",
			Context:    withMemoryVeto(baseContext(now, baseCost, fp, adj, eventContext(true, 25, -15), false)),
			WantAction: "NO_TRADE",
			WantReject: []string{RejectMemoryVeto},
		},
		{
			Name:       "breakeven_fail_no_trade",
			Context:    baseContext(now, baseCost, fp, adj, eventContext(true, 5, -3), false),
			WantAction: "NO_TRADE",
			WantReject: []string{RejectBreakevenFail},
		},
		{
			Name:       "stress_insolvent_no_trade",
			Context:    withInsolvent(baseContext(now, baseCost, fp, adj, eventContext(true, 8, -6), false)),
			WantAction: "NO_TRADE",
			WantReject: []string{RejectStressInsolvent},
		},
		{
			Name:       "min_lot_skip",
			Context:    withMinNotional(baseContext(now, baseCost, fp, adj, eventContext(true, 40, -30), false), 500),
			WantAction: "NO_TRADE",
			WantReject: []string{RejectMinLotInfeasible},
		},
		{
			Name:       "delay_stress",
			Context:    withDelay(baseContext(now, baseCost, fp, adj, eventContext(true, 75, -25), false), 2),
			WantAction: "LONG",
		},
		{
			Name:       "no_event_hold_only",
			Context:    withPosition(baseContext(now, baseCost, fp, adj, eventContext(false, 0, 0), false), "long", 80),
			WantAction: "HOLD",
		},
		{
			Name:       "no_event_force_open_reject",
			Context:    baseContext(now, baseCost, fp, adj, eventContext(false, 20, -10), false),
			WantAction: "NO_TRADE",
		},
	}
	for i := range scenarios {
		scenarios[i].SchemaVersion = SchemaVersionFixture
	}
	bundle := FixtureBundle{
		SchemaVersion: SchemaVersionFixture,
		Generation:    Generation,
		ProtocolHash:  proto,
		Scenarios:     scenarios,
	}
	hash, _ := HashFixtureBundle(bundle)
	bundle.BundleHash = hash
	return bundle
}

// WriteFixtures writes the default fixture bundle to outputDir.
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

// LoadFixtures reads a fixture bundle from disk.
func LoadFixtures(path string) (FixtureBundle, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FixtureBundle{}, err
	}
	var bundle FixtureBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return FixtureBundle{}, err
	}
	return bundle, nil
}

// HashFixtureBundle returns canonical hash for one fixture bundle.
func HashFixtureBundle(b FixtureBundle) (string, error) {
	payload := b
	payload.BundleHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func baseContext(
	at time.Time,
	cost residualmap.CostBase,
	fp fingerprint.PersonalExecutionFingerprint,
	adj residualmap.PersonalAdjustments,
	ev EventContext,
	ood bool,
) DecisionContext {
	return DecisionContext{
		Symbol:       "BTC-USDT-SWAP",
		ProfileID:    eventmap.ProfileOKX1H,
		DecisionTime: at,
		EquityUSDT:   100,
		Event:        ev,
		Position: PositionContext{
			MaxConcurrent: protocol.MaxConcurrent100U,
		},
		Query: episodic.QueryVector{
			Symbol:      "BTC-USDT-SWAP",
			ProfileID:   eventmap.ProfileOKX1H,
			EventFamily: eventmap.FamilyExtremeContinuation,
			EventPhase:  0.5,
			State: episodic.StateVector{
				ReturnBPS:         50,
				PriorReturnBPS:    10,
				BTCRelativeBPS:    5,
				BarRangeBPS:       40,
				RollingVolumeUSDT: 5e5,
			},
			PersonalExec: episodic.PersonalExecSnapshot{
				FeeDeltaBPS: 1, SlipDeltaBPS: 0.5, LatencyP90MS: 120, FingerprintKnown: true,
			},
		},
		Memory: episodic.MemoryAdvice{
			SchemaVersion: episodic.SchemaVersionAdvice,
			SizeScale:     1,
			AdviceCodes:   []string{episodic.AdviceNoVeto},
			HitCount:      4,
			WinCount:      3,
			LossCount:     1,
		},
		Fingerprint:     fp,
		CostBase:        cost,
		PersonalAdj:     adj,
		MinNotionalUSDT: 5,
		LotStep:         0.01,
		ContractValue:   1,
		OOD:             ood,
		Solvent:         true,
		StressBudgetBPS: protocol.MaxEventStressLossFraction * 10000,
		CompletionBars:  4,
		CapacityUSDT:    1000,
	}
}

func eventContext(has bool, longGross, shortGross float64) EventContext {
	return EventContext{
		HasEvent:      has,
		EventFamily:   eventmap.FamilyExtremeContinuation,
		PlaybookHash:  "playbook-test",
		Horizon:       "1h",
		GrossLongBPS:  longGross,
		GrossShortBPS: shortGross,
		DelayBars:     0,
	}
}

func withOOD(ctx DecisionContext) DecisionContext {
	ctx.OOD = true
	return ctx
}

func withInsolvent(ctx DecisionContext) DecisionContext {
	ctx.Solvent = false
	return ctx
}

func withMemoryVeto(ctx DecisionContext) DecisionContext {
	ctx.Memory.VetoOpen = true
	ctx.Memory.SizeScale = 0
	ctx.Memory.AdviceCodes = []string{episodic.AdviceVetoOpen}
	ctx.Memory.HitCount = 8
	ctx.Memory.LossCount = 6
	ctx.Memory.WinCount = 2
	return ctx
}

func withMinNotional(ctx DecisionContext, min float64) DecisionContext {
	ctx.MinNotionalUSDT = min
	ctx.EquityUSDT = 30
	return ctx
}

func withDelay(ctx DecisionContext, delay int) DecisionContext {
	ctx.Event.DelayBars = delay
	ctx.CostBase.DelayBars = delay
	return ctx
}

func withPosition(ctx DecisionContext, dir string, notional float64) DecisionContext {
	ctx.Position.HasPosition = true
	ctx.Position.Direction = dir
	ctx.Position.NotionalUSDT = notional
	return ctx
}

func syntheticFingerprint() fingerprint.PersonalExecutionFingerprint {
	cell := knownExecutionCell("long")
	shortCell := knownExecutionCell("short")
	return fingerprint.PersonalExecutionFingerprint{
		SchemaVersion:   fingerprint.SchemaVersion,
		Generation:      fingerprint.Generation,
		FingerprintHash: "test-fingerprint-known-hash-v1",
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
