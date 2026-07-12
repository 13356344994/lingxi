package validation

import (
	"fmt"
	"math"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

func TestFiveDaysCannotPassDeterministicGates(t *testing.T) {
	stats := passingStats()
	stats.UniqueDays = 5
	status, _, reason := EvaluateGates(stats)
	if status != StatusInconclusive || reason != ReasonCoverage {
		t.Fatalf("five days must be inconclusive, got %s/%s", status, reason)
	}
}

func TestDayPositiveConcentrationFails(t *testing.T) {
	stats := passingStats()
	stats.MaxDayPositiveContributionFraction = 0.36
	status, _, reason := EvaluateGates(stats)
	if status != StatusValidationFail || reason != ReasonConcentration {
		t.Fatalf("day concentration must fail, got %s/%s", status, reason)
	}
}

func TestBHUsesAllFrozenCandidates(t *testing.T) {
	const family = 1024
	candidates := make([]reversemine.Candidate, family)
	accumulators := make([]*firingAccumulator, family)
	for i := 0; i < family; i++ {
		candidates[i] = testScreenCandidate(i, "volatility_cost_ratio", float64(i))
		accumulators[i] = newFiringAccumulator()
	}
	accumulators[0] = passingAccumulator(14)
	results := BuildCandidateResults(candidates, accumulators)
	if !results[0].BHPass || results[0].Status != StatusValidationPass {
		t.Fatalf("strong signal did not pass full-family BH: %+v", results[0])
	}
	if results[0].SignP >= protocol.ValidationFDRQ/family {
		t.Fatalf("strong p=%g does not establish full-family denominator", results[0].SignP)
	}
	for i := 1; i < family; i++ {
		if results[i].BHPass {
			t.Fatalf("null candidate %d passed BH", i)
		}
	}
}

func TestExactSignPStableAtN100(t *testing.T) {
	p, err := protocol.ExactOneSidedPositiveBinomialSignP(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p >= 1e-20 {
		t.Fatalf("unstable n=100 exact p=%g", p)
	}
}

func TestPhenomenonDedupeKeepsSubpaths(t *testing.T) {
	first := testScreenCandidate(1, "symbol_return_1h", 1)
	second := first
	second.Ordinal = 2
	second.CellKey.CapitalTier = 1000
	second.CellKey.Liquidity = "liq_9"
	second.CellKey.SymbolClass = "long_tail"
	firstID, secondID := phenomenonID(first), phenomenonID(second)
	if firstID == "" || firstID != secondID {
		t.Fatalf("capital/subpath split phenomenon: %q vs %q", firstID, secondID)
	}
	results := []CandidateResult{
		{Status: StatusValidationPass, PhenomenonID: firstID, Candidate: first},
		{Status: StatusValidationPass, PhenomenonID: secondID, Candidate: second},
	}
	report, err := BuildReport(testRunConfig(t), CandidateBundle{}, nil, results)
	if err != nil {
		t.Fatal(err)
	}
	if report.PassCount != 2 || report.UniquePhenomenonPassCount != 1 {
		t.Fatalf("candidate/phenomenon counts wrong: %+v", report)
	}
	survivors, err := BuildSurvivors(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(survivors.Survivors) != 2 {
		t.Fatalf("subpaths collapsed from survivors: %d", len(survivors.Survivors))
	}
}

func TestRMSCandidateSurvivesScreening(t *testing.T) {
	candidate := testScreenCandidate(1, "volatility_cost_ratio", 1)
	results := BuildCandidateResults(
		[]reversemine.Candidate{candidate},
		[]*firingAccumulator{passingAccumulator(14)},
	)
	if len(results) != 1 || !results[0].BHPass ||
		results[0].Status != StatusValidationPass {
		t.Fatalf("RMS candidate degraded: %+v", results)
	}
}

func TestCheckpointRestoresBlocksAndDayContributions(t *testing.T) {
	acc := passingAccumulator(14)
	snapshot := exportCheckpointCandidateStats([]*firingAccumulator{acc})
	restored := restoreAccumulatorsFromCheckpoint(snapshot)[0]
	if len(restored.blocks) != 14 || len(restored.dayPositive) != 10 {
		t.Fatalf("checkpoint lost block/day state: blocks=%d days=%d",
			len(restored.blocks), len(restored.dayPositive))
	}
	before, after := acc.finalize(), restored.finalize()
	if before != after {
		t.Fatalf("checkpoint changed block stats:\n%+v\n%+v", before, after)
	}
}

func TestCandidateHorizonBuildsNonoverlapObservationBlocks(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candidate := testScreenCandidate(1, "symbol_return_1h", 1)
	candidate.CellKey.Horizon = "4h"
	spec, err := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	if err != nil {
		t.Fatal(err)
	}
	accumulators, err := buildCandidateAccumulators(
		[]reversemine.Candidate{candidate},
		[]profileContext{{
			ProfileID: candidate.ProfileID,
			Spec:      spec,
			Boundaries: profitmap.Boundaries{
				ValidationStart: start,
				ValidationEnd:   start.Add(20 * 24 * time.Hour),
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	blocks := accumulators[0].blocks
	if len(blocks) < protocol.ValidationMinIndependentBlocks {
		t.Fatalf("blocks=%d", len(blocks))
	}
	for i := 1; i < len(blocks); i++ {
		if blocks[i].Start.Before(blocks[i-1].End.Add(4 * time.Hour)) {
			t.Fatalf("block %d violates horizon gap: %+v after %+v", i, blocks[i], blocks[i-1])
		}
	}
}

func passingStats() FiringStats {
	return FiringStats{
		Count: 30, UniqueDays: 10, UniqueSymbols: 3,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction:    0.10,
		MaxDayPositiveContributionFraction: 0.10,
		NonzeroBlocks:                      10, PositiveBlocks: 9,
	}
}

func passingAccumulator(blockCount int) *firingAccumulator {
	acc := newFiringAccumulator()
	acc.count = 30
	acc.grossSum = 90
	acc.feeSum = 60
	acc.consSum = 30
	acc.delaySum = 30
	acc.stressSum = 15
	acc.consPositive = 30
	acc.positiveSum = 100
	acc.maxPositive = 10
	acc.symbols = map[string]bool{"A": true, "B": true, "C": true}
	acc.days = map[string]bool{}
	acc.dayPositive = map[string]float64{}
	for i := 0; i < 10; i++ {
		day := fmt.Sprintf("2024-01-%02d", i+1)
		acc.days[day] = true
		acc.dayPositive[day] = 10
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < blockCount; i++ {
		start := base.Add(time.Duration(i*2) * 24 * time.Hour)
		acc.blocks = append(acc.blocks, blockAggregate{
			Start: start, End: start.Add(24 * time.Hour), Sum: 1, Count: 1,
		})
	}
	return acc
}

func testScreenCandidate(ordinal int, feature string, threshold float64) reversemine.Candidate {
	return reversemine.Candidate{
		Ordinal:   ordinal,
		ProfileID: profitmap.ProfileOKX1H,
		CellKey: profitmap.CellKey{
			SkyCoarse: "sky", Liquidity: "liq_1", SymbolClass: "major",
			Horizon: "1h", Direction: "long", CapitalTier: 30,
		},
		Conditions: []reversemine.Condition{{
			FeatureKey: feature, Op: reversemine.OpGTE,
			Threshold: threshold, Quantile: 0.5,
		}},
		CanonicalHash: fmt.Sprintf("candidate-%d", ordinal),
	}
}

func testRunConfig(t *testing.T) RunConfig {
	t.Helper()
	return RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot:   t.TempDir(), CandidateOutputDir: t.TempDir(),
		OutputDir: t.TempDir(), MapDirs: []string{t.TempDir()},
		MaxFiles: 1, MaxLines: 1, ProgressEvery: 1,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash:       protocol.HistoryDataHash,
		CandidateManifestHash: "manifest",
	}
}
