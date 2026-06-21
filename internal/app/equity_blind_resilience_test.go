package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
)

func TestComputeRealEquityWaterMark_BlindGapDoesNotIncreaseDrawdown(t *testing.T) {
	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:30:00Z","venue":"okx","cash_usdt":385}`,
		`{"ts":"2026-06-19T00:33:00Z","venue":"okx","cash_usdt":385}`,
		`{"ts":"2026-06-19T00:36:00Z","venue":"okx","cash_usdt":385}`,
	)

	current, peak, drawdown := computeRealEquityWaterMark(dir)
	if current != 385 {
		t.Fatalf("current=%v want 385", current)
	}
	if drawdown != 0 {
		t.Fatalf("blind gap must re-anchor drawdown=0, got peak=%v drawdown=%v", peak, drawdown)
	}
}

func TestComputeRealEquityWaterMark_RealDrawdownStillDetected(t *testing.T) {
	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":425}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"okx","cash_usdt":415}`,
		`{"ts":"2026-06-19T00:15:00Z","venue":"okx","cash_usdt":410}`,
	)

	current, peak, drawdown := computeRealEquityWaterMark(dir)
	if peak != 435 {
		t.Fatalf("peak=%v want 435", peak)
	}
	if current != 410 {
		t.Fatalf("current=%v want 410", current)
	}
	if drawdown < 24 || drawdown > 26 {
		t.Fatalf("drawdown=%v want ~25", drawdown)
	}
}

func TestSampleEquityOnce_AllVenueFailureMarksConnectivityBlind(t *testing.T) {
	dir := t.TempDir()
	fail := &equityTestVenue{name: "okx", balancesErr: errors.New("403 forbidden")}
	exec := &CrossVenueExecutor{
		recordDir: dir,
		venues:    map[string]venue.Venue{"okx": fail, "gate": fail},
		logger:    log.New(os.Stderr, "[test] ", 0),
		nowFn:     func() time.Time { return time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC) },
	}

	exec.sampleEquityOnce(context.Background())

	snapPath := filepath.Join(crossVenueRecordDir(dir), equitySnapshotsFileName)
	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Fatal("all-fail must not write equity snapshots")
	}

	health, ok := loadEquitySamplerHealth(dir)
	if !ok {
		t.Fatal("health file missing")
	}
	if !health.ConnectivityBlind {
		t.Fatalf("connectivity_blind=false, want true: %+v", health)
	}
	if health.ConsecutiveAllFail != 1 {
		t.Fatalf("consecutive_all_fail=%d want 1", health.ConsecutiveAllFail)
	}
	if health.ExpectedVenueCount != 2 {
		t.Fatalf("expected_venue_count=%d want 2", health.ExpectedVenueCount)
	}
	if health.LastSuccessTS != "" {
		t.Fatalf("last_success_ts should be empty on all-fail, got %q", health.LastSuccessTS)
	}
}

func TestSampleEquityOnce_PartialVenueFailureNotBlind(t *testing.T) {
	dir := t.TempDir()
	okVenue := &equityTestVenue{
		name: "okx",
		balances: venue.Balances{ByAsset: map[string]venue.Balance{
			"USDT": {Asset: "USDT", Free: 100},
		}},
	}
	fail := &equityTestVenue{name: "gate", balancesErr: errors.New("403 forbidden")}
	now := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	exec := &CrossVenueExecutor{
		recordDir: dir,
		venues:    map[string]venue.Venue{"okx": okVenue, "gate": fail},
		logger:    log.New(os.Stderr, "[test] ", 0),
		nowFn:     func() time.Time { return now },
	}

	exec.sampleEquityOnce(context.Background())

	health, ok := loadEquitySamplerHealth(dir)
	if !ok {
		t.Fatal("health file missing")
	}
	if health.ConnectivityBlind {
		t.Fatalf("partial fail must not mark blind: %+v", health)
	}
	if health.ConsecutiveAllFail != 0 {
		t.Fatalf("consecutive_all_fail=%d want 0", health.ConsecutiveAllFail)
	}
	if health.LastSuccessVenueCount != 1 {
		t.Fatalf("last_success_venue_count=%d want 1", health.LastSuccessVenueCount)
	}

	snapPath := filepath.Join(crossVenueRecordDir(dir), equitySnapshotsFileName)
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("partial success should write snapshots: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected snapshot lines")
	}
}

func TestSampleEquityOnce_ConsecutiveAllFailIncrements(t *testing.T) {
	dir := t.TempDir()
	fail := &equityTestVenue{name: "okx", balancesErr: errors.New("down")}
	exec := &CrossVenueExecutor{
		recordDir: dir,
		venues:    map[string]venue.Venue{"okx": fail},
		logger:    log.New(os.Stderr, "[test] ", 0),
		nowFn:     func() time.Time { return time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC) },
	}
	exec.sampleEquityOnce(context.Background())
	exec.sampleEquityOnce(context.Background())

	health, _ := loadEquitySamplerHealth(dir)
	if health.ConsecutiveAllFail != 2 {
		t.Fatalf("consecutive_all_fail=%d want 2", health.ConsecutiveAllFail)
	}
}

func TestAccountRiskEquityStatePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	st := accountRiskEquityState{
		DayStartCST:             beijingCSTDayStart(now),
		LastTrustedTotalUSDT:    435,
		LastTrustedByVenue:      map[string]float64{"okx": 435},
		HasLastTrusted:          true,
		LastTrustedAt:           now.Add(-3 * time.Minute),
		DailyFundFlowAdjustUSDT: 0,
		LastPositionCount:       0,
	}
	if err := saveAccountRiskEquityState(dir, &st, now); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultEngineConfig()
	cfg.AlertingConfig.Enabled = false
	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	if !w.equityState.HasLastTrusted || w.equityState.LastTrustedTotalUSDT != 435 {
		t.Fatalf("restored state=%+v", w.equityState)
	}

	w.nowFn = func() time.Time { return now }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 435, 435, 0 }
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		return realEquityQualityResult{Passed: true, PeakConfirmed: true}
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(432, -3, map[string]float64{"okx": 432})
	}
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error { return nil }

	w.tickOnce()
	if !w.equityState.HasLastTrusted || w.equityState.LastTrustedTotalUSDT != 432 {
		t.Fatalf("tick-to-tick continuity broken: %+v", w.equityState)
	}
}

func TestAccountRiskWatcherBlindGapNoDrawdownHalt(t *testing.T) {
	var haltCount atomic.Int32
	var flattenCount atomic.Int32

	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:30:00Z","venue":"okx","cash_usdt":385}`,
		`{"ts":"2026-06-19T00:33:00Z","venue":"okx","cash_usdt":385}`,
		`{"ts":"2026-06-19T00:36:00Z","venue":"okx","cash_usdt":385}`,
	)

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = true
	cfg.RiskConfig.AutoFlattenAccountDrawdownUSDT = 40
	cfg.AlertingConfig.Enabled = false

	now := time.Date(2026, 6, 19, 0, 40, 0, 0, time.UTC)
	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return now }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = computeRealEquityWaterMark
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		_, _, _, q := computeRealEquityWaterMarkWithQuality(recordDir)
		return q
	}
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 435
	w.equityState.LastTrustedByVenue = map[string]float64{"okx": 435}
	w.equityState.LastTrustedAt = now.Add(-25 * time.Minute)
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(385, -50, map[string]float64{"okx": 385})
	}
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 || flattenCount.Load() != 0 {
		t.Fatalf("blind gap must not halt/flatten, halt=%d flatten=%d", haltCount.Load(), flattenCount.Load())
	}
	if w.equityState.DailyFundFlowAdjustUSDT != 0 {
		t.Fatalf("fund flow adjust=%v want 0 after re-anchor", w.equityState.DailyFundFlowAdjustUSDT)
	}
	if w.equityState.LastTrustedTotalUSDT != 385 {
		t.Fatalf("re-anchor total=%v want 385", w.equityState.LastTrustedTotalUSDT)
	}
}

func TestAccountRiskWatcherRealDrawdownStillHalts(t *testing.T) {
	var haltCount atomic.Int32

	dir := t.TempDir()
	writeEquitySnapshotLines(t, dir,
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":435}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":410}`,
		`{"ts":"2026-06-19T00:12:00Z","venue":"okx","cash_usdt":410}`,
		`{"ts":"2026-06-19T00:15:00Z","venue":"okx","cash_usdt":410}`,
	)

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 20
	cfg.AlertingConfig.Enabled = false

	now := time.Date(2026, 6, 19, 0, 16, 0, 0, time.UTC)
	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return now }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary {
		return accountRiskPositionSummary{TotalCount: 1, OpenNotional: 50}
	}
	w.realEquityFn = computeRealEquityWaterMark
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		_, _, _, q := computeRealEquityWaterMarkWithQuality(recordDir)
		return q
	}
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 425
	w.equityState.LastTrustedAt = now.Add(-3 * time.Minute)
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(410, -25, map[string]float64{"okx": 410})
	}
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 1 {
		t.Fatalf("real drawdown must halt, got halt=%d", haltCount.Load())
	}
}

func TestAccountRiskWatcherCrossCSTDayUsesLastTrustedReference(t *testing.T) {
	dir := t.TempDir()
	prevDay := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	newDay := time.Date(2026, 6, 21, 1, 0, 0, 0, time.UTC)

	st := accountRiskEquityState{
		DayStartCST:          beijingCSTDayStart(prevDay),
		LastTrustedTotalUSDT: 435,
		LastTrustedByVenue:   map[string]float64{"okx": 435},
		HasLastTrusted:       true,
		LastTrustedAt:        prevDay,
	}
	if err := saveAccountRiskEquityState(dir, &st, prevDay); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 40
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return newDay }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 430, 435, 5 }
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		return realEquityQualityResult{Passed: true, PeakConfirmed: true}
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(430, -5, map[string]float64{"okx": 430})
	}
	w.notifyFn = func(title, detail string) {}
	var haltCount atomic.Int32
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 {
		t.Fatalf("small day-boundary move must not halt, halt=%d adjust=%v",
			haltCount.Load(), w.equityState.DailyFundFlowAdjustUSDT)
	}
	if w.equityState.DailyFundFlowAdjustUSDT != 0 {
		t.Fatalf("day boundary must not mis-detect fund flow, adjust=%v", w.equityState.DailyFundFlowAdjustUSDT)
	}
}

func TestAccountRiskWatcherConnectivityBlindSkipsHalt(t *testing.T) {
	dir := t.TempDir()
	if err := recordEquitySamplerAttempt(dir, time.Now().UTC().Format(time.RFC3339), 2, 0, false); err != nil {
		t.Fatal(err)
	}

	var haltCount atomic.Int32
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 385, 435, 50 }
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 435
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(385, -50, map[string]float64{"okx": 385})
	}
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 {
		t.Fatalf("connectivity blind must skip halt, got %d", haltCount.Load())
	}

	path := filepath.Join(dir, accountRiskWatchFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedAccountRiskWatch
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Snapshot.EquityDataTrusted {
		t.Fatalf("expected untrusted snapshot during blind, got %+v", persisted.Snapshot)
	}
}

func TestSaveAccountRiskEquityStateAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	st := accountRiskEquityState{HasLastTrusted: true, LastTrustedTotalUSDT: 100}
	if err := saveAccountRiskEquityState(dir, &st, now); err != nil {
		t.Fatal(err)
	}
	path := accountRiskEquityStatePath(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p persistedAccountRiskEquityState
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != accountRiskEquityStateSchemaVersion || !p.HasLastTrusted || p.LastTrustedTotalUSDT != 100 {
		t.Fatalf("unexpected persisted payload: %+v", p)
	}
}
