package app

import (
	"context"

	"lingxi/internal/venue"
)

// 滑点预算统一链:SlippageBudgetBps 为基准(bps),其余由 factor 派生。
// legacy:SlippageBudgetBps=0 时退回各独立常量(EVExpectedSlippageBps 等),行为与改造前一致。
const (
	defaultSlippageBudgetBps             = 20.0
	defaultMaxOpenSlippageBps            = 15.0
	defaultTakerSlippageBudgetBps        = 40.0
	defaultSizingMaxSlippageBps          = 20.0
	defaultEVExpectedSlippageBps         = 3.0
	defaultSlippageGateFactor            = 0.75 // 20×0.75=15 开仓 gate
	defaultSlippageExecutionSafetyFactor = 2.0  // 20×2=40 执行 taker
	defaultSlippageCloseMaxMultiplier    = 12.5 // 20×12.5=250 平仓阶梯上限
	crossVenueMaxTakerSlippageBpsHardCap = 100.0
	crossVenueCloseMaxSlippageBpsLegacy  = 250.0
)

// crossVenueEVSlippageLeg EV 成本侧单腿深度模拟输入;Depth 为空时回退基准/legacy。
type crossVenueEVSlippageLeg struct {
	Depth  *venue.Depth
	Side   venue.Side
	RefMid float64
}

// crossVenueSlippageBudgetBase 统一基准;0=legacy 模式(退回各独立常量)。
func crossVenueSlippageBudgetBase(cfg CrossVenueExecutorConfig) float64 {
	if cfg.SlippageBudgetBps > 0 {
		return cfg.SlippageBudgetBps
	}
	return 0
}

// crossVenueResolvedSizingMaxSlippageBps 流动性 sizing cap = 基准。
func crossVenueResolvedSizingMaxSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		return base
	}
	if cfg.SizingMaxSlippageBps > 0 {
		return cfg.SizingMaxSlippageBps
	}
	return defaultSizingMaxSlippageBps
}

// crossVenueResolvedMaxOpenSlippageBps 开仓 gate 拒开阈值 ≈ 基准×gate_factor。
func crossVenueResolvedMaxOpenSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		factor := cfg.SlippageGateFactor
		if factor <= 0 {
			factor = defaultSlippageGateFactor
		}
		return base * factor
	}
	if cfg.MaxOpenSlippageBps > 0 {
		return cfg.MaxOpenSlippageBps
	}
	return defaultMaxOpenSlippageBps
}

// crossVenueResolvedIOCSlippageBps 第一腿 IOC 限价 = 基准。
func crossVenueResolvedIOCSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		return base
	}
	if cfg.SlippageBudgetBps > 0 {
		return cfg.SlippageBudgetBps
	}
	return defaultSlippageBudgetBps
}

// crossVenueResolvedTakerSlippageBps 吃单腿 IOC = 基准×execution_safety_factor,硬顶 100bps。
func crossVenueResolvedTakerSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		factor := cfg.SlippageExecutionSafetyFactor
		if factor <= 0 {
			factor = defaultSlippageExecutionSafetyFactor
		}
		bps := base * factor
		if bps > crossVenueMaxTakerSlippageBpsHardCap {
			bps = crossVenueMaxTakerSlippageBpsHardCap
		}
		return bps
	}
	bps := cfg.TakerSlippageBudgetBps
	if bps <= 0 {
		bps = cfg.SlippageBudgetBps
	}
	if bps <= 0 {
		bps = defaultTakerSlippageBudgetBps
	}
	if bps < crossVenueResolvedIOCSlippageBps(cfg) {
		bps = crossVenueResolvedIOCSlippageBps(cfg)
	}
	if bps > crossVenueMaxTakerSlippageBpsHardCap {
		bps = crossVenueMaxTakerSlippageBpsHardCap
	}
	return bps
}

// crossVenueResolvedCloseMaxSlippageBps 平仓阶梯穿价上限 = 基准×close_max_multiplier。
func crossVenueResolvedCloseMaxSlippageBps(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		mult := cfg.SlippageCloseMaxMultiplier
		if mult <= 0 {
			mult = defaultSlippageCloseMaxMultiplier
		}
		return base * mult
	}
	return crossVenueCloseMaxSlippageBpsLegacy
}

func crossVenueEVSlippageFallbackBpsPerLeg(cfg CrossVenueExecutorConfig) float64 {
	if base := crossVenueSlippageBudgetBase(cfg); base > 0 {
		return base
	}
	bps := cfg.EVExpectedSlippageBps
	if bps <= 0 {
		bps = defaultEVExpectedSlippageBps
	}
	return bps
}

func crossVenueEVSlippageBpsPerLeg(cfg CrossVenueExecutorConfig, notional float64, leg crossVenueEVSlippageLeg) float64 {
	if notional <= 0 {
		return 0
	}
	if leg.Depth != nil && depthHasLevels(*leg.Depth) && leg.RefMid > 0 {
		levels, _ := depthLevelsForSide(*leg.Depth, leg.Side)
		if slip, ok := simulateTakerSlippageBps(leg.Side, levels, notional, leg.RefMid); ok {
			return slip
		}
	}
	return crossVenueEVSlippageFallbackBpsPerLeg(cfg)
}

func crossVenueExpectedSlippageUSDTWithLegs(cfg CrossVenueExecutorConfig, notional float64, legs []crossVenueEVSlippageLeg) float64 {
	if notional <= 0 || len(legs) == 0 {
		return 0
	}
	totalBps := 0.0
	for _, leg := range legs {
		totalBps += crossVenueEVSlippageBpsPerLeg(cfg, notional, leg)
	}
	return totalBps * notional / 10000
}

// crossVenueDepthMid 从盘口深度取中间价;缺一侧则退回有效侧。
func crossVenueDepthMid(d venue.Depth) float64 {
	bid, ask := 0.0, 0.0
	if len(d.Bids) > 0 {
		bid = d.Bids[0].Price
	}
	if len(d.Asks) > 0 {
		ask = d.Asks[0].Price
	}
	if bid > 0 && ask > 0 {
		return (bid + ask) / 2
	}
	if bid > 0 {
		return bid
	}
	return ask
}

// crossVenuePerpPerpMidFromDepthOrBook 扫描期 perp 中间价:book ticker 优先,否则 depth。
func crossVenuePerpPerpMidFromDepthOrBook(ctx context.Context, v venue.Venue, sym venue.Symbol) float64 {
	if v == nil || ctx == nil {
		return 0
	}
	if bt, err := v.FetchBookTicker(ctx, venue.MarketPerp, sym); err == nil {
		if bt.BidPrice > 0 && bt.AskPrice > 0 {
			if mid := (bt.BidPrice + bt.AskPrice) / 2; mid > 0 {
				return mid
			}
		}
	}
	if depth, err := v.FetchDepth(ctx, venue.MarketPerp, sym, crossVenueOpenSlippageDepthLimit); err == nil {
		if mid := crossVenueDepthMid(depth); mid > 0 {
			return mid
		}
	}
	return 0
}

func crossVenuePerpPerpEVSlippageLegs(
	fetch func(context.Context, venue.Venue, venue.Symbol, int) (venue.Depth, error),
	ctx context.Context,
	highV, lowV venue.Venue,
	sym venue.Symbol,
	highMid, lowMid float64,
) []crossVenueEVSlippageLeg {
	if fetch == nil {
		return nil
	}
	legs := []crossVenueEVSlippageLeg{
		{Side: venue.SideSell, RefMid: highMid},
		{Side: venue.SideBuy, RefMid: lowMid},
		{Side: venue.SideBuy, RefMid: highMid},
		{Side: venue.SideSell, RefMid: lowMid},
	}
	venues := []venue.Venue{highV, lowV, highV, lowV}
	for i := range legs {
		if venues[i] == nil {
			continue
		}
		if depth, err := fetch(ctx, venues[i], sym, crossVenueOpenSlippageDepthLimit); err == nil {
			legs[i].Depth = &depth
		}
	}
	return legs
}

func crossVenueSpotPerpEVSlippageLegs(spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) []crossVenueEVSlippageLeg {
	return []crossVenueEVSlippageLeg{
		{Depth: spotMkt.SpotDepth, Side: venue.SideBuy, RefMid: spotMkt.SpotMid},
		{Depth: perpMkt.PerpDepth, Side: venue.SideSell, RefMid: perpMkt.PerpMid},
		{Depth: spotMkt.SpotDepth, Side: venue.SideSell, RefMid: spotMkt.SpotMid},
		{Depth: perpMkt.PerpDepth, Side: venue.SideBuy, RefMid: perpMkt.PerpMid},
	}
}
