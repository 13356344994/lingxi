package g2prize

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
)

func writeFixtureHistory(t *testing.T) (root string, catalog historydata.HistoryCatalog) {
	t.Helper()
	root = t.TempDir()
	priceDir := filepath.Join(root, "okx_1h")
	fundingDir := filepath.Join(root, "okx_funding")
	if err := os.MkdirAll(priceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fundingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	instrument := "BTC-USDT-SWAP"
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// A 168h warmup followed by a storm-down leg (large
	// negative hourly returns) so skyStateOf resolves to "storm_down" at
	// the funding points that land inside it, and a separate calm-up leg
	// so a second, distinct SkyState is exercised by the same fixture.
	var priceLines []string
	price := 100.0
	for i := 0; i < 260; i++ {
		switch {
		case i >= 175 && i < 200:
			price *= 0.985 // storm_down leg: large negative hourly returns
		case i >= 210 && i < 235:
			price *= 1.003 // calm_up leg: small positive hourly returns
		}
		row := map[string]any{"data": []string{
			fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
			fmt.Sprint(price), fmt.Sprint(price + 1), fmt.Sprint(price - 1),
			fmt.Sprint(price + 0.5), "1", "1", "5000000", "1",
		}}
		raw, _ := json.Marshal(row)
		priceLines = append(priceLines, string(raw))
	}
	pricePath := filepath.Join(priceDir, instrument+".jsonl")
	if err := os.WriteFile(pricePath, []byte(strings.Join(priceLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var fundingLines []string
	for _, index := range []int{200, 235} {
		row := map[string]any{"data": map[string]string{
			"fundingRate": "0.002",
			"fundingTime": fmt.Sprint(baseTime.Add(time.Duration(index) * time.Hour).UnixMilli()),
			"instId":      instrument,
		}}
		raw, _ := json.Marshal(row)
		fundingLines = append(fundingLines, string(raw))
	}
	fundingPath := filepath.Join(fundingDir, instrument+".jsonl")
	if err := os.WriteFile(fundingPath, []byte(strings.Join(fundingLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog = historydata.HistoryCatalog{
		DatasetID: "history.test", RootDir: root, DataHash: "test-data-hash", SchemaHash: "test-schema-hash",
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{fixtureFileStats(t, fundingPath, "okx_funding/"+instrument+".jsonl")}},
			{Name: "okx_1h", Files: []historydata.FileStats{fixtureFileStats(t, pricePath, "okx_1h/"+instrument+".jsonl")}},
		},
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

func TestBuildEventsIsDeterministic(t *testing.T) {
	root, catalog := writeFixtureHistory(t)
	cfg := Config{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000}
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
		t.Fatalf("deterministic regeneration must be byte-identical: %d events (%s) vs %d events (%s)", len(first), hashA, len(second), hashB)
	}
	if len(first) == 0 {
		t.Fatal("fixture must produce at least one event")
	}
}

func TestBuildEventsEmitsBothHorizonsAndDirectionsPerDecisionPoint(t *testing.T) {
	root, catalog := writeFixtureHistory(t)
	events, err := BuildEvents(context.Background(), Config{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000})
	if err != nil {
		t.Fatal(err)
	}
	byDecision := map[int64]map[string]bool{}
	for _, e := range events {
		key := e.FeatureAsOf.UnixMilli()
		if byDecision[key] == nil {
			byDecision[key] = map[string]bool{}
		}
		byDecision[key][string(e.Horizon)+"|"+string(e.Direction)] = true
	}
	for key, combos := range byDecision {
		for _, horizon := range []Horizon{Horizon1h, Horizon4h, Horizon24h} {
			for _, direction := range []string{"long", "short"} {
				if !combos[string(horizon)+"|"+direction] {
					t.Fatalf("decision point %d is missing horizon=%s direction=%s: got %v", key, horizon, direction, combos)
				}
			}
		}
	}
}

func TestBuildEventsAssignsDistinctSkyStatesAcrossRegimeLegs(t *testing.T) {
	root, catalog := writeFixtureHistory(t)
	events, err := BuildEvents(context.Background(), Config{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000})
	if err != nil {
		t.Fatal(err)
	}
	skyStates := map[string]bool{}
	for _, e := range events {
		skyStates[e.SkyState] = true
	}
	if len(skyStates) < 2 {
		t.Fatalf("a fixture with a storm-down leg and a calm-up leg must resolve to at least 2 distinct SkyStates, got %v", skyStates)
	}
}

func TestBuildEventsRejectsUnboundedConfiguration(t *testing.T) {
	root, catalog := writeFixtureHistory(t)
	cases := []Config{
		{HistoryRoot: "", Catalog: &catalog, MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000},
		{HistoryRoot: root, Catalog: &catalog, MaxSymbols: 0, MaxFundingLines: 100, MaxPriceLines: 1000},
		{HistoryRoot: root, Catalog: &catalog, MaxSymbols: 10, MaxFundingLines: 0, MaxPriceLines: 1000},
		{HistoryRoot: root, Catalog: &catalog, MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 0},
		{HistoryRoot: root, Catalog: nil, MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000},
	}
	for i, cfg := range cases {
		if _, err := BuildEvents(context.Background(), cfg); err == nil {
			t.Fatalf("case %d: expected an unbounded/invalid configuration to be rejected", i)
		}
	}
}

func TestBuildEventsDetectsTamperedHistoryFile(t *testing.T) {
	root, catalog := writeFixtureHistory(t)
	cfg := Config{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 10, MaxFundingLines: 100, MaxPriceLines: 1000}
	if _, err := BuildEvents(context.Background(), cfg); err != nil {
		t.Fatalf("baseline run must succeed: %v", err)
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
		t.Fatal("a file that no longer matches its catalog-pinned hash/size must be rejected, not silently re-read")
	}
}
