package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/g2prize"
	"lingxi/internal/routerg2/protocol"
	"lingxi/internal/routerg2/sky"
)

func testRun(args []string, stdout, stderr io.Writer) error {
	return runWithCatalogValidator(args, stdout, stderr, func(historydata.HistoryCatalog) error { return nil })
}

const testHMACKeyEnv = "LINGXI_ROUTER_G2_DISCOVERY_HMAC_KEY"
const testHMACKey = "0123456789abcdef0123456789abcdef"

func testSkyCutpoints() sky.Cutpoints {
	cuts := map[string][]float64{}
	for _, dimension := range sky.Dimensions {
		cuts[dimension] = []float64{0, 1}
	}
	return sky.Cutpoints{
		Partition: sky.FitPartition, Quantiles: []float64{0.3333333333, 0.6666666667},
		Cuts: cuts, FitSamples: 10, Frozen: true,
	}
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
	return historydata.FileStats{Path: rel, Size: info.Size(), FileHash: hex.EncodeToString(sum[:])}
}

// writeSmallFixtureTree writes a tiny, deterministic raw-history fixture
// (never real records) that produces a real, matched-control-backed
// nomination for a storm_down/liquid/1h/long conditional leaf, plus one
// unrelated fixture family, and returns everything a CLI invocation needs.
func writeSmallFixtureTree(t *testing.T, root string) (historyRoot, catalogPath, searchSpacePath string) {
	t.Helper()
	t.Setenv(testHMACKeyEnv, testHMACKey)
	historyRoot = filepath.Join(root, "raw_history")
	priceDir := filepath.Join(historyRoot, "okx_1h")
	fundingDir := filepath.Join(historyRoot, "okx_funding")
	if err := os.MkdirAll(priceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fundingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	instrument := "BTC-USDT-SWAP"
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var priceLines []string
	price := 100.0
	for i := 0; i < 260; i++ {
		if i >= 175 && i < 200 {
			price *= 0.985 // storm_down leg
		} else if i >= 210 && i < 235 {
			price *= 1.003
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

	catalog := historydata.HistoryCatalog{
		DatasetID: "history.test", RootDir: historyRoot, DataHash: protocol.HistoryDataHash, SchemaHash: "test-schema-hash",
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{fixtureFileStats(t, fundingPath, "okx_funding/"+instrument+".jsonl")}},
			{Name: "okx_1h", Files: []historydata.FileStats{fixtureFileStats(t, pricePath, "okx_1h/"+instrument+".jsonl")}},
		},
	}

	manifestEvents, err := g2prize.BuildEvents(context.Background(), g2prize.Config{
		HistoryRoot: historyRoot, Catalog: &catalog, SkyCutpoints: testSkyCutpoints(),
		MaxSymbols:      protocol.G2SmokeMaxSymbols,
		MaxFundingLines: protocol.G2SmokeMaxFundingLines,
		MaxPriceLines:   protocol.G2SmokeMaxPriceLines,
	})
	if err != nil {
		t.Fatal(err)
	}

	famA := filepath.Join(root, "family_a")
	famB := filepath.Join(root, "family_b")
	if err := os.MkdirAll(famA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(famB, 0o755); err != nil {
		t.Fatal(err)
	}
	pathA := filepath.Join(famA, "events.jsonl")
	for _, event := range manifestEvents {
		writeFixtureEvent(t, pathA, event)
	}
	pathB := filepath.Join(famB, "events.jsonl")
	writeFixtureEvent(t, pathB, unrelatedEvent())

	manifest, err := g2prize.NewManifest(
		manifestEvents, catalog, testSkyCutpoints(), protocol.Generation, protocol.RouterProtocolHash,
		protocol.G2SmokeMaxSymbols, protocol.G2SmokeMaxFundingLines, protocol.G2SmokeMaxPriceLines,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = g2prize.SignManifest(manifest, []byte(testHMACKey), "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "event_manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalogRaw, _ := json.MarshalIndent(catalog, "", "  ")
	catalogPath = filepath.Join(root, "history_catalog.json")
	if err := os.WriteFile(catalogPath, catalogRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	cutpointsRaw, _ := json.MarshalIndent(testSkyCutpoints(), "", "  ")
	if err := os.WriteFile(filepath.Join(root, "sky_cutpoints.json"), cutpointsRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	boundariesRaw, _ := json.MarshalIndent(g2prize.Boundaries{
		DiscoveryEnd: baseTime.Add(260 * time.Hour),
		ForwardStart: baseTime.Add(261 * time.Hour),
		WFStart:      baseTime.Add(262 * time.Hour),
		End:          baseTime.Add(263 * time.Hour),
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "partition_boundaries.json"), boundariesRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	space, err := g2prize.DefaultSearchSpace()
	if err != nil {
		t.Fatal(err)
	}
	spaceRaw, _ := json.MarshalIndent(space, "", "  ")
	searchSpacePath = filepath.Join(root, "search_space.json")
	if err := os.WriteFile(searchSpacePath, spaceRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	return historyRoot, catalogPath, searchSpacePath
}

func unrelatedEvent() g2prize.Event {
	dt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return g2prize.Event{
		ID: "other1", Symbol: "ETHUSDT", Cluster: "c9", SkyState: "calm_up", SorterDomain: "liquid",
		LiquidityBucket: "liq_5", Horizon: g2prize.Horizon1h, DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
		Payer: mechanism.PayerFundingPayers, Direction: mechanism.DirectionLong,
		Features: map[string]float64{"funding_rate": 0.0001}, NetOutcomeBPS: 5,
	}
}

func writeFixtureEvent(t *testing.T, path string, e g2prize.Event) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestCLIMinesSmallFixtureAndAccountsForEveryCombination(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000",
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error: %v, stderr=%s", err, stderr.String())
	}
	var report g2prize.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid report json: %v\n%s", err, stdout.String())
	}
	if len(report.Nominations)+len(report.Rejected) != protocol.BackwardRegisteredCombos {
		t.Fatalf("every frozen combination must be accounted for: %+v", report)
	}
	if report.Origin != g2prize.OriginBackwardMined {
		t.Fatalf("origin=%s", report.Origin)
	}
}

func TestCLIRejectsNonPositiveMaxFiles(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a", "-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "0", "-max-lines", "1000",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --max-files=0 (unbounded scanning must be forbidden)")
	}

	stdout.Reset()
	stderr.Reset()
	err = testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-max-files", "-1", "-max-lines", "1000",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --max-files=-1")
	}

	stdout.Reset()
	stderr.Reset()
	err = testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a", "-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "999999999", "-max-lines", "1000",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --max-files above the hard cap")
	}
}

func TestCLIRejectsNonPositiveMaxLines(t *testing.T) {
	dir := t.TempDir()
	_, _, spacePath := writeSmallFixtureTree(t, dir)

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-max-files", "10", "-max-lines", "0",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --max-lines=0 (unbounded scanning must be forbidden)")
	}
}

func TestCLIRequiresCheckpoint(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath, "-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when --checkpoint is missing; the frozen protocol requires one")
	}
}

func TestCLIRequiresInputDirHistoryAndSearchSpace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := testRun([]string{"-max-files", "10", "-max-lines", "1000"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --input-dir is missing; there must be no default that reaches real records")
	}

	dir := t.TempDir()
	writeSmallFixtureTree(t, dir)
	stdout.Reset()
	if err := testRun([]string{"-input-dir", dir, "-max-files", "10", "-max-lines", "1000"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --history-root/--history-catalog/--search-space are missing")
	}
}

func TestCLIMaxFilesBoundsFamilyScanning(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)

	var stdout, stderr bytes.Buffer
	// Only 1 file total is allowed, family_a sorts before family_b, so the
	// unrelated family_b file must never be scanned.
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a", "-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "1", "-max-lines", "1000",
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	var report g2prize.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.EventsInput == 0 {
		t.Fatal("expected the deterministically generated events from family_a to be scanned")
	}
}

func TestCLICheckpointIsIdempotentAndDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	checkpointPath := filepath.Join(t.TempDir(), "router-g2-discovery.checkpoint.json")

	args := []string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000", "-checkpoint", checkpointPath,
	}

	var stdout1, stderr1 bytes.Buffer
	if err := testRun(args, &stdout1, &stderr1); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	var stdout2, stderr2 bytes.Buffer
	if err := testRun(args, &stdout2, &stderr2); err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if stdout1.String() != stdout2.String() {
		t.Fatalf("rerun with unchanged inputs/config must be byte-identical:\nfirst=%s\nsecond=%s", stdout1.String(), stdout2.String())
	}

	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte(`"done": true`), []byte(`"done": false`), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("tampering fixture assumption failed: expected to find \"done\": true in the checkpoint file")
	}
	tampered = bytes.Replace(tampered, []byte(`"done": false`), []byte(`"done": true`), 1)
	// Flip a numeric field inside the embedded report without recomputing
	// report_hash.
	tampered = bytes.Replace(tampered, []byte(`"events_input"`), []byte(`"events_input_renamed"`), 1)
	if err := os.WriteFile(checkpointPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout3, stderr3 bytes.Buffer
	if err := testRun(args, &stdout3, &stderr3); err == nil {
		t.Fatal("a checkpoint whose embedded report no longer matches its own report_hash must fail closed")
	}
}

func TestCLICheckpointDriftIsRefused(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	checkpointPath := filepath.Join(t.TempDir(), "router-g2-discovery.checkpoint.json")

	args := []string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000", "-checkpoint", checkpointPath,
	}
	var stdout, stderr bytes.Buffer
	if err := testRun(args, &stdout, &stderr); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	extra := filepath.Join(dir, "family_a", "events.jsonl")
	writeFixtureEvent(t, extra, unrelatedEvent())

	stdout.Reset()
	stderr.Reset()
	if err := testRun(args, &stdout, &stderr); err == nil {
		t.Fatal("expected drift error when input changes after a checkpoint was sealed")
	}
}

func TestCLIWritesOutputFileWhenRequested(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	outPath := filepath.Join(t.TempDir(), "report.json")

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000", "-output", outPath,
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}
	var report g2prize.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("invalid report json in output file: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must be empty when -output is set, got %q", stdout.String())
	}
}

func TestProductionRunRejectsSelfConsistentForgedCatalog(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	err := run([]string{
		"-input-dir", dir, "-search-space", spacePath, "-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "1", "-max-lines", "1000",
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "catalog hashes/counts do not match") {
		t.Fatalf("production path must reject forged declared catalog hash, got %v", err)
	}
}

func TestCLIRejectsMissingEventManifest(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	if err := os.Remove(filepath.Join(dir, "event_manifest.json")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath, "-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000",
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when the frozen event manifest is missing")
	}
}

func TestCLIRejectsWrongHMACKey(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath, spacePath := writeSmallFixtureTree(t, dir)
	t.Setenv(testHMACKeyEnv, "different-key-different-key-different")
	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath, "-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000",
		"-checkpoint", filepath.Join(t.TempDir(), "checkpoint.json"),
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected signature verification failure with the wrong HMAC key")
	}
}
