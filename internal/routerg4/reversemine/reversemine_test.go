package reversemine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/reversemine"
)

func TestCurrentBarForbidden(t *testing.T) {
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
	ev := reversemine.ValidationEvent{Features: map[string]float64{"current_open_bar": 1}}
	if err := reversemine.RejectForbiddenEventFeatures(ev.Features); err == nil {
		t.Fatal("forbidden current bar feature must be rejected")
	}
}

func TestMissingOptionalFeaturesAllowed(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := makeTrendBars(base, 80, 1.001)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	decision := bars[40].OpenTime.Add(time.Hour)
	features, err := reversemine.ComputeFeatures(spec, bars, bars, decision, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := features["okx_bybit_funding_spread"]; ok {
		t.Fatal("optional funding should be absent without loader")
	}
	if !isFinite(features["symbol_return_1h"]) {
		t.Fatalf("core return feature missing: %+v", features)
	}
}

func TestDeterministicRoundRobinAndLedgerComplete(t *testing.T) {
	cells, matrix := syntheticCells()
	limit := 16
	a1, l1, err := reversemine.Derive(cells, matrix, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, l2, err := reversemine.Derive(cells, matrix, limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	ha1, _ := reversemine.HashCandidates(a1)
	ha2, _ := reversemine.HashCandidates(a2)
	if ha1 != ha2 {
		t.Fatal("derivation must be deterministic")
	}
	if len(l1.Entries) == 0 {
		t.Fatal("ledger must record attempts")
	}
	for i, entry := range l1.Entries {
		if entry.AttemptOrdinal != i+1 {
			t.Fatalf("ledger ordinal gap at %d", i)
		}
		if entry.EnumeratorCursor == "" {
			t.Fatalf("ledger missing enumerator cursor at %d", i)
		}
	}
	if l1.LedgerHash != l2.LedgerHash {
		t.Fatal("ledger hash drift")
	}
	profiles := map[string]int{}
	directions := map[string]bool{}
	for _, c := range a1 {
		profiles[c.ProfileID]++
		directions[c.CellKey.Direction] = true
	}
	if len(profiles) < 2 {
		t.Fatalf("expected multi-profile coverage, got %v", profiles)
	}
	if !directions["long"] || !directions["short"] {
		t.Fatalf("expected long/short candidate coverage, got %v", directions)
	}
}

func TestCandidateCap1024(t *testing.T) {
	cells, matrix := syntheticCells()
	candidates, ledger, err := reversemine.Derive(cells, matrix, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) > 1024 {
		t.Fatalf("candidate cap exceeded: %d", len(candidates))
	}
	if len(ledger.Entries) < len(candidates) {
		t.Fatal("ledger must include non-registered attempts")
	}
}

func TestWinnerOnlySeedsNotPassClaim(t *testing.T) {
	for _, c := range []reversemine.Candidate{{Status: reversemine.CandidateStatus}} {
		if c.Status == "RESEARCH_PASS" || c.Status == "VALIDATION_PASS" {
			t.Fatal("discovery candidates must not claim pass")
		}
	}
	if reversemine.CandidateStatus != protocol.UnexplainedStatus {
		t.Fatalf("status must be unexplained: %s", reversemine.CandidateStatus)
	}
}

func TestRuleFiresExactCellDimensions(t *testing.T) {
	cell := reversemine.CellWorkItem{
		ProfileID: profitmap.ProfileOKX1H,
		Key: profitmap.CellKey{
			SkyCoarse: "ret=low/rv=mid", Liquidity: "liq_6", SymbolClass: "major",
			Horizon: "1h", Direction: "long", CapitalTier: 30,
		},
	}
	candidate, err := reversemineBuildTestCandidate(cell, []reversemine.Condition{
		{FeatureKey: "symbol_return_1h", Op: reversemine.OpGTE, Threshold: 0, Quantile: 0.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	match := reversemine.ValidationEvent{
		ProfileID: profitmap.ProfileOKX1H,
		CellKey:   cell.Key,
		Features:  map[string]float64{"symbol_return_1h": 10},
	}
	mismatch := match
	mismatch.CellKey.Direction = "short"
	fires, _ := candidate.Fires(match)
	if !fires {
		t.Fatal("expected rule to fire on exact cell")
	}
	fires, _ = candidate.Fires(mismatch)
	if fires {
		t.Fatal("must not fire when cell direction differs")
	}
}

func TestManifestAndCheckpointTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := reversemine.RunConfig{
		SchemaVersion: reversemine.SchemaVersionConfig,
		HistoryRoot:   dir, OutputDir: dir, MapDirs: []string{dir},
		MaxFiles: 10, MaxLines: 1000, CandidateLimit: 32,
		ProgressEvery: protocol.DiscoveryProgressEvery,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	cfgHash, _ := reversemine.ConfigFingerprint(cfg)
	cp := reversemine.Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		CatalogHash: "abc", OutputDir: dir, Done: false,
		MapManifestHashes: map[string]string{"okx_1h": "hash"},
	}
	if err := reversemine.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := reversemine.LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load checkpoint: ok=%v err=%v", ok, err)
	}
	if loaded.ConfigHash != cfgHash {
		t.Fatal("checkpoint config hash mismatch")
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"done": false`, `"done": true`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reversemine.LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}

	manifest := reversemine.CandidateManifest{
		SchemaVersion: reversemine.SchemaVersionManifest,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, SearchSpaceID: protocol.ReverseMineSearchSpaceID,
		DerivationAlgorithm: protocol.DerivationAlgorithmVersion,
		ConfigHash:          cfgHash, CatalogHash: "abc",
		MapManifestHashes: map[string]string{"okx_1h": "h"},
		CandidateLimit:    32, ActualCandidateCount: 0,
		FreezeBeforeValidation: true, ExamActions: 0,
	}
	coverage, coverageHash, err := reversemine.BuildCoverage(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := reversemine.BuildCandidateManifest(
		cfg, "abc", manifest.MapManifestHashes, nil, nil,
		reversemine.CandidateLedger{}, coverage, coverageHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := reversemine.ValidateCandidateManifest(sealed); err != nil {
		t.Fatal(err)
	}
	sealed.ActualCandidateCount = 99
	if err := reversemine.ValidateCandidateManifest(sealed); err == nil {
		t.Fatal("tampered manifest count must fail validation")
	}
}

func TestCheckpointResume(t *testing.T) {
	cells, matrix := syntheticCells()
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := reversemine.RunConfig{
		SchemaVersion: reversemine.SchemaVersionConfig,
		HistoryRoot:   dir, OutputDir: dir, MapDirs: []string{dir},
		MaxFiles: 10, MaxLines: 1000, CandidateLimit: 8,
		ProgressEvery: protocol.DiscoveryProgressEvery,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	cfgHash, _ := reversemine.ConfigFingerprint(cfg)
	cp := reversemine.Checkpoint{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ConfigHash: cfgHash,
		CatalogHash: "abc", OutputDir: dir,
		MapManifestHashes: map[string]string{"okx_1h": "h1", "okx_5m": "h2"},
		Done:              false, EnumeratorPhase: "single",
	}
	candidates, ledger, err := reversemine.Derive(cells, matrix, 8, &cp)
	if err != nil {
		t.Fatal(err)
	}
	cp.Candidates = candidates
	cp.LedgerEntries = ledger.Entries
	cp.CandidateCount = len(candidates)
	if err := reversemine.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := reversemine.LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("resume load: ok=%v err=%v", ok, err)
	}
	if loaded.CandidateCount != len(candidates) {
		t.Fatalf("resume candidate count = %d want %d", loaded.CandidateCount, len(candidates))
	}
}

func TestEndToEndSmokeTwoProfiles(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	map1h := runProfitMap(t, root, catalog, profitmap.ProfileOKX1H)
	map5m := runProfitMap(t, root, catalog, profitmap.ProfileOKX5M)
	outDir := filepath.Join(root, "reverse-out")
	cfg := reversemine.RunConfig{
		SchemaVersion: reversemine.SchemaVersionConfig,
		HistoryRoot:   root, OutputDir: outDir,
		MapDirs:  []string{map1h, map5m},
		MaxFiles: 100, MaxLines: 5000, CandidateLimit: 64,
		ProgressEvery: protocol.DiscoveryProgressEvery,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	runner := reversemine.Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(outDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	maxLiveSymbolBars := 0
	btcFamilies := map[string]string{}
	runner.OnSymbolLoaded = func(profileID, symbol string, liveSymbolBars int) {
		if liveSymbolBars > maxLiveSymbolBars {
			maxLiveSymbolBars = liveSymbolBars
		}
	}
	runner.OnBTCLoaded = func(profileID, primaryFamily string) {
		btcFamilies[profileID] = primaryFamily
	}
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.ExamActions != 0 {
		t.Fatal("exam_actions must be zero")
	}
	if result.Summary.DiscoveryCellCount == 0 {
		t.Fatal("expected discovery cells")
	}
	for _, path := range []string{
		"g4_candidate_ledger.json", "candidates.json", "candidate_manifest.json", "reverse_mine_summary.json",
	} {
		if _, err := os.Stat(filepath.Join(outDir, path)); err != nil {
			t.Fatalf("missing output %s: %v", path, err)
		}
	}
	if len(result.Summary.ProfileIDs) < 2 {
		t.Fatalf("expected two profile coverage, got %v", result.Summary.ProfileIDs)
	}
	cellDirs := map[string]bool{}
	for _, bundle := range []string{map1h, map5m} {
		b, err := reversemine.LoadMapBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range b.Discovery.Cells {
			cellDirs[cell.Key.Direction] = true
		}
	}
	if !cellDirs["long"] || !cellDirs["short"] {
		t.Fatalf("discovery map must include long and short cells: %v", cellDirs)
	}
	for _, c := range result.Candidates {
		if c.Status != reversemine.CandidateStatus {
			t.Fatalf("candidate status must be unexplained: %s", c.Status)
		}
	}
	if result.Summary.WinnerSeedCount == 0 {
		t.Fatal("expected winner seeds from reservoir")
	}
	if maxLiveSymbolBars != 1 {
		t.Fatalf("symbol bar memory must be O(one symbol), saw %d live", maxLiveSymbolBars)
	}
	if result.Manifest.CoverageHash == "" ||
		result.Manifest.CoverageHash != result.Summary.CoverageHash {
		t.Fatal("manifest must bind summary coverage hash")
	}
	if btcFamilies[profitmap.ProfileOKX5M] != "okx_5m" {
		t.Fatalf("5m BTC must load okx_5m, got %q", btcFamilies[profitmap.ProfileOKX5M])
	}
}

func runProfitMap(t *testing.T, root string, catalog historydata.HistoryCatalog, profile string) string {
	t.Helper()
	outDir := filepath.Join(root, "map-"+profile)
	cfg := profitmap.RunConfig{
		SchemaVersion: "test", ProfileID: profile, HistoryRoot: root,
		OutputDir: outDir, MaxFiles: 100, MaxLines: 5000, CatalogSymbolLimit: 0,
		ProgressEvery: protocol.ScanProgressEveryLines,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	runner := profitmap.Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(outDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	if _, err := runner.Run(t.Context()); err != nil {
		t.Fatalf("profit map %s: %v", profile, err)
	}
	formalizeMapManifest(t, outDir, true, protocol.FullMarketScope)
	return outDir
}

func formalizeMapManifest(t *testing.T, outputDir string, fullMarket bool, scope string) {
	t.Helper()
	path := filepath.Join(outputDir, "map_manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["full_market"] = fullMarket
	manifest["map_scope"] = scope
	manifest["manifest_hash"] = ""
	hash, _, err := datatruth.HashJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest["manifest_hash"] = hash
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedSmokeMapRejected(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	mapDir := runProfitMap(t, root, catalog, profitmap.ProfileOKX1H)
	formalizeMapManifest(t, mapDir, false, protocol.TruncatedSmokeScope)
	if _, err := reversemine.LoadMapBundle(mapDir); err == nil ||
		!strings.Contains(err.Error(), "full_market=true") {
		t.Fatalf("TRUNCATED_SMOKE must be rejected from candidate path: %v", err)
	}
}

func syntheticCells() ([]reversemine.CellWorkItem, reversemine.WinnerFeatureMatrix) {
	cells := []reversemine.CellWorkItem{
		{
			ProfileID: profitmap.ProfileOKX1H, ManifestHash: "m1",
			Key:     profitmap.CellKey{SkyCoarse: "ret=low/rv=mid", Liquidity: "liq_6", SymbolClass: "major", Horizon: "1h", Direction: "long", CapitalTier: 30},
			Winners: []profitmap.WinnerEntry{{Symbol: "AAA", DecisionTime: time.Date(2024, 1, 2, 1, 0, 0, 0, time.UTC), ConservativeBPS: 5}},
		},
		{
			ProfileID: profitmap.ProfileOKX1H, ManifestHash: "m1",
			Key:     profitmap.CellKey{SkyCoarse: "ret=mid/rv=low", Liquidity: "liq_5", SymbolClass: "mid", Horizon: "4h", Direction: "short", CapitalTier: 100},
			Winners: []profitmap.WinnerEntry{{Symbol: "BBB", DecisionTime: time.Date(2024, 1, 3, 1, 0, 0, 0, time.UTC), ConservativeBPS: 3}},
		},
		{
			ProfileID: profitmap.ProfileOKX5M, ManifestHash: "m2",
			Key:     profitmap.CellKey{SkyCoarse: "ret=high/rv=high", Liquidity: "liq_7", SymbolClass: "long_tail", Horizon: "5m", Direction: "long", CapitalTier: 1000},
			Winners: []profitmap.WinnerEntry{{Symbol: "CCC", DecisionTime: time.Date(2024, 1, 4, 1, 0, 0, 0, time.UTC), ConservativeBPS: 2}},
		},
	}
	matrix := reversemine.WinnerFeatureMatrix{}
	for _, cell := range cells {
		key := syntheticCellID(cell)
		matrix[key] = map[string]map[string]float64{
			"winner-a": {
				"symbol_return_1h": 10, "symbol_return_5m": 1,
				"quote_volume_24h_log_bucket": 5,
				"time_of_day_session":         0, "btc_relative_return": 1,
			},
			"winner-b": {
				"symbol_return_1h": 20, "symbol_return_5m": 2,
				"quote_volume_24h_log_bucket": 6,
				"time_of_day_session":         1, "btc_relative_return": -1,
			},
		}
	}
	return cells, matrix
}

func syntheticCellID(cell reversemine.CellWorkItem) string {
	k := cell.Key
	return cell.ProfileID + "|" + k.Horizon + "|" + k.Direction + "|" +
		fmt.Sprint(k.CapitalTier) + "|" + k.SkyCoarse + "|" + k.Liquidity + "|" + k.SymbolClass
}

func reversemineBuildTestCandidate(cell reversemine.CellWorkItem, conditions []reversemine.Condition) (reversemine.Candidate, error) {
	c := reversemine.Candidate{
		ProfileID: cell.ProfileID, CellKey: cell.Key, Conditions: conditions,
		Status: reversemine.CandidateStatus, SourceWinnerCount: 1, SourceWinnerIDsHash: "h",
		MapManifestHash: "m", WinnerSeedMode: reversemine.WinnerSeedMode,
		SearchSpaceID: protocol.ReverseMineSearchSpaceID, DerivationAlgorithm: protocol.DerivationAlgorithmVersion,
	}
	phenomenonID, err := reversemine.CandidatePhenomenonID(c)
	if err != nil {
		return reversemine.Candidate{}, err
	}
	c.PhenomenonID = phenomenonID
	hash, err := reversemine.CanonicalCandidateHash(c)
	if err != nil {
		return reversemine.Candidate{}, err
	}
	c.CanonicalHash = hash
	return c, nil
}

func isFinite(v float64) bool {
	return v == v && v < 1e100 && v > -1e100
}

// fixture helpers copied from profitmap tests (same package path via duplicate to avoid export churn).

func writeFixtureTree(t *testing.T) (root string, catalog historydata.HistoryCatalog) {
	t.Helper()
	root = t.TempDir()
	for _, sub := range []string{"okx_1h", "okx_5m", "okx_funding"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	writeInstrument(t, root, "okx_1h", "BTC-USDT-SWAP", base, time.Hour, 800, 1.0004)
	writeInstrument(t, root, "okx_1h", "ETH-USDT-SWAP", base, time.Hour, 800, 1.0006)
	writeInstrument(t, root, "okx_5m", "BTC-USDT-SWAP", base, 5*time.Minute, 800*12, 1.0001)
	writeFunding(t, root, "BTC-USDT-SWAP", base, 800)
	btc1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/BTC-USDT-SWAP.jsonl")
	eth1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "ETH-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/ETH-USDT-SWAP.jsonl")
	btc5m := fixtureFileStats(t, filepath.Join(root, "okx_5m", "BTC-USDT-SWAP.jsonl"), "okx_5m", "okx_5m/BTC-USDT-SWAP.jsonl")
	fund := fixtureFileStats(t, filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl"), "okx_funding", "okx_funding/BTC-USDT-SWAP.jsonl")
	catalog = buildValidCatalog(t, root, []historydata.FamilyCatalog{
		{Name: "okx_1h", Files: []historydata.FileStats{btc1h, eth1h}},
		{Name: "okx_5m", Files: []historydata.FileStats{btc5m}},
		{Name: "okx_funding", Files: []historydata.FileStats{fund}},
	})
	catalog.DataHash = protocol.HistoryDataHash
	catalog.DatasetID = "history.test.smoke"
	return root, catalog
}

func writeInstrument(t *testing.T, root, family, instrument string, base time.Time, step time.Duration, n int, drift float64) {
	t.Helper()
	var lines []string
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(step * time.Duration(i))
		lines = append(lines, profitmap.FormatCandleLine(open, price, price+1, price-1, price*drift, 2e6))
		price *= drift
	}
	path := filepath.Join(root, family, instrument+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFunding(t *testing.T, root, instrument string, base time.Time, hours int) {
	t.Helper()
	var lines []string
	for i := 0; i < hours; i += 8 {
		row := map[string]any{"data": map[string]string{
			"fundingRate": "0.0001",
			"fundingTime": fmt.Sprint(base.Add(time.Duration(i) * time.Hour).UnixMilli()),
		}}
		raw, _ := json.Marshal(row)
		lines = append(lines, string(raw))
	}
	path := filepath.Join(root, "okx_funding", instrument+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeTrendBars(base time.Time, n int, factor float64) []profitmap.Bar {
	var bars []profitmap.Bar
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		bars = append(bars, profitmap.Bar{
			OpenTime: open, Open: price, High: price * 1.01, Low: price * 0.99,
			Close: price * factor, QuoteVolume: 5e6, Confirmed: true,
		})
		price *= factor
	}
	return bars
}

func buildValidCatalog(t *testing.T, root string, families []historydata.FamilyCatalog) historydata.HistoryCatalog {
	t.Helper()
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	var files []historydata.FileStats
	for _, family := range families {
		for _, file := range family.Files {
			f := file
			f.Family = family.Name
			files = append(files, f)
		}
	}
	return rebuildCatalog(root, files, now)
}

func rebuildCatalog(rootDir string, files []historydata.FileStats, now time.Time) historydata.HistoryCatalog {
	familyBuckets := map[string][]historydata.FileStats{}
	for _, file := range files {
		familyBuckets[file.Family] = append(familyBuckets[file.Family], file)
	}
	familyNames := make([]string, 0, len(familyBuckets))
	for family := range familyBuckets {
		familyNames = append(familyNames, family)
	}
	sort.Strings(familyNames)
	catalog := historydata.HistoryCatalog{
		SchemaVersion: historydata.CatalogSchemaVersion,
		RootDir:       rootDir,
		CreatedAt:     now.UTC(),
	}
	dataParts := make([]string, 0, len(files))
	totalLines := int64(0)
	for _, family := range familyNames {
		bucket := familyBuckets[family]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Path < bucket[j].Path })
		entry := historydata.FamilyCatalog{Name: family, Files: bucket}
		for _, file := range bucket {
			entry.FilesScanned++
			entry.LinesScanned += file.Lines
			totalLines += file.Lines
			dataParts = append(dataParts, fmt.Sprintf("%s\x00%s\x00%s\x00%d", file.Family, file.Path, file.FileHash, file.Lines))
		}
		catalog.Families = append(catalog.Families, entry)
	}
	catalog.FilesScanned = len(files)
	catalog.LinesScanned = totalLines
	catalog.DataHash = datatruth.HashStrings(dataParts...)
	return catalog
}

func fixtureFileStats(t *testing.T, path, family, rel string) historydata.FileStats {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return historydata.FileStats{
		Family: family, Path: rel, Size: info.Size(),
		FileHash: hex.EncodeToString(sum[:]),
		Lines:    int64(strings.Count(string(raw), "\n")),
	}
}
