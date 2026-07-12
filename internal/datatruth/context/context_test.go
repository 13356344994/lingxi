package contextdata

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

func TestSnapshotPITNeverBackfillsBeforeKnownFrom(t *testing.T) {
	raw := []byte(`{
		"fetched_at":"2026-07-11T12:00:00Z",
		"instruments":{"BTC-USDT":{"list_time_ms":1573557408000,"ct_val":"0.01","lot":"0.01","tick":"0.1","state":"live"}}
	}`)
	catalog, err := ConvertInstrumentSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 7, 11, 11, 59, 59, 0, time.UTC)
	if _, ok := catalog.InstrumentAt("BTC-USDT", before, before); ok {
		t.Fatal("current snapshot backfilled an event before fetched_at")
	}
	after := time.Date(2026, 7, 11, 12, 0, 1, 0, time.UTC)
	if got, ok := catalog.InstrumentAt("BTC-USDT", after, after); !ok || got.ContractValue != "0.01" {
		t.Fatalf("snapshot should apply after fetched_at: got=%+v ok=%v", got, ok)
	}
	merged, err := MergeInstrumentCatalog(catalog, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Versions) != 1 {
		t.Fatalf("append-only merge must dedupe exact versions, got %d", len(merged.Versions))
	}
}

func TestDecodeLegacyAdaptersAndBybitSideRepair(t *testing.T) {
	tests := []struct {
		kind Kind
		raw  string
	}{
		{KindMids, `{"recorded_at":"2026-07-11T00:00:00Z","mids":{"BTC-USDT":100}}`},
		{KindOI, `{"recorded_at":"2026-07-11T00:00:00Z","venues":{"okx":{"BTC-USDT":[0.001,1000,0]}}}`},
		{KindXVenue, `{"recorded_at":"2026-07-11T00:00:01Z","venue":"bybit","symbol":"BTC-USDT","side_raw":"Buy","pos_side":"short","qty":2,"price":100,"exch_ts":1783728000000}`},
		{KindDepth, `{"recorded_at":"2026-07-11T00:00:00Z","symbol":"BTC-USDT","bid":99,"ask":101,"top_bid_qty":2,"top_ask_qty":3,"depth_notional_10bps":500}`},
	}
	for i, tc := range tests {
		record, missing, err := DecodeLegacy(tc.kind, []byte(tc.raw), int64(i+1))
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if missing {
			t.Fatalf("%s unexpectedly missing required fields", tc.kind)
		}
		if record.Event.EventTime.IsZero() || record.Event.ReceiveTime.IsZero() || record.Event.ProcessTime.IsZero() {
			t.Fatalf("%s missing three clocks: %+v", tc.kind, record.Event)
		}
		if tc.kind == KindXVenue {
			var payload XVenuePayload
			if err := json.Unmarshal(record.Event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.PosSide != "long" {
				t.Fatalf("Bybit side_raw=Buy must repair pos_side to long, got %q", payload.PosSide)
			}
		}
	}
}

func TestMaterializerCheckpointResumeIsIdempotentAndBounded(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mids.jsonl")
	lines := []string{`{"recorded_at":"2026-07-10T23:59:59Z","mids":{"BTC-USDT":99}}`}
	for i := 0; i < 20; i++ {
		at := time.Date(2026, 7, 11, 0, 0, i, 0, time.UTC)
		lines = append(lines, `{"recorded_at":"`+at.Format(time.RFC3339)+`","mids":{"BTC-USDT":100}}`)
	}
	lines = append(lines, lines[len(lines)-1])
	lines = append(lines, `{"recorded_at":"2026-07-12T00:00:00Z","mids":{"BTC-USDT":102}}`)
	writeLines(t, input, lines)
	checkpointPath := filepath.Join(dir, "checkpoint.json")
	cfg := Config{
		Kind: KindMids, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", CheckpointPath: checkpointPath, MaxLines: 7,
		ProgressEvery: 2, MaxDedupeEntries: 4,
	}
	first, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReachedEOF || first.Manifest.Completed {
		t.Fatal("limited first run must remain resumable")
	}
	cfg.MaxLines = 100
	second, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ReachedEOF || !second.Manifest.Completed {
		t.Fatal("resume should reach EOF")
	}
	if second.Manifest.RowsEmitted != 20 || second.Manifest.Quality.Duplicate != 1 {
		t.Fatalf("exact duplicate should be removed once, manifest=%+v", second.Manifest)
	}
	var cp checkpoint
	if err := readJSON(checkpointPath, &cp); err != nil {
		t.Fatal(err)
	}
	if len(cp.StreamState.SeenSequences) > 4 || len(cp.StreamState.SeenHashes) > 4 {
		t.Fatalf("dedupe checkpoint is unbounded: %+v", cp.StreamState)
	}
	third, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if third.Manifest.OutputHash != second.Manifest.OutputHash || third.Manifest.RowsEmitted != second.Manifest.RowsEmitted {
		t.Fatal("completed rerun changed deterministic output")
	}
}

func TestMaterializerRejectsChangedInputOnResume(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "oi.jsonl")
	writeLines(t, input, []string{
		`{"recorded_at":"2026-07-11T00:00:00Z","venues":{"okx":{"BTC-USDT":[0,100,0]}}}`,
		`{"recorded_at":"2026-07-11T00:01:00Z","venues":{"okx":{"BTC-USDT":[0,101,0]}}}`,
	})
	cfg := Config{
		Kind: KindOI, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", CheckpointPath: filepath.Join(dir, "checkpoint.json"), MaxLines: 1,
	}
	if _, err := Materialize(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = ' '
	if err := os.WriteFile(input, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.MaxLines = 10
	if _, err := Materialize(context.Background(), cfg); err == nil ||
		(!strings.Contains(err.Error(), ReasonSourceIdentity) && !strings.Contains(err.Error(), ReasonInputConfigChanged)) {
		t.Fatalf("changed input must fail closed, got %v", err)
	}
}

func TestMaterializerRejectsChangedConsumedRangePastPrefix(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mids.jsonl")
	lines := make([]string, 0, 2000)
	for i := 0; i < 2000; i++ {
		at := time.Date(2026, 7, 11, 0, 0, 0, i, time.UTC)
		lines = append(lines, `{"recorded_at":"`+at.Format(time.RFC3339Nano)+`","mids":{"BTC-USDT":100.000000}}`)
	}
	writeLines(t, input, lines)
	cfg := Config{
		Kind: KindMids, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", CheckpointPath: filepath.Join(dir, "checkpoint.json"), MaxLines: 1500,
	}
	if _, err := Materialize(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	index := bytes.Index(raw[70*1024:], []byte("100.000000"))
	if index < 0 {
		t.Fatal("fixture did not extend beyond identity prefix")
	}
	index += 70 * 1024
	raw[index] = '9'
	if err := os.WriteFile(input, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.MaxLines = 2000
	if _, err := Materialize(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), ReasonSourceIdentity) {
		t.Fatalf("changed consumed range beyond 64KiB must fail closed, got %v", err)
	}
}

func TestMaterializerDoesNotConsumePartialTailLine(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mids.jsonl")
	line := `{"recorded_at":"2026-07-11T00:00:00Z","mids":{"BTC-USDT":100}}`
	if err := os.WriteFile(input, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Kind: KindMids, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", CheckpointPath: filepath.Join(dir, "checkpoint.json"),
	}
	first, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReachedEOF || first.Manifest.RowsScanned != 0 {
		t.Fatalf("unterminated tail must remain unconsumed: %+v", first)
	}
	file, err := os.OpenFile(input, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !second.ReachedEOF || second.Manifest.RowsEmitted != 1 {
		t.Fatalf("completed line should be consumed exactly once: %+v", second)
	}
}

func TestMaterializerRecoversCrashAfterFinalRename(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "mids.jsonl")
	writeLines(t, input, []string{`{"recorded_at":"2026-07-11T00:00:00Z","mids":{"BTC-USDT":100}}`})
	cfg := Config{
		Kind: KindMids, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", CheckpointPath: filepath.Join(dir, "checkpoint.json"),
	}
	first, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var cp checkpoint
	if err := readJSON(cfg.CheckpointPath, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Completed = false
	cp.FinalizeReady = true
	cp.ManifestPath = ""
	if err := saveJSONAtomic(cfg.CheckpointPath, cp); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "out", "2026-07-11", "mids.manifest.json")
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	second, err := Materialize(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.OutputHash != first.Manifest.OutputHash || second.Manifest.RowsEmitted != 1 {
		t.Fatalf("rename-window recovery corrupted output: first=%+v second=%+v", first, second)
	}
	raw, err := os.ReadFile(second.Manifest.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		t.Fatal("recovered final contains zero-filled corruption")
	}
}

func TestPerInstrumentEventOrderingAvoidsFalseGlobalOutOfOrder(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	makeRecord := func(sequence int64, instrument string, eventTime time.Time) string {
		payload := XVenuePayload{
			RecordedAt: base.Add(10 * time.Second), Venue: "bybit", Symbol: instrument,
			SideRaw: "Buy", PosSide: "long", Qty: 1, Price: 100, ExchTS: eventTime.UnixMilli(),
		}
		env, err := datatruth.NewEnvelope("bybit", "fast_xvenue_liq_events", instrument, sequence, eventTime, payload.RecordedAt, payload.RecordedAt, payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(Record{SchemaVersion: SchemaVersion, Kind: KindXVenue, Event: env})
		return string(raw)
	}
	input := filepath.Join(dir, "xvenue.jsonl")
	writeLines(t, input, []string{
		makeRecord(1, "BTC-USDT", base.Add(5*time.Second)),
		makeRecord(2, "ETH-USDT", base.Add(4*time.Second)),
	})
	result, err := Materialize(context.Background(), Config{
		Kind: KindXVenue, InputPath: input, OutputDir: filepath.Join(dir, "out"), Date: "2026-07-11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Quality.OutOfOrder != 0 {
		t.Fatalf("cross-instrument event-time interleaving is not source-internal disorder: %+v", result.Manifest.Quality)
	}
}

func TestMaterializerCountsLateOutOfOrderAndCorrection(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	makeRecord := func(sequence int64, eventTime, receiveTime time.Time, revision string) string {
		payload := MidsPayload{RecordedAt: receiveTime, Mids: map[string]float64{"BTC-USDT": 100}}
		env, err := datatruth.NewEnvelope("okx", "fast_market_mids", "", sequence, eventTime, receiveTime, receiveTime, payload)
		if err != nil {
			t.Fatal(err)
		}
		env.RevisionOf = revision
		record := Record{SchemaVersion: SchemaVersion, Kind: KindMids, Event: env}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	input := filepath.Join(dir, "canonical.jsonl")
	writeLines(t, input, []string{
		makeRecord(2, base.Add(2*time.Second), base.Add(2*time.Second), ""),
		makeRecord(1, base.Add(time.Second), base.Add(time.Second), ""),
		makeRecord(3, base.Add(3*time.Second), base.Add(2*time.Minute), "event-2"),
	})
	result, err := Materialize(context.Background(), Config{
		Kind: KindMids, InputPath: input, OutputDir: filepath.Join(dir, "out"),
		Date: "2026-07-11", LateAfter: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := result.Manifest.Quality
	if q.OutOfOrder == 0 || q.Late == 0 || q.Correction == 0 {
		t.Fatalf("quality counters missing: %+v", q)
	}
	if result.Manifest.Verdict != VerdictFail {
		t.Fatalf("bad-quality partition must fail closed: %+v", result.Manifest)
	}
}

func TestValidateCompleteDayAndMissingDepth(t *testing.T) {
	root := t.TempDir()
	date := "2026-07-11"
	start := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	end := start.Add(24*time.Hour - 5*time.Minute)

	instrument := filepath.Join(root, "instrument.json")
	writeFile(t, instrument, `{"fetched_at":"2026-07-10T23:00:00Z","instruments":{"BTC-USDT":{"list_time_ms":1573557408000,"ct_val":"0.01","lot":"0.01","tick":"0.1","state":"live"}}}`)
	universe := filepath.Join(root, "universe.json")
	writeFile(t, universe, `{"at":"2026-07-10T23:30:00Z","shortlist":[{"symbol":"BTC-USDT"}]}`)
	mids := filepath.Join(root, "mids.jsonl")
	writeLines(t, mids, []string{
		`{"recorded_at":"` + start.Add(5*time.Minute).Format(time.RFC3339) + `","mids":{"BTC-USDT":100}}`,
		`{"recorded_at":"` + end.Format(time.RFC3339) + `","mids":{"BTC-USDT":101}}`,
	})
	oi := filepath.Join(root, "oi.jsonl")
	writeLines(t, oi, []string{
		`{"recorded_at":"` + end.Format(time.RFC3339) + `","venues":{"okx":{"BTC-USDT":[0,1000,0]}}}`,
	})
	xvenue := filepath.Join(root, "xvenue.jsonl")
	writeLines(t, xvenue, []string{
		`{"recorded_at":"2026-07-11T12:00:00Z","venue":"bybit","symbol":"BTC-USDT","side_raw":"Buy","qty":1,"price":100,"exch_ts":1783771200000}`,
	})
	depth := filepath.Join(root, "depth.jsonl")
	writeLines(t, depth, []string{
		`{"recorded_at":"` + start.Add(5*time.Minute).Format(time.RFC3339) + `","symbol":"BTC-USDT","bid":99,"ask":101,"top_bid_qty":2,"top_ask_qty":2,"depth_notional_10bps":400}`,
		`{"recorded_at":"` + end.Format(time.RFC3339) + `","symbol":"BTC-USDT","bid":100,"ask":102,"top_bid_qty":2,"top_ask_qty":2,"depth_notional_10bps":404}`,
	})

	inputs := map[Kind]string{
		KindInstrument: instrument, KindUniverse: universe, KindMids: mids,
		KindOI: oi, KindXVenue: xvenue, KindDepth: depth,
	}
	for _, kind := range RequiredKinds {
		if _, err := Materialize(context.Background(), Config{
			Kind: kind, InputPath: inputs[kind], OutputDir: root, Date: date,
		}); err != nil {
			t.Fatalf("materialize %s: %v", kind, err)
		}
	}
	dayDir := filepath.Join(root, date)
	verdict, err := ValidateDay(ValidateConfig{DayDir: dayDir, Date: date})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Verdict != VerdictPass {
		t.Fatalf("complete fixture should pass: %+v", verdict)
	}
	if err := os.Remove(filepath.Join(dayDir, "depth.manifest.json")); err != nil {
		t.Fatal(err)
	}
	verdict, err = ValidateDay(ValidateConfig{DayDir: dayDir, Date: date})
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Verdict != VerdictFail || !contains(verdict.Reasons, ReasonDepthMissing) {
		t.Fatalf("missing depth must fail closed: %+v", verdict)
	}
}

func TestManifestRejectsInvalidSchema(t *testing.T) {
	m := PartitionManifest{SchemaVersion: "wrong"}
	if err := m.Validate(); err == nil {
		t.Fatal("invalid schema must be rejected")
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
