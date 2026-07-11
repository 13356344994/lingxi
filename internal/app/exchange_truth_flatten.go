package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const exchangeTruthFlattenSlip = 0.004

// flattenExchangeTruth 以交易所真实持仓为准收敛全部 perp，而不是相信本地 position 文件。
// 先由各方向引擎正常结算，再调用本函数清理未登记腿/部分成交残腿；reduce-only 保证不反向开仓。
func flattenExchangeTruth(ctx context.Context, venues map[string]venue.Venue) error {
	var lastErrs []string
	for round := 0; round < 3; round++ {
		lastErrs = nil
		remaining := 0
		for name, v := range venues {
			positions, err := v.FetchPositions(ctx)
			if err != nil {
				lastErrs = append(lastErrs, fmt.Sprintf("%s FetchPositions: %v", name, err))
				continue
			}
			for _, p := range positions {
				if math.Abs(p.PositionAmt) <= executionQtyTolerance {
					continue
				}
				remaining++
				book, berr := v.FetchBookTicker(ctx, venue.MarketPerp, p.Symbol)
				if berr != nil || book.BidPrice <= 0 || book.AskPrice <= 0 {
					lastErrs = append(lastErrs, fmt.Sprintf("%s %s book: %v", name, p.Symbol, berr))
					continue
				}
				side, price := venue.SideSell, book.BidPrice*(1-exchangeTruthFlattenSlip)
				if p.PositionAmt < 0 {
					side, price = venue.SideBuy, book.AskPrice*(1+exchangeTruthFlattenSlip)
				}
				_, oerr := v.PlaceOrder(ctx, venue.OrderRequest{
					Market: venue.MarketPerp, Symbol: p.Symbol, Side: side, Type: venue.OrderTypeIOC,
					Qty: math.Abs(p.PositionAmt), Price: price, ReduceOnly: true,
					ClientOrderID: fmt.Sprintf("autoflat%d", time.Now().UnixNano()%1e12),
				})
				if oerr != nil {
					lastErrs = append(lastErrs, fmt.Sprintf("%s %s close: %v", name, p.Symbol, oerr))
				}
			}
		}
		if remaining == 0 && len(lastErrs) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(750 * time.Millisecond):
		}
	}
	// 最终必须重新读取四所确认归零；任何查询失败也视为未完成。
	var errs []string
	for name, v := range venues {
		positions, err := v.FetchPositions(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s verify: %v", name, err))
			continue
		}
		for _, p := range positions {
			if math.Abs(p.PositionAmt) > executionQtyTolerance {
				errs = append(errs, fmt.Sprintf("%s %s remains %.8f", name, p.Symbol, p.PositionAmt))
			}
		}
	}
	errs = append(errs, lastErrs...)
	if len(errs) > 0 {
		return fmt.Errorf("交易所实仓未完全归零: %s", strings.Join(errs, "; "))
	}
	return nil
}
