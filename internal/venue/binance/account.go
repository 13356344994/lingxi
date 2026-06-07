package binance

import (
	"context"

	"lingxi/internal/venue"
)

// FetchTradingCostProfile 委托 client.ResolveAccountTradingCostProfile,翻译成中性 CostProfile。
// 币安返回的是账户级真实吃单费率(已反映 VIP 阶梯与 BNB 抵扣),故 DiscountApplied=true。
func (a *Adapter) FetchTradingCostProfile(ctx context.Context, syms []venue.Symbol) (venue.CostProfile, error) {
	spotSymbols := make([]string, 0, len(syms))
	futuresSymbols := make([]string, 0, len(syms))
	for _, s := range syms {
		if spot, err := a.ToVenue(s, venue.MarketSpot); err == nil {
			spotSymbols = append(spotSymbols, spot)
		}
		if perp, err := a.ToVenue(s, venue.MarketPerp); err == nil {
			futuresSymbols = append(futuresSymbols, perp)
		}
	}
	profile, err := a.client.ResolveAccountTradingCostProfile(ctx, spotSymbols, futuresSymbols, nil)
	if err != nil {
		return venue.CostProfile{}, err
	}
	return venue.CostProfile{
		SpotTaker:       profile.SpotTakerFeeRate,
		FuturesTaker:    profile.FuturesTakerFeeRate,
		DiscountApplied: true,
	}, nil
}

// MaxBorrowable 委托 client.MaxBorrowable(负 carry 借币用)。
func (a *Adapter) MaxBorrowable(ctx context.Context, asset string) (float64, error) {
	return a.client.MaxBorrowable(ctx, asset)
}

// 以下账户子能力当前由 internal/app 层签名端点实现(持仓/余额/杠杆档/key 自检),P1 迁入本包。
func (a *Adapter) FetchBalances(ctx context.Context) (venue.Balances, error) {
	return venue.Balances{}, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchPositions(ctx context.Context) ([]venue.Position, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchLeverageBrackets(ctx context.Context, s venue.Symbol) ([]venue.LeverageBracket, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) VerifyAPIKeyPosture(ctx context.Context) (venue.KeyPosture, error) {
	return venue.KeyPosture{}, venue.ErrUnsupported // TODO(P1)
}
