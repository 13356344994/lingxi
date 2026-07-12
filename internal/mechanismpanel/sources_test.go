package mechanismpanel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lingxi/internal/datatruth"
	contextdata "lingxi/internal/datatruth/context"
)

func fixedClock(t time.Time) clockFunc {
	return func() time.Time { return t }
}

func TestLiquidationSourceDecodesAndEnvelopesThreeClocks(t *testing.T) {
	proc := time.Date(2026, 7, 11, 0, 5, 0, 0, time.UTC)
	src, err := NewLiquidationSource(filepath.Join("testdata", "liquidation.jsonl"), 0, fixedClock(proc))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer src.Close()

	var got []Record
	for {
		rec, ok, err := src.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		got = append(got, rec)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 liquidation records, got %d", len(got))
	}
	first := got[0]
	if first.Liquidation == nil || first.Liquidation.Symbol != "BTC-USDT" || first.Liquidation.Qty != 10 {
		t.Fatalf("unexpected decoded payload: %+v", first.Liquidation)
	}
	wantEvent := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	if !first.Envelope.EventTime.Equal(wantEvent) {
		t.Errorf("event time = %v, want %v", first.Envelope.EventTime, wantEvent)
	}
	if !first.Envelope.ReceiveTime.Equal(wantEvent) {
		t.Errorf("receive time = %v, want %v", first.Envelope.ReceiveTime, wantEvent)
	}
	if !first.Envelope.ProcessTime.Equal(proc) {
		t.Errorf("process time = %v, want %v (injected clock)", first.Envelope.ProcessTime, proc)
	}
	if first.Envelope.Sequence != 1 {
		t.Errorf("sequence = %d, want 1 (line number)", first.Envelope.Sequence)
	}
	if src.LinesRead() != 3 {
		t.Errorf("LinesRead = %d, want 3", src.LinesRead())
	}
}

func TestLiquidationSourceUsesExchTsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "liq.jsonl")
	writeLines(t, path, []string{
		`{"recorded_at":"2026-07-11T00:00:05Z","symbol":"ETH-USDT","pos_side":"short","side":"buy","qty":1,"bk_px":3000,"exch_ts":"1783843200123"}`,
	})
	src, err := NewLiquidationSource(path, 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	want := time.UnixMilli(1783843200123).UTC()
	if !rec.Envelope.EventTime.Equal(want) {
		t.Errorf("event time = %v, want %v (from exch_ts, not recorded_at)", rec.Envelope.EventTime, want)
	}
}

func TestJSONLSourceSkipLinesForResume(t *testing.T) {
	src, err := NewLiquidationSource(filepath.Join("testdata", "liquidation.jsonl"), 2, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	if rec.Liquidation.Qty != 30 {
		t.Fatalf("expected to resume at 3rd line (qty=30), got qty=%v", rec.Liquidation.Qty)
	}
	if src.LinesRead() != 3 {
		t.Errorf("LinesRead = %d, want 3", src.LinesRead())
	}
	_, ok, err = src.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ok {
		t.Fatalf("expected EOF after resuming past all lines")
	}
}

func TestJSONLSourceRejectsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	writeLines(t, path, []string{`not json`})
	src, err := NewLiquidationSource(path, 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	_, _, err = src.Next()
	if err == nil {
		t.Fatalf("expected decode error for malformed line")
	}
}

func TestMarketMidsSourceDecodesMultiSymbolLine(t *testing.T) {
	src, err := NewMarketMidsSource(filepath.Join("testdata", "mids.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	if rec.Mids.Mids["BTC-USDT"] != 60010 {
		t.Fatalf("unexpected mid: %+v", rec.Mids.Mids)
	}
}

func TestXMetaOISourceDecodesVenueSymbolArray(t *testing.T) {
	src, err := NewXMetaOISource(filepath.Join("testdata", "xmeta.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	oi := rec.OI.Venues["bybit"]["BTC-USDT"]
	if len(oi) < 2 || oi[1] != 1000000 {
		t.Fatalf("unexpected oi array: %+v", oi)
	}
}

func TestDepthSourceDecodesTopOfBook(t *testing.T) {
	src, err := NewDepthSource(filepath.Join("testdata", "depth.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	if rec.Depth.Symbol != "BTC-USDT" || rec.Depth.BidPx != 59990 {
		t.Fatalf("unexpected depth: %+v", rec.Depth)
	}
}

func TestXVenueLiquidationSourceDecodes(t *testing.T) {
	src, err := NewXVenueLiquidationSource(filepath.Join("testdata", "xvenue.jsonl"), 0, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	rec, ok, err := src.Next()
	if err != nil || !ok {
		t.Fatalf("Next: ok=%v err=%v", ok, err)
	}
	if rec.XVenue.Venue != "bybit" || rec.XVenue.Symbol != "BTC-USDT" || rec.XVenue.Qty != 2 {
		t.Fatalf("unexpected xvenue record: %+v", rec.XVenue)
	}
}

func TestLoadInstrumentCatalogAndCtValAt(t *testing.T) {
	cat, err := LoadInstrumentCatalog(filepath.Join("testdata", "instrument_meta.json"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	at := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	cv, ok := CtValAt(cat, "BTC-USDT", at, at)
	if !ok || cv != 0.01 {
		t.Fatalf("ctVal = %v ok=%v, want 0.01/true", cv, ok)
	}
	_, ok = CtValAt(cat, "NOPE-USDT", at, at)
	if ok {
		t.Fatalf("expected not-found instrument to report ok=false")
	}
}

func TestLoadInstrumentCatalogRejectsCurrentSnapshotSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instrument_meta.json")
	if err := os.WriteFile(path, []byte(`{"fetched_at":"2026-07-11T00:00:00Z","instruments":{"BTC-USDT":{"ct_val":"0.01"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInstrumentCatalog(path); err == nil {
		t.Fatal("current-snapshot schema must not be accepted as a PIT instrument catalog")
	}
}

func TestMarketMidsSourceAcceptsCanonicalContextPartition(t *testing.T) {
	at := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	payload := MarketMidsRecord{RecordedAt: at, Mids: map[string]float64{"BTC-USDT": 100}}
	env, err := datatruth.NewEnvelope("okx", "fast_market_mids", "", 1, at, at, at, payload)
	if err != nil {
		t.Fatal(err)
	}
	record := contextdata.Record{
		SchemaVersion: contextdata.SchemaVersion,
		Kind:          contextdata.KindMids,
		Event:         env,
		Clocks: contextdata.ClockProvenance{
			EventTime: "recorded_at", ReceiveTime: "recorded_at", ProcessTime: "legacy_receive_proxy",
		},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mids.jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewMarketMidsSource(path, 0, fixedClock(at.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	got, ok, err := source.Next()
	if err != nil || !ok {
		t.Fatalf("canonical source read failed: ok=%v err=%v", ok, err)
	}
	if got.Mids.Mids["BTC-USDT"] != 100 || !got.Envelope.ProcessTime.Equal(at) {
		t.Fatalf("canonical record was not preserved: %+v", got)
	}
}

func TestLegacyBybitXVenueUsesSideRawAsTruth(t *testing.T) {
	at := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "xvenue.jsonl")
	raw := `{"recorded_at":"2026-07-11T00:00:00Z","venue":"bybit","symbol":"BTC-USDT","side_raw":"Sell","pos_side":"long","qty":1,"price":100}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewXVenueLiquidationSource(path, 0, fixedClock(at))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record, ok, err := source.Next()
	if err != nil || !ok {
		t.Fatalf("read failed: ok=%v err=%v", ok, err)
	}
	if record.XVenue.PosSide != "short" {
		t.Fatalf("Bybit side_raw=Sell must map to short liquidation, got %q", record.XVenue.PosSide)
	}
}

func TestCtValAtRejectsMalformedContractValue(t *testing.T) {
	at := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	catalog := datatruth.InstrumentCatalog{Versions: []datatruth.InstrumentVersion{{
		InstrumentID: "BTC-USDT", Venue: "okx", Symbol: "BTC-USDT-SWAP",
		ListedAt: at.Add(-time.Hour), KnownFrom: at.Add(-time.Hour), ContractValue: "not-a-number",
	}}}
	if _, ok := CtValAt(catalog, "BTC-USDT", at, at); ok {
		t.Fatal("malformed contract value must not silently fall back to 1")
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
