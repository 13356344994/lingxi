package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
)

// venue_funding_plan.go 单所正费率选币与定量(复用 Bybit 纯规划核心,选币数据来自扫描器快照)。

// venueFundingExecConfigFromEngine 把引擎配置映射为 BybitExecConfig(正 carry 专用,无负 carry)。
func venueFundingExecConfigFromEngine(cfg EngineConfig) BybitExecConfig {
	fx := cfg.FundingExecutor
	topN := fx.TopN
	if topN <= 0 {
		topN = 1
	}
	return BybitExecConfig{
		AllocatedUSDT:      cfg.AllocatedCapitalUSDT,
		TopN:               topN,
		MinFundingRate:     fx.MinFundingRate,
		MinNetProfitRate:   fx.MinNetProfitRate,
		SpotTakerFee:       cfg.HistoricalConfig.SpotTakerFeeRate,
		PerpTakerFee:       cfg.HistoricalConfig.FuturesTakerFeeRate,
		EVGateEnabled:      fx.EVGateEnabled,
		RollEnabled:        fx.RollEnabled,
		MaxHoldSettlements: fx.MaxHoldSettlements,
		PerpLeverage:       1, // OKX/Gate 单所引擎固定 1x
	}
}

func lookupVenueSpotPerpInstruments(ctx context.Context, v venue.Venue, sym venue.Symbol) (spotInstr, perpInstr venue.Instrument, ok bool) {
	instrs, err := v.FetchInstruments(ctx)
	if err != nil {
		return venue.Instrument{}, venue.Instrument{}, false
	}
	for _, in := range instrs {
		if in.Symbol != sym {
			continue
		}
		switch in.Market {
		case venue.MarketSpot:
			spotInstr = in
		case venue.MarketPerp:
			perpInstr = in
		}
	}
	return spotInstr, perpInstr, spotInstr.Symbol != "" && perpInstr.Symbol != ""
}

// VenueFundingPlanner 从扫描器快照选币+定量(不拉全量资金费快照)。
type VenueFundingPlanner struct {
	venueName string
	adapter   venue.Venue
	scanner   *CrossVenueScanner
	cfg       BybitExecConfig
	logf      func(format string, args ...any)
}

func NewVenueFundingPlanner(venueName string, adapter venue.Venue, scanner *CrossVenueScanner, cfg BybitExecConfig, logf func(string, ...any)) *VenueFundingPlanner {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &VenueFundingPlanner{venueName: venueName, adapter: adapter, scanner: scanner, cfg: cfg, logf: logf}
}

func (p *VenueFundingPlanner) fundingSnapsFromScanner() []venue.FundingSnapshot {
	if p.scanner == nil {
		return nil
	}
	snap := p.scanner.Snapshot()
	bySym, ok := snap.FundingByVenue[p.venueName]
	if !ok || len(bySym) == 0 {
		return nil
	}
	out := make([]venue.FundingSnapshot, 0, len(bySym))
	for sym, e := range bySym {
		out = append(out, venue.FundingSnapshot{
			Symbol:          venue.Symbol(sym),
			LastFundingRate: e.Rate,
			IntervalHours:   e.IntervalHours,
			NextFundingTime: e.NextFundingTime,
		})
	}
	return out
}

// PlanCycle 一轮选币+定量。perCandidateUSDT 为单候选部署预算(USDT)。
func (p *VenueFundingPlanner) PlanCycle(ctx context.Context, availableSlots int, perCandidateUSDT float64) (BybitCyclePlan, error) {
	snaps := p.fundingSnapsFromScanner()
	if len(snaps) == 0 {
		return BybitCyclePlan{}, nil
	}
	fundingPassers := selectBybitCandidates(snaps, p.cfg)
	if len(fundingPassers) == 0 {
		return BybitCyclePlan{}, nil
	}

	result := BybitCyclePlan{}
	tradeableSnaps := make([]venue.FundingSnapshot, 0, len(snaps))
	for _, s := range snaps {
		spotInstr, perpInstr, hasBoth := lookupVenueSpotPerpInstruments(ctx, p.adapter, s.Symbol)
		if !hasBoth {
			for _, c := range fundingPassers {
				if c.Symbol != s.Symbol {
					continue
				}
				if perpInstr.Symbol == "" {
					result.Rejected = append(result.Rejected, BybitPlanRejection{s.Symbol, "缺少永续合约元数据"})
				} else if spotInstr.Symbol == "" {
					result.Rejected = append(result.Rejected, BybitPlanRejection{s.Symbol, "缺少现货合约元数据(无法做多现货腿)"})
				}
				break
			}
			continue
		}
		_ = spotInstr
		tradeableSnaps = append(tradeableSnaps, s)
	}
	candidates := selectBybitCandidates(tradeableSnaps, p.cfg)
	result.Candidates = candidates
	if len(candidates) == 0 {
		return result, nil
	}

	syms := make([]venue.Symbol, 0, len(candidates))
	for _, c := range candidates {
		syms = append(syms, c.Symbol)
	}
	cfg := p.cfg
	if profile, perr := p.adapter.FetchTradingCostProfile(ctx, syms); perr == nil {
		if profile.SpotTaker > 0 {
			cfg.SpotTakerFee = profile.SpotTaker
		}
		if profile.FuturesTaker > 0 {
			cfg.PerpTakerFee = profile.FuturesTaker
		}
	}

	budget := perCandidateUSDT
	if budget <= 0 {
		budget = p.cfg.AllocatedUSDT
	}
	divisor := len(candidates)
	if availableSlots > 0 && availableSlots < divisor {
		divisor = availableSlots
	}
	perUSDT := budget / float64(divisor)

	for _, c := range candidates {
		spotInstr, perpInstr, hasBoth := lookupVenueSpotPerpInstruments(ctx, p.adapter, c.Symbol)
		if !hasBoth {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "缺少现货+永续合约元数据"})
			continue
		}
		spotPx, serr := p.midPrice(ctx, venue.MarketSpot, c.Symbol)
		if serr != nil {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "现货盘口缺失: " + serr.Error()})
			continue
		}
		perpPx, perr := p.midPrice(ctx, venue.MarketPerp, c.Symbol)
		if perr != nil {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "永续盘口缺失: " + perr.Error()})
			continue
		}
		candCfg := cfg
		plan, ok, reason := planBybitDeltaNeutralOpen(c, spotPx, perpPx, spotInstr, perpInstr, perUSDT, candCfg)
		if !ok {
			result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, reason})
			continue
		}
		if cfg.EVGateEnabled {
			// OKX/Gate 暂无历史费率接口,EV 门在无历史时由单次门兜底。
			if histSrc, ok := p.adapter.(bybitFundingHistorySource); ok {
				hist, herr := histSrc.FetchFundingRateHistory(ctx, c.Symbol, bybitHistFetchLimit)
				if herr == nil && len(hist) >= bybitEVMinSamples {
					if ev, detail, evOK := bybitMultiSettlementEV(plan, hist, cfg); evOK && ev <= 0 {
						result.Rejected = append(result.Rejected, BybitPlanRejection{c.Symbol, "多结算期望值为负: " + detail})
						continue
					} else if evOK {
						p.logf("[%s] %s 通过 EV 门: %s", p.venueName, c.Symbol, detail)
					}
				}
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

func (p *VenueFundingPlanner) FundingSnapshotFor(sym venue.Symbol) (venue.FundingSnapshot, bool) {
	if p.scanner == nil {
		return venue.FundingSnapshot{}, false
	}
	snap := p.scanner.Snapshot()
	bySym, ok := snap.FundingByVenue[p.venueName]
	if !ok {
		return venue.FundingSnapshot{}, false
	}
	e, ok := bySym[string(sym)]
	if !ok {
		return venue.FundingSnapshot{}, false
	}
	return venue.FundingSnapshot{
		Symbol:          sym,
		LastFundingRate: e.Rate,
		IntervalHours:   e.IntervalHours,
		NextFundingTime: e.NextFundingTime,
	}, true
}

func (p *VenueFundingPlanner) midPrice(ctx context.Context, m venue.Market, s venue.Symbol) (float64, error) {
	bt, err := p.adapter.FetchBookTicker(ctx, m, s)
	if err != nil {
		return 0, err
	}
	if bt.BidPrice <= 0 || bt.AskPrice <= 0 {
		return 0, fmt.Errorf("盘口价非正")
	}
	return (bt.BidPrice + bt.AskPrice) / 2, nil
}

// venueFundingPerCandidateBudget 计算单候选部署预算:min(max_position_usdt, 可用×0.45×2 两腿)。
func venueFundingPerCandidateBudget(availUSDT, maxPositionUSDT float64) float64 {
	legCap := maxPositionUSDT / 2
	if legCap <= 0 {
		legCap = maxPositionUSDT
	}
	if availUSDT > 0 {
		availLeg := availUSDT * 0.45
		if availLeg < legCap {
			legCap = availLeg
		}
	}
	if legCap <= 0 {
		return 0
	}
	return legCap * 2
}

func venueFundingModeLabel(cfg EngineConfig) string {
	if cfg.FundingExecutor.LiveTradingEnabled {
		return "实盘下单"
	}
	return "OFF(不下单)"
}

func venueFundingIdemPrefix(sym venue.Symbol, funding time.Time) string {
	return bybitIdemPrefix(sym, funding)
}

func venueFundingSpotUsesIOC(venueName string) bool {
	return stringsEqualFoldVenue(venueName, gate.VenueName)
}

func stringsEqualFoldVenue(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
