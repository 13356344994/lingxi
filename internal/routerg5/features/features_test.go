package features_test

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/features"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestFeatureSchemaFrozenHash(t *testing.T) {
	s1, err := features.FrozenFeatureSchema()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := features.FrozenFeatureSchema()
	if err != nil {
		t.Fatal(err)
	}
	if s1.FeatureHash == "" || s1.FeatureHash != s2.FeatureHash {
		t.Fatalf("feature hash not frozen: %q vs %q", s1.FeatureHash, s2.FeatureHash)
	}
	for _, forbidden := range []string{"outcome", "forward_return", "reward_cube_label", "pnl"} {
		if !features.IsForbiddenFeatureKey(forbidden) {
			t.Fatalf("forbidden key %q must be rejected", forbidden)
		}
	}
}

func TestPITListingDelisting(t *testing.T) {
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	profile, _ := features.ProfileFor(features.ProfileOKX1H)
	bars := makeBars(base, 50, "LIST", 1.001)
	listing := features.ListingRule{
		ListedAt:   base.Add(24 * time.Hour),
		DelistedAt: base.Add(40 * time.Hour),
		HasCatalog: true,
	}
	in := features.PITUniverseInput{Symbol: "LIST-USDT-SWAP", Bars: bars, Listing: listing}
	early := base.Add(20 * time.Hour)
	if features.EligibleAt(in, profile, early) {
		t.Fatal("must reject before catalog listing")
	}
	mid := base.Add(30 * time.Hour)
	if !features.EligibleAt(in, profile, mid) {
		t.Fatal("must include after listing with lookback")
	}
	late := base.Add(41 * time.Hour)
	if features.EligibleAt(in, profile, late) {
		t.Fatal("must reject after delisting")
	}
}

func TestPITFutureListingNoBackfill(t *testing.T) {
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	profile, _ := features.ProfileFor(features.ProfileOKX1H)
	// Symbol starts at bar 30 — future listing relative to early timestamps.
	bars := makeBars(base.Add(29*time.Hour), 20, "NEW", 1.001)
	in := features.PITUniverseInput{Symbol: "NEW-USDT-SWAP", Bars: bars}
	decision := base.Add(30 * time.Hour)
	if features.EligibleAt(in, profile, decision) {
		t.Fatal("future listing must not backfill into current universe")
	}
}

func TestCrossSectionRankTiesDeterministic(t *testing.T) {
	values := map[string]float64{
		"AAA": 1.0,
		"BBB": 2.0,
		"CCC": 2.0,
		"DDD": 3.0,
	}
	r1 := features.PercentileRanks(values, true)
	r2 := features.PercentileRanks(values, true)
	if r1["BBB"] != r2["BBB"] || r1["CCC"] != r2["CCC"] {
		t.Fatalf("rank drift: %+v vs %+v", r1, r2)
	}
	if r1["BBB"] != r1["CCC"] {
		t.Fatalf("ties must share rank: bbb=%v ccc=%v", r1["BBB"], r1["CCC"])
	}
	if r1["AAA"] >= r1["BBB"] {
		t.Fatalf("expected AAA lower rank percentile than tied leaders: %+v", r1)
	}
	if r1["DDD"] != 1 {
		t.Fatalf("best value should have percentile 1: %+v", r1)
	}
}

func TestRankOnlyVisibleCrossSection(t *testing.T) {
	locals := map[string]features.SymbolLocal{
		"A": {Symbol: "A", Return: 10, RV: 5, AvgQuoteVolume: 100, Beta: 1, BTCRelativeReturn: 1},
		"B": {Symbol: "B", Return: 20, RV: 10, AvgQuoteVolume: 200, Beta: 2, BTCRelativeReturn: 2},
	}
	ranks := features.ApplyCrossSectionRanks(locals)
	if ranks["A"]["return_rank"] >= ranks["B"]["return_rank"] {
		t.Fatalf("return rank ordering wrong: %+v", ranks)
	}
	delete(locals, "B")
	ranksA := features.ApplyCrossSectionRanks(locals)
	if ranksA["A"]["return_rank"] != 0.5 {
		t.Fatalf("single visible symbol rank should be 0.5, got %v", ranksA["A"]["return_rank"])
	}
}

func TestSoftScoreNoHardBucket(t *testing.T) {
	btc := features.BTCFeatures{
		Return: 0.01, Vol: 0.02, Trend: 0.5, Drawdown: 0.1, Confidence: 0.75,
	}
	scores := features.SoftSkyScores(btc)
	for _, k := range []string{"btc_return", "btc_vol", "btc_trend", "btc_drawdown", "sky_confidence"} {
		if !isFinite(scores[k]) && k != "btc_funding_state" {
			t.Fatalf("missing continuous sky score %s: %+v", k, scores)
		}
	}
	if strings.Contains(jsonString(scores), `"low"`) || strings.Contains(jsonString(scores), `"high"`) {
		t.Fatal("soft scores must not contain bucket labels")
	}
}

func TestFundingMissingNonBlocking(t *testing.T) {
	btc := features.BTCFeatures{Return: 0.01, Vol: 0.02, Trend: 0.4, Drawdown: 0.05, Confidence: 1}
	scores := features.SoftSkyScores(btc)
	if scores["mask_btc_funding_missing"] != 1 {
		t.Fatal("funding mask must be 1 when missing")
	}
	if isFinite(scores["btc_return"]) && scores["sky_confidence"] > 0 {
		// OHLCV path continues without funding.
	} else {
		t.Fatal("missing funding must not block core sky scores")
	}
}

func TestCurrentBarAndOutcomeLeakRejected(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []profitmap.Bar{{
		OpenTime: base, Open: 100, High: 101, Low: 99, Close: 100.5,
		QuoteVolume: 1e6, Confirmed: false,
	}}
	profile, _ := features.ProfileFor(features.ProfileOKX1H)
	in := features.PITUniverseInput{Symbol: "X-USDT-SWAP", Bars: bars}
	if features.EligibleAt(in, profile, base.Add(time.Hour)) {
		t.Fatal("current unconfirmed bar must be rejected")
	}
	for _, key := range []string{"outcome", "forward_return", "reward_cube_label", "current_open_bar"} {
		if err := features.RejectForbiddenContext(map[string]float64{key: 1}); err == nil {
			t.Fatalf("forbidden key %q must leak-check fail", key)
		}
	}
}

func TestTimestampBatchDeterminism(t *testing.T) {
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	profile, _ := features.ProfileFor(features.ProfileOKX1H)
	schema, _ := features.FrozenFeatureSchema()
	btcBars := makeBars(base, 60, "BTC", 1.001)
	ethBars := makeBars(base, 60, "ETH", 1.002)
	solBars := makeBars(base, 60, "SOL", 0.999)
	decision := btcBars[30].OpenTime.Add(time.Hour)
	inputs := []features.PITUniverseInput{
		{Symbol: "ETH-USDT-SWAP", Bars: ethBars},
		{Symbol: "SOL-USDT-SWAP", Bars: solBars},
	}
	market := features.MarketSnapshot{BTCBars: btcBars}
	a, err := features.TimestampBatch(schema, profile, decision, "discovery", market, inputs, 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := features.TimestampBatch(schema, profile, decision, "discovery", market, inputs, 1000)
	if err != nil {
		t.Fatal(err)
	}
	ha, _ := json.Marshal(a)
	hb, _ := json.Marshal(b)
	if string(ha) != string(hb) {
		t.Fatal("timestamp batch must be deterministic")
	}
}

func TestCheckpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	cp := features.Checkpoint{
		ProfileID: features.ProfileOKX1H, Generation: g5protocol.Generation,
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
		CatalogHash: "cat", ConfigHash: "cfg", BoundariesHash: "b", SelectedFilesHash: "s",
		FeatureHash: "fh", OutputDir: dir, NextShardIndex: 1, TotalRows: 10,
	}
	if err := features.SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := features.LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load checkpoint: ok=%v err=%v", ok, err)
	}
	if loaded.NextShardIndex != 1 || loaded.TotalRows != 10 {
		t.Fatalf("checkpoint resume mismatch: %+v", loaded)
	}
	raw, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	rawStr := strings.Replace(string(raw), `"total_rows": 10`, `"total_rows": 999`, 1)
	if err := os.WriteFile(cpPath, []byte(rawStr), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := features.LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash on reload")
	}
}

func TestWriteShardManifest(t *testing.T) {
	dir := t.TempDir()
	schema, _ := features.FrozenFeatureSchema()
	row, err := features.BuildDecisionContext(
		schema, features.ProfileOKX1H, "ETH-USDT-SWAP",
		time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "discovery",
		features.BTCFeatures{Return: 0.01, Vol: 0.02, Trend: 0.5, Drawdown: 0.1, Confidence: 1},
		features.SymbolLocal{Symbol: "ETH-USDT-SWAP", Return: 5, RV: 10, Beta: 1.1, LiquidityScore: 6, CapacityHeadroom: 0.8},
		map[string]float64{"return_rank": 0.5},
	)
	if err != nil {
		t.Fatal(err)
	}
	shard, err := features.WriteContextShard(dir, features.ShardManifest{
		ProfileID: features.ProfileOKX1H, ShardIndex: 0,
		FileName: "feature_context_okx_1h_0000.jsonl",
	}, []features.DecisionContext{row})
	if err != nil {
		t.Fatal(err)
	}
	if shard.RowCount != 1 || shard.FileHash == "" {
		t.Fatalf("shard manifest incomplete: %+v", shard)
	}
	rows, err := features.ReadContextShard(filepath.Join(dir, shard.FileName))
	if err != nil || len(rows) != 1 {
		t.Fatalf("read shard: %v len=%d", err, len(rows))
	}
}

func makeBars(base time.Time, n int, prefix string, drift float64) []profitmap.Bar {
	bars := make([]profitmap.Bar, n)
	price := 100.0
	for i := 0; i < n; i++ {
		price *= drift
		bars[i] = profitmap.Bar{
			OpenTime: base.Add(time.Duration(i) * time.Hour),
			Open: price / drift, High: price * 1.001, Low: price * 0.999,
			Close: price, QuoteVolume: float64(1_000_000 + i*1000), Confirmed: true,
		}
	}
	_ = prefix
	return bars
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func jsonString(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
