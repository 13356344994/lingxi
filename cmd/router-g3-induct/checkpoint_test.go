package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g3reverse"
	"lingxi/internal/routerg3/protocol"
)

func TestCheckpointRejectsBindingDrift(t *testing.T) {
	root := t.TempDir()
	binding := checkpointBinding{
		EventManifestHash: "m1", InductionEventHash: "e1", WinnerIndexHash: "w1",
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "c1",
		OutputDir: root,
	}
	cp := newCheckpoint(binding)
	path := filepath.Join(root, "checkpoint.json")
	if err := saveCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := loadCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	drift := binding
	drift.EventManifestHash = "tampered"
	if err := loaded.validateBinding(drift); err == nil {
		t.Fatal("expected checkpoint binding drift")
	}
	drift = binding
	drift.RecomputedEventHash = "tampered"
	if err := loaded.validateBinding(drift); err == nil {
		t.Fatal("expected recomputed event checkpoint drift")
	}
}

func TestCheckpointResumeRequiresMatchingOutputs(t *testing.T) {
	root := t.TempDir()
	rules := []g3reverse.Rule{{
		Ordinal: 1, SkyState: "sky", SorterDomain: "dom", Horizon: g3reverse.Horizon1h,
		Direction: mechanism.DirectionLong,
		Conditions: []g3reverse.RuleCondition{{
			FeatureKey: "symbol_return_1h", Op: g3reverse.OpGTE, Threshold: 0.1, Quantile: 0.5,
		}},
	}}
	hash, err := g3reverse.CanonicalRuleHash(rules[0])
	if err != nil {
		t.Fatal(err)
	}
	rules[0].CanonicalHash = hash
	rulesHash, err := g3reverse.HashRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	ledger := g3reverse.AlphaLedger{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
		Entries: []g3reverse.LedgerEntry{{
			Ordinal: 1, CanonicalRuleHash: hash,
			DerivationAlgorithmConfigHash: protocol.DerivationAlgorithmConfigHash,
			AlphaStage:                    "reverse_induction", AlphaCharge: 0.004, Status: g3reverse.LedgerStatusRegistered,
			SkyState: "sky", SorterDomain: "dom", Horizon: g3reverse.Horizon1h,
			Direction: mechanism.DirectionLong, RuleKind: "single",
			Conditions: rules[0].Conditions, SourceWinnerEventIDs: []string{"w1"},
			AvailableFeatureSampleCount: 1,
		}},
	}
	ledger, err = g3reverse.SealLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := g3reverse.NewSearchManifest(rules, ledger, g3reverse.SearchManifestBindings{
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "der",
		EventManifestHash: "em", InductionEventHash: "ih", WinnerIndexHash: "wh",
		CoverageHash: "coverage",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"search_manifest.json": manifest,
		"rules.json":           rules,
		"alpha_ledger.json":    ledger,
	} {
		if err := writeJSONAtomic(filepath.Join(root, name), value); err != nil {
			t.Fatal(err)
		}
	}
	binding := checkpointBinding{
		EventManifestHash: "em", InductionEventHash: "ih", WinnerIndexHash: "wh",
		SourceDataHash: protocol.HistoryDataHash, CatalogDerivationHash: "der",
		OutputDir: root,
	}
	cp := newCheckpoint(binding)
	cp.Done = true
	cp.SearchManifestHash = manifest.ManifestHash
	cp.RulesHash = rulesHash
	cp.LedgerHash = ledger.LedgerHash
	cp.SavedAt = time.Now().UTC()
	if err := verifyCompletedOutputs(root, cp); err != nil {
		t.Fatal(err)
	}
	// Tamper rules file → drift.
	tampered := append([]g3reverse.Rule{}, rules...)
	tampered[0].Conditions[0].Threshold = 99
	raw, _ := json.MarshalIndent(tampered, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "rules.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyCompletedOutputs(root, cp); err == nil {
		t.Fatal("expected rules drift detection")
	}
}

type canonicalFixture struct {
	root             string
	catalogPath      string
	bundleDir        string
	manifest         g3reverse.Manifest
	events           []g3reverse.Event
	winners          g3reverse.WinnerIndex
	pricePath        string
	originalPriceRaw []byte
}

func buildCanonicalFixture(t *testing.T) canonicalFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "history")
	priceDir := filepath.Join(root, "okx_1h")
	fundingDir := filepath.Join(root, "okx_funding")
	if err := os.MkdirAll(priceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fundingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var priceLines []string
	price := 100.0
	for i := 0; i < 1200; i++ {
		switch {
		case i >= 175 && i < 200:
			price *= 0.985
		case i >= 210 && i < 235:
			price *= 1.003
		default:
			price *= 1.001
		}
		row := map[string]any{"data": []string{
			fmt.Sprint(base.Add(time.Duration(i) * time.Hour).UnixMilli()),
			fmt.Sprint(price), fmt.Sprint(price + 1), fmt.Sprint(price - 1),
			fmt.Sprint(price + 0.2), "1", "1", "5000000", "1",
		}}
		raw, _ := json.Marshal(row)
		priceLines = append(priceLines, string(raw))
	}
	pricePath := filepath.Join(priceDir, "BTC-USDT-SWAP.jsonl")
	priceRaw := []byte(strings.Join(priceLines, "\n") + "\n")
	if err := os.WriteFile(pricePath, priceRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pricePath+".done", []byte("sealed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fundingLines []string
	for i := 0; i < 1200; i += 8 {
		row := map[string]any{"data": map[string]string{
			"fundingRate": "0.001",
			"fundingTime": fmt.Sprint(base.Add(time.Duration(i) * time.Hour).UnixMilli()),
			"instId":      "BTC-USDT-SWAP",
		}}
		raw, _ := json.Marshal(row)
		fundingLines = append(fundingLines, string(raw))
	}
	fundingPath := filepath.Join(fundingDir, "BTC-USDT-SWAP.jsonl")
	if err := os.WriteFile(
		fundingPath,
		[]byte(strings.Join(fundingLines, "\n")+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fundingPath+".done", []byte("sealed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inventory, err := historydata.Inventory(context.Background(), historydata.Config{
		RootDir: root, Families: []string{"okx_1h", "okx_funding"},
		MaxFiles: 10, Checkpoint: filepath.Join(root, "inventory.checkpoint.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Completed {
		t.Fatal("fixture inventory did not complete")
	}
	catalog := inventory.Catalog
	catalogPath := filepath.Join(t.TempDir(), "history_catalog.json")
	if err := writeJSONAtomic(catalogPath, catalog); err != nil {
		t.Fatal(err)
	}

	const (
		maxSymbols = 1
		maxFunding = 500
		maxPrice   = 2000
		maxFamily  = 2000
	)
	_, boundaries, err := g3reverse.AnchorFundingSpan(root, maxFunding)
	if err != nil {
		t.Fatal(err)
	}
	cfg := g3reverse.Config{
		HistoryRoot: root, Catalog: &catalog, MaxSymbols: maxSymbols,
		MaxFundingLines: maxFunding, MaxPriceLines: maxPrice, MaxFamilyLines: maxFamily,
	}
	cutpoints, err := g3reverse.FitSkyCutpoints(context.Background(), cfg, boundaries)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SkyCutpoints = cutpoints
	cfg.Start, cfg.End = boundaries.Window(g3reverse.PartitionInduction)
	events, err := g3reverse.BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	winners, err := g3reverse.BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	eventHash, err := g3reverse.HashEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	cutpointsHash, err := cutpoints.Hash()
	if err != nil {
		t.Fatal(err)
	}
	boundariesHash, err := g3reverse.HashBoundaries(boundaries)
	if err != nil {
		t.Fatal(err)
	}
	selectedHash, err := g3reverse.SelectedFileHash(catalog, maxSymbols)
	if err != nil {
		t.Fatal(err)
	}
	configHash, err := g3reverse.ConfigHash(
		maxSymbols, maxFunding, maxPrice, maxFamily, cutpointsHash, boundariesHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest := g3reverse.Manifest{
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		SourceDataHash: catalog.DataHash, SourceSchemaHash: catalog.SchemaHash,
		SourceDatasetID: catalog.DatasetID, DerivationHash: selectedHash,
		PartitionBoundariesHash: boundariesHash, SkyCutpointsHash: cutpointsHash,
		InductionEventHash: eventHash, WinnerIndexHash: winners.IndexHash,
		InductionEventCount: len(events), WinnerCount: len(winners.Entries),
		MaxSymbols: maxSymbols, MaxFundingLines: maxFunding,
		MaxPriceLines: maxPrice, MaxFamilyLines: maxFamily, ConfigHash: configHash,
	}
	bundleDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bundleDir, "g3_induction"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(bundleDir, "sky_cutpoints.json"), cutpoints); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(bundleDir, "partition_boundaries.json"), boundaries); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSONL(t, filepath.Join(bundleDir, "g3_induction", "events.jsonl"), events)
	writeFixtureJSONL(t, filepath.Join(bundleDir, "g3_induction", "winner_index.jsonl"), winners.Entries)
	return canonicalFixture{
		root: root, catalogPath: catalogPath, bundleDir: bundleDir, manifest: manifest,
		events: events, winners: winners, pricePath: pricePath, originalPriceRaw: priceRaw,
	}
}

func writeFixtureJSONL(t *testing.T, path string, rows any) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	switch values := rows.(type) {
	case []g3reverse.Event:
		for _, value := range values {
			if err := encoder.Encode(value); err != nil {
				file.Close()
				t.Fatal(err)
			}
		}
	case []g3reverse.WinnerIndexEntry:
		for _, value := range values {
			if err := encoder.Encode(value); err != nil {
				file.Close()
				t.Fatal(err)
			}
		}
	default:
		file.Close()
		t.Fatal("unsupported fixture JSONL type")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalProvenanceRejectsDriftAndForgery(t *testing.T) {
	fixture := buildCanonicalFixture(t)
	ctx := context.Background()
	if _, err := verifyCanonicalProvenance(
		ctx, fixture.root, fixture.catalogPath, fixture.bundleDir, fixture.manifest,
	); err != nil {
		t.Fatalf("valid canonical fixture rejected: %v", err)
	}

	t.Run("canonical history drift", func(t *testing.T) {
		tampered := append([]byte(nil), fixture.originalPriceRaw...)
		tampered[len(tampered)/2] ^= 1
		if err := os.WriteFile(fixture.pricePath, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(fixture.pricePath, fixture.originalPriceRaw, 0o644)
		if _, err := verifyCanonicalProvenance(
			ctx, fixture.root, fixture.catalogPath, fixture.bundleDir, fixture.manifest,
		); err == nil {
			t.Fatal("expected canonical history drift rejection")
		}
	})

	t.Run("tampered bundle", func(t *testing.T) {
		events := append([]g3reverse.Event(nil), fixture.events...)
		if len(events) == 0 {
			t.Fatal("fixture must produce events")
		}
		events[0].Features = cloneFeatures(events[0].Features)
		events[0].Features["symbol_return_1h"] += 1
		path := filepath.Join(fixture.bundleDir, "g3_induction", "events.jsonl")
		writeFixtureJSONL(t, path, events)
		defer writeFixtureJSONL(t, path, fixture.events)
		if _, err := verifyCanonicalProvenance(
			ctx, fixture.root, fixture.catalogPath, fixture.bundleDir, fixture.manifest,
		); err == nil {
			t.Fatal("expected tampered bundle rejection")
		}
	})

	t.Run("self-consistent forged manifest", func(t *testing.T) {
		events := append([]g3reverse.Event(nil), fixture.events...)
		if len(events) == 0 {
			t.Fatal("fixture must produce events")
		}
		events[0].Features = cloneFeatures(events[0].Features)
		events[0].Features["symbol_return_1h"] += 2
		forgedHash, err := g3reverse.HashEvents(events)
		if err != nil {
			t.Fatal(err)
		}
		forged := fixture.manifest
		forged.InductionEventHash = forgedHash
		path := filepath.Join(fixture.bundleDir, "g3_induction", "events.jsonl")
		writeFixtureJSONL(t, path, events)
		defer writeFixtureJSONL(t, path, fixture.events)
		if _, err := verifyCanonicalProvenance(
			ctx, fixture.root, fixture.catalogPath, fixture.bundleDir, forged,
		); err == nil {
			t.Fatal("expected self-consistent forged manifest rejection")
		}
	})
}

func cloneFeatures(features map[string]float64) map[string]float64 {
	cloned := make(map[string]float64, len(features))
	for key, value := range features {
		cloned[key] = value
	}
	return cloned
}
