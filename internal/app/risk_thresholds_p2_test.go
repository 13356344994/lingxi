package app

import (
	"math"
	"testing"
	"time"

	"lingxi/internal/core"
)

func TestBaseMinLiquidationDistanceRateSingleSource(t *testing.T) {
	risk := core.RiskConfig{MinLiquidationDistanceRate: 0.12}
	cross := core.CrossVenueExecutorConfig{MinLiqDistance: 0.20}
	if got := BaseMinLiquidationDistanceRate(risk, cross); got != 0.12 {
		t.Fatalf("RiskConfig 优先: got %.4f want 0.12", got)
	}
	risk.MinLiquidationDistanceRate = 0
	if got := BaseMinLiquidationDistanceRate(risk, cross); got != 0.20 {
		t.Fatalf("CrossVenue 回退: got %.4f want 0.20", got)
	}
	cross.MinLiqDistance = 0
	if got := BaseMinLiquidationDistanceRate(risk, cross); got != defaultMinLiquidationDistanceRate {
		t.Fatalf("默认: got %.4f want %.4f", got, defaultMinLiquidationDistanceRate)
	}
}

func TestEffectiveMinLiquidationDistanceVolScale(t *testing.T) {
	risk := core.RiskConfig{
		MinLiquidationDistanceRate:    0.15,
		MinLiqDistanceVolScaleEnabled: false,
	}
	if got := EffectiveMinLiquidationDistance(risk, core.CrossVenueExecutorConfig{}, "BTC-USDT", 2.0); math.Abs(got-0.15) > 1e-9 {
		t.Fatalf("vol 缩放关闭应等于 base: got %.4f", got)
	}
	risk.MinLiqDistanceVolScaleEnabled = true
	risk.MinLiqDistanceVolScaleFactor = 1.0
	if got := EffectiveMinLiquidationDistance(risk, core.CrossVenueExecutorConfig{}, "BTC-USDT", 0); math.Abs(got-0.15) > 1e-9 {
		t.Fatalf("无 vol 信号应等于 base: got %.4f", got)
	}
	if got := EffectiveMinLiquidationDistance(risk, core.CrossVenueExecutorConfig{}, "BTC-USDT", 1.5); math.Abs(got-0.225) > 1e-9 {
		t.Fatalf("高波动应放大: got %.4f want 0.225", got)
	}
	risk.MinLiqDistanceVolScaleFactor = 2.0
	if got := EffectiveMinLiquidationDistance(risk, core.CrossVenueExecutorConfig{}, "SHIB-USDT", 1.5); math.Abs(got-0.45) > 1e-9 {
		t.Fatalf("配置 multiplier 应生效: got %.4f want 0.45", got)
	}
	risk.MinLiqDistanceVolScaleFactor = 1.0
	if got := EffectiveMinLiquidationDistance(risk, core.CrossVenueExecutorConfig{}, "BTC-USDT", 0.5); math.Abs(got-0.15) > 1e-9 {
		t.Fatalf("低波动不缩小(更严不更松): got %.4f", got)
	}
}

func TestCrossVenueManageUsesUnifiedMinLiqDistance(t *testing.T) {
	cfg := testCrossVenueExecutorConfig()
	cfg.RiskConfig.MinLiquidationDistanceRate = 0.18
	cfg.CrossVenueExecutor.MinLiqDistance = 0.10 // 应被 RiskConfig 覆盖
	e := &CrossVenueExecutor{
		cfg:   cfg,
		nowFn: func() time.Time { return time.Now() },
	}
	volMult := 0.0
	if e.symbolVolMultiplier != nil {
		volMult = e.symbolVolMultiplier("BTC-USDT")
	}
	minDist := EffectiveMinLiquidationDistance(e.cfg.RiskConfig, e.cfg.CrossVenueExecutor, "BTC-USDT", volMult)
	if math.Abs(minDist-0.18) > 1e-9 {
		t.Fatalf("manage 路径应读统一阈值: got %.4f want 0.18", minDist)
	}
}

func TestSymbolCooldownForSeverity(t *testing.T) {
	risk := core.RiskConfig{
		SymbolMinorCooldown:    5 * time.Minute,
		SymbolAbnormalCooldown: 10 * time.Minute,
		SymbolCriticalCooldown: 30 * time.Minute,
	}
	if got := SymbolCooldownForSeverity(risk, "minor"); got != 5*time.Minute {
		t.Fatalf("minor=%v want 5m", got)
	}
	if got := SymbolCooldownForSeverity(risk, "major"); got != 10*time.Minute {
		t.Fatalf("major/default=%v want 10m", got)
	}
	if got := SymbolCooldownForSeverity(risk, "critical"); got != 30*time.Minute {
		t.Fatalf("critical=%v want 30m", got)
	}
}

func TestCrossVenueSymbolCooldownForCategory(t *testing.T) {
	risk := core.RiskConfig{
		SymbolAbnormalCooldown: 10 * time.Minute,
		SymbolCriticalCooldown: 30 * time.Minute,
	}
	cross := core.CrossVenueExecutorConfig{
		SymbolCooldownHours:         1,
		BybitAgreementCooldownHours: 6,
	}
	if got := CrossVenueSymbolCooldownForCategory(risk, cross, "execution", "major"); got != 10*time.Minute {
		t.Fatalf("execution/major=%v want 10m", got)
	}
	if got := CrossVenueSymbolCooldownForCategory(risk, cross, "bybit_agreement", "major"); got != 6*time.Hour {
		t.Fatalf("bybit_agreement=%v want 6h", got)
	}
}

func TestRiskSupervisorSetSymbolCooldownBySeverity(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: EngineConfig{RiskConfig: core.RiskConfig{
			SymbolMinorCooldown:    time.Minute,
			SymbolAbnormalCooldown: 2 * time.Minute,
			SymbolCriticalCooldown: 5 * time.Minute,
		}},
		portfolioManager: portfolioManager{symbolCooldowns: make(map[string]symbolCooldownState)},
	}
	s := newRiskSupervisor(engine)
	before := time.Now().UTC()
	s.SetSymbolCooldown("BTCUSDT", "test", "critical", "unit")
	state := engine.symbolCooldowns["BTCUSDT"]
	if state.Until.Before(before.Add(4*time.Minute)) || state.Until.After(before.Add(6*time.Minute)) {
		t.Fatalf("critical 冷却应≈5m: until=%v", state.Until)
	}
}

func TestEffectiveRiskBudgetLimit(t *testing.T) {
	cfg := core.RiskConfig{
		MaxExecutionIncidents:          3,
		RiskBudgetScaleEnabled:         false,
		RiskBudgetScalePerOpenPosition: 2,
	}
	if got := effectiveRiskBudgetLimit(3, 5, cfg); got != 3 {
		t.Fatalf("开关关=%d want 3", got)
	}
	cfg.RiskBudgetScaleEnabled = true
	if got := effectiveRiskBudgetLimit(3, 0, cfg); got != 3 {
		t.Fatalf("低并发≈原值: got %d want 3", got)
	}
	if got := effectiveRiskBudgetLimit(3, 4, cfg); got != 11 {
		t.Fatalf("高并发放大: got %d want 11", got)
	}
}

func TestEngineEffectiveMaxExecutionIncidentsScaled(t *testing.T) {
	engine := &FundingRateSniperEngine{
		cfg: EngineConfig{RiskConfig: core.RiskConfig{
			MaxExecutionIncidents:          3,
			RiskBudgetScaleEnabled:         true,
			RiskBudgetScalePerOpenPosition: 1,
		}},
		portfolioManager: portfolioManager{
			activeOpenHedges: []OpenHedge{{}, {}},
		},
	}
	if got := engine.effectiveMaxExecutionIncidents(); got != 5 {
		t.Fatalf("2 持仓 budget=%d want 5", got)
	}
}
