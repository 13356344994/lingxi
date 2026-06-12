package app

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPanicHardStopOptInGatesFatalStop 钉死 P5-2:panic-flatten 后硬停机是 opt-in。
// 开关关闭(默认)→ requestPanicHardStopIfEnabled 为 no-op,checkLiveSafetyGuards 不返回 fatal(熔断冷却自动复活,行为不变);
// 开关开启 → panic 触发后 checkLiveSafetyGuards 返回 fatalEngineError(永久停机,需人工核对重启)。
func TestPanicHardStopOptInGatesFatalStop(t *testing.T) {
	newEngine := func(enabled bool) *FundingRateSniperEngine {
		cfg := DefaultEngineConfig()
		cfg.RiskConfig.PanicFlattenHardStopEnabled = enabled
		return &FundingRateSniperEngine{cfg: cfg, logger: log.New(os.Stderr, "", 0), runtimeReporter: newRuntimeReporter(nil)}
	}

	// 关闭(默认):请求硬停机为 no-op,guard 放行。
	off := newEngine(false)
	off.requestPanicHardStopIfEnabled("realtime drawdown panic")
	if off.panicHardStopReason != "" {
		t.Fatalf("开关关闭时不应设置硬停机原因, got %q", off.panicHardStopReason)
	}
	if err := off.checkPanicHardStop("idle"); err != nil {
		t.Fatalf("开关关闭时 guard 应放行, got %v", err)
	}

	// 开启:panic 后设原因,guard 返回 fatalEngineError(主循环据此永久停机)。
	on := newEngine(true)
	if err := on.checkPanicHardStop("idle"); err != nil {
		t.Fatalf("panic 触发前 guard 应放行, got %v", err)
	}
	on.requestPanicHardStopIfEnabled("realtime drawdown panic")
	if on.panicHardStopReason == "" {
		t.Fatal("开关开启时 panic 后应设置硬停机原因")
	}
	err := on.checkPanicHardStop("before_cycle")
	if err == nil {
		t.Fatal("开关开启且 panic 后,guard 应返回 fatal 错误")
	}
	var fatal fatalEngineError
	if !errors.As(err, &fatal) {
		t.Fatalf("应返回 fatalEngineError(主循环据此停机), got %T: %v", err, err)
	}
}

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

// TestRecoveryOrdersDoNotBurnDailyBudget 钉死根因修复:ExposureManager 残腿收敛/清理类下单(recovery)
// 只计入审计计数 DailyRecoveryOrderCount,绝不增加 DailyOrderCount,也绝不触发 MaxDailyOrderCount 日级闸。
// 否则残腿 attempt=6/6 反复重试会烧穿当日交易额度,让引擎启动即被日级闸挡死(假崩溃)。
func TestRecoveryOrdersDoNotBurnDailyBudget(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxDailyOrderCount = 2
	engine := &FundingRateSniperEngine{
		cfg:                cfg,
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: filepath.Join(t.TempDir(), "live_daily_risk_state.json"),
		dailyRiskDate:      tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	recoveryCtx := withExposureManagerRequest(context.Background())
	// 模拟残腿反复重试 10 次,远超 MaxDailyOrderCount=2。
	for i := 0; i < 10; i++ {
		if err := engine.noteOrderPlaced(recoveryCtx, "spot_market_buy", "ENAUSDT 残腿", 1, false); err != nil {
			t.Fatalf("恢复类下单不应被日级闸阻断, 第 %d 次 err=%v", i, err)
		}
	}
	if engine.riskBudgetState.DailyOrderCount != 0 {
		t.Fatalf("恢复类下单不应增加交易额度 DailyOrderCount, got %d", engine.riskBudgetState.DailyOrderCount)
	}
	if engine.riskBudgetState.DailyRecoveryOrderCount != 10 {
		t.Fatalf("恢复类下单应计入审计计数, want 10 got %d", engine.riskBudgetState.DailyRecoveryOrderCount)
	}
	if engine.riskBudgetState.DailyLimitTriggered {
		t.Fatalf("恢复类下单不应触发日级下单闸")
	}

	// 真实交易单仍正常占用并在超限时触发日级闸。
	if err := engine.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err != nil {
		t.Fatalf("首笔交易单不应被阻断: %v", err)
	}
	if err := engine.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err != nil {
		t.Fatalf("第二笔交易单不应被阻断: %v", err)
	}
	if err := engine.noteOrderPlaced(context.Background(), "batch_open_futures", "n=1", 1, true); err == nil {
		t.Fatalf("第三笔交易单应触发 MaxDailyOrderCount=2 日级闸")
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

// TestNoteCycleFinalSettlementTripsConsecutiveLossBreaker 钉死 P3：实盘连续亏损熔断。
// 当「按 funding 实收结算后的真实净利」连续为负达到阈值时必须停机，且这是 fail-safe（只停机不下单）。
func TestNoteCycleFinalSettlementTripsConsecutiveLossBreaker(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxConsecutiveLossCycles = 2
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9 // 排除日亏维度干扰，单独验证连亏维度
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	if err := engine.noteCycleFinalSettlement(-0.5); err != nil {
		t.Fatalf("第一次亏损不应触线: %v", err)
	}
	if engine.riskBudgetState.ConsecutiveLossCycles != 1 {
		t.Fatalf("expected consecutive loss count 1, got %d", engine.riskBudgetState.ConsecutiveLossCycles)
	}
	err := engine.noteCycleFinalSettlement(-0.5)
	if err == nil {
		t.Fatal("连续第二次亏损应触发连亏熔断")
	}
	if !strings.Contains(err.Error(), "连续亏损周期数") {
		t.Fatalf("unexpected breaker error: %v", err)
	}
	if !engine.riskBudgetState.DailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set after consecutive loss breaker")
	}
}

// TestNoteCycleFinalSettlementResetsOnProfit 断言一旦出现盈利周期，连亏计数清零，不会错误累积触发熔断。
func TestNoteCycleFinalSettlementResetsOnProfit(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxConsecutiveLossCycles = 2
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	if err := engine.noteCycleFinalSettlement(-0.5); err != nil {
		t.Fatalf("第一次亏损不应触线: %v", err)
	}
	if err := engine.noteCycleFinalSettlement(0.8); err != nil {
		t.Fatalf("盈利周期不应触线: %v", err)
	}
	if engine.riskBudgetState.ConsecutiveLossCycles != 0 {
		t.Fatalf("expected counter reset to 0 after profit, got %d", engine.riskBudgetState.ConsecutiveLossCycles)
	}
	if err := engine.noteCycleFinalSettlement(-0.5); err != nil {
		t.Fatalf("盈利清零后单次亏损不应触线: %v", err)
	}
}

// TestConsecutiveLossSurvivesRestart 钉死连亏计数必须随持久化状态在进程重启后恢复，
// 不能因为重启把 riskBudgetState 清零而绕过连亏熔断。
func TestConsecutiveLossSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live_daily_risk_state.json")
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxConsecutiveLossCycles = 2
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9

	first := &FundingRateSniperEngine{
		cfg:                cfg,
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		dailyRiskStatePath: path,
		dailyRiskDate:      tradingDayUTC(time.Now().UTC()),
	}
	first.runtimeReporter = newRuntimeReporter(first)
	if err := first.noteCycleFinalSettlement(-0.5); err != nil {
		t.Fatalf("第一次亏损不应触线: %v", err)
	}

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
	if second.riskBudgetState.ConsecutiveLossCycles != 1 {
		t.Fatalf("expected restored consecutive loss count 1 after restart, got %d", second.riskBudgetState.ConsecutiveLossCycles)
	}
	if err := second.noteCycleFinalSettlement(-0.5); err == nil {
		t.Fatal("重启后再亏一次应触发连亏熔断")
	}
}

// TestAccountDrawdownBreakerTripsFromHighWaterMark 钉死账户级高水位回撤熔断：累计净利冲高后回撤超阈值即停机。
// 这是 MaxDailyLossUSDT(日清零)抓不到的「跨日慢性失血」维度。
func TestAccountDrawdownBreakerTripsFromHighWaterMark(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 10
	cfg.RiskConfig.MaxConsecutiveLossCycles = 0 // 隔离：只验证回撤维度
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9
	cfg.RiskConfig.MaxCycleLossUSDT = 0 // 隔离：关闭单周期净亏熔断，避免 -9 单周期亏损先触线
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	// 先盈利冲出高水位 +20。
	if err := engine.noteCycleFinalSettlement(20); err != nil {
		t.Fatalf("盈利周期不应触线: %v", err)
	}
	if engine.riskBudgetState.RealizedPnLPeakUSDT != 20 {
		t.Fatalf("expected peak 20, got %.4f", engine.riskBudgetState.RealizedPnLPeakUSDT)
	}
	// 回撤 -9：尚未触线（drawdown=9 < 10）。
	if err := engine.noteCycleFinalSettlement(-9); err != nil {
		t.Fatalf("回撤未达阈值不应触线: %v", err)
	}
	// 高水位不应随回撤下降。
	if engine.riskBudgetState.RealizedPnLPeakUSDT != 20 {
		t.Fatalf("expected high-water mark to stay at 20, got %.4f", engine.riskBudgetState.RealizedPnLPeakUSDT)
	}
	// 再回撤 -2：累计=9，drawdown=11 >= 10，触发停机。
	err := engine.noteCycleFinalSettlement(-2)
	if err == nil {
		t.Fatal("自高水位回撤超阈值应触发账户级熔断")
	}
	if !strings.Contains(err.Error(), "账户级回撤") {
		t.Fatalf("unexpected breaker error: %v", err)
	}
	if !engine.riskBudgetState.DailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set after account drawdown breaker")
	}
}

// TestCycleLossBreakerTripsOnFinalNetLoss 钉死 R8：单周期净亏熔断必须用 funding 实收后的真实最终净利判定。
// 当某个周期最终净亏超过 MaxCycleLossUSDT 时停机，即便日亏/连亏/回撤维度都未触线；funding 实收前不会因
// 「资金费尚未到账」的 pre-funding 负净利而误触。
func TestCycleLossBreakerTripsOnFinalNetLoss(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxCycleLossUSDT = 8
	// 隔离其余维度，单独验证单周期净亏维度。
	cfg.RiskConfig.MaxConsecutiveLossCycles = 0
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 0
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	// 单周期最终净亏 7.5 < 8：不触线。
	if err := engine.noteCycleFinalSettlement(-7.5); err != nil {
		t.Fatalf("单周期净亏未达阈值不应触线: %v", err)
	}
	// 单周期最终净亏 9 >= 8：触发停机。
	err := engine.noteCycleFinalSettlement(-9)
	if err == nil {
		t.Fatal("单周期最终净亏超过阈值应触发熔断")
	}
	if !strings.Contains(err.Error(), "单周期最终净亏损") {
		t.Fatalf("unexpected breaker error: %v", err)
	}
	if !engine.riskBudgetState.DailyLimitTriggered {
		t.Fatal("expected daily limit flag to be set after cycle loss breaker")
	}
}

// TestProjectedCycleNetPnLAddsExpectedFundingWhenPending 钉死单周期熔断早刹车口径：funding 未实收时投影最终净利
// 必须叠加预期资金费，避免把「资金费还没到账」误判成单周期巨亏（资金费套利平仓那刻 NetPnL 几乎必为负）。
func TestProjectedCycleNetPnLAddsExpectedFundingWhenPending(t *testing.T) {
	// pre-funding 净亏 -6，预期资金费 +10：投影最终净利应为 +4（不应被当作亏损）。
	pending := CycleSettlement{NetPnL: -6, ExpectedFundingIncome: 10, FundingIncomePending: true}
	if got := projectedCycleNetPnL(pending); got <= 0 {
		t.Fatalf("funding 未实收时应叠加预期资金费得正投影, got %.6f", got)
	}
	// funding 已实收：直接用真实 NetPnL，不再叠加。
	settled := CycleSettlement{NetPnL: -6, ExpectedFundingIncome: 10, FundingIncomePending: false}
	if got := projectedCycleNetPnL(settled); got != -6 {
		t.Fatalf("funding 已实收应直接用 NetPnL=-6, got %.6f", got)
	}
}

// TestAccountDrawdownWaterMarkSeededFromRecords 钉死高水位由已落盘结算记录按时间顺序回放重算复原，
// 重启后无需独立持久化峰值即可恢复正确回撤，不会被重启清零绕过。
func TestAccountDrawdownWaterMarkSeededFromRecords(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReportStore() error = %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	// 时间乱序写入，验证回放时会按时间排序得到正确的累计曲线: +30(峰) -> -12 -> -6，累计=12，峰=30，回撤=18。
	appendSettled := func(cycle string, at time.Time, net float64) {
		if appendErr := store.AppendSettlement(CycleSettlement{
			RunID:           "run-1",
			CycleID:         cycle,
			CloseExecutedAt: at,
			NetPnL:          net,
		}); appendErr != nil {
			t.Fatalf("AppendSettlement() error = %v", appendErr)
		}
	}
	appendSettled("c2", base.Add(2*time.Minute), -12)
	appendSettled("c1", base.Add(1*time.Minute), 30)
	appendSettled("c3", base.Add(3*time.Minute), -6)
	// 仍在等待 funding 的周期净利未定稿，必须被回放跳过。
	if appendErr := store.AppendSettlement(CycleSettlement{
		RunID: "run-1", CycleID: "c4", CloseExecutedAt: base.Add(4 * time.Minute),
		NetPnL: -999, FundingIncomePending: true,
	}); appendErr != nil {
		t.Fatalf("AppendSettlement() error = %v", appendErr)
	}

	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 15
	cfg.RiskConfig.MaxConsecutiveLossCycles = 0
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9 // 隔离日亏维度，单独验证回撤维度
	engine := &FundingRateSniperEngine{
		cfg:                cfg,
		logger:             log.Default(),
		runtimeReporter:    newRuntimeReporter(nil),
		reportStore:        store,
		dailyRiskStatePath: filepath.Join(t.TempDir(), "live_daily_risk_state.json"),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)
	if err := engine.loadDailyRiskState(); err != nil {
		t.Fatalf("loadDailyRiskState() error = %v", err)
	}

	if engine.riskBudgetState.RealizedPnLPeakUSDT != 30 {
		t.Fatalf("expected replayed peak 30, got %.4f", engine.riskBudgetState.RealizedPnLPeakUSDT)
	}
	if engine.riskBudgetState.RealizedPnLCumulativeUSDT != 12 {
		t.Fatalf("expected replayed cumulative 12 (pending cycle skipped), got %.4f", engine.riskBudgetState.RealizedPnLCumulativeUSDT)
	}
	// 回撤=18 >= 15：重启后首次安全检查就应停机，且原因必须是账户级回撤。
	err = engine.checkDailyRiskLimits("post_restart")
	if err == nil {
		t.Fatal("重启后自高水位回撤超阈值应触发账户级熔断")
	}
	if !strings.Contains(err.Error(), "账户级回撤") {
		t.Fatalf("unexpected halt reason after restart: %v", err)
	}
}

// TestConsecutiveLossResetsAtUTCDayRollover 钉死 R9 的已知行为:连亏计数在 UTC 跨日时清零。
// 这是有意为之(快速系统性失效闸,日级语义),其留下的跨日慢性失血缺口由账户级回撤兜底(见下一条)。
func TestConsecutiveLossResetsAtUTCDayRollover(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg:             DefaultEngineConfig(),
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   "2000-01-01", // 过去的交易日 → 强制下次检查触发跨日清零
	}
	engine.runtimeReporter = newRuntimeReporter(engine)
	engine.riskBudgetState.ConsecutiveLossCycles = 5

	engine.platformRiskMu.Lock()
	engine.ensureDailyRiskDateLocked()
	engine.platformRiskMu.Unlock()

	if engine.dailyRiskDate != tradingDayUTC(time.Now().UTC()) {
		t.Fatalf("跨日后 dailyRiskDate 应推进到今天, got %s", engine.dailyRiskDate)
	}
	if engine.riskBudgetState.ConsecutiveLossCycles != 0 {
		t.Fatalf("R9: 连亏计数应在 UTC 跨日清零, got %d", engine.riskBudgetState.ConsecutiveLossCycles)
	}
}

// TestChronicCrossDayBleedCaughtByDrawdownDespiteConsecutiveReset 核验 R9 的兜底:每个 UTC 交易日只亏一次
// 小额(连亏计数每日清零、永不触线)的慢性失血,最终必然被账户级高水位回撤熔断 MaxAccountDrawdownUSDT 拦下。
// 这证明「连亏日级清零」留下的缝隙不是真窟窿——回撤维度才是跨日慢性失血的安全网。
func TestChronicCrossDayBleedCaughtByDrawdownDespiteConsecutiveReset(t *testing.T) {
	cfg := DefaultEngineConfig()
	cfg.RiskConfig.MaxAccountDrawdownUSDT = 10
	cfg.RiskConfig.MaxConsecutiveLossCycles = 3 // 每天只亏 1 次,连亏永远停在 1 < 3,绝不触线
	cfg.RiskConfig.MaxDailyLossUSDT = 1e9       // 隔离日亏维度,单独验证跨日慢性失血
	engine := &FundingRateSniperEngine{
		cfg:             cfg,
		logger:          log.Default(),
		runtimeReporter: newRuntimeReporter(nil),
		dailyRiskDate:   tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	// 第 0 天:盈利冲出高水位 +5。
	if err := engine.noteCycleFinalSettlement(5); err != nil {
		t.Fatalf("盈利周期不应触线: %v", err)
	}

	// 之后每个交易日开始前强制跨日(连亏清零),再亏 1 次小额 → 模拟跨日慢性失血。
	var tripErr error
	for day := 0; day < 30; day++ {
		engine.platformRiskMu.Lock()
		engine.dailyRiskDate = "2000-01-01" // 强制下次 ensureDailyRiskDateLocked 视为跨日 → 连亏清零
		engine.platformRiskMu.Unlock()
		if err := engine.noteCycleFinalSettlement(-1); err != nil {
			tripErr = err
			break
		}
		if engine.riskBudgetState.ConsecutiveLossCycles > 1 {
			t.Fatalf("连亏计数应因每日清零停在 <=1, got %d", engine.riskBudgetState.ConsecutiveLossCycles)
		}
	}

	if tripErr == nil {
		t.Fatal("跨日慢性失血最终应被账户级回撤熔断兜住,却始终未触发")
	}
	if !strings.Contains(tripErr.Error(), "账户级回撤") {
		t.Fatalf("兜底熔断应为账户级回撤, got %v", tripErr)
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
