package g2prize

import (
	"context"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg2/sky"
)

func testKey() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func manifestFixtureEvents() []Event {
	dt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []Event{{
		ID: "e1", Symbol: "BTCUSDT", Cluster: "c1", SkyState: "storm_down", SorterDomain: "liquid",
		LiquidityBucket: "liq_5", Horizon: Horizon1h, DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
		Payer: mechanism.PayerFundingPayers, Direction: mechanism.DirectionLong,
		Features: map[string]float64{"signal": 0.9}, NetOutcomeBPS: 10,
	}}
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

func testCatalog() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{
		DatasetID: "history.test", DataHash: "data-hash", SchemaHash: "schema-hash",
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{{Path: "okx_funding/BTC-USDT-SWAP.jsonl", FileHash: "h1", Size: 10}}},
			{Name: "okx_1h", Files: []historydata.FileStats{{Path: "okx_1h/BTC-USDT-SWAP.jsonl", FileHash: "h2", Size: 20}}},
		},
	}
}

func TestNewManifestSealsAndValidates(t *testing.T) {
	manifest, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 500, 25000)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("freshly sealed manifest must validate: %v", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.GeneratorID != GeneratorID {
		t.Fatalf("manifest identity drifted: %+v", manifest)
	}
}

func TestManifestHashDetectsTampering(t *testing.T) {
	manifest, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 500, 25000)
	if err != nil {
		t.Fatal(err)
	}
	manifest.EventCount = 999999
	if err := manifest.Validate(); err == nil {
		t.Fatal("a manifest whose content was mutated after sealing must fail validation")
	}
}

func TestSignAndVerifyManifestSignature(t *testing.T) {
	manifest, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 500, 25000)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignManifest(manifest, testKey(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifestSignature(signed, testKey()); err != nil {
		t.Fatalf("a correctly signed manifest must verify: %v", err)
	}
	if err := VerifyManifestSignature(signed, []byte("wrongwrongwrongwrongwrongwrongwr")); err == nil {
		t.Fatal("verification with the wrong key must fail")
	}
	tampered := signed
	tampered.EventCount = signed.EventCount + 1
	tampered.ManifestHash = ""
	resealed, err := SealManifest(tampered)
	if err != nil {
		t.Fatal(err)
	}
	resealed.KeyID = signed.KeyID
	resealed.Signature = signed.Signature
	if err := VerifyManifestSignature(resealed, testKey()); err == nil {
		t.Fatal("a resealed manifest wearing the old signature must fail verification")
	}
}

func TestSignManifestRejectsShortKey(t *testing.T) {
	manifest, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 500, 25000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignManifest(manifest, []byte("short"), "test-key"); err == nil {
		t.Fatal("a signing key shorter than 32 bytes must be rejected")
	}
}

func TestNewManifestRejectsMissingGenerationOrProtocolHash(t *testing.T) {
	if _, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "", "test-g2-protocol", 20, 500, 25000); err == nil {
		t.Fatal("missing generation must be rejected")
	}
	if _, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "", 20, 500, 25000); err == nil {
		t.Fatal("missing protocol hash must be rejected")
	}
}

func TestNewManifestRejectsUnboundedLimits(t *testing.T) {
	if _, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 0, 500, 25000); err == nil {
		t.Fatal("non-positive max symbols must be rejected")
	}
	if _, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 0, 25000); err == nil {
		t.Fatal("non-positive max funding lines must be rejected")
	}
	if _, err := NewManifest(manifestFixtureEvents(), testCatalog(), testCutpoints(), "test-g2", "test-g2-protocol", 20, 500, 0); err == nil {
		t.Fatal("non-positive max price lines must be rejected")
	}
}

func TestSelectedFileHashChangesWhenCatalogContentChanges(t *testing.T) {
	catalog := testCatalog()
	hashA, err := SelectedFileHash(catalog, 20)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Families[1].Files[0].FileHash = "changed"
	hashB, err := SelectedFileHash(catalog, 20)
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashB {
		t.Fatal("selected file hash must change when a selected catalog file's content hash changes")
	}
}

func TestSelectedFileHashIsBoundedByMaxSymbols(t *testing.T) {
	catalog := historydata.HistoryCatalog{
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{
				{Path: "okx_funding/AAA-USDT-SWAP.jsonl", FileHash: "h1", Size: 10},
				{Path: "okx_funding/BBB-USDT-SWAP.jsonl", FileHash: "h2", Size: 10},
			}},
			{Name: "okx_1h", Files: []historydata.FileStats{
				{Path: "okx_1h/AAA-USDT-SWAP.jsonl", FileHash: "h3", Size: 20},
				{Path: "okx_1h/BBB-USDT-SWAP.jsonl", FileHash: "h4", Size: 20},
			}},
		},
	}
	hashOne, err := SelectedFileHash(catalog, 1)
	if err != nil {
		t.Fatal(err)
	}
	hashTwo, err := SelectedFileHash(catalog, 2)
	if err != nil {
		t.Fatal(err)
	}
	if hashOne == hashTwo {
		t.Fatal("expanding max symbols must select more files and change the derivation hash")
	}
}

// TestManifestBindsToRealCatalogDerivedFromBuildEvents proves the manifest
// produced for one BuildEvents run is refused once the underlying history
// file catalog it was derived from is replaced by even a slightly
// different (but self-consistent) catalog — the canonical catalog binding
// this package's manifest exists to enforce.
func TestManifestBindsToRealCatalogDerivedFromBuildEvents(t *testing.T) {
	root, catalog := writeFixtureHistoryForManifest(t)
	events, err := BuildEvents(context.Background(), Config{HistoryRoot: root, Catalog: &catalog, SkyCutpoints: testCutpoints(), MaxSymbols: 5, MaxFundingLines: 50, MaxPriceLines: 500})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewManifest(events, catalog, testCutpoints(), "test-g2", "test-g2-protocol", 5, 50, 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest derived from a real BuildEvents run must validate: %v", err)
	}
	derivationHash, err := SelectedFileHash(catalog, manifest.MaxSymbols)
	if err != nil {
		t.Fatal(err)
	}
	if derivationHash != manifest.DerivationHash {
		t.Fatal("selected-file derivation hash must match what the manifest recorded")
	}

	// A different catalog (different pinned file hash) must produce a
	// different derivation hash, so the CLI's provenance check would
	// correctly refuse it.
	catalog.Families[1].Files[0].FileHash = strings.Repeat("f", 64)
	tamperedHash, err := SelectedFileHash(catalog, manifest.MaxSymbols)
	if err != nil {
		t.Fatal(err)
	}
	if tamperedHash == manifest.DerivationHash {
		t.Fatal("a catalog whose pinned file hash changed must not still match the frozen manifest's derivation hash")
	}
}

func writeFixtureHistoryForManifest(t *testing.T) (string, historydata.HistoryCatalog) {
	t.Helper()
	return writeFixtureHistory(t)
}
