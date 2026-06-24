package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

// ---- #2 盘口闸 maker 化 ----

// TestMakerLegBookSpreadRelaxedUnderUnified 验证:统一模型 + maker-first 下,maker(高/首)腿盘口价差
// 可放宽到 MakerLegMaxBookSpreadBps;同样的宽盘口落在 taker(低)腿仍按严格 MaxBookSpreadBps 拒;
// 统一模型关闭时 maker 腿也按严格阈值拒(回退旧行为)。
func TestMakerLegBookSpreadRelaxedUnderUnified(t *testing.T) {
	now := time.Now().UTC()
	nextFunding := now.Add(120 * time.Second)
	wide := venue.BookTicker{BidPrice: 99.8, AskPrice: 100.2, BidQty: 1000, AskQty: 1000} // ~40bps
	tight := venue.BookTicker{BidPrice: 99.99, AskPrice: 100.01, BidQty: 1000, AskQty: 1000}

	runGate := func(unified bool, highBook, lowBook venue.BookTicker) crossVenueGateResult {
		high := newCrossVenueTestVenue(okx.VenueName, 0.004, 4, nextFunding)
		high.book = highBook
		high.depth = venueDepthDeep()
		low := newCrossVenueTestVenue(bybit.VenueName, 0.0001, 4, nextFunding)
		low.book = lowBook
		low.depth = venueDepthDeep()
		cfg := testNetProfitGateExecutorConfig()
		cfg.AssumeMakerFills = true
		cfg.ExpectedHoldHours = 12
		cfg.OpenFundingLeadSeconds = 180
		cfg.MaxBookSpreadBps = 30
		cfg.MakerLegMaxBookSpreadBps = 80
		cfg.UnifiedCaptureModelEnabled = unified
		return evaluateCrossVenueOpenGate(context.Background(), cfg, testCrossVenueGateDeps(1000), map[string]venue.Venue{
			okx.VenueName: high, bybit.VenueName: low,
		}, crossVenueGateInput{
			Opp: CrossVenueOpportunity{
				Symbol: "BTC-USDT", HighVenue: okx.VenueName, LowVenue: bybit.VenueName,
				SpreadAPR: 3.5, HighFundingRate: 0.004, LowFundingRate: 0.0001,
				HighIntervalHours: 4, LowIntervalHours: 4,
				HighNextFundingTime: nextFunding, LowNextFundingTime: nextFunding,
			},
			Now: now,
		})
	}

	// 统一模型 ON + maker 腿 40bps(≤80)→ 放行。
	if res := runGate(true, wide, tight); !res.OK {
		t.Fatalf("unified maker-first should allow 40bps maker leg, got reject: %s", res.Reason)
	}
	// 统一模型 ON 但 40bps 落在 taker(低)腿 → 严格 30bps 拒。
	if res := runGate(true, tight, wide); res.OK || !strings.Contains(res.Reason, "低费所盘口价差") {
		t.Fatalf("taker leg 40bps must reject strictly, ok=%v reason=%q", res.OK, res.Reason)
	}
	// 统一模型 OFF → maker 腿也按 30bps 拒(回退旧行为)。
	if res := runGate(false, wide, tight); res.OK || !strings.Contains(res.Reason, "高费所盘口价差") {
		t.Fatalf("unified OFF must reject 40bps high leg strictly, ok=%v reason=%q", res.OK, res.Reason)
	}
}

// ---- #3 单笔下注单元(基差预算封顶)----

// TestCapturePlanPerTradeLossCap 验证 PerTradeMaxLossPct 把基差止损预算钉死在 pct×名义 之内,
// 且只会往小里收(更早止损),不会放大;pct=0 时不封顶。
func TestCapturePlanPerTradeLossCap(t *testing.T) {
	base := CrossVenueExecutorConfig{
		UnifiedCaptureModelEnabled: true,
		CaptureTargetSettlements:   1,
		BasisStopBudgetFraction:    1.0,
		PlannedExitBufferSeconds:   90,
	}
	openedAt := time.Date(2026, 6, 23, 3, 57, 0, 0, time.UTC)
	next := time.Date(2026, 6, 23, 4, 0, 0, 0, time.UTC)
	notional := 80.0
	// 未封顶:budget = 1.0 × (0.003×80) = 0.24。
	if p := crossVenueBuildCapturePlan(base, "H-USDT", -0.0003, -0.003, notional, 1, next, 2, next, openedAt, 0); p == nil || p.BasisBudgetUSDT < 0.2399 || p.BasisBudgetUSDT > 0.2401 {
		t.Fatalf("uncapped budget want ~0.24, got %+v", p)
	}
	// 封顶生效:pct=0.001 → cap = 0.001×80 = 0.08 < 0.24 → 收到 0.08。
	capped := base
	capped.PerTradeMaxLossPct = 0.001
	p := crossVenueBuildCapturePlan(capped, "H-USDT", -0.0003, -0.003, notional, 1, next, 2, next, openedAt, 0)
	if p == nil {
		t.Fatal("expected non-nil plan")
	}
	if p.BasisBudgetUSDT < 0.0799 || p.BasisBudgetUSDT > 0.0801 {
		t.Fatalf("capped budget want ~0.08, got %.6f", p.BasisBudgetUSDT)
	}
	// 预期资费不受封顶影响(只收预算)。
	if p.ExpectedTargetFundingUSDT < 0.2399 || p.ExpectedTargetFundingUSDT > 0.2401 {
		t.Fatalf("expected funding should stay ~0.24, got %.6f", p.ExpectedTargetFundingUSDT)
	}
	// pct 较大时不收(cap=0.08×80=6.4 > 0.24)。
	loose := base
	loose.PerTradeMaxLossPct = 0.08
	if p := crossVenueBuildCapturePlan(loose, "H-USDT", -0.0003, -0.003, notional, 1, next, 2, next, openedAt, 0); p == nil || p.BasisBudgetUSDT < 0.2399 || p.BasisBudgetUSDT > 0.2401 {
		t.Fatalf("loose cap should not shrink budget, got %+v", p)
	}
}

// ---- #3 当日错次预算 ----

func TestCountLosingSettlementsInRange(t *testing.T) {
	day := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	mk := func(net float64, hour int) strategySettlementRecord {
		return strategySettlementRecord{
			StrategyType: "cross_venue_perp_spot", // 不在 trust switch 内 → 仅 CloseTime 判据
			CloseTime:    day.Add(time.Duration(hour) * time.Hour),
			NetPnL:       net,
		}
	}
	records := []strategySettlementRecord{
		mk(-0.5, 1), mk(0.3, 2), mk(-1.2, 3), mk(0, 4), mk(-0.01, 5),
		// 窗口外(前一天)不计:
		{StrategyType: "cross_venue_perp_spot", CloseTime: day.Add(-2 * time.Hour), NetPnL: -9},
	}
	got := countLosingSettlementsInRange(records, day, day.Add(24*time.Hour))
	if got != 3 {
		t.Fatalf("losing trades in day = %d, want 3", got)
	}
}

func TestDailyLosingTradesHardHalt(t *testing.T) {
	cfg := RiskConfig{DailyMaxLosingTrades: 3}
	base := accountRiskSnapshot{EquityDataTrusted: true}

	below := base
	below.DailyLosingTradeCount = 2
	if intent := evaluateAccountHardHaltIntent(below, cfg); intent.ShouldHalt {
		t.Fatalf("2 < 3 losing trades should not halt, got %+v", intent)
	}

	at := base
	at.DailyLosingTradeCount = 3
	intent := evaluateAccountHardHaltIntent(at, cfg)
	if !intent.ShouldHalt || !intent.DailyLosingTradesHit {
		t.Fatalf("3 >= 3 losing trades should halt, got %+v", intent)
	}

	// 0=关闭该维度。
	off := cfg
	off.DailyMaxLosingTrades = 0
	if intent := evaluateAccountHardHaltIntent(at, off); intent.DailyLosingTradesHit {
		t.Fatalf("DailyMaxLosingTrades=0 must disable the dimension, got %+v", intent)
	}
}

// ---- #4 ViolenceScore ----

func TestCrossVenueViolenceScore(t *testing.T) {
	// 真暴力币:高幅度(spreadAPR 4.0 → mag=1)且高持续(hq=1, ss=1 → persistence=1)→ ~1。
	if s := crossVenueViolenceScore(4.0, 1.0, 1.0); s < 0.99 || s > 1.0001 {
		t.Fatalf("violent+persistent want ~1.0, got %.4f", s)
	}
	// 瞬时尖刺:高幅度但低持续(hq=0, ss=0)→ ~0。
	if s := crossVenueViolenceScore(4.0, 0, 0); s > 1e-9 {
		t.Fatalf("violent but transient want ~0, got %.4f", s)
	}
	// 同样持续性下,幅度越大分越高(单调)。
	lo := crossVenueViolenceScore(0.5, 0.8, 0.8)
	hi := crossVenueViolenceScore(1.5, 0.8, 0.8)
	if !(hi > lo) {
		t.Fatalf("violence score should increase with magnitude: lo=%.4f hi=%.4f", lo, hi)
	}
	// 非正幅度 → 0。
	if s := crossVenueViolenceScore(0, 1, 1); s != 0 {
		t.Fatalf("non-positive spread want 0, got %.4f", s)
	}
}

// ---- #4b ViolenceScore 软调仓 + 软排序 ----

// TestViolenceSoftBoostSizingAndRanking 验证:统一模型下高 violence 机会的 SizeMultiplier/RankScore
// 被软上调(向满仓/前排靠拢);clamp01 封顶不超额;violence=0 的瞬时尖刺不受影响;权重=0 关闭。
func TestViolenceSoftBoostSizingAndRanking(t *testing.T) {
	mk := func(violence float64) CrossVenueOpportunity {
		return CrossVenueOpportunity{
			Symbol: "BTC-USDT", HighVenue: "okx", LowVenue: "bybit",
			NetEV: 5.0, ViolenceScore: violence,
			ExecutionScore: 0.8, LiquidityScore: 0.8, RecoveryQualityScore: 0.8,
			CapitalEfficiencyEV: 0.8, HistoryQualityScore: 0.8, StabilityScore: 0.8,
			TradableNotional: 100,
		}
	}
	cfgOn := CrossVenueExecutorConfig{
		UnifiedCaptureModelEnabled: true,
		ViolenceSizeBoostWeight:    0.3,
		ViolenceRankBoostWeight:    0.3,
		MinNetProfitUSDT:           0,
	}
	cfgOff := cfgOn
	cfgOff.ViolenceSizeBoostWeight = 0
	cfgOff.ViolenceRankBoostWeight = 0

	// 高 violence vs 零 violence(同其余画像)。
	high := []CrossVenueOpportunity{mk(0.9)}
	zero := []CrossVenueOpportunity{mk(0)}
	crossVenueFinalizeOpportunityRanking(cfgOn, high)
	crossVenueFinalizeOpportunityRanking(cfgOn, zero)
	if !(high[0].SizeMultiplier > zero[0].SizeMultiplier) {
		t.Fatalf("high violence should size up: high=%.4f zero=%.4f", high[0].SizeMultiplier, zero[0].SizeMultiplier)
	}
	if !(high[0].RankScore > zero[0].RankScore) {
		t.Fatalf("high violence should rank higher: high=%.4f zero=%.4f", high[0].RankScore, zero[0].RankScore)
	}
	if high[0].SizeMultiplier > 1.0+1e-9 {
		t.Fatalf("size multiplier must be clamped at 1.0, got %.4f", high[0].SizeMultiplier)
	}

	// 权重=0 → 与零 violence 同结果(关闭)。
	off := []CrossVenueOpportunity{mk(0.9)}
	crossVenueFinalizeOpportunityRanking(cfgOff, off)
	if off[0].SizeMultiplier != zero[0].SizeMultiplier {
		t.Fatalf("weight=0 should disable size boost: off=%.4f zero=%.4f", off[0].SizeMultiplier, zero[0].SizeMultiplier)
	}
}

// ---- #1b 错过机会复盘:真暴力币漏单聚合 ----

func TestAggregateMissedOpportunities(t *testing.T) {
	records := []MissedOpportunity{
		// 真暴力币被 cost 挡 → 计入 high-violence + cost/liq headline。
		{RejectCategory: "cost_ev", BlockingLayer: "cost", ViolenceScore: 0.8},
		// 真暴力币被 liquidity 挡 → 计入 high-violence + cost/liq headline。
		{RejectCategory: "liquidity_book_spread", BlockingLayer: "liquidity", ViolenceScore: 0.5},
		// 真暴力币但 benign 推迟 → 不算真漏单(剔除)。
		{RejectCategory: "funding_window", BlockingLayer: "window", ViolenceScore: 0.9, BenignTiming: true},
		// 低暴力度被 cost 挡 → 不计 high-violence。
		{RejectCategory: "cost_ev", BlockingLayer: "cost", ViolenceScore: 0.1},
		// 真暴力币被 execution 挡 → 计 high-violence,但不计 cost/liq headline。
		{RejectCategory: "exec_leg_failure", BlockingLayer: "execution", ViolenceScore: 0.7},
	}
	agg := aggregateMissedOpportunities(records, missedOpportunityHighViolenceThreshold)
	if agg.ByLayer["cost"] != 2 {
		t.Fatalf("by_layer cost = %d, want 2", agg.ByLayer["cost"])
	}
	if agg.HighViolenceCount != 3 {
		t.Fatalf("high_violence_count = %d, want 3 (0.8,0.5,0.7; benign excluded; 0.1 excluded)", agg.HighViolenceCount)
	}
	if agg.HighViolenceBlockedByCostLiq != 2 {
		t.Fatalf("high_violence_blocked_by_cost_liq = %d, want 2", agg.HighViolenceBlockedByCostLiq)
	}
	if agg.HighViolenceByLayer["execution"] != 1 {
		t.Fatalf("high_violence_by_layer execution = %d, want 1", agg.HighViolenceByLayer["execution"])
	}
	if agg.HighViolenceByLayer["window"] != 0 {
		t.Fatalf("benign window should be excluded from high-violence, got %d", agg.HighViolenceByLayer["window"])
	}
}

// ---- #1 错过机会复盘:拒绝原因归类 ----

func TestClassifyOpenReject(t *testing.T) {
	cases := []struct {
		reason   string
		category string
		layer    string
	}{
		{"开仓闸: 高费所盘口价差 40.0bps 超限(maker上限 80)", "liquidity_book_spread", "liquidity"},
		{"资费/成本比 0.42 < 1.00", "cost_ev", "cost"},
		{"funding_entry_window_not_reached", "funding_window", "window"},
		{"okx: 下单数量未对齐 lot size", "exec_lot_size", "execution"},
		{"symbol 已被 Bybit 单所引擎持有", "symbol_held", "control"},
		{"急停(.halt-live)生效,禁止开新仓", "halt", "control"},
		{"完全没见过的奇怪原因", "other", "other"},
	}
	for _, c := range cases {
		cat, layer, cf := classifyOpenReject(c.reason)
		if cat != c.category || layer != c.layer {
			t.Fatalf("classify(%q) = (%q,%q), want (%q,%q)", c.reason, cat, layer, c.category, c.layer)
		}
		if strings.TrimSpace(cf) == "" {
			t.Fatalf("classify(%q) returned empty counterfactual", c.reason)
		}
	}
}
