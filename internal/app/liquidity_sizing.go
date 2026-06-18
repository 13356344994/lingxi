package app

import (
	"context"
	"math"
	"time"

	"lingxi/internal/venue"
)

// liquidityNotionalCap 按盘口深度(滑点预算内)与 24h 成交量参与率计算单腿流动性名义上限。
// 深度项:买吃 ask / 卖吃 bid,沿档位累加直至成交均价相对最优价滑点 > slippageBps。
// 参与率项:participationPct × volume24hUSDT(volume24h≤0 时跳过)。
// 返回可用项的 min;两项均不可用 → +Inf(退化为仅受 max_position_usdt 约束)。
// slippageBps≤0 或 participationPct≤0 时关闭对应项。
func liquidityNotionalCap(depth venue.Depth, side venue.Side, slippageBps, volume24hUSDT, participationPct float64) float64 {
	caps := make([]float64, 0, 2)
	if slippageBps > 0 {
		levels, refPrice := depthLevelsForSide(depth, side)
		if len(levels) > 0 && refPrice > 0 {
			if cap := depthNotionalWithinSlippage(side, levels, slippageBps, refPrice); cap < math.MaxFloat64 {
				caps = append(caps, cap)
			}
		}
	}
	if participationPct > 0 && volume24hUSDT > 0 {
		caps = append(caps, participationPct*volume24hUSDT)
	}
	if len(caps) == 0 {
		return math.MaxFloat64
	}
	out := caps[0]
	for i := 1; i < len(caps); i++ {
		if caps[i] < out {
			out = caps[i]
		}
	}
	return out
}

func depthLevelsForSide(depth venue.Depth, side venue.Side) ([]venue.DepthLevel, float64) {
	switch side {
	case venue.SideBuy:
		ref := bestLevelPrice(depth.Asks)
		return depth.Asks, ref
	case venue.SideSell:
		ref := bestLevelPrice(depth.Bids)
		return depth.Bids, ref
	default:
		return nil, 0
	}
}

func bestLevelPrice(levels []venue.DepthLevel) float64 {
	for _, lv := range levels {
		if lv.Price > 0 && lv.Qty > 0 {
			return lv.Price
		}
	}
	return 0
}

func depthHasLevels(depth venue.Depth) bool {
	for _, lv := range depth.Asks {
		if lv.Price > 0 && lv.Qty > 0 {
			return true
		}
	}
	for _, lv := range depth.Bids {
		if lv.Price > 0 && lv.Qty > 0 {
			return true
		}
	}
	return false
}

// depthNotionalWithinSlippage 沿对应方向订单簿,返回滑点 ≤ slippageBps 时的最大名义(USDT)。
// refPrice 用于 notional→qty 换算,与 simulateTakerSlippageBps 一致。
func depthNotionalWithinSlippage(side venue.Side, levels []venue.DepthLevel, slippageBps, refPrice float64) float64 {
	if slippageBps <= 0 || refPrice <= 0 || len(levels) == 0 {
		return math.MaxFloat64
	}
	maxFillable := 0.0
	for _, lv := range levels {
		if lv.Price > 0 && lv.Qty > 0 {
			maxFillable += lv.Qty * refPrice
		}
	}
	if maxFillable <= 0 {
		return math.MaxFloat64
	}
	if slip, ok := simulateTakerSlippageBps(side, levels, maxFillable, refPrice); ok && slip <= slippageBps+1e-9 {
		return maxFillable
	}
	lo, hi := 0.0, maxFillable
	for i := 0; i < 48; i++ {
		mid := (lo + hi) / 2
		if mid <= 0 {
			hi = mid
			continue
		}
		s, okMid := simulateTakerSlippageBps(side, levels, mid, refPrice)
		if !okMid || s > slippageBps+1e-9 {
			hi = mid
			continue
		}
		lo = mid
	}
	return lo
}

const defaultSizingConservativeNoDepthFactor = 0.25

func applyLiquidityCapToMaxPosition(maxPosition, liquidityCap float64) float64 {
	if maxPosition <= 0 {
		maxPosition = 50
	}
	if liquidityCap >= math.MaxFloat64 {
		return maxPosition
	}
	if liquidityCap <= 0 {
		return 0
	}
	return math.Min(maxPosition, liquidityCap)
}

// resolvedSizingConservativeNoDepthFactor 解析无深度保守系数;≥1 退回 legacy 满额 max_position。
func resolvedSizingConservativeNoDepthFactor(cfg CrossVenueExecutorConfig) float64 {
	if cfg.SizingConservativeNoDepthFactor >= 1.0 {
		return 1.0
	}
	if cfg.SizingConservativeNoDepthFactor > 0 {
		return cfg.SizingConservativeNoDepthFactor
	}
	return defaultSizingConservativeNoDepthFactor
}

// applyLiquiditySizingCap 有流动性 cap 时 min(max,cap);无数据(+Inf)时按 conservative factor 降级(更严不更松)。
func applyLiquiditySizingCap(maxPosition, liquidityCap, noDepthFactor float64) float64 {
	if maxPosition <= 0 {
		maxPosition = 50
	}
	if liquidityCap < math.MaxFloat64 {
		return applyLiquidityCapToMaxPosition(maxPosition, liquidityCap)
	}
	if noDepthFactor >= 1.0 {
		return maxPosition
	}
	if noDepthFactor <= 0 {
		noDepthFactor = defaultSizingConservativeNoDepthFactor
	}
	return maxPosition * noDepthFactor
}

// crossVenuePerpPerpScanTradableNotional 扫描期 perp-perp 可交易名义。
// CrossVenueScanner 持有 venues 且 scanOnce 有 ctx 时可拉 live depth;否则无深度数据走 conservative factor。
func crossVenuePerpPerpScanTradableNotional(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	highVenue, lowVenue, sym string,
) float64 {
	maxPos := cfg.MaxPositionUSDT
	if maxPos <= 0 {
		maxPos = 50
	}
	noDepthFactor := resolvedSizingConservativeNoDepthFactor(cfg)
	if cfg.SizingMaxSlippageBps <= 0 && cfg.SizingMaxParticipationPct24h <= 0 {
		return applyLiquiditySizingCap(maxPos, math.MaxFloat64, noDepthFactor)
	}
	highV := venues[highVenue]
	lowV := venues[lowVenue]
	if ctx == nil || highV == nil || lowV == nil {
		return applyLiquiditySizingCap(maxPos, math.MaxFloat64, noDepthFactor)
	}
	deps := crossVenueGateDeps{
		fetchDepth: func(c context.Context, v venue.Venue, s venue.Symbol, limit int) (venue.Depth, error) {
			return v.FetchDepth(c, venue.MarketPerp, s, limit)
		},
	}
	liqCap := crossVenueGateLiquidityNotionalCap(ctx, cfg, deps, highV, lowV, venue.Symbol(sym))
	return applyLiquiditySizingCap(maxPos, liqCap, noDepthFactor)
}

// crossVenueSpotPerpLiquidityNotionalCap spot-perp 双腿流动性上限:min(现货买入,永续卖出,各自参与率)。
// 决策期:仅当 MarketSnap 已含 Depth/24h 量时生效;否则返回 +Inf。
func crossVenueSpotPerpLiquidityNotionalCap(exec CrossVenueExecutorConfig, spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) float64 {
	slipBps := crossVenueResolvedSizingMaxSlippageBps(exec)
	partPct := exec.SizingMaxParticipationPct24h
	if slipBps <= 0 && partPct <= 0 {
		return math.MaxFloat64
	}
	spotCap := math.MaxFloat64
	perpCap := math.MaxFloat64
	if (spotMkt.SpotDepth != nil && depthHasLevels(*spotMkt.SpotDepth)) || (partPct > 0 && spotMkt.Volume24hSpotUSDT > 0) {
		depth := venue.Depth{}
		if spotMkt.SpotDepth != nil {
			depth = *spotMkt.SpotDepth
		}
		spotCap = liquidityNotionalCap(depth, venue.SideBuy, slipBps, spotMkt.Volume24hSpotUSDT, partPct)
	}
	if (perpMkt.PerpDepth != nil && depthHasLevels(*perpMkt.PerpDepth)) || (partPct > 0 && perpMkt.Volume24hPerpUSDT > 0) {
		depth := venue.Depth{}
		if perpMkt.PerpDepth != nil {
			depth = *perpMkt.PerpDepth
		}
		perpCap = liquidityNotionalCap(depth, venue.SideSell, slipBps, perpMkt.Volume24hPerpUSDT, partPct)
	}
	out := math.MaxFloat64
	for _, c := range []float64{spotCap, perpCap} {
		if c < out {
			out = c
		}
	}
	return out
}

// crossVenueGateLiquidityNotionalCap 跨所 perp-perp 执行期流动性上限(拉 live depth + 可选 24h 量)。
func crossVenueGateLiquidityNotionalCap(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	deps crossVenueGateDeps,
	highV, lowV venue.Venue,
	sym venue.Symbol,
) float64 {
	slipBps := crossVenueResolvedSizingMaxSlippageBps(cfg)
	gateSlipBps := crossVenueResolvedMaxOpenSlippageBps(cfg)
	if gateSlipBps > 0 && (slipBps <= 0 || gateSlipBps < slipBps) {
		slipBps = gateSlipBps
	}
	partPct := cfg.SizingMaxParticipationPct24h
	if slipBps <= 0 && partPct <= 0 {
		return math.MaxFloat64
	}
	if deps.fetchDepth == nil {
		return math.MaxFloat64
	}
	caps := make([]float64, 0, 2)
	if highV != nil {
		if depth, err := deps.fetchDepth(ctx, highV, sym, crossVenueOpenSlippageDepthLimit); err == nil {
			vol := fetchVenuePerpQuoteVolume24h(ctx, highV, sym)
			if cap := liquidityNotionalCap(depth, venue.SideSell, slipBps, vol, partPct); cap < math.MaxFloat64 {
				caps = append(caps, cap)
			}
		}
	}
	if lowV != nil {
		if depth, err := deps.fetchDepth(ctx, lowV, sym, crossVenueOpenSlippageDepthLimit); err == nil {
			vol := fetchVenuePerpQuoteVolume24h(ctx, lowV, sym)
			if cap := liquidityNotionalCap(depth, venue.SideBuy, slipBps, vol, partPct); cap < math.MaxFloat64 {
				caps = append(caps, cap)
			}
		}
	}
	if len(caps) == 0 {
		return math.MaxFloat64
	}
	out := caps[0]
	for i := 1; i < len(caps); i++ {
		if caps[i] < out {
			out = caps[i]
		}
	}
	return out
}

// fetchVenueQuoteVolume24h 拉取指定市场 24h 成交额(USDT);失败或未实现则返回 0(跳过参与率项)。
func fetchVenueQuoteVolume24h(ctx context.Context, v venue.Venue, m venue.Market, sym venue.Symbol) float64 {
	if v == nil || ctx.Err() != nil {
		return 0
	}
	vol, err := v.FetchRecentQuoteVolume(ctx, m, sym, 24*time.Hour)
	if err != nil || vol <= 0 {
		return 0
	}
	return vol
}

// fetchVenuePerpQuoteVolume24h 拉取永续 24h 成交额(USDT);失败或未实现则返回 0(跳过参与率项)。
func fetchVenuePerpQuoteVolume24h(ctx context.Context, v venue.Venue, sym venue.Symbol) float64 {
	return fetchVenueQuoteVolume24h(ctx, v, venue.MarketPerp, sym)
}
