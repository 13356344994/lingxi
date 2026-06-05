package app

import (
	"fmt"
	"time"
)

// rolling_economics.go 提供「单周期 vs 滚动续持」的确定性经济回归仿真(M-S4.5)——README §16.10 的退出门:
// 在同一 funding 费率序列上对比两种策略的累计净利,量化「滚动续持省下的往返成本是否真实落地」。
//
// 仿真为纯函数:无网络、无引擎状态、确定性。它复用实盘同一条续持判据 evaluateRollDecision(同口径,避免
// 「测试用一套简化逻辑、实盘用另一套」的偏差),成本用真实费率常数(往返手续费/点差)折算。两种策略走同一
// 仿真主循环,唯一差异是「在仓时是否续持到下一周期」的判据:单周期恒平仓;滚动按 evaluateRollDecision。
//
// 经济直觉:新开必须覆盖完整往返(≈ coherentMinFundingRate ≈ 0.0055),而续持只需覆盖「多持一周期的边际成本」
// (HoldRollThresholdRate,远低)。故存在一段「续持有利但不值得新开」的费率带——滚动续持在这段里继续吃 funding,
// 而单周期会平掉、错过。滚动的净利优势 = 省下的往返成本 + 这段费率带多吃的 funding − 多持的边际借币。

// economicFundingEvent 是某币在一个 funding 结算点的输入:NetFundingRate 是已按持仓 carry 方向归一的「净资金费率」
// (对我有利为正);MarginalHoldCostRate 是「多持一周期」的边际成本率(负向 carry 借币利息;正向 carry≈0,≥0)。
// 本仿真取 estimate=actual(完美前瞻),使续持判据(看下期预期)与实际收益自洽、结果确定可断言。
type economicFundingEvent struct {
	FundingTime          time.Time
	NetFundingRate       float64
	MarginalHoldCostRate float64
}

// economicSymbolScenario 是单币在一段连续 funding 周期上的费率序列(名义额固定)。组合场景是多个单币场景之和。
type economicSymbolScenario struct {
	Symbol   string
	Notional float64
	Events   []economicFundingEvent
}

// economicCostModel 是开/平两侧各一次性的交易成本率,从真实费率常数折算(均以名义额为基数):
//   - 开仓侧 ≈ 现货 taker + 合约 taker + 进场点差(+ slip/impact,可由调用方加进来);
//   - 平仓侧 ≈ 现货 taker + 合约 taker + 退场点差(ExitSpreadReserveRate)(+ slip/impact)。
//
// 滚动续持每少做一次「开+平」,就省下 (OpenCostRate + CloseCostRate) × Notional——这正是本回归要量化的收益来源。
type economicCostModel struct {
	OpenCostRate  float64
	CloseCostRate float64
}

// economicThresholds 是两道门槛:EntryRate 为新开入场门槛(净资金费率须 ≥ 此值才新开,覆盖完整往返);
// HoldRollThresholdRate / RollSafetyBufferRate 为续持两道闸(喂给 evaluateRollDecision,远低于入场门槛)。
type economicThresholds struct {
	EntryRate             float64
	HoldRollThresholdRate float64
	RollSafetyBufferRate  float64
}

// economicResult 汇总一种策略在整段序列上的经济结果。NetPnL = FundingIncome − BorrowCosts − TradingCosts。
type economicResult struct {
	Strategy      string
	FundingIncome float64
	BorrowCosts   float64
	TradingCosts  float64 // 累计「开+平」一次性成本
	NetPnL        float64
	OpenCount     int
	CloseCount    int
	FundingCycles int // 实际在仓、吃到 funding 的周期数
}

// economicComparison 是两种策略在同一组场景上的对比结果。NetPnLDelta ≥ 0 即证明滚动续持不劣于单周期;
// SavedRoundTrips 是省下的开仓(往返)次数,是净利优势的主要来源。
type economicComparison struct {
	SingleCycle     economicResult
	RollingHold     economicResult
	NetPnLDelta     float64
	SavedRoundTrips int
}

// simulateSymbolEconomics 在单币序列上仿真一种策略。keepForNext 决定「在仓时是否续持到下一周期」:
// 单周期传 nil(恒平仓);滚动传基于 evaluateRollDecision 的判据。入场门槛 EntryRate 对两种策略一视同仁
// (未持仓时须过入场门槛才新开),保证 funding 捕获的差异只来自「续持」本身。纯函数、确定性。
func simulateSymbolEconomics(sc economicSymbolScenario, costs economicCostModel, th economicThresholds, keepForNext func(next economicFundingEvent) bool) economicResult {
	res := economicResult{}
	held := false
	for i := range sc.Events {
		ev := sc.Events[i]
		if !held {
			if ev.NetFundingRate < th.EntryRate {
				continue // 未过入场门槛 → 本周期不新开(两种策略同口径)
			}
			res.OpenCount++
			res.TradingCosts += sc.Notional * costs.OpenCostRate
			held = true
		}
		// 在仓本周期:吃 funding − 多持一周期的边际借币。
		res.FundingIncome += sc.Notional * ev.NetFundingRate
		holdCost := ev.MarginalHoldCostRate
		if holdCost < 0 {
			holdCost = 0
		}
		res.BorrowCosts += sc.Notional * holdCost
		res.FundingCycles++
		// 决定是否续持到下一周期(末周期无下期 → 必平)。
		keep := false
		if keepForNext != nil && i+1 < len(sc.Events) {
			keep = keepForNext(sc.Events[i+1])
		}
		if !keep {
			res.CloseCount++
			res.TradingCosts += sc.Notional * costs.CloseCostRate
			held = false
		}
	}
	if held {
		// 收尾兜底:正常情况下末周期判定已平仓(keepForNext 在 i+1==len 时不被调用),此处仅防御未来改动。
		res.CloseCount++
		res.TradingCosts += sc.Notional * costs.CloseCostRate
	}
	res.NetPnL = res.FundingIncome - res.BorrowCosts - res.TradingCosts
	return res
}

// simulateSingleCycleEconomics 仿真单周期策略:每个 funding 周期独立「开→吃一周期 funding→平」,从不续持。
func simulateSingleCycleEconomics(sc economicSymbolScenario, costs economicCostModel, th economicThresholds) economicResult {
	r := simulateSymbolEconomics(sc, costs, th, nil)
	r.Strategy = "single_cycle"
	return r
}

// simulateRollingHoldEconomics 仿真滚动续持策略:开仓后每个结算边界用实盘同一条 evaluateRollDecision 判定是否续持,
// 续持则省去本次「平+下次开」,只多担一周期边际借币。判据用「下期」事件的费率(与 :55 复核的前瞻口径一致)。
func simulateRollingHoldEconomics(sc economicSymbolScenario, costs economicCostModel, th economicThresholds) economicResult {
	keepForNext := func(next economicFundingEvent) bool {
		return evaluateRollDecision(rollDecisionInput{
			Symbol:                     sc.Symbol,
			ExpectedNextNetFundingRate: next.NetFundingRate,
			MarginalHoldCostRate:       next.MarginalHoldCostRate,
			HoldRollThresholdRate:      th.HoldRollThresholdRate,
			RollSafetyBufferRate:       th.RollSafetyBufferRate,
		}).Keep
	}
	r := simulateSymbolEconomics(sc, costs, th, keepForNext)
	r.Strategy = "rolling_hold"
	return r
}

// compareEconomics 在同一组场景上分别仿真两种策略并汇总对比。NetPnLDelta = 滚动 − 单周期(应 ≥ 0);
// SavedRoundTrips = 单周期开仓数 − 滚动开仓数。是 M-S4.5 经济回归的核心断言对象。
func compareEconomics(scenarios []economicSymbolScenario, costs economicCostModel, th economicThresholds) economicComparison {
	single := economicResult{Strategy: "single_cycle"}
	rolling := economicResult{Strategy: "rolling_hold"}
	for _, sc := range scenarios {
		single = addEconomicResult(single, simulateSingleCycleEconomics(sc, costs, th))
		rolling = addEconomicResult(rolling, simulateRollingHoldEconomics(sc, costs, th))
	}
	return economicComparison{
		SingleCycle:     single,
		RollingHold:     rolling,
		NetPnLDelta:     rolling.NetPnL - single.NetPnL,
		SavedRoundTrips: single.OpenCount - rolling.OpenCount,
	}
}

// addEconomicResult 把 b 的各计数/金额累加到 a(保留 a.Strategy),用于跨币组合汇总。
func addEconomicResult(a, b economicResult) economicResult {
	a.FundingIncome += b.FundingIncome
	a.BorrowCosts += b.BorrowCosts
	a.TradingCosts += b.TradingCosts
	a.NetPnL += b.NetPnL
	a.OpenCount += b.OpenCount
	a.CloseCount += b.CloseCount
	a.FundingCycles += b.FundingCycles
	return a
}

// economicCostModelFromConfig 从真实费率常数折算成本模型:往返手续费(FeeReserveRate)按开/平对半分,
// 平仓侧额外加退场点差(ExitSpreadReserveRate)。市场相关的 slip/impact 不在此默认里(由调用方按场景加进来)。
func economicCostModelFromConfig(cfg EngineConfig) economicCostModel {
	roundTripFee := cfg.StrategyConfig.FeeReserveRate
	if roundTripFee < 0 {
		roundTripFee = 0
	}
	return economicCostModel{
		OpenCostRate:  roundTripFee / 2,
		CloseCostRate: roundTripFee/2 + maxFloat(cfg.StrategyConfig.ExitSpreadReserveRate, 0),
	}
}

// economicThresholdsFromConfig 从配置取门槛:入场门槛复用实盘海选的 coherentMinFundingRate(盈亏平衡兜底),
// 续持两道闸取 PortfolioConfig 的 HoldRollThresholdRate / RollSafetyBufferRate。
func economicThresholdsFromConfig(cfg EngineConfig) economicThresholds {
	return economicThresholds{
		EntryRate:             coherentMinFundingRate(cfg),
		HoldRollThresholdRate: cfg.PortfolioConfig.HoldRollThresholdRate,
		RollSafetyBufferRate:  cfg.PortfolioConfig.RollSafetyBufferRate,
	}
}

// summaryLine 返回一行人类可读的对比摘要,供回归日志/报告打印。
func (c economicComparison) summaryLine() string {
	return fmt.Sprintf(
		"经济回归: 单周期 NetPnL=%.4f(开%d/funding%d) vs 滚动续持 NetPnL=%.4f(开%d/funding%d) → Δ=%.4f, 省往返=%d",
		c.SingleCycle.NetPnL, c.SingleCycle.OpenCount, c.SingleCycle.FundingCycles,
		c.RollingHold.NetPnL, c.RollingHold.OpenCount, c.RollingHold.FundingCycles,
		c.NetPnLDelta, c.SavedRoundTrips,
	)
}
