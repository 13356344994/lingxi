package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// fakeFundingVenue 是单测用的 venue.Venue 桩,只定制 FetchFundingSnapshot。
type fakeFundingVenue struct {
	name         string
	snaps        []venue.FundingSnapshot
	err          error
	delay        time.Duration
	called       int
	capsOverride *venue.Capabilities // 可选覆盖
}

func (f *fakeFundingVenue) Name() string { return f.name }
func (f *fakeFundingVenue) Capabilities() venue.Capabilities {
	if f.capsOverride != nil {
		return *f.capsOverride
	}
	caps := venue.Capabilities{}
	switch f.name {
	case bybit.VenueName, okx.VenueName, gate.VenueName:
		caps.UnifiedWallet = true
	}
	return caps
}
func (f *fakeFundingVenue) ToVenue(c venue.Symbol, m venue.Market) (string, error) {
	return string(c), nil
}
func (f *fakeFundingVenue) ToCanonical(raw string, m venue.Market) (venue.Symbol, error) {
	return venue.Symbol(raw), nil
}
func (f *fakeFundingVenue) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	f.called++
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.snaps, nil
}
func (f *fakeFundingVenue) FetchInstruments(context.Context) ([]venue.Instrument, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchDepth(context.Context, venue.Market, venue.Symbol, int) (venue.Depth, error) {
	return venue.Depth{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchOpenInterest(context.Context, venue.Symbol) (float64, error) {
	return 0, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchRecentQuoteVolume(context.Context, venue.Market, venue.Symbol, time.Duration) (float64, error) {
	return 0, venue.ErrUnsupported
}
func (f *fakeFundingVenue) ServerTime(context.Context) (time.Time, error) {
	return time.Time{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchBalances(context.Context) (venue.Balances, error) {
	return venue.Balances{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchTradingCostProfile(context.Context, []venue.Symbol) (venue.CostProfile, error) {
	return venue.CostProfile{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) MaxBorrowable(context.Context, string) (float64, error) {
	return 0, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchBorrowQuote(context.Context, string) (venue.BorrowQuote, error) {
	return venue.BorrowQuote{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchLeverageBrackets(context.Context, venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) VerifyAPIKeyPosture(context.Context) (venue.KeyPosture, error) {
	return venue.KeyPosture{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) PlaceOrder(context.Context, venue.OrderRequest) (venue.OrderResult, error) {
	return venue.OrderResult{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) PlaceBatch(context.Context, []venue.OrderRequest) ([]venue.OrderResult, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) CancelOrder(context.Context, venue.Market, venue.Symbol, string) error {
	return venue.ErrUnsupported
}
func (f *fakeFundingVenue) QueryOrder(context.Context, venue.Market, venue.Symbol, string) (venue.OrderResult, error) {
	return venue.OrderResult{}, venue.ErrUnsupported
}
func (f *fakeFundingVenue) SetLeverage(context.Context, venue.Symbol, int) error {
	return venue.ErrUnsupported
}
func (f *fakeFundingVenue) SetMarginMode(context.Context, venue.Symbol, venue.MarginMode) error {
	return venue.ErrUnsupported
}
func (f *fakeFundingVenue) BorrowRepay(context.Context, string, float64, venue.BorrowDir) error {
	return venue.ErrUnsupported
}
func (f *fakeFundingVenue) FetchRealizedFunding(context.Context, venue.Symbol, time.Time) ([]venue.FundingIncome, error) {
	return nil, nil
}
func (f *fakeFundingVenue) FetchUserTrades(context.Context, venue.Market, venue.Symbol, time.Time) ([]venue.Trade, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) StreamBookTicker(context.Context, []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) StreamDepth(context.Context, []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) StreamUserData(context.Context) (<-chan venue.UserEvent, error) {
	return nil, venue.ErrUnsupported
}
func (f *fakeFundingVenue) Classify(error) (string, string) { return "", "" }
func (f *fakeFundingVenue) ReserveOrderBudget(int) bool     { return true }
func (f *fakeFundingVenue) ReleaseOrderBudget(int)          {}
func (f *fakeFundingVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return venue.ErrUnsupported
}
func (f *fakeFundingVenue) CaptureMovableSnapshot(context.Context) ([]venue.WalletSnapshot, float64, error) {
	return nil, 0, venue.ErrUnsupported
}
func (f *fakeFundingVenue) PlanAllocation(float64, venue.Capabilities) venue.CapitalAllocationPlan {
	return venue.CapitalAllocationPlan{}
}
func (f *fakeFundingVenue) ExecuteRebalance(context.Context, venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	return nil, venue.ErrUnsupported
}

func TestFundingAnnualizedAPRIntervals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rate     float64
		interval int
		want     float64
	}{
		{0.001, 8, 0.001 * (8760.0 / 8)},
		{0.001, 4, 0.001 * (8760.0 / 4)},
		{0.001, 1, 0.001 * 8760.0},
		{-0.002, 8, -0.002 * (8760.0 / 8)},
	}
	for _, tc := range cases {
		got := fundingAnnualizedAPR(tc.rate, tc.interval)
		if got != tc.want {
			t.Fatalf("interval=%dh rate=%v got=%v want=%v", tc.interval, tc.rate, got, tc.want)
		}
	}
	if apr := fundingAnnualizedAPR(0.001, 0); apr != 0.001*(8760.0/8) {
		t.Fatalf("interval<=0 应回退 8h: got=%v", apr)
	}
}

func TestBestPairSpreadPicksMax(t *testing.T) {
	t.Parallel()
	entries := []symbolVenueFunding{
		{Venue: binance.VenueName, AnnualizedAPR: 0.10, IntervalHours: 8},
		{Venue: bybit.VenueName, AnnualizedAPR: 0.50, IntervalHours: 1},
		{Venue: okx.VenueName, AnnualizedAPR: 0.20, IntervalHours: 4},
	}
	opp, ok := bestPairSpread("HOME-USDT", entries)
	if !ok {
		t.Fatal("expected pair")
	}
	if opp.HighVenue != bybit.VenueName || opp.LowVenue != binance.VenueName {
		t.Fatalf("best pair = %s/%s, want bybit/binance", opp.HighVenue, opp.LowVenue)
	}
	if opp.SpreadAPR != 0.40 {
		t.Fatalf("spread=%v want=0.40", opp.SpreadAPR)
	}
}

func TestComputeCrossVenueOpportunitiesFilterAndTopN(t *testing.T) {
	t.Parallel()
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: {
			{Symbol: "AAA-USDT", LastFundingRate: 0.001, IntervalHours: 8},
			{Symbol: "BBB-USDT", LastFundingRate: 0.0001, IntervalHours: 8},
		},
		bybit.VenueName: {
			{Symbol: "AAA-USDT", LastFundingRate: 0.01, IntervalHours: 1},
			{Symbol: "BBB-USDT", LastFundingRate: 0.0002, IntervalHours: 8},
		},
		okx.VenueName: {
			{Symbol: "CCC-USDT", LastFundingRate: 0.00005, IntervalHours: 8},
		},
		gate.VenueName: {
			{Symbol: "CCC-USDT", LastFundingRate: 0.00006, IntervalHours: 8},
		},
	}
	opps := computeCrossVenueOpportunities(byVenue, 0.30, 1)
	if len(opps) != 1 {
		t.Fatalf("top_n=1 got %d opps", len(opps))
	}
	if opps[0].Symbol != "AAA-USDT" {
		t.Fatalf("expected AAA-USDT top spread, got %s", opps[0].Symbol)
	}
	if opps[0].SpreadAPR < 0.30 {
		t.Fatalf("spread below threshold: %v", opps[0].SpreadAPR)
	}
}

func TestCrossVenueScannerSingleVenueFailureDegrades(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	failVenue := &fakeFundingVenue{name: binance.VenueName, err: errors.New("boom")}
	nextFunding := now.Add(2 * time.Hour)
	okVenue := &fakeFundingVenue{
		name: bybit.VenueName,
		snaps: []venue.FundingSnapshot{
			{Symbol: "X-USDT", LastFundingRate: 0.0002, IntervalHours: 8, NextFundingTime: nextFunding},
		},
	}
	okVenue2 := &fakeFundingVenue{
		name: okx.VenueName,
		snaps: []venue.FundingSnapshot{
			{Symbol: "X-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
		},
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			Enabled:      true,
			MinSpreadAPR: 0.01,
			TopN:         10,
		},
		recordDir: dir,
		venues: map[string]venue.Venue{
			binance.VenueName: failVenue,
			bybit.VenueName:   okVenue,
			okx.VenueName:     okVenue2,
			gate.VenueName:    &fakeFundingVenue{name: gate.VenueName, err: errors.New("skip")},
		},
		logger:     logDefaultForTest(t),
		nowFn:      func() time.Time { return now },
		fetchVenue: defaultFetchVenueSnapshot,
	}
	s.scanOnce(context.Background())
	snap := s.Snapshot()
	if len(snap.Opportunities) == 0 {
		t.Fatal("expected opportunities despite binance/gate failure")
	}
	var binanceStatus *CrossVenueVenueStatus
	for i := range snap.VenueStatuses {
		if snap.VenueStatuses[i].Venue == binance.VenueName {
			binanceStatus = &snap.VenueStatuses[i]
			break
		}
	}
	if binanceStatus == nil || binanceStatus.OK || binanceStatus.Error == "" {
		t.Fatalf("binance status should record failure: %+v", binanceStatus)
	}
}

func TestCrossVenueScannerAnnotatesSnapshotLineageAndMetrics(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	nextFunding := now.Add(4 * time.Hour)
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			Enabled:                true,
			MinSpreadAPR:           0.01,
			TopN:                   5,
			SpreadPersistenceScans: 1,
		},
		execCfg: CrossVenueExecutorConfig{
			MaxPositionUSDT:       60,
			MaxEffectiveLeverage:  2,
			MarginSafetyFactor:    1.5,
			EVExpectedSlippageBps: 3,
		},
		recordDir:      dir,
		nowFn:          func() time.Time { return now },
		logger:         log.New(&bytes.Buffer{}, "", 0),
		fundingHistory: make(map[string]map[string][]float64),
		venues: map[string]venue.Venue{
			binance.VenueName: &fakeFundingVenue{name: binance.VenueName, snaps: []venue.FundingSnapshot{{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: nextFunding}}},
			okx.VenueName:     &fakeFundingVenue{name: okx.VenueName, snaps: []venue.FundingSnapshot{{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding}}},
		},
		fetchVenue: defaultFetchVenueSnapshot,
	}
	s.scanOnce(context.Background())
	snap := s.Snapshot()
	if snap.SnapshotID == "" {
		t.Fatal("snapshot_id should be populated")
	}
	if len(snap.Opportunities) != 1 {
		t.Fatalf("expected 1 opportunity, got %d", len(snap.Opportunities))
	}
	opp := snap.Opportunities[0]
	if opp.SnapshotID != snap.SnapshotID {
		t.Fatalf("opp snapshot_id=%q want %q", opp.SnapshotID, snap.SnapshotID)
	}
	if opp.ComparableWindowHours <= 0 {
		t.Fatalf("opp comparable window not populated: %+v", opp)
	}
	if math.Abs(opp.RankScore-opp.NetEV) > 1e-9 {
		t.Fatalf("RankScore should equal NetEV, got rank=%.6f net=%.6f opp=%+v", opp.RankScore, opp.NetEV, opp)
	}
}

func TestCrossVenueScannerAlertCooldown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	clock := now
	var buf bytes.Buffer
	s := &CrossVenueScanner{
		cfg:       CrossVenueScannerConfig{AlertSpreadAPR: 0.50},
		logger:    log.New(&buf, "", 0),
		nowFn:     func() time.Time { return clock },
		lastAlert: make(map[string]time.Time),
		venues: map[string]venue.Venue{
			bybit.VenueName: &fakeFundingVenue{name: bybit.VenueName},
			okx.VenueName:   &fakeFundingVenue{name: okx.VenueName},
		},
	}
	opp := CrossVenueOpportunity{
		Symbol:    "HOME-USDT",
		HighVenue: bybit.VenueName,
		LowVenue:  okx.VenueName,
		SpreadAPR: 1.0,
	}
	s.maybeAlert(opp)
	s.maybeAlert(opp)
	if strings.Count(buf.String(), "[跨所扫描] 价差告警") != 1 {
		t.Fatalf("cooldown should suppress second log: buf=%q", buf.String())
	}
	clock = clock.Add(5 * time.Hour)
	s.maybeAlert(opp)
	if strings.Count(buf.String(), "[跨所扫描] 价差告警") != 2 {
		t.Fatalf("after cooldown should log again: buf=%q", buf.String())
	}
}

type alertSinkFunc func(context.Context, Alert) error

func (f alertSinkFunc) Dispatch(ctx context.Context, alert Alert) error { return f(ctx, alert) }
func (f alertSinkFunc) Name() string                                    { return "test" }

func TestHandleCrossVenueFundingAPI(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	scanner := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{MinSpreadAPR: 0.30},
		lastSnapshot: CrossVenueScanSnapshot{
			ScannedAt: now,
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "HOME-USDT", SpreadAPR: 1.2, HighVenue: bybit.VenueName, LowVenue: okx.VenueName},
			},
			OpportunityCount: 1,
		},
	}
	orch := NewOrchestrator()
	primaryRT, err := buildVenueRuntime(context.Background(), DefaultEngineConfig(), binance.VenueName, nil)
	if err != nil {
		t.Fatalf("buildVenueRuntime: %v", err)
	}
	orch.Register(primaryRT)
	dashboard, err := NewDashboardServer("127.0.0.1:0", orch, nil)
	if err != nil {
		t.Fatalf("NewDashboardServer: %v", err)
	}
	dashboard.SetCrossVenueScanner(scanner)

	req := httptest.NewRequest(http.MethodGet, "/api/cross-venue/funding", nil)
	rec := httptest.NewRecorder()
	dashboard.handleCrossVenueFundingAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload CrossVenueScanSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(payload.Opportunities) != 1 || payload.Opportunities[0].Symbol != "HOME-USDT" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func logDefaultForTest(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(testLogWriter{t: t}, "", 0)
}

type testLogWriter struct{ t *testing.T }

func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// 编译期断言 fakeFundingVenue 满足 venue.Venue。
var _ venue.Venue = (*fakeFundingVenue)(nil)

// TestCrossVenueScannerConcurrentFetch 四所并发拉取互不阻塞(单所慢/失败不影响其它所)。
func TestCrossVenueScannerConcurrentFetch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var mu sync.Mutex
	order := make([]string, 0, 4)
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	nextFunding := time.Now().Add(2 * time.Hour)
	slow := &fakeFundingVenue{
		name:  okx.VenueName,
		delay: 50 * time.Millisecond,
		snaps: []venue.FundingSnapshot{{Symbol: "Y-USDT", LastFundingRate: 0.0002, IntervalHours: 8, NextFundingTime: nextFunding}},
	}
	fast := func(name string, rate float64) *fakeFundingVenue {
		return &fakeFundingVenue{
			name: name,
			snaps: []venue.FundingSnapshot{{
				Symbol: "Y-USDT", LastFundingRate: rate, IntervalHours: 8, NextFundingTime: nextFunding,
			}},
		}
	}
	s := &CrossVenueScanner{
		cfg:       CrossVenueScannerConfig{MinSpreadAPR: 0.01, TopN: 5},
		recordDir: dir,
		venues: map[string]venue.Venue{
			binance.VenueName: fast(binance.VenueName, 0.0001),
			bybit.VenueName:   fast(bybit.VenueName, 0.0002),
			okx.VenueName:     slow,
			gate.VenueName:    &fakeFundingVenue{name: gate.VenueName, err: errors.New("down")},
		},
		logger: logDefaultForTest(t),
		nowFn:  func() time.Time { return time.Now() },
		fetchVenue: func(ctx context.Context, name string, adp venue.Venue) ([]venue.FundingSnapshot, error) {
			record(name)
			return defaultFetchVenueSnapshot(ctx, name, adp)
		},
	}
	s.scanOnce(context.Background())
	if len(s.Snapshot().Opportunities) == 0 {
		t.Fatal("expected opportunities from bybit/okx/binance")
	}
}

func TestCrossVenueScannerPopulatesFundingSpikeThreshold(t *testing.T) {
	now := time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)
	nextFunding := now.Add(4 * time.Hour)
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			Enabled:                true,
			MinSpreadAPR:           0.01,
			SpreadPersistenceScans: 1,
		},
		execCfg: CrossVenueExecutorConfig{MaxPositionUSDT: 60},
		nowFn:   func() time.Time { return now },
		logger:  log.New(&bytes.Buffer{}, "", 0),
		fundingHistory: map[string]map[string][]float64{
			bybit.VenueName: {
				"BTC-USDT": {0.0001, 0.0003, 0.0002, 0.0004},
			},
		},
		venues: map[string]venue.Venue{
			bybit.VenueName: &fakeFundingVenue{name: bybit.VenueName, snaps: []venue.FundingSnapshot{
				{Symbol: "BTC-USDT", LastFundingRate: 0.0005, IntervalHours: 8, NextFundingTime: nextFunding},
			}},
			okx.VenueName: &fakeFundingVenue{name: okx.VenueName, snaps: []venue.FundingSnapshot{
				{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
			}},
		},
		fetchVenue: defaultFetchVenueSnapshot,
	}
	s.scanOnce(context.Background())
	entry := s.Snapshot().FundingByVenue[bybit.VenueName]["BTC-USDT"]
	wantP90 := absRatePercentile([]float64{0.0001, 0.0003, 0.0002, 0.0004, 0.0005}, 90)
	if math.Abs(entry.SpikeThreshold-wantP90) > 1e-12 {
		t.Fatalf("spike_threshold = %.8f, want %.8f", entry.SpikeThreshold, wantP90)
	}
}

func TestAbsRatePercentileRequiresTwoSamples(t *testing.T) {
	if got := absRatePercentile([]float64{0.001}, 90); got != 0 {
		t.Fatalf("single sample p90 = %.8f, want 0", got)
	}
}

func TestSelectNegativeFundingProbeSymbols(t *testing.T) {
	t.Parallel()
	funding := map[string]map[string]CrossVenueFundingEntry{
		binance.VenueName: {
			"AAA-USDT": {Rate: -0.003},
			"BBB-USDT": {Rate: 0.001},
			"CCC-USDT": {Rate: -0.001},
		},
		bybit.VenueName: {
			"AAA-USDT": {Rate: -0.002},
			"CCC-USDT": {Rate: -0.004},
		},
	}
	got := selectNegativeFundingProbeSymbols(funding, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != "CCC-USDT" || got[1] != "AAA-USDT" {
		t.Fatalf("order = %#v, want [CCC-USDT AAA-USDT]", got)
	}
}

func TestCrossVenueBorrowProbeCacheTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	var cache crossVenueBorrowProbeCache
	probe := CrossVenueBorrowProbe{Symbol: "BTC-USDT", Venue: binance.VenueName, Available: true, HourlyRateAPR: 0.5}
	cache.put(binance.VenueName, "BTC", probe, now)
	if _, ok := cache.get(binance.VenueName, "BTC", now.Add(5*time.Minute)); !ok {
		t.Fatal("expected cache hit within TTL")
	}
	if _, ok := cache.get(binance.VenueName, "BTC", now.Add(crossVenueBorrowProbeCacheTTL-time.Second)); !ok {
		t.Fatal("expected cache hit just before TTL")
	}
	if _, ok := cache.get(binance.VenueName, "BTC", now.Add(crossVenueBorrowProbeCacheTTL)); ok {
		t.Fatal("expected cache miss at TTL boundary")
	}
	valid := cache.allValid(now.Add(crossVenueBorrowProbeCacheTTL + time.Second))
	if len(valid) != 0 {
		t.Fatalf("allValid after expiry = %d, want 0", len(valid))
	}
}

type fakeBorrowQuoteVenue struct {
	fakeFundingVenue
	quotes map[string]venue.BorrowQuote
	calls  int
}

func (f *fakeBorrowQuoteVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{SupportsBorrowQuote: true}
}

func (f *fakeBorrowQuoteVenue) FetchBorrowQuote(_ context.Context, asset string) (venue.BorrowQuote, error) {
	f.calls++
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if q, ok := f.quotes[asset]; ok {
		return q, nil
	}
	return venue.BorrowQuote{Asset: asset, Available: false}, nil
}

func TestCrossVenueScannerProbesNegativeFundingSymbols(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	borrowVenue := &fakeBorrowQuoteVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		quotes: map[string]venue.BorrowQuote{
			"NEG": {Asset: "NEG", Available: true, MaxBorrowable: 500, HourlyRate: 0.0001},
		},
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			Enabled:                   true,
			BorrowProbeEnabled:        true,
			BorrowProbeTopN:           1,
			BorrowProbeFundingTopK:    5,
			BorrowProbeMaxLivePerScan: 10,
		},
		recordDir: dir,
		logger:    log.New(&bytes.Buffer{}, "", 0),
		nowFn:     func() time.Time { return now },
		sleepFn:   func(time.Duration) {},
		venues: map[string]venue.Venue{
			binance.VenueName: borrowVenue,
		},
		fetchVenue: func(_ context.Context, name string, adp venue.Venue) ([]venue.FundingSnapshot, error) {
			if name == binance.VenueName {
				return []venue.FundingSnapshot{
					{Symbol: "SPREAD-USDT", LastFundingRate: 0.01, IntervalHours: 8, NextFundingTime: nextFunding},
					{Symbol: "NEG-USDT", LastFundingRate: -0.005, IntervalHours: 8, NextFundingTime: nextFunding},
				}, nil
			}
			return []venue.FundingSnapshot{
				{Symbol: "SPREAD-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
				{Symbol: "NEG-USDT", LastFundingRate: -0.004, IntervalHours: 8, NextFundingTime: nextFunding},
			}, nil
		},
	}
	s.scanOnce(context.Background())
	snap := s.Snapshot()
	if len(snap.BorrowProbes) == 0 {
		t.Fatal("expected borrow probes")
	}
	daily, limits := rotationBorrowMapsFromProbes(snap.BorrowProbes)
	if daily["NEG"] <= 0 {
		t.Fatalf("NEG daily rate = %v, want >0", daily["NEG"])
	}
	if limits["NEG"] != 500 {
		t.Fatalf("NEG limit = %v, want 500", limits["NEG"])
	}
}

func TestCrossVenueScannerBorrowProbeRespectsLiveBudgetAndCache(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	borrowVenue := &fakeBorrowQuoteVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		quotes: map[string]venue.BorrowQuote{
			"A": {Asset: "A", Available: true, MaxBorrowable: 1, HourlyRate: 0.0001},
			"B": {Asset: "B", Available: true, MaxBorrowable: 2, HourlyRate: 0.0001},
			"C": {Asset: "C", Available: true, MaxBorrowable: 3, HourlyRate: 0.0001},
		},
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			BorrowProbeFundingTopK:    3,
			BorrowProbeMaxLivePerScan: 1,
		},
		logger:  log.New(&bytes.Buffer{}, "", 0),
		nowFn:   func() time.Time { return now },
		sleepFn: func(time.Duration) {},
		venues:  map[string]venue.Venue{binance.VenueName: borrowVenue},
	}
	funding := map[string]map[string]CrossVenueFundingEntry{
		binance.VenueName: {
			"A-USDT": {Rate: -0.003},
			"B-USDT": {Rate: -0.002},
			"C-USDT": {Rate: -0.001},
		},
	}
	probes1 := s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if borrowVenue.calls != 1 {
		t.Fatalf("first scan live calls = %d, want 1", borrowVenue.calls)
	}
	if len(probes1) != 1 {
		t.Fatalf("first scan probes = %d, want 1", len(probes1))
	}
	probes2 := s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if borrowVenue.calls != 2 {
		t.Fatalf("second scan live calls = %d, want 2", borrowVenue.calls)
	}
	if len(probes2) != 2 {
		t.Fatalf("second scan probes = %d, want 2", len(probes2))
	}
	probes3 := s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if borrowVenue.calls != 3 {
		t.Fatalf("third scan live calls = %d, want 3", borrowVenue.calls)
	}
	if len(probes3) != 3 {
		t.Fatalf("third scan probes = %d, want 3", len(probes3))
	}
	probes4 := s.probeBorrowForOpportunities(context.Background(), now, nil, funding)
	if borrowVenue.calls != 3 {
		t.Fatalf("fourth scan live calls = %d, want 3 (all cached)", borrowVenue.calls)
	}
	if len(probes4) != 3 {
		t.Fatalf("fourth scan probes = %d, want 3", len(probes4))
	}
}

func TestCrossVenueScannerComputeOpportunitiesLiquidityAdaptiveNotional(t *testing.T) {
	thinDepth := venue.Depth{
		Asks: []venue.DepthLevel{{Price: 100, Qty: 0.05}},
		Bids: []venue.DepthLevel{{Price: 100, Qty: 0.05}},
	}
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{MinSpreadAPR: 0.01, TopN: 5},
		execCfg: CrossVenueExecutorConfig{
			MaxPositionUSDT:       150,
			SizingMaxSlippageBps:  20,
			DefaultTakerFeeBps:    1,
			EVExpectedSlippageBps: 1,
		},
		venues: map[string]venue.Venue{
			binance.VenueName: &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}, depth: thinDepth},
			bybit.VenueName:   &scannerPerpDepthVenue{fakeFundingVenue: fakeFundingVenue{name: bybit.VenueName}, depth: thinDepth},
		},
		nowFn:          func() time.Time { return now },
		fundingHistory: make(map[string]map[string][]float64),
	}
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: nextFunding},
		},
		bybit.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
		},
	}
	s.recordFundingHistory(byVenue)
	opps := s.computeCrossVenueOpportunities(context.Background(), byVenue)
	if len(opps) != 1 {
		t.Fatalf("opps = %d, want 1", len(opps))
	}
	raw := crossVenuePerpPerpScanTradableNotional(context.Background(), s.execCfg, s.venues, opps[0].HighVenue, opps[0].LowVenue, opps[0].Symbol)
	if raw <= 0 {
		t.Fatalf("raw TradableNotional=%.2f want >0 on thin depth", raw)
	}
	if raw >= 150 {
		t.Fatalf("raw TradableNotional=%.2f want <150 (liquidity capped, not full max_position)", raw)
	}
}

func TestCrossVenueScannerComputeOpportunitiesNoDepthConservative(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	nextFunding := now.Add(2 * time.Hour)
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{MinSpreadAPR: 0.01, TopN: 5},
		execCfg: CrossVenueExecutorConfig{
			MaxPositionUSDT:       150,
			SizingMaxSlippageBps:  20,
			DefaultTakerFeeBps:    1,
			EVExpectedSlippageBps: 1,
		},
		venues: map[string]venue.Venue{
			binance.VenueName: &fakeFundingVenue{name: binance.VenueName},
			bybit.VenueName:   &fakeFundingVenue{name: bybit.VenueName},
		},
		nowFn:          func() time.Time { return now },
		fundingHistory: make(map[string]map[string][]float64),
	}
	byVenue := map[string][]venue.FundingSnapshot{
		binance.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.001, IntervalHours: 8, NextFundingTime: nextFunding},
		},
		bybit.VenueName: {
			{Symbol: "BTC-USDT", LastFundingRate: 0.0001, IntervalHours: 8, NextFundingTime: nextFunding},
		},
	}
	s.recordFundingHistory(byVenue)
	opps := s.computeCrossVenueOpportunities(context.Background(), byVenue)
	if len(opps) != 1 {
		t.Fatalf("opps = %d, want 1", len(opps))
	}
	// finalize 前应为 150×0.25;排名后可能按 SizeMultiplier 缩放,但须显著低于满额 150。
	raw := crossVenuePerpPerpScanTradableNotional(context.Background(), s.execCfg, s.venues, opps[0].HighVenue, opps[0].LowVenue, opps[0].Symbol)
	if math.Abs(raw-37.5) > 1e-9 {
		t.Fatalf("no-depth raw TradableNotional=%.2f want 37.5 (150×0.25)", raw)
	}
	if opps[0].TradableNotional >= 150 {
		t.Fatalf("ranked TradableNotional=%.2f want <150 when net_ev>0 path applies", opps[0].TradableNotional)
	}
}

type scannerPerpDepthVenue struct {
	fakeFundingVenue
	depth venue.Depth
}

func (v *scannerPerpDepthVenue) FetchDepth(context.Context, venue.Market, venue.Symbol, int) (venue.Depth, error) {
	return v.depth, nil
}
