package residualmap

import (
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

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg10/fingerprint"
	g10playbook "lingxi/internal/routerg10/playbook"
	g10protocol "lingxi/internal/routerg10/protocol"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

func TestTierCostComponentsSumToConservative(t *testing.T) {
	base := CostBase{
		RollingQuoteVolumeUSDT: 1e6,
		BarHigh:                100.2,
		BarLow:                 99.8,
		BarMid:                 100,
		FundingBPS:             1,
		ProfileID:              eventmap.ProfileOKX1H,
		Horizon:                "1h",
	}
	adj := PersonalAdjustments{FeeDeltaBPS: 1, SlipDeltaBPS: 0.5}
	tiers := ComputeTierCosts(50, base, adj, 80)
	for label, tier := range tiers {
		want := tier.GrossBPS - tier.FeeBPS - tier.SlipBPS - tier.ImpactBPS - tier.FundingBPS
		if diff := tier.ConservativeBPS - want; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("tier %s conservative=%v want=%v", label, tier.ConservativeBPS, want)
		}
	}
}

func TestPersonalResidualAcceptedWithCapacityCliff(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event: TriggeredEventEntry{
			EventID: "evt-1", Symbol: "BTC-USDT-SWAP", EventFamily: eventmap.FamilyExtremeReversal,
			Horizon: "1h", Direction: "long", DelayBars: 1,
		},
	}
	base := CostBase{
		RollingQuoteVolumeUSDT: 5e5,
		BarHigh:                100.05, BarLow: 99.95, BarMid: 100,
		FundingBPS: 0.5, ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1,
	}
	adj := PersonalAdjustments{}
	result := EvaluateFromGross(item, 80, base, adj, 80)
	if !result.Accepted {
		t.Fatalf("expected personal residual accepted, got %+v", result.RejectReasons)
	}
	if result.MaxCapacityUSDT < 1000 {
		t.Fatalf("max capacity too low: %v", result.MaxCapacityUSDT)
	}
	if result.CapacityCliffUSDT <= 0 {
		t.Fatal("expected capacity cliff")
	}
	if len(result.TierCosts) != 6 {
		t.Fatalf("tier count = %d want 6", len(result.TierCosts))
	}
}

func TestRejectPublicScalable(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event: TriggeredEventEntry{
			EventID: "evt-2", EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon: "4h", DelayBars: 2,
		},
	}
	base := CostBase{RollingQuoteVolumeUSDT: 1e9, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX1H, Horizon: "4h", DelayBars: 2}
	result := EvaluateFromGross(item, 80, base, PersonalAdjustments{}, 100)
	if result.Accepted {
		t.Fatal("public scalable event must be rejected")
	}
	if !contains(result.RejectReasons, RejectPublicScalable) {
		t.Fatalf("want PUBLIC_SCALABLE, got %+v", result.RejectReasons)
	}
}

func TestRejectSubsecondSpeed(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX5M,
		Event: TriggeredEventEntry{
			EventID: "evt-3", EventFamily: eventmap.FamilySessionLiquidity,
			Horizon: "5m", DelayBars: 0,
		},
	}
	base := CostBase{RollingQuoteVolumeUSDT: 2e5, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX5M, Horizon: "5m"}
	result := EvaluateFromGross(item, 25, base, PersonalAdjustments{}, 50)
	if !contains(result.RejectReasons, RejectSubsecondSpeed) {
		t.Fatalf("want SUBSECOND_SPEED_REQUIRED, got %+v", result.RejectReasons)
	}
}

func TestRejectPublicSimpleFactor(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event: TriggeredEventEntry{
			EventID: "evt-4", EventFamily: eventmap.FamilyExtremeContinuation,
			Horizon: "1h", DelayBars: 1,
		},
	}
	base := CostBase{RollingQuoteVolumeUSDT: 1e9, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1}
	result := EvaluateFromGross(item, 60, base, PersonalAdjustments{}, 100)
	if !contains(result.RejectReasons, RejectPublicSimpleFactor) {
		t.Fatalf("want PUBLIC_SIMPLE_FACTOR, got %+v", result.RejectReasons)
	}
}

func TestRejectUnknownCriticalCosts(t *testing.T) {
	item := EventWorkItem{ProfileID: eventmap.ProfileOKX1H, Event: TriggeredEventEntry{EventID: "evt-5", EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h", DelayBars: 1}}
	base := CostBase{RollingQuoteVolumeUSDT: 3e5, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1}
	adj := PersonalAdjustments{CriticalUnknown: true}
	result := EvaluateFromGross(item, 30, base, adj, 80)
	if !contains(result.RejectReasons, RejectUnknownCriticalCosts) {
		t.Fatalf("want UNKNOWN_CRITICAL_COSTS, got %+v", result.RejectReasons)
	}
}

func TestRejectFingerprintNegative(t *testing.T) {
	item := EventWorkItem{ProfileID: eventmap.ProfileOKX1H, Event: TriggeredEventEntry{EventID: "evt-6", EventFamily: eventmap.FamilyBTCRelative, Horizon: "4h", DelayBars: 0}}
	base := CostBase{RollingQuoteVolumeUSDT: 4e5, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX1H, Horizon: "4h"}
	adj := PersonalAdjustments{CriticalNegative: true}
	result := EvaluateFromGross(item, 35, base, adj, 90)
	if !contains(result.RejectReasons, RejectFingerprintNegative) {
		t.Fatalf("want FINGERPRINT_NEGATIVE, got %+v", result.RejectReasons)
	}
}

func TestOpportunityAndMapHashTamperDetection(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H, ManifestHash: "map-hash",
		Event: TriggeredEventEntry{
			EventID: "evt-7", Symbol: "ETH-USDT-SWAP", EventFamily: eventmap.FamilyExtremeReversal,
			Horizon: "1h", Direction: "short", DelayBars: 1, DecisionTime: time.Date(2024, 1, 2, 3, 0, 0, 0, time.UTC),
		},
	}
	base := CostBase{RollingQuoteVolumeUSDT: 4e5, BarHigh: 100.2, BarLow: 99.8, BarMid: 100, ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1}
	result := EvaluateFromGross(item, 35, base, PersonalAdjustments{}, 90)
	opp, err := BuildOpportunity(item, result, "fp-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOpportunity(opp); err != nil {
		t.Fatal(err)
	}
	bad := opp
	bad.MaxCapacityUSDT = 999
	if err := ValidateOpportunity(bad); err == nil {
		t.Fatal("tampered opportunity must fail")
	}
	cfg := RunConfig{SchemaVersion: SchemaVersionConfig, CandidateLimit: 8}
	m, err := BuildResidualMap(cfg, "fp-hash", []ResidualOpportunity{opp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadResidualMap(writeTempMap(t, m)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointTamperDetection(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: dir, OutputDir: dir,
		FingerprintPath: filepath.Join(dir, "fp.json"), MapDirs: []string{dir},
		MaxFiles: 10, MaxLines: 1000, CandidateLimit: 8, ProgressEvery: 100,
		Generation: g10protocol.Generation, ProtocolHash: g10protocol.RouterProtocolHash,
		HistoryDataHash: g10protocol.HistoryDataHash,
	}
	cp := NewCheckpoint(cfg, "catalog", "fp", map[string]string{"okx_1h": "m"})
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"next_work_index": 0`, `"next_work_index": 1`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
}

func TestManifestBinding(t *testing.T) {
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o",
		FingerprintPath: "/fp.json", MapDirs: []string{"/m"}, MaxFiles: 1, MaxLines: 100,
		CandidateLimit: 8, ProgressEvery: 100, Generation: g10protocol.Generation,
		ProtocolHash: g10protocol.RouterProtocolHash, HistoryDataHash: g10protocol.HistoryDataHash,
	}
	residualMap, _ := BuildResidualMap(cfg, "fp", nil)
	ledger, _ := BuildLedger("fp", map[string]string{"okx_1h": "m"}, nil)
	manifest, err := BuildManifest(cfg, g10protocol.HistoryDataHash, "fp", "/fp.json", map[string]string{"okx_1h": "m"}, nil, residualMap, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ProtocolHash != g10protocol.RouterProtocolHash {
		t.Fatal("manifest must bind G10 protocol hash")
	}
}

func TestSmokeRunnerWithSyntheticFixtures(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   filepath.Join(root, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if result.Summary.AttemptCount == 0 {
		t.Fatal("expected attempts")
	}
	if result.Manifest.FingerprintHash == "" || result.Manifest.MapHash == "" {
		t.Fatal("manifest binding missing")
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(result.Ledger); err != nil {
		t.Fatal(err)
	}
}

func writeTempMap(t *testing.T, m PersonalResidualMap) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), OutputFileName)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestProfileBundlePlaybookCompatible(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   filepath.Join(root, "cp2.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	profileManifestPath := filepath.Join(outDir, eventmap.ProfileOKX1H, ManifestFileName)
	manifest, err := LoadProfileBundleManifest(profileManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CandidateStatus != CandidateStatus {
		t.Fatalf("candidate status = %q", manifest.CandidateStatus)
	}
	if len(manifest.ResidualTierLabels) != 6 {
		t.Fatalf("tier labels = %d", len(manifest.ResidualTierLabels))
	}
	discoveryName := fmt.Sprintf("residual_map_discovery_%s.json", eventmap.ProfileOKX1H)
	if manifest.ArtifactHashes[discoveryName] == "" {
		t.Fatal("discovery artifact hash missing")
	}
	profileDir := filepath.Join(outDir, eventmap.ProfileOKX1H)
	if _, err := os.Stat(filepath.Join(profileDir, ThresholdsFileName)); err != nil {
		t.Fatalf("thresholds artifact: %v", err)
	}
	bundle, err := g10playbook.LoadResidualMapBundle(profileDir)
	if err != nil {
		t.Fatalf("playbook load profile bundle: %v", err)
	}
	if bundle.Manifest.SchemaVersion != g10playbook.ResidualMapManifestSchema {
		t.Fatalf("manifest schema = %q", bundle.Manifest.SchemaVersion)
	}
	if bundle.Discovery.SchemaVersion != g10playbook.ResidualMapDiscoverySchema {
		t.Fatalf("discovery schema = %q", bundle.Discovery.SchemaVersion)
	}
	_ = result
}

func writeSyntheticFixtures(t *testing.T) (root string, catalog historydata.HistoryCatalog, mapDir, fpPath string) {
	t.Helper()
	root = t.TempDir()
	for _, sub := range []string{"okx_1h", "okx_funding"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	writeBars(t, root, "okx_1h", "BTC-USDT-SWAP", base, time.Hour, 200, 1.002)
	decision := base.Add(48 * time.Hour)
	eventID := eventmap.BuildEventID("BTC-USDT-SWAP", decision, eventmap.FamilyExtremeReversal)

	mapDir = filepath.Join(root, "map")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	discovery := eventmap.PartitionMap{
		ProfileID: eventmap.ProfileOKX1H, Partition: eventmap.PartitionDiscovery,
		Cells: map[string]eventmap.PartitionMapCell{
			"k": {
				Key: eventmap.CellKey{
					EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h",
					Direction: "long", DelayBars: 0, CapitalTier: 30,
				},
				Winners: []eventmap.WinnerEntry{{
					EventID: eventID, Symbol: "BTC-USDT-SWAP", DecisionTime: decision,
					EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h", Direction: "long",
					DelayBars: 0, CapitalTier: 30, ConservativeBPS: 12, ShockCluster: "shock-a",
				}},
			},
		},
	}
	discoveryName := fmt.Sprintf("event_map_%s_%s.json", eventmap.PartitionDiscovery, eventmap.ProfileOKX1H)
	discoveryPath := filepath.Join(mapDir, discoveryName)
	writeJSONFile(t, discoveryPath, discovery)
	discoveryHash, _ := eventmap.HashFile(discoveryPath)
	thresholds := eventmap.FrozenThresholds{
		ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20,
	}
	thresholdsPath := filepath.Join(mapDir, "event_map_thresholds.json")
	writeJSONFile(t, thresholdsPath, thresholds)
	thresholdsHash, _ := eventmap.HashFile(thresholdsPath)
	manifest := eventmap.MapManifest{
		SchemaVersion:   "lingxi.router-g6-event-map.manifest.v1",
		Generation:      protocol.Generation,
		ProtocolHash:    protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash,
		ProfileID:       eventmap.ProfileOKX1H,
		CatalogHash:     g10protocol.HistoryDataHash,
		FullMarket:      true,
		ArtifactHashes: map[string]string{
			discoveryName:               discoveryHash,
			"event_map_thresholds.json": thresholdsHash,
		},
	}

	fpPath = filepath.Join(root, "fp.json")
	fp := buildTestFingerprint(t)
	writeJSONFile(t, fpPath, fp)

	btcPath := filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl")
	catalog = historydata.HistoryCatalog{
		DataHash: g10protocol.HistoryDataHash,
		Families: []historydata.FamilyCatalog{{
			Name:  "okx_1h",
			Files: []historydata.FileStats{fixtureFileStats(t, btcPath, "okx_1h", "okx_1h/BTC-USDT-SWAP.jsonl")},
		}},
	}
	selection, err := eventmap.SelectPrimaryFiles(catalog, eventmap.ProfileOKX1H, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	filesHash, _, err := datatruth.HashJSON(selection.Files)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SelectedFilesHash = filesHash
	manifest.CatalogSymbolCount = len(selection.Files)
	manifest.SelectedPrimaryFiles = len(selection.Files)
	manifest.AvailablePrimaryFiles = selection.Available
	manifest.ManifestHash = hashMapManifest(t, manifest)
	writeJSONFile(t, filepath.Join(mapDir, "event_map_manifest.json"), manifest)
	return root, catalog, mapDir, fpPath
}

func TestActualEquityUnknownDoesNotDefault30(t *testing.T) {
	capital := TierCapitalUSDT("actual", 0)
	if capital != 0 {
		t.Fatalf("actual tier with unknown equity must not default, got %v", capital)
	}
	tiers := ComputeTierCosts(40, CostBase{
		RollingQuoteVolumeUSDT: 5e5,
		BarHigh:                100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h",
	}, PersonalAdjustments{}, 0)
	if tiers["actual"].CapitalUSDT != 0 {
		t.Fatalf("actual tier capital = %v want 0", tiers["actual"].CapitalUSDT)
	}
	if tiers["30"].CapitalUSDT != 30 {
		t.Fatalf("30 tier capital = %v want 30", tiers["30"].CapitalUSDT)
	}
}

func TestLargeTiersRequireEvaluableCapacityFail(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event:     TriggeredEventEntry{EventID: "evt-cap", EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h", DelayBars: 1},
	}
	base := CostBase{
		RollingQuoteVolumeUSDT: 5e5,
		BarHigh:                100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1,
	}
	result := EvaluateFromGross(item, 80, base, PersonalAdjustments{}, 80)
	if !result.Accepted {
		t.Fatalf("expected accepted residual with capacity cliff, got %+v", result.RejectReasons)
	}
	for _, label := range []string{"10000", "100000"} {
		tier, ok := result.TierCosts[label]
		if !ok || tier.CapitalUSDT <= 0 {
			t.Fatalf("tier %s must be evaluable", label)
		}
		if !tier.CapacityFail {
			t.Fatalf("tier %s must capacity-fail", label)
		}
	}
}

func TestUnknownCriticalDoesNotHelpLargeTierFail(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event:     TriggeredEventEntry{EventID: "evt-unk", EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h", DelayBars: 1},
	}
	base := CostBase{
		RollingQuoteVolumeUSDT: 1e9,
		BarHigh:                100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1,
	}
	adj := PersonalAdjustments{CriticalUnknown: true}
	result := EvaluateFromGross(item, 60, base, adj, 80)
	if !contains(result.RejectReasons, RejectUnknownCriticalCosts) {
		t.Fatalf("want UNKNOWN_CRITICAL_COSTS, got %+v", result.RejectReasons)
	}
	for _, label := range LargeTierLabels {
		tier := result.TierCosts[label]
		if tier.ExecutionComplexityFail {
			t.Fatalf("UNKNOWN must not force execution fail on tier %s", label)
		}
	}
}

func TestPublicSimpleFactorIncludes10k(t *testing.T) {
	item := EventWorkItem{
		ProfileID: eventmap.ProfileOKX1H,
		Event:     TriggeredEventEntry{EventID: "evt-pub", EventFamily: eventmap.FamilyExtremeContinuation, Horizon: "1h", DelayBars: 1},
	}
	base := CostBase{
		RollingQuoteVolumeUSDT: 1e9,
		BarHigh:                100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1,
	}
	result := EvaluateFromGross(item, 60, base, PersonalAdjustments{}, 100)
	if !contains(result.RejectReasons, RejectPublicSimpleFactor) && !contains(result.RejectReasons, RejectPublicScalable) {
		t.Fatalf("expected public factor/scalable rejection, got %+v", result.RejectReasons)
	}
}

func TestCollectTriggeredEventWorkItemsNotWinnerSeeded(t *testing.T) {
	root, catalog, mapDir, _ := writeSyntheticFixtures(t)
	bundles, _, _, err := LoadMapBundles([]string{mapDir})
	if err != nil {
		t.Fatal(err)
	}
	items, err := CollectTriggeredEventWorkItems(bundles, catalog, root, 10, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected triggered events from streamed history")
	}
	for _, item := range items {
		if item.Event.ConservativeBPS > 0 {
			t.Fatal("triggered work items must not carry winner conservative labels")
		}
	}
}

func TestCriticalKnownSymbolSetExcludesUnknownFingerprintSymbols(t *testing.T) {
	fp := buildTestFingerprint(t)
	fp.Cells = append(fp.Cells, fingerprint.ExecutionCell{
		CellKey:     fingerprint.CellKey{Symbol: "ETH-USDT", Direction: "long"},
		SampleCount: 100,
	})
	eligible := CriticalKnownSymbolSet(fp)
	if !eligible["BTC-USDT"] {
		t.Fatalf("known BTC symbol missing: %+v", eligible)
	}
	if eligible["ETH-USDT"] {
		t.Fatalf("unknown ETH symbol must not enter residual scan: %+v", eligible)
	}
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

func buildTestFingerprint(t *testing.T) fingerprint.PersonalExecutionFingerprint {
	t.Helper()
	fp := fingerprint.PersonalExecutionFingerprint{
		SchemaVersion: fingerprint.SchemaVersion,
		Generation:    fingerprint.Generation,
		BuiltAt:       time.Now().UTC(),
		ConfigHash:    "cfg",
		SourceStatus: map[string]fingerprint.SourceStatus{
			"maker_attempts":    {Status: fingerprint.StatusAvailable, Decoded: 3},
			"executable_shadow": {Status: fingerprint.StatusAvailable, Decoded: 3},
			"instrument_meta":   {Status: fingerprint.StatusAvailable, Decoded: 1},
		},
		Equity: fingerprint.EquitySummary{
			MedianCashUSDT: 80, Status: fingerprint.StatusAvailable,
		},
		Cells: []fingerprint.ExecutionCell{{
			CellKey:       fingerprint.CellKey{Symbol: "BTC-USDT", Direction: "long"},
			FeeBPS:        fingerprint.QuantileMetric{P50: 1, P90: 2, N: 3, Status: fingerprint.StatusAvailable},
			EntrySlipBPS:  fingerprint.QuantileMetric{P50: 0.5, P90: 1, N: 3, Status: fingerprint.StatusAvailable},
			ExitSlipBPS:   fingerprint.QuantileMetric{P50: 0.5, P90: 1, N: 3, Status: fingerprint.StatusAvailable},
			LatencyMS:     fingerprint.QuantileMetric{P50: 100, P90: 200, N: 3, Status: fingerprint.StatusAvailable},
			MakerWaitMS:   fingerprint.QuantileMetric{P50: 500, P90: 800, N: 3, Status: fingerprint.StatusAvailable},
			MakerFillRate: fingerprint.RateMetric{Rate: 0.6, N: 3, Status: fingerprint.StatusAvailable},
			Instrument:    fingerprint.KnownLinearInstrument("1", "1", "0.1", 100),
			SampleCount:   3,
		}},
	}
	payload := fp
	payload.FingerprintHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	fp.FingerprintHash = hash
	return fp
}

func writeBars(t *testing.T, root, family, instrument string, base time.Time, step time.Duration, n int, drift float64) {
	t.Helper()
	var lines []string
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(step * time.Duration(i))
		lines = append(lines, profitmap.FormatCandleLine(open, price, price+2, price-2, price*drift, 2e5))
		price *= drift
	}
	path := filepath.Join(root, family, instrument+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, path string, payload any) {
	t.Helper()
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashMapManifest(t *testing.T, m eventmap.MapManifest) string {
	t.Helper()
	payload := m
	payload.ManifestHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestProgressEveryIs1000(t *testing.T) {
	if ProgressEvery != 1000 {
		t.Fatalf("ProgressEvery = %d want 1000", ProgressEvery)
	}
}

func TestProgressThrottledToProgressEvery(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1000,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	progressCalls := 0
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   filepath.Join(root, "progress-cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
		OnProgress: func(attempts, accepted int) {
			progressCalls++
			if attempts%cfg.ProgressEvery != 0 {
				t.Fatalf("progress at attempts=%d, want multiple of %d", attempts, cfg.ProgressEvery)
			}
		},
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.AttemptCount == 0 {
		t.Fatal("expected attempts")
	}
	if result.Summary.AttemptCount >= cfg.ProgressEvery && progressCalls == 0 {
		t.Fatal("expected at least one throttled progress callback")
	}
	if result.Summary.AttemptCount < cfg.ProgressEvery && progressCalls != 0 {
		t.Fatalf("small run should not emit progress, got %d calls", progressCalls)
	}
}

func TestResumeSkipsProcessedAttemptsWithoutDuplicate(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	cpPath := filepath.Join(root, "resume-cp.json")
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   cpPath,
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	first, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstAttempts := first.Summary.AttemptCount
	if firstAttempts == 0 {
		t.Fatal("expected attempts from first run")
	}

	cp, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("checkpoint: ok=%v err=%v", ok, err)
	}
	cp.NextWorkIndex = 0
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}

	second, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.AttemptCount != firstAttempts {
		t.Fatalf("resume duplicated attempts: first=%d second=%d", firstAttempts, second.Summary.AttemptCount)
	}
	seen := map[string]int{}
	for _, attempt := range second.Ledger.Attempts {
		seen[attempt.EventID]++
	}
	for eventID, count := range seen {
		if count > 1 {
			t.Fatalf("duplicate ledger event %s count=%d", eventID, count)
		}
	}
}

func TestFailClosedZeroCandidatesWithoutScanning(t *testing.T) {
	root, catalog, mapDir, _ := writeSyntheticFixtures(t)
	fpPath := filepath.Join(root, "unknown-fp.json")
	writeJSONFile(t, fpPath, buildUnknownFingerprint(t))
	outDir := filepath.Join(root, "fail-closed-out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1000,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   filepath.Join(root, "fail-closed-cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.AcceptedCount != 0 {
		t.Fatalf("accepted = %d want 0", result.Summary.AcceptedCount)
	}
	if result.Summary.AttemptCount != 0 {
		t.Fatalf("attempt_count = %d want 0 fail-closed", result.Summary.AttemptCount)
	}
	if result.Summary.TriggeredSeedCount != 0 {
		t.Fatalf("triggered_seed_count = %d want 0", result.Summary.TriggeredSeedCount)
	}
	if result.Summary.FailClosedReason != FailClosedFingerprintCoverage {
		t.Fatalf("fail_closed_reason = %q want %q", result.Summary.FailClosedReason, FailClosedFingerprintCoverage)
	}
	if len(result.Map.Opportunities) != 0 {
		t.Fatal("map must be empty")
	}
	if err := ValidateManifest(result.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(result.Ledger); err != nil {
		t.Fatal(err)
	}
	for _, profileID := range []string{eventmap.ProfileOKX1H} {
		profileDir := filepath.Join(outDir, profileID)
		bundle, err := g10playbook.LoadResidualMapBundle(profileDir)
		if err != nil {
			t.Fatalf("playbook load %s: %v", profileID, err)
		}
		if bundle.Manifest.ResidualFailClosedReason != FailClosedFingerprintCoverage {
			t.Fatalf("profile %s residual_fail_closed_reason = %q want %q",
				profileID, bundle.Manifest.ResidualFailClosedReason, FailClosedFingerprintCoverage)
		}
		if bundle.Discovery.SchemaVersion != g10playbook.ResidualMapDiscoverySchema {
			t.Fatalf("profile %s discovery schema = %q", profileID, bundle.Discovery.SchemaVersion)
		}
		if len(bundle.Discovery.Cells) != 0 {
			t.Fatalf("profile %s discovery cells = %d want 0", profileID, len(bundle.Discovery.Cells))
		}
	}
}

func TestFailClosedNotUsedWhenCoverageExists(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "coverage-out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1000,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   filepath.Join(root, "coverage-cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.FailClosedReason != "" {
		t.Fatalf("unexpected fail_closed_reason %q", result.Summary.FailClosedReason)
	}
	if result.Summary.AttemptCount == 0 {
		t.Fatal("expected scan attempts when fingerprint coverage exists")
	}
}

func TestSIGTERMSavesCheckpointForResume(t *testing.T) {
	root, catalog, mapDir, fpPath := writeSyntheticFixtures(t)
	outDir := filepath.Join(root, "sigterm-out")
	cfg := RunConfig{
		SchemaVersion:       SchemaVersionConfig,
		HistoryRoot:         root,
		OutputDir:           outDir,
		FingerprintPath:     fpPath,
		MapDirs:             []string{mapDir},
		MaxFiles:            10,
		MaxLines:            2000,
		CandidateLimit:      16,
		ProgressEvery:       1,
		AllowTruncatedSmoke: true,
		Generation:          g10protocol.Generation,
		ProtocolHash:        g10protocol.RouterProtocolHash,
		HistoryDataHash:     g10protocol.HistoryDataHash,
	}
	cpPath := filepath.Join(root, "sigterm-cp.json")
	ctx, cancel := context.WithCancel(context.Background())
	runner := Runner{
		Config:           cfg,
		Catalog:          catalog,
		CheckpointPath:   cpPath,
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
		OnProgress: func(attempts, accepted int) {
			cancel()
		},
	}
	_, err := runner.Run(ctx)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	cp, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("checkpoint after cancel: ok=%v err=%v", ok, err)
	}
	if cp.Done {
		t.Fatal("interrupted checkpoint must not be done")
	}
	if len(cp.Attempts) == 0 {
		t.Fatal("checkpoint must retain processed attempts")
	}

	full, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if full.Summary.AttemptCount < len(cp.Attempts) {
		t.Fatalf("resume lost attempts: checkpoint=%d final=%d", len(cp.Attempts), full.Summary.AttemptCount)
	}
}

func TestSymbolHasCriticalKnownCoverage(t *testing.T) {
	known := buildTestFingerprint(t)
	if !SymbolHasCriticalKnownCoverage(known, "BTC-USDT-SWAP") {
		t.Fatal("test fingerprint must cover BTC swap")
	}
	unknown := buildUnknownFingerprint(t)
	if SymbolHasCriticalKnownCoverage(unknown, "BTC-USDT-SWAP") {
		t.Fatal("unknown fingerprint must not cover BTC swap")
	}
}

func buildUnknownFingerprint(t *testing.T) fingerprint.PersonalExecutionFingerprint {
	t.Helper()
	fp := fingerprint.PersonalExecutionFingerprint{
		SchemaVersion: fingerprint.SchemaVersion,
		Generation:    fingerprint.Generation,
		BuiltAt:       time.Now().UTC(),
		ConfigHash:    "cfg",
		SourceStatus: map[string]fingerprint.SourceStatus{
			"maker_attempts":    {Status: fingerprint.StatusUnknown},
			"executable_shadow": {Status: fingerprint.StatusUnknown},
			"instrument_meta":   {Status: fingerprint.StatusUnknown},
		},
		Equity: fingerprint.EquitySummary{Status: fingerprint.StatusUnknown},
	}
	payload := fp
	payload.FingerprintHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	fp.FingerprintHash = hash
	return fp
}
