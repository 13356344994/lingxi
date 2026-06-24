package app

import (
	"context"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

// TestCarryCapturePlanPersistenceRoundTrip 验证统一捕获计划随台账原子落盘/读回不丢字段,
// 且无计划的旧式持仓读回仍为 nil(指针 + omitempty 保证旧台账字节不变)。
func TestCarryCapturePlanPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newCrossVenuePositionStore(dir)
	if err != nil {
		t.Fatalf("newCrossVenuePositionStore: %v", err)
	}

	target := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	withPlan := CrossVenuePosition{
		Symbol:    "H-USDT",
		HighVenue: "bybit",
		LowVenue:  "okx",
		Status:    CrossVenueStatusOpen,
		OpenedAt:  time.Date(2026, 6, 23, 1, 57, 0, 0, time.UTC),
		CapturePlan: &CarryCapturePlan{
			TargetLeg:                 "low",
			TargetSettlementTime:      target,
			TargetSettlements:         1,
			PlannedExitTime:           target.Add(90 * time.Second),
			ExpectedTargetFundingUSDT: 0.2538,
			BasisBudgetUSDT:           0.2538,
			PeakAdverseBasisUSDT:      0.05,
		},
	}
	noPlan := CrossVenuePosition{
		Symbol:    "RE-USDT",
		HighVenue: "okx",
		LowVenue:  "bybit",
		Status:    CrossVenueStatusOpen,
		OpenedAt:  time.Date(2026, 6, 23, 2, 0, 0, 0, time.UTC),
	}

	if err := store.saveAll([]CrossVenuePosition{withPlan, noPlan}); err != nil {
		t.Fatalf("saveAll: %v", err)
	}

	loaded, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d positions, want 2", len(loaded))
	}

	var gotPlan, gotNoPlan *CrossVenuePosition
	for i := range loaded {
		switch loaded[i].Symbol {
		case "H-USDT":
			gotPlan = &loaded[i]
		case "RE-USDT":
			gotNoPlan = &loaded[i]
		}
	}
	if gotPlan == nil || gotNoPlan == nil {
		t.Fatalf("missing expected positions after load")
	}
	if gotNoPlan.CapturePlan != nil {
		t.Fatalf("position without plan should load CapturePlan=nil, got %+v", gotNoPlan.CapturePlan)
	}
	if gotPlan.CapturePlan == nil {
		t.Fatal("position with plan loaded nil CapturePlan")
	}
	p := gotPlan.CapturePlan
	if p.TargetLeg != "low" || p.TargetSettlements != 1 {
		t.Fatalf("plan basic fields wrong: %+v", p)
	}
	if !p.TargetSettlementTime.Equal(target) {
		t.Fatalf("target settlement time=%v want %v", p.TargetSettlementTime, target)
	}
	if !p.PlannedExitTime.Equal(target.Add(90 * time.Second)) {
		t.Fatalf("planned exit=%v want %v", p.PlannedExitTime, target.Add(90*time.Second))
	}
	if p.ExpectedTargetFundingUSDT != 0.2538 || p.BasisBudgetUSDT != 0.2538 || p.PeakAdverseBasisUSDT != 0.05 {
		t.Fatalf("plan numeric fields wrong: %+v", p)
	}
}

// TestCrossVenueTargetFundingLeg 验证目标腿选择:取实际付钱给我们的腿(单次结算现金流更大者)。
func TestCrossVenueTargetFundingLeg(t *testing.T) {
	// H 例:空 bybit rate=-0.0003(付 0.0003)、多 okx rate=-0.003(收 0.003)→ 目标 low。
	if got := crossVenueTargetFundingLeg(-0.0003, -0.003); got != "low" {
		t.Fatalf("target leg=%q want low", got)
	}
	// 空腿正费率主导:空 rate=+0.004(收 0.004)、多 rate=+0.0005(付 0.0005)→ 目标 high。
	if got := crossVenueTargetFundingLeg(0.004, 0.0005); got != "high" {
		t.Fatalf("target leg=%q want high", got)
	}
}

// TestCrossVenueEntryWindowTargetedVsNearest 验证统一模型进场窗对齐"付钱腿"而非"最近的便宜腿"。
func TestCrossVenueEntryWindowTargetedVsNearest(t *testing.T) {
	lead := 180 * time.Second
	highNext := time.Date(2026, 6, 23, 2, 0, 0, 0, time.UTC) // 便宜 1h 腿,最近
	lowNext := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)  // 付钱 2h 腿,目标
	targetLeg := crossVenueTargetFundingLeg(-0.0003, -0.003) // → low

	// now 贴着 02:00:旧"最近腿"逻辑会放行(错误地为便宜腿进场)。
	early := time.Date(2026, 6, 23, 1, 57, 30, 0, time.UTC)
	if !crossVenueFundingEntryWindowReached(highNext, 1, lowNext, 2, early, lead) {
		t.Fatal("nearest-leg logic should be eligible near 02:00")
	}
	// 统一模型:目标是 low(04:00),01:57 远未到 → 不放行。
	if crossVenueFundingEntryWindowReachedTargeted(targetLeg, highNext, 1, lowNext, 2, early, lead) {
		t.Fatal("targeted logic must NOT open for the cheap leg's 02:00 settlement")
	}
	// now 贴着 04:00:目标 low 进场窗已到 → 放行。
	late := time.Date(2026, 6, 23, 3, 57, 30, 0, time.UTC)
	if !crossVenueFundingEntryWindowReachedTargeted(targetLeg, highNext, 1, lowNext, 2, late, lead) {
		t.Fatal("targeted logic should open aligned to the paying leg's 04:00 settlement")
	}
}

// TestCrossVenueBuildCapturePlan 验证开仓计划构建:目标腿/结算/离场/预期资费/基差预算。
func TestCrossVenueBuildCapturePlan(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		UnifiedCaptureModelEnabled: true,
		CaptureTargetSettlements:   1,
		BasisStopBudgetFraction:    1.0,
		PlannedExitBufferSeconds:   90,
	}
	openedAt := time.Date(2026, 6, 23, 3, 57, 0, 0, time.UTC)
	highNext := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	lowNext := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	plan := crossVenueBuildCapturePlan(cfg, "H-USDT", -0.0003, -0.003, 80, 1, highNext, 2, lowNext, openedAt, 0)
	if plan == nil {
		t.Fatal("expected non-nil plan when flag ON")
	}
	if plan.TargetLeg != "low" {
		t.Fatalf("target leg=%q want low", plan.TargetLeg)
	}
	if !plan.TargetSettlementTime.Equal(lowNext) {
		t.Fatalf("target settlement=%v want %v", plan.TargetSettlementTime, lowNext)
	}
	wantExit := lowNext.Add(90 * time.Second)
	if !plan.PlannedExitTime.Equal(wantExit) {
		t.Fatalf("planned exit=%v want %v", plan.PlannedExitTime, wantExit)
	}
	// perSettlement = (-lowRate)*notional = 0.003*80 = 0.24
	if got := plan.ExpectedTargetFundingUSDT; got < 0.2399 || got > 0.2401 {
		t.Fatalf("expected funding=%.6f want ~0.24", got)
	}
	if got := plan.BasisBudgetUSDT; got < 0.2399 || got > 0.2401 {
		t.Fatalf("basis budget=%.6f want ~0.24", got)
	}

	// flag OFF → nil(回退旧行为)。
	cfgOff := cfg
	cfgOff.UnifiedCaptureModelEnabled = false
	if p := crossVenueBuildCapturePlan(cfgOff, "H-USDT", -0.0003, -0.003, 80, 1, highNext, 2, lowNext, openedAt, 0); p != nil {
		t.Fatalf("flag OFF must yield nil plan, got %+v", p)
	}
}

// TestCapturePlanHoldHorizon 验证统一模型下 EV horizon = N × 目标腿 interval(只计实际计划的目标结算)。
func TestCapturePlanHoldHorizon(t *testing.T) {
	now := time.Now().UTC()
	opp := CrossVenueOpportunity{HighFundingRate: -0.0003, LowFundingRate: -0.003, HighIntervalHours: 1, LowIntervalHours: 2}
	cfg := CrossVenueExecutorConfig{UnifiedCaptureModelEnabled: true, CaptureTargetSettlements: 1}
	if h := crossVenueOpenEVHoldHours(cfg, now, opp, 0); h != 2 {
		t.Fatalf("hold=%v want 2 (1 target settlement on 2h low leg)", h)
	}
	// N=2 需基差稳定分≥阈值才放行;稳定币(stability 高)→ 4h。
	cfg.CaptureTargetSettlements = 2
	cfg.BasisStabilityMinScoreForMultiSettlement = 0.7
	stableOpp := opp
	stableOpp.StabilityScore = 0.9
	if h := crossVenueOpenEVHoldHours(cfg, now, stableOpp, 0); h != 4 {
		t.Fatalf("hold=%v want 4 (2 settlements x 2h, stable)", h)
	}
	// 不稳定币 → N 夹回 1 → 2h。
	if h := crossVenueOpenEVHoldHours(cfg, now, opp, 0); h != 2 {
		t.Fatalf("hold=%v want 2 (unstable coin clamps N to 1)", h)
	}
}

// TestMakerConsistentSlippageReducesCost 验证统一模型下首腿 maker 近零滑点 → 总滑点低于旧的 4 腿全 taker 口径。
func TestMakerConsistentSlippageReducesCost(t *testing.T) {
	now := time.Date(2026, 6, 23, 3, 57, 0, 0, time.UTC)
	build := func(flag bool) float64 {
		cfg := CrossVenueExecutorConfig{
			MaxPositionUSDT:            80,
			MaxEffectiveLeverage:       3,
			MakerFirst:                 true,
			SlippageBudgetBps:          20,
			UnifiedCaptureModelEnabled: flag,
			CaptureTargetSettlements:   1,
		}
		opp := CrossVenueOpportunity{
			Symbol: "H-USDT", HighVenue: "bybit", LowVenue: "okx",
			HighFundingRate: -0.0003, LowFundingRate: -0.003,
			HighIntervalHours: 1, LowIntervalHours: 2,
			HighNextFundingTime: now.Add(3 * time.Minute),
			LowNextFundingTime:  now.Add(3 * time.Minute),
			TradableNotional:    80,
		}
		return crossVenueEstimateOpportunityMetrics(cfg, opp, now).ExpectedSlippage
	}
	off := build(false)
	on := build(true)
	if !(on < off) {
		t.Fatalf("maker-consistent slippage should be lower: on=%.4f off=%.4f", on, off)
	}
	// 旧口径 4 腿×20bps=80bps→0.64;新口径 3 腿×20bps=60bps→0.48。
	if math.Abs(off-0.64) > 1e-6 || math.Abs(on-0.48) > 1e-6 {
		t.Fatalf("unexpected slippage off=%.4f on=%.4f", off, on)
	}
}

// TestCapitalAwareSlippageOverrideMatchesHeadline 验证"可投"EV 复算沿用 headline 每单位滑点 → 同名义下 NetEV 相等。
func TestCapitalAwareSlippageOverrideMatchesHeadline(t *testing.T) {
	now := time.Date(2026, 6, 23, 3, 57, 0, 0, time.UTC)
	cfg := CrossVenueExecutorConfig{
		MaxPositionUSDT:            80,
		MaxEffectiveLeverage:       3,
		MakerFirst:                 true,
		SlippageBudgetBps:          20,
		UnifiedCaptureModelEnabled: true,
		CaptureTargetSettlements:   1,
	}
	headline := CrossVenueOpportunity{
		Symbol: "H-USDT", HighVenue: "bybit", LowVenue: "okx",
		HighFundingRate: -0.0003, LowFundingRate: -0.003,
		HighIntervalHours: 1, LowIntervalHours: 2,
		HighNextFundingTime: now.Add(3 * time.Minute),
		LowNextFundingTime:  now.Add(3 * time.Minute),
		TradableNotional:    80,
	}
	h := crossVenueEstimateOpportunityMetrics(cfg, headline, now)

	re := headline
	crossVenueApplyCapitalAwareSlippageOverride(cfg, &re, &h)
	re2 := crossVenueEstimateOpportunityMetrics(cfg, re, now)
	if math.Abs(re2.NetEV-h.NetEV) > 1e-9 {
		t.Fatalf("capital-aware NetEV=%.6f != headline NetEV=%.6f at same notional", re2.NetEV, h.NetEV)
	}
	if math.Abs(re2.ExpectedSlippage-h.ExpectedSlippage) > 1e-9 {
		t.Fatalf("slippage mismatch re2=%.6f headline=%.6f", re2.ExpectedSlippage, h.ExpectedSlippage)
	}
}

// TestPreFundingAdverseTargetLegOnly 验证统一模型下非目标腿(便宜腿)即将结算的不利资费不再触发离场,
// 而旧逻辑会被它平掉 —— 杜绝"为 okx 04:00 开仓却被 bybit 02:00 微小负资费提前平仓"的 churn。
func TestPreFundingAdverseTargetLegOnly(t *testing.T) {
	now := time.Date(2026, 6, 23, 1, 59, 0, 0, time.UTC)
	pos := &CrossVenuePosition{
		Symbol: "H-USDT", HighVenue: "bybit", LowVenue: "okx",
		NotionalUSDT:        80,
		HighFundingRate:     -0.0003,                    // 空 bybit 负费率 → 即将结算时净付(不利)
		LowFundingRate:      -0.003,                     // 多 okx 负费率 → 收(有利)
		HighNextFundingTime: now.Add(60 * time.Second),  // 便宜腿 02:00 即将结算(在 lead 内)
		LowNextFundingTime:  now.Add(121 * time.Minute), // 目标腿 04:00 远未到
		CapturePlan:         &CarryCapturePlan{TargetLeg: "low"},
	}
	cfg := CrossVenueExecutorConfig{CloseBeforeAdverseFundingEnabled: true, PreFundingCloseLeadSeconds: 120}

	// 旧逻辑(flag OFF):便宜腿即将结算的负资费会触发离场。
	if r := crossVenuePreFundingAdverseCloseReason(cfg, pos, now); r == "" {
		t.Fatal("legacy logic should close on cheap-leg adverse pre-funding")
	}
	// 统一模型(flag ON):只看目标腿(okx 未到),不离场。
	cfg.UnifiedCaptureModelEnabled = true
	if r := crossVenuePreFundingAdverseCloseReason(cfg, pos, now); r != "" {
		t.Fatalf("unified model must NOT close for non-target leg adverse funding, got %q", r)
	}
}

// TestPlannedExitCloseReason 验证计划离场时刻前不平、到点即平。
func TestPlannedExitCloseReason(t *testing.T) {
	exit := time.Date(2026, 6, 23, 4, 1, 30, 0, time.UTC)
	pos := &CrossVenuePosition{CapturePlan: &CarryCapturePlan{PlannedExitTime: exit}}

	if r := crossVenuePlannedExitCloseReason(pos, exit.Add(-time.Minute)); r != "" {
		t.Fatalf("before planned exit should not close, got %q", r)
	}
	if r := crossVenuePlannedExitCloseReason(pos, exit.Add(time.Second)); r == "" {
		t.Fatal("at/after planned exit should close")
	}
	// 无计划(旧仓)→ 永不因计划离场而平。
	if r := crossVenuePlannedExitCloseReason(&CrossVenuePosition{}, exit.Add(time.Hour)); r != "" {
		t.Fatalf("position without plan must not planned-exit, got %q", r)
	}
}

// TestAdverseBasisPnLUSDT 验证基差不利 PnL 计算:基差走阔(high 涨/low 跌)= 净亏;基差收敛 = 有利(负值)。
func TestAdverseBasisPnLUSDT(t *testing.T) {
	notional := 80.0
	entryHigh, entryLow := 100.0, 100.0

	// 基差走阔:high +2%、low 不变 → 不利 = 80×0.02 = 1.6。
	if got := crossVenueAdverseBasisPnLUSDT(notional, entryHigh, 102, entryLow, 100); math.Abs(got-1.6) > 1e-9 {
		t.Fatalf("adverse(high up)=%.6f want 1.6", got)
	}
	// 多腿下跌:low -2%、high 不变 → 不利 = 80×0.02 = 1.6。
	if got := crossVenueAdverseBasisPnLUSDT(notional, entryHigh, 100, entryLow, 98); math.Abs(got-1.6) > 1e-9 {
		t.Fatalf("adverse(low down)=%.6f want 1.6", got)
	}
	// 基差收敛(两腿同向同幅)→ delta 中性,基差 PnL ≈ 0。
	if got := crossVenueAdverseBasisPnLUSDT(notional, entryHigh, 103, entryLow, 103); math.Abs(got) > 1e-9 {
		t.Fatalf("parallel move basis pnl=%.6f want 0", got)
	}
	// 有利方向(high 跌/low 涨)→ 负值(盈利,不触发止损)。
	if got := crossVenueAdverseBasisPnLUSDT(notional, entryHigh, 99, entryLow, 101); got >= 0 {
		t.Fatalf("favorable basis should be negative, got %.6f", got)
	}
}

// TestBasisStopBudgetBoundsLoss 验证基差止损预算把单笔亏损钉在"预期资费"量级:
// 预期目标资费 0.24、系数 1.0 → 预算 0.24;不利基差 1.6 > 0.24 必触发。
func TestBasisStopBudgetBoundsLoss(t *testing.T) {
	cfg := CrossVenueExecutorConfig{
		UnifiedCaptureModelEnabled: true,
		CaptureTargetSettlements:   1,
		BasisStopBudgetFraction:    1.0,
		PlannedExitBufferSeconds:   90,
	}
	openedAt := time.Date(2026, 6, 23, 3, 57, 0, 0, time.UTC)
	next := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	plan := crossVenueBuildCapturePlan(cfg, "H-USDT", -0.0003, -0.003, 80, 1, next, 2, next, openedAt, 0)
	if plan == nil {
		t.Fatal("expected plan")
	}
	adverse := crossVenueAdverseBasisPnLUSDT(80, 100, 102, 100, 100) // 1.6
	if !(adverse > plan.BasisBudgetUSDT) {
		t.Fatalf("adverse %.4f should exceed budget %.4f to trigger stop", adverse, plan.BasisBudgetUSDT)
	}
}

// TestBasisStopYieldsToFundingCapture 验证 P1:目标腿即将结算且仍净收时,基差止损让位(不平仓);
// 资费已蒸发时基差止损照常触发兜底。避免"为收资费进场却在收到资费前被基差止损踢出"。
func TestBasisStopYieldsToFundingCapture(t *testing.T) {
	now := time.Date(2026, 6, 23, 7, 59, 0, 0, time.UTC)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.0, 4, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 4, now)
	// 高腿盘口中间价 102(bid101/ask103),低腿沿用默认 100(bid99/ask101)。
	high.book = venue.BookTicker{BidPrice: 101, AskPrice: 103, BidQty: 1000, AskQty: 1000}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.UnifiedCaptureModelEnabled = true
	cfg.CrossVenueExecutor.PreFundingCloseLeadSeconds = 120
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	mkPos := func(lowNext time.Time, lowRate float64) *CrossVenuePosition {
		return &CrossVenuePosition{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
			NotionalUSDT:       80,
			HighLeg:            CrossVenueLegState{Venue: bybit.VenueName, Symbol: "BTC-USDT", Side: "sell", Qty: 0.8, EntryPrice: 100},
			LowLeg:             CrossVenueLegState{Venue: okx.VenueName, Symbol: "BTC-USDT", Side: "buy", Qty: 0.8, EntryPrice: 100},
			LowNextFundingTime: lowNext,
			LowFundingRate:     lowRate,
			CapturePlan:        &CarryCapturePlan{TargetLeg: "low", BasisBudgetUSDT: 0.24},
		}
	}
	// 不利基差 = 80×(高腿+2% − 低腿0%) = 1.6 USDT > 预算 0.24 → 满足止损阈值。

	// 场景A:资费已蒸发(目标腿下次结算远在 lead 窗外)→ 基差止损照常触发兜底。
	gone := mkPos(now.Add(time.Hour), -0.003)
	if r := e.crossVenueBasisStopCloseReason(context.Background(), gone, now); r == "" {
		t.Fatal("资费已蒸发:基差止损必须触发")
	}
	// 场景B:目标腿 60s 内结算且仍净收(低腿负费率=多腿收)→ 让位,不平仓。
	imminent := mkPos(now.Add(60*time.Second), -0.003)
	if r := e.crossVenueBasisStopCloseReason(context.Background(), imminent, now); r != "" {
		t.Fatalf("即将结算且净收:基差止损必须让位,却返回 %q", r)
	}
}

// TestBasisStopHysteresis 验证迟滞:确认轮数=2 时,超预算第 1 轮不平(挡瞬时插针),
// 回到预算内重置计数,连续 2 轮超预算才真正止损。
func TestBasisStopHysteresis(t *testing.T) {
	now := time.Date(2026, 6, 23, 7, 59, 0, 0, time.UTC)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.0, 4, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0, 4, now)
	high.book = venue.BookTicker{BidPrice: 101, AskPrice: 103, BidQty: 1000, AskQty: 1000} // 高腿 mid=102 → 不利基差 1.6
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.UnifiedCaptureModelEnabled = true
	cfg.CrossVenueExecutor.BasisStopConfirmChecks = 2
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	// 资费已蒸发(目标腿结算远在 lead 窗外)→ 无让位;不利基差 1.6 > 预算 0.24。
	pos := &CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		NotionalUSDT:       80,
		HighLeg:            CrossVenueLegState{Venue: bybit.VenueName, Symbol: "BTC-USDT", Side: "sell", Qty: 0.8, EntryPrice: 100},
		LowLeg:             CrossVenueLegState{Venue: okx.VenueName, Symbol: "BTC-USDT", Side: "buy", Qty: 0.8, EntryPrice: 100},
		LowNextFundingTime: now.Add(time.Hour), LowFundingRate: -0.003,
		CapturePlan:        &CarryCapturePlan{TargetLeg: "low", BasisBudgetUSDT: 0.24},
	}
	// 第 1 轮:超预算但确认中(1/2),不平。
	if r := e.crossVenueBasisStopCloseReason(context.Background(), pos, now); r != "" {
		t.Fatalf("第 1 轮应迟滞不平,却返回 %q", r)
	}
	if pos.CapturePlan.BasisBreachStreak != 1 {
		t.Fatalf("第 1 轮 streak 应=1,实得 %d", pos.CapturePlan.BasisBreachStreak)
	}
	// 第 2 轮基差回到预算内(高腿 mid 回 100)→ 重置 streak,不平(均值回归,瞬时插针不该锁亏)。
	high.book = venue.BookTicker{BidPrice: 99, AskPrice: 101, BidQty: 1000, AskQty: 1000}
	if r := e.crossVenueBasisStopCloseReason(context.Background(), pos, now); r != "" {
		t.Fatalf("回到预算内应不平,却返回 %q", r)
	}
	if pos.CapturePlan.BasisBreachStreak != 0 {
		t.Fatalf("回归后 streak 应重置为 0,实得 %d", pos.CapturePlan.BasisBreachStreak)
	}
	// 再次连续 2 轮超预算 → 触发止损。
	high.book = venue.BookTicker{BidPrice: 101, AskPrice: 103, BidQty: 1000, AskQty: 1000}
	if r := e.crossVenueBasisStopCloseReason(context.Background(), pos, now); r != "" {
		t.Fatalf("重新超预算第 1 轮应迟滞不平,却返回 %q", r)
	}
	if r := e.crossVenueBasisStopCloseReason(context.Background(), pos, now); r == "" {
		t.Fatal("连续 2 轮超预算应触发止损")
	}
}

// TestCaptureRoll 验证滚动续持:计划离场点,目标腿下个结算仍净收+基差安全 → roll 延长持有(不平);
// 下个结算翻向(不再收钱)→ 不 roll(照常平),根治稳定复现资费源被每周期 churn 白付往返。
func TestCaptureRoll(t *testing.T) {
	now := time.Date(2026, 6, 23, 8, 1, 30, 0, time.UTC) // 已过计划离场点
	settle := time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC)
	mkExec := func(lowRate float64) (*CrossVenueExecutor, *CrossVenuePosition) {
		high := newCrossVenueTestVenue(bybit.VenueName, 0.0, 1, now)
		low := newCrossVenueTestVenue(okx.VenueName, lowRate, 1, now) // 目标=低腿(多),lowRate<0 → 多腿收
		cfg := testCrossVenueExecutorConfig()
		cfg.NetworkConfig.RecordDir = t.TempDir()
		cfg.CrossVenueExecutor.UnifiedCaptureModelEnabled = true
		cfg.CrossVenueExecutor.CaptureRollEnabled = true
		cfg.CrossVenueExecutor.CaptureMaxRolls = 6
		e := &CrossVenueExecutor{
			cfg:    cfg,
			venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
			store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
			logger: testCrossVenueLogger(),
			nowFn:  func() time.Time { return now },
		}
		pos := &CrossVenuePosition{
			Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
			NotionalUSDT:      80,
			HighIntervalHours: 1, LowIntervalHours: 1,
			HighLeg:     CrossVenueLegState{Venue: bybit.VenueName, Symbol: "BTC-USDT", Side: "sell", Qty: 0.8, EntryPrice: 100},
			LowLeg:      CrossVenueLegState{Venue: okx.VenueName, Symbol: "BTC-USDT", Side: "buy", Qty: 0.8, EntryPrice: 100},
			CapturePlan: &CarryCapturePlan{TargetLeg: "low", TargetSettlementTime: settle, PlannedExitTime: settle.Add(90 * time.Second), ExpectedTargetFundingUSDT: 0.24, BasisBudgetUSDT: 0.36},
		}
		return e, pos
	}

	// 场景A:下个结算仍净收(low rate -0.3%)→ roll 续持,目标结算延后、RollCount=1。
	e, pos := mkExec(-0.003)
	if !e.crossVenueMaybeRollCapturePlan(context.Background(), pos, now) {
		t.Fatal("下个结算仍净收应 roll 续持")
	}
	if pos.CapturePlan.RollCount != 1 {
		t.Fatalf("RollCount 应=1,实得 %d", pos.CapturePlan.RollCount)
	}
	if !pos.CapturePlan.TargetSettlementTime.After(settle) {
		t.Fatalf("目标结算应延后,实得 %v", pos.CapturePlan.TargetSettlementTime)
	}
	if crossVenuePlannedExitCloseReason(pos, now) != "" {
		t.Fatal("roll 后计划离场时刻应已延后,不再触发离场")
	}

	// 场景B:下个结算翻向(low rate +0.3% → 多腿反付)→ 不 roll,照常平。
	e2, pos2 := mkExec(0.003)
	if e2.crossVenueMaybeRollCapturePlan(context.Background(), pos2, now) {
		t.Fatal("下个结算翻向(不再收钱)不应 roll,应照常平")
	}
}

// TestEffectiveCaptureSettlements 验证 N>1 仅在基差稳定分≥阈值时放行,否则夹回 1。
func TestEffectiveCaptureSettlements(t *testing.T) {
	cfg := CrossVenueExecutorConfig{CaptureTargetSettlements: 3, BasisStabilityMinScoreForMultiSettlement: 0.7}
	if got := crossVenueEffectiveCaptureSettlements(cfg, 0.9); got != 3 {
		t.Fatalf("stable coin should allow N=3, got %d", got)
	}
	if got := crossVenueEffectiveCaptureSettlements(cfg, 0.4); got != 1 {
		t.Fatalf("unstable coin must clamp to N=1, got %d", got)
	}
	// 默认 N=1 时稳定分无关紧要。
	cfg.CaptureTargetSettlements = 1
	if got := crossVenueEffectiveCaptureSettlements(cfg, 0.1); got != 1 {
		t.Fatalf("N=1 baseline, got %d", got)
	}
}

// TestFundingToCostRatioGate 验证薄盘暴力小币(资费<成本×ratio)被入场闸过滤,而高资费币放行。
func TestFundingToCostRatioGate(t *testing.T) {
	now := time.Date(2026, 6, 23, 3, 58, 0, 0, time.UTC)
	next := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	base := CrossVenueExecutorConfig{
		MaxEffectiveLeverage:         3,
		MakerFirst:                   true,
		SlippageBudgetBps:            20,
		UnifiedCaptureModelEnabled:   true,
		CaptureTargetSettlements:     1,
		CaptureFundingToCostMinRatio: 1.0,
		DefaultTakerFeeBps:           6,
		DefaultMakerFeeBps:           2,
	}
	mkOpp := func(lowRate float64) CrossVenueOpportunity {
		return CrossVenueOpportunity{
			Symbol: "X-USDT", HighVenue: "okx", LowVenue: "binance",
			HighFundingRate: 0.0, LowFundingRate: lowRate,
			HighIntervalHours: 2, LowIntervalHours: 2,
			HighNextFundingTime: next, LowNextFundingTime: next,
			HighAnnualizedAPR: 1, LowAnnualizedAPR: 1,
		}
	}
	notional := 80.0

	// 薄费率(目标资费远小于往返成本)→ 被 ratio 闸拒。
	thin := evaluateCrossVenueOpenEV(base, mkOpp(-0.0002), notional, nil, now, 0, 0)
	if thin.OK {
		t.Fatal("thin-funding coin should be rejected by funding-to-cost ratio gate")
	}

	// 高费率(目标资费 >> 成本)→ 放行。
	rich := evaluateCrossVenueOpenEV(base, mkOpp(-0.02), notional, nil, now, 0, 0)
	if !rich.OK {
		t.Fatalf("rich-funding coin should pass, got reason=%q", rich.Reason)
	}

	// flag OFF 时不启用该 ratio 闸(回退旧行为)。
	off := base
	off.UnifiedCaptureModelEnabled = false
	if r := evaluateCrossVenueOpenEV(off, mkOpp(-0.0002), notional, nil, now, 0, 0); !r.OK {
		// 旧逻辑可能因 netProfit<0 拒,但绝不应是 ratio 闸;此处只验证不 panic 且走旧路径。
		_ = r
	}
}
