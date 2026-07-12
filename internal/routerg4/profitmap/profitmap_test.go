package profitmap

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg4/protocol"
)

func TestNextOpenEntry(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var bars []Bar
	price := 100.0
	for i := 0; i < 30; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		bars = append(bars, Bar{
			OpenTime: open, Open: price, High: price + 1, Low: price - 1,
			Close: price + 0.5, QuoteVolume: 1e6, Confirmed: true,
		})
		price *= 1.01
	}
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	boundaries := Boundaries{
		Start: base, DiscoveryEnd: base.Add(48 * time.Hour),
		ValidationStart: base.Add(72 * time.Hour), ValidationEnd: base.Add(96 * time.Hour),
		WFStart: base.Add(120 * time.Hour), End: base.Add(30 * time.Hour),
	}
	opps := ScanSymbol(bars, "TEST-USDT-SWAP", ScanSymbolConfig{
		Spec: spec, Boundaries: boundaries, SkyCutpoints: frozenTestCutpoints(t),
		SkySeries: sky.Series{}, SymbolClass: SymbolClassCutpoints{Frozen: true},
		SymbolClassLabel: SymbolClassMajor,
	})
	if len(opps) == 0 {
		t.Fatal("expected opportunities")
	}
	for _, opp := range opps {
		idx := BarIndexAt(bars, opp.DecisionTime, spec.BarDuration)
		if idx < 0 || idx+1 >= len(bars) {
			t.Fatalf("bad indices for %+v", opp)
		}
		if !bars[idx+1].OpenTime.Equal(opp.DecisionTime) {
			t.Fatalf("entry must be next bar open at decision close")
		}
		entry := bars[idx+1].Open
		exitIdx := idx + 1 + horizonBars(spec, opp.Horizon)
		if exitIdx >= len(bars) {
			continue
		}
		wantGross := SignedGrossBPS(entry, bars[exitIdx].Open, string(opp.Direction))
		if math.Abs(wantGross-opp.GrossBPS) > 1e-6 {
			t.Fatalf("next-open gross mismatch: want %v got %v", wantGross, opp.GrossBPS)
		}
	}
}

func TestCurrentBarRejected(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []Bar{{
		OpenTime: base, Open: 100, High: 101, Low: 99, Close: 100.5,
		QuoteVolume: 1e6, Confirmed: false,
	}}
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	if !RejectCurrentBarOpen(bars, 0, spec.BarDuration) {
		t.Fatal("unconfirmed bar must be rejected")
	}
	bars[0].Confirmed = true
	if RejectCurrentBarOpen(bars, 0, spec.BarDuration) {
		t.Fatal("confirmed closed bar should not be rejected")
	}
}

func TestLongShortSymmetric(t *testing.T) {
	entry, exit := 100.0, 110.0
	longG := SignedGrossBPS(entry, exit, string(DirectionLong))
	shortG := SignedGrossBPS(entry, exit, string(DirectionShort))
	if math.Abs(longG+shortG) > 1e-9 {
		t.Fatalf("long/short not symmetric: %v %v", longG, shortG)
	}
}

func TestCapitalCostMonotonic(t *testing.T) {
	in := CostInputs{
		RollingQuoteVolumeUSDT: 1e5,
		BarHigh:                101, BarLow: 99, BarMid: 100, FundingBPS: 1,
	}
	prev := math.Inf(1)
	for _, tier := range protocol.CapitalTiersUSDT {
		in.CapitalUSDT = float64(tier)
		_, cons, _ := ComputeReturns(50, in)
		if cons >= prev {
			t.Fatalf("conservative net must decrease with capital tier: tier=%d cons=%v prev=%v", tier, cons, prev)
		}
		prev = cons
	}
}

func TestCostWipeout(t *testing.T) {
	in := CostInputs{
		CapitalUSDT: 1000, RollingQuoteVolumeUSDT: 100,
		BarHigh: 120, BarLow: 80, BarMid: 100, FundingBPS: 5,
	}
	gross := 20.0
	_, cons, _ := ComputeReturns(gross, in)
	if gross > protocol.NeutralBandBPS && cons > 0 {
		t.Fatalf("expected cost wipeout, cons=%v", cons)
	}
}

func TestProfilePartitionsIndependent(t *testing.T) {
	spec1h, _ := ProfileSpecFor(ProfileOKX1H)
	spec5m, _ := ProfileSpecFor(ProfileOKX5M)
	bars1h := makeBTCSyntheticBars(t, spec1h.BarDuration, 500)
	bars5m := makeBTCSyntheticBars(t, spec5m.BarDuration, 500*12)
	b1, err := ComputeBoundaries(spec1h, bars1h)
	if err != nil {
		t.Fatal(err)
	}
	b5, err := ComputeBoundaries(spec5m, bars5m)
	if err != nil {
		t.Fatal(err)
	}
	if b1.DiscoveryEnd.Equal(b5.DiscoveryEnd) {
		t.Fatal("profiles must have independent partition cut points")
	}
	if b1.PurgeEmbargo <= 0 || b5.PurgeEmbargo <= 0 {
		t.Fatal("purge/embargo must be positive")
	}
}

func TestSelectedSymbolsFullCatalog(t *testing.T) {
	catalog := historydata.HistoryCatalog{
		Families: []historydata.FamilyCatalog{
			{Name: familyOKX1H, Files: []historydata.FileStats{
				{Path: "okx_1h/AAA-USDT-SWAP.jsonl", Empty: false},
				{Path: "okx_1h/BBB-USDT-SWAP.jsonl", Empty: false},
				{Path: "okx_1h/EMPTY-USDT-SWAP.jsonl", Empty: true},
			}},
			{Name: familyOKX5M, Files: []historydata.FileStats{
				{Path: "okx_5m/CCC-USDT-SWAP.jsonl", Empty: false},
			}},
		},
	}
	got, err := SelectedSymbols(catalog, ProfileOKX1H, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "AAA-USDT-SWAP" || got[1] != "BBB-USDT-SWAP" {
		t.Fatalf("full catalog selection = %v", got)
	}
	limited, err := SelectedSymbols(catalog, ProfileOKX5M, 1)
	if err != nil || len(limited) != 1 || limited[0] != "CCC-USDT-SWAP" {
		t.Fatalf("limited selection = %v err=%v", limited, err)
	}
}

func TestCheckpointResumeDriftTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: dir,
		OutputDir: dir, MaxFiles: 10, MaxLines: 1000, CatalogSymbolLimit: 0,
		ProgressEvery: 100000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		ProfileID: ProfileOKX1H, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		CatalogHash: "abc", CatalogSymbolCount: 2, ConfigHash: cfgHash,
		BoundariesHash: "b", CutpointsHash: "c", SymbolClassHash: "d",
		OutputDir: dir, Done: false, NextSymbolIndex: 1,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.NextSymbolIndex != 1 {
		t.Fatalf("resume index = %d", loaded.NextSymbolIndex)
	}
	raw, _ := os.ReadFile(cpPath)
	rawStr := string(raw)
	tampered := strings.Replace(rawStr, `"next_symbol_index": 1`, `"next_symbol_index": 9`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
	if err := VerifyCheckpointBinding(loaded, cfg, "xyz", 2, "", MapScope{}, "b", "c", "d"); err == nil {
		t.Fatal("catalog hash drift must be rejected")
	}
}

func TestBoundedCellCountDeterminism(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := makeTrendBars(base, 200, 1.002)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	boundaries := Boundaries{
		Start: base, DiscoveryEnd: base.Add(200 * time.Hour),
		ValidationStart: base.Add(220 * time.Hour), ValidationEnd: base.Add(240 * time.Hour),
		WFStart: base.Add(260 * time.Hour), End: base.Add(200 * time.Hour),
	}
	cfg := ScanSymbolConfig{
		Spec: spec, Boundaries: boundaries, SkyCutpoints: frozenTestCutpoints(t),
		SkySeries: sky.Series{}, SymbolClass: SymbolClassCutpoints{Frozen: true, Upper: 1e12},
		SymbolClassLabel: SymbolClassMajor,
	}
	a := aggregateOpportunities(ScanSymbol(bars, "A", cfg))
	b := aggregateOpportunities(ScanSymbol(bars, "A", cfg))
	if a.CellCount() != b.CellCount() {
		t.Fatalf("cell count drift: %d vs %d", a.CellCount(), b.CellCount())
	}
	ha, _ := a.Hash()
	hb, _ := b.Hash()
	if ha != hb {
		t.Fatalf("determinism failed: %s vs %s", ha, hb)
	}
	if a.CellCount() > 5000 {
		t.Fatalf("unbounded cell count: %d", a.CellCount())
	}
}

func TestCoarseSkyWildcard(t *testing.T) {
	full := sky.SkyState{Label: "ret=low/te=mid/rv=high/dd=low/fund=mid"}
	got := CoarseSkyLabel(full)
	if !strings.Contains(got, "te=*") || !strings.Contains(got, "dd=*") {
		t.Fatalf("coarse sky missing wildcards: %q", got)
	}
}

func TestProgressEveryNotHardcoded20(t *testing.T) {
	if protocol.ScanProgressEveryLines != 100000 {
		t.Fatalf("protocol progress = %d, want 100000", protocol.ScanProgressEveryLines)
	}
}

func aggregateOpportunities(opps []Opportunity) *MapStore {
	store := NewMapStore()
	for _, opp := range opps {
		store.Add(opp)
	}
	return store
}

func frozenTestCutpoints(t *testing.T) sky.Cutpoints {
	t.Helper()
	cuts := map[string][]float64{}
	for _, dim := range sky.Dimensions {
		cuts[dim] = []float64{-1, 1}
	}
	return sky.Cutpoints{
		Partition: sky.FitPartition, Quantiles: []float64{0.33, 0.67},
		Cuts: cuts, FitSamples: 10, Frozen: true,
	}
}

func makeBTCSyntheticBars(t *testing.T, step time.Duration, n int) []Bar {
	t.Helper()
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	return makeTrendBars(base, n, 1.0005)
}

func makeTrendBars(base time.Time, n int, factor float64) []Bar {
	var bars []Bar
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		if i > 0 {
			open = bars[i-1].OpenTime.Add(time.Hour)
		}
		bars = append(bars, Bar{
			OpenTime: open, Open: price, High: price * 1.01, Low: price * 0.99,
			Close: price * factor, QuoteVolume: 5e6, Confirmed: true,
		})
		price *= factor
	}
	return bars
}

func horizonBars(spec ProfileSpec, label string) int {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h.BarCount
		}
	}
	return 1
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
	btc1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl"), familyOKX1H, "okx_1h/BTC-USDT-SWAP.jsonl")
	eth1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "ETH-USDT-SWAP.jsonl"), familyOKX1H, "okx_1h/ETH-USDT-SWAP.jsonl")
	btc5m := fixtureFileStats(t, filepath.Join(root, "okx_5m", "BTC-USDT-SWAP.jsonl"), familyOKX5M, "okx_5m/BTC-USDT-SWAP.jsonl")
	fund := fixtureFileStats(t, filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl"), "okx_funding", "okx_funding/BTC-USDT-SWAP.jsonl")
	catalog = buildValidCatalog(t, root, []historydata.FamilyCatalog{
		{Name: familyOKX1H, Files: []historydata.FileStats{btc1h, eth1h}},
		{Name: familyOKX5M, Files: []historydata.FileStats{btc5m}},
		{Name: "okx_funding", Files: []historydata.FileStats{fund}},
	})
	catalog.DataHash = protocol.HistoryDataHash
	return root, catalog
}

func writeInstrument(t *testing.T, root, family, instrument string, base time.Time, step time.Duration, n int, drift float64) {
	t.Helper()
	var lines []string
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(step * time.Duration(i))
		lines = append(lines, FormatCandleLine(open, price, price+1, price-1, price*drift, 2e6))
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

func TestRunnerSmoke(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: root,
		OutputDir: outDir, MaxFiles: 100, MaxLines: 5000, CatalogSymbolLimit: 0,
		ProgressEvery: protocol.ScanProgressEveryLines,
		Generation:    protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(outDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Store.CellCount() == 0 {
		t.Fatal("expected map cells")
	}
	if _, err := os.Stat(filepath.Join(outDir, "map_manifest.json")); err != nil {
		t.Fatal(err)
	}
}

var _ mechanism.Direction
