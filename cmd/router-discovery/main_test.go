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
	"lingxi/internal/hierarchicalrouter"
	"lingxi/internal/mechanism"
	"lingxi/internal/routerdiscovery/backward"
	"lingxi/internal/routerdiscovery/historyevents"
)

func testRun(args []string, stdout, stderr io.Writer) error {
	return runWithCatalogValidator(args, stdout, stderr, func(historydata.HistoryCatalog) error { return nil })
}

// writeFixtureEvent appends one backward.Event JSON line to path.
func writeFixtureEvent(t *testing.T, path string, e backward.Event) {
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

func fixtureEvent(id, symbol, cluster string, decisionOffset time.Duration, feature, net float64) backward.Event {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	dt := base.Add(decisionOffset)
	return backward.Event{
		ID: id, Symbol: symbol, Cluster: cluster, Stratum: "s1",
		DecisionTime: dt, FeatureAsOf: dt.Add(-time.Minute),
		Payer: "funding_receivers", Direction: mechanism.DirectionLong, Volatility: 1.0, Liquidity: 1.0,
		Features: map[string]float64{
			"signal":       feature,
			"funding_rate": map[bool]float64{true: 0.002, false: 0.0002}[feature >= 0.8],
		}, NetOutcomeBPS: net,
	}
}

// writeSmallFixtureTree writes a tiny, deterministic set of fixture families
// (never real records) with a real, matched-control-backed winner effect
// across two clusters, plus one unrelated family.
func writeSmallFixtureTree(t *testing.T, root string) (string, string) {
	t.Helper()
	t.Setenv("LINGXI_ROUTER_DISCOVERY_HMAC_KEY", "0123456789abcdef0123456789abcdef")
	historyRoot := filepath.Join(root, "raw_history")
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
	pricePath := filepath.Join(priceDir, instrument+".jsonl")
	var priceLines []string
	for i := 0; i < 60; i++ {
		price := 100 + float64(i)
		row := map[string]any{"data": []string{
			fmt.Sprint(baseTime.Add(time.Duration(i) * time.Hour).UnixMilli()),
			fmt.Sprint(price), fmt.Sprint(price + 1), fmt.Sprint(price - 1),
			fmt.Sprint(price + 0.5), "1", "1", "1000000", "1",
		}}
		raw, _ := json.Marshal(row)
		priceLines = append(priceLines, string(raw))
	}
	if err := os.WriteFile(pricePath, []byte(strings.Join(priceLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fundingPath := filepath.Join(fundingDir, instrument+".jsonl")
	var fundingLines []string
	for _, item := range []struct {
		index int
		rate  float64
	}{{25, .002}, {26, .0002}, {33, .002}, {34, .0002}, {41, .002}, {42, .0002}} {
		row := map[string]any{"data": map[string]string{
			"fundingRate": fmt.Sprint(item.rate),
			"fundingTime": fmt.Sprint(baseTime.Add(time.Duration(item.index) * time.Hour).UnixMilli()),
			"instId":      instrument,
		}}
		raw, _ := json.Marshal(row)
		fundingLines = append(fundingLines, string(raw))
	}
	if err := os.WriteFile(fundingPath, []byte(strings.Join(fundingLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := historydata.HistoryCatalog{
		DatasetID: "history.test", RootDir: historyRoot,
		DataHash: hierarchicalrouter.HistoryDataHash, SchemaHash: "schema",
		Families: []historydata.FamilyCatalog{
			{Name: "okx_funding", Files: []historydata.FileStats{fixtureFileStats(t, fundingPath, "okx_funding/"+instrument+".jsonl")}},
			{Name: "okx_1h", Files: []historydata.FileStats{fixtureFileStats(t, pricePath, "okx_1h/"+instrument+".jsonl")}},
		},
	}
	manifestEvents, err := historyevents.BuildFundingPostSettlement(context.Background(), historyevents.Config{
		HistoryRoot: historyRoot, Catalog: &catalog,
		MaxSymbols:      hierarchicalrouter.BackwardSmokeMaxSymbols,
		MaxFundingLines: hierarchicalrouter.BackwardSmokeMaxFundingLines,
		MaxPriceLines:   hierarchicalrouter.BackwardSmokeMaxPriceLines,
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
	writeFixtureEvent(t, pathB, fixtureEvent("other1", "ETHUSDT", "c9", 0, 0.05, 5))
	manifest, err := historyevents.NewManifest(
		manifestEvents, catalog,
		hierarchicalrouter.BackwardSmokeMaxSymbols,
		hierarchicalrouter.BackwardSmokeMaxFundingLines,
		hierarchicalrouter.BackwardSmokeMaxPriceLines,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = historyevents.SignManifest(manifest, []byte("0123456789abcdef0123456789abcdef"), "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "event_manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalogRaw, _ := json.MarshalIndent(catalog, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "history_catalog.json"), catalogRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	return historyRoot, filepath.Join(root, "history_catalog.json")
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

func writeSearchSpaceFile(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean("../../research/firstbattle/backward_search_space.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCLIMinesSmallFixtureAndNominatesCandidate(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)

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
	var report backward.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid report json: %v\n%s", err, stdout.String())
	}
	if len(report.Nominations)+len(report.Rejected) != 2 {
		t.Fatalf("every frozen combination must be accounted for: %+v", report)
	}
}

func TestCLIRejectsNonPositiveMaxFiles(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)

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
}

func TestCLIRejectsNonPositiveMaxLines(t *testing.T) {
	dir := t.TempDir()
	writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)

	var stdout, stderr bytes.Buffer
	err := testRun([]string{
		"-input-dir", dir, "-search-space", spacePath,
		"-max-files", "10", "-max-lines", "0",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for --max-lines=0 (unbounded scanning must be forbidden)")
	}
}

func TestCLIRequiresInputDirAndSearchSpace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := testRun([]string{"-max-files", "10", "-max-lines", "1000"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --input-dir is missing; there must be no default that reaches real records")
	}

	dir := t.TempDir()
	writeSmallFixtureTree(t, dir)
	stdout.Reset()
	if err := testRun([]string{"-input-dir", dir, "-max-files", "10", "-max-lines", "1000"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when --search-space is missing")
	}
}

func TestCLIMaxFilesBoundsFamilyScanning(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)

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
	var report backward.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.EventsInput != 6 {
		t.Fatalf("expected only the six deterministically generated events, got %d", report.EventsInput)
	}
}

func TestCLICheckpointIsIdempotentAndDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)
	checkpointPath := filepath.Join(t.TempDir(), "router-discovery.checkpoint.json")

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

	// Plain rerun with unchanged inputs must be byte-identical.
	var stdout2, stderr2 bytes.Buffer
	if err := testRun(args, &stdout2, &stderr2); err != nil {
		t.Fatalf("second run error: %v", err)
	}
	if stdout1.String() != stdout2.String() {
		t.Fatalf("rerun with unchanged inputs/config must be byte-identical:\nfirst=%s\nsecond=%s", stdout1.String(), stdout2.String())
	}

	// Tampering the cached report without recomputing its commitment must fail.
	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"search_space_size": 2`), []byte(`"search_space_size": 9`), 1)
	if err := os.WriteFile(checkpointPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout3, stderr3 bytes.Buffer
	if err := testRun(args, &stdout3, &stderr3); err == nil || !strings.Contains(err.Error(), "report hash mismatch") {
		t.Fatalf("tampered checkpoint must fail closed, got %v", err)
	}
}

func TestCLICheckpointDriftIsRefused(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)
	checkpointPath := filepath.Join(t.TempDir(), "router-discovery.checkpoint.json")

	args := []string{
		"-input-dir", dir, "-search-space", spacePath,
		"-family", "family_a",
		"-history-root", historyRoot, "-history-catalog", catalogPath,
		"-max-files", "10", "-max-lines", "1000", "-checkpoint", checkpointPath,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := testRun(args, &stdout, &stderr); err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Mutate the fixture input after the checkpoint was sealed.
	extra := filepath.Join(dir, "family_a", "events.jsonl")
	writeFixtureEvent(t, extra, fixtureEvent("drifted", "BTCUSDT", "c99", 0, 0.9, 40))

	stdout.Reset()
	stderr.Reset()
	if err := testRun(args, &stdout, &stderr); err == nil {
		t.Fatal("expected drift error when input changes after a checkpoint was sealed")
	}
}

func TestCLIWritesOutputFileWhenRequested(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)
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
	var report backward.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("invalid report json in output file: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must be empty when -output is set, got %q", stdout.String())
	}
}

func TestProductionRunRejectsSelfConsistentForgedCatalog(t *testing.T) {
	dir := t.TempDir()
	historyRoot, catalogPath := writeSmallFixtureTree(t, dir)
	spacePath := filepath.Join(dir, "search_space.json")
	writeSearchSpaceFile(t, spacePath)
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
