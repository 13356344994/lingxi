package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/okx"
)

// deadManFalsePositiveVenue 模拟 binance:Capabilities 声明 HasNativeDeadMan=true,
// 但 ArmDeadMansSwitch 仍返回 ErrUnsupported(须按 symbol 逐笔武装,全局接口未实现)。
type deadManFalsePositiveVenue struct {
	fakeFundingVenue
}

func (d *deadManFalsePositiveVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (d *deadManFalsePositiveVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return venue.ErrUnsupported
}

// deadManArmFailVenue 声明支持 dead-man,但续期返回真实错误(非 unsupported)。
type deadManArmFailVenue struct {
	fakeFundingVenue
}

func (d *deadManArmFailVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (d *deadManArmFailVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return errors.New("network timeout")
}

// deadManCountVenue 声明支持 dead-man 且续期成功,统计被调用次数(验证按所节流)。
type deadManCountVenue struct {
	fakeFundingVenue
	arms int
}

func (d *deadManCountVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (d *deadManCountVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	d.arms++
	return nil
}

// TestArmDeadMansForLegsThrottlesPerVenue:同一周期内多笔含同一所的腿只续期一次,
// 避免账户级 cancel-all-after(OKX 限频 1次/2秒)被逐笔重复触发而 429。
func TestArmDeadMansForLegsThrottlesPerVenue(t *testing.T) {
	v := &deadManCountVenue{fakeFundingVenue: fakeFundingVenue{name: okx.VenueName}}
	now := time.Now()
	e := &CrossVenueExecutor{
		cfg:                      testCrossVenueExecutorConfig(),
		venues:                   map[string]venue.Venue{okx.VenueName: v},
		logger:                   log.New(&bytes.Buffer{}, "", 0),
		deadManUnsupportedVenues: make(map[string]struct{}),
		deadManLastArm:           make(map[string]time.Time),
		nowFn:                    func() time.Time { return now },
	}
	pos := CrossVenuePosition{HighVenue: okx.VenueName, LowVenue: okx.VenueName}
	// 同一周期(now 不变)三笔含 okx 腿的持仓:应只真正续期一次。
	e.armDeadMansForLegs(context.Background(), pos, true)
	e.armDeadMansForLegs(context.Background(), pos, true)
	e.armDeadMansForLegs(context.Background(), pos, true)
	if v.arms != 1 {
		t.Fatalf("同周期同所应只续期 1 次(节流),实得 %d", v.arms)
	}
	// 推进超过 cd/2 后应再次续期。
	cd := time.Duration(e.cfg.CrossVenueExecutor.DeadManCountdownSeconds) * time.Second
	if cd <= 0 {
		cd = 60 * time.Second
	}
	now = now.Add(cd/2 + time.Second)
	e.armDeadMansForLegs(context.Background(), pos, true)
	if v.arms != 2 {
		t.Fatalf("超过 cd/2 后应再续期一次,实得 %d", v.arms)
	}
}

func TestIsDeadManUnsupportedErr(t *testing.T) {
	if !isDeadManUnsupportedErr(venue.ErrUnsupported) {
		t.Fatal("ErrUnsupported should match")
	}
	if !isDeadManUnsupportedErr(errors.New("venue: capability unsupported")) {
		t.Fatal("capability unsupported message should match")
	}
	if isDeadManUnsupportedErr(errors.New("network timeout")) {
		t.Fatal("real errors must not match")
	}
}

func TestArmDeadMansForLegsSkipsUnsupportedVenue(t *testing.T) {
	var buf bytes.Buffer
	e := &CrossVenueExecutor{
		cfg: testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{
			binance.VenueName: &deadManFalsePositiveVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}},
			okx.VenueName:     newCrossVenueTestVenue(okx.VenueName, 0.001, 8, time.Now()),
		},
		logger:                   log.New(&buf, "", 0),
		deadManUnsupportedVenues: make(map[string]struct{}),
	}
	pos := CrossVenuePosition{HighVenue: binance.VenueName, LowVenue: okx.VenueName}

	e.armDeadMansForLegs(context.Background(), pos, true)
	e.armDeadMansForLegs(context.Background(), pos, true)

	if strings.Contains(buf.String(), "dead-man 续期失败") {
		t.Fatalf("unsupported venue should not log renewal failure, got: %q", buf.String())
	}
	e.mu.RLock()
	_, cached := e.deadManUnsupportedVenues[binance.VenueName]
	e.mu.RUnlock()
	if !cached {
		t.Fatal("binance should be cached as dead-man unsupported")
	}
}

func TestArmDeadMansForLegsLogsRealFailure(t *testing.T) {
	var buf bytes.Buffer
	e := &CrossVenueExecutor{
		cfg: testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{
			okx.VenueName: &deadManArmFailVenue{fakeFundingVenue: fakeFundingVenue{name: okx.VenueName}},
		},
		logger:                   log.New(&buf, "", 0),
		deadManUnsupportedVenues: make(map[string]struct{}),
	}
	pos := CrossVenuePosition{HighVenue: okx.VenueName, LowVenue: okx.VenueName}

	e.armDeadMansForLegs(context.Background(), pos, true)

	if !strings.Contains(buf.String(), "[dead-man] 续期失败") {
		t.Fatalf("real renewal failure should be logged, got: %q", buf.String())
	}
}

type realizedFundingVenue struct {
	fakeFundingVenue
	incomes []venue.FundingIncome
}

func (v *realizedFundingVenue) FetchRealizedFunding(_ context.Context, _ venue.Symbol, _ time.Time) ([]venue.FundingIncome, error) {
	return v.incomes, nil
}

func TestSyncRealizedFundingOverwritesLegs(t *testing.T) {
	t0 := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	high := &realizedFundingVenue{
		fakeFundingVenue: fakeFundingVenue{name: "high"},
		incomes:          []venue.FundingIncome{{Income: 1.25}},
	}
	low := &realizedFundingVenue{
		fakeFundingVenue: fakeFundingVenue{name: "low"},
		incomes:          []venue.FundingIncome{{Income: -0.25}},
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.RealizedFundingPollMinutes = 5
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{"high": high, "low": low},
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return t0 },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: 0.001, IntervalHours: 8}},
			"low":  {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8}},
		}),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		NotionalUSDT: 100, OpenedAt: t0.Add(-2 * time.Hour),
		HighLegFundingUSDT: 99, LowLegFundingUSDT: -99,
	}
	e.syncRealizedFunding(context.Background(), &pos)
	if pos.HighLegFundingUSDT != 1.25 {
		t.Fatalf("high leg want 1.25 got %.4f", pos.HighLegFundingUSDT)
	}
	if pos.LowLegFundingUSDT != -0.25 {
		t.Fatalf("low leg want -0.25 got %.4f", pos.LowLegFundingUSDT)
	}
	if pos.LastRealizedFundingAt != t0 {
		t.Fatalf("LastRealizedFundingAt=%v want %v", pos.LastRealizedFundingAt, t0)
	}
}

func TestShouldCloseCrossVenueMinHoldBlocksSoftClose(t *testing.T) {
	now := time.Date(2026, 6, 14, 1, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.MinHoldHours = 24
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 1
	e := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: 0.001, IntervalHours: 8}},
			"low":  {"BTC-USDT": {Rate: 0.0008, IntervalHours: 8}},
		}),
		logger: testCrossVenueLogger(),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		CloseSpreadStreak: 1,
	}
	if reason := e.shouldCloseCrossVenue(context.Background(), &pos); reason != "" {
		t.Fatalf("min hold should block soft close, got reason=%q", reason)
	}
}

func TestShouldCloseCrossVenueHardNegativeBypassesMinHold(t *testing.T) {
	now := time.Date(2026, 6, 14, 1, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.MinHoldHours = 24
	cfg.CrossVenueExecutor.CloseHardNegativeSpreadAPR = -0.5
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 1
	e := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: -0.01, IntervalHours: 8}},
			"low":  {"BTC-USDT": {Rate: 0.02, IntervalHours: 8}},
		}),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		CloseHardNegativeSpreadStreak: 1,
	}
	reason := e.shouldCloseCrossVenue(context.Background(), &pos)
	if reason == "" || !strings.Contains(reason, "硬止损") {
		t.Fatalf("hard negative should close immediately, got %q", reason)
	}
}

func TestShouldCloseCrossVenueSoftCloseAfterFundingCoversFees(t *testing.T) {
	now := time.Date(2026, 6, 14, 1, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.MinHoldHours = 24
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 1
	e := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: 0.001, IntervalHours: 8}},
			"low":  {"BTC-USDT": {Rate: 0.0008, IntervalHours: 8}},
		}),
	}
	required := crossVenueRoundTripFeeUSDT(cfg.CrossVenueExecutor, &CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low", NotionalUSDT: 100,
	})
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		HighLegFundingUSDT: required, LowLegFundingUSDT: 0,
		CloseSpreadStreak: 1,
	}
	reason := e.shouldCloseCrossVenue(context.Background(), &pos)
	if reason == "" || strings.Contains(reason, "硬止损") {
		t.Fatalf("funding-covered soft close should trigger, got %q", reason)
	}
}

func TestRefreshCurrentPositionMetricsPopulatesFutureEV(t *testing.T) {
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	e := &CrossVenueExecutor{
		cfg:   testCrossVenueExecutorConfig(),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			"low":  {"BTC-USDT": {Rate: 0.0001, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
		}),
	}
	e.scanner.lastSnapshot.SnapshotID = "snap-1"
	e.scanner.lastSnapshot.ScannedAt = now.Add(-time.Minute)
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		NotionalUSDT: 100, OpenedAt: now.Add(-time.Hour),
		ExecutionScore: 0.8, RecoveryQualityScore: 0.7,
	}
	e.refreshCurrentPositionMetrics(&pos)
	if pos.CurrentSnapshotID != "snap-1" {
		t.Fatalf("current_snapshot_id=%q", pos.CurrentSnapshotID)
	}
	if pos.CurrentComparableWindowHours <= 0 {
		t.Fatalf("comparable window not populated: %+v", pos)
	}
	if pos.CurrentFutureEV == 0 {
		t.Fatalf("future EV should be populated: %+v", pos)
	}
}

func TestCloseBeforeAdverseFunding(t *testing.T) {
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.MinHoldHours = 24
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 3
	cfg.CrossVenueExecutor.CloseBeforeAdverseFundingEnabled = true
	cfg.CrossVenueExecutor.PreFundingCloseLeadSeconds = 120
	lowNext := now.Add(60 * time.Second)
	e := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: 0.001, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			"low":  {"BTC-USDT": {Rate: 0.003, IntervalHours: 1, NextFundingTime: lowNext}},
		}),
		logger: testCrossVenueLogger(),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		HighNextFundingTime: now.Add(4 * time.Hour), LowNextFundingTime: lowNext,
		HighIntervalHours: 8, LowIntervalHours: 1,
	}
	e.refreshCurrentPositionMetrics(&pos)
	reason := e.shouldCloseCrossVenue(context.Background(), &pos)
	if reason == "" || !strings.Contains(reason, "结算前不利资费") {
		t.Fatalf("expected immediate close before adverse funding, got %q", reason)
	}
}

func TestNoCloseOnNoiseAwayFromSettlement(t *testing.T) {
	now := time.Date(2026, 6, 20, 4, 0, 0, 0, time.UTC)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.MinHoldHours = 24
	cfg.CrossVenueExecutor.CloseSpreadConsecutiveRounds = 3
	cfg.CrossVenueExecutor.CloseBeforeAdverseFundingEnabled = true
	cfg.CrossVenueExecutor.PreFundingCloseLeadSeconds = 120
	e := &CrossVenueExecutor{
		cfg:   cfg,
		store: mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		nowFn: func() time.Time { return now },
		scanner: testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
			"high": {"BTC-USDT": {Rate: -0.002, IntervalHours: 8, NextFundingTime: now.Add(4 * time.Hour)}},
			"low":  {"BTC-USDT": {Rate: 0.003, IntervalHours: 1, NextFundingTime: now.Add(2 * time.Hour)}},
		}),
		logger: testCrossVenueLogger(),
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		HighNextFundingTime: now.Add(4 * time.Hour), LowNextFundingTime: now.Add(2 * time.Hour),
		HighIntervalHours: 8, LowIntervalHours: 1,
	}
	e.refreshCurrentPositionMetrics(&pos)
	if reason := e.shouldCloseCrossVenue(context.Background(), &pos); reason != "" {
		t.Fatalf("adverse rates far from settlement should not trigger close, got %q", reason)
	}

	nearNext := now.Add(30 * time.Second)
	e.scanner = testScannerWithFunding(map[string]map[string]CrossVenueFundingEntry{
		"high": {"BTC-USDT": {Rate: 0.004, IntervalHours: 8, NextFundingTime: nearNext}},
		"low":  {"BTC-USDT": {Rate: 0.0001, IntervalHours: 1, NextFundingTime: now.Add(2 * time.Hour)}},
	})
	pos2 := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: "high", LowVenue: "low",
		OpenedAt: now.Add(-2 * time.Hour), NotionalUSDT: 100,
		HighNextFundingTime: nearNext, LowNextFundingTime: now.Add(2 * time.Hour),
		HighIntervalHours: 8, LowIntervalHours: 1,
	}
	e.refreshCurrentPositionMetrics(&pos2)
	if reason := e.shouldCloseCrossVenue(context.Background(), &pos2); reason != "" {
		t.Fatalf("favorable funding near settlement should not trigger adverse close, got %q", reason)
	}
}
