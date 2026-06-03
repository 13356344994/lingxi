package app

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureEmergencyStopClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".halt-live")
	if err := ensureEmergencyStopClear(path); err != nil {
		t.Fatalf("expected clear kill switch to pass, got %v", err)
	}
	if err := os.WriteFile(path, []byte("halt"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := ensureEmergencyStopClear(path); err == nil {
		t.Fatal("expected emergency stop file to block startup")
	}
}

func TestFundingRateSniperEngineCheckDailyRiskLimitsTripsOnLoss(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyLossUSDT = 10
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.riskBudgetState.DailyRealizedPnL = -12
	engine.runtimeReporter = newRuntimeReporter(engine)

	err := engine.checkDailyRiskLimits("test")
	if err == nil {
		t.Fatal("expected daily loss limit to trip")
	}
	if !strings.Contains(err.Error(), "当日累计亏损") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !engine.riskBudgetState.DailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set")
	}
}

func TestNoteOrderPlacedQueuesDailyRiskPersistenceAsynchronously(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: filepath.Join(t.TempDir(), "live_daily_risk_state.json"),
		dailyRiskDate:      tradingDayUTC(time.Now().UTC()),
		dailyRiskPersistCh: make(chan persistedDailyRiskState, 1),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)
	engine.dailyRiskPersistCh <- persistedDailyRiskState{TradingDay: engine.dailyRiskDate}

	start := time.Now()
	if err := engine.noteOrderPlaced(context.Background(), "batch_open_futures", "targets=1", 1, true); err != nil {
		t.Fatalf("noteOrderPlaced() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("expected noteOrderPlaced to avoid blocking on disk persistence, got %s", elapsed)
	}
	if engine.riskBudgetState.DailyOrderCount != 1 {
		t.Fatalf("expected daily order count to increment in memory, got %d", engine.riskBudgetState.DailyOrderCount)
	}
}

func TestStartDailyRiskStateWriterPersistsQueuedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live_daily_risk_state.json")
	engine := &FundingRateSniperEngine{
		cfg:                DefaultEngineConfig(),
		logger:             log.Default(),
		dailyRiskStatePath: path,
		dailyRiskPersistCh: make(chan persistedDailyRiskState, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.startDailyRiskStateWriter(ctx)
	engine.dailyRiskPersistCh <- persistedDailyRiskState{
		TradingDay:       tradingDayUTC(time.Now().UTC()),
		RealizedPnL:      -3.5,
		OrderCount:       2,
		OpenNotionalUSDT: 40,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			if !strings.Contains(string(data), "\"order_count\": 2") {
				t.Fatalf("unexpected persisted payload: %s", string(data))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expected async daily risk writer to persist queued state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDailyOrderLimitSurvivesRestart 钉死 C1：日级下单计数必须随持久化状态在进程重启后恢复，
// 不能因为重启把 riskBudgetState 清零而绕过 MaxDailyOrderCount。
func TestDailyOrderLimitSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live_daily_risk_state.json")
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyOrderCount = 2

	first := &FundingRateSniperEngine{
		cfg:                cfg,
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: path,
		dailyRiskDate:      tradingDayUTC(time.Now().UTC()),
	}
	first.runtimeReporter = newRuntimeReporter(first)

	if err := first.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err != nil {
		t.Fatalf("first order should pass: %v", err)
	}
	if err := first.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err != nil {
		t.Fatalf("second order should pass: %v", err)
	}
	if first.riskBudgetState.DailyOrderCount != 2 {
		t.Fatalf("expected in-memory order count 2, got %d", first.riskBudgetState.DailyOrderCount)
	}

	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "\"order_count\": 2") {
		t.Fatalf("expected persisted order_count=2, err=%v payload=%s", err, string(data))
	}

	// 模拟进程重启：全新引擎从同一份持久化状态恢复。
	second := &FundingRateSniperEngine{
		cfg:                cfg,
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: path,
	}
	second.runtimeReporter = newRuntimeReporter(second)
	if err := second.loadDailyRiskState(); err != nil {
		t.Fatalf("loadDailyRiskState() error = %v", err)
	}
	if second.riskBudgetState.DailyOrderCount != 2 {
		t.Fatalf("expected restored order count 2 after restart, got %d", second.riskBudgetState.DailyOrderCount)
	}

	// 重启后再下一单应当因为已达日限而被阻断。
	if err := second.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err == nil {
		t.Fatal("expected daily order limit to trip after restart, got nil")
	}
}

// TestSetCycleOpenNotionalUsedEnforcesDailyLimit 钉死 C2：MaxDailyOpenNotionalUSDT 必须真正生效，
// 每个周期的开仓名义累加进当日名义并在超限时熔断。
func TestSetCycleOpenNotionalUsedEnforcesDailyLimit(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyOpenNotionalUSDT = 100
	cfg.RiskConfig.MaxCycleOpenNotionalUSDT = 0

	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	if err := engine.setCycleOpenNotionalUsed(60); err != nil {
		t.Fatalf("first cycle notional should pass: %v", err)
	}
	if engine.riskBudgetState.DailyOpenNotionalUsed != 60 {
		t.Fatalf("expected daily notional 60, got %.4f", engine.riskBudgetState.DailyOpenNotionalUsed)
	}

	// 进入下一周期，周期内累计清零但当日累计保留。
	engine.riskBudgetState.ResetCycle()

	if err := engine.setCycleOpenNotionalUsed(60); err == nil {
		t.Fatal("expected daily notional limit to trip when cumulative exceeds 100, got nil")
	}
	if !engine.riskBudgetState.DailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set after notional breach")
	}
}

// TestGuardRecoveryFuturesReduceOnly 钉死 C3 硬保证：恢复/平残腿通道只能减仓，绝不能开新仓。
func TestGuardRecoveryFuturesReduceOnly(t *testing.T) {
	normal := context.Background()
	recovery := withExposureManagerRequest(context.Background())

	if err := guardRecoveryFuturesReduceOnly(normal, "BTCUSDT", "side=BUY", false); err != nil {
		t.Fatalf("普通开仓不应被守卫拦截: %v", err)
	}
	if err := guardRecoveryFuturesReduceOnly(normal, "BTCUSDT", "side=SELL", true); err != nil {
		t.Fatalf("普通 reduceOnly 不应被守卫拦截: %v", err)
	}
	if err := guardRecoveryFuturesReduceOnly(recovery, "BTCUSDT", "side=SELL", true); err != nil {
		t.Fatalf("恢复通道的 reduceOnly 减仓应被允许: %v", err)
	}
	if err := guardRecoveryFuturesReduceOnly(recovery, "BTCUSDT", "side=BUY", false); err == nil {
		t.Fatal("恢复通道的非 reduceOnly(开新仓)下单必须被拒绝")
	}
}

// TestNoteOrderPlacedRecoveryBypassesEmergencyStop 钉死 C3 策略：减仓型恢复在急停(.halt-live)期间仍可继续，
// 以尽快收敛敞口；而普通下单必须被急停阻断。
func TestNoteOrderPlacedRecoveryBypassesEmergencyStop(t *testing.T) {
	haltPath := filepath.Join(t.TempDir(), ".halt-live")
	if err := os.WriteFile(haltPath, []byte("halt"), 0o644); err != nil {
		t.Fatalf("写入急停文件失败: %v", err)
	}
	engine := &FundingRateSniperEngine{
		cfg:               DefaultEngineConfig(),
		logger:            log.Default(),
		runtimeReporter:   newRuntimeReporter(nil),
		emergencyStopFile: haltPath,
		dailyRiskDate:     tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	if err := engine.noteOrderPlaced(context.Background(), "spot_market_sell", "x", 1, false); err == nil {
		t.Fatal("普通下单在急停文件存在时必须被阻断")
	}
	recovery := withExposureManagerRequest(context.Background())
	if err := engine.noteOrderPlaced(recovery, "exposure_rescue_sell_spot", "x", 1, false); err != nil {
		t.Fatalf("减仓型恢复应能在急停期间继续，却被阻断: %v", err)
	}
}

func TestEvaluateOpenDepthStabilityTripsOnVisibleDepthCollapse(t *testing.T) {
	engine := &FundingRateSniperEngine{cfg: DefaultEngineConfig()}
	target := &MonitoredTarget{
		LastObserveTime:            time.Now().UTC(),
		LastSpotAsk:                100,
		LastFuturesBid:             101,
		LastSpotVisibleNotional:    1000,
		LastFuturesVisibleNotional: 1000,
	}
	spotDepth := ParsedOrderBookDepth{
		Asks:       []DepthLevel{{Price: 100.2, Quantity: 4}},
		Bids:       []DepthLevel{{Price: 99.8, Quantity: 4}},
		ReceivedAt: time.Now().UTC(),
	}
	futuresDepth := ParsedOrderBookDepth{
		Bids:       []DepthLevel{{Price: 100.7, Quantity: 4}},
		Asks:       []DepthLevel{{Price: 101.1, Quantity: 4}},
		ReceivedAt: time.Now().UTC(),
	}

	decision, detail, unstable, spotVisible, futuresVisible, ratio := engine.evaluateOpenDepthStability(target, fundingCarryDirectionPositive, spotDepth, futuresDepth, 100.2, 100.7)
	if !unstable {
		t.Fatal("expected visible depth collapse to be treated as unstable")
	}
	if decision.Action != "symbol_cooldown" {
		t.Fatalf("expected symbol cooldown, got %+v", decision)
	}
	if !strings.Contains(detail, "可见深度快速塌陷") {
		t.Fatalf("unexpected stability detail: %s", detail)
	}
	if spotVisible <= 0 || futuresVisible <= 0 {
		t.Fatalf("expected visible notionals to be returned, got spot=%.4f futures=%.4f", spotVisible, futuresVisible)
	}
	if ratio >= 0.65 {
		t.Fatalf("expected collapsed ratio below cooldown threshold, got %.4f", ratio)
	}
}
