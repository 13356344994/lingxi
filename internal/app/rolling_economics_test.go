package app

import (
	"math"
	"testing"
)

// rolling_economics_test.go 是 M-S4.5 经济回归(README §16.10 退出门):在同一 funding 费率序列上对比
// 「单周期」与「滚动续持」的累计净利,证明滚动续持省下的往返成本 + 多吃的「续持费率带」funding 真实落地,
// 且在费率转差时能正确平仓、绝不劣于单周期。复用实盘同一条 evaluateRollDecision 判据,口径与实盘一致。

const economicEps = 1e-9

func approxEqualEcon(a, b float64) bool { return math.Abs(a-b) <= economicEps }

// 测试用成本/门槛:往返一次性成本 = 开 0.0015 + 平 0.0020 = 0.0035;入场门槛 0.0055 远高于续持门槛 0.0010。
var (
	testEconCosts = economicCostModel{OpenCostRate: 0.0015, CloseCostRate: 0.0020}
	testEconTh    = economicThresholds{EntryRate: 0.0055, HoldRollThresholdRate: 0.0010, RollSafetyBufferRate: 0.0005}
)

func evenEvents(rate, cost float64, n int) []economicFundingEvent {
	events := make([]economicFundingEvent, n)
	for i := range events {
		events[i] = economicFundingEvent{NetFundingRate: rate, MarginalHoldCostRate: cost}
	}
	return events
}

// TestEconomicsRollingWinsOnPersistentProfit 钉死核心命题:全程明显盈利(5 周期 @0.008)时,单周期每周期开平(5 往返),
// 滚动续持只开平一次,净利优势 = 省下的 (N−1) 次往返成本,可精确对账。
func TestEconomicsRollingWinsOnPersistentProfit(t *testing.T) {
	const notional = 10000.0
	sc := economicSymbolScenario{Symbol: "BTCUSDT", Notional: notional, Events: evenEvents(0.0080, 0, 5)}
	cmp := compareEconomics([]economicSymbolScenario{sc}, testEconCosts, testEconTh)
	t.Log(cmp.summaryLine())

	if cmp.SingleCycle.OpenCount != 5 || cmp.SingleCycle.FundingCycles != 5 {
		t.Fatalf("单周期应开平 5 次、吃 5 周期 funding, got open=%d funding=%d", cmp.SingleCycle.OpenCount, cmp.SingleCycle.FundingCycles)
	}
	if cmp.RollingHold.OpenCount != 1 || cmp.RollingHold.CloseCount != 1 || cmp.RollingHold.FundingCycles != 5 {
		t.Fatalf("滚动续持应只开平 1 次、吃 5 周期 funding, got open=%d close=%d funding=%d",
			cmp.RollingHold.OpenCount, cmp.RollingHold.CloseCount, cmp.RollingHold.FundingCycles)
	}
	// 两者 funding 一致(均 5 周期);差异纯来自往返成本。
	if !approxEqualEcon(cmp.SingleCycle.FundingIncome, cmp.RollingHold.FundingIncome) {
		t.Fatalf("两策略 funding 应相同, single=%.6f rolling=%.6f", cmp.SingleCycle.FundingIncome, cmp.RollingHold.FundingIncome)
	}
	if cmp.SavedRoundTrips != 4 {
		t.Fatalf("应省 4 次往返, got %d", cmp.SavedRoundTrips)
	}
	wantDelta := 4 * notional * (testEconCosts.OpenCostRate + testEconCosts.CloseCostRate) // 4×10000×0.0035 = 140
	if !approxEqualEcon(cmp.NetPnLDelta, wantDelta) {
		t.Fatalf("净利优势应等于省下的 4 次往返成本 %.6f, got %.6f", wantDelta, cmp.NetPnLDelta)
	}
	if cmp.NetPnLDelta <= 0 {
		t.Fatalf("滚动续持净利应严格优于单周期, Δ=%.6f", cmp.NetPnLDelta)
	}
}

// TestEconomicsRollingCapturesRollBand 钉死「续持费率带」收益:费率序列 0.008,0.008,0.003,0.003,0.008——
// 0.003 低于入场门槛(单周期会跳过,错过 funding)但高于续持门槛(滚动续持继续持有并吃到)。
// 滚动续持因此既省往返、又多吃 2 个周期的 funding。
func TestEconomicsRollingCapturesRollBand(t *testing.T) {
	const notional = 10000.0
	sc := economicSymbolScenario{Symbol: "ETHUSDT", Notional: notional, Events: []economicFundingEvent{
		{NetFundingRate: 0.0080}, {NetFundingRate: 0.0080},
		{NetFundingRate: 0.0030}, {NetFundingRate: 0.0030},
		{NetFundingRate: 0.0080},
	}}
	cmp := compareEconomics([]economicSymbolScenario{sc}, testEconCosts, testEconTh)

	if cmp.SingleCycle.FundingCycles != 3 {
		t.Fatalf("单周期应只在 3 个越过入场门槛的周期开仓吃 funding, got %d", cmp.SingleCycle.FundingCycles)
	}
	if cmp.RollingHold.FundingCycles != 5 {
		t.Fatalf("滚动续持应全程持有吃 5 周期 funding(含 2 个续持带), got %d", cmp.RollingHold.FundingCycles)
	}
	if cmp.RollingHold.OpenCount != 1 {
		t.Fatalf("滚动续持应只开 1 次(全程未平), got %d", cmp.RollingHold.OpenCount)
	}
	// 单周期 funding = 3×80 = 240;滚动 = 80+80+30+30+80 = 300。滚动多吃 60。
	if !approxEqualEcon(cmp.RollingHold.FundingIncome-cmp.SingleCycle.FundingIncome, 2*notional*0.0030) {
		t.Fatalf("滚动应多吃 2 个续持带周期的 funding(=60), got Δfunding=%.6f", cmp.RollingHold.FundingIncome-cmp.SingleCycle.FundingIncome)
	}
	if cmp.NetPnLDelta <= 0 {
		t.Fatalf("滚动续持净利应优于单周期, Δ=%.6f", cmp.NetPnLDelta)
	}
}

// TestEconomicsRollingDropsOnBadTail 钉死 fail-safe:费率在前两周期盈利后跌到续持门槛之下(0.0002),
// 滚动续持应在转差前果断平仓、不持有进入亏损/薄边际周期 → 在仓周期数与单周期相同,仍因省往返而净利占优。
func TestEconomicsRollingDropsOnBadTail(t *testing.T) {
	const notional = 10000.0
	sc := economicSymbolScenario{Symbol: "SOLUSDT", Notional: notional, Events: []economicFundingEvent{
		{NetFundingRate: 0.0080}, {NetFundingRate: 0.0080},
		{NetFundingRate: 0.0002}, {NetFundingRate: 0.0002},
	}}
	cmp := compareEconomics([]economicSymbolScenario{sc}, testEconCosts, testEconTh)

	if cmp.RollingHold.FundingCycles != 2 {
		t.Fatalf("滚动续持应在费率转差前平仓、只吃 2 个周期, got %d", cmp.RollingHold.FundingCycles)
	}
	if cmp.RollingHold.OpenCount != 1 || cmp.RollingHold.CloseCount != 1 {
		t.Fatalf("滚动续持应开 1 平 1(转差即退出), got open=%d close=%d", cmp.RollingHold.OpenCount, cmp.RollingHold.CloseCount)
	}
	if cmp.SingleCycle.FundingCycles != 2 {
		t.Fatalf("单周期同样只在 2 个达标周期开仓, got %d", cmp.SingleCycle.FundingCycles)
	}
	if cmp.NetPnLDelta < 0 {
		t.Fatalf("即便费率转差,滚动续持也不应劣于单周期, Δ=%.6f", cmp.NetPnLDelta)
	}
}

// TestEconomicsSingleEventIdentical 钉死边界:只有 1 个 funding 周期时两策略完全等价(都开 1 平 1),净利差为 0。
func TestEconomicsSingleEventIdentical(t *testing.T) {
	sc := economicSymbolScenario{Symbol: "BTCUSDT", Notional: 10000, Events: evenEvents(0.0080, 0, 1)}
	cmp := compareEconomics([]economicSymbolScenario{sc}, testEconCosts, testEconTh)
	if cmp.RollingHold.OpenCount != 1 || cmp.SingleCycle.OpenCount != 1 {
		t.Fatalf("单周期场景两策略都应只开 1 次, single=%d rolling=%d", cmp.SingleCycle.OpenCount, cmp.RollingHold.OpenCount)
	}
	if !approxEqualEcon(cmp.NetPnLDelta, 0) {
		t.Fatalf("单周期场景净利差应为 0, got %.6f", cmp.NetPnLDelta)
	}
}

// TestEconomicsNegativeCarryBorrowAccounted 钉死负向 carry 借币:每周期边际借币 0.0020,两策略持有周期相同时
// 借币成本相同,净利差仍纯来自省下的往返成本(借币不抵消滚动的优势)。
func TestEconomicsNegativeCarryBorrowAccounted(t *testing.T) {
	const notional = 10000.0
	sc := economicSymbolScenario{Symbol: "DOGEUSDT", Notional: notional, Events: evenEvents(0.0080, 0.0020, 3)}
	cmp := compareEconomics([]economicSymbolScenario{sc}, testEconCosts, testEconTh)

	if !approxEqualEcon(cmp.SingleCycle.BorrowCosts, cmp.RollingHold.BorrowCosts) {
		t.Fatalf("两策略持有 3 周期,借币成本应相同, single=%.6f rolling=%.6f", cmp.SingleCycle.BorrowCosts, cmp.RollingHold.BorrowCosts)
	}
	if !approxEqualEcon(cmp.RollingHold.BorrowCosts, 3*notional*0.0020) {
		t.Fatalf("滚动借币应为 3×20=60, got %.6f", cmp.RollingHold.BorrowCosts)
	}
	wantDelta := 2 * notional * (testEconCosts.OpenCostRate + testEconCosts.CloseCostRate) // 省 2 次往返
	if !approxEqualEcon(cmp.NetPnLDelta, wantDelta) {
		t.Fatalf("净利差应为省下的 2 次往返成本 %.6f, got %.6f", wantDelta, cmp.NetPnLDelta)
	}
}

// TestEconomicsPortfolioAggregation 钉死多币组合汇总:跨币结果按字段相加,组合净利差 = 各币净利差之和。
func TestEconomicsPortfolioAggregation(t *testing.T) {
	scenarios := []economicSymbolScenario{
		{Symbol: "BTCUSDT", Notional: 10000, Events: evenEvents(0.0080, 0, 4)},
		{Symbol: "ETHUSDT", Notional: 5000, Events: evenEvents(0.0080, 0, 3)},
	}
	cmp := compareEconomics(scenarios, testEconCosts, testEconTh)
	// BTC 省 3 往返×10000、ETH 省 2 往返×5000。
	wantSaved := 3 + 2
	if cmp.SavedRoundTrips != wantSaved {
		t.Fatalf("组合应省 %d 次往返, got %d", wantSaved, cmp.SavedRoundTrips)
	}
	rt := testEconCosts.OpenCostRate + testEconCosts.CloseCostRate
	wantDelta := 3*10000*rt + 2*5000*rt
	if !approxEqualEcon(cmp.NetPnLDelta, wantDelta) {
		t.Fatalf("组合净利差应为各币之和 %.6f, got %.6f", wantDelta, cmp.NetPnLDelta)
	}
}

// TestEconomicsConfigDerivedModelSane 钉死从默认配置折算的成本/门槛口径:往返成本对半分且为正,
// 入场门槛 = 盈亏平衡线(coherentMinFundingRate),与实盘海选门槛一致。
func TestEconomicsConfigDerivedModelSane(t *testing.T) {
	cfg := DefaultEngineConfig()
	costs := economicCostModelFromConfig(cfg)
	if costs.OpenCostRate <= 0 || costs.CloseCostRate <= 0 {
		t.Fatalf("折算成本应为正, got %+v", costs)
	}
	if costs.CloseCostRate < costs.OpenCostRate {
		t.Fatalf("平仓侧含退场点差,应 ≥ 开仓侧, open=%.6f close=%.6f", costs.OpenCostRate, costs.CloseCostRate)
	}
	th := economicThresholdsFromConfig(cfg)
	if !approxEqualEcon(th.EntryRate, coherentMinFundingRate(cfg)) {
		t.Fatalf("入场门槛应等于盈亏平衡线 %.6f, got %.6f", coherentMinFundingRate(cfg), th.EntryRate)
	}
}
