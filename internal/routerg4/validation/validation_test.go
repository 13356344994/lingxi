package validation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
	"lingxi/internal/routerg4/validation"
)

func TestEvaluateGatesStablePass(t *testing.T) {
	stats := validation.FiringStats{
		Count: 30, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 5, ConservativeMeanBPS: 3,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.10,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationPass || reason != "" {
		t.Fatalf("expected pass, got status=%s reason=%s", status, reason)
	}
}

func TestEvaluateGatesCoverageInconclusive(t *testing.T) {
	stats := validation.FiringStats{Count: 10, UniqueDays: 4, UniqueSymbols: 2}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusInconclusive || reason != validation.ReasonCoverage {
		t.Fatalf("expected inconclusive coverage, got %s/%s", status, reason)
	}
}

func TestEvaluateGatesCostFail(t *testing.T) {
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: -0.1,
		MaxPositiveContributionFraction: 0.1,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationFail || reason != validation.ReasonCost {
		t.Fatalf("expected cost fail, got %s/%s", status, reason)
	}
}

func TestEvaluateGatesDelayFail(t *testing.T) {
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 0, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.1,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationFail || reason != validation.ReasonDelay {
		t.Fatalf("expected delay fail, got %s/%s", status, reason)
	}
}

func TestEvaluateGatesConcentrationFail(t *testing.T) {
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.40,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationFail || reason != validation.ReasonConcentration {
		t.Fatalf("expected concentration fail, got %s/%s", status, reason)
	}
}

func TestEvaluateGatesCapacityFail(t *testing.T) {
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: -1, ConservativeMeanBPS: -2,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		CapacityFailRate:                1.0,
		MaxPositiveContributionFraction: 0.1,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationFail || reason != validation.ReasonCapacity {
		t.Fatalf("expected capacity fail, got %s/%s", status, reason)
	}
}

func TestEvaluateGatesNoEdgeFail(t *testing.T) {
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: -1, ConservativeMeanBPS: -1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		CapacityFailRate:                0.1,
		MaxPositiveContributionFraction: 0.1,
	}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusValidationFail || reason != validation.ReasonNoEdge {
		t.Fatalf("expected no_edge fail, got %s/%s", status, reason)
	}
}

func TestCandidateIndexEfficiency(t *testing.T) {
	candidates := make([]reversemine.Candidate, 1024)
	cell := profitmap.CellKey{
		SkyCoarse: "ret=low/rv=mid", Liquidity: "liq_6", SymbolClass: "major",
		Horizon: "1h", Direction: "long", CapitalTier: 30,
	}
	for i := range candidates {
		c, err := buildTestCandidate(i+1, cell, []reversemine.Condition{
			{FeatureKey: "symbol_return_1h", Op: reversemine.OpGTE, Threshold: float64(i), Quantile: 0.5},
		})
		if err != nil {
			t.Fatal(err)
		}
		candidates[i] = c
	}
	idx := validation.BuildCandidateIndex(candidates)
	if idx.TotalCandidates() != 1024 {
		t.Fatalf("index count=%d", idx.TotalCandidates())
	}
	if idx.MaxBucketSize() != 1024 {
		t.Fatalf("expected single bucket of 1024, got max=%d", idx.MaxBucketSize())
	}
	bucket := idx.Lookup(profitmap.ProfileOKX1H, cell)
	if len(bucket) != 1024 {
		t.Fatalf("bucket size=%d", len(bucket))
	}
}

func TestZeroCandidatesReport(t *testing.T) {
	cfg := validation.RunConfig{
		SchemaVersion: validation.SchemaVersionConfig,
		HistoryRoot:   t.TempDir(), CandidateOutputDir: t.TempDir(), OutputDir: t.TempDir(),
		MapDirs: []string{t.TempDir()}, MaxFiles: 1, MaxLines: 100, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CandidateManifestHash: "h",
	}
	results := validation.BuildCandidateResults(nil, nil)
	report, err := validation.BuildReport(cfg, validation.CandidateBundle{
		Manifest: reversemine.CandidateManifest{ManifestHash: "h", CandidatesHash: "c", LedgerHash: "l", CatalogHash: "cat"},
	}, map[string]string{}, results)
	if err != nil {
		t.Fatal(err)
	}
	if report.CandidateCount != 0 {
		t.Fatalf("expected zero candidates, got %d", report.CandidateCount)
	}
}

func TestCheckpointTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := validation.RunConfig{
		SchemaVersion: validation.SchemaVersionConfig,
		HistoryRoot:   dir, CandidateOutputDir: dir, OutputDir: dir,
		MapDirs: []string{dir}, MaxFiles: 10, MaxLines: 1000, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CandidateManifestHash: "manifest",
	}
	cfgHash, _ := validation.ConfigFingerprint(cfg)
	cp := validation.Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		CatalogHash: "abc", CandidateManifestHash: "manifest",
		CandidatesHash: "c", LedgerHash: "l", OutputDir: dir,
		MapManifestHashes: map[string]string{"okx_1h": "hash"},
	}
	if err := validation.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validation.LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
}

func TestReportReceiptProvenanceTamper(t *testing.T) {
	cfg := validation.RunConfig{
		SchemaVersion: validation.SchemaVersionConfig,
		HistoryRoot:   t.TempDir(), CandidateOutputDir: t.TempDir(), OutputDir: t.TempDir(),
		MapDirs: []string{t.TempDir()}, MaxFiles: 1, MaxLines: 100, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CandidateManifestHash: "manifest",
	}
	bundle := validation.CandidateBundle{
		Manifest: reversemine.CandidateManifest{
			ManifestHash: "manifest", CandidatesHash: "c", LedgerHash: "l", CatalogHash: "cat",
		},
	}
	results := validation.BuildCandidateResults(nil, nil)
	report, err := validation.BuildReport(cfg, bundle, map[string]string{"okx_1h": "m"}, results)
	if err != nil {
		t.Fatal(err)
	}
	survivors, err := validation.BuildSurvivors(report)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := validation.BuildReceipt(cfg, bundle, map[string]string{"okx_1h": "m"}, nil, map[string]string{"catalog_hash": "cat"}, report, survivors)
	if err != nil {
		t.Fatal(err)
	}
	if err := validation.ValidateReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	receipt.ReportHash = "tampered"
	if err := validation.ValidateReceipt(receipt); err == nil {
		t.Fatal("tampered receipt must fail")
	}
}

func TestPartitionLeakRejected(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b := profitmap.Boundaries{
		Start: base, DiscoveryEnd: base.Add(48 * time.Hour),
		ValidationStart: base.Add(50 * time.Hour), ValidationEnd: base.Add(72 * time.Hour),
		End: base.Add(96 * time.Hour),
	}
	if err := validation.RejectValidationPartitionLeak(b, base.Add(24*time.Hour)); err == nil {
		t.Fatal("discovery decision must leak-check fail")
	}
	if err := validation.RejectValidationPartitionLeak(b, base.Add(60*time.Hour)); err != nil {
		t.Fatalf("validation decision must pass: %v", err)
	}
}

func TestCurrentBarForbiddenInStream(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []profitmap.Bar{{
		OpenTime: base, Open: 100, High: 101, Low: 99, Close: 100.5,
		QuoteVolume: 1e6, Confirmed: false,
	}}
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	_, err := reversemine.ComputeFeatures(spec, bars, bars, base.Add(time.Hour), nil)
	if err == nil || !strings.Contains(err.Error(), "current bar") {
		t.Fatalf("expected current bar rejection, err=%v", err)
	}
}

func TestHindsightWinnerNotPredictableFails(t *testing.T) {
	// Candidate requires feature threshold never met in validation despite positive gross hindsight.
	stats := validation.FiringStats{Count: 0}
	status, _, reason := validation.EvaluateGates(stats)
	if status != validation.StatusInconclusive || reason != validation.ReasonCoverage {
		t.Fatalf("unpredictable winner should fail coverage, got %s/%s", status, reason)
	}
}

func TestSmallCapitalPassLargeCapitalFailScenario(t *testing.T) {
	small := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 3, ConservativeMeanBPS: 2,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		CapacityFailRate: 0, MaxPositiveContributionFraction: 0.1,
	}
	large := small
	large.CapacityFailRate = 1.0
	large.ConservativeMeanBPS = -5
	large.FeeNetMeanBPS = -2
	sStatus, _, _ := validation.EvaluateGates(small)
	lStatus, _, lReason := validation.EvaluateGates(large)
	if sStatus != validation.StatusValidationPass {
		t.Fatalf("small capital should pass, got %s", sStatus)
	}
	if lStatus != validation.StatusValidationFail || lReason != validation.ReasonCapacity {
		t.Fatalf("large capital should fail capacity, got %s/%s", lStatus, lReason)
	}
}

func TestSurvivorsRemainUnexplained(t *testing.T) {
	cell := profitmap.CellKey{
		SkyCoarse: "ret=low/rv=mid", Liquidity: "liq_6", SymbolClass: "major",
		Horizon: "1h", Direction: "long", CapitalTier: 30,
	}
	candidate, err := buildTestCandidate(1, cell, []reversemine.Condition{
		{FeatureKey: "symbol_return_1h", Op: reversemine.OpGTE, Threshold: 0, Quantile: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := validation.FiringStats{
		Count: 25, UniqueDays: 12, UniqueSymbols: 4, NonzeroBlocks: 10, PositiveBlocks: 9,
		FeeNetMeanBPS: 2, ConservativeMeanBPS: 1,
		DelayConservativeMeanBPS: 1, StressConservativeMeanBPS: 0.5,
		MaxPositiveContributionFraction: 0.1,
	}
	status, _, _ := validation.EvaluateGates(stats)
	if status != validation.StatusValidationPass {
		t.Fatalf("expected pass stats")
	}
	cfg := validation.RunConfig{
		SchemaVersion: validation.SchemaVersionConfig,
		HistoryRoot:   t.TempDir(), CandidateOutputDir: t.TempDir(), OutputDir: t.TempDir(),
		MapDirs: []string{t.TempDir()}, MaxFiles: 1, MaxLines: 100, ProgressEvery: 100,
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, CandidateManifestHash: "m",
	}
	report, err := validation.BuildReport(cfg, validation.CandidateBundle{
		Manifest: reversemine.CandidateManifest{ManifestHash: "m", CandidatesHash: "c", LedgerHash: "l", CatalogHash: "cat"},
	}, nil, []validation.CandidateResult{{
		Ordinal: 1, Status: validation.StatusValidationPass, Candidate: candidate, Firing: stats,
	}})
	if err != nil {
		t.Fatal(err)
	}
	survivors, err := validation.BuildSurvivors(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(survivors.Survivors) != 1 || survivors.Survivors[0].Status != validation.SurvivorStatus {
		t.Fatalf("survivor status must remain UNEXPLAINED: %+v", survivors.Survivors)
	}
}

func TestFrozenCandidateAppendRejected(t *testing.T) {
	dir := t.TempDir()
	manifest := reversemine.CandidateManifest{
		SchemaVersion: reversemine.SchemaVersionManifest,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ActualCandidateCount: 1,
		CandidatesHash: "will-fix", LedgerHash: "l", FreezeBeforeValidation: true, ExamActions: 0,
	}
	candidates := []reversemine.Candidate{{Ordinal: 1, Status: reversemine.CandidateStatus}}
	hash, _ := reversemine.HashCandidates(candidates)
	manifest.CandidatesHash = hash
	sealed, err := reversemine.BuildCandidateManifest(
		reversemine.RunConfig{SchemaVersion: reversemine.SchemaVersionConfig, MaxFiles: 1, MaxLines: 1, CandidateLimit: 1, ProgressEvery: 1},
		"cat", nil, nil, candidates,
		reversemine.CandidateLedger{SchemaVersion: reversemine.SchemaVersionLedger, Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash, SearchSpaceID: protocol.ReverseMineSearchSpaceID, DerivationAlgorithm: protocol.DerivationAlgorithmVersion, WinnerSeedMode: reversemine.WinnerSeedMode, Entries: []reversemine.LedgerEntry{}},
		reversemine.CoverageSummary{},
		"coverage",
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = manifest
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := struct {
		Candidates []reversemine.Candidate `json:"candidates"`
	}{Candidates: append(candidates, candidates[0])}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(dir, "candidates.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sealed.ActualCandidateCount = 1
	manifestRaw, _ := json.MarshalIndent(sealed, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "candidate_manifest.json"), manifestRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	ledger, _ := reversemine.SealLedger(reversemine.CandidateLedger{
		SchemaVersion: reversemine.SchemaVersionLedger, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, SearchSpaceID: protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm: protocol.DerivationAlgorithmVersion, WinnerSeedMode: reversemine.WinnerSeedMode,
	})
	ledgerRaw, _ := json.MarshalIndent(ledger, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "g4_candidate_ledger.json"), ledgerRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validation.LoadCandidateBundle(dir); err == nil {
		t.Fatal("appended candidate set must be rejected")
	}
}

func buildTestCandidate(ordinal int, cell profitmap.CellKey, conditions []reversemine.Condition) (reversemine.Candidate, error) {
	c := reversemine.Candidate{
		Ordinal: ordinal, ProfileID: profitmap.ProfileOKX1H, CellKey: cell,
		Conditions: conditions, Status: reversemine.CandidateStatus,
		SourceWinnerCount: 1, SourceWinnerIDsHash: "h", MapManifestHash: "m",
		WinnerSeedMode: reversemine.WinnerSeedMode, SearchSpaceID: protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm: protocol.DerivationAlgorithmVersion,
	}
	hash, err := reversemine.CanonicalCandidateHash(c)
	if err != nil {
		return reversemine.Candidate{}, err
	}
	c.CanonicalHash = hash
	return c, nil
}
