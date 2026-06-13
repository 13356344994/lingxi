package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

type recordingAlertSink struct {
	mu     sync.Mutex
	alerts []Alert
}

func (s *recordingAlertSink) Dispatch(_ context.Context, alert Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, alert)
	return nil
}

func (s *recordingAlertSink) Name() string { return "recording" }

func (s *recordingAlertSink) titles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.alerts))
	for i, a := range s.alerts {
		out[i] = a.Title
	}
	return out
}

func (s *recordingAlertSink) waitTitles(t *testing.T, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.titles()) >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.titles()
}

func newCrossVenueHealthTestOrchestrator(t *testing.T, statuses map[string]RuntimeTaskStatus) *Orchestrator {
	t.Helper()
	o := NewOrchestrator()
	for _, name := range crossVenueExecutorVenues {
		st, ok := statuses[name]
		if !ok {
			st = RuntimeTaskStatus{}
		}
		store, err := NewReportStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewReportStore(%s): %v", name, err)
		}
		ctrl, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
		if err != nil {
			t.Fatalf("NewRuntimeController(%s): %v", name, err)
		}
		ctrl.venueName = name
		ctrl.mu.Lock()
		ctrl.status = st
		ctrl.mu.Unlock()
		o.Register(&venueRuntime{name: name, controller: ctrl, store: store})
	}
	return o
}

func newHealthGateTestExecutor(t *testing.T, orch *Orchestrator, sink *recordingAlertSink) (*CrossVenueExecutor, *crossVenueTestVenue, *crossVenueTestVenue) {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg: cfg,
		scanner: &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
			},
		}},
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		orch:      orch,
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	e.openingsEnabled.Store(true)
	if sink != nil {
		e.alerts = &AlertDispatcher{
			sinks:    []AlertSink{sink},
			minRank:  severityRank(EventLevelInfo),
			timeout:  time.Second,
			now:      time.Now,
			lastSent: map[string]time.Time{},
		}
	}
	return e, high, low
}

func TestFirstUnhealthyTradeableVenueRunningFailed(t *testing.T) {
	orch := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		bybit.VenueName: {Running: true, HealthStatus: "failed"},
	})
	e := &CrossVenueExecutor{orch: orch}
	if got := e.firstUnhealthyTradeableVenue(); got != bybit.VenueName {
		t.Fatalf("got %q want %q", got, bybit.VenueName)
	}
}

func TestFirstUnhealthyTradeableVenueCircuitBreaker(t *testing.T) {
	orch := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		okx.VenueName: {Running: false, CircuitBreakerTripped: true},
	})
	e := &CrossVenueExecutor{orch: orch}
	if got := e.firstUnhealthyTradeableVenue(); got != okx.VenueName {
		t.Fatalf("got %q want %q", got, okx.VenueName)
	}
}

func TestFirstUnhealthyTradeableVenueIdleNotUnhealthy(t *testing.T) {
	orch := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		"binance":       {Running: false, HealthStatus: "failed"},
		bybit.VenueName: {Running: false, HealthStatus: "healthy"},
		okx.VenueName:   {Running: true, HealthStatus: "healthy"},
	})
	e := &CrossVenueExecutor{orch: orch}
	if got := e.firstUnhealthyTradeableVenue(); got != "" {
		t.Fatalf("idle/failed 不应触发, got %q", got)
	}
}

func TestCrossVenueHealthGateSkipsOpensStillManages(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	orch := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		okx.VenueName: {Running: true, HealthStatus: "failed"},
	})
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg: cfg,
		scanner: &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "ETH-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
			},
		}},
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		orch:   orch,
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	e.positions = []CrossVenuePosition{{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "health",
	}}
	e.openingsEnabled.Store(true)
	e.tick(context.Background(), true)
	if atomic.LoadInt32(high.placeCount) < 1 {
		t.Fatal("健康闸 ON 时 manageOpenPositions 仍应运行")
	}
	if atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("健康闸 ON 时不应 evaluateNewOpens 开新仓")
	}
}

func TestCrossVenueHealthGateAlertOnlyOnTransition(t *testing.T) {
	sink := &recordingAlertSink{}
	orch := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		bybit.VenueName: {Running: true, HealthStatus: "healthy"},
	})
	e, _, _ := newHealthGateTestExecutor(t, orch, sink)

	e.syncHealthHaltAlerts("")
	e.syncHealthHaltAlerts("")
	if len(sink.waitTitles(t, 0)) != 0 {
		t.Fatalf("稳定健康态不应告警: %v", sink.titles())
	}

	e.syncHealthHaltAlerts(bybit.VenueName)
	e.syncHealthHaltAlerts(bybit.VenueName)
	titles := sink.waitTitles(t, 1)
	if len(titles) != 1 || titles[0] != "跨所健康闸" {
		t.Fatalf("进入 halt 应告警一次, got %v", titles)
	}

	e.syncHealthHaltAlerts("")
	e.syncHealthHaltAlerts("")
	titles = sink.waitTitles(t, 2)
	if len(titles) != 2 {
		t.Fatalf("恢复应再告警一次, got %d alerts", len(titles))
	}

	// 不健康时 tick 跳过开仓。
	orch2 := newCrossVenueHealthTestOrchestrator(t, map[string]RuntimeTaskStatus{
		bybit.VenueName: {Running: true, HealthStatus: "failed"},
	})
	e2, high, low := newHealthGateTestExecutor(t, orch2, nil)
	e2.tick(context.Background(), true)
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("不健康时不应开仓: high=%d low=%d", *high.placeCount, *low.placeCount)
	}
}

func TestHandleCrossVenueOpeningsAPI(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	exec, err := NewCrossVenueExecutor(cfg, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewCrossVenueExecutor: %v", err)
	}
	dashboard := &DashboardServer{crossVenueExecutor: exec, controlToken: "test-secret"}
	body := strings.NewReader(`{"enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/crossvenue/openings", body)
	req.Header.Set("X-Dashboard-Token", "bad-token")
	rec := httptest.NewRecorder()
	dashboard.handleCrossVenueOpeningsAPI(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad token: status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/crossvenue/openings", strings.NewReader(`{"enabled":true}`))
	req.Header.Set("X-Dashboard-Token", "test-secret")
	rec = httptest.NewRecorder()
	dashboard.handleCrossVenueOpeningsAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !exec.OpeningsEnabled() {
		t.Fatal("openings should be enabled")
	}
}
