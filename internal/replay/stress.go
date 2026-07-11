package replay

import (
	"math"

	"lingxi/internal/money"
)

// StressPosition 压力测试输入:账户当前一条持仓的快照(与真实持仓解耦的纯输入)。
type StressPosition struct {
	Symbol       string
	Side         Side
	NotionalUSDT float64
	// Beta 相对簇领头标的(如 BTC)的敏感度;压力场景「相关性=1」下所有仓位
	// 以 Beta×冲击 同向移动。<=0 视为 1。
	Beta float64
}

// StressAccount 压力测试的账户输入。
type StressAccount struct {
	EquityUSDT float64
	// PendingOutflowUSDT 已发起未确认的转出:压力下视为已离开偿付缓冲(计划文档第 2 节)。
	PendingOutflowUSDT float64
	Positions          []StressPosition
}

// StressScenario 一个账户级压力场景。所有比率为小数(0.30=30%)。
type StressScenario struct {
	Name string
	// MarketMovePct 簇领头标的的价格冲击(负=暴跌,正=暴涨);相关性=1:
	// 每条仓位按 Beta×MarketMovePct 同向受击。
	MarketMovePct float64
	// SideFilter 非空时只冲击该方向仓位(全多暴跌用 MarketMovePct<0 + 不过滤即可;
	// 保留过滤器供构造非对称场景)。
	SideFilter Side
	// ExitFrictionRate 退出摩擦率(流动性蒸发/退出拥堵):平仓时对全部受击名义
	// 额外计提的损耗比例(盘口消失时远大于常态 taker 费)。
	ExitFrictionRate float64
	// FrozenDurationHoldPct API 断流:断流期间无法平仓,价格继续对我方不利移动的
	// 附加冲击比例(叠加在 MarketMovePct 之上,同号放大)。
	FrozenDurationHoldPct float64
	// CountPendingOutflow 是否把 pending 转出从缓冲中扣除(API 断流/提现拥堵场景为 true)。
	CountPendingOutflow bool
}

// StressResult 单场景结果。
type StressResult struct {
	Scenario           string
	LossUSDT           float64 // 场景总损失(正=亏)
	SurvivingBufferPct float64 // 剩余偿付缓冲 / 原权益;<=0 即破产
	Ruined             bool
}

// DefaultStressScenarios 计划文档第 5 节要求的最小压力集
// (相关性=1、全多暴跌、全空暴涨、盘口消失、API 断流+pending 转出)。
// 幅度是保守缺省值,调用方可自带场景覆盖。
func DefaultStressScenarios() []StressScenario {
	return []StressScenario{
		{Name: "corr1_crash_30", MarketMovePct: -0.30},
		{Name: "corr1_squeeze_30", MarketMovePct: +0.30},
		{Name: "all_long_crash_20", MarketMovePct: -0.20, SideFilter: SideLong},
		{Name: "all_short_squeeze_20", MarketMovePct: +0.20, SideFilter: SideShort},
		{Name: "liquidity_evaporation", MarketMovePct: -0.10, ExitFrictionRate: 0.05},
		{Name: "api_outage_pending_outflow", MarketMovePct: -0.10, FrozenDurationHoldPct: 0.10, CountPendingOutflow: true},
	}
}

// RunStress 对账户逐场景计算损失与剩余偿付缓冲。相关性=1 体现在:场景内所有
// (未被 SideFilter 排除的)仓位同时按 Beta×冲击 受击,不存在分散化收益。
func RunStress(acct StressAccount, scenarios []StressScenario) []StressResult {
	out := make([]StressResult, 0, len(scenarios))
	for _, sc := range scenarios {
		move := sc.MarketMovePct
		if sc.FrozenDurationHoldPct != 0 {
			// 断流期间无法平仓:冲击同号放大。
			move += math.Copysign(sc.FrozenDurationHoldPct, sc.MarketMovePct)
		}
		var loss money.Micros
		for _, p := range acct.Positions {
			if sc.SideFilter != "" && p.Side != sc.SideFilter {
				continue
			}
			sign := p.Side.Sign()
			if sign == 0 || p.NotionalUSDT <= 0 {
				continue
			}
			beta := p.Beta
			if beta <= 0 {
				beta = 1
			}
			pnl := sign * beta * move * p.NotionalUSDT
			loss = loss.Add(money.FromUSDT(-pnl))
			if sc.ExitFrictionRate > 0 {
				loss = loss.Add(money.FromUSDT(p.NotionalUSDT * sc.ExitFrictionRate))
			}
		}
		buffer := money.FromUSDT(acct.EquityUSDT).Sub(loss)
		if sc.CountPendingOutflow {
			buffer = buffer.Sub(money.FromUSDT(math.Max(0, acct.PendingOutflowUSDT)))
		}
		res := StressResult{Scenario: sc.Name, LossUSDT: loss.USDT()}
		if acct.EquityUSDT > 0 {
			res.SurvivingBufferPct = buffer.USDT() / acct.EquityUSDT
		}
		res.Ruined = buffer.USDT() <= 0
		out = append(out, res)
	}
	return out
}

// WorstStress 返回剩余缓冲最小的场景结果;空输入返回零值。
func WorstStress(results []StressResult) StressResult {
	var worst StressResult
	for i, r := range results {
		if i == 0 || r.SurvivingBufferPct < worst.SurvivingBufferPct {
			worst = r
		}
	}
	return worst
}
