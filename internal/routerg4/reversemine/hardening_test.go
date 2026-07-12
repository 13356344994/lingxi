package reversemine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
)

func TestFiveMinuteBTCFeaturesAreStrictlySameFrequency(t *testing.T) {
	spec, err := profitmap.ProfileSpecFor(profitmap.ProfileOKX5M)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	symbol := testBars(base, 40, 5*time.Minute, 1.02)
	btc := testBars(base, 40, 5*time.Minute, 1.01)
	decision := symbol[30].OpenTime.Add(5 * time.Minute)
	features, err := ComputeFeatures(spec, symbol, btc, decision, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ((symbol[30].Close/symbol[29].Close - 1) -
		(btc[30].Close/btc[29].Close - 1)) * 10000
	if got := features[featureBTCRelativeReturn]; math.Abs(got-want) > 1e-9 {
		t.Fatalf("5m BTC relative return used wrong frequency/index: got=%v want=%v", got, want)
	}
	if _, ok := features["symbol_return_1h"]; !ok {
		t.Fatal("5m profile must expose normalized symbol_return_1h")
	}
}

func TestRealizedVolUsesRMSAndShortestWindowNonzero(t *testing.T) {
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX5M)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := testBars(base, 3, 5*time.Minute, 1.01)
	realized := realizedVolBPS(bars, 1, spec, nil)
	want := protocol.SymbolRealizedVolRMSBPS([]float64{
		bars[1].Close/bars[0].Close - 1,
	})
	if got := realized["5m"]; got <= 0 || math.Abs(got-want) > 1e-12 {
		t.Fatalf("shortest realized vol must be nonzero RMS: got=%v want=%v", got, want)
	}
	if got := volatilityCostRatio(realized, profitmap.ProfileOKX5M); math.Abs(
		got-protocol.VolatilityCostRatioFromRMS(want),
	) > 1e-12 {
		t.Fatalf("volatility_cost_ratio not based on RMS: %v", got)
	}
}

func TestDualThresholdsUseCommonWinnerIntersection(t *testing.T) {
	cell := CellWorkItem{
		ProfileID: profitmap.ProfileOKX1H,
		Key: profitmap.CellKey{
			SkyCoarse: "sky", Liquidity: "liq_1", SymbolClass: "major",
			Horizon: "1h", Direction: "long", CapitalTier: 30,
		},
	}
	matrix := WinnerFeatureMatrix{
		cellID(cell): {
			"only-first":  {"quote_volume_24h_log_bucket": 1},
			"only-second": {"volatility_cost_ratio": 100},
			"common": {
				"quote_volume_24h_log_bucket": 7,
				"volatility_cost_ratio":       9,
			},
		},
	}
	state := mineState{
		matrix: matrix, candidateLimit: 1,
		registered: map[string]bool{}, coveredCells: map[string]bool{},
	}
	if err := state.attemptDual(
		cell, "quote_volume_24h_log_bucket", "volatility_cost_ratio",
		0.5, 0.5, OpLTE, OpGTE, "dual-test",
	); err != nil {
		t.Fatal(err)
	}
	if len(state.candidates) != 1 {
		t.Fatalf("expected one dual candidate, got %d", len(state.candidates))
	}
	candidate := state.candidates[0]
	if candidate.SourceWinnerCount != 1 {
		t.Fatalf("dual source count must be common intersection, got %d", candidate.SourceWinnerCount)
	}
	if candidate.Conditions[0].Threshold != 7 || candidate.Conditions[1].Threshold != 9 {
		t.Fatalf("dual thresholds not based on common winner: %+v", candidate.Conditions)
	}
	wantHash, _ := HashWinnerIDs([]string{"common"})
	if candidate.SourceWinnerIDsHash != wantHash {
		t.Fatal("dual source hash must bind common winner intersection")
	}
}

func TestFairBudgetCoversAllBucketsBeforeRepeating(t *testing.T) {
	var cells []CellWorkItem
	matrix := WinnerFeatureMatrix{}
	profiles := []string{profitmap.ProfileOKX1H, profitmap.ProfileOKX5M}
	directions := []string{"long", "short"}
	capitals := []int{30, 100, 1000}
	horizons := map[string][]string{
		profitmap.ProfileOKX1H: {"1h", "4h", "24h"},
		profitmap.ProfileOKX5M: {"5m", "15m", "60m", "240m"},
	}
	for _, profile := range profiles {
		for _, horizon := range horizons[profile] {
			for _, direction := range directions {
				for _, capital := range capitals {
					cell := CellWorkItem{
						ProfileID: profile, ManifestHash: profile,
						Key: profitmap.CellKey{
							SkyCoarse: "sky", Liquidity: "liq_1", SymbolClass: "major",
							Horizon: horizon, Direction: direction, CapitalTier: capital,
						},
						PriorityTier: capital / 100,
					}
					cells = append(cells, cell)
					matrix[cellID(cell)] = map[string]map[string]float64{
						"winner": {"quote_volume_24h_log_bucket": 5},
					}
				}
			}
		}
	}
	candidates, _, err := Derive(cells, matrix, len(cells), nil)
	if err != nil {
		t.Fatal(err)
	}
	coverage, _, err := BuildCoverage(cells, matrix, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.CoveredCells != len(cells) {
		t.Fatalf("fair first round covered %d/%d cells", coverage.CoveredCells, len(cells))
	}
	if len(coverage.CoverageByProfile) != 2 ||
		len(coverage.CoverageByDirection) != 2 ||
		len(coverage.CoverageByCapital) != 3 {
		t.Fatalf("incomplete multidimensional coverage: %+v", coverage)
	}
}

func TestLargeFairBudgetUsesAll1024ForDistinctCells(t *testing.T) {
	const total = 1100
	cells := make([]CellWorkItem, 0, total)
	matrix := WinnerFeatureMatrix{}
	profiles := []string{profitmap.ProfileOKX1H, profitmap.ProfileOKX5M}
	directions := []string{"long", "short"}
	capitals := []int{30, 100, 1000}
	for i := 0; i < total; i++ {
		profile := profiles[i%len(profiles)]
		horizon := "1h"
		if profile == profitmap.ProfileOKX5M {
			horizon = []string{"5m", "15m", "60m", "240m"}[i%4]
		} else {
			horizon = []string{"1h", "4h", "24h"}[i%3]
		}
		cell := CellWorkItem{
			ProfileID: profile, ManifestHash: profile,
			Key: profitmap.CellKey{
				SkyCoarse: fmt.Sprintf("sky_%04d", i),
				Liquidity: fmt.Sprintf("liq_%d", i%10), SymbolClass: "major",
				Horizon: horizon, Direction: directions[(i/2)%2],
				CapitalTier: capitals[(i/4)%3],
			},
			PriorityTier: i % 4,
		}
		cells = append(cells, cell)
		winnerFeatures := map[string]float64{}
		for featureIndex, feature := range ProfileFeatureOrder(profile) {
			winnerFeatures[feature] = float64(i + featureIndex + 1)
		}
		matrix[cellID(cell)] = map[string]map[string]float64{"winner": winnerFeatures}
	}
	candidates, _, err := Derive(cells, matrix, protocol.MaxTotalCandidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	coverage, _, err := BuildCoverage(cells, matrix, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != protocol.MaxTotalCandidates ||
		coverage.CoveredCells != protocol.MaxTotalCandidates {
		t.Fatalf("1024 budget prefix exhaustion: candidates=%d covered=%d",
			len(candidates), coverage.CoveredCells)
	}
	if len(coverage.CoverageByProfile) != 2 ||
		len(coverage.CoverageByDirection) != 2 ||
		len(coverage.CoverageByCapital) != 3 ||
		len(coverage.CoverageByPriority) != 4 {
		t.Fatalf("large budget lost bucket coverage: %+v", coverage)
	}
	availableSlots := 0
	for _, names := range coverage.AvailableProfileFeatureSlots {
		availableSlots += len(names)
	}
	coveredSlots := map[string]bool{}
	for _, candidate := range candidates {
		for _, condition := range candidate.Conditions {
			coveredSlots[candidate.ProfileID+"|"+condition.FeatureKey] = true
		}
	}
	if availableSlots == 0 || len(coveredSlots) != availableSlots {
		t.Fatalf("feature queues not fully covered before repeats: available=%d covered=%d",
			availableSlots, len(coveredSlots))
	}
	firstRoundSeen := map[string]int{}
	for _, candidate := range candidates {
		if len(candidate.Conditions) != 1 {
			continue
		}
		key := candidate.ProfileID + "|" + candidate.Conditions[0].FeatureKey
		firstRoundSeen[key]++
		if firstRoundSeen[key] == 2 {
			if len(firstRoundSeen) != availableSlots {
				t.Fatalf("feature %s received second candidate before all %d available profile-features received one; saw %d",
					key, availableSlots, len(firstRoundSeen))
			}
			break
		}
	}
	if len(coverage.CandidatesByProfile) != 2 ||
		len(coverage.CandidatesByFeature) == 0 {
		t.Fatalf("candidate coverage counters incomplete: %+v", coverage)
	}
	repeated, repeatedLedger, err := Derive(cells, matrix, protocol.MaxTotalCandidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, _ := HashCandidates(candidates)
	secondHash, _ := HashCandidates(repeated)
	if firstHash != secondHash || len(repeatedLedger.Entries) == 0 {
		t.Fatal("large fair enumeration must be deterministic")
	}
}

func TestOptionalAsOfAndStaleness(t *testing.T) {
	decision := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	series := optionalSeries{
		okxFunding: []optionalPoint{
			{Time: decision.Add(-time.Hour), First: 0.02},
			{Time: decision.Add(time.Hour), First: 9},
		},
		bybitFunding: []optionalPoint{{Time: decision.Add(-time.Hour), First: -0.01}},
		rubikTaker: []optionalPoint{
			{Time: decision.Add(-time.Hour), First: 9, Last: 1},
			{Time: decision, First: 1, Last: 9},
		},
		rubikRatio: []optionalPoint{{Time: decision.Add(-26 * time.Hour), First: 2}},
	}
	features := series.featuresAt(decision)
	if got := features["okx_bybit_funding_spread"]; got != 0.03 {
		t.Fatalf("funding as-of mismatch: %v", got)
	}
	if got := features["rubik_taker_imbalance_1h"]; got != 0.8 {
		t.Fatalf("rubik must use last closed bar: %v", got)
	}
	if _, ok := features["rubik_lsratio_1h"]; ok {
		t.Fatal("stale optional feature must be absent")
	}
}

func TestOptionalCatalogTamperFailsClosed(t *testing.T) {
	root := t.TempDir()
	rel := "okx_funding/BTC-USDT-SWAP.jsonl"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"data":{"fundingRate":"0.001","fundingTime":"1704067200000"}}` + "\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	catalog := historydata.HistoryCatalog{
		Families: []historydata.FamilyCatalog{{
			Name: "okx_funding",
			Files: []historydata.FileStats{{
				Family: "okx_funding", Path: rel, Size: int64(len(raw)),
				FileHash: hex.EncodeToString(sum[:]), Lines: 1,
			}},
		}},
	}
	raw[10] ^= 1
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOptionalSeries(catalog, root, "BTC-USDT-SWAP", 10); err == nil {
		t.Fatal("declared optional file tamper must not be silently treated as missing")
	}
}

func TestManifestBindsCoverageHash(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		MapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 1, CandidateLimit: 1,
		ProgressEvery: 1, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	coverage, coverageHash, err := BuildCoverage(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildCandidateManifest(
		cfg, "catalog", map[string]string{"p": "m"}, nil, nil,
		CandidateLedger{}, coverage, coverageHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CoverageHash = "tampered"
	if err := ValidateCandidateManifest(manifest); err == nil {
		t.Fatal("coverage hash tamper must invalidate manifest")
	}
}

func TestReverseMineVersionDriftRejected(t *testing.T) {
	cell := CellWorkItem{
		ProfileID: profitmap.ProfileOKX1H,
		Key: profitmap.CellKey{
			SkyCoarse: "sky", Liquidity: "liq_1", SymbolClass: "major",
			Horizon: "1h", Direction: "long", CapitalTier: 30,
		},
	}
	candidate, err := buildCandidate(1, cell, []Condition{{
		FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 1, Quantile: 0.5,
	}}, 1, "source")
	if err != nil {
		t.Fatal(err)
	}
	candidate.DerivationAlgorithm = "stale-v1"
	if err := ValidateCandidate(candidate); err == nil {
		t.Fatal("candidate version drift must fail protocol binding")
	}
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		MapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 1, CandidateLimit: 1,
		ProgressEvery: 1, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	coverage, coverageHash, err := BuildCoverage(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildCandidateManifest(
		cfg, "catalog", map[string]string{"p": "m"}, nil, nil,
		CandidateLedger{}, coverage, coverageHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DerivationAlgorithm = "stale-v1"
	if err := ValidateCandidateManifest(manifest); err == nil {
		t.Fatal("manifest/load version drift must fail protocol binding")
	}
}

func TestPhenomenonIgnoresCapitalAndSummarizesSubpaths(t *testing.T) {
	conditions := []Condition{{
		FeatureKey: "symbol_return_1h", Op: OpGTE, Threshold: 5, Quantile: 0.5,
	}}
	firstCell := CellWorkItem{
		ProfileID: profitmap.ProfileOKX1H,
		Key: profitmap.CellKey{
			SkyCoarse: "sky", Liquidity: "liq_5", SymbolClass: "major",
			Horizon: "1h", Direction: "long", CapitalTier: 30,
		},
	}
	secondCell := firstCell
	secondCell.Key.CapitalTier = 1000
	secondCell.Key.Liquidity = "liq_8"
	secondCell.Key.SymbolClass = "long_tail"
	first, err := buildCandidate(1, firstCell, conditions, 1, "source")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildCandidate(2, secondCell, conditions, 1, "source")
	if err != nil {
		t.Fatal(err)
	}
	if first.PhenomenonID != second.PhenomenonID {
		t.Fatal("capital/liquidity/symbolclass must not split phenomenon identity")
	}
	coverage, _, err := BuildCoverage(
		[]CellWorkItem{firstCell, secondCell}, nil, []Candidate{first, second},
	)
	if err != nil {
		t.Fatal(err)
	}
	summary := BuildSummary(
		[]CellWorkItem{firstCell, secondCell}, []Candidate{first, second},
		CandidateLedger{}, CandidateManifest{}, coverage, 2, 2,
	)
	if summary.CandidateCount != 2 || summary.UniquePhenomenonCount != 1 ||
		summary.SubpathCount != 2 {
		t.Fatalf("dishonest phenomenon summary: %+v", summary)
	}
	tiers := summary.CapitalTiersByPhenomenon[first.PhenomenonID]
	if len(tiers) != 2 || tiers[0] != 30 || tiers[1] != 1000 {
		t.Fatalf("capital tiers not aggregated by phenomenon: %v", tiers)
	}
}

func TestFeatureSummaryDistinguishesNamesFromProfileSlots(t *testing.T) {
	cells := []CellWorkItem{
		{ProfileID: profitmap.ProfileOKX1H, Key: profitmap.CellKey{Horizon: "1h"}},
		{ProfileID: profitmap.ProfileOKX5M, Key: profitmap.CellKey{Horizon: "5m"}},
	}
	matrix := WinnerFeatureMatrix{
		cellID(cells[0]): {"a": {
			"quote_volume_24h_log_bucket": 1,
			"symbol_return_1h":            2,
		}},
		cellID(cells[1]): {"b": {
			"quote_volume_24h_log_bucket": 1,
		}},
	}
	candidate := Candidate{
		ProfileID: profitmap.ProfileOKX1H, CellKey: cells[0].Key,
		Conditions: []Condition{{FeatureKey: "quote_volume_24h_log_bucket"}},
	}
	coverage, _, err := BuildCoverage(cells, matrix, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	slots := 0
	for _, names := range coverage.AvailableProfileFeatureSlots {
		slots += len(names)
	}
	if slots != 3 {
		t.Fatalf("available profile slots = %d, want 3", slots)
	}
	if len(coverage.DeclaredDistinctFeatureNames) != len(protocol.DeclaredDistinctFeatureNames()) {
		t.Fatal("declared distinct names do not match protocol")
	}
	if len(coverage.CoveredDistinctFeatureNames) != 1 ||
		coverage.CoveredDistinctFeatureNames[0] != "quote_volume_24h_log_bucket" {
		t.Fatalf("covered distinct names dishonest: %v", coverage.CoveredDistinctFeatureNames)
	}
	if len(coverage.MissingDeclaredFeatureNames) == 0 {
		t.Fatal("missing declared feature names must be reported")
	}
}

func testBars(base time.Time, count int, duration time.Duration, factor float64) []profitmap.Bar {
	bars := make([]profitmap.Bar, 0, count)
	price := 100.0
	for i := 0; i < count; i++ {
		bars = append(bars, profitmap.Bar{
			OpenTime: base.Add(time.Duration(i) * duration),
			Open:     price, High: price * 1.01, Low: price * 0.99,
			Close: price * factor, QuoteVolume: 1e6, Confirmed: true,
		})
		price *= factor
	}
	return bars
}
