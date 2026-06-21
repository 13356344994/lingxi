package app

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testAccountRiskSnapshotForEval(delta float64, extra func(*accountRiskSnapshot)) accountRiskSnapshot {
	snap := accountRiskSnapshot{
		DailyEquityDeltaUSDT:        delta,
		DailyEquityDeltaForRiskUSDT: delta,
		EquityDataTrusted:           true,
	}
	if extra != nil {
		extra(&snap)
	}
	return snap
}

func testAccountEquityObservation(total, rawDelta float64, venues map[string]float64) accountEquityObservation {
	return accountEquityObservation{
		TotalUSDT:     total,
		ByVenue:       venues,
		VenueDeltas:   copyVenueEquityMap(venues),
		RawDailyDelta: rawDelta,
		ReadOK:        true,
	}
}

func TestEvaluateAccountRiskThresholds_BelowThreshold(t *testing.T) {
	snap := testAccountRiskSnapshotForEval(5, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 3
		s.DailyRealizedPnLUSDT = 4
		s.OpenPositionCount = 1
		s.OpenNotionalUSDT = 50
	})
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 25
	cfg.MaxAccountDrawdownUSDT = 60

	got := evaluateAccountRiskThresholds(snap, cfg)
	if got.AnyBreached || got.DailyLossBreached || got.DrawdownBreached {
		t.Fatalf("expected no breach, got %+v", got)
	}
}

func TestEvaluateAccountRiskThresholds_DailyLossBreached(t *testing.T) {
	snap := testAccountRiskSnapshotForEval(-30, func(s *accountRiskSnapshot) {
		s.DailyRealizedPnLUSDT = -12
		s.OpenPositionCount = 2
		s.OpenNotionalUSDT = 120
	})
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 25
	cfg.MaxAccountDrawdownUSDT = 60

	got := evaluateAccountRiskThresholds(snap, cfg)
	if !got.DailyLossBreached || got.DrawdownBreached || !got.AnyBreached {
		t.Fatalf("expected daily loss breach only, got %+v", got)
	}
	intents := accountRiskAlertIntents(got)
	if len(intents) != 1 || intents[0].Title == "" {
		t.Fatalf("expected one alert intent, got %+v", intents)
	}
}

func TestEvaluateAccountRiskThresholds_DrawdownBreached(t *testing.T) {
	snap := testAccountRiskSnapshotForEval(-1, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 65
		s.RealizedPnLPeakUSDT = 100
		s.CumulativeRealizedPnL = 35
	})
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 25
	cfg.MaxAccountDrawdownUSDT = 60

	got := evaluateAccountRiskThresholds(snap, cfg)
	if got.DailyLossBreached || !got.DrawdownBreached || !got.AnyBreached {
		t.Fatalf("expected drawdown breach only, got %+v", got)
	}
}

func TestEvaluateAccountRiskThresholds_ZeroLimitsDisabled(t *testing.T) {
	snap := testAccountRiskSnapshotForEval(-100, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 200
	})
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 0
	cfg.MaxAccountDrawdownUSDT = 0

	got := evaluateAccountRiskThresholds(snap, cfg)
	if got.AnyBreached {
		t.Fatalf("zero limits should disable checks, got %+v", got)
	}
}

func TestEvaluateAccountHardHaltIntent_DailyLossBoundary(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 25
	cfg.MaxDailyLossPct = 0
	cfg.MaxAccountDrawdownUSDT = 0
	cfg.MaxStrategyCumulativeLossUSDT = 0
	cfg.MaxAccountOpenNotionalUSDT = 0
	cfg.MaxGrossLeverage = 0

	below := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(-24.99, nil), cfg)
	if below.ShouldHalt {
		t.Fatalf("expected no halt below threshold, got %+v", below)
	}
	at := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(-25, nil), cfg)
	if !at.ShouldHalt || !at.DailyLossBreached {
		t.Fatalf("expected halt at boundary, got %+v", at)
	}
}

func TestEvaluateAccountHardHaltIntent_SettlementDrawdownBoundary(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 0
	cfg.MaxAccountDrawdownUSDT = 60
	cfg.MaxAccountDrawdownPct = 0
	cfg.MaxStrategyCumulativeLossUSDT = 0
	cfg.MaxAccountOpenNotionalUSDT = 0
	cfg.MaxGrossLeverage = 0

	below := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 59.99
	}), cfg)
	if below.ShouldHalt {
		t.Fatalf("expected no halt below drawdown threshold, got %+v", below)
	}
	at := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 60
	}), cfg)
	if !at.ShouldHalt || !at.SettlementDrawdownHit {
		t.Fatalf("expected halt at drawdown boundary, got %+v", at)
	}
}

func TestEvaluateAccountHardHaltIntent_StrategyLossBoundary(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 0
	cfg.MaxAccountDrawdownUSDT = 0
	cfg.MaxStrategyCumulativeLossUSDT = 35
	cfg.MaxStrategyCumulativeLossPct = 0
	cfg.MaxAccountOpenNotionalUSDT = 0
	cfg.MaxGrossLeverage = 0

	below := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.StrategyNetPnL = map[string]float64{"cross_venue_spot_perp": -34.99}
	}), cfg)
	if below.ShouldHalt {
		t.Fatalf("expected no halt below strategy loss threshold, got %+v", below)
	}
	at := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.StrategyNetPnL = map[string]float64{"single_venue_spot_perp": -35}
	}), cfg)
	if !at.ShouldHalt || !at.StrategyLossBreached {
		t.Fatalf("expected halt at strategy loss boundary, got %+v", at)
	}
}

func TestEvaluateAccountHardHaltIntent_OpenNotionalBoundary(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 0
	cfg.MaxAccountDrawdownUSDT = 0
	cfg.MaxStrategyCumulativeLossUSDT = 0
	cfg.MaxAccountOpenNotionalUSDT = 1200
	cfg.MaxGrossLeverage = 0

	below := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.OpenNotionalUSDT = 1199.99
	}), cfg)
	if below.ShouldHalt {
		t.Fatalf("expected no halt below notional cap, got %+v", below)
	}
	at := evaluateAccountHardHaltIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.OpenNotionalUSDT = 1200
	}), cfg)
	if !at.ShouldHalt || !at.OpenNotionalBreached {
		t.Fatalf("expected halt at notional cap, got %+v", at)
	}
}

func TestEvaluateAccountAutoFlattenIntent_OptInOff(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = false
	cfg.AutoFlattenAccountDrawdownUSDT = 150

	got := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 500
	}), cfg)
	if got.ShouldFlatten {
		t.Fatalf("opt-in off must not flatten, got %+v", got)
	}
}

func TestEvaluateAccountAutoFlattenIntent_RealEquityBoundary(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150
	cfg.AutoFlattenAccountDrawdownPct = 0

	below := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 149.99
		s.RealEquityTotalUSDT = 850
		s.RealEquityPeakUSDT = 999.99
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if below.ShouldFlatten {
		t.Fatalf("expected no flatten below threshold, got %+v", below)
	}
	at := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.RealEquityDrawdownUSDT = 150
		s.RealEquityTotalUSDT = 850
		s.RealEquityPeakUSDT = 1000
		s.RealEquityQualityPassed = true
		s.RealEquityPeakConfirmed = true
	}), cfg)
	if !at.ShouldFlatten || at.Reason == "" {
		t.Fatalf("expected flatten at real equity boundary, got %+v", at)
	}
}

func TestEvaluateAccountAutoFlattenIntent_SettlementIllusionIgnored(t *testing.T) {
	cfg := DefaultEngineConfig().RiskConfig
	cfg.AutoFlattenOnHardDrawdownEnabled = true
	cfg.AutoFlattenAccountDrawdownUSDT = 150

	got := evaluateAccountAutoFlattenIntent(testAccountRiskSnapshotForEval(0, func(s *accountRiskSnapshot) {
		s.AccountDrawdownUSDT = 500
		s.RealEquityDrawdownUSDT = 10
	}), cfg)
	if got.ShouldFlatten {
		t.Fatalf("settlement illusion must not trigger auto-flatten, got %+v", got)
	}
}

func TestComputeRealEquityWaterMark(t *testing.T) {
	dir := t.TempDir()
	cvDir := crossVenueRecordDir(dir)
	if err := os.MkdirAll(cvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := stringsJoinLines(
		`{"ts":"2026-06-19T00:00:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:00:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:03:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"okx","cash_usdt":1000}`,
		`{"ts":"2026-06-19T00:06:00Z","venue":"gate","cash_usdt":500}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"okx","cash_usdt":950}`,
		`{"ts":"2026-06-19T00:09:00Z","venue":"gate","cash_usdt":500}`,
	)
	path := filepath.Join(cvDir, equitySnapshotsFileName)
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	current, peak, drawdown := computeRealEquityWaterMark(dir)
	if peak != 1500 || current != 1450 || drawdown != 50 {
		t.Fatalf("watermark current=%v peak=%v drawdown=%v, want 1450/1500/50", current, peak, drawdown)
	}
}

func TestComputeRealizedPnLWaterMark(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	records := []strategySettlementRecord{
		{SettlementID: "a", CloseTime: now.Add(-3 * time.Hour), NetPnL: 10},
		{SettlementID: "b", CloseTime: now.Add(-2 * time.Hour), NetPnL: 15},
		{SettlementID: "c", CloseTime: now.Add(-1 * time.Hour), NetPnL: -20},
	}
	cumulative, peak, drawdown := computeRealizedPnLWaterMark(records)
	if cumulative != 5 || peak != 25 || drawdown != 20 {
		t.Fatalf("watermark cumulative=%v peak=%v drawdown=%v, want 5/25/20", cumulative, peak, drawdown)
	}
}

func TestReadEquitySnapshotTotalDelta(t *testing.T) {
	dir := t.TempDir()
	cvDir := crossVenueRecordDir(dir)
	if err := os.MkdirAll(cvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dayStart := beijingCSTDayStart(time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC))
	beforeDay := dayStart.Add(-time.Hour).UTC().Format(time.RFC3339)
	afterDay := dayStart.Add(30 * time.Minute).UTC().Format(time.RFC3339)
	lines := stringsJoinLines(
		`{"ts":"`+beforeDay+`","venue":"okx","cash_usdt":1000}`,
		`{"ts":"`+afterDay+`","venue":"okx","cash_usdt":1000}`,
		`{"ts":"`+afterDay+`","venue":"gate","cash_usdt":500}`,
		`{"ts":"`+dayStart.Add(2*time.Hour).UTC().Format(time.RFC3339)+`","venue":"okx","cash_usdt":970}`,
		`{"ts":"`+dayStart.Add(2*time.Hour).UTC().Format(time.RFC3339)+`","venue":"gate","cash_usdt":490}`,
	)
	path := filepath.Join(cvDir, equitySnapshotsFileName)
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	total, byVenue := readEquitySnapshotTotalDelta(dir, dayStart)
	if byVenue["okx"] != -30 {
		t.Fatalf("okx delta=%v want -30", byVenue["okx"])
	}
	if byVenue["gate"] != -10 {
		t.Fatalf("gate delta=%v want -10", byVenue["gate"])
	}
	if total != -40 {
		t.Fatalf("total delta=%v want -40", total)
	}
}

func TestAccountRiskWatcherTickTriggersHaltWithoutFlatten(t *testing.T) {
	var notifyCount atomic.Int32
	var haltCount atomic.Int32
	var flattenCount atomic.Int32

	dir := t.TempDir()
	cfg := DefaultEngineConfig()
	cfg.NetworkConfig.RecordDir = dir
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = false
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(85, -15, map[string]float64{"okx": 85})
	}
	w.positionFn = func() accountRiskPositionSummary {
		return accountRiskPositionSummary{TotalCount: 1, OpenNotional: 50, ByStrategy: map[string]int{"cross_venue_perp_perp": 1}}
	}
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 100, 100, 0 }
	w.notifyFn = func(title, detail string) { notifyCount.Add(1) }
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}

	w.tickOnce()

	if haltCount.Load() != 1 {
		t.Fatalf("expected SetGlobalHalt once, got %d", haltCount.Load())
	}
	if flattenCount.Load() != 0 {
		t.Fatalf("flatten must not run with opt-in off, got %d", flattenCount.Load())
	}
	if notifyCount.Load() != 1 {
		t.Fatalf("expected one critical alert, got %d", notifyCount.Load())
	}

	path := filepath.Join(dir, accountRiskWatchFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted watch: %v", err)
	}
	var persisted persistedAccountRiskWatch
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal persisted watch: %v", err)
	}
	if !persisted.Wired || !persisted.HardHalt.ShouldHalt {
		t.Fatalf("expected wired hard-halt snapshot, got %+v", persisted)
	}
}

func TestAccountRiskWatcherAutoFlattenOptIn(t *testing.T) {
	var haltCount atomic.Int32
	var rotationOff atomic.Bool
	var flattenCount atomic.Int32

	dir := t.TempDir()
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 0
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 0
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = true
	cfg.RiskConfig.AutoFlattenAccountDrawdownUSDT = 150
	cfg.RiskConfig.AutoFlattenAccountDrawdownPct = 0
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: dir,
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.equityFn = func(now time.Time) accountEquityObservation { return accountEquityObservation{ReadOK: true} }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 850, 1000, 150 }
	w.realEquityQualityFn = func(recordDir string) realEquityQualityResult {
		return realEquityQualityResult{Passed: true, PeakConfirmed: true}
	}
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.setRotationActiveFn = func(on bool) {
		if !on {
			rotationOff.Store(true)
		}
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 1 {
		t.Fatalf("expected halt before flatten, got halt=%d", haltCount.Load())
	}
	if !rotationOff.Load() {
		t.Fatal("expected rotation SetActive(false) before flatten")
	}
	if flattenCount.Load() != 1 {
		t.Fatalf("expected flatten once, got %d", flattenCount.Load())
	}

	w.tickOnce()
	if flattenCount.Load() != 1 {
		t.Fatalf("flatten must be idempotent across ticks, got %d", flattenCount.Load())
	}
}

func TestAccountRiskWatcherSettlementsIllusionNoHalt(t *testing.T) {
	var haltCount atomic.Int32

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 0
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 60
	cfg.RiskConfig.MaxStrategyCumulativeLossUSDT = 35
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = false
	cfg.AlertingConfig.Enabled = false

	after := attributionFixBoundaryTime.Add(time.Hour)
	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return after.Add(time.Hour) }
	w.settlementFn = func() ([]strategySettlementRecord, error) {
		return []strategySettlementRecord{
			{
				SettlementID:       "hei",
				StrategyType:       UnifiedStrategyPerpPerp,
				CloseTime:          after,
				FundingTruthStatus: "complete",
				TradePnL:           -801.56,
				NetPnL:             -801.56,
			},
		}, nil
	}
	w.equityFn = func(now time.Time) accountEquityObservation { return accountEquityObservation{ReadOK: true} }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 1000, 1000, 0 }
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 {
		t.Fatalf("HEI untrusted_legacy must not trigger halt, got halt=%d", haltCount.Load())
	}
}

func TestAccountRiskWatcherSettlementsIllusionNoAutoFlatten(t *testing.T) {
	var flattenCount atomic.Int32

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 0
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 0
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = true
	cfg.RiskConfig.AutoFlattenAccountDrawdownUSDT = 150
	cfg.RiskConfig.AutoFlattenAccountDrawdownPct = 0
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.settlementFn = func() ([]strategySettlementRecord, error) {
		return []strategySettlementRecord{
			{SettlementID: "x", CloseTime: time.Now(), NetPnL: -500, StrategyType: "cross_venue_perp_perp"},
		}, nil
	}
	w.equityFn = func(now time.Time) accountEquityObservation { return accountEquityObservation{ReadOK: true} }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 990, 1000, 10 }
	w.notifyFn = func(title, detail string) {}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}

	w.tickOnce()
	if flattenCount.Load() != 0 {
		t.Fatalf("settlement illusion must not auto-flatten, flatten count=%d", flattenCount.Load())
	}
}

func TestAccountRiskWatcherNoActionWhenSafe(t *testing.T) {
	var haltCount atomic.Int32
	var flattenCount atomic.Int32
	var notifyCount atomic.Int32

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 25
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 60
	cfg.RiskConfig.MaxStrategyCumulativeLossUSDT = 35
	cfg.RiskConfig.MaxAccountOpenNotionalUSDT = 1200
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = false
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(1005, 5, nil)
	}
	w.positionFn = func() accountRiskPositionSummary {
		return accountRiskPositionSummary{OpenNotional: 100}
	}
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 1000, 1000, 0 }
	w.notifyFn = func(title, detail string) { notifyCount.Add(1) }
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}

	w.tickOnce()
	if haltCount.Load() != 0 || flattenCount.Load() != 0 || notifyCount.Load() != 0 {
		t.Fatalf("safe snapshot must not halt/flatten/alert, halt=%d flatten=%d notify=%d",
			haltCount.Load(), flattenCount.Load(), notifyCount.Load())
	}
}

func TestAccountRiskWatcherRunDoesNotBlockOnCancel(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 0
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		startAccountRiskWatcher(ctx, accountRiskWatchConfig{
			Cfg:       cfg,
			RecordDir: t.TempDir(),
			Logger:    log.New(os.Stderr, "", 0),
		})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher goroutine did not exit on context cancel")
	}
}

func TestEvaluateAccountRiskThresholds_UntrustedSkips(t *testing.T) {
	snap := testAccountRiskSnapshotForEval(-100, nil)
	snap.EquityDataTrusted = false
	cfg := DefaultEngineConfig().RiskConfig
	cfg.MaxDailyLossUSDT = 25
	got := evaluateAccountRiskThresholds(snap, cfg)
	if got.AnyBreached {
		t.Fatalf("untrusted snapshot must skip thresholds, got %+v", got)
	}
}

func TestAssessAccountEquityTrust_VenueZeroSkipped(t *testing.T) {
	st := &accountRiskEquityState{
		HasLastTrusted:       true,
		LastTrustedTotalUSDT: 118,
		LastTrustedByVenue:   map[string]float64{"okx": 118},
	}
	cfg := DefaultEngineConfig().RiskConfig
	trusted, reason := assessAccountEquityTrust(
		accountEquityObservation{TotalUSDT: 0, ByVenue: map[string]float64{"okx": 0}, ReadOK: true},
		0,
		accountRiskPositionSummary{},
		st,
		cfg,
		t.TempDir(),
		time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
	)
	if trusted || reason == "" {
		t.Fatalf("venue zero must be untrusted, trusted=%v reason=%q", trusted, reason)
	}
}

func TestAssessAccountEquityTrust_ReadFailureSkipped(t *testing.T) {
	trusted, reason := assessAccountEquityTrust(
		accountEquityObservation{ReadOK: false},
		100,
		accountRiskPositionSummary{},
		&accountRiskEquityState{},
		DefaultEngineConfig().RiskConfig,
		t.TempDir(),
		time.Now().UTC(),
	)
	if trusted || reason == "" {
		t.Fatalf("read failure must be untrusted, trusted=%v reason=%q", trusted, reason)
	}
}

func TestDetectAccountFundFlowStep_WithdrawalRebaseline(t *testing.T) {
	st := &accountRiskEquityState{
		HasLastTrusted:       true,
		LastTrustedTotalUSDT: 435,
		LastPositionCount:    0,
	}
	step := detectAccountFundFlowStep(st, 385.55, -49.45, 0, accountRiskPositionSummary{}, 0, 5)
	if step != -49.45 {
		t.Fatalf("step=%v want -49.45", step)
	}
	if st.DailyFundFlowAdjustUSDT != -49.45 {
		t.Fatalf("adjust=%v want -49.45", st.DailyFundFlowAdjustUSDT)
	}
	adjusted := -49.45 - st.DailyFundFlowAdjustUSDT
	if adjusted != 0 {
		t.Fatalf("risk delta=%v want 0", adjusted)
	}
}

func TestAccountRiskWatcherWithdrawalNoHalt(t *testing.T) {
	var haltCount atomic.Int32
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 40
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.nowFn = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 385.55, 435, 49.45 }
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 435
	w.equityState.LastTrustedByVenue = map[string]float64{"okx": 435}
	w.equityState.LastPositionCount = 0
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(385.55, -49.45, map[string]float64{"okx": 385.55})
	}

	w.tickOnce()
	if haltCount.Load() != 0 {
		t.Fatalf("withdrawal must not halt, got halt=%d", haltCount.Load())
	}
}

func TestAccountRiskWatcherUnreliableEquitySkipsHaltAndFlatten(t *testing.T) {
	var haltCount atomic.Int32
	var flattenCount atomic.Int32

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	cfg.RiskConfig.AutoFlattenOnHardDrawdownEnabled = true
	cfg.RiskConfig.AutoFlattenAccountDrawdownUSDT = 150
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.settlementFn = func() ([]strategySettlementRecord, error) { return nil, nil }
	w.positionFn = func() accountRiskPositionSummary { return accountRiskPositionSummary{} }
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 0, 118, 118 }
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.flattenFn = func(ctx context.Context) error {
		flattenCount.Add(1)
		return nil
	}
	w.equityState.HasLastTrusted = true
	w.equityState.LastTrustedTotalUSDT = 118
	w.equityState.LastTrustedByVenue = map[string]float64{"okx": 118}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(0, -118, map[string]float64{"okx": 0})
	}

	w.tickOnce()
	if haltCount.Load() != 0 || flattenCount.Load() != 0 {
		t.Fatalf("unreliable equity must skip halt/flatten, halt=%d flatten=%d", haltCount.Load(), flattenCount.Load())
	}
}

func TestAccountRiskWatcherRealTradingLossStillHalts(t *testing.T) {
	var haltCount atomic.Int32
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	cfg.AlertingConfig.Enabled = false

	w := newAccountRiskWatcher(accountRiskWatchConfig{
		Cfg:       cfg,
		RecordDir: t.TempDir(),
		Logger:    log.New(os.Stderr, "", 0),
	})
	w.settlementFn = func() ([]strategySettlementRecord, error) {
		return []strategySettlementRecord{
			{SettlementID: "loss", CloseTime: time.Now(), NetPnL: -15, StrategyType: "cross_venue_perp_perp"},
		}, nil
	}
	w.positionFn = func() accountRiskPositionSummary {
		return accountRiskPositionSummary{TotalCount: 1, OpenNotional: 50}
	}
	w.realEquityFn = func(recordDir string) (float64, float64, float64) { return 85, 100, 15 }
	w.notifyFn = func(title, detail string) {}
	w.haltFn = func(reason string) error {
		haltCount.Add(1)
		return nil
	}
	w.equityFn = func(now time.Time) accountEquityObservation {
		return testAccountEquityObservation(85, -15, map[string]float64{"okx": 85})
	}

	w.tickOnce()
	if haltCount.Load() != 1 {
		t.Fatalf("real trading loss must halt, got halt=%d", haltCount.Load())
	}
}

func stringsJoinLines(lines ...string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out + "\n"
}
