package fingerprint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCriticalCellJoinsComplementarySourceCells(t *testing.T) {
	fp := PersonalExecutionFingerprint{
		Generation:      Generation,
		FingerprintHash: "fixture",
		SourceStatus: map[string]SourceStatus{
			sourceMakerAttempts:    {Status: StatusAvailable, Decoded: 10},
			sourceExecutableShadow: {Status: StatusAvailable, Decoded: 10},
		},
		Cells: []ExecutionCell{
			{
				CellKey:      CellKey{Venue: "okx", Symbol: "BTC-USDT", Direction: "long", OrderType: "maker", NotionalBucket: "small"},
				FeeBPS:       QuantileMetric{P50: 1, P90: 2, N: 4, Status: StatusAvailable},
				EntrySlipBPS: QuantileMetric{P50: 0.1, P90: 0.4, N: 4, Status: StatusAvailable},
				ExitSlipBPS:  QuantileMetric{P50: 0.2, P90: 0.5, N: 4, Status: StatusAvailable},
				SampleCount:  4,
				Instrument:   KnownLinearInstrument("0.01", "1", "0.1", 100),
			},
			{
				CellKey:       CellKey{Venue: "okx", Symbol: "BTC-USDT", Direction: "long", OrderType: "maker", NotionalBucket: "unknown"},
				MakerFillRate: RateMetric{Rate: 0.6, N: 10, Status: StatusAvailable},
				MakerWaitMS:   QuantileMetric{P50: 100, P90: 300, N: 10, Status: StatusAvailable},
				LatencyMS:     QuantileMetric{P50: 100, P90: 300, N: 10, Status: StatusAvailable},
				SampleCount:   10,
				Instrument:    KnownLinearInstrument("0.01", "1", "0.1", 100),
			},
		},
	}
	cell, ok := ResolveCriticalCell(fp, "BTC-USDT-SWAP", "long")
	if !ok {
		t.Fatal("expected resolved cell")
	}
	if FingerprintCriticalUnknown(fp, &cell) {
		t.Fatalf("complementary evidence should resolve critical-known: %+v", cell)
	}
	if cell.SampleCount != 4 {
		t.Fatalf("effective sample count=%d want 4", cell.SampleCount)
	}
}

func TestCoverageReportExplainsMissingExitSlip(t *testing.T) {
	fp := PersonalExecutionFingerprint{
		Generation:      Generation,
		FingerprintHash: "fixture",
		SourceStatus: map[string]SourceStatus{
			sourceMakerAttempts:    {Status: StatusAvailable, Decoded: 10},
			sourceExecutableShadow: {Status: StatusAvailable, Decoded: 10},
		},
		Cells: []ExecutionCell{{
			CellKey:       CellKey{Venue: "okx", Symbol: "ETH-USDT", Direction: "long"},
			FeeBPS:        QuantileMetric{N: 3, Status: StatusAvailable},
			EntrySlipBPS:  QuantileMetric{N: 3, Status: StatusAvailable},
			LatencyMS:     QuantileMetric{N: 3, Status: StatusAvailable},
			MakerFillRate: RateMetric{N: 3, Status: StatusAvailable},
			SampleCount:   3,
			Instrument:    KnownLinearInstrument("0.01", "1", "0.1", 100),
		}},
	}
	report, err := BuildCoverageReport(fp)
	if err != nil {
		t.Fatal(err)
	}
	if report.CriticalKnownCount != 0 {
		t.Fatalf("critical known=%d want 0", report.CriticalKnownCount)
	}
	if report.CriticalUnknownByReason["EXIT_SLIP_UNKNOWN_OR_SPARSE"] == 0 {
		t.Fatalf("missing exit slip reason absent: %+v", report.CriticalUnknownByReason)
	}
}

func TestMissingSettlementMetricsRemainUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settlements.jsonl")
	line := `{"time":"2026-07-01T00:00:00Z","symbol":"BTC-USDT","side":"long","notional":30}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := ingestContext{store: newStore(), defaultVenue: "okx"}
	if _, err := ingestSettlements(path, 10, 0, ctx); err != nil {
		t.Fatal(err)
	}
	cells := ctx.store.sortedCells()
	if len(cells) != 1 {
		t.Fatalf("cells=%d want 1", len(cells))
	}
	if cells[0].FeeBPS.Status != StatusUnknown ||
		cells[0].EntrySlipBPS.Status != StatusUnknown ||
		cells[0].ExitSlipBPS.Status != StatusUnknown {
		t.Fatalf("missing metrics must remain UNKNOWN: %+v", cells[0])
	}
}

func TestCompletedCheckpointReturnsFrozenArtifacts(t *testing.T) {
	outDir := t.TempDir()
	cpPath := filepath.Join(t.TempDir(), "checkpoint.json")
	cfg := RunConfig{
		SchemaVersion: ConfigSchema,
		Generation:    Generation,
		RecordRoot:    t.TempDir(),
		OutputDir:     outDir,
		Sources:       testSources(t),
		MaxLines:      100,
	}
	runner := Runner{Config: cfg, CheckpointPath: cpPath}
	first, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint.FingerprintHash != second.Fingerprint.FingerprintHash {
		t.Fatalf("completed rerun changed fingerprint: %s != %s",
			first.Fingerprint.FingerprintHash, second.Fingerprint.FingerprintHash)
	}
	if first.Coverage.ReportHash != second.Coverage.ReportHash {
		t.Fatalf("completed rerun changed coverage: %s != %s",
			first.Coverage.ReportHash, second.Coverage.ReportHash)
	}
}
