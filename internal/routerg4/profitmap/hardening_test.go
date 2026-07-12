package profitmap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/protocol"
)

func TestMaxFilesAlphabeticPrefixRejectedByDefault(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	out := filepath.Join(root, "max-files-out")
	runner := Runner{
		Config: RunConfig{
			SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: root,
			OutputDir: out, MaxFiles: 1, MaxLines: 5000, CatalogSymbolLimit: 0,
			ProgressEvery: protocol.ScanProgressEveryLines, Generation: protocol.Generation,
			ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog: catalog, CheckpointPath: filepath.Join(out, "checkpoint.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
	if err == nil {
		t.Fatalf("silent alphabetical prefix must fail, got %+v", result.Manifest)
	}
}

func TestExplicitTruncatedSmokeIsLabeled(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	out := filepath.Join(root, "smoke-out")
	runner := Runner{
		Config: RunConfig{
			SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: root,
			OutputDir: out, MaxFiles: 1, MaxLines: 5000, CatalogSymbolLimit: 0,
			AllowTruncatedSmoke: true,
			ProgressEvery:       protocol.ScanProgressEveryLines, Generation: protocol.Generation,
			ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog: catalog, CheckpointPath: filepath.Join(out, "checkpoint.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 || !result.Manifest.SelectionTruncated {
		t.Fatalf("max-files not enforced: symbols=%v manifest=%+v", result.Symbols, result.Manifest)
	}
	if result.Manifest.AvailablePrimaryFiles != 2 || result.Manifest.SelectedPrimaryFiles != 1 {
		t.Fatalf("selection counts wrong: %+v", result.Manifest)
	}
	if result.Manifest.FullMarket || result.Manifest.MapScope != protocol.TruncatedSmokeScope {
		t.Fatalf("truncated output not explicitly smoke: %+v", result.Manifest)
	}
}

func TestAllowTruncatedSmokeBoundIntoConfig(t *testing.T) {
	base := RunConfig{
		SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: "/history",
		OutputDir: "/out", MaxFiles: 1, MaxLines: 100, CatalogSymbolLimit: 0,
		ProgressEvery: 100000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	fullHash, err := ConfigFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.AllowTruncatedSmoke = true
	smokeHash, err := ConfigFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if fullHash == smokeHash {
		t.Fatal("allow-truncated-smoke must be checkpoint/config bound")
	}
}

func TestFundingReverseOrderStrictAndTamper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "funding.jsonl")
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []string{
		fundingLine(base.Add(8*time.Hour), "0.002"),
		fundingLine(base, "0.001"),
	}
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	points, err := loadFundingJSONL(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || !points[0].Time.Equal(base) || !points[1].Time.Equal(base.Add(8*time.Hour)) {
		t.Fatalf("funding not sorted: %+v", points)
	}
	if err := os.WriteFile(path, []byte(fundingLine(base, "not-a-number")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFundingJSONL(path, 10); err == nil {
		t.Fatal("invalid funding rate must fail")
	}
}

func TestDeclaredFundingHashTamperFails(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	path := filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl")
	file, ok := CatalogFile(catalog, "okx_funding/BTC-USDT-SWAP.jsonl")
	if !ok || file.FileHash == "" {
		t.Fatal("fixture funding identity missing")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 1
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCatalogFile(catalog, root, "okx_funding/BTC-USDT-SWAP.jsonl"); err == nil {
		t.Fatal("declared funding tamper must fail")
	}
}

func TestStreamSymbolUsesCallback(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := makeTrendBars(base, 80, 1.001)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	cfg := ScanSymbolConfig{
		Spec: spec,
		Boundaries: Boundaries{
			Start: base, DiscoveryEnd: base.Add(80 * time.Hour), End: base.Add(80 * time.Hour),
		},
		SkyCutpoints: frozenTestCutpoints(t), SkySeries: sky.Series{},
		SymbolClassLabel: SymbolClassMajor,
	}
	count := 0
	store := NewMapStore()
	if err := StreamSymbol(bars, "BTC-USDT-SWAP", cfg, func(opp Opportunity) error {
		count++
		store.Add(opp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count == 0 || store.CellCount() == 0 {
		t.Fatalf("stream callback not used: count=%d cells=%d", count, store.CellCount())
	}
}

func TestDoneResumeVerifiesArtifactsAndRestoresCells(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	out := filepath.Join(root, "resume-out")
	runner := smokeRunner(root, out, catalog)
	first, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if second.Store.CellCount() != first.Store.CellCount() || second.Store.CellCount() == 0 {
		t.Fatalf("done resume lost cells: first=%d second=%d", first.Store.CellCount(), second.Store.CellCount())
	}
	mapPath := filepath.Join(out, "map_discovery_okx_1h.json")
	file, err := os.OpenFile(mapPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("tamper"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := runner.Run(t.Context()); err == nil {
		t.Fatal("done resume must reject tampered map")
	}
}

func TestMissingFundingProducesUnknownWithoutBlockingPriceSky(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	series := sky.Series{}
	for i := 0; i < 200; i++ {
		series.Bars = append(series.Bars, sky.Bar{
			OpenTime: start.Add(time.Duration(i) * time.Hour),
			Close:    100 + float64(i),
		})
	}
	cuts := map[string][]float64{}
	for _, dimension := range sky.Dimensions {
		cuts[dimension] = []float64{0, 0}
	}
	cuts[sky.DimReturn24h] = []float64{0.05, 0.15}
	cuts[sky.DimRealizedVol24h] = []float64{0.001, 0.01}
	label := SkyAtDecision(sky.Cutpoints{
		Partition: sky.FitPartition, Cuts: cuts, Frozen: true,
	}, series, start.Add(200*time.Hour))
	if label == "ood" || !strings.Contains(label, "fund=unknown") {
		t.Fatalf("price sky blocked by optional funding: %q", label)
	}
}

func TestQuantilesDeterministicAndCapacityRate(t *testing.T) {
	makeStore := func(reverse bool) *MapStore {
		store := NewMapStore()
		for i := 0; i < 500; i++ {
			j := i
			if reverse {
				j = 499 - i
			}
			store.Add(Opportunity{
				Symbol: "S" + itoa(j%7), Partition: PartitionDiscovery,
				DecisionTime: time.Unix(int64(j), 0).UTC(), SkyCoarse: "sky",
				LiquidityBucket: "liq_1", SymbolClass: SymbolClassMid, Horizon: "1h",
				Direction: DirectionLong, CapitalTier: 100,
				GrossBPS: float64(j), FeeNetBPS: float64(j - 10),
				ConservativeBPS: float64(j - 20), CapacityFail: j%2 == 0,
			})
		}
		return store
	}
	a, b := makeStore(false), makeStore(true)
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Fatalf("bounded quantiles are not deterministic: %s != %s", ha, hb)
	}
	pm := a.ExportPartition(ProfileOKX1H, PartitionDiscovery)
	for _, cell := range pm.Cells {
		if cell.Stats.CapacityFailureRate < 0.49 || cell.Stats.CapacityFailureRate > 0.51 {
			t.Fatalf("capacity failure rate=%v", cell.Stats.CapacityFailureRate)
		}
		if cell.Stats.ConservativeP10BPS > cell.Stats.ConservativeP50BPS ||
			cell.Stats.ConservativeP50BPS > cell.Stats.ConservativeP90BPS {
			t.Fatalf("invalid quantile order: %+v", cell.Stats)
		}
	}
}

func TestSummaryCoversAllPartitions(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	out := filepath.Join(root, "summary-out")
	runner := smokeRunner(root, out, catalog)
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Summaries) != 3 {
		t.Fatalf("summaries=%d, want discovery/validation/walk-forward", len(result.Summaries))
	}
	if !result.Manifest.FullMarket || result.Manifest.MapScope != protocol.FullMarketScope ||
		result.Manifest.SelectionTruncated ||
		result.Manifest.AvailablePrimaryFiles != result.Manifest.SelectedPrimaryFiles {
		t.Fatalf("full manifest scope fields invalid: %+v", result.Manifest)
	}
	for _, summary := range result.Summaries {
		if !summary.FullMarket || summary.MapScope != protocol.FullMarketScope ||
			summary.SelectionTruncated ||
			summary.AvailablePrimaryFiles != summary.SelectedPrimaryFiles {
			t.Fatalf("full summary scope fields invalid: %+v", summary)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "map_discovery_okx_1h.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pm PartitionMap
	if err := json.Unmarshal(raw, &pm); err != nil {
		t.Fatal(err)
	}
	if !pm.FullMarket || pm.MapScope != protocol.FullMarketScope ||
		pm.SelectionTruncated || pm.AvailablePrimaryFiles != pm.SelectedPrimaryFiles {
		t.Fatalf("full partition scope fields invalid: %+v", pm)
	}
}

func smokeRunner(root, out string, catalog historydata.HistoryCatalog) Runner {
	return Runner{
		Config: RunConfig{
			SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: root,
			OutputDir: out, MaxFiles: 100, MaxLines: 5000, CatalogSymbolLimit: 0,
			ProgressEvery: protocol.ScanProgressEveryLines, Generation: protocol.Generation,
			ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog: catalog, CheckpointPath: filepath.Join(out, "checkpoint.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
}

func fundingLine(at time.Time, rate string) string {
	raw, _ := json.Marshal(map[string]any{"data": map[string]string{
		"fundingRate": rate, "fundingTime": itoa64(at.UnixMilli()),
	}})
	return string(raw)
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
