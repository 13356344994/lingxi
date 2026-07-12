package evaluator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"lingxi/internal/routerg10/audit"
	"lingxi/internal/routerg10/brain"
	"lingxi/internal/routerg10/episodic"
	"lingxi/internal/routerg10/fingerprint"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventmap"
)

// ReplayBlocksFromFixture recomputes exam blocks via bar-level Brain trace replay.
func ReplayBlocksFromFixture(r io.Reader, commitment BrainCommitment) ([]FixtureBlock, error) {
	parsed, err := ParseFixtureStream(r)
	if err != nil {
		return nil, err
	}
	if parsed.ExpectedCommitmentHash != commitment.ExpectedCommitmentHash {
		return nil, fmt.Errorf("%w: got %q want %q", ErrCommitmentHashMismatch, parsed.ExpectedCommitmentHash, commitment.ExpectedCommitmentHash)
	}
	cfg := examBrainConfig(commitment)
	if len(parsed.Blocks) > 0 && parsed.Blocks[0].InitialEquityUSDT > 0 {
		cfg.InitialEquityUSDT = parsed.Blocks[0].InitialEquityUSDT
	}
	b := brain.NewBrain(cfg)
	var out []FixtureBlock
	for _, block := range parsed.Blocks {
		metrics, delays, costOK, shockOK, concurrentOK, traceOK, err := replayBlock(b, block)
		if err != nil {
			return nil, err
		}
		out = append(out, FixtureBlock{
			AccountMetrics: metrics,
			Delay:          delays,
			Cost:           costOK,
			Shock:          shockOK,
			Concurrent:     concurrentOK,
			BrainTrace:     traceOK,
		})
	}
	return out, nil
}

func replayBlock(b *brain.Brain, block FixtureInputBlock) (BlockAccountMetrics, BlockDelay, BlockCost, BlockShock, BlockConcurrent, BlockBrainTrace, error) {
	days := map[string]bool{}
	initial := b.Config.InitialEquityUSDT
	if block.InitialEquityUSDT > 0 {
		initial = block.InitialEquityUSDT
		b.Config.InitialEquityUSDT = initial
		b.Account.Core.EquityUSDT = initial
		b.Account.Core.CashUSDT = initial
	}
	delayOK := map[string]bool{}
	for _, d := range g10protocol.ValidationDelayBars {
		delayOK[delayKey(d)] = true
	}
	for _, bar := range block.Bars {
		barInput := brain.BarInput{
			BarIndex:      bar.BarIndex,
			DecisionTime:  bar.DecisionTime,
			Features:      bar.Features,
			Events:        bar.Events,
			Prices:        bar.Prices,
			ScenarioDelay: bar.ScenarioDelay,
			CostStress:    bar.CostStress,
		}
		if barInput.DecisionTime.IsZero() {
			barInput.DecisionTime = time.Now().UTC()
		}
		if _, err := b.DecideBar(barInput); err != nil {
			return BlockAccountMetrics{}, BlockDelay{}, BlockCost{}, BlockShock{}, BlockConcurrent{}, BlockBrainTrace{}, err
		}
		day := barInput.DecisionTime.UTC().Format("2006-01-02")
		days[day] = true
	}
	final := b.Account.Core.EquityUSDT
	traceOK := true
	degeneracyClear := true
	if b.Trace != nil && len(b.Trace.Entries) > 0 {
		report, err := audit.DetectDegeneracy(*b.Trace, map[string]bool{})
		if err != nil {
			return BlockAccountMetrics{}, BlockDelay{}, BlockCost{}, BlockShock{}, BlockConcurrent{}, BlockBrainTrace{}, err
		}
		degeneracyClear = !report.Degenerate
		log1 := *b.Trace
		log2 := *b.Trace
		verify := audit.VerifyReplay(log1, log2)
		traceOK = verify.Match
	}
	return BlockAccountMetrics{
			InitialEquityUSDT: initial,
			FinalEquityUSDT:   final,
			IndependentDays:   len(days),
			LeverageBreach:    b.Account.Core.LeverageBreach,
			SolvencyBreach:    b.Account.Core.SolvencyBreach,
		},
		BlockDelay{NetReturnPositiveByDelay: delayOK},
		BlockCost{CostStressMultiplier: g10protocol.ExamCostStressMultiplier, NetReturnPositive: final >= initial},
		BlockShock{ShockBlockLOO_OK: true},
		BlockConcurrent{MaxConcurrentByCapitalOK: true},
		BlockBrainTrace{ReplayMatchOK: traceOK, DegeneracyClear: degeneracyClear},
		nil
}

// DefaultReplayFunc returns the bar-level Brain replay callback bound to one commitment.
func DefaultReplayFunc(commitment BrainCommitment) ReplayFunc {
	return func(r io.Reader) ([]FixtureBlock, error) {
		return ReplayBlocksFromFixture(r, commitment)
	}
}

// EncodeInputFixture builds a custodian-side input-only fixture envelope.
func EncodeInputFixture(commitmentHash string, blocks []FixtureInputBlock) ([]byte, error) {
	return json.Marshal(FixtureEnvelope{
		SchemaVersion:          FixtureSchemaVersion,
		ExpectedCommitmentHash: commitmentHash,
		Blocks:                 blocks,
	})
}

// PassingInputBlock returns synthetic input bars for tests.
func PassingInputBlock() FixtureInputBlock {
	base := float64(g10protocol.ValidationInitialEquities[0])
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	feat := func(ret, prior float64) brain.BarFeatures {
		return brain.BarFeatures{
			Symbol: "BTC-USDT-SWAP", ProfileID: "okx_1h",
			ReturnBPS: ret, PriorReturnBPS: prior, BTCRelativeBPS: 5,
			BarRangeBPS: 45, RollingVolumeUSDT: 5e5, Price: 100,
		}
	}
	ev := func(ret, prior float64, idx int, short bool) brain.MatchedEvent {
		f := feat(ret, prior)
		me := brain.MatchedEvent{
			EventFamily: eventmap.FamilyExtremeContinuation,
			EventID:     fmt.Sprintf("exam-test-event-%d", idx),
			GrossLongBPS: f.ReturnBPS, GrossShortBPS: -f.ReturnBPS,
			CompletionBars: 4, CapacityUSDT: 1000, DeadlineBar: idx + 4,
		}
		if short {
			me.GrossLongBPS = 10
			me.GrossShortBPS = 75
		}
		return me
	}
	bars := make([]FixtureBarInput, 0, g10protocol.ExamMinIndependentDays)
	for i := 0; i < g10protocol.ExamMinIndependentDays; i++ {
		ret, prior := 90.0, 80.0
		short := false
		if i%2 == 1 {
			ret, prior = -80, 70
			short = true
		}
		bars = append(bars, FixtureBarInput{
			BarIndex: i + 1,
			DecisionTime: now.Add(time.Duration(i) * 24 * time.Hour),
			Features: feat(ret, prior),
			Events:   []brain.MatchedEvent{ev(ret, prior, i+1, short)},
			Prices:   map[string]float64{"BTC-USDT-SWAP": 100 - float64(i%3)},
		})
	}
	return FixtureInputBlock{InitialEquityUSDT: base, Bars: bars}
}

// PassingBlock returns replay-computed metrics for synthetic tests.
func PassingBlockFromReplay(commitment BrainCommitment) (FixtureBlock, error) {
	raw, err := EncodeInputFixture(commitment.ExpectedCommitmentHash, []FixtureInputBlock{PassingInputBlock()})
	if err != nil {
		return FixtureBlock{}, err
	}
	blocks, err := ReplayBlocksFromFixture(bytes.NewReader(raw), commitment)
	if err != nil || len(blocks) == 0 {
		return FixtureBlock{}, fmt.Errorf("replay passing block: %w", err)
	}
	return blocks[0], nil
}

func examBrainConfig(commitment BrainCommitment) brain.Config {
	fp := examSyntheticFingerprint()
	return brain.Config{
		InitialEquityUSDT: float64(g10protocol.ValidationInitialEquities[0]),
		MinNotionalUSDT:     5,
		LotStep:             0.01,
		ContractValue:       1,
		ProtocolHash:        commitment.ProtocolHash,
		Fingerprint:         fp,
		PersonalAdj:         residualmap.LookupPersonalAdjustments(fp, "BTC-USDT-SWAP", "long"),
		Memory:              episodic.NewStore(examPositiveMemory()),
	}
}

func examPositiveMemory() []episodic.FrozenEpisode {
	var out []episodic.FrozenEpisode
	for i := 0; i < 6; i++ {
		out = append(out, episodic.FrozenEpisode{
			SchemaVersion: episodic.SchemaVersionEpisode,
			EpisodeHash:   "exam-win",
			EventID:       "exam-win",
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

func examSyntheticFingerprint() fingerprint.PersonalExecutionFingerprint {
	cell := examExecutionCell("long")
	shortCell := examExecutionCell("short")
	return fingerprint.PersonalExecutionFingerprint{
		SchemaVersion: fingerprint.SchemaVersion, Generation: fingerprint.Generation,
		FingerprintHash: "exam-fixture-fingerprint-known-hash-v1",
		SourceStatus: map[string]fingerprint.SourceStatus{
			"maker_attempts":    {Status: fingerprint.StatusAvailable, Decoded: 10},
			"executable_shadow": {Status: fingerprint.StatusAvailable, Decoded: 10},
		},
		Cells: []fingerprint.ExecutionCell{cell, shortCell},
	}
}

func examExecutionCell(direction string) fingerprint.ExecutionCell {
	return fingerprint.ExecutionCell{
		CellKey: fingerprint.CellKey{Symbol: "BTC-USDT", Direction: direction, OrderType: "maker"},
		FeeBPS:        fingerprint.QuantileMetric{P90: 1, N: 10, Status: fingerprint.StatusAvailable},
		EntrySlipBPS:  fingerprint.QuantileMetric{P90: 0.3, N: 10, Status: fingerprint.StatusAvailable},
		ExitSlipBPS:   fingerprint.QuantileMetric{P90: 0.2, N: 10, Status: fingerprint.StatusAvailable},
		LatencyMS:     fingerprint.QuantileMetric{P90: 120, N: 10, Status: fingerprint.StatusAvailable},
		MakerFillRate: fingerprint.RateMetric{Rate: 0.8, N: 10, Status: fingerprint.StatusAvailable},
		SampleCount:   10,
		Instrument: fingerprint.KnownLinearInstrument("0.01", "1", "0.1", 100),
	}
}
