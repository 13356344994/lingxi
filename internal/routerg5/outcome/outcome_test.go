package outcome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg5/actionspace"
	g5protocol "lingxi/internal/routerg5/protocol"
)

func TestDelayEntryTiming(t *testing.T) {
	bars := syntheticBars(t, 40, 1.01)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	flat := actionspace.StandardOracleStates(bars[10].Close)[0]
	action := actionspace.Action{
		Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
		TargetHorizon: "1h", SizeFraction: 0.5, DelayBars: 2,
	}
	rec, ok := EvaluateReward(bars, 10, flat, action, spec.Horizons[0], spec, nil)
	if !ok {
		t.Fatal("expected delayed reward")
	}
	entryIndex := 10 + 1 + 2
	exitIndex := entryIndex + 1
	if bars[entryIndex].Open <= 0 || bars[exitIndex].Open <= 0 {
		t.Fatal("bad indices")
	}
	if rec.TurnoverUSDT <= 0 {
		t.Fatal("open must have turnover")
	}
}

func TestOpenHoldTrimCloseReverse(t *testing.T) {
	bars := syntheticBars(t, 60, 1.005)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	horizon := spec.Horizons[0]
	states := actionspace.StandardOracleStates(bars[20].Close)
	verbs := map[string]bool{}
	for _, state := range states[1:] {
		for _, verb := range []string{
			actionspace.VerbHold, actionspace.VerbIncrease, actionspace.VerbTrim,
			actionspace.VerbClose, actionspace.VerbReverse,
		} {
			size := 0.25
			if verb == actionspace.VerbHold || verb == actionspace.VerbClose {
				size = 0
			}
			action := actionspace.Action{
				Verb: verb, Direction: state.Side, TargetHorizon: horizon.Label,
				SizeFraction: size, DelayBars: 0,
			}
			rec, ok := EvaluateReward(bars, 20, state, action, horizon, spec, nil)
			if !ok {
				t.Fatalf("failed %s from %s", verb, state.Side)
			}
			verbs[verb] = true
			if rec.Label == "" {
				t.Fatal("reward label required")
			}
		}
	}
	for _, verb := range []string{actionspace.VerbHold, actionspace.VerbTrim, actionspace.VerbClose, actionspace.VerbReverse} {
		if !verbs[verb] {
			t.Fatalf("missing verb %s", verb)
		}
	}
}

func TestCurrentBarRejected(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []profitmap.Bar{{
		OpenTime: base, Open: 100, High: 101, Low: 99, Close: 100.5,
		QuoteVolume: 1e6, Confirmed: false,
	}}
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	flat := actionspace.StandardOracleStates(100)[0]
	action := actionspace.Action{
		Verb: actionspace.VerbFlat, TargetHorizon: "1h", DelayBars: 0,
	}
	if _, ok := EvaluateReward(bars, 0, flat, action, spec.Horizons[0], spec, nil); ok {
		t.Fatal("unconfirmed bar must be rejected")
	}
}

func TestOverlapRewardNotDoubleCounted(t *testing.T) {
	bars := syntheticBars(t, 50, 1.002)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	state := actionspace.StandardOracleStates(bars[15].Close)[1]
	if !OverlapIncrementalOK(bars, 15, state, spec) {
		t.Fatal("hold reward must equal incremental mark-to-market delta")
	}
	openAction := actionspace.Action{
		Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
		TargetHorizon: spec.Horizons[0].Label, SizeFraction: 0.5, DelayBars: 0,
	}
	openRec, ok := EvaluateReward(bars, 15, actionspace.StandardOracleStates(bars[15].Close)[0], openAction, spec.Horizons[0], spec, nil)
	if !ok {
		t.Fatal("open failed")
	}
	holdRec, ok := EvaluateReward(bars, 15, state, actionspace.Action{
		Verb: actionspace.VerbHold, Direction: actionspace.DirectionLong,
		TargetHorizon: spec.Horizons[0].Label, DelayBars: 0,
	}, spec.Horizons[0], spec, nil)
	if !ok {
		t.Fatal("hold failed")
	}
	if math.Abs(openRec.GrossEquityDeltaBPS-holdRec.GrossEquityDeltaBPS) < 1e-6 {
		t.Fatal("open and hold from positioned state must not duplicate the same path")
	}
}

func TestSizeMonotonicAndLeverageCap(t *testing.T) {
	bars := syntheticBars(t, 50, 1.003)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	flat := actionspace.StandardOracleStates(bars[10].Close)[0]
	var records []RewardRecord
	for _, size := range []float64{0.1, 0.25, 0.5, 1.0} {
		action := actionspace.Action{
			Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
			TargetHorizon: "1h", SizeFraction: size, DelayBars: 0,
		}
		rec, ok := EvaluateReward(bars, 10, flat, action, spec.Horizons[0], spec, nil)
		if !ok {
			t.Fatalf("size %v failed", size)
		}
		post := actionspace.PositionState{Side: actionspace.SideLong, SizeFraction: size, EquityUSDT: actionspace.BaseEquityUSDT}
		if !LeverageCapOK(post) {
			t.Fatalf("size %v exceeds leverage cap", size)
		}
		records = append(records, rec)
	}
	prev := math.Inf(1)
	for _, rec := range records {
		if rec.ConservativeEquityDeltaBPS > prev+1e-6 {
			t.Fatalf("conservative reward must not increase with size: %v > %v", rec.ConservativeEquityDeltaBPS, prev)
		}
		prev = rec.ConservativeEquityDeltaBPS
	}
}

func TestIncreaseLabelSplitsOldAndNewLegs(t *testing.T) {
	bars := syntheticBars(t, 60, 1.005)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	horizon := spec.Horizons[0]
	long := actionspace.StandardOracleStates(bars[20].Close)[1]
	action := actionspace.Action{
		Verb: actionspace.VerbIncrease, Direction: actionspace.DirectionLong,
		TargetHorizon: horizon.Label, SizeFraction: 0.25, DelayBars: 1,
	}
	rec, ok := EvaluateReward(bars, 20, long, action, horizon, spec, nil)
	if !ok {
		t.Fatal("increase reward failed")
	}
	flatOpen, ok := EvaluateReward(bars, 20, actionspace.StandardOracleStates(bars[20].Close)[0], actionspace.Action{
		Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
		TargetHorizon: horizon.Label, SizeFraction: 0.25, DelayBars: 1,
	}, horizon, spec, nil)
	if !ok {
		t.Fatal("open reward failed")
	}
	if math.Abs(rec.ConservativeEquityDeltaBPS-flatOpen.ConservativeEquityDeltaBPS) < 1e-6 {
		t.Fatal("increase from positioned state must differ from fresh open")
	}
	if rec.TurnoverUSDT <= 0 {
		t.Fatal("increase must charge turnover on added notional")
	}
}

func TestCheckpointResumeTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: "test", ProfileID: profitmap.ProfileOKX1H, HistoryRoot: dir,
		OutputDir: dir, MaxFiles: 10, MaxLines: 1000, CatalogSymbolLimit: 0,
		ProgressEvery: 100000, Generation: g5protocol.Generation,
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		ProfileID: profitmap.ProfileOKX1H, Generation: g5protocol.Generation,
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
		CatalogHash: "abc", CatalogSymbolCount: 2, ConfigHash: cfgHash,
		BoundariesHash: "b", CutpointsHash: "c", SymbolClassHash: "d",
		OutputDir: dir, Done: false, NextSymbolIndex: 1,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok || loaded.NextSymbolIndex != 1 {
		t.Fatalf("load failed ok=%v err=%v idx=%d", ok, err, loaded.NextSymbolIndex)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"next_symbol_index": 1`, `"next_symbol_index": 9`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail")
	}
}

func TestRunnerSmokeAndPartitions(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	outDir := filepath.Join(root, "out")
	cfg := RunConfig{
		SchemaVersion: "test", ProfileID: profitmap.ProfileOKX1H, HistoryRoot: root,
		OutputDir: outDir, MaxFiles: 100, MaxLines: 5000, CatalogSymbolLimit: 0,
		ProgressEvery: DefaultProgressEvery(), Generation: g5protocol.Generation,
		ProtocolHash: g5protocol.RouterProtocolHash, HistoryDataHash: g5protocol.HistoryDataHash,
	}
	runner := Runner{
		Config: cfg, Catalog: catalog,
		CheckpointPath:   filepath.Join(outDir, "cp.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Store.CellCount() == 0 {
		t.Fatal("expected cube cells")
	}
	if len(result.Summaries) != 3 {
		t.Fatalf("want 3 partition summaries, got %d", len(result.Summaries))
	}
	if result.Manifest.ExamActions != 0 {
		t.Fatal("exam_actions must be 0")
	}
	for _, part := range []string{PartitionDiscovery, PartitionValidation, PartitionWalkFwd} {
		if _, err := os.Stat(filepath.Join(outDir, fmt.Sprintf("cube_%s_okx_1h.json", part))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrainingLabelReservoirDeterministic(t *testing.T) {
	store := NewCubeStore()
	base := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1200; i++ {
		verb := g5protocol.ActionVerbs[i%len(g5protocol.ActionVerbs)]
		reward := float64(i%30 - 15)
		rec := RewardRecord{
			Symbol: "S", Partition: PartitionDiscovery,
			DecisionTime: base.Add(time.Duration(i) * time.Minute),
			StateLabel: actionspace.SideFlat, Action: actionspace.Action{
				Verb: verb, Direction: actionspace.DirectionFlat,
				TargetHorizon: "1h", SizeFraction: 0.25, DelayBars: 0,
			},
			SoftRegime: "sky", GrossEquityDeltaBPS: reward,
			ConservativeEquityDeltaBPS: reward, Label: fmt.Sprintf("l-%d", i),
		}
		store.Add(rec, "liq_1", "major")
	}
	labels := store.TrainingLabels()
	if len(labels) > g5protocol.DiscoveryTrainingLabelReservoir {
		t.Fatalf("training reservoir exceeded cap: %d", len(labels))
	}
	if len(labels) == 0 {
		t.Fatal("expected training labels")
	}
	var pos, neg bool
	verbs := map[string]bool{}
	for _, e := range labels {
		verbs[e.Action.Verb] = true
		if e.ConservativeEquityDeltaBPS > 0 {
			pos = true
		}
		if e.ConservativeEquityDeltaBPS < 0 {
			neg = true
		}
	}
	if !pos || !neg {
		t.Fatal("training labels must include positive and negative samples")
	}
	if len(verbs) < 2 {
		t.Fatal("training labels must include multiple verbs")
	}
	h1, _ := HashTrainingLabels(labels)
	h2, _ := HashTrainingLabels(store.TrainingLabels())
	if h1 != h2 {
		t.Fatal("training label hash must be deterministic")
	}
}

func TestCubeReservoirBoundedMemory(t *testing.T) {
	store := NewCubeStore()
	for i := 0; i < 500; i++ {
		rec := RewardRecord{
			Symbol: "S", Partition: PartitionDiscovery,
			DecisionTime: time.Unix(int64(i), 0).UTC(),
			StateLabel: actionspace.SideFlat, Action: actionspace.Action{
				Verb: actionspace.VerbOpenLong, Direction: actionspace.DirectionLong,
				TargetHorizon: "1h", SizeFraction: 0.5, DelayBars: 0,
			},
			SoftRegime: "sky", GrossEquityDeltaBPS: float64(i),
			ConservativeEquityDeltaBPS: float64(i - 5), Label: fmt.Sprintf("l-%d", i),
		}
		store.Add(rec, "liq_1", "major")
	}
	pm := store.ExportPartitionScoped(profitmap.ProfileOKX1H, PartitionDiscovery, CubeScope{FullMarket: true, CubeScope: FullMarketScope})
	for _, cell := range pm.Cells {
		if len(cell.Stats.ConservativeSamples) > QuantileReservoirPerCell {
			t.Fatalf("quantile reservoir exceeded cap")
		}
		if len(cell.Winners) > WinnerReservoirPerCell {
			t.Fatalf("winner reservoir exceeded cap")
		}
	}
}

func syntheticBars(t *testing.T, n int, drift float64) []profitmap.Bar {
	t.Helper()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var bars []profitmap.Bar
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(time.Duration(i) * time.Hour)
		bars = append(bars, profitmap.Bar{
			OpenTime: open, Open: price, High: price + 1, Low: price - 1,
			Close: price * drift, QuoteVolume: 2e6, Confirmed: true,
		})
		price *= drift
	}
	return bars
}

func writeFixtureTree(t *testing.T) (root string, catalog historydata.HistoryCatalog) {
	t.Helper()
	root = t.TempDir()
	for _, sub := range []string{"okx_1h", "okx_5m", "okx_funding"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	writeInstrument(t, root, "okx_1h", "BTC-USDT-SWAP", base, time.Hour, 800, 1.0004)
	writeInstrument(t, root, "okx_1h", "ETH-USDT-SWAP", base, time.Hour, 800, 1.0006)
	writeFunding(t, root, "BTC-USDT-SWAP", base, 800)
	btc1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/BTC-USDT-SWAP.jsonl")
	eth1h := fixtureFileStats(t, filepath.Join(root, "okx_1h", "ETH-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/ETH-USDT-SWAP.jsonl")
	fund := fixtureFileStats(t, filepath.Join(root, "okx_funding", "BTC-USDT-SWAP.jsonl"), "okx_funding", "okx_funding/BTC-USDT-SWAP.jsonl")
	catalog = historydata.HistoryCatalog{
		DataHash: g5protocol.HistoryDataHash,
		Families: []historydata.FamilyCatalog{
			{Name: "okx_1h", Files: []historydata.FileStats{btc1h, eth1h}},
			{Name: "okx_funding", Files: []historydata.FileStats{fund}},
		},
	}
	return root, catalog
}

func writeInstrument(t *testing.T, root, family, instrument string, base time.Time, step time.Duration, n int, drift float64) {
	t.Helper()
	var lines []string
	price := 100.0
	for i := 0; i < n; i++ {
		open := base.Add(step * time.Duration(i))
		lines = append(lines, profitmap.FormatCandleLine(open, price, price+1, price-1, price*drift, 2e6))
		price *= drift
	}
	path := filepath.Join(root, family, instrument+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFunding(t *testing.T, root, instrument string, base time.Time, hours int) {
	t.Helper()
	var lines []string
	for i := 0; i < hours; i += 8 {
		row := map[string]any{"data": map[string]string{
			"fundingRate": "0.0001",
			"fundingTime": fmt.Sprint(base.Add(time.Duration(i) * time.Hour).UnixMilli()),
		}}
		raw, _ := json.Marshal(row)
		lines = append(lines, string(raw))
	}
	path := filepath.Join(root, "okx_funding", instrument+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
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

func TestTrimOracleUsesTrimmedNotional(t *testing.T) {
	bars := syntheticBars(t, 60, 1.005)
	spec, _ := profitmap.ProfileSpecFor(profitmap.ProfileOKX1H)
	long := actionspace.StandardOracleStates(bars[20].Close)[1]
	trim := actionspace.Action{Verb: actionspace.VerbTrim, Direction: actionspace.DirectionLong, TargetHorizon: spec.Horizons[0].Label, SizeFraction: 0.25, DelayBars: 0}
	closeAction := actionspace.Action{Verb: actionspace.VerbClose, Direction: actionspace.DirectionLong, TargetHorizon: spec.Horizons[0].Label, DelayBars: 0}
	trimRec, ok := EvaluateReward(bars, 20, long, trim, spec.Horizons[0], spec, nil)
	if !ok {
		t.Fatal("trim reward failed")
	}
	closeRec, ok := EvaluateReward(bars, 20, long, closeAction, spec.Horizons[0], spec, nil)
	if !ok {
		t.Fatal("close reward failed")
	}
	if math.Abs(trimRec.ConservativeEquityDeltaBPS) >= math.Abs(closeRec.ConservativeEquityDeltaBPS) {
		t.Fatalf("trim delta magnitude must be smaller than full close: trim=%v close=%v", trimRec.ConservativeEquityDeltaBPS, closeRec.ConservativeEquityDeltaBPS)
	}
	prior := actionspace.NotionalUSDT(long)
	trimRatio := 0.25 / long.SizeFraction
	if math.Abs(trimRec.MarginUSDT-prior*trimRatio) > prior*0.01 {
		t.Fatalf("trim capital=%v want ~%v", trimRec.MarginUSDT, prior*trimRatio)
	}
}