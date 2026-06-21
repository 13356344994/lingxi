package app

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEquitySnapshotLines(t *testing.T, dir string, lines ...string) {
	t.Helper()
	cvDir := crossVenueRecordDir(dir)
	if err := os.MkdirAll(cvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cvDir, equitySnapshotsFileName)
	if err := os.WriteFile(path, []byte(stringsJoinLines(lines...)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestComputeRealEquityWaterMark_SingleSampleSpikeDoesNotRaisePeak(t *testing.T) {
	dir := t.TempDir()
	base := `{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":200}`
	gate := `{"ts":"2026-06-19T00:00:00Z","venue":"gate","cash_usdt":200}`
	writeEquitySnapshotLines(t, dir,
		base, gate,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":255}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"gate","cash_usdt":200}`,
	)

	current, peak, drawdown, quality := computeRealEquityWaterMarkWithQuality(dir)
	if peak != 400 {
		t.Fatalf("peak=%v want 400 (spike must not raise peak)", peak)
	}
	if current != 400 {
		t.Fatalf("current=%v want 400", current)
	}
	if drawdown != 0 {
		t.Fatalf("drawdown=%v want 0", drawdown)
	}
	if !quality.PeakConfirmed {
		t.Fatalf("expected confirmed peak, got %+v", quality)
	}
}

func TestComputeRealEquityWaterMark_IncompleteSampleExcluded(t *testing.T) {
	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:00:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":10}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"okx","cash_usdt":950}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"gate","cash_usdt":500}`,
	)

	current, peak, drawdown := computeRealEquityWaterMark(dir)
	if peak != 1500 || current != 1450 || drawdown != 50 {
		t.Fatalf("watermark current=%v peak=%v drawdown=%v, want 1450/1500/50", current, peak, drawdown)
	}
}

func TestComputeRealEquityWaterMark_ZeroVenueReadingExcluded(t *testing.T) {
	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:00:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"gate","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":200}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"gate","cash_usdt":0}`,
	)

	current, peak, drawdown, quality := computeRealEquityWaterMarkWithQuality(dir)
	if current != 400 || peak != 400 || drawdown != 0 {
		t.Fatalf("zero venue sample must be ignored: current=%v peak=%v drawdown=%v", current, peak, drawdown)
	}
	if quality.Passed {
		t.Fatalf("latest sample with gate=0 must fail quality, got %+v", quality)
	}
}

func TestComputeRealEquityWaterMark_WithdrawalStepReasonable(t *testing.T) {
	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":385.55}`,
	)

	current, peak, drawdown, quality := computeRealEquityWaterMarkWithQuality(dir)
	if peak != 435 {
		t.Fatalf("peak=%v want 435", peak)
	}
	if current != 385.55 {
		t.Fatalf("current=%v want 385.55", current)
	}
	if drawdown < 49 || drawdown > 50 {
		t.Fatalf("drawdown=%v want ~49.45", drawdown)
	}
	if !quality.Passed {
		t.Fatalf("withdrawal step should remain quality-passed, got %+v", quality)
	}
}

func TestEvaluateAccountAutoFlattenIntent_QualityNotPassed(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150

	got := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 200
		s.RealEquityTotalUSDT = 800
		s.RealEquityPeakUSDT = 1000
		s.RealEquityQualityPassed = false
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if got.ShouldFlatten {
		t.Fatalf("quality fail must not flatten, got %+v", got)
	}
}

func TestEvaluateAccountAutoFlattenIntent_FundFlowSuspect(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150

	got := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 49.45
		s.RealEquityTotalUSDT = 385.55
		s.RealEquityPeakUSDT = 435
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
		s.DailyFundFlowAdjustUSDT = -49.45
	}), cfg)
	if got.ShouldFlatten {
		t.Fatalf("fund-flow driven drawdown must not flatten, got %+v", got)
	}
}

func TestEvaluateAccountAutoFlattenIntent_UnconfirmedPeak(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150

	got := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 200
		s.RealEquityTotalUSDT = 800
		s.RealEquityPeakUSDT = 1000
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = false
	}), cfg)
	if got.ShouldFlatten {
		t.Fatalf("unconfirmed peak must not flatten, got %+v", got)
	}
}
