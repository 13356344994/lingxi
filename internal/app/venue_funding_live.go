package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
)

// venue_funding_live.go 单所正费率 live 执行:空永续+买现货,Gate 现货买单 IOC 限价必带价。

type venueFundingTrader interface {
	PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error)
	CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error
	QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error)
	FetchPositions(ctx context.Context) ([]venue.Position, error)
	FetchBalances(ctx context.Context) (venue.Balances, error)
	FetchInstruments(ctx context.Context) ([]venue.Instrument, error)
	FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error)
}

type VenueFundingLiveExecutor struct {
	trader         venueFundingTrader
	venueName      string
	slippageBps    float64
	logf           func(string, ...any)
	confirmTimeout time.Duration
	confirmPoll    time.Duration
	now            func() time.Time
}

func NewVenueFundingLiveExecutor(trader venueFundingTrader, venueName string, slippageBps float64, logf func(string, ...any)) *VenueFundingLiveExecutor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if slippageBps <= 0 {
		slippageBps = 20
	}
	return &VenueFundingLiveExecutor{
		trader:         trader,
		venueName:      venueName,
		slippageBps:    slippageBps,
		logf:           logf,
		confirmTimeout: 8 * time.Second,
		confirmPoll:    400 * time.Millisecond,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

func venueFundingLegClientID(prefix, leg string) string {
	return bybitLegClientID(prefix, leg)
}

func (e *VenueFundingLiveExecutor) confirmFill(ctx context.Context, m venue.Market, s venue.Symbol, clientID string) (venue.OrderResult, error) {
	deadline := e.now().Add(e.confirmTimeout)
	var last venue.OrderResult
	for {
		res, err := e.trader.QueryOrder(ctx, m, s, clientID)
		if err == nil {
			last = res
			switch res.Status {
			case "filled":
				return res, nil
			case "rejected", "canceled":
				return res, fmt.Errorf("订单终态=%s", res.Status)
			}
		}
		if e.now().After(deadline) {
			if err != nil {
				return last, fmt.Errorf("确认成交超时: %w", err)
			}
			return last, fmt.Errorf("确认成交超时,状态=%s", last.Status)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(e.confirmPoll):
		}
	}
}

func (e *VenueFundingLiveExecutor) placeAndConfirm(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if _, err := e.trader.PlaceOrder(ctx, req); err != nil && !isVenueFundingDuplicateOrder(err) {
		return venue.OrderResult{}, err
	}
	return e.confirmFill(ctx, req.Market, req.Symbol, req.ClientOrderID)
}

func (e *VenueFundingLiveExecutor) spotBuyRequest(ctx context.Context, sym venue.Symbol, qty float64, clientID string) (venue.OrderRequest, error) {
	if venueFundingSpotUsesIOC(e.venueName) {
		bt, err := e.trader.FetchBookTicker(ctx, venue.MarketSpot, sym)
		if err != nil || bt.AskPrice <= 0 {
			return venue.OrderRequest{}, fmt.Errorf("Gate 现货 IOC 需盘口 ask: %v", err)
		}
		price := iocLimitPrice(venue.SideBuy, bt, e.slippageBps)
		return venue.OrderRequest{
			Market:        venue.MarketSpot,
			Symbol:        sym,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeIOC,
			Qty:           qty,
			Price:         price,
			ClientOrderID: clientID,
		}, nil
	}
	return venue.OrderRequest{
		Market:        venue.MarketSpot,
		Symbol:        sym,
		Side:          venue.SideBuy,
		Type:          venue.OrderTypeMarket,
		Qty:           qty,
		ClientOrderID: clientID,
	}, nil
}

// OpenDeltaNeutral 正 carry:永续空先行→现货多对冲;现货失败立即回补永续。
func (e *VenueFundingLiveExecutor) OpenDeltaNeutral(ctx context.Context, plan BybitOpenPlan, idemPrefix, runID string) (VenueFundingPosition, error) {
	if plan.BaseQty <= executionQtyTolerance {
		return VenueFundingPosition{}, fmt.Errorf("开仓数量过小")
	}
	perpReq := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        plan.Symbol,
		Side:          venue.SideSell,
		Type:          venue.OrderTypeMarket,
		Qty:           plan.BaseQty,
		ClientOrderID: venueFundingLegClientID(idemPrefix, "op"),
	}
	perpExec, err := e.placeAndConfirm(ctx, perpReq)
	if err != nil {
		if perpExec.ExecutedQty <= executionQtyTolerance {
			_ = e.trader.CancelOrder(ctx, venue.MarketPerp, plan.Symbol, perpReq.ClientOrderID)
			return VenueFundingPosition{}, fmt.Errorf("永续腿开仓失败: %w", err)
		}
		e.logf("[%s-live] %s 永续腿部分成交 %.6f: %v", e.venueName, plan.Symbol, perpExec.ExecutedQty, err)
	}
	hedgeQty := perpExec.ExecutedQty
	if hedgeQty <= executionQtyTolerance {
		return VenueFundingPosition{}, fmt.Errorf("永续腿成交为 0")
	}

	spotReq, serr := e.spotBuyRequest(ctx, plan.Symbol, hedgeQty, venueFundingLegClientID(idemPrefix, "os"))
	if serr != nil {
		e.revertPerp(ctx, plan.Symbol, hedgeQty, idemPrefix)
		return VenueFundingPosition{}, serr
	}
	spotExec, spotErr := e.placeAndConfirm(ctx, spotReq)
	if spotErr != nil || spotExec.ExecutedQty <= executionQtyTolerance {
		revertErr := e.revertPerp(ctx, plan.Symbol, hedgeQty, idemPrefix)
		if revertErr != nil {
			naked := VenueFundingPosition{
				Symbol:          plan.Symbol,
				BaseQty:         hedgeQty,
				PerpEntryPrice:  perpExec.AvgPrice,
				PerpNotional:    perpExec.CumQuoteUSDT,
				FundingRate:     plan.FundingRate,
				NextFundingTime: plan.NextFundingTime,
				OpenedAt:        e.now(),
				RunID:           runID,
				IdemPrefix:      idemPrefix,
			}
			return naked, fmt.Errorf("现货腿失败且永续回补失败: spot=%v revert=%w", spotErr, revertErr)
		}
		return VenueFundingPosition{}, fmt.Errorf("现货对冲腿失败,已回补永续: %v", firstNonNilErr(spotErr, fmt.Errorf("现货成交为 0")))
	}

	if spotExec.ExecutedQty+executionQtyTolerance < hedgeQty {
		excess := hedgeQty - spotExec.ExecutedQty
		if trimErr := e.revertPerp(ctx, plan.Symbol, excess, idemPrefix); trimErr != nil {
			return VenueFundingPosition{
				Symbol:           plan.Symbol,
				BaseQty:          hedgeQty,
				SpotEntryPrice:   spotExec.AvgPrice,
				PerpEntryPrice:   perpExec.AvgPrice,
				SpotNotional:     spotExec.CumQuoteUSDT,
				PerpNotional:     perpExec.CumQuoteUSDT,
				FundingRate:      plan.FundingRate,
				NextFundingTime:  plan.NextFundingTime,
				OpenedAt:         e.now(),
				RunID:            runID,
				IdemPrefix:       idemPrefix,
				OpenPerpFee:      perpExec.Fee,
				OpenPerpFeeAsset: perpExec.FeeAsset,
				OpenSpotFee:      spotExec.Fee,
				OpenSpotFeeAsset: spotExec.FeeAsset,
			}, fmt.Errorf("现货部分成交且永续削减失败: %w", trimErr)
		}
		hedgeQty = spotExec.ExecutedQty
	}

	return VenueFundingPosition{
		Symbol:           plan.Symbol,
		BaseQty:          hedgeQty,
		SpotEntryPrice:   spotExec.AvgPrice,
		PerpEntryPrice:   perpExec.AvgPrice,
		SpotNotional:     spotExec.CumQuoteUSDT,
		PerpNotional:     hedgeQty * perpExec.AvgPrice,
		FundingRate:      plan.FundingRate,
		NextFundingTime:  plan.NextFundingTime,
		OpenedAt:         e.now(),
		RunID:            runID,
		IdemPrefix:       idemPrefix,
		OpenPerpFee:      perpExec.Fee,
		OpenPerpFeeAsset: perpExec.FeeAsset,
		OpenSpotFee:      spotExec.Fee,
		OpenSpotFeeAsset: spotExec.FeeAsset,
	}, nil
}

func (e *VenueFundingLiveExecutor) revertPerp(ctx context.Context, sym venue.Symbol, qty float64, idemPrefix string) error {
	req := venue.OrderRequest{
		Market:        venue.MarketPerp,
		Symbol:        sym,
		Side:          venue.SideBuy,
		Type:          venue.OrderTypeMarket,
		Qty:           qty,
		ClientOrderID: venueFundingLegClientID(idemPrefix, "rp"),
		ReduceOnly:    true,
	}
	_, err := e.placeAndConfirm(ctx, req)
	if err != nil && isVenueFundingPositionZero(err) {
		return nil
	}
	return err
}

// CloseDeltaNeutral 先平永续再卖现货。
func (e *VenueFundingLiveExecutor) CloseDeltaNeutral(ctx context.Context, pos VenueFundingPosition) error {
	if pos.BaseQty <= executionQtyTolerance {
		return nil
	}
	perpQty := pos.BaseQty
	if actual := e.PerpOpenQty(ctx, pos.Symbol); actual >= 0 {
		if actual <= executionQtyTolerance {
			perpQty = 0
		} else if actual < perpQty {
			perpQty = actual
		}
	}
	var errs []string
	if perpQty > executionQtyTolerance {
		perpReq := venue.OrderRequest{
			Market:        venue.MarketPerp,
			Symbol:        pos.Symbol,
			Side:          venue.SideBuy,
			Type:          venue.OrderTypeMarket,
			Qty:           perpQty,
			ClientOrderID: venueFundingLegClientID(pos.IdemPrefix, "cp"),
			ReduceOnly:    true,
		}
		if _, err := e.placeAndConfirm(ctx, perpReq); err != nil && !isVenueFundingPositionZero(err) {
			errs = append(errs, "永续平仓: "+err.Error())
		}
	}
	spotSellQty := pos.BaseQty
	if avail, step, ok := e.spotSellableQty(ctx, pos.Symbol); ok {
		if avail < spotSellQty {
			spotSellQty = avail
		}
		if step > 0 {
			spotSellQty = roundDownToStep(spotSellQty, step)
		}
	}
	if spotSellQty > executionQtyTolerance {
		spotReq := venue.OrderRequest{
			Market:        venue.MarketSpot,
			Symbol:        pos.Symbol,
			Side:          venue.SideSell,
			Type:          venue.OrderTypeMarket,
			Qty:           spotSellQty,
			ClientOrderID: venueFundingLegClientID(pos.IdemPrefix, "cs"),
		}
		if venueFundingSpotUsesIOC(e.venueName) {
			bt, berr := e.trader.FetchBookTicker(ctx, venue.MarketSpot, pos.Symbol)
			if berr == nil && bt.BidPrice > 0 {
				spotReq.Type = venue.OrderTypeIOC
				spotReq.Price = iocLimitPrice(venue.SideSell, bt, e.slippageBps)
			}
		}
		if _, err := e.placeAndConfirm(ctx, spotReq); err != nil {
			errs = append(errs, "现货平仓: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (e *VenueFundingLiveExecutor) PerpOpenQty(ctx context.Context, sym venue.Symbol) float64 {
	positions, err := e.trader.FetchPositions(ctx)
	if err != nil {
		return -1
	}
	var q float64
	for _, p := range positions {
		if p.Symbol != sym {
			continue
		}
		amt := p.PositionAmt
		if amt < 0 {
			amt = -amt
		}
		q += amt
	}
	return q
}

func (e *VenueFundingLiveExecutor) spotSellableQty(ctx context.Context, sym venue.Symbol) (avail, step float64, ok bool) {
	bals, err := e.trader.FetchBalances(ctx)
	if err != nil {
		return 0, 0, false
	}
	base := baseAssetOf(string(sym))
	if instrs, ierr := e.trader.FetchInstruments(ctx); ierr == nil {
		for _, in := range instrs {
			if in.Symbol == sym && in.Market == venue.MarketSpot {
				step = in.StepSize
				if in.BaseAsset != "" {
					base = in.BaseAsset
				}
				break
			}
		}
	}
	b, found := bals.ByAsset[base]
	if !found {
		return 0, step, true
	}
	return b.Free, step, true
}

func isVenueFundingDuplicateOrder(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate") || strings.Contains(s, "exist") || strings.Contains(s, "duplicat")
}

func isVenueFundingPositionZero(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "position is zero") || strings.Contains(s, "110017") || strings.Contains(s, "no position")
}

// venueFundingUsesGateIOC 供单测钉死 Gate 现货 IOC 语义。
func venueFundingUsesGateIOC(venueName string) bool {
	return stringsEqualFoldVenue(venueName, gate.VenueName)
}
