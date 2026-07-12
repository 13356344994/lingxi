package mechanismpanel

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

// buildTestdataSources opens the canonical testdata fixture set (small, hand-computed scenario:
// a 3-event OKX liquidation burst on BTC-USDT with one bybit confirmation, an OI drawdown, a
// depth snapshot and a mid path spanning before/during/after the episode).
func buildTestdataSources(t *testing.T) []Source {
	t.Helper()
	dir := "testdata"
	liq, err := NewLiquidationSource(filepath.Join(dir, "liquidation.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("liquidation source: %v", err)
	}
	xv, err := NewXVenueLiquidationSource(filepath.Join(dir, "xvenue.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("xvenue source: %v", err)
	}
	mids, err := NewMarketMidsSource(filepath.Join(dir, "mids.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("mids source: %v", err)
	}
	oi, err := NewXMetaOISource(filepath.Join(dir, "xmeta.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("xmeta source: %v", err)
	}
	depth, err := NewDepthSource(filepath.Join(dir, "depth.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("depth source: %v", err)
	}
	return []Source{liq, xv, mids, oi, depth}
}

// runAggregatorOverTestdata feeds the full merged stream through datatruth.Observe (real
// three-clock/quality pipeline, not a stub) into a fresh Aggregator, then Flushes it.
func runAggregatorOverTestdata(t *testing.T, cfg AggregatorConfig) ([]EntryFeatures, []FutureLabel) {
	t.Helper()
	sources := buildTestdataSources(t)
	defer func() {
		for _, s := range sources {
			s.Close()
		}
	}()
	cat, err := LoadInstrumentCatalog(filepath.Join("testdata", "instrument_meta.json"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	cfg.InstrumentCatalog = cat

	var features []EntryFeatures
	var labels []FutureLabel
	agg := NewAggregator(cfg, func(f EntryFeatures) { features = append(features, f) }, func(l FutureLabel) { labels = append(labels, l) })

	merger, err := NewMerger(sources)
	if err != nil {
		t.Fatalf("NewMerger: %v", err)
	}
	states := map[string]datatruth.StreamState{}
	for {
		rec, ok, err := merger.Next()
		if err != nil {
			t.Fatalf("merger.Next: %v", err)
		}
		if !ok {
			break
		}
		key := streamKeyForKind(rec.Kind)
		next, assess := datatruth.Observe(states[key], rec.Envelope, datatruth.ObserveOptions{})
		states[key] = next
		rec.Envelope = assess.Event
		if !assess.Accepted {
			continue
		}
		if err := agg.Ingest(rec, assess); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	agg.Flush()
	return features, labels
}

func testAggregatorConfig() AggregatorConfig {
	cfg := DefaultAggregatorConfig()
	cfg.GapThreshold = 5 * time.Second
	cfg.MaxEpisodeDuration = time.Minute
	cfg.LabelHorizons = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	return cfg
}

func TestEpisodeAggregationOverFixture(t *testing.T) {
	features, labels := runAggregatorOverTestdata(t, testAggregatorConfig())

	if len(features) != 1 {
		t.Fatalf("expected exactly 1 episode of EntryFeatures, got %d: %+v", len(features), features)
	}
	if len(labels) != 1 {
		t.Fatalf("expected exactly 1 FutureLabel, got %d", len(labels))
	}
	f := features[0]

	wantStart := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	wantAsOf := time.Date(2026, 7, 11, 0, 0, 8, 0, time.UTC)
	wantDecision := wantAsOf.Add(250 * time.Millisecond)
	if !f.Episode.StartTime.Equal(wantStart) {
		t.Errorf("StartTime = %v, want %v", f.Episode.StartTime, wantStart)
	}
	if !f.Episode.AsOfTime.Equal(wantAsOf) {
		t.Errorf("AsOfTime = %v, want %v", f.Episode.AsOfTime, wantAsOf)
	}
	if !f.Episode.DecisionTime.Equal(wantDecision) {
		t.Errorf("DecisionTime = %v, want %v", f.Episode.DecisionTime, wantDecision)
	}
	if f.Episode.Phase != PhaseRelay {
		t.Fatalf("phase = %q, want relay", f.Episode.Phase)
	}
	if f.Episode.RawEpisodeID == "" {
		t.Fatalf("RawEpisodeID should not be empty")
	}
	if f.Episode.EpisodeID != f.Episode.RawEpisodeID+"#relay" {
		t.Fatalf("EpisodeID = %q, want %q", f.Episode.EpisodeID, f.Episode.RawEpisodeID+"#relay")
	}
	if f.Episode.Instrument != "BTC-USDT" {
		t.Errorf("Instrument = %q, want BTC-USDT", f.Episode.Instrument)
	}

	// ForcedFlow: 3 OKX events (qty*ctVal*bk_px) + 1 bybit event in as-of window.
	wantOKX := 10*0.01*60000 + 15*0.01*59950 + 30*0.01*59900
	wantBybit := 2 * 59900.0
	if !almostEqual(f.ForcedFlow.VenueNotional["okx"], wantOKX, 1e-6) {
		t.Errorf("okx notional = %v, want %v", f.ForcedFlow.VenueNotional["okx"], wantOKX)
	}
	if !almostEqual(f.ForcedFlow.VenueNotional["bybit"], wantBybit, 1e-6) {
		t.Errorf("bybit notional = %v, want %v", f.ForcedFlow.VenueNotional["bybit"], wantBybit)
	}
	if !almostEqual(f.ForcedFlow.TotalNotional, wantOKX+wantBybit, 1e-6) {
		t.Errorf("total notional = %v, want %v", f.ForcedFlow.TotalNotional, wantOKX+wantBybit)
	}
	if f.ForcedFlow.DominantSide != "long_squeezed" {
		t.Errorf("dominant side = %q, want long_squeezed", f.ForcedFlow.DominantSide)
	}
	if f.ForcedFlow.EventCount != 4 {
		t.Errorf("event count = %d, want 4 (3 okx + 1 bybit)", f.ForcedFlow.EventCount)
	}

	// Acceleration: split at StartTime+4s. t=0,4 -> first half; t=8 -> second half.
	wantFirst := 10*0.01*60000 + 15*0.01*59950
	wantSecond := 30 * 0.01 * 59900
	if !almostEqual(f.Acceleration.FirstHalfNotional, wantFirst, 1e-6) {
		t.Errorf("first half = %v, want %v", f.Acceleration.FirstHalfNotional, wantFirst)
	}
	if !almostEqual(f.Acceleration.SecondHalfNotional, wantSecond, 1e-6) {
		t.Errorf("second half = %v, want %v", f.Acceleration.SecondHalfNotional, wantSecond)
	}
	if !f.Acceleration.Accelerating {
		t.Errorf("expected Accelerating=true (second half %v > first half %v)", wantSecond, wantFirst)
	}

	// OI: bybit venue, start 1,000,000 -> end 950,000 (the t=12s sample of 850,000 is strictly
	// after AsOfTime=8s and MUST NOT leak into the feature).
	if !f.OI.Observed {
		t.Fatalf("expected OI.Observed=true")
	}
	if f.OI.Venue != "bybit" || f.OI.StartOI != 1000000 || f.OI.EndOI != 950000 {
		t.Errorf("OI change unexpected: %+v", f.OI)
	}
	if !almostEqual(f.OI.DropRatio, 0.05, 1e-9) {
		t.Errorf("OI drop ratio = %v, want 0.05", f.OI.DropRatio)
	}

	// Cross-venue confirmation: bybit event at t=6s, 6s after StartTime.
	if !f.CrossVenue.Confirmed {
		t.Fatalf("expected cross-venue confirmation")
	}
	if len(f.CrossVenue.Venues) != 1 || f.CrossVenue.Venues[0] != "bybit" {
		t.Errorf("cross venue venues = %v, want [bybit]", f.CrossVenue.Venues)
	}
	if f.CrossVenue.LagMs != 6000 {
		t.Errorf("cross venue lag = %dms, want 6000", f.CrossVenue.LagMs)
	}

	// Price path only contains points with event_time <= AsOfTime (t=2s, t=6s).
	if len(f.PricePath) != 2 {
		t.Fatalf("expected 2 price path points at/before as-of time, got %d: %+v", len(f.PricePath), f.PricePath)
	}
	if f.PricePath[0].OffsetMs != 2000 || f.PricePath[1].OffsetMs != 6000 {
		t.Errorf("unexpected price path offsets: %+v", f.PricePath)
	}
	for _, p := range f.PricePath {
		if p.OffsetMs > f.Episode.AsOfTime.Sub(f.Episode.StartTime).Milliseconds() {
			t.Errorf("price path point %+v leaks beyond AsOfTime", p)
		}
	}

	// Absorption: depth snapshot at t=7s (479280 notional) is the last one at/before as-of
	// time; the t=12s snapshot (leaks otherwise very different depth) must be excluded.
	if !f.Absorption.Observed {
		t.Fatalf("expected Absorption.Observed=true")
	}
	wantDepthNotional := 59900.0*4 + 59920.0*4
	if !almostEqual(f.Absorption.TopDepthNotional, wantDepthNotional, 1e-6) {
		t.Errorf("top depth notional = %v, want %v", f.Absorption.TopDepthNotional, wantDepthNotional)
	}
	entryMid, exitMid := 60010.0, 59900.0
	wantMoveBps := (exitMid/entryMid - 1) * 10000
	if !almostEqual(f.Absorption.PriceMoveBps, wantMoveBps, 1e-6) {
		t.Errorf("price move bps = %v, want %v", f.Absorption.PriceMoveBps, wantMoveBps)
	}

	if f.Quality.MissingCtVal {
		t.Errorf("MissingCtVal should be false, instrument_meta.json has BTC-USDT")
	}
	if f.Quality.MissingOI || f.Quality.MissingDepth {
		t.Errorf("MissingOI/MissingDepth should be false: %+v", f.Quality)
	}
	if f.Quality.EvictedCapacity {
		t.Errorf("this episode should close via natural gap timeout, not capacity eviction")
	}

	// ===== physical separation: FutureLabel must never reference decision-time-or-earlier data =====
	l := labels[0]
	if l.EpisodeID != f.Episode.EpisodeID {
		t.Fatalf("label episode id %q does not match feature episode id %q", l.EpisodeID, f.Episode.EpisodeID)
	}
	if l.RawEpisodeID != f.Episode.RawEpisodeID {
		t.Fatalf("label raw episode id %q does not match feature raw id %q", l.RawEpisodeID, f.Episode.RawEpisodeID)
	}
	if l.Phase != f.Episode.Phase {
		t.Fatalf("label phase %q does not match feature phase %q", l.Phase, f.Episode.Phase)
	}
	if !l.DecisionTime.Equal(f.Episode.DecisionTime) {
		t.Errorf("label decision time = %v, want %v", l.DecisionTime, f.Episode.DecisionTime)
	}
	if !l.Complete {
		t.Errorf("expected label to complete within the fixture's future horizon coverage")
	}
	for _, p := range l.FuturePricePath {
		if p.OffsetMs <= 0 {
			t.Fatalf("FutureLabel leaked a point at/before decision time: %+v", p)
		}
	}
}

func TestEpisodeSplitsByGapIntoIndependentEpisodes(t *testing.T) {
	cfg := testAggregatorConfig()
	cfg.MinTriggerNotionalUSDT = 1
	var features []EntryFeatures
	agg := NewAggregator(cfg, func(f EntryFeatures) { features = append(features, f) }, nil)

	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	send := func(offsetSec int, qty, bkPx float64) {
		at := base.Add(time.Duration(offsetSec) * time.Second)
		rec := Record{Kind: KindLiquidation, Envelope: mkEnvelope(at), Liquidation: &LiquidationRecord{RecordedAt: at, Symbol: "ETH-USDT", PosSide: "short", Side: "buy", Qty: qty, BkPx: bkPx}}
		_ = agg.Ingest(rec, datatruth.EventAssessment{Accepted: true})
	}
	send(0, 1, 3000)
	send(2, 1, 3000)
	send(4, 3, 3000) // 第一段 episode (relay 在 t=4s 触发)
	send(200, 1, 3000)
	send(202, 1, 3000)
	send(204, 3, 3000) // 第二段 episode
	agg.Flush()

	if len(features) != 2 {
		t.Fatalf("expected 2 independent episodes, got %d", len(features))
	}
	if features[0].ForcedFlow.EventCount != 3 {
		t.Errorf("first episode should have 3 events, got %d", features[0].ForcedFlow.EventCount)
	}
	if features[1].ForcedFlow.EventCount != 3 {
		t.Errorf("second episode should have 3 events, got %d", features[1].ForcedFlow.EventCount)
	}
	if features[0].Episode.RawEpisodeID == features[1].Episode.RawEpisodeID {
		t.Fatalf("split episodes must carry different RawEpisodeID")
	}
}

func TestComputeFeaturesRejectsRecordsReceivedAfterAsOf(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	asOf := base.Add(10 * time.Second)
	ep := &openEpisode{
		ID:         "BTC-USDT-late",
		Instrument: "BTC-USDT",
		StartTime:  base,
		StartMid:   100,
		ForcingEvents: []timedNotional{
			{T: base, R: base, Venue: "okx", PosSide: "long", Notional: 100},
			{T: base.Add(2 * time.Second), R: base.Add(11 * time.Second), Venue: "okx", PosSide: "long", Notional: 900},
		},
		Mids: []timedPoint{
			{T: base.Add(time.Second), R: base.Add(time.Second), V: 99},
			{T: base.Add(3 * time.Second), R: base.Add(12 * time.Second), V: 50},
		},
	}

	f, _ := computeFeaturesAsOf(ep, testAggregatorConfig(), PhaseRelay, asOf, false, asOf)
	if f.ForcedFlow.TotalNotional != 100 || f.ForcedFlow.EventCount != 1 {
		t.Fatalf("late forcing event leaked into features: %+v", f.ForcedFlow)
	}
	if len(f.PricePath) != 1 || f.PricePath[0].Mid != 99 {
		t.Fatalf("late mid leaked into price path: %+v", f.PricePath)
	}
}

func TestSyntheticFixtureDualPhaseFeatureLagFutureIsolationAndOutOfOrder(t *testing.T) {
	cfg := testAggregatorConfig()
	cfg.GapThreshold = 10 * time.Second
	cfg.FeatureLagMs = 1000
	cfg.MinTriggerNotionalUSDT = 50000
	cfg.LabelHorizons = []time.Duration{1 * time.Second, 2 * time.Second}

	var features []EntryFeatures
	var labels []FutureLabel
	agg := NewAggregator(cfg, func(f EntryFeatures) { features = append(features, f) }, func(l FutureLabel) { labels = append(labels, l) })

	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	ingest := func(rec Record) {
		if err := agg.Ingest(rec, datatruth.EventAssessment{Accepted: true}); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	// 主 episode: 先 relay(加速触发),后在 gap 确认后 exhaustion 触发。
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base), Liquidation: &LiquidationRecord{RecordedAt: base, Symbol: "BTC-USDT", PosSide: "long", Side: "sell", Qty: 1, BkPx: 10000}})
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base.Add(1 * time.Second)), Liquidation: &LiquidationRecord{RecordedAt: base.Add(1 * time.Second), Symbol: "BTC-USDT", PosSide: "long", Side: "sell", Qty: 2, BkPx: 10000}})
	// 反向跨所(short)必须不确认 dominant long_squeezed。
	ingest(Record{Kind: KindXVenueLiquidation, Envelope: mkEnvelope(base.Add(1500 * time.Millisecond)), XVenue: &XVenueLiquidationRecord{RecordedAt: base.Add(1500 * time.Millisecond), Venue: "bybit", Symbol: "BTC-USDT", PosSide: "short", Qty: 5, Price: 10000}})

	// 乱序 mids: 先到 t=8s,后补 t=1s/t=2s/t=2.5s; relay 在 t=2s 触发时必须能保留 t=1s/t=2s。
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(8 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(8 * time.Second), Mids: map[string]float64{"BTC-USDT": 101}}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(1 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(1 * time.Second), Mids: map[string]float64{"BTC-USDT": 99}}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(2 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(2 * time.Second), Mids: map[string]float64{"BTC-USDT": 98}}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(2500 * time.Millisecond)), Mids: &MarketMidsRecord{RecordedAt: base.Add(2500 * time.Millisecond), Mids: map[string]float64{"BTC-USDT": 97}}})
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base.Add(2 * time.Second)), Liquidation: &LiquidationRecord{RecordedAt: base.Add(2 * time.Second), Symbol: "BTC-USDT", PosSide: "long", Side: "sell", Qty: 5, BkPx: 10000}})
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base.Add(2200 * time.Millisecond)), Liquidation: &LiquidationRecord{RecordedAt: base.Add(2200 * time.Millisecond), Symbol: "BTC-USDT", PosSide: "long", Side: "sell", Qty: 0.5, BkPx: 10000}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(4 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(4 * time.Second), Mids: map[string]float64{"BTC-USDT": 98.5}}})
	ingest(Record{Kind: KindMarketMids, Envelope: mkEnvelope(base.Add(5 * time.Second)), Mids: &MarketMidsRecord{RecordedAt: base.Add(5 * time.Second), Mids: map[string]float64{"BTC-USDT": 99}}})

	ingest(Record{Kind: KindDepth, Envelope: mkEnvelope(base), Depth: &DepthRecord{RecordedAt: base, Symbol: "BTC-USDT", BidPx: 100, BidQty: 2000, AskPx: 100, AskQty: 2000}})
	ingest(Record{Kind: KindDepth, Envelope: mkEnvelope(base.Add(2200 * time.Millisecond)), Depth: &DepthRecord{RecordedAt: base.Add(2200 * time.Millisecond), Symbol: "BTC-USDT", BidPx: 100, BidQty: 2000, AskPx: 100, AskQty: 2000}})

	// 推动 watermark 触发主 episode gap 收口,从而允许 exhaustion 评估。
	ingest(Record{Kind: KindLiquidation, Envelope: mkEnvelope(base.Add(25 * time.Second)), Liquidation: &LiquidationRecord{RecordedAt: base.Add(25 * time.Second), Symbol: "ETH-USDT", PosSide: "short", Side: "buy", Qty: 1, BkPx: 100}})
	agg.Flush()

	if len(features) != 2 {
		t.Fatalf("expected relay+exhaustion (2 features), got %d", len(features))
	}
	sort.Slice(features, func(i, j int) bool { return string(features[i].Episode.Phase) < string(features[j].Episode.Phase) })
	exhaustion := features[0]
	relay := features[1]
	if relay.Episode.Phase != PhaseRelay || exhaustion.Episode.Phase != PhaseExhaustion {
		t.Fatalf("unexpected phase set: %+v", []PhaseKind{features[0].Episode.Phase, features[1].Episode.Phase})
	}
	if relay.Episode.RawEpisodeID != exhaustion.Episode.RawEpisodeID {
		t.Fatalf("relay/exhaustion must share RawEpisodeID, got %q vs %q", relay.Episode.RawEpisodeID, exhaustion.Episode.RawEpisodeID)
	}
	if relay.Episode.EpisodeID == exhaustion.Episode.EpisodeID {
		t.Fatalf("relay/exhaustion must use independent EpisodeID")
	}

	// relay as-of 在 t=2s, decision=t=3s(feature lag=1s)。
	wantRelayAsOf := base.Add(2 * time.Second)
	if !relay.Episode.AsOfTime.Equal(wantRelayAsOf) {
		t.Fatalf("relay AsOfTime = %v, want %v", relay.Episode.AsOfTime, wantRelayAsOf)
	}
	if !relay.Episode.DecisionTime.Equal(wantRelayAsOf.Add(1 * time.Second)) {
		t.Fatalf("relay DecisionTime = %v, want %v", relay.Episode.DecisionTime, wantRelayAsOf.Add(1*time.Second))
	}

	// 反向跨所不确认: bybit=short 与 dominant(long_squeezed)反向。
	if relay.CrossVenue.Confirmed {
		t.Fatalf("reverse xvenue side must not confirm relay cross-venue")
	}

	// 乱序输入不应触发 break 假设: t=1s/t=2s 仍需进入 as-of 价格路径。
	if len(relay.PricePath) != 2 {
		t.Fatalf("relay PricePath should include only t=1s/t=2s mids, got %+v", relay.PricePath)
	}
	if relay.PricePath[0].OffsetMs != 1000 || relay.PricePath[1].OffsetMs != 2000 {
		t.Fatalf("relay PricePath offsets = %+v, want [1000,2000]", relay.PricePath)
	}

	relayLabel := FutureLabel{}
	foundRelayLabel := false
	for _, l := range labels {
		if l.Phase == PhaseRelay && l.RawEpisodeID == relay.Episode.RawEpisodeID {
			relayLabel = l
			foundRelayLabel = true
			break
		}
	}
	if !foundRelayLabel {
		t.Fatalf("relay label not emitted")
	}
	// 未来隔离: decision=3s 之前/之内的数据(如 2.5s)不得进入 label。
	for _, p := range relayLabel.FuturePricePath {
		if p.OffsetMs <= 0 {
			t.Fatalf("future path leaked non-future point: %+v", p)
		}
	}
}
