package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

func TestCrossVenueControlStatePersistence(t *testing.T) {
	dir := t.TempDir()
	if enabled, err := loadCrossVenueControlState(dir); err != nil || enabled {
		t.Fatalf("缺省应为 false: enabled=%v err=%v", enabled, err)
	}
	if err := saveCrossVenueControlState(dir, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	enabled, err := loadCrossVenueControlState(dir)
	if err != nil || !enabled {
		t.Fatalf("读回应为 true: enabled=%v err=%v", enabled, err)
	}
	if err := saveCrossVenueControlState(dir, false); err != nil {
		t.Fatalf("save false: %v", err)
	}
	enabled, err = loadCrossVenueControlState(dir)
	if err != nil || enabled {
		t.Fatalf("读回应为 false: enabled=%v err=%v", enabled, err)
	}
}

func TestCrossVenueExecutorReloadsControlStateOnConstruct(t *testing.T) {
	dir := t.TempDir()
	if err := saveCrossVenueControlState(dir, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	e, err := NewCrossVenueExecutor(cfg, nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewCrossVenueExecutor: %v", err)
	}
	if !e.OpeningsEnabled() {
		t.Fatal("构造时应从 control.json 读回 openings_enabled=true")
	}
}

func TestCrossVenueOpeningsDisabledSkipsEvaluateNewOpens(t *testing.T) {
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
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	e.openingsEnabled.Store(false)
	e.tick(context.Background(), true)
	if atomic.LoadInt32(high.placeCount) != 0 || atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatalf("开仓开关 OFF 时不应 evaluateNewOpens: high=%d low=%d", atomic.LoadInt32(high.placeCount), atomic.LoadInt32(low.placeCount))
	}
}

func TestCrossVenueOpeningsDisabledStillManagesPositions(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg: cfg,
		scanner: &CrossVenueScanner{lastSnapshot: CrossVenueScanSnapshot{
			Opportunities: []CrossVenueOpportunity{
				{Symbol: "ETH-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
			},
		}},
		venues:    map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:     mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger:    testCrossVenueLogger(),
		nowFn:     func() time.Time { return now },
		recordDir: cfg.NetworkConfig.RecordDir,
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpen, HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "mgmt",
	}
	e.positions = []CrossVenuePosition{pos}
	e.openingsEnabled.Store(false)
	e.tick(context.Background(), true)
	if atomic.LoadInt32(high.placeCount) < 1 {
		t.Fatal("开仓开关 OFF 时 manageOpenPositions 仍应运行(orphan 平仓)")
	}
	if atomic.LoadInt32(low.placeCount) != 0 {
		t.Fatal("不应因 scanner 机会而开新仓")
	}
}

func newStrategyTestOrchestrator(t *testing.T) (*Orchestrator, map[string]*RuntimeController) {
	t.Helper()
	o := NewOrchestrator()
	controllers := make(map[string]*RuntimeController)
	for _, name := range strategyControlVenues {
		store, err := NewReportStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewReportStore(%s): %v", name, err)
		}
		c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
		if err != nil {
			t.Fatalf("NewRuntimeController(%s): %v", name, err)
		}
		c.venueName = name
		controllers[name] = c
		o.Register(&venueRuntime{name: name, controller: c, store: store})
	}
	return o, controllers
}

func TestStrategyCapitalParallelPerVenueResults(t *testing.T) {
	o, ctrls := newStrategyTestOrchestrator(t)
	// gate: 可调不足
	orig := venueCapitalManagerFactory
	venueCapitalManagerFactory = func(name, sp string) (venue.CapitalManagement, venue.Capabilities, error) {
		switch name {
		case bybit.VenueName:
			return &fakeCapMgr{uni: 0, fund: 200}, venue.Capabilities{}, nil
		case okx.VenueName:
			return &fakeOKXCapMgr{trading: 0, funding: 200}, venue.Capabilities{}, nil
		case gate.VenueName:
			return &fakeCapMgr{uni: 0, fund: 50}, venue.Capabilities{}, nil
		default:
			return nil, venue.Capabilities{}, nil
		}
	}
	defer func() { venueCapitalManagerFactory = orig }()

	// binance: 运行中拒绝
	ctrls[binance.VenueName].mu.Lock()
	ctrls[binance.VenueName].status.Running = true
	ctrls[binance.VenueName].mu.Unlock()

	s := &DashboardServer{orchestrator: o}
	resp := s.executeStrategyCapital(StrategyCapitalRequest{TargetUSDT: 100})
	if len(resp.Results) != 4 {
		t.Fatalf("应返回 4 所结果, got %d", len(resp.Results))
	}
	byResult := map[string]StrategyVenueCapitalResult{}
	for _, r := range resp.Results {
		byResult[r.Venue] = r
	}
	if !byResult[bybit.VenueName].Success || byResult[bybit.VenueName].After == nil {
		t.Fatalf("bybit 应成功: %+v", byResult[bybit.VenueName])
	}
	if !byResult[okx.VenueName].Success {
		t.Fatalf("okx 应成功: %+v", byResult[okx.VenueName])
	}
	if byResult[gate.VenueName].Success || !strings.Contains(byResult[gate.VenueName].Error, "不足") {
		t.Fatalf("gate 应可调不足失败: %+v", byResult[gate.VenueName])
	}
	if byResult[binance.VenueName].Success || !strings.Contains(byResult[binance.VenueName].Error, "运行中") {
		t.Fatalf("binance 运行中应拒绝: %+v", byResult[binance.VenueName])
	}
}

func TestStrategyStartStopOrchestration(t *testing.T) {
	o, ctrls := newStrategyTestOrchestrator(t)
	dir := t.TempDir()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	exec, err := NewCrossVenueExecutor(cfg, o, nil, "", nil)
	if err != nil {
		t.Fatalf("NewCrossVenueExecutor: %v", err)
	}
	s := &DashboardServer{orchestrator: o, crossVenueExecutor: exec}

	origFactory := venueFundingVenueFactory
	venueFundingVenueFactory = func(_, _ string) (venue.Venue, error) {
		return &fakeVenueFundingTrader{}, nil
	}
	defer func() { venueFundingVenueFactory = origFactory }()
	origBybit := bybitVenueFactory
	bybitVenueFactory = func(_ string) (bybitFullVenue, error) {
		return &fakeBybitVenue{
			fakeBybitMarket: &fakeBybitMarket{},
			fakeBybitTrader: newFakeBybitTrader(),
		}, nil
	}
	defer func() { bybitVenueFactory = origBybit }()

	startResp := s.executeStrategyStart(StrategyStartRequest{SecretPath: "secret.yaml"})
	if len(startResp.Steps) < 5 {
		t.Fatalf("start steps: %+v", startResp.Steps)
	}
	if startResp.Steps[0].Step != "bybit_start" || !startResp.Steps[0].Success {
		t.Fatalf("bybit_start 应成功: %+v", startResp.Steps[0])
	}
	if startResp.Steps[1].Step != "cross_venue_openings_on" || !startResp.Steps[1].Success {
		t.Fatalf("openings_on 应成功: %+v", startResp.Steps[1])
	}
	for _, want := range []string{"funding_start_okx", "funding_start_gate"} {
		found := false
		for _, st := range startResp.Steps {
			if st.Step == want && st.Success {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少成功的 %s 步骤: %+v", want, startResp.Steps)
		}
	}
	var binanceStart *StrategyStepResult
	for i := range startResp.Steps {
		if startResp.Steps[i].Step == "binance_start" {
			binanceStart = &startResp.Steps[i]
			break
		}
	}
	if binanceStart == nil {
		t.Fatalf("缺少 binance_start 步骤: %+v", startResp.Steps)
	}
	if !binanceStart.Skipped {
		t.Fatalf("binance_start 应默认跳过(币安经跨所参与): %+v", *binanceStart)
	}
	if !binanceStart.Success {
		t.Fatalf("binance_start 跳过时应标记 success: %+v", *binanceStart)
	}
	if ctrls[binance.VenueName].Status().Running {
		t.Fatal("binance_start 跳过时控制器不应自动运行")
	}
	if !exec.OpeningsEnabled() {
		t.Fatal("启动后跨所开仓开关应为 ON")
	}
	if !ctrls[bybit.VenueName].Status().Running {
		t.Fatal("Bybit 控制器应为运行中")
	}

	// 再次启动应跳过 bybit
	startAgain := s.executeStrategyStart(StrategyStartRequest{})
	if !startAgain.Steps[0].Skipped {
		t.Fatalf("已运行 Bybit 应 skipped: %+v", startAgain.Steps[0])
	}

	stopResp := s.executeStrategyStop()
	if len(stopResp.Steps) < 2 {
		t.Fatalf("stop steps: %+v", stopResp.Steps)
	}
	if stopResp.Steps[0].Step != "bybit_stop" || !stopResp.Steps[0].Success {
		t.Fatalf("bybit_stop 应成功: %+v", stopResp.Steps[0])
	}
	if stopResp.Steps[1].Step != "binance_stop" {
		t.Fatalf("第二步应为 binance_stop: %+v", stopResp.Steps[1])
	}
	if !stopResp.Steps[1].Skipped && !stopResp.Steps[1].Success {
		t.Fatalf("binance_stop 应成功或跳过: %+v", stopResp.Steps[1])
	}
	if stopResp.Steps[2].Step != "cross_venue_openings_off" || !stopResp.Steps[2].Success {
		t.Fatalf("openings_off 应成功: %+v", stopResp.Steps[2])
	}
	if exec.OpeningsEnabled() {
		t.Fatal("停止后跨所开仓开关应为 OFF")
	}
	deadline := time.Now().Add(2 * time.Second)
	for ctrls[bybit.VenueName].Status().Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if ctrls[bybit.VenueName].Status().Running {
		t.Fatal("Bybit 停止后不应再 running")
	}
}

func TestStrategyAPIAuthRejectsMissingToken(t *testing.T) {
	o, _ := newStrategyTestOrchestrator(t)
	s := &DashboardServer{orchestrator: o, controlToken: "test-secret-token"}

	body, _ := json.Marshal(StrategyCapitalRequest{TargetUSDT: 100})
	for _, path := range []string{"/api/strategy/capital", "/api/strategy/start", "/api/strategy/stop"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		switch path {
		case "/api/strategy/capital":
			s.handleStrategyCapitalAPI(rec, req)
		case "/api/strategy/start":
			s.handleStrategyStartAPI(rec, req)
		case "/api/strategy/stop":
			s.handleStrategyStopAPI(rec, req)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 无令牌应 403, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestStrategyStatusAPI(t *testing.T) {
	o, ctrls := newStrategyTestOrchestrator(t)
	dir := t.TempDir()
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = dir
	exec, err := NewCrossVenueExecutor(cfg, o, nil, "", nil)
	if err != nil {
		t.Fatalf("NewCrossVenueExecutor: %v", err)
	}
	_ = exec.SetOpeningsEnabled(true)
	ctrls[bybit.VenueName].mu.Lock()
	ctrls[bybit.VenueName].status.Running = true
	ctrls[bybit.VenueName].status.Mode = "live"
	ctrls[bybit.VenueName].mu.Unlock()

	s := &DashboardServer{orchestrator: o, crossVenueExecutor: exec}
	req := httptest.NewRequest(http.MethodGet, "/api/strategy/status", nil)
	rec := httptest.NewRecorder()
	s.handleStrategyStatusAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var st StrategyStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !st.Bybit.Running || !st.CrossVenue.OpeningsEnabled {
		t.Fatalf("unexpected status: %+v", st)
	}
}

// 编译期引用 gate venue 名(策略四所包含 gate)。
var _ = gate.VenueName
