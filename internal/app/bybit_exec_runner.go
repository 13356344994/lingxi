package app

import (
	"context"
	"fmt"
	"sort"

	"lingxi/internal/venue"
)

// bybit_exec_runner.go 是精简 Bybit 执行器的 **shadow 运行核心**(B2):扫描→选币→定量→产出意图计划。
// 它**只读**行情/账户,产出 BybitOpenPlan 与放弃原因,**绝不下单**。实盘下单(B3)另起 live 路径并门控。
//
// 依赖最小只读接口 bybitMarketReader(venue.Venue 天然满足),便于离线用 fake 单测,且从类型上杜绝误发单。

// bybitMarketReader 是 shadow 规划所需的最小只读能力子集。
type bybitMarketReader interface {
	FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error)
	FetchInstruments(ctx context.Context) ([]venue.Instrument, error)
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
	FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error)
}

// BybitShadowExecutor 用只读行情产出 delta-neutral 开仓计划(shadow,不下单)。
type BybitShadowExecutor struct {
	market bybitMarketReader
	cfg    BybitExecConfig
	logf   func(format string, args ...any)
}

// NewBybitShadowExecutor 构造 shadow 执行器。logf 为空时用 no-op。
func NewBybitShadowExecutor(market bybitMarketReader, cfg BybitExecConfig, logf func(string, ...any)) *BybitShadowExecutor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &BybitShadowExecutor{market: market, cfg: cfg, logf: logf}
}

// BybitPlanRejection 记录一个被放弃的候选及原因(供 shadow 日志/面板解释)。
type BybitPlanRejection struct {
	Symbol venue.Symbol
	Reason string
}

// BybitCyclePlan 是一轮 shadow 规划结果。
type BybitCyclePlan struct {
	Accepted   []BybitOpenPlan
	Rejected   []BybitPlanRejection
	Candidates []BybitCandidate
}

// instrumentKey 唯一标识 (符号, 市场)。
type instrumentKey struct {
	sym    venue.Symbol
	market venue.Market
}

// PlanCycle 执行一轮只读规划:拉资金费→选币→拉合约元数据/价格/费率→定量,返回计划与放弃原因。
func (e *BybitShadowExecutor) PlanCycle(ctx context.Context) (BybitCyclePlan, error) {
	snaps, err := e.market.FetchFundingSnapshot(ctx)
	if err != nil {
		return BybitCyclePlan{}, fmt.Errorf("拉资金费快照失败: %w", err)
	}
	// 便宜的空闸:本轮没有任何正费率标的越过初筛地板 → 直接结束,省掉一次合约元数据拉取。
	fundingPassers := selectBybitCandidates(snaps, e.cfg)
	if len(fundingPassers) == 0 {
		return BybitCyclePlan{}, nil
	}

	instruments, err := e.market.FetchInstruments(ctx)
	if err != nil {
		return BybitCyclePlan{}, fmt.Errorf("拉合约元数据失败: %w", err)
	}
	instrByKey := make(map[instrumentKey]venue.Instrument, len(instruments))
	for _, in := range instruments {
		instrByKey[instrumentKey{in.Symbol, in.Market}] = in
	}

	result := BybitCyclePlan{}
	// 诊断可见性:越过费率初筛但缺少可对冲腿的高费率币(尤其盘前 perp-only 标的)记入放弃原因——但**不让它们
	// 参与下面的 top_n 名额与每腿资金均分**。这是本函数的关键修复:否则无现货腿的高费率标的会占满 top_n、并按
	// 候选数稀释每腿分配资金,把唯一可成交的现货标的(如 TACUSDT)挤出候选集或压到最小下单额以下 → 永远开不出单。
	for _, c := range fundingPassers {
		if _, hasPerp := instrByKey[instrumentKey{c.Symbol, venue.MarketPerp}]; !hasPerp {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "缺少永续合约元数据"})
			continue
		}
		if _, hasSpot := instrByKey[instrumentKey{c.Symbol, venue.MarketSpot}]; !hasSpot {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "缺少现货合约元数据(无法做多现货腿)"})
		}
	}

	// 只在「现货+永续元数据齐全(可 delta 对冲)」的标的里按费率取 top_n,使 perCandidateUSDT 只按真正可开仓的
	// 标的数均分,不被 perp-only 标的占名额/稀释。
	tradeableSnaps := make([]venue.FundingSnapshot, 0, len(snaps))
	for _, s := range snaps {
		_, hasSpot := instrByKey[instrumentKey{s.Symbol, venue.MarketSpot}]
		_, hasPerp := instrByKey[instrumentKey{s.Symbol, venue.MarketPerp}]
		if hasSpot && hasPerp {
			tradeableSnaps = append(tradeableSnaps, s)
		}
	}
	candidates := selectBybitCandidates(tradeableSnaps, e.cfg)
	result.Candidates = candidates
	if len(candidates) == 0 {
		return result, nil
	}

	// 实拉费率(账户级);失败则退回 cfg 中静态费率。
	syms := make([]venue.Symbol, 0, len(candidates))
	for _, c := range candidates {
		syms = append(syms, c.Symbol)
	}
	cfg := e.cfg
	if profile, perr := e.market.FetchTradingCostProfile(ctx, syms); perr == nil {
		if profile.SpotTaker > 0 {
			cfg.SpotTakerFee = profile.SpotTaker
		}
		if profile.FuturesTaker > 0 {
			cfg.PerpTakerFee = profile.FuturesTaker
		}
	}

	perCandidateUSDT := e.cfg.AllocatedUSDT / float64(len(candidates))

	for _, c := range candidates {
		spotInstr, hasSpot := instrByKey[instrumentKey{c.Symbol, venue.MarketSpot}]
		perpInstr, hasPerp := instrByKey[instrumentKey{c.Symbol, venue.MarketPerp}]
		if !hasPerp {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "缺少永续合约元数据"})
			continue
		}
		if !hasSpot {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "缺少现货合约元数据(无法做多现货腿)"})
			continue
		}
		spotPx, serr := e.midPrice(ctx, venue.MarketSpot, c.Symbol)
		if serr != nil {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "现货盘口缺失: " + serr.Error()})
			continue
		}
		perpPx, perr := e.midPrice(ctx, venue.MarketPerp, c.Symbol)
		if perr != nil {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "永续盘口缺失: " + perr.Error()})
			continue
		}
		plan, ok, reason := planBybitDeltaNeutralOpen(c, spotPx, perpPx, spotInstr, perpInstr, perCandidateUSDT, cfg)
		if !ok {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, reason})
			continue
		}
		result.Accepted = append(result.Accepted, plan)
	}
	sort.Slice(result.Accepted, func(i, j int) bool {
		return result.Accepted[i].FundingRate > result.Accepted[j].FundingRate
	})
	return result, nil
}

// FundingSnapshotFor 拉取并返回某标的当前资金费快照(供滚动持有在结算后复核下期费率)。找不到则 ok=false。
func (e *BybitShadowExecutor) FundingSnapshotFor(ctx context.Context, sym venue.Symbol) (venue.FundingSnapshot, bool, error) {
	snaps, err := e.market.FetchFundingSnapshot(ctx)
	if err != nil {
		return venue.FundingSnapshot{}, false, err
	}
	for _, s := range snaps {
		if s.Symbol == sym {
			return s, true, nil
		}
	}
	return venue.FundingSnapshot{}, false, nil
}

// midPrice 取某市场某符号的盘口中价(bid/ask 任一缺失则报错)。
func (e *BybitShadowExecutor) midPrice(ctx context.Context, m venue.Market, s venue.Symbol) (float64, error) {
	bt, err := e.market.FetchBookTicker(ctx, m, s)
	if err != nil {
		return 0, err
	}
	if bt.BidPrice <= 0 || bt.AskPrice <= 0 {
		return 0, fmt.Errorf("盘口价非正")
	}
	return (bt.BidPrice + bt.AskPrice) / 2, nil
}

// LogShadowPlan 把一轮规划结果打印为人类可读 shadow 日志(不下单)。
func (e *BybitShadowExecutor) LogShadowPlan(plan BybitCyclePlan) {
	e.logf("[bybit-shadow] 候选=%d 通过=%d 放弃=%d", len(plan.Candidates), len(plan.Accepted), len(plan.Rejected))
	for _, p := range plan.Accepted {
		e.logf("[bybit-shadow][拟开仓] %s 费率=%.4f%% 数量=%.6f 现货名义=%.2f 永续名义=%.2f 预计毛资金费=%.4f 费用=%.4f 净利=%.4f 净利率=%.4f%%",
			p.Symbol, p.FundingRate*100, p.BaseQty, p.SpotNotional, p.PerpNotional,
			p.EstGrossFunding, p.EstFeeCost, p.EstNetProfit, p.EstNetProfitRate*100)
	}
	for _, r := range plan.Rejected {
		e.logf("[bybit-shadow][放弃] %s: %s", r.Symbol, r.Reason)
	}
}
