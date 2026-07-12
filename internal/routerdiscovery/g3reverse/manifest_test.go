package g3reverse

import (
	"context"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerg3/protocol"
)

func testKey() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func manifestFixtureEvents() []Event {
	dt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return []Event{{
		ID: "e1", Symbol: "BTCUSDT", Cluster: "BTCUSDT", SkyState: "storm_down", SorterDomain: "liquid",
		LiquidityBucket: "liq_5", Horizon: Horizon1h, DecisionTime: dt, FeatureAsOf: dt,
		Direction: mechanism.DirectionLong,
		Features:  map[string]float64{"symbol_return_1h": 10}, NetOutcomeBPS: 20, OutcomeClass: OutcomeWinner,
	}}
}

func testCatalog() historydata.HistoryCatalog {
	return historydata.HistoryCatalog{
		DatasetID: "history.test", DataHash: protocol.HistoryDataHash, SchemaHash: "schema-hash",
		Families: []historydata.FamilyCatalog{
			{Name: familyOKXFunding, Files: []historydata.FileStats{{Path: "okx_funding/BTC-USDT-SWAP.jsonl", FileHash: "h1", Size: 10}}},
			{Name: familyOKX1h, Files: []historydata.FileStats{{Path: "okx_1h/BTC-USDT-SWAP.jsonl", FileHash: "h2", Size: 20}}},
		},
	}
}

func testBoundaries() Boundaries {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return Boundaries{
		Start: start, InductionEnd: start.Add(400 * time.Hour), RecheckStart: start.Add(568 * time.Hour),
		RecheckEnd: start.Add(600 * time.Hour), ForwardStart: start.Add(768 * time.Hour),
		ForwardEnd: start.Add(800 * time.Hour), WFStart: start.Add(968 * time.Hour), End: start.Add(1000 * time.Hour),
		EmbargoHours: 168, InductionPerm: 400, RecheckPerm: 200, ForwardPerm: 200, WalkForwardPerm: 200,
	}
}

func TestNewManifestSealsAndValidates(t *testing.T) {
	events := manifestFixtureEvents()
	winner, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.GeneratorID != GeneratorID {
		t.Fatalf("identity drift: %+v", manifest)
	}
}

func TestManifestHashDetectsTampering(t *testing.T) {
	events := manifestFixtureEvents()
	winner, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000)
	if err != nil {
		t.Fatal(err)
	}
	manifest.InductionEventCount = 999
	if err := manifest.Validate(); err == nil {
		t.Fatal("tampered manifest must fail validation")
	}
}

func TestManifestRejectsNonG3Bindings(t *testing.T) {
	events := manifestFixtureEvents()
	winner, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), "other-generation", protocol.RouterProtocolHash, 20, 500, 25000, 25000); err == nil {
		t.Fatal("non-G3 generation must be rejected")
	}
	badCatalog := testCatalog()
	badCatalog.DataHash = "other-history"
	if _, err := NewManifest(events, nil, nil, nil, winner, badCatalog, testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000); err == nil {
		t.Fatal("non-G3 history data hash must be rejected")
	}
	manifest, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ProtocolHash = "other-protocol"
	manifest, err = SealManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("resealed non-G3 protocol manifest must be rejected")
	}
}

func TestSignAndVerifyManifestSignature(t *testing.T) {
	events := manifestFixtureEvents()
	winner, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignManifest(manifest, testKey(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifestSignature(signed, testKey()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := VerifyManifestSignature(signed, []byte("wrongwrongwrongwrongwrongwrongwr")); err == nil {
		t.Fatal("wrong key must fail")
	}
	tampered := signed
	tampered.WinnerCount = signed.WinnerCount + 1
	tampered.ManifestHash = ""
	resealed, err := SealManifest(tampered)
	if err != nil {
		t.Fatal(err)
	}
	resealed.KeyID = signed.KeyID
	resealed.Signature = signed.Signature
	if err := VerifyManifestSignature(resealed, testKey()); err == nil {
		t.Fatal("old signature on resealed manifest must fail")
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
		t.Fatal("selected file hash must change with catalog content")
	}
}

func TestManifestBindsToRealBuildEventsRun(t *testing.T) {
	root, catalog := writeFixtureHistory(t, false)
	cfg := fixtureConfig(t, root, catalog)
	events, err := BuildEvents(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	if err != nil {
		t.Fatal(err)
	}
	_, boundaries, err := AnchorFundingSpan(root, 500)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewManifest(events, nil, nil, nil, winner, catalog, testCutpoints(), boundaries, protocol.Generation, protocol.RouterProtocolHash, 5, 50, 500, 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	derivationHash, err := SelectedFileHash(catalog, manifest.MaxSymbols)
	if err != nil {
		t.Fatal(err)
	}
	if derivationHash != manifest.DerivationHash {
		t.Fatal("derivation hash mismatch")
	}
}

func TestSignManifestRejectsShortKey(t *testing.T) {
	events := manifestFixtureEvents()
	winner, _ := BuildWinnerIndex(events, protocol.Generation, protocol.RouterProtocolHash)
	manifest, err := NewManifest(events, nil, nil, nil, winner, testCatalog(), testCutpoints(), testBoundaries(), protocol.Generation, protocol.RouterProtocolHash, 20, 500, 25000, 25000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SignManifest(manifest, []byte("short"), "test-key"); err == nil {
		t.Fatal("short key must be rejected")
	}
}
