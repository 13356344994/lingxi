package datatruth

import (
	"testing"
	"time"
)

func TestInstrumentAndUniversePointInTime(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)
	t2 := t1.Add(24 * time.Hour)

	instruments := InstrumentCatalog{Versions: []InstrumentVersion{
		{
			InstrumentID: "BTC-PERP", Venue: "okx", Symbol: "BTC-USDT-SWAP",
			ListedAt: t0, TickSize: "0.1", LotSize: "0.01", MinNotional: "5", KnownFrom: t0,
		},
		{
			InstrumentID: "BTC-PERP", Venue: "okx", Symbol: "BTC-USDT-SWAP",
			ListedAt: t0, TickSize: "0.01", LotSize: "0.01", MinNotional: "5", KnownFrom: t2,
		},
		{
			InstrumentID: "DOGE-PERP", Venue: "okx", Symbol: "DOGE-USDT-SWAP",
			ListedAt: t1, DelistedAt: t2, TickSize: "0.0001", LotSize: "1", MinNotional: "5", KnownFrom: t1,
		},
	}}
	if err := instruments.Validate(); err != nil {
		t.Fatal(err)
	}

	old, ok := instruments.InstrumentAt("BTC-PERP", t1, t1)
	if !ok || old.TickSize != "0.1" {
		t.Fatalf("expected old tick point-in-time, got %+v ok=%v", old, ok)
	}
	newer, ok := instruments.InstrumentAt("BTC-PERP", t1, t2)
	if !ok || newer.TickSize != "0.01" {
		t.Fatalf("expected known-later tick point-in-time, got %+v ok=%v", newer, ok)
	}
	if _, ok := instruments.InstrumentAt("DOGE-PERP", t2.Add(time.Second), t2.Add(time.Second)); ok {
		t.Fatal("delisted instrument should not be active")
	}

	universes := UniverseCatalog{Versions: []UniverseVersion{
		{UniverseID: "perp-major", Members: []string{"BTC-PERP", "BTC-PERP"}, Effective: t0, KnownFrom: t0},
		{UniverseID: "perp-major", Members: []string{"DOGE-PERP", "BTC-PERP"}, Effective: t1, KnownFrom: t1},
	}}
	u, ok := universes.UniverseAt("perp-major", t1, t1)
	if !ok || len(u.Members) != 2 || !u.Contains("DOGE-PERP") {
		t.Fatalf("expected revised universe, got %+v ok=%v", u, ok)
	}
	if err := ValidateUniverseMembers(u, instruments, t1, t1); err != nil {
		t.Fatalf("universe members should be point-in-time valid: %v", err)
	}
}

func TestManifestHashesAreNormalized(t *testing.T) {
	m1 := DataManifest{SchemaVersion: "1", DatasetID: "d", EventHashes: []string{"b", "a"}, UniverseHash: "u"}
	m2 := DataManifest{SchemaVersion: "1", DatasetID: "d", EventHashes: []string{"a", "b"}, UniverseHash: "u"}
	h1, err := m1.NormalizedHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := m2.NormalizedHash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("data manifest hash should ignore event order: %s != %s", h1, h2)
	}

	fh, err := (FeatureManifest{
		SchemaVersion: "1",
		FeatureSetID:  "features",
		DataHash:      h1,
		CodeHash:      "code",
		FeatureNames:  []string{"z", "a"},
	}).NormalizedHash()
	if err != nil {
		t.Fatal(err)
	}
	eh, err := (ExperimentManifest{
		SchemaVersion: "1",
		ExperimentID:  "experiment",
		FeatureHash:   fh,
		ProtocolHash:  "protocol",
		Parameters:    map[string]any{"walk_forward": true},
	}).NormalizedHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (DeploymentManifest{
		SchemaVersion:  "1",
		DeploymentID:   "deploy",
		ExperimentHash: eh,
		DecisionSchema: DecisionSchema{
			DecisionID:       "decision-1",
			DataHash:         h1,
			FeatureHash:      fh,
			ExperimentHash:   eh,
			InputEventHashes: []string{"event"},
		},
	}).NormalizedHash(); err != nil {
		t.Fatalf("deployment hash rejected: %v", err)
	}
}
