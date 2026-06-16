package app

import (
	"context"
	"math"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

const (
	crossVenueCloseCriticalAttempts     = 3
	crossVenueClosePierceAfterAttempts  = 8
	crossVenueCloseMaxSlippageBps       = 250.0
	crossVenueClosePierceBuyMultiplier  = 5.0
	crossVenueClosePierceSellMultiplier = 0.2
)

// crossVenueCloseCriticalAlertMilestones 达到 critical 阈值后的告警里程碑:首报 + 指数退避续报。
var crossVenueCloseCriticalAlertMilestones = []int{3, 10, 30, 100}

// crossVenueCloseSlippageBps 按 CloseAttempts 阶梯放大平仓 IOC 滑点预算(base→2x→4x…),上限 crossVenueCloseMaxSlippageBps。
func crossVenueCloseSlippageBps(baseBps float64, closeAttempts int) float64 {
	if closeAttempts <= 0 {
		closeAttempts = 1
	}
	if baseBps <= 0 {
		baseBps = 40
	}
	mult := math.Pow(2, float64(closeAttempts-1))
	bps := baseBps * mult
	if bps > crossVenueCloseMaxSlippageBps {
		return crossVenueCloseMaxSlippageBps
	}
	return bps
}

// crossVenueCloseIocLimitPrice 平仓 IOC 限价:随 CloseAttempts 递进放宽;≥ pierce 阈值后穿价兜底(marketable reduce-only)。
func crossVenueCloseIocLimitPrice(side venue.Side, book venue.BookTicker, baseBps float64, closeAttempts int) (price float64, pierce bool) {
	if closeAttempts >= crossVenueClosePierceAfterAttempts {
		return crossVenueClosePierceLimitPrice(side, book), true
	}
	bps := crossVenueCloseSlippageBps(baseBps, closeAttempts)
	return iocLimitPrice(side, book, bps), false
}

// crossVenueClosePierceLimitPrice 强力穿价 reduce-only 限价:买平远高于 ask、卖平远低于 bid。
func crossVenueClosePierceLimitPrice(side venue.Side, book venue.BookTicker) float64 {
	if side == venue.SideBuy {
		ref := book.AskPrice
		if ref <= 0 {
			ref = book.BidPrice
		}
		if ref <= 0 {
			return 0
		}
		return ref * crossVenueClosePierceBuyMultiplier
	}
	ref := book.BidPrice
	if ref <= 0 {
		ref = book.AskPrice
	}
	if ref <= 0 {
		return 0
	}
	return ref * crossVenueClosePierceSellMultiplier
}

// crossVenueCloseCriticalShouldAlert 判定本轮是否应推送「平仓持续失败」CRITICAL(首报 + 退避里程碑)。
func crossVenueCloseCriticalShouldAlert(attempt int) bool {
	if attempt < crossVenueCloseCriticalAttempts {
		return false
	}
	for _, m := range crossVenueCloseCriticalAlertMilestones {
		if attempt == m {
			return true
		}
	}
	if attempt > 100 && attempt%100 == 0 {
		return true
	}
	return false
}

// crossVenueCloseSlippageRefMid 平仓滑点归因参考价:决策时刻盘口中间价(非穿价限价)。
func crossVenueCloseSlippageRefMid(book venue.BookTicker) float64 {
	return crossVenueBookMid(book)
}

func (e *CrossVenueExecutor) syncCrossVenueCloseLegQtyFromExchange(ctx context.Context, v venue.Venue, sym venue.Symbol, legQty *float64) {
	if v == nil || legQty == nil {
		return
	}
	positions, err := v.FetchPositions(ctx)
	if err != nil {
		return
	}
	exchangeQty := absQty(perpPositionQty(positions, sym))
	if exchangeQty <= executionQtyTolerance {
		*legQty = 0
		return
	}
	if *legQty > exchangeQty {
		*legQty = exchangeQty
	}
}

// placeCrossVenueCloseLeg 平仓专用腿执行:续平重试跳过 maker 追价,直接 IOC + 递进/穿价限价;全程 reduce-only。
func (e *CrossVenueExecutor) placeCrossVenueCloseLeg(
	ctx context.Context,
	tr crossVenueTrader,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	live bool,
	closeAttempts int,
	baseBps float64,
	deadline time.Time,
) (venue.OrderResult, venue.BookTicker, error) {
	book, err := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		return venue.OrderResult{}, book, err
	}
	limitPrice, pierce := crossVenueCloseIocLimitPrice(side, book, baseBps, closeAttempts)
	cfg := e.cfg.CrossVenueExecutor
	if closeAttempts == 1 && cfg.MakerFirst && live && !pierce {
		slipBps := crossVenueCloseSlippageBps(baseBps, closeAttempts)
		res, err := e.placeCrossVenueLegExec(ctx, tr, sym, side, qty, clientID, true, live, true, slipBps, deadline)
		return res, book, err
	}
	res, err := e.placeCrossVenueIOC(ctx, tr, sym, side, qty, limitPrice, clientID, true, live)
	return res, book, err
}

func crossVenueCloseLegDeadline(now time.Time, cfg core.CrossVenueExecutorConfig) time.Time {
	_, _, budget, _ := crossVenueMakerChaseParams(cfg)
	half := budget / 2
	if half < 3*time.Second {
		half = 3 * time.Second
	}
	return now.Add(half)
}
