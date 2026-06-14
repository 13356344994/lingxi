package gate

import (
	"context"
	"log/slog"
	"math"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

// FetchPerpAvailableUSDT 返回 USDT 永续账户 available(不含现货)。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	return a.fetchFuturesUSDT(ctx)
}

// TopUpPerpMarginUSDT 从 spot 划进 futures,只补差额。
func (a *Adapter) TopUpPerpMarginUSDT(ctx context.Context, requiredUSDT float64) (float64, error) {
	requiredUSDT = math.Max(requiredUSDT, 0)
	current, err := a.FetchPerpAvailableUSDT(ctx)
	if err != nil {
		return 0, err
	}
	if current+1e-6 >= requiredUSDT {
		return 0, nil
	}
	need := requiredUSDT - current
	spot, err := a.fetchSpotUSDT(ctx)
	if err != nil {
		return 0, err
	}
	if spot <= 1e-6 {
		return 0, nil
	}
	amount := truncateTransferUSDT(math.Min(need, spot))
	if amount <= 1e-6 {
		return 0, nil
	}
	if _, err := a.transferUSDT(ctx, WalletSpot, WalletFutures, amount); err != nil {
		return 0, err
	}
	slog.Info("gate perp top-up: spot→futures", "amount", amount)
	return amount, nil
}
