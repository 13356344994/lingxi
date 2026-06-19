package app

import (
	"lingxi/internal/venue"
)

// carryCloseTakerBaseBps carry 平仓滑点阶梯起点(与 perp-perp crossVenueTakerSlippageBps 同源)。
func carryCloseTakerBaseBps(cfg CrossVenueExecutorConfig) float64 {
	return crossVenueResolvedTakerSlippageBps(cfg)
}

// carryCloseIocLimitPrice carry 平仓 IOC 限价:复用 crossVenueCloseIocLimitPrice 阶梯/穿价兜底。
func carryCloseIocLimitPrice(cfg CrossVenueExecutorConfig, side venue.Side, book venue.BookTicker, closeAttempts int) (float64, bool) {
	baseBps := carryCloseTakerBaseBps(cfg)
	maxBps := crossVenueResolvedCloseMaxSlippageBps(cfg)
	return crossVenueCloseIocLimitPrice(side, book, baseBps, closeAttempts, maxBps)
}
