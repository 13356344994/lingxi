package eventvalidation

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
	"lingxi/internal/routerg6/reverseevent"
)

func passingTierStats() FiringStats {
	return FiringStats{
		Count: 60, UniqueDays: 25, UniqueSymbols: 1, UniqueShockBlocks: 12,
		ConservativeMeanBPS: 5, FeeNetMeanBPS: 8, StressConservativeMeanBPS: 1,
		MaxPositiveContributionFraction: 0.2, MaxDayPositiveContributionFraction: 0.2,
		DelayMeanBPS:       map[int]float64{0: 5, 1: 2, 2: 1.5, 3: 1},
		NonzeroShockBlocks: 12, PositiveShockBlocks: 10,
	}
}

func passingAccumulator() *cardAccumulator {
	acc := newCardAccumulator()
	for _, tier := range protocol.ValidationInitialEquities {
		t := acc.tiers[int(tier)]
		for i := 0; i < 60; i++ {
			day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%25).Format("2006-01-02")
			cluster := "shock-" + string(rune('a'+i%12))
			for _, delay := range protocol.ValidationDelayBars {
				t.add(FiringSample{
					Symbol: "AAA-USDT-SWAP", Day: day, ShockCluster: cluster, DelayBars: delay,
					CapitalTier: int(tier), OutcomeClass: OutcomeWinner,
					ConservativeBPS: 5, FeeNetBPS: 8, StressConservativeBPS: 1, GrossBPS: 12,
				})
			}
		}
	}
	return acc
}

func testCard(ordinal int) reverseevent.FrozenEventCard {
	card := reverseevent.FrozenEventCard{
		SchemaVersion: reverseevent.SchemaVersionCard,
		Generation:    protocol.Generation,
		ProtocolHash:  protocol.RouterProtocolHash,
		ProfileID:     eventmap.ProfileOKX1H,
		Ordinal:       ordinal,
		EventFamily:   eventmap.FamilyExtremeContinuation,
		Horizon:       "1h",
		Direction:     "long",
		DelayBars:     0,
		ShockCluster:  "shock-2024-03-01",
		TriggerConditions: reverseevent.TriggerSpec{
			EventFamily: eventmap.FamilyExtremeContinuation,
			Conditions: []reverseevent.TriggerCondition{
				{Field: "abs_return_bps", Op: "gte", Value: 10},
				{Field: "rms_bps", Op: "gte", Value: 5},
				{Field: "return_sign", Op: "eq_prior_sign"},
			},
		},
		DirectionRule:         "fixed:long",
		MaxWaitTime:           "1h",
		DynamicDeadline:       reverseevent.DynamicDeadlineSpec{Horizon: "1h", FallbackOnly: true, StopErrorRequired: true},
		InvalidatorConditions: []string{"deadline"},
		CapacityBand:          "tier_30_usdt",
		CapitalTiers:          []int{30, 100, 1000},
		DataRequirements:      []string{"okx_1h/ohlcv"},
		EventCompletion:       reverseevent.EventCompletionStopError,
		PayerStatus:           reverseevent.PayerStatusUnexplained,
		Status:                protocol.UnexplainedCandidateStatus,
		ValidationStage:       reverseevent.ValidationStagePending,
		TradeAllowed:          false,
		ExamEligible:          false,
		SourceWinnerCount:     3,
		SourceWinnerIDsHash:   "abc",
		MapManifestHash:       "map",
		DiscoveryMode:         protocol.DiscoveryMode,
		ExamActions:           protocol.ExamActions,
	}
	hash, _ := reverseevent.CanonicalEventCardHash(card)
	card.EventCardHash = hash
	return card
}

func TestSparseSampleInconclusive(t *testing.T) {
	stats := passingTierStats()
	stats.Count = 10
	status, _, reason := EvaluateTierGates(stats)
	if status != StatusInconclusive || reason != ReasonCoverage {
		t.Fatalf("sparse sample must be inconclusive, got %s/%s", status, reason)
	}
}

func TestDelayFailure(t *testing.T) {
	stats := passingTierStats()
	stats.DelayMeanBPS[3] = -0.1
	status, _, reason := EvaluateTierGates(stats)
	if status != StatusValidationFail || reason != ReasonDelay {
		t.Fatalf("delay failure expected, got %s/%s", status, reason)
	}
}

func TestDayConcentrationFails(t *testing.T) {
	stats := passingTierStats()
	stats.MaxDayPositiveContributionFraction = 0.36
	status, _, reason := EvaluateTierGates(stats)
	if status != StatusValidationFail || reason != ReasonConcentration {
		t.Fatalf("day concentration must fail, got %s/%s", status, reason)
	}
}

func TestBHUsesLedgerAttemptFamily(t *testing.T) {
	const family = 64
	cards := []reverseevent.FrozenEventCard{testCard(1)}
	accumulators := []*cardAccumulator{passingAccumulator()}
	ledger := reverseevent.ExperimentLedger{
		Attempts: make([]reverseevent.ExperimentAttempt, family),
	}
	for i := 0; i < family; i++ {
		ledger.Attempts[i] = reverseevent.ExperimentAttempt{
			AttemptOrdinal: i + 1,
			Status:         reverseevent.LedgerStatusDuplicate,
		}
	}
	ledger.Attempts[0] = reverseevent.ExperimentAttempt{
		AttemptOrdinal: 1,
		EventCardHash:  cards[0].EventCardHash,
		Status:         reverseevent.LedgerStatusUnexplained,
	}
	results := BuildCardResults(cards, accumulators, ledger)
	if !results[0].BHPass || results[0].Status != StatusValidationPass {
		t.Fatalf("strong signal did not pass ledger-family BH: %+v", results[0])
	}
	if results[0].SignP >= DiscoveryScreenFDRQ/float64(family) {
		t.Fatalf("strong p=%g does not establish ledger-family denominator", results[0].SignP)
	}
}

func TestWinnerBiasFullSampleFails(t *testing.T) {
	acc := newCardAccumulator()
	for _, tier := range protocol.ValidationInitialEquities {
		tierAcc := acc.tiers[int(tier)]
		for i := 0; i < 60; i++ {
			day := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i%25).Format("2006-01-02")
			cluster := "shock-" + string(rune('a'+i%12))
			outcome := OutcomeWinner
			cons := 5.0
			if i%2 == 0 {
				outcome = OutcomeLoss
				cons = -3
			}
			tierAcc.add(FiringSample{
				Symbol: "AAA-USDT-SWAP", Day: day, ShockCluster: cluster, DelayBars: 0,
				CapitalTier: int(tier), OutcomeClass: outcome,
				ConservativeBPS: cons, FeeNetBPS: cons, StressConservativeBPS: cons,
			})
		}
	}
	results := BuildCardResults([]reverseevent.FrozenEventCard{testCard(1)}, []*cardAccumulator{acc}, reverseevent.ExperimentLedger{})
	if results[0].Status == StatusValidationPass {
		t.Fatalf("winner-biased mixed sample must not pass: %+v", results[0])
	}
}

func TestSingleSymbolMultiDateCoverage(t *testing.T) {
	acc := passingAccumulator()
	stats := acc.tiers[30].finalize()
	if stats.UniqueSymbols != 1 {
		t.Fatalf("expected single symbol, got %d", stats.UniqueSymbols)
	}
	if stats.UniqueDays < protocol.ValidationMinIndependentDays {
		t.Fatalf("expected >=%d days, got %d", protocol.ValidationMinIndependentDays, stats.UniqueDays)
	}
	if stats.UniqueShockBlocks < protocol.ValidationMinNonOverlapShockBlocks {
		t.Fatalf("expected >=%d shock blocks, got %d", protocol.ValidationMinNonOverlapShockBlocks, stats.UniqueShockBlocks)
	}
	status, _, _ := EvaluateTierGates(stats)
	if status != StatusValidationPass {
		t.Fatalf("single-symbol multi-date must pass coverage gates, got %s", status)
	}
}

func TestWindowOverlapDoesNotDoubleCount(t *testing.T) {
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	bars := syntheticTrendBars(base, time.Hour, 120, 1.02)
	spec, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	boundaries := syntheticBoundaries(base, 48*time.Hour, 48*time.Hour)
	th := syntheticThresholds(spec.ID)
	tracker := eventmap.NewWindowTracker()
	card := testCard(1)
	acc := newCardAccumulator()
	cfg := ScanConfig{
		Spec: spec, Boundaries: boundaries, Thresholds: th, BTCBars: bars,
		ShockClusterAt: func(time.Time) string { return "shock-a" },
	}
	if err := ScanCardSymbol(bars, "AAA-USDT-SWAP", card, cfg, tracker, acc); err != nil {
		t.Fatal(err)
	}
	first := acc.tiers[30].count
	overlapTracker := eventmap.NewWindowTracker()
	overlapAcc := newCardAccumulator()
	for i := 0; i < 3; i++ {
		if err := ScanCardSymbol(bars, "AAA-USDT-SWAP", card, cfg, overlapTracker, overlapAcc); err != nil {
			t.Fatal(err)
		}
	}
	if overlapAcc.tiers[30].count != first {
		t.Fatalf("overlap rescan changed count: first=%d second=%d", first, overlapAcc.tiers[30].count)
	}
}

func TestSurvivorsRemainUnexplainedNoTradeExam(t *testing.T) {
	cards := []reverseevent.FrozenEventCard{testCard(1)}
	results := BuildCardResults(cards, []*cardAccumulator{passingAccumulator()}, reverseevent.ExperimentLedger{
		Attempts: []reverseevent.ExperimentAttempt{{
			AttemptOrdinal: 1, EventCardHash: cards[0].EventCardHash, Status: reverseevent.LedgerStatusUnexplained,
		}},
	})
	report, err := BuildReport(RunConfig{SchemaVersion: SchemaVersionConfig, ProgressEvery: 1}, CardBundle{
		Manifest: reverseevent.EventCardManifest{
			ManifestHash: "m", EventCardHash: cards[0].EventCardHash,
			ExperimentHash: "e", CatalogHash: protocol.HistoryDataHash,
		},
	}, nil, results, reverseevent.ExperimentLedger{
		Attempts: []reverseevent.ExperimentAttempt{{
			AttemptOrdinal: 1, EventCardHash: cards[0].EventCardHash, Status: reverseevent.LedgerStatusUnexplained,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSurvivors(survivors); err != nil {
		t.Fatal(err)
	}
	if report.DiscoveryScreenFamilySize != 1 {
		t.Fatalf("report family size=%d want 1", report.DiscoveryScreenFamilySize)
	}
	for _, r := range results {
		if r.TradeAllowed || r.ExamEligible {
			t.Fatal("validation results must not allow trade or exam")
		}
	}
}

func TestExactSignPStable(t *testing.T) {
	p, err := g4protocol.ExactOneSidedPositiveBinomialSignP(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p >= 1e-20 {
		t.Fatalf("unstable exact p=%g", p)
	}
}

func TestCostStressApplied(t *testing.T) {
	bars := syntheticTrendBars(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Hour, 20, 1.01)
	spec, _ := eventmap.ProfileSpecFor(eventmap.ProfileOKX1H)
	horizon := spec.Horizons[0]
	costIn := profitmap.CostInputs{
		RollingQuoteVolumeUSDT: 1e8, BarHigh: 101, BarLow: 99, BarMid: 100,
	}
	_, _, conservative, stress, _, ok := computeOutcome(bars, 5, horizon, "long", 30, costIn, nil, 0)
	if !ok {
		t.Fatal("expected outcome")
	}
	if stress >= conservative {
		t.Fatalf("stress must be below base conservative: stress=%v conservative=%v", stress, conservative)
	}
}

func syntheticTrendBars(base time.Time, step time.Duration, n int, growth float64) []eventmap.Bar {
	bars := make([]eventmap.Bar, n)
	prevClose := 100.0
	for i := range bars {
		bars[i].OpenTime = base.Add(time.Duration(i) * step)
		if i == 0 {
			bars[i].Open = prevClose
		} else {
			bars[i].Open = prevClose
		}
		bars[i].Close = prevClose * growth
		bars[i].High = bars[i].Close * 1.001
		bars[i].Low = bars[i].Open * 0.999
		bars[i].QuoteVolume = 1e7
		prevClose = bars[i].Close
	}
	return bars
}

func syntheticBoundaries(base time.Time, discoverySpan, validationSpan time.Duration) eventmap.Boundaries {
	return eventmap.Boundaries{
		Start: base, DiscoveryEnd: base.Add(discoverySpan),
		ValidationStart: base.Add(discoverySpan + 24*time.Hour),
		ValidationEnd:   base.Add(discoverySpan + validationSpan + 24*time.Hour),
		WFStart:         base.Add(discoverySpan + validationSpan + 48*time.Hour),
		End:             base.Add(discoverySpan + validationSpan + 72*time.Hour),
	}
}

func syntheticThresholds(profileID string) eventmap.FrozenThresholds {
	return eventmap.FrozenThresholds{
		ProfileID: profileID, ExtremeReturnAbsBPS: 5, ExtremeRMSBPS: 2,
		LowLiquidityQuote: 1e12, VolumeSpikeQuote: 1e12, BTCRelativeAbsBPS: 1e6,
		BTCShockReturnAbsBPS: 1e6,
	}
}

func TestCheckpointTamperDetection(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: dir, CardOutputDir: dir,
		OutputDir: dir, MapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000,
		ProgressEvery: 1000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		CardManifestHash: "manifest",
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CatalogHash: "abc", ConfigHash: cfgHash,
		CardManifestHash: "manifest", EventCardHash: "cards", ExperimentHash: "ledger",
		MapManifestHashes: map[string]string{"okx_1h": "m"}, OutputDir: dir, Done: false,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
}
