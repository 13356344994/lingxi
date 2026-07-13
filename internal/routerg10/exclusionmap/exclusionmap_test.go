package exclusionmap

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
	g10announcements "lingxi/internal/routerg10/announcements"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/eventmap"
	"lingxi/internal/routerg6/protocol"
)

func TestTripleExclusionProtocolNoDrift(t *testing.T) {
	assertStrSlice(t, "axes", TripleExclusionAxes, g10protocolv2.TripleExclusionAxes)
	assertStrSlice(t, "institution", InstitutionEvidence, g10protocolv2.InstitutionExclusionEvidence)
	assertStrSlice(t, "retail", RetailEvidence, g10protocolv2.RetailExclusionEvidence)
	assertStrSlice(t, "bot", BotEvidence, g10protocolv2.BotExclusionEvidence)
	assertStrSlice(t, "forbidden", ForbiddenWildEvidence, g10protocolv2.ForbiddenWildEvidence)
}

func TestRejectPublicCrowded(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = manualScalableTiers()
	ctx.CellWinnerCount = publicCrowdedWinnerThreshold
	result := EvaluateEvent(ctx, 60)
	if result.Accepted {
		t.Fatal("public crowded must be rejected")
	}
	if !contains(result.RejectReasons, RejectPublicCrowded) {
		t.Fatalf("want PUBLIC_CROWDED, got %+v", result.RejectReasons)
	}
}

func TestRejectFixedSignal(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Item.Event.EventFamily = eventmap.FamilyExtremeContinuation
	ctx.Tiers = manualScalableTiers()
	result := EvaluateEvent(ctx, 60)
	if !contains(result.RejectReasons, RejectFixedSignal) {
		t.Fatalf("want FIXED_SIGNAL, got %+v", result.RejectReasons)
	}
}

func TestRejectFirstInOnly(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Item.ProfileID = eventmap.ProfileOKX5M
	ctx.Item.Event.DelayBars = 0
	result := EvaluateFromGross(ctx, 40)
	if !contains(result.RejectReasons, RejectFirstInOnly) {
		t.Fatalf("want FIRST_IN_ONLY, got %+v", result.RejectReasons)
	}
}

func TestAcceptPersonalConditionDependent(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Base.RollingQuoteVolumeUSDT = 5e5
	ctx.Base.DelayBars = 1
	ctx.CellCount = 6
	ctx.Tiers = cliffTiers()
	result := EvaluateFromGross(ctx, 80)
	if !result.Accepted {
		t.Fatalf("expected accepted exclusion proof, got %+v", result.RejectReasons)
	}
	if result.AcceptReason != AcceptPersonalConditionDependent {
		t.Fatalf("accept reason = %q want %q", result.AcceptReason, AcceptPersonalConditionDependent)
	}
	if !result.Institution.Excluded || !result.Retail.Excluded || !result.Bot.Excluded {
		t.Fatal("triple axis must be excluded")
	}
	for _, axis := range []AxisExclusion{result.Institution, result.Retail, result.Bot} {
		if axis.PrimaryEvidence == "" {
			t.Fatalf("axis %s missing primary evidence", axis.Axis)
		}
		if len(axis.EvidenceBindings) == 0 {
			t.Fatalf("axis %s missing metric bindings", axis.Axis)
		}
	}
}

func TestCrowdingStressLevels135(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	stress := ComputeCrowdingStress(ctx, 80)
	if len(stress.Levels) != 3 {
		t.Fatalf("levels = %d want 3", len(stress.Levels))
	}
	want := []int{1, 3, 5}
	for i, level := range stress.Levels {
		if level.Competitors != want[i] {
			t.Fatalf("level[%d] competitors = %d want %d", i, level.Competitors, want[i])
		}
	}
}

func TestProofHashTamperDetection(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	result := EvaluateFromGross(ctx, 80)
	proof, err := BuildProof(ctx.Item, result, g10constitution.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProof(proof); err != nil {
		t.Fatal(err)
	}
	proof.Institution.PrimaryEvidence = "tampered"
	if err := ValidateProof(proof); err == nil {
		t.Fatal("tampered proof must fail validation")
	}
}

func TestLedgerAndMapHashes(t *testing.T) {
	ctx := baseMetricContext(t)
	ctx.Tiers = cliffTiers()
	result := EvaluateFromGross(ctx, 80)
	proof, err := BuildProof(ctx.Item, result, "const-hash")
	if err != nil {
		t.Fatal(err)
	}
	cfg := RunConfig{SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: "/o", MapDirs: []string{"/m"}, AnnouncementDir: "/a", MaxFiles: 1, MaxLines: 100, CandidateLimit: 5, ProgressEvery: 10}
	exclusionMap, err := BuildExclusionMap(cfg, "const-hash", []ExclusionProof{proof})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := BuildLedger("const-hash", map[string]string{"okx_1h": "mh"}, "tape", "ann-manifest", []ExperimentAttempt{
		NewAttempt(1, ctx.Item, result, proof),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateLedger(ledger); err != nil {
		t.Fatal(err)
	}
	wantMap, err := HashExclusionMap(exclusionMap)
	if err != nil || wantMap != exclusionMap.MapHash {
		t.Fatal("map hash mismatch")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig, HistoryRoot: "/h", OutputDir: dir,
		MapDirs: []string{"/m"}, AnnouncementDir: "/a",
		MaxFiles: 1, MaxLines: 100, CandidateLimit: 5, ProgressEvery: 10,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	cp := NewCheckpoint(cfg, g10protocolv2.HistoryDataHash, "const", map[string]string{"okx_1h": "mh"}, "tape", "ann")
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load checkpoint: ok=%v err=%v", ok, err)
	}
	if err := VerifyCheckpointBinding(loaded, cfg, g10protocolv2.HistoryDataHash, "const", map[string]string{"okx_1h": "mh"}, "tape", "ann"); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerWithSyntheticFixtures(t *testing.T) {
	root, catalog, mapDir, annDir := writeSyntheticFixtures(t)
	out := filepath.Join(root, "exclusion_out")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: root, OutputDir: out, MapDirs: []string{mapDir}, AnnouncementDir: annDir,
		MaxFiles: 10, MaxLines: 2000, CandidateLimit: 32, ProgressEvery: 1,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog, CheckpointPath: filepath.Join(out, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.ManifestHash == "" || result.Map.MapHash == "" {
		t.Fatal("expected sealed outputs")
	}
	if result.Map.ConstitutionHash == "" {
		t.Fatal("constitution hash required")
	}
	if _, err := os.Stat(filepath.Join(out, OutputFileName)); err != nil {
		t.Fatal(err)
	}
	assertArtifactIntegrity(t, out, result)
}

func TestArtifactIntegrityRoundTrip(t *testing.T) {
	root, catalog, mapDir, annDir := writeSyntheticFixtures(t)
	out := filepath.Join(root, "exclusion_out")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: root, OutputDir: out, MapDirs: []string{mapDir}, AnnouncementDir: annDir,
		MaxFiles: 10, MaxLines: 2000, CandidateLimit: 32, ProgressEvery: 1,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog, CheckpointPath: filepath.Join(out, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertArtifactIntegrity(t, out, result)
	manifest, err := LoadManifest(filepath.Join(out, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOutputArtifacts(out, manifest, result.Summary); err != nil {
		t.Fatal(err)
	}
}

func TestFailClosedArtifactIntegrityRoundTrip(t *testing.T) {
	root, catalog, mapDir, _ := writeSyntheticFixtures(t)
	annOut := filepath.Join(root, "ann_incomplete")
	annCfg := g10announcements.RunConfig{
		SchemaVersion: g10announcements.ConfigSchema,
		Generation:    g10announcements.Generation,
		ArchiveRoot:   filepath.Join(root, "missing_archive_root"),
		OutputDir:     annOut,
		MaxLines:      100,
	}
	annRunner := g10announcements.Runner{Config: annCfg, CheckpointPath: filepath.Join(annOut, "cp.json")}
	annResult, err := annRunner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !annResult.Tape.Incomplete {
		t.Fatal("missing archives must fail closed")
	}
	out := filepath.Join(root, "exclusion_failclosed")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: root, OutputDir: out, MapDirs: []string{mapDir}, AnnouncementDir: annOut,
		MaxFiles: 10, MaxLines: 2000, CandidateLimit: 32, ProgressEvery: 1,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog, CheckpointPath: filepath.Join(out, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.FailClosedReason != RejectIncompletePIT {
		t.Fatalf("fail_closed_reason=%q want %q", result.Summary.FailClosedReason, RejectIncompletePIT)
	}
	assertArtifactIntegrity(t, out, result)
}

func TestCompletedResumeRoundTrip(t *testing.T) {
	root, catalog, mapDir, annDir := writeSyntheticFixtures(t)
	out := filepath.Join(root, "exclusion_out")
	cfg := RunConfig{
		SchemaVersion: SchemaVersionConfig,
		HistoryRoot: root, OutputDir: out, MapDirs: []string{mapDir}, AnnouncementDir: annDir,
		MaxFiles: 10, MaxLines: 2000, CandidateLimit: 32, ProgressEvery: 1,
		Generation: g10protocolv2.Generation, ProtocolHash: g10protocolv2.RouterProtocolHash,
		HistoryDataHash: g10protocolv2.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog, CheckpointPath: filepath.Join(out, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	first, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.ManifestHash != first.Manifest.ManifestHash {
		t.Fatalf("resume manifest hash drift: %s vs %s", second.Manifest.ManifestHash, first.Manifest.ManifestHash)
	}
	if second.Summary.ManifestHash != second.Manifest.ManifestHash {
		t.Fatal("resumed summary must bind final manifest hash")
	}
	assertArtifactIntegrity(t, out, second)
}

func assertArtifactIntegrity(t *testing.T, out string, result RunResult) {
	t.Helper()
	if result.Summary.ManifestHash != result.Manifest.ManifestHash {
		t.Fatalf("summary manifest_hash=%s disk manifest_hash=%s",
			result.Summary.ManifestHash, result.Manifest.ManifestHash)
	}
	if result.Summary.MapHash != result.Map.MapHash {
		t.Fatal("summary map hash mismatch")
	}
	if result.Summary.LedgerHash != result.Ledger.LedgerHash {
		t.Fatal("summary ledger hash mismatch")
	}
	if err := VerifyOutputArtifacts(out, result.Manifest, result.Summary); err != nil {
		t.Fatal(err)
	}
	diskManifest, err := LoadManifest(filepath.Join(out, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if diskManifest.ManifestHash != result.Manifest.ManifestHash {
		t.Fatalf("disk manifest hash=%s result manifest hash=%s",
			diskManifest.ManifestHash, result.Manifest.ManifestHash)
	}
	rawSummary, err := os.ReadFile(filepath.Join(out, SummaryFileName))
	if err != nil {
		t.Fatal(err)
	}
	var diskSummary RunSummary
	if err := json.Unmarshal(rawSummary, &diskSummary); err != nil {
		t.Fatal(err)
	}
	if diskSummary.ManifestHash != diskManifest.ManifestHash {
		t.Fatalf("disk summary manifest_hash=%s disk manifest_hash=%s",
			diskSummary.ManifestHash, diskManifest.ManifestHash)
	}
}

func baseMetricContext(t *testing.T) MetricContext {
	t.Helper()
	return MetricContext{
		Item: EventWorkItem{
			ProfileID: eventmap.ProfileOKX1H,
			ManifestHash: "map-hash",
			ShockCluster: "shock-a",
			Event: residualmap.TriggeredEventEntry{
				EventID: "evt-test", Symbol: "BTC-USDT-SWAP",
				EventFamily: eventmap.FamilyExtremeReversal,
				Horizon: "1h", DelayBars: 1,
				DecisionTime: time.Date(2023, 6, 3, 0, 0, 0, 0, time.UTC),
			},
		},
		Base: residualmap.CostBase{
			RollingQuoteVolumeUSDT: 5e5,
			BarHigh: 100.2, BarLow: 99.8, BarMid: 100,
			ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1, HoldHours: 1,
		},
		CellCount: 6,
		ConstitutionHash: "const-hash",
		MapManifestHash: "map-hash",
		DecisionBarIdx: 48,
		DynamicMinNotionalUSDT: 10,
	}
}

func cliffTiers() map[string]residualmap.TierCostBreakdown {
	base := residualmap.CostBase{
		RollingQuoteVolumeUSDT: 5e5,
		BarHigh: 100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1, HoldHours: 1,
	}
	return residualmap.ComputeTierCosts(80, base, residualmap.PersonalAdjustments{}, 100)
}

func manualScalableTiers() map[string]residualmap.TierCostBreakdown {
	mk := func(label string, cap, cons float64, capFail bool) residualmap.TierCostBreakdown {
		return residualmap.TierCostBreakdown{
			TierLabel: label, CapitalUSDT: cap, ConservativeBPS: cons, CapacityFail: capFail,
			GrossBPS: cons + 10, FeeBPS: 5, SlipBPS: 2, ImpactBPS: 1, FundingBPS: 2,
		}
	}
	return map[string]residualmap.TierCostBreakdown{
		"30":     mk("30", 30, 60, false),
		"100":    mk("100", 100, 55, false),
		"1000":   mk("1000", 1000, 50, false),
		"10000":  mk("10000", 10000, 45, false),
		"100000": mk("100000", 100000, 40, false),
	}
}

func scalableTiers() map[string]residualmap.TierCostBreakdown {
	base := residualmap.CostBase{
		RollingQuoteVolumeUSDT: 1e9,
		BarHigh: 100.2, BarLow: 99.8, BarMid: 100,
		ProfileID: eventmap.ProfileOKX1H, Horizon: "1h", DelayBars: 1, HoldHours: 1,
	}
	return residualmap.ComputeTierCosts(60, base, residualmap.PersonalAdjustments{}, 100)
}

func writeSyntheticFixtures(t *testing.T) (root string, catalog historydata.HistoryCatalog, mapDir, annDir string) {
	t.Helper()
	root = t.TempDir()
	for _, sub := range []string{"okx_1h", "okx_funding"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	writeBars(t, root, "okx_1h", "BTC-USDT-SWAP", base, time.Hour, 200, 1.002)

	mapDir = filepath.Join(root, "map")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	discovery := eventmap.PartitionMap{
		ProfileID: eventmap.ProfileOKX1H, Partition: eventmap.PartitionDiscovery,
		Cells: map[string]eventmap.PartitionMapCell{
			"k": {
				Key: eventmap.CellKey{EventFamily: eventmap.FamilyExtremeReversal, Horizon: "1h", Direction: "long", DelayBars: 1, CapitalTier: 30},
				Stats: eventmap.CellStats{Count: 6},
			},
		},
	}
	discoveryName := fmt.Sprintf("event_map_%s_%s.json", eventmap.PartitionDiscovery, eventmap.ProfileOKX1H)
	discoveryPath := filepath.Join(mapDir, discoveryName)
	writeJSONFile(t, discoveryPath, discovery)
	discoveryHash, _ := eventmap.HashFile(discoveryPath)
	thresholds := eventmap.FrozenThresholds{ProfileID: eventmap.ProfileOKX1H, ExtremeReturnAbsBPS: 50, ExtremeRMSBPS: 20}
	thresholdsPath := filepath.Join(mapDir, "event_map_thresholds.json")
	writeJSONFile(t, thresholdsPath, thresholds)
	thresholdsHash, _ := eventmap.HashFile(thresholdsPath)
	manifest := eventmap.MapManifest{
		SchemaVersion: "lingxi.router-g6-event-map.manifest.v1",
		Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
		HistoryDataHash: protocol.HistoryDataHash, ProfileID: eventmap.ProfileOKX1H,
		CatalogHash: g10protocolv2.HistoryDataHash, FullMarket: true,
		ArtifactHashes: map[string]string{discoveryName: discoveryHash, "event_map_thresholds.json": thresholdsHash},
	}
	btcPath := filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl")
	catalog = historydata.HistoryCatalog{
		DataHash: g10protocolv2.HistoryDataHash,
		Families: []historydata.FamilyCatalog{{
			Name: "okx_1h",
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

	annDir = filepath.Join(root, "announcements")
	if err := os.MkdirAll(annDir, 0o755); err != nil {
		t.Fatal(err)
	}
	annRoot := filepath.Join("..", "announcements", "testdata")
	annCfg := g10announcements.RunConfig{
		SchemaVersion: g10announcements.ConfigSchema, Generation: g10announcements.Generation,
		ArchiveRoot: annRoot, OutputDir: annDir,
		Sources: g10announcements.SourcePaths{
			OKX:     filepath.Join(annRoot, "multi_venue.jsonl"),
			Binance: filepath.Join(annRoot, "multi_venue.jsonl"),
			Bybit:   filepath.Join(annRoot, "multi_venue.jsonl"),
			Gate:    filepath.Join(annRoot, "multi_venue.jsonl"),
		},
		MaxLines: 100,
	}
	annRunner := g10announcements.Runner{Config: annCfg, CheckpointPath: filepath.Join(annDir, "cp.json")}
	if _, err := annRunner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return root, catalog, mapDir, annDir
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
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashMapManifest(t *testing.T, manifest eventmap.MapManifest) string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["manifest_hash"] = ""
	hash, _, err := datatruth.HashJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	return hash
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
		Lines: int64(strings.Count(string(raw), "\n")),
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func assertStrSlice(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d want %d", name, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q want %q", name, i, got[i], want[i])
		}
	}
}

// unused import guard for profitmap in fixtures
var _ = profitmap.ProfileOKX1H
