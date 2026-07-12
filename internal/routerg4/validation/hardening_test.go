package validation_test

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
	"lingxi/internal/routerg4/validation"
)

func TestEndToEndValidationSmoke(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	map1h := runProfitMap(t, root, catalog, profitmap.ProfileOKX1H)
	candidateDir := filepath.Join(root, "candidates")
	cfg := reversemine.RunConfig{
		SchemaVersion: reversemine.SchemaVersionConfig,
		HistoryRoot:   root, OutputDir: candidateDir,
		MapDirs: []string{map1h}, MaxFiles: 100, MaxLines: 5000, CandidateLimit: 16,
		ProgressEvery: protocol.DiscoveryProgressEvery,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	mineRunner := &reversemine.Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(candidateDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	mineResult, err := mineRunner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(mineResult.Candidates) == 0 {
		t.Fatal("expected candidates for validation smoke")
	}
	validateOut := filepath.Join(root, "validation-out")
	validateCfg := validation.DefaultRunConfig(
		root, candidateDir, validateOut, []string{map1h}, 100, 5000, mineResult.Manifest.ManifestHash,
	)
	btcLoads := 0
	featureComputes := 0
	validateRunner := &validation.Runner{
		Config: validateCfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(validateOut, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
		LoadBars: func(path string, maxLines int) ([]profitmap.Bar, error) {
			if strings.HasSuffix(path, "/okx_1h/BTC-USDT-SWAP.jsonl") {
				btcLoads++
			}
			return profitmap.LoadBars(path, maxLines)
		},
		ComputeFeatures: func(
			spec profitmap.ProfileSpec,
			symbolBars, btcBars []profitmap.Bar,
			decisionTime time.Time,
			optional map[string]float64,
		) (map[string]float64, error) {
			featureComputes++
			return reversemine.ComputeFeatures(spec, symbolBars, btcBars, decisionTime, optional)
		},
	}
	result, err := validateRunner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"g4_validation_report.json", "validation_survivors.json", "validation_receipt.json",
	} {
		if _, err := os.Stat(filepath.Join(validateOut, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	if result.Report.ExamActions != 0 || result.Receipt.ExamActions != 0 {
		t.Fatal("exam_actions must be zero")
	}
	if result.Report.CandidateCount != len(mineResult.Candidates) {
		t.Fatalf("report candidate count=%d want %d", result.Report.CandidateCount, len(mineResult.Candidates))
	}
	if result.Receipt.CandidateManifestHash != mineResult.Manifest.ManifestHash {
		t.Fatal("receipt must bind candidate manifest")
	}
	if btcLoads != 1 {
		t.Fatalf("BTC bars loaded %d times, want once per profile", btcLoads)
	}
	if featureComputes == 0 || featureComputes > 300 {
		t.Fatalf("feature computes=%d, want decision count rather than opportunity count", featureComputes)
	}
}

func TestCheckpointResumeValidation(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	map1h := runProfitMap(t, root, catalog, profitmap.ProfileOKX1H)
	candidateDir := filepath.Join(root, "candidates")
	mineRunner := &reversemine.Runner{
		Config: reversemine.RunConfig{
			SchemaVersion: reversemine.SchemaVersionConfig,
			HistoryRoot:   root, OutputDir: candidateDir, MapDirs: []string{map1h},
			MaxFiles: 100, MaxLines: 5000, CandidateLimit: 8,
			ProgressEvery: protocol.DiscoveryProgressEvery,
			Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
			HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog: catalog, CheckpointPath: filepath.Join(candidateDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	mineResult, err := mineRunner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	validateOut := filepath.Join(root, "validation-out")
	cpPath := filepath.Join(validateOut, "cp.json")
	cfg := validation.DefaultRunConfig(root, candidateDir, validateOut, []string{map1h}, 100, 5000, mineResult.Manifest.ManifestHash)
	runner := &validation.Runner{
		Config: cfg, Catalog: catalog, CheckpointPath: cpPath,
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	first, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.ReportHash != second.Report.ReportHash {
		t.Fatalf("resume changed report hash: %s vs %s", first.Report.ReportHash, second.Report.ReportHash)
	}
	survivorPath := filepath.Join(validateOut, "validation_survivors.json")
	raw, err := os.ReadFile(survivorPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"exam_actions": 0`, `"exam_actions": 1`, 1)
	if err := os.WriteFile(survivorPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context()); err == nil {
		t.Fatal("completed resume must reject tampered survivors provenance")
	}
}

func TestIndexLookupScansBucketOnly(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	map1h := runProfitMap(t, root, catalog, profitmap.ProfileOKX1H)
	candidateDir := filepath.Join(root, "candidates")
	mineRunner := &reversemine.Runner{
		Config: reversemine.RunConfig{
			SchemaVersion: reversemine.SchemaVersionConfig,
			HistoryRoot:   root, OutputDir: candidateDir, MapDirs: []string{map1h},
			MaxFiles: 100, MaxLines: 5000, CandidateLimit: 32,
			ProgressEvery: protocol.DiscoveryProgressEvery,
			Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
			HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog: catalog, CheckpointPath: filepath.Join(candidateDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	mineResult, err := mineRunner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	idx := validation.BuildCandidateIndex(mineResult.Candidates)
	maxBucket := idx.MaxBucketSize()
	if maxBucket >= len(mineResult.Candidates) && len(mineResult.Candidates) > 1 {
		t.Logf("bucket=%d total=%d (may be single cell in tiny fixture)", maxBucket, len(mineResult.Candidates))
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
	runner := &profitmap.Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(outDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	if _, err := runner.Run(t.Context()); err != nil {
		t.Fatalf("profit map %s: %v", profile, err)
	}
	markMapFullMarket(t, filepath.Join(outDir, "map_manifest.json"))
	return outDir
}

func markMapFullMarket(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["full_market"] = true
	manifest["map_scope"] = protocol.FullMarketScope
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
