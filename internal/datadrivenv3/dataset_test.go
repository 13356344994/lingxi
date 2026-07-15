package datadrivenv3

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReverseIteratorChronological(t *testing.T) {
	root := makeStreamFixture(t, []string{"BTC-USDT-SWAP"}, 72)
	it, err := openPriceIterator(filepath.Join(root, "records/history/okx_1h/BTC-USDT-SWAP.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer it.lines.Close()
	last := int64(-1)
	for {
		if it.current.Time <= last {
			t.Fatalf("order %d <= %d", it.current.Time, last)
		}
		last = it.current.Time
		if err := it.advance(); err != nil {
			break
		}
	}
}
func TestRankDeterministicAndTies(t *testing.T) {
	rows := []computedRow{{it: &priceIterator{symbol: "B"}}, {it: &priceIterator{symbol: "A"}}, {it: &priceIterator{symbol: "C"}}}
	rows[0].values[fOwnRet1] = 1
	rows[1].values[fOwnRet1] = 1
	rows[2].values[fOwnRet1] = 2
	rankFeature(rows, fOwnRet1, fCSRetRank)
	if rows[0].values[fCSRetRank] != .25 || rows[1].values[fCSRetRank] != .25 || rows[2].values[fCSRetRank] != 1 {
		t.Fatalf("ranks=%v %v %v", rows[0].values[fCSRetRank], rows[1].values[fCSRetRank], rows[2].values[fCSRetRank])
	}
}

type futureOptional struct{}

func (futureOptional) At(_ string, decision int64) (OptionalPoint, error) {
	return OptionalPoint{HasFunding: true, ObservedAtMS: decision + 1}, nil
}
func TestPITRejectsFutureOptional(t *testing.T) {
	root := makeStreamFixture(t, []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"}, 24*5)
	cfg := fixtureConfig(root, filepath.Join(root, "benchmark-future"), []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"})
	cfg.CompleteDays = 1
	cfg.RequireCompleteCrossSection = true
	cfg.Optional = futureOptional{}
	if _, err := BuildStateRowsV2(cfg); err == nil || !strings.Contains(err.Error(), "future leak") {
		t.Fatalf("future optional accepted: %v", err)
	}
}
func TestMonthAtomicAndStalledCheckpointReject(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "benchmark-atomic")
	w, err := openMonthWriter(out, "discovery/2024-01")
	if err != nil {
		t.Fatal(err)
	}
	row := StateRowV2{DecisionMS: 1}
	if err := w.encode(row); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.final); !os.IsNotExist(err) {
		t.Fatal("final visible before seal")
	}
	receipt, err := w.seal("binding")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Rows != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	cpPath := filepath.Join(out, "checkpoint.json")
	if err := os.WriteFile(cpPath, []byte(`{"schema_version":"lingxi.data-driven-router-v3.dataset-checkpoint.v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCheckpointV2(cpPath, out, "binding"); err == nil || !strings.Contains(err.Error(), StalledV1Status) {
		t.Fatalf("stalled checkpoint accepted: %v", err)
	}
}
func TestCrashPartialRemovedAndSealedResumeVerified(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "benchmark-resume")
	w, err := openMonthWriter(out, "discovery/2024-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.encode(StateRowV2{DecisionMS: 1}); err != nil {
		t.Fatal(err)
	}
	_ = w.gzip.Close()
	_ = w.file.Close()
	if err := removePartials(out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.partial); !os.IsNotExist(err) {
		t.Fatal("partial survived restart cleanup")
	}
	w, err = openMonthWriter(out, "discovery/2024-01")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.encode(StateRowV2{DecisionMS: 2})
	receipt, err := w.seal("binding")
	if err != nil {
		t.Fatal(err)
	}
	cp := CheckpointV2{SchemaVersion: "lingxi.data-driven-router-v3.dataset-checkpoint.v2", Status: "BUILDING", SourceBinding: "binding", SealedThrough: 2, Shards: []ShardReceiptV2{receipt}}
	if err := writeCheckpointV2(filepath.Join(out, "checkpoint.json"), cp); err != nil {
		t.Fatal(err)
	}
	got, err := loadCheckpointV2(filepath.Join(out, "checkpoint.json"), out, "binding")
	if err != nil || got.SealedThrough != 2 {
		t.Fatalf("resume=%+v %v", got, err)
	}
}
func TestBuilderSchemaOrderAndDeterminism(t *testing.T) {
	for _, name := range FeatureOrder {
		if strings.Contains(name, "symbol") || strings.Contains(name, "date") {
			t.Fatalf("forbidden feature %s", name)
		}
	}
	if len(FeatureOrder) != FeatureCount {
		t.Fatal("schema count drift")
	}
	root := makeStreamFixture(t, []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"}, 24*7)
	cfg1 := fixtureConfig(root, filepath.Join(root, "benchmark-one"), []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"})
	cfg1.CompleteDays = 2
	cfg1.RequireCompleteCrossSection = true
	cfg2 := cfg1
	cfg2.OutputDir = filepath.Join(root, "benchmark-two")
	one, err := BuildStateRowsV2(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := BuildStateRowsV2(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if one.Rows != 96 || two.Rows != 96 {
		t.Fatalf("rows %d %d", one.Rows, two.Rows)
	}
	var h1, h2 []string
	for _, s := range one.Shards {
		h1 = append(h1, s.SHA256)
	}
	for _, s := range two.Shards {
		h2 = append(h2, s.SHA256)
	}
	if !reflect.DeepEqual(h1, h2) {
		t.Fatalf("non-deterministic hashes %v %v", h1, h2)
	}
}

func TestBuilderAppliesGlobalPurgeAtPartitionBoundaries(t *testing.T) {
	root := makeStreamFixture(t, []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"}, 24*12)
	cfg := fixtureConfig(root, filepath.Join(root, "benchmark-purge"), []string{"ADA-USDT-SWAP", "BTC-USDT-SWAP"})
	cfg.Bounds = [3]time.Time{
		time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
	}
	result, err := BuildStateRowsV2(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range result.Shards {
		for _, boundary := range cfg.Bounds {
			lower := boundary.Add(-24 * time.Hour).UnixMilli()
			upper := boundary.Add(24 * time.Hour).UnixMilli()
			if shard.FirstMS >= lower && shard.FirstMS <= upper ||
				shard.LastMS >= lower && shard.LastMS <= upper {
				t.Fatalf("shard %s reaches purge window around %s: %d..%d", shard.Path, boundary, shard.FirstMS, shard.LastMS)
			}
		}
	}
}

func makeStreamFixture(t *testing.T, symbols []string, hours int) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "records/history/okx_1h")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for symbolIndex, symbol := range symbols {
		var body []byte
		// Source history is newest-first.
		for i := hours - 1; i >= 0; i-- {
			ts := start.Add(time.Duration(i) * time.Hour).UnixMilli()
			price := 100 + symbolIndex*10 + i%17
			row := map[string]any{"data": []string{formatInt(ts), formatInt(int64(price)), formatInt(int64(price + 2)), formatInt(int64(price - 2)), formatInt(int64(price + 1)), "1", "1", formatInt(int64(100000 + price)), "1"}}
			raw, _ := json.Marshal(row)
			body = append(body, raw...)
			body = append(body, '\n')
		}
		if err := os.WriteFile(filepath.Join(dir, symbol+".jsonl"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "research/firstbattle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "research/firstbattle/history_catalog.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
func fixtureConfig(root, out string, symbols []string) BuildConfigV2 {
	return BuildConfigV2{Root: root, OutputDir: out, Symbols: symbols, ProtocolHashes: map[string]string{"p": "h"}, FundingManifestHash: "m", FundingReceiptHash: "r", Bounds: [3]time.Time{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}}
}
func formatInt(v int64) string { return strconv.FormatInt(v, 10) }
