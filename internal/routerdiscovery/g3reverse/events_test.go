package g3reverse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
	"lingxi/internal/routerg3/protocol"
)

func writeFixtureHistory(t *testing.T, withRubik bool) (root string, catalog historydata.HistoryCatalog) {
	t.Helper()
	root = t.TempDir()
	priceDir := filepath.Join(root, "okx_1h")
	fundingDir := filepath.Join(root, "okx_funding")
	bybitDir := filepath.Join(root, "bybit_funding")
	if err := os.MkdirAll(priceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fundingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bybitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	instrument := "BTC-USDT-SWAP"
	symbol := "BTCUSDT"
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	var priceLines []string
	price := 100.0
	totalHours := 1200
	for i := 0; i < totalHours; i++ {
		switch {
		case i >= 175 && i < 200:
			price *= 0.985
		case i >= 210 && i < 235:
			price *= 1.003
		default:
			price *= 1.001
		}
		row := map[string]any{"data": []string{
			fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
			fmt.Sprint(price), fmt.Sprint(price + 1), fmt.Sprint(price - 1),
			fmt.Sprint(price + 0.5), "1", "1", "5000000", "1",
		}, "fetched_at": baseTime.Add(time.Duration(i)*time.Hour + 48*time.Hour).UnixMilli()}
		raw, _ := json.Marshal(row)
		priceLines = append(priceLines, string(raw))
	}
	pricePath := filepath.Join(priceDir, instrument+".jsonl")
	if err := os.WriteFile(pricePath, []byte(strings.Join(priceLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var fundingLines []string
	for i := 0; i < totalHours; i += 8 {
		row := map[string]any{"data": map[string]string{
			"fundingRate": "0.002",
			"fundingTime": fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
			"instId":      instrument,
		}, "fetched_at": fmt.Sprint(baseTime.Add(time.Duration(i)*time.Hour + 72*time.Hour).UnixMilli())}
		raw, _ := json.Marshal(row)
		fundingLines = append(fundingLines, string(raw))
	}
	fundingPath := filepath.Join(fundingDir, instrument+".jsonl")
	if err := os.WriteFile(fundingPath, []byte(strings.Join(fundingLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var bybitLines []string
	for i := 0; i < totalHours; i += 8 {
		row := map[string]any{"data": map[string]string{
			"fundingRate":          "0.0015",
			"fundingRateTimestamp": fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
			"symbol":               symbol,
		}, "fetched_at": fmt.Sprint(baseTime.Add(time.Duration(i)*time.Hour + 72*time.Hour).UnixMilli())}
		raw, _ := json.Marshal(row)
		bybitLines = append(bybitLines, string(raw))
	}
	bybitPath := filepath.Join(bybitDir, symbol+".jsonl")
	if err := os.WriteFile(bybitPath, []byte(strings.Join(bybitLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	families := []historydata.FamilyCatalog{
		{Name: familyOKXFunding, Files: []historydata.FileStats{fixtureFileStats(t, fundingPath, familyOKXFunding+"/"+instrument+".jsonl")}},
		{Name: familyOKX1h, Files: []historydata.FileStats{fixtureFileStats(t, pricePath, familyOKX1h+"/"+instrument+".jsonl")}},
		{Name: familyBybitFunding, Files: []historydata.FileStats{fixtureFileStats(t, bybitPath, familyBybitFunding+"/"+symbol+".jsonl")}},
	}
	if withRubik {
		rubikDir := filepath.Join(root, familyRubikTaker1h)
		if err := os.MkdirAll(rubikDir, 0o755); err != nil {
			t.Fatal(err)
		}
		var rubikLines []string
		for i := 0; i < totalHours; i++ {
			row := map[string]any{"data": []string{
				fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
				"100", "80",
			}, "fetched_at": baseTime.Add(time.Duration(i)*time.Hour + 96*time.Hour).UnixMilli()}
			raw, _ := json.Marshal(row)
			rubikLines = append(rubikLines, string(raw))
		}
		rubikPath := filepath.Join(rubikDir, "BTC.jsonl")
		if err := os.WriteFile(rubikPath, []byte(strings.Join(rubikLines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		families = append(families, historydata.FamilyCatalog{
			Name: familyRubikTaker1h, Files: []historydata.FileStats{fixtureFileStats(t, rubikPath, familyRubikTaker1h+"/BTC.jsonl")},
		})
	}

	catalog = historydata.HistoryCatalog{
		DatasetID: "history.test", RootDir: root, DataHash: protocol.HistoryDataHash, SchemaHash: "test-schema-hash",
		Families: families,
	}
	return root, catalog
}

func fixtureFileStats(t *testing.T, path, rel string) historydata.FileStats {
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
		Path: rel, Size: info.Size(), FileHash: hex.EncodeToString(sum[:]), Lines: int64(bytes.Count(raw, []byte{'\n'})),
	}
}

func testCutpoints() sky.Cutpoints {
	cuts := map[string][]float64{
		sky.DimReturn24h:          {-0.01, 0.01},
		sky.DimTrendEfficiency24h: {0.3, 0.7},
		sky.DimRealizedVol24h:     {0.01, 0.03},
		sky.DimDrawdown168h:       {0.05, 0.20},
		sky.DimFundingState:       {0.0001, 0.001},
	}
	return sky.Cutpoints{
		Partition: sky.FitPartition, Quantiles: []float64{0.3333333333, 0.6666666667},
		Cuts: cuts, FitSamples: 10, Frozen: true,
	}
}

func fixtureConfig(t *testing.T, root string, catalog historydata.HistoryCatalog) Config {
	t.Helper()
	_, boundaries, err := AnchorFundingSpan(root, 500)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(),
		MaxSymbols: 10, MaxFundingLines: 500, MaxPriceLines: 5000, MaxFamilyLines: 5000,
		Start: boundaries.Start, End: boundaries.End,
	}
}

func TestBuildEventsIsDeterministic(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	first, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	hashA, err := HashEvents(first)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := HashEvents(second)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB || len(first) != len(second) {
		t.Fatalf("deterministic regeneration must match: %d (%s) vs %d (%s)", len(first), hashA, len(second), hashB)
	}
	if len(first) == 0 {
		t.Fatal("fixture must produce events")
	}
}

func TestBuildEventsEmitsBothHorizonsAndDirectionsPerDecisionPoint(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	byDecision := map[int64]map[string]bool{}
	for _, e := range events {
		key := e.DecisionTime.UnixMilli()
		if byDecision[key] == nil {
			byDecision[key] = map[string]bool{}
		}
		byDecision[key][string(e.Horizon)+"|"+string(e.Direction)] = true
	}
	for key, combos := range byDecision {
		for _, horizon := range []Horizon{Horizon1h, Horizon4h, Horizon24h} {
			for _, direction := range []string{"long", "short"} {
				if !combos[string(horizon)+"|"+direction] {
					t.Fatalf("decision %d missing %s/%s: %v", key, horizon, direction, combos)
				}
			}
		}
	}
}

func TestBuildEventsMissingRubikDoesNotBlock(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected events without rubik")
	}
	for _, event := range events {
		if _, ok := event.Features["rubik_taker_imbalance_1h"]; ok {
			t.Fatal("rubik feature must remain missing when rubik file absent")
		}
		found := false
		for _, m := range event.MissingFeatures {
			if m == "rubik_taker_imbalance_1h" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("event %s must list missing rubik feature", event.ID)
		}
	}
}

func TestEmptyOptionalCatalogFileIsVerifiedButRemainsMissing(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	hashWithoutEmpty, err := SelectedFileHash(catalog, 10)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(familyRubikTaker1h, "BTC.jsonl")
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stats := fixtureFileStats(t, path, rel)
	stats.Empty = true
	catalog.Families = append(catalog.Families, historydata.FamilyCatalog{
		Name: familyRubikTaker1h, Files: []historydata.FileStats{stats},
	})

	hashWithEmpty, err := SelectedFileHash(catalog, 10)
	if err != nil {
		t.Fatal(err)
	}
	if hashWithEmpty != hashWithoutEmpty {
		t.Fatal("catalog-empty optional file must not enter SelectedFileHash")
	}
	if err := VerifySelectedFiles(&catalog, root, 10); err != nil {
		t.Fatalf("catalog-empty physical file should verify as legal missing: %v", err)
	}
	events, err := BuildEvents(context.Background(), fixtureConfig(t, root, catalog))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("empty optional file must not block price events")
	}
	for _, event := range events {
		if _, ok := event.Features["rubik_taker_imbalance_1h"]; ok {
			t.Fatal("catalog-empty optional feature must remain missing, not zero-filled")
		}
	}

	if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySelectedFiles(&catalog, root, 10); err == nil || !strings.Contains(err.Error(), rel) {
		t.Fatalf("changed empty optional file must fail with path context, got %v", err)
	}
}

func TestOptionalCatalogPresenceMatrixFailsClosed(t *testing.T) {
	t.Run("physical without catalog", func(t *testing.T) {
		root, catalog := writeFixtureHistory(t, false)
		rel := filepath.Join(familyRubikTaker1h, "BTC.jsonl")
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"data":["1704067200000","1","1"]}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := BuildEvents(context.Background(), fixtureConfig(t, root, catalog))
		if err == nil || !strings.Contains(err.Error(), rel) {
			t.Fatalf("uncataloged optional file must fail with path context, got %v", err)
		}
	})

	t.Run("nonempty catalog without physical", func(t *testing.T) {
		root, catalog := writeFixtureHistory(t, true)
		rel := filepath.Join(familyRubikTaker1h, "BTC.jsonl")
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			t.Fatal(err)
		}
		_, err := BuildEvents(context.Background(), fixtureConfig(t, root, catalog))
		if err == nil || !strings.Contains(err.Error(), rel) {
			t.Fatalf("missing catalog-declared optional file must fail with path context, got %v", err)
		}
	})
}

func TestBuildEventsWithRubikPopulatesImbalance(t *testing.T) {
	root, catalog := writeFixtureHistory(t, true)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var withRubik int
	for _, event := range events {
		if v, ok := event.Features["rubik_taker_imbalance_1h"]; ok {
			withRubik++
			if v == 0 {
				t.Fatal("rubik imbalance must not be silently zero-filled")
			}
		}
	}
	if withRubik == 0 {
		t.Fatal("expected rubik imbalance on fixture with rubik data")
	}
}

func TestFeatureAsOfEqualsDecisionTime(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if !event.FeatureAsOf.Equal(event.DecisionTime) {
			t.Fatalf("feature_as_of must equal decision_time for %s", event.ID)
		}
	}
}

func TestNoCurrentOpenBarLeakage(t *testing.T) {
	root, _ := writeFixtureHistory(t, false)
	pricePath := filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl")
	prices, err := loadPrices(pricePath, 5000)
	if err != nil {
		t.Fatal(err)
	}
	data := symbolSeries{prices: prices}
	for i := 24; i < len(prices)-24; i++ {
		decision := prices[i].OpenTime.Add(time.Hour)
		features, _ := computeFeatureCatalog(data, i, decision)
		if i+1 < len(prices) {
			futureClose := prices[i+1].Close
			for _, v := range features {
				if v == futureClose {
					t.Fatalf("feature value equals future bar close at index %d", i)
				}
			}
		}
		if prices[i+1].OpenTime.Add(time.Hour).After(decision) {
			continue
		}
	}
}

func TestExecutionUsesNextAndFutureBarOpens(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	prices, err := loadPrices(filepath.Join(root, familyOKX1h, "BTC-USDT-SWAP.jsonl"), 5000)
	if err != nil {
		t.Fatal(err)
	}
	byDecision := map[int64]int{}
	for i, bar := range prices {
		byDecision[bar.OpenTime.Add(time.Hour).UnixMilli()] = i
	}
	for _, event := range events {
		i := byDecision[event.DecisionTime.UnixMilli()]
		entryIndex := i + 1
		exitIndex := entryIndex + event.Horizon.Hours()
		if event.EntryPrice != prices[entryIndex].Open || !event.EntryTime.Equal(prices[entryIndex].OpenTime) {
			t.Fatalf("%s entry = %v at %v, want next open %v at %v", event.ID, event.EntryPrice, event.EntryTime, prices[entryIndex].Open, prices[entryIndex].OpenTime)
		}
		if event.ExitPrice != prices[exitIndex].Open || !event.ExitTime.Equal(prices[exitIndex].OpenTime) {
			t.Fatalf("%s exit = %v at %v, want horizon open %v at %v", event.ID, event.ExitPrice, event.ExitTime, prices[exitIndex].Open, prices[exitIndex].OpenTime)
		}
		if event.EntryPrice == prices[i].Close {
			t.Fatalf("%s illegally enters at decision bar close", event.ID)
		}
	}
}

func TestLoadPricesRejectsUnconfirmedBars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bars.jsonl")
	rows := []string{
		`{"data":["1704067200000","100","101","99","100.5","1","1","1000000","1"]}`,
		`{"data":["1704070800000","200","201","199","200.5","1","1","1000000","0"]}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bars, err := loadPrices(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Open != 100 || !bars[0].Confirmed {
		t.Fatalf("only confirmed bar should survive: %+v", bars)
	}
}

func TestRubikCurrentBucketAndStaleFundingAreMissing(t *testing.T) {
	decision := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if _, ok := latestRubikTakerBefore([]rubikTakerPoint{{Time: decision, Buy: 10, Sell: 1}}, decision); ok {
		t.Fatal("Rubik bucket opening at decision is not complete")
	}
	want := rubikTakerPoint{Time: decision.Add(-time.Hour), Buy: 10, Sell: 1}
	if got, ok := latestRubikTakerBefore([]rubikTakerPoint{want}, decision); !ok || got != want {
		t.Fatalf("closed Rubik bucket unavailable: got=%+v ok=%v", got, ok)
	}
	if _, ok := latestFundingBefore([]fundingPoint{{Time: decision.Add(-24*time.Hour - time.Millisecond), Rate: 0.1}}, decision); ok {
		t.Fatal("funding older than 24h must be missing")
	}
}

func TestRubikStalenessExactBoundaryAndTimeout(t *testing.T) {
	decision := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	maxAge := time.Duration(protocol.Rubik1hMaxStalenessHours) * time.Hour
	boundaryOpen := decision.Add(-time.Hour).Add(-maxAge)

	taker := rubikTakerPoint{Time: boundaryOpen, Buy: 9, Sell: 1}
	if got, ok := latestRubikTakerBefore([]rubikTakerPoint{taker}, decision); !ok || got != taker {
		t.Fatalf("taker at exact %v staleness must be available: got=%+v ok=%v", maxAge, got, ok)
	}
	if _, ok := latestRubikTakerBefore([]rubikTakerPoint{{
		Time: boundaryOpen.Add(-time.Millisecond), Buy: 9, Sell: 1,
	}}, decision); ok {
		t.Fatal("taker beyond exact staleness boundary must be missing")
	}

	ratio := rubikRatioPoint{Time: boundaryOpen, Ratio: 1.5}
	if got, ok := latestRubikRatioBefore([]rubikRatioPoint{ratio}, decision); !ok || got != ratio {
		t.Fatalf("ratio at exact %v staleness must be available: got=%+v ok=%v", maxAge, got, ok)
	}
	if _, ok := latestRubikRatioBefore([]rubikRatioPoint{{
		Time: boundaryOpen.Add(-time.Millisecond), Ratio: 1.5,
	}}, decision); ok {
		t.Fatal("ratio beyond exact staleness boundary must be missing")
	}
}

func TestStaleRubikFeaturesRemainMissingNotZero(t *testing.T) {
	decision := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	var prices []priceBar
	for i := 0; i < 25; i++ {
		prices = append(prices, priceBar{
			OpenTime: decision.Add(time.Duration(i-25) * time.Hour),
			Open:     100 + float64(i), Close: 100.5 + float64(i),
			QuoteVolume: 5_000_000, Confirmed: true,
		})
	}
	staleOpen := decision.Add(-(time.Duration(protocol.Rubik1hMaxStalenessHours+1)*time.Hour + time.Hour))
	features, missing := computeFeatureCatalog(symbolSeries{
		prices:     prices,
		rubikTaker: []rubikTakerPoint{{Time: staleOpen, Buy: 10, Sell: 1}},
		rubikRatio: []rubikRatioPoint{{Time: staleOpen, Ratio: 1.5}},
	}, len(prices)-1, decision)
	for _, name := range []string{"rubik_taker_imbalance_1h", "rubik_lsratio_1h"} {
		if _, ok := features[name]; ok {
			t.Fatalf("stale %s must be absent, not zero-filled", name)
		}
		found := false
		for _, missingName := range missing {
			if missingName == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("stale %s must be listed missing", name)
		}
	}
}

func TestComputeBoundariesFourPartitionsWithEmbargo(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(4000 * time.Hour)
	points := []fundingPoint{
		{Time: start},
		{Time: start.Add(2000 * time.Hour)},
		{Time: end.Add(-time.Millisecond)},
	}
	b, err := ComputeBoundaries(points)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Start.Equal(start) || !b.End.After(start) {
		t.Fatalf("unexpected anchor: %+v", b)
	}
	if !b.InductionEnd.After(b.Start) || !b.RecheckStart.After(b.InductionEnd) ||
		!b.RecheckEnd.After(b.RecheckStart) || !b.ForwardStart.After(b.RecheckEnd) ||
		!b.ForwardEnd.After(b.ForwardStart) || !b.WFStart.After(b.ForwardEnd) || !b.End.After(b.WFStart) {
		t.Fatalf("partition ordering violated: %+v", b)
	}
	embargo := time.Duration(protocol.PartitionEmbargoHours) * time.Hour
	if b.RecheckStart.Sub(b.InductionEnd) != embargo {
		t.Fatalf("recheck embargo = %v want %v", b.RecheckStart.Sub(b.InductionEnd), embargo)
	}
}

func TestNetOutcomeUsesDirectionMinusCost(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.OutcomeClass == OutcomeWinner && event.NetOutcomeBPS <= protocol.NeutralBandBPS {
			t.Fatalf("winner must exceed neutral band: %+v", event)
		}
		if event.Direction != mechanism.DirectionLong && event.Direction != mechanism.DirectionShort {
			t.Fatalf("unexpected direction %s", event.Direction)
		}
	}
}

func TestBuildEventsDetectsTamperedHistoryFile(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	if _, err := BuildEvents(context.Background(), cfg); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	fundingPath := filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl")
	raw, err := os.ReadFile(fundingPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append(raw, []byte(`{"data":{"fundingRate":"0.09","fundingTime":"99999999999999","instId":"BTC-USDT-SWAP"}}`+"\n")...)
	if err := os.WriteFile(fundingPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildEvents(context.Background(), cfg); err == nil {
		t.Fatal("tampered catalog-bound file must be rejected")
	}
}

func TestBuildEventsDetectsTamperedOptionalFiles(t *testing.T) {
	for _, withRubik := range []bool{false, true} {
		root, catalog := writeFixtureHistory(t, withRubik)
		cfg := fixtureConfig(t, root, catalog)
		path := filepath.Join(root, familyBybitFunding, "BTCUSDT.jsonl")
		if withRubik {
			path = filepath.Join(root, familyRubikTaker1h, "BTC.jsonl")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw[0] ^= 1
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildEvents(context.Background(), cfg); err == nil {
			t.Fatalf("tampered optional file %s must be rejected", path)
		}
	}
}

func TestBuildEventsRejectsUnboundedConfiguration(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cases := []Config{
		{HistoryRoot: "", Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000},
		{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 0, MaxFundingLines: 100, MaxPriceLines: 1000},
		{HistoryRoot: root, Catalog: nil, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000},
	}
	for i, cfg := range cases {
		if _, err := BuildEvents(context.Background(), cfg); err == nil {
			t.Fatalf("case %d must fail", i)
		}
	}
}

func TestCanonicalSymbolAdapters(t *testing.T) {
	if got := CanonicalSymbol("BTC-USDT-SWAP"); got != "BTCUSDT" {
		t.Fatalf("canonical=%q", got)
	}
	if got := RubikFileName("BTCUSDT"); got != "BTC" {
		t.Fatalf("rubik=%q", got)
	}
	if got := BybitFileName("BTCUSDT"); got != "BTCUSDT" {
		t.Fatalf("bybit=%q", got)
	}
}

func TestWinnerIndexOnlyWinners(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	_, boundaries, err := AnchorFundingSpan(root, 500)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Start = boundaries.Start
	cfg.End = boundaries.InductionEnd
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	index, err := BuildWinnerIndex(events, "g3-test", "hash")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index.Entries {
		if entry.OutcomeClass != OutcomeWinner {
			t.Fatalf("winner index must only contain winners, got %s", entry.OutcomeClass)
		}
	}
}
