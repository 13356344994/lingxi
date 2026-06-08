package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/exchange"
)

var errTestRescueFailure = errors.New("rescue 收敛失败(测试)")

// TestScheduleExposureRescueStartsWithRunCtx 钉死 C3：ExposureManager 必须由 engine 统一持有，
// 调度成功后任务进入队列。这里预先触发 startOnce，避免后台 worker 在半初始化引擎上消费任务，
// 让断言聚焦于「调度 -> 入队」的接线契约本身，保持确定性。
func TestScheduleExposureRescueStartsWithRunCtx(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.runCtx = runCtx

	manager := newExposureManager(engine)
	manager.statePath = ""
	manager.startOnce.Do(func() {}) // 抢占 once：后续 Start 成为 no-op，不会启动消费 worker
	engine.exposureManager = manager

	if err := engine.scheduleExposureRescue(context.Background(), exposureRescueTask{
		Phase:  "open_recovery",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}); err != nil {
		t.Fatalf("scheduleExposureRescue() error = %v", err)
	}
	if engine.exposureManager == nil {
		t.Fatal("expected exposure manager to be retained on engine")
	}
	select {
	case queued := <-manager.taskQueue:
		if queued.Symbol != "BTCUSDT" || queued.Phase != "open_recovery" {
			t.Fatalf("unexpected queued task: %+v", queued)
		}
	default:
		t.Fatal("expected rescue task to be enqueued")
	}
}

// TestExposureManagerRunStopsWhenContextCanceled 钉死 C3：关停 runCtx 后 worker 必须退出，
// 不能再在后台无限循环恢复。
func TestExposureManagerRunStopsWhenContextCanceled(t *testing.T) {
	manager := &ExposureManager{
		logger:    log.New(io.Discard, "", 0),
		taskQueue: make(chan exposureRescueTask, 1),
		engine: &FundingRateSniperEngine{
			cfg:    DefaultEngineConfig(),
			logger: log.New(io.Discard, "", 0),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected exposure manager run loop to exit when parent ctx is canceled")
	}
}

func TestExposureManagerSubmitDoesNotBlockOnTaskCompletion(t *testing.T) {
	manager := &ExposureManager{
		logger:    log.New(io.Discard, "", 0),
		taskQueue: make(chan exposureRescueTask, 1),
	}

	done := make(chan error, 1)
	task := exposureRescueTask{
		Phase:  "open_recovery",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}
	go func() {
		done <- manager.Submit(context.Background(), task)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected Submit() to return immediately after enqueue")
	}

	select {
	case queued := <-manager.taskQueue:
		if queued.Symbol != task.Symbol || queued.Phase != task.Phase {
			t.Fatalf("unexpected queued task: %+v", queued)
		}
	default:
		t.Fatal("expected task to be enqueued")
	}
}

func TestExposureManagerFlattenSpotResidualUsesSpotReferenceAnchor(t *testing.T) {
	var capturedPrice float64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/order" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		capturedPrice = parseFloat(r.URL.Query().Get("price"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"FILLED","clientOrderId":"exp_sell-1","executedQty":"1","cummulativeQuoteQty":"99.9","origQty":"1","fills":[{"price":"99.9","qty":"1","commission":"0","commissionAsset":"USDT"}]}`))
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL: server.URL,
			APIKey:      "demo-api-key",
			SecretKey:   "demo-secret-key",
			HTTPClient:  server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
		spotTickerCache: NewBookTickerCache(),
	}
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 20
	now := time.Now().UTC()
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "90", AskPrice: "90.1"}, now)

	manager := &ExposureManager{
		engine: engine,
		logger: engine.logger,
	}
	task := exposureRescueTask{
		Phase:              "open_recovery",
		Symbol:             "BTCUSDT",
		Pair:               "BTCUSDT",
		SpotBaseAsset:      "BTC",
		SpotBasePrecision:  3,
		SpotReferencePrice: 100,
	}

	if err := manager.flattenSpotResidual(context.Background(), task, 1, "SELL", "exposure_rescue_sell_spot"); err != nil {
		t.Fatalf("flattenSpotResidual() error = %v", err)
	}
	if capturedPrice < 99.9-1e-9 {
		t.Fatalf("expected anchored rescue price near 99.9, got %.8f", capturedPrice)
	}
}

func TestExposureLockBlocksSymbolUntilRescueReportsCompletion(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	target := &MonitoredTarget{
		Candidate: FundingCandidate{
			Symbol: "BTCUSDT",
			Pair:   "BTCUSDT",
		},
	}
	task := exposureRescueTask{
		Mode:   exposureRescueModeOpen,
		Phase:  "parallel_open_spot_unknown",
		Symbol: "BTCUSDT",
		Pair:   "BTCUSDT",
	}
	engine.lockSymbolForExposure(task)

	filtered := engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target})
	if len(filtered) != 0 {
		t.Fatalf("expected exposure-locked symbol to be skipped, got %+v", filtered)
	}

	manager := &ExposureManager{
		engine: engine,
		logger: engine.logger,
	}
	manager.reportTaskResult(context.Background(), task, nil)

	filtered = engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target})
	if len(filtered) != 1 || filtered[0].Candidate.Symbol != "BTCUSDT" {
		t.Fatalf("expected symbol to be released after rescue completion, got %+v", filtered)
	}
}

// TestExposureManagerFailedRescueRetriesAndKeepsLock 钉死:救援失败但未耗尽重试时,**不**释放 symbol 锁
// (带残腿的币不得被重新开仓),并把尝试次数+1 持久化以便后台/重启重排。
func TestExposureManagerFailedRescueRetriesAndKeepsLock(t *testing.T) {
	recordDir := t.TempDir()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	engine.cfg.NetworkConfig.RecordDir = recordDir
	manager := newExposureManager(engine)
	task := exposureRescueTask{Mode: exposureRescueModeOpen, Phase: "x", Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC"}
	engine.lockSymbolForExposure(task)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.reportTaskResult(ctx, task, errTestRescueFailure)

	target := &MonitoredTarget{Candidate: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT"}}
	if filtered := engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target}); len(filtered) != 0 {
		t.Fatalf("救援未耗尽时 symbol 应保持锁定, got %+v", filtered)
	}
	state, err := loadPersistedExposureRescueState(filepath.Join(recordDir, string(liveExposureRescueStateName)))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Attempts != 1 {
		t.Fatalf("expected persisted retry task attempts=1, got %+v", state.Tasks)
	}
}

// TestExposureManagerExhaustedRescueKeepsLock 钉死:重试耗尽后**保持** symbol 锁定(防止带未收敛残腿的币被重开叠加敞口),
// 任务保留在盘待人工/重启重排;锁死边界由 critical 告警 + 重启介入兜底。
func TestExposureManagerExhaustedRescueKeepsLock(t *testing.T) {
	recordDir := t.TempDir()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	engine.cfg.NetworkConfig.RecordDir = recordDir
	manager := newExposureManager(engine)
	task := exposureRescueTask{Mode: exposureRescueModeOpen, Phase: "x", Symbol: "BTCUSDT", Pair: "BTCUSDT", SpotBaseAsset: "BTC", Attempts: exposureRescueMaxAttempts - 1}
	engine.lockSymbolForExposure(task)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.reportTaskResult(ctx, task, errTestRescueFailure)

	target := &MonitoredTarget{Candidate: FundingCandidate{Symbol: "BTCUSDT", Pair: "BTCUSDT"}}
	if filtered := engine.filterTargetsBySymbolCooldown([]*MonitoredTarget{target}); len(filtered) != 0 {
		t.Fatalf("救援耗尽后 symbol 应保持锁定(不可重开), got %+v", filtered)
	}
}

func TestExposureManagerSubmitPersistsAndClearsPendingTasks(t *testing.T) {
	recordDir := t.TempDir()
	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	engine.cfg.NetworkConfig.RecordDir = recordDir
	manager := newExposureManager(engine)
	task := exposureRescueTask{
		Mode:          exposureRescueModeOpen,
		Phase:         "open_recovery",
		Symbol:        "BTCUSDT",
		Pair:          "BTCUSDT",
		SpotBaseAsset: "BTC",
	}

	if err := manager.Submit(context.Background(), task); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	state, err := loadPersistedExposureRescueState(filepath.Join(recordDir, string(liveExposureRescueStateName)))
	if err != nil {
		t.Fatalf("loadPersistedExposureRescueState() error = %v", err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Symbol != "BTCUSDT" {
		t.Fatalf("expected pending task to be persisted, got %+v", state.Tasks)
	}

	if err := manager.removePendingTask(task); err != nil {
		t.Fatalf("removePendingTask() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(recordDir, string(liveExposureRescueStateName))); !os.IsNotExist(err) {
		t.Fatalf("expected persisted pending task file to be removed, stat err=%v", err)
	}
}

func TestExposureManagerRestorePendingTasksRequeuesPersistedState(t *testing.T) {
	recordDir := t.TempDir()
	statePath := filepath.Join(recordDir, string(liveExposureRescueStateName))
	task := exposureRescueTask{
		Mode:          exposureRescueModeClose,
		Phase:         "close_recovery",
		Symbol:        "ethusdt",
		Pair:          "ethusdt",
		SpotBaseAsset: "eth",
	}
	if err := writePersistedExposureRescueState(statePath, persistedExposureRescueState{Tasks: []exposureRescueTask{task}}); err != nil {
		t.Fatalf("writePersistedExposureRescueState() error = %v", err)
	}

	engine := &FundingRateSniperEngine{
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		portfolioManager: portfolioManager{
			symbolCooldowns: make(map[string]symbolCooldownState),
			exposureLocks:   make(map[string]exposureLockState),
		},
	}
	engine.cfg.NetworkConfig.RecordDir = recordDir
	manager := newExposureManager(engine)

	if err := manager.restorePendingTasks(); err != nil {
		t.Fatalf("restorePendingTasks() error = %v", err)
	}
	select {
	case restored := <-manager.taskQueue:
		if restored.Symbol != "ETHUSDT" || restored.Pair != "ETHUSDT" || restored.SpotBaseAsset != "ETH" {
			t.Fatalf("unexpected restored task: %+v", restored)
		}
	default:
		t.Fatal("expected persisted task to be requeued")
	}
}

func TestExposureManagerDynamicallyFlattensResidualUntilSpotBalanceClears(t *testing.T) {
	var (
		spotAccountReads int32
		spotSellOrders   int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/account":
			read := atomic.AddInt32(&spotAccountReads, 1)
			free := "0"
			switch read {
			case 1:
				free = "1.0"
			case 2:
				free = "0.4"
			}
			_, _ = w.Write([]byte(`{"balances":[{"asset":"BTC","free":"` + free + `","locked":"0"},{"asset":"USDT","free":"100","locked":"0"}]}`))
		case "/fapi/v3/account":
			_, _ = w.Write([]byte(`{"assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"100"}],"positions":[{"symbol":"BTCUSDT","positionAmt":"0"}]}`))
		case "/api/v3/order":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected order method: %s", r.Method)
			}
			atomic.AddInt32(&spotSellOrders, 1)
			qty := r.URL.Query().Get("quantity")
			_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":1,"status":"FILLED","clientOrderId":"exp_sell-1","executedQty":"` + qty + `","cummulativeQuoteQty":"99.9","origQty":"` + qty + `","fills":[{"price":"99.9","qty":"` + qty + `","commission":"0","commissionAsset":"USDT"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	engine := &FundingRateSniperEngine{
		client: exchange.NewClient(exchange.ClientConfig{
			SpotBaseURL:    server.URL,
			FuturesBaseURL: server.URL,
			APIKey:         "demo-api-key",
			SecretKey:      "demo-secret-key",
			HTTPClient:     server.Client(),
		}),
		cfg:    DefaultEngineConfig(),
		logger: log.New(io.Discard, "", 0),
		spotRules: map[string]SymbolTradeRules{
			"BTCUSDT": {StepSize: 0.001, PriceTick: 0.1, MinQty: 0.001, MinNotional: 1},
		},
		spotTickerCache: NewBookTickerCache(),
	}
	engine.cfg.RiskConfig.MaxCycleLossUSDT = 20
	engine.spotTickerCache.UpdateAt(BookTicker{Symbol: "BTCUSDT", BidPrice: "100", AskPrice: "100.1"}, time.Now().UTC())

	manager := &ExposureManager{
		engine: engine,
		logger: engine.logger,
	}
	task := exposureRescueTask{
		Mode:              exposureRescueModeOpen,
		Phase:             "open_recovery",
		Symbol:            "BTCUSDT",
		Pair:              "BTCUSDT",
		SpotBaseAsset:     "BTC",
		SpotBasePrecision: 3,
	}

	if err := manager.flattenOpenExposure(context.Background(), task, 1, 0); err != nil {
		t.Fatalf("flattenOpenExposure() error = %v", err)
	}
	if atomic.LoadInt32(&spotSellOrders) != 2 {
		t.Fatalf("expected dynamic rescue to sell spot twice until flat, got %d", spotSellOrders)
	}
}
