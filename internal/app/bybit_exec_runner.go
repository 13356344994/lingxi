package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"lingxi/internal/venue"
)

// bybit_exec_runner.go 是精简 Bybit 执行器的 **规划运行核心**(B2):扫描→选币→定量→产出意图计划。
// 它**只读**行情/账户,产出 BybitOpenPlan 与放弃原因,**绝不下单**。实盘下单(B3)另起 live 路径并门控。
//
// 依赖最小只读接口 bybitMarketReader(venue.Venue 天然满足),便于离线用 fake 单测,且从类型上杜绝误发单。

// bybitMarketReader 是 周期规划所需的最小只读能力子集。
type bybitMarketReader interface {
	FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error)
	FetchInstruments(ctx context.Context) ([]venue.Instrument, error)
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
	FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error)
}

// bybitFundingHistorySource 是可选能力:能拉历史资金费率(EV 门槛用)。适配器满足;fake 可不实现(回退单次门)。
type bybitFundingHistorySource interface {
	FetchFundingRateHistory(ctx context.Context, s venue.Symbol, limit int) ([]float64, error)
}

// bybitHistCacheTTL:历史费率每个结算间隔才多一条,20 分钟缓存足够新鲜且把 API 调用降到可忽略。
const bybitHistCacheTTL = 20 * time.Minute

// bybitHistFetchLimit:72 条样本(1h 间隔=3 天,8h 间隔=24 天),对连跑统计足够。
const bybitHistFetchLimit = 72

type bybitHistCacheEntry struct {
	rates []float64
	at    time.Time
}

// BybitCyclePlanner 用只读行情产出 delta-neutral 开仓计划(OFF,不下单)。
// 仅在单个管理协程内使用(histCache 无锁)。
type BybitCyclePlanner struct {
	market    bybitMarketReader
	cfg       BybitExecConfig
	logf      func(format string, args ...any)
	histCache map[venue.Symbol]bybitHistCacheEntry
}

// NewBybitCyclePlanner 构造 周期规划器。logf 为空时用 no-op。
func NewBybitCyclePlanner(market bybitMarketReader, cfg BybitExecConfig, logf func(string, ...any)) *BybitCyclePlanner {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &BybitCyclePlanner{market: market, cfg: cfg, logf: logf, histCache: make(map[venue.Symbol]bybitHistCacheEntry)}
}

// fundingHistory 带缓存拉取某标的历史资金费率;行情源不支持或拉取失败返回 nil(调用方回退单次门)。
func (e *BybitCyclePlanner) fundingHistory(ctx context.Context, sym venue.Symbol) []float64 {
	src, ok := e.market.(bybitFundingHistorySource)
	if !ok {
		return nil
	}
	if entry, hit := e.histCache[sym]; hit && time.Since(entry.at) < bybitHistCacheTTL {
		return entry.rates
	}
	rates, err := src.FetchFundingRateHistory(ctx, sym, bybitHistFetchLimit)
	if err != nil {
		e.logf("[bybit] 拉历史费率失败 %s(EV 门回退单次门): %v", sym, err)
		return nil
	}
	e.histCache[sym] = bybitHistCacheEntry{rates: rates, at: time.Now()}
	return rates
}

// BybitPlanRejection 记录一个被放弃的候选及原因(供 规划日志/面板解释)。
type BybitPlanRejection struct {
	Symbol venue.Symbol
	Reason string
}

// BybitCyclePlan 是一轮 周期规划结果。
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
// availableSlots 是本轮真正可开的仓位数(并发上限 − 在场数;≤0 视为不限)。资金按
// min(候选数, 可开槽位) 均分——否则 3 个候选只开 1 笔时,开出的那笔只拿到 1/3 资金,
// 其余资金闲置(资金占用率的头号杀手)。budgetUSDT 是本轮可部署预算(≤0 退回 AllocatedUSDT)。
func (e *BybitCyclePlanner) PlanCycle(ctx context.Context, availableSlots int, budgetUSDT float64) (BybitCyclePlan, error) {
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
		spotInstr, hasSpot := instrByKey[instrumentKey{s.Symbol, venue.MarketSpot}]
		_, hasPerp := instrByKey[instrumentKey{s.Symbol, venue.MarketPerp}]
		if !hasSpot || !hasPerp {
			continue
		}
		if s.LastFundingRate < 0 && !spotInstr.SpotBorrowable {
			result.Rejected = append(result.Rejected, BybitPlanRejection{s.Symbol, "现货不支持杠杆借币(marginTrading=none),负carry无法做空对冲腿"})
			continue
		}
		tradeableSnaps = append(tradeableSnaps, s)
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

	budget := budgetUSDT
	if budget <= 0 {
		budget = e.cfg.AllocatedUSDT
	}
	divisor := len(candidates)
	if availableSlots > 0 && availableSlots < divisor {
		divisor = availableSlots
	}
	perCandidateUSDT := budget / float64(divisor)

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
		// EV 门启用且历史样本充足时,由「多结算期望值」接管开仓门槛(单次结算门在 taker 费率下
		// 注定为负,不再适用);历史不可得/样本不足回退单次门(保守)。
		candCfg := cfg
		hist := []float64(nil)
		if cfg.EVGateEnabled {
			hist = e.fundingHistory(ctx, c.Symbol)
			if len(hist) >= bybitEVMinSamples {
				candCfg.MinNetProfitRate = -1 // 单次门让位,EV 门在下方接管
			}
		}
		plan, ok, reason := planBybitDeltaNeutralOpen(c, spotPx, perpPx, spotInstr, perpInstr, perCandidateUSDT, candCfg)
		if !ok {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, reason})
			continue
		}
		if cfg.EVGateEnabled && len(hist) >= bybitEVMinSamples {
			if ev, detail, evOK := bybitMultiSettlementEV(plan, hist, cfg); evOK && ev <= 0 {
				result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "多结算期望值为负: " + detail})
				continue
			} else if evOK {
				e.logf("[bybit] %s 通过 EV 门: %s", c.Symbol, detail)
			}
		}
		result.Accepted = append(result.Accepted, plan)
	}
	sort.Slice(result.Accepted, func(i, j int) bool {
		return bybitRatePerHour(result.Accepted[i].FundingRate, result.Accepted[i].IntervalHours) >
			bybitRatePerHour(result.Accepted[j].FundingRate, result.Accepted[j].IntervalHours)
	})
	return result, nil
}

// FundingSnapshotFor 拉取并返回某标的当前资金费快照(供滚动持有在结算后复核下期费率)。找不到则 ok=false。
func (e *BybitCyclePlanner) FundingSnapshotFor(ctx context.Context, sym venue.Symbol) (venue.FundingSnapshot, bool, error) {
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
func (e *BybitCyclePlanner) midPrice(ctx context.Context, m venue.Market, s venue.Symbol) (float64, error) {
	bt, err := e.market.FetchBookTicker(ctx, m, s)
	if err != nil {
		return 0, err
	}
	if bt.BidPrice <= 0 || bt.AskPrice <= 0 {
		return 0, fmt.Errorf("盘口价非正")
	}
	return (bt.BidPrice + bt.AskPrice) / 2, nil
}

// LogCyclePlan 把一轮规划结果打印为人类可读 规划日志(不下单)。
func (e *BybitCyclePlanner) LogCyclePlan(plan BybitCyclePlan) {
	e.logf("[dry-run] 候选=%d 通过=%d 放弃=%d", len(plan.Candidates), len(plan.Accepted), len(plan.Rejected))
	for _, p := range plan.Accepted {
		e.logf("[dry-run][拟开仓] %s 费率=%.4f%% 数量=%.6f 现货名义=%.2f 永续名义=%.2f 预计毛资金费=%.4f 费用=%.4f 净利=%.4f 净利率=%.4f%%",
			p.Symbol, p.FundingRate*100, p.BaseQty, p.SpotNotional, p.PerpNotional,
			p.EstGrossFunding, p.EstFeeCost, p.EstNetProfit, p.EstNetProfitRate*100)
	}
	for _, r := range plan.Rejected {
		e.logf("[dry-run][放弃] %s: %s", r.Symbol, r.Reason)
	}
}
