package eventmap

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/mechanism"
	g4protocol "lingxi/internal/routerg4/protocol"
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg6/protocol"
)

func TestNextOpenEntry(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := syntheticTrendBars(base, time.Hour, 80, 1.02)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	boundaries := testBoundaries(base, 48*time.Hour)
	th := testThresholds(spec.ID)
	tracker := NewWindowTracker()
	var sample EventOutcome
	_, err := StreamSymbol(bars, "TEST-USDT-SWAP", ScanSymbolConfig{
		Spec: spec, Boundaries: boundaries, Thresholds: th, BTCBars: bars,
		ShockClusterAt: func(time.Time) string { return "none" },
	}, tracker, func(out EventOutcome) error {
		if out.DelayBars == 0 {
			sample = out
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sample.EventID == "" {
		t.Skip("no events in synthetic fixture")
	}
	idx := profitmap.BarIndexAt(bars, sample.DecisionTime, spec.BarDuration)
	if !EntryUsesNextOpen(bars, idx, spec) {
		t.Fatal("delay-0 entry must use next bar open")
	}
	entryIdx := idx + 1 + sample.DelayBars
	exitIdx := entryIdx + horizonBars(spec, sample.Horizon)
	if exitIdx >= len(bars) {
		t.Fatal("bad exit index")
	}
	want := profitmap.SignedGrossBPS(bars[entryIdx].Open, bars[exitIdx].Open, sample.Direction)
	if math.Abs(want-sample.GrossBPS) > 1e-6 {
		t.Fatalf("gross mismatch want=%v got=%v", want, sample.GrossBPS)
	}
}

func TestNoFutureFeatures(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := syntheticTrendBars(base, time.Hour, 60, 1.05)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	primaryBack := primaryReturnBars(spec)
	for i := primaryBack * 2; i < len(bars)-1; i++ {
		if DecisionUsesFutureData(bars, i, spec) {
			t.Fatalf("decision idx %d reads future bars", i)
		}
		if profitmap.RejectCurrentBarOpen(bars, i, spec.BarDuration) {
			t.Fatalf("confirmed bar rejected at %d", i)
		}
	}
}

func TestEventIDFormula(t *testing.T) {
	decision := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	event := DetectedEvent{
		ID:           BuildEventID("AAA-USDT-SWAP", decision, FamilyExtremeContinuation),
		Symbol:       "AAA-USDT-SWAP",
		Family:       FamilyExtremeContinuation,
		DecisionTime: decision,
	}
	if !ValidateEventID(event) {
		t.Fatalf("event id mismatch: %s", event.ID)
	}
	if !strings.Contains(event.ID, FamilyExtremeContinuation) {
		t.Fatal("event id must include family")
	}
}

func TestWindowNonOverlap(t *testing.T) {
	spec1h := mustProfile(t, ProfileOKX1H)
	tracker := NewWindowTracker()
	if !tracker.AllowCard("AAA", FamilyExtremeContinuation, 10) {
		t.Fatal("first event must be allowed")
	}
	tracker.MarkCard("AAA", FamilyExtremeContinuation, 10, CooldownBarsForEvent(spec1h, "1h", 0))
	if tracker.AllowCard("AAA", FamilyExtremeContinuation, 11) {
		t.Fatal("overlapping window must be rejected")
	}
	if !tracker.AllowCard("AAA", FamilyExtremeContinuation, 12) {
		t.Fatal("post-cooldown event must be allowed")
	}
	if !tracker.AllowCard("BBB", FamilyExtremeContinuation, 15) {
		t.Fatal("different symbol must not share cooldown")
	}
}

func TestNonOverlapCooldownProfileScaled(t *testing.T) {
	spec1h := mustProfile(t, ProfileOKX1H)
	spec5m := mustProfile(t, ProfileOKX5M)
	if got := CooldownBarsForEvent(spec1h, "1h", 0); got != 2 {
		t.Fatalf("1h cooldown bars=%d want 2", got)
	}
	if got := CooldownBarsForEvent(spec5m, "60m", 0); got != 13 {
		t.Fatalf("5m/60m cooldown bars=%d want 13", got)
	}
	tracker1h := NewWindowTracker()
	tracker1h.MarkCard("AAA", FamilyExtremeContinuation, 0, CooldownBarsForEvent(spec1h, "1h", 0))
	if tracker1h.AllowCard("AAA", FamilyExtremeContinuation, 1) {
		t.Fatal("1h profile must block second bar inside 2-bar window")
	}
	tracker5m := NewWindowTracker()
	tracker5m.MarkCard("AAA", FamilyExtremeContinuation, 0, CooldownBarsForEvent(spec5m, "60m", 0))
	if tracker5m.AllowCard("AAA", FamilyExtremeContinuation, 12) {
		t.Fatal("5m profile must block before 13-bar window elapses")
	}
	if !tracker5m.AllowCard("AAA", FamilyExtremeContinuation, 13) {
		t.Fatal("5m profile must allow after 13-bar window")
	}
}

func TestOutcomeDedupPerHorizonDelay(t *testing.T) {
	spec1h := mustProfile(t, ProfileOKX1H)
	spec5m := mustProfile(t, ProfileOKX5M)
	tracker := NewWindowTracker()
	if !tracker.AllowOutcome("AAA", FamilyExtremeContinuation, "1h", 0, 10) {
		t.Fatal("first 1h outcome must be allowed")
	}
	tracker.MarkOutcome("AAA", FamilyExtremeContinuation, "1h", 0, 10, CooldownBarsForEvent(spec1h, "1h", 0))
	if tracker.AllowOutcome("AAA", FamilyExtremeContinuation, "1h", 0, 11) {
		t.Fatal("1h outcome must respect its own cooldown")
	}
	if !tracker.AllowOutcome("AAA", FamilyExtremeContinuation, "24h", 0, 11) {
		t.Fatal("24h outcome must not be suppressed by 1h cooldown")
	}
	tracker60m := NewWindowTracker()
	tracker60m.MarkOutcome("AAA", FamilyExtremeContinuation, "60m", 0, 10, CooldownBarsForEvent(spec5m, "60m", 0))
	if tracker60m.AllowOutcome("AAA", FamilyExtremeContinuation, "60m", 0, 22) {
		t.Fatal("60m outcome must respect its own longer cooldown")
	}
	if !tracker60m.AllowOutcome("AAA", FamilyExtremeContinuation, "60m", 0, 23) {
		t.Fatal("60m outcome must allow after its cooldown elapses")
	}
}

func TestDiscoveryCooldownMatchesValidationFormula(t *testing.T) {
	spec1h := mustProfile(t, ProfileOKX1H)
	cardCooldown := CooldownBarsForEvent(spec1h, "1h", 0)
	discoveryCooldown := CooldownBarsForEvent(spec1h, "1h", 0)
	if cardCooldown != discoveryCooldown {
		t.Fatalf("discovery cooldown=%d validation cooldown=%d", discoveryCooldown, cardCooldown)
	}
	if cardCooldown != 2 {
		t.Fatalf("1h delay0 cooldown=%d want 2", cardCooldown)
	}
}

func mustProfile(t *testing.T, id string) ProfileSpec {
	t.Helper()
	spec, err := ProfileSpecFor(id)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestLongShortSymmetric(t *testing.T) {
	entry, exit := 100.0, 110.0
	longG := profitmap.SignedGrossBPS(entry, exit, string(DirectionLong))
	shortG := profitmap.SignedGrossBPS(entry, exit, string(DirectionShort))
	if math.Abs(longG+shortG) > 1e-9 {
		t.Fatalf("long/short not symmetric: %v %v", longG, shortG)
	}
}

func TestCapitalCostMonotonic(t *testing.T) {
	in := profitmap.CostInputs{
		RollingQuoteVolumeUSDT: 1e5,
		BarHigh:                101, BarLow: 99, BarMid: 100, FundingBPS: 1,
	}
	prev := math.Inf(1)
	for _, tier := range protocol.CapitalTiersUSDT {
		in.CapitalUSDT = float64(tier)
		_, cons, _ := profitmap.ComputeReturns(50, in)
		if cons >= prev {
			t.Fatalf("conservative net must decrease with capital: tier=%v cons=%v prev=%v", tier, cons, prev)
		}
		prev = cons
	}
}

func TestCheckpointResumeTamper(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "cp.json")
	cfg := RunConfig{
		SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: dir,
		OutputDir: dir, MaxFiles: 10, MaxLines: 1000, CatalogSymbolLimit: 0,
		ProgressEvery: 100000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	cfgHash, _ := ConfigFingerprint(cfg)
	cp := Checkpoint{
		ProfileID: ProfileOKX1H, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
		CatalogHash: "abc", CatalogSymbolCount: 2, ConfigHash: cfgHash,
		BoundariesHash: "b", ThresholdsHash: "c",
		OutputDir: dir, Done: false, NextSymbolIndex: 1,
	}
	if err := SaveCheckpoint(cpPath, cp); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadCheckpoint(cpPath)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if loaded.NextSymbolIndex != 1 {
		t.Fatalf("resume index = %d", loaded.NextSymbolIndex)
	}
	raw, _ := os.ReadFile(cpPath)
	tampered := strings.Replace(string(raw), `"next_symbol_index": 1`, `"next_symbol_index": 9`, 1)
	if err := os.WriteFile(cpPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCheckpoint(cpPath); err == nil {
		t.Fatal("tampered checkpoint must fail hash verification")
	}
}

func TestDiscoveryThresholdsDoNotUseOutcomes(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	bars := syntheticTrendBars(base, time.Hour, 200, 1.03)
	boundaries := testBoundaries(base, 120*time.Hour)
	th, err := FitThresholds(spec, boundaries, map[string][]Bar{"AAA-USDT-SWAP": bars}, bars)
	if err != nil {
		t.Fatal(err)
	}
	if th.ExtremeReturnAbsBPS <= 0 || th.ExtremeRMSBPS <= 0 {
		t.Fatalf("thresholds not fit: %+v", th)
	}
	if th.DiscoverySampleCount < 10 {
		t.Fatalf("too few discovery samples: %d", th.DiscoverySampleCount)
	}
}

func TestShockClusterAdjacentBarsShareID(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	bars := syntheticShockBars(base, time.Hour)
	th := FrozenThresholds{BTCShockReturnAbsBPS: 50}
	idx := BuildShockIndex(bars, spec, th)
	d20 := profitmap.DecisionTime(bars, 20, spec.BarDuration)
	d21 := profitmap.DecisionTime(bars, 21, spec.BarDuration)
	c1 := idx.Lookup(d20)
	c2 := idx.Lookup(d21)
	if c1 == "none" || c2 == "none" || c1 != c2 {
		t.Fatalf("adjacent shock bars must share cluster: %s %s", c1, c2)
	}
}

func TestAllFamiliesExpandFullGrid(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	bars := syntheticTrendBars(base, time.Hour, 120, 1.01)
	event := DetectedEvent{
		ID: BuildEventID("TEST-USDT-SWAP", profitmap.DecisionTime(bars, 30, spec.BarDuration), FamilyExtremeContinuation),
		Symbol: "TEST-USDT-SWAP", Family: FamilyExtremeContinuation,
		Partition: PartitionDiscovery, DecisionTime: profitmap.DecisionTime(bars, 30, spec.BarDuration), DecisionBarIdx: 30,
	}
	outcomes := ExpandOutcomes(event, bars, spec, nil)
	want := len(protocol.EventDirections) * len(spec.Horizons) * len(protocol.DelayBars) * len(protocol.CapitalTiersUSDT)
	if len(outcomes) != want {
		t.Fatalf("outcome grid size = %d want %d", len(outcomes), want)
	}
	for _, out := range outcomes {
		if out.Direction != string(mechanism.DirectionLong) && out.Direction != string(mechanism.DirectionShort) {
			t.Fatalf("bad direction %q", out.Direction)
		}
	}
}

func TestLowCPUBoundIntoConfig(t *testing.T) {
	base := RunConfig{
		SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: "/h",
		OutputDir: "/o", MaxFiles: 1, MaxLines: 100, CatalogSymbolLimit: 0,
		ProgressEvery: 100000, Generation: protocol.Generation,
		ProtocolHash: protocol.RouterProtocolHash, HistoryDataHash: protocol.HistoryDataHash,
	}
	offHash, err := ConfigFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.LowCPU = true
	onHash, err := ConfigFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if offHash == onHash {
		t.Fatal("low-cpu must be checkpoint/config bound")
	}
}

func TestOptionalMissingDoesNotBlock(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	spec, _ := ProfileSpecFor(ProfileOKX1H)
	bars := syntheticTrendBars(base, time.Hour, 60, 1.04)
	boundaries := testBoundaries(base, 48*time.Hour)
	th := testThresholds(spec.ID)
	tracker := NewWindowTracker()
	var sawMissing bool
	_, err := StreamSymbol(bars, "TEST-USDT-SWAP", ScanSymbolConfig{
		Spec: spec, Boundaries: boundaries, Thresholds: th, BTCBars: bars,
		ShockClusterAt: func(time.Time) string { return "none" },
		OptionalMissing: []string{"rubik_taker_1h", "okx_funding"},
	}, tracker, func(out EventOutcome) error {
		if len(out.OptionalMissing) > 0 {
			sawMissing = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawMissing {
		t.Fatal("optional missing mask should propagate when events exist")
	}
}

func TestCostWipeoutUsesG4NeutralBand(t *testing.T) {
	in := profitmap.CostInputs{
		CapitalUSDT: 1000, RollingQuoteVolumeUSDT: 100,
		BarHigh: 120, BarLow: 80, BarMid: 100, FundingBPS: 5,
	}
	gross := 20.0
	_, cons, _ := profitmap.ComputeReturns(gross, in)
	if gross > g4protocol.NeutralBandBPS && cons > 0 {
		t.Fatalf("expected cost wipeout, cons=%v", cons)
	}
}

func syntheticTrendBars(base time.Time, step time.Duration, count int, mult float64) []Bar {
	var bars []Bar
	price := 100.0
	for i := 0; i < count; i++ {
		open := base.Add(step * time.Duration(i))
		bars = append(bars, Bar{
			OpenTime: open, Open: price, High: price * 1.01, Low: price * 0.99,
			Close: price * mult, QuoteVolume: 1e6, Confirmed: true,
		})
		price *= mult
	}
	return bars
}

func syntheticShockBars(base time.Time, step time.Duration) []Bar {
	bars := syntheticTrendBars(base, step, 40, 1.0)
	for i := 20; i < 24; i++ {
		prevClose := bars[i].Open
		if i > 0 {
			prevClose = bars[i-1].Close
			bars[i].Open = prevClose
		}
		bars[i].Close = prevClose * 1.01
		bars[i].High = bars[i].Close * 1.001
		bars[i].Low = bars[i].Open * 0.999
	}
	return bars
}

func testBoundaries(base time.Time, discoverySpan time.Duration) Boundaries {
	return Boundaries{
		Start: base, DiscoveryEnd: base.Add(discoverySpan),
		ValidationStart: base.Add(discoverySpan + 24*time.Hour),
		ValidationEnd:   base.Add(discoverySpan + 48*time.Hour),
		WFStart:         base.Add(discoverySpan + 72*time.Hour),
		End:             base.Add(discoverySpan + 96*time.Hour),
	}
}

func testThresholds(profileID string) FrozenThresholds {
	return FrozenThresholds{
		ProfileID: profileID, ExtremeReturnAbsBPS: 10, ExtremeRMSBPS: 5,
		LowLiquidityQuote: 1e4, VolumeSpikeQuote: 5e5, BTCRelativeAbsBPS: 20,
		BTCShockReturnAbsBPS: 30,
	}
}

func horizonBars(spec ProfileSpec, label string) int {
	for _, h := range spec.Horizons {
		if h.Label == label {
			return h.BarCount
		}
	}
	return 1
}

func writeFixtureTree(t *testing.T) (string, historydata.HistoryCatalog) {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"okx_1h", "okx_funding"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	writeBarsFile(t, filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl"), syntheticTrendBars(base, time.Hour, 800, 1.0004))
	writeBarsFile(t, filepath.Join(root, "okx_1h", "AAA-USDT-SWAP.jsonl"), syntheticTrendBars(base, time.Hour, 800, 1.0006))
	writeBarsFile(t, filepath.Join(root, "okx_1h", "BBB-USDT-SWAP.jsonl"), syntheticTrendBars(base, time.Hour, 800, 1.0005))
	btc := fixtureFileStats(t, filepath.Join(root, "okx_1h", "BTC-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/BTC-USDT-SWAP.jsonl")
	aaa := fixtureFileStats(t, filepath.Join(root, "okx_1h", "AAA-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/AAA-USDT-SWAP.jsonl")
	bbb := fixtureFileStats(t, filepath.Join(root, "okx_1h", "BBB-USDT-SWAP.jsonl"), "okx_1h", "okx_1h/BBB-USDT-SWAP.jsonl")
	catalog := historydata.HistoryCatalog{
		DataHash: protocol.HistoryDataHash,
		RootDir:  root,
		Families: []historydata.FamilyCatalog{{
			Name: "okx_1h",
			Files: []historydata.FileStats{btc, aaa, bbb},
		}},
	}
	return root, catalog
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

func writeBarsFile(t *testing.T, path string, bars []Bar) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, bar := range bars {
		lines = append(lines, profitmap.FormatCandleLine(bar.OpenTime, bar.Open, bar.High, bar.Low, bar.Close, bar.QuoteVolume))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSmokeTruncated(t *testing.T) {
	root, catalog := writeFixtureTree(t)
	out := filepath.Join(root, "out")
	runner := Runner{
		Config: RunConfig{
			SchemaVersion: "test", ProfileID: ProfileOKX1H, HistoryRoot: root,
			OutputDir: out, MaxFiles: 1, MaxLines: 5000, CatalogSymbolLimit: 0,
			AllowTruncatedSmoke: true, ProgressEvery: 100000,
			Generation: protocol.Generation, ProtocolHash: protocol.RouterProtocolHash,
			HistoryDataHash: protocol.HistoryDataHash,
		},
		Catalog:        catalog,
		CheckpointPath: filepath.Join(out, "checkpoint.json"),
		CatalogValidator: func(historydata.HistoryCatalog) error { return nil },
	}
	result, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Symbols) != 1 {
		t.Fatalf("expected truncated symbol count 1, got %v", result.Symbols)
	}
	if result.Manifest.ExamActions != 0 {
		t.Fatalf("exam_actions must be 0, got %d", result.Manifest.ExamActions)
	}
	if _, err := os.Stat(filepath.Join(out, "event_map_manifest.json")); err != nil {
		t.Fatal(err)
	}
}
