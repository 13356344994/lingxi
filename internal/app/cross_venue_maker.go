package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/core"
	"lingxi/internal/venue"
)

const crossVenueMakerTakerBuffer = 3 * time.Second

// crossVenueMakerChaseParams 从配置解析 maker 追价参数(含默认值)。
func crossVenueMakerChaseParams(cfg core.CrossVenueExecutorConfig) (rounds int, interval, deadlineBudget, takerBuffer time.Duration) {
	rounds = cfg.MakerChaseRounds
	if rounds <= 0 {
		rounds = 6
	}
	ms := cfg.MakerChaseIntervalMs
	if ms <= 0 {
		ms = 1500
	}
	interval = time.Duration(ms) * time.Millisecond
	sec := cfg.MakerDeadlineSeconds
	if sec <= 0 {
		sec = 12
	}
	deadlineBudget = time.Duration(sec) * time.Second
	takerBuffer = crossVenueMakerTakerBuffer
	return rounds, interval, deadlineBudget, takerBuffer
}

func makerPassivePrice(side venue.Side, book venue.BookTicker) float64 {
	if side == venue.SideBuy {
		return book.BidPrice
	}
	return book.AskPrice
}

func mergeCrossVenueOrderResults(a, b venue.OrderResult) venue.OrderResult {
	if a.ExecutedQty <= executionQtyTolerance {
		return b
	}
	if b.ExecutedQty <= executionQtyTolerance {
		return a
	}
	out := a
	total := a.ExecutedQty + b.ExecutedQty
	out.AvgPrice = (a.AvgPrice*a.ExecutedQty + b.AvgPrice*b.ExecutedQty) / total
	out.ExecutedQty = total
	out.CumQuoteUSDT = a.CumQuoteUSDT + b.CumQuoteUSDT
	out.Fee = a.Fee + b.Fee
	if out.FeeAsset == "" {
		out.FeeAsset = b.FeeAsset
	}
	out.Status = "filled"
	return out
}

// placeCrossVenueLegExec 统一腿执行入口:maker_first=false 或OFF 模式走纯 IOC;
// maker_first=true 时 post-only 追价,allowTakerFallback 控制是否 IOC 兜底(第一腿开仓为 false)。
func (e *CrossVenueExecutor) placeCrossVenueLegExec(
	ctx context.Context,
	tr crossVenueTrader,
	sym venue.Symbol,
	side venue.Side,
	qty float64,
	clientID string,
	reduceOnly bool,
	live bool,
	allowTakerFallback bool,
	takerSlippageBps float64,
	deadline time.Time,
) (venue.OrderResult, error) {
	cfg := e.cfg.CrossVenueExecutor
	if !cfg.MakerFirst || !live {
		book, err := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if err != nil {
			return venue.OrderResult{}, err
		}
		price := iocLimitPrice(side, book, takerSlippageBps)
		return e.placeCrossVenueIOC(ctx, tr, sym, side, qty, price, clientID, reduceOnly, live)
	}

	rounds, interval, _, takerBuf := crossVenueMakerChaseParams(cfg)
	if allowTakerFallback {
		// 平仓/对冲/回滚:预留更短 maker 缓冲,尽早留给 taker 兜底。
		if takerBuf > time.Second {
			takerBuf = time.Second
		}
	}

	var acc venue.OrderResult
	remaining := qty

	for attempt := 1; attempt <= rounds && remaining > executionQtyTolerance; attempt++ {
		now := e.nowFn()
		if allowTakerFallback {
			if !now.Add(interval + takerBuf).Before(deadline) {
				break
			}
		} else if !now.Add(interval).Before(deadline) {
			break
		}

		book, berr := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
		if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
			break
		}
		price := makerPassivePrice(side, book)
		makerID := crossVenueLegClientID(clientID, fmt.Sprintf("m%d", attempt))
		if placed, perr := tr.PlaceOrder(ctx, venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        sym,
			Side:          side,
			Type:          venue.OrderTypePostOnly,
			Qty:           remaining,
			Price:         price,
			ClientOrderID: makerID,
			ReduceOnly:    reduceOnly,
		}); perr != nil {
			e.logger.Printf("[跨所][maker] %s 第%d轮挂单失败: %v", sym, attempt, perr)
			break
		} else if placed.ExecutedQty > executionQtyTolerance && strings.EqualFold(placed.Status, "filled") {
			acc = mergeCrossVenueOrderResults(acc, placed)
			remaining = qty - acc.ExecutedQty
			if remaining <= executionQtyTolerance {
				return acc, nil
			}
		}

		waitUntil := time.Now().Add(interval)
		for time.Now().Before(waitUntil) {
			res, qerr := tr.QueryOrder(ctx, venue.MarketPerp, sym, makerID)
			if qerr == nil {
				st := res.Status
				if st == "filled" {
					acc = mergeCrossVenueOrderResults(acc, res)
					remaining = qty - acc.ExecutedQty
					if remaining <= executionQtyTolerance {
						return acc, nil
					}
					break
				}
				if st == "canceled" || st == "rejected" {
					break
				}
			}
			select {
			case <-ctx.Done():
				_ = tr.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
				if acc.ExecutedQty > executionQtyTolerance {
					return acc, ctx.Err()
				}
				return acc, ctx.Err()
			case <-time.After(crossVenueConfirmPoll):
			}
		}

		_ = tr.CancelOrder(ctx, venue.MarketPerp, sym, makerID)
		if res, qerr := tr.QueryOrder(ctx, venue.MarketPerp, sym, makerID); qerr == nil && res.ExecutedQty > executionQtyTolerance {
			acc = mergeCrossVenueOrderResults(acc, res)
			remaining = qty - acc.ExecutedQty
		}
		if remaining <= executionQtyTolerance {
			return acc, nil
		}
	}

	if remaining <= executionQtyTolerance && acc.ExecutedQty > executionQtyTolerance {
		return acc, nil
	}

	if !allowTakerFallback {
		if acc.ExecutedQty > executionQtyTolerance {
			return acc, fmt.Errorf("第一腿 maker 部分成交 %.6f 后未凑满", acc.ExecutedQty)
		}
		return venue.OrderResult{}, fmt.Errorf("第一腿 maker 未成交")
	}

	// taker 兜底:最后一轮或预算不足时强制 IOC 完成对冲/平仓。
	if remaining <= executionQtyTolerance {
		remaining = qty - acc.ExecutedQty
	}
	book, err := tr.FetchBookTicker(ctx, venue.MarketPerp, sym)
	if err != nil {
		if acc.ExecutedQty > executionQtyTolerance {
			return acc, fmt.Errorf("maker 部分成交 %.6f 后 taker 取盘口失败: %w", acc.ExecutedQty, err)
		}
		return venue.OrderResult{}, err
	}
	takerPrice := iocLimitPrice(side, book, takerSlippageBps)
	takerID := crossVenueLegClientID(clientID, "tk")
	takerRes, terr := e.placeCrossVenueIOC(ctx, tr, sym, side, remaining, takerPrice, takerID, reduceOnly, live)
	if terr != nil {
		if acc.ExecutedQty > executionQtyTolerance {
			return acc, fmt.Errorf("maker 部分成交 %.6f 后 taker 兜底失败: %w", acc.ExecutedQty, terr)
		}
		return takerRes, terr
	}
	return mergeCrossVenueOrderResults(acc, takerRes), nil
}
