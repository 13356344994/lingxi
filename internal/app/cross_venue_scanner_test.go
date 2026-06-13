package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
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
	name   string
	snaps  []venue.FundingSnapshot
	err    error
	delay  time.Duration
	called int
}

func (f *fakeFundingVenue) Name() string { return f.name }
func (f *fakeFundingVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{}
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
	return nil, venue.ErrUnsupported
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
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	failVenue := &fakeFundingVenue{name: binance.VenueName, err: errors.New("boom")}
	okVenue := &fakeFundingVenue{
		name: bybit.VenueName,
		snaps: []venue.FundingSnapshot{
			{Symbol: "X-USDT", LastFundingRate: 0.01, IntervalHours: 1},
		},
	}
	okVenue2 := &fakeFundingVenue{
		name: okx.VenueName,
		snaps: []venue.FundingSnapshot{
			{Symbol: "X-USDT", LastFundingRate: 0.001, IntervalHours: 8},
		},
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{
			Enabled:      true,
			MinSpreadAPR: 0.01,
			TopN:         10,
		},
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
	var mu sync.Mutex
	order := make([]string, 0, 4)
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}
	slow := &fakeFundingVenue{
		name:  okx.VenueName,
		delay: 50 * time.Millisecond,
		snaps: []venue.FundingSnapshot{{Symbol: "Y-USDT", LastFundingRate: 0.002, IntervalHours: 4}},
	}
	fast := func(name string, rate float64) *fakeFundingVenue {
		return &fakeFundingVenue{
			name:  name,
			snaps: []venue.FundingSnapshot{{Symbol: "Y-USDT", LastFundingRate: rate, IntervalHours: 8}},
		}
	}
	s := &CrossVenueScanner{
		cfg: CrossVenueScannerConfig{MinSpreadAPR: 0.01, TopN: 5},
		venues: map[string]venue.Venue{
			binance.VenueName: fast(binance.VenueName, 0.001),
			bybit.VenueName:   fast(bybit.VenueName, 0.003),
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
