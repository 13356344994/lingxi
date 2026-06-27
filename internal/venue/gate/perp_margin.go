package gate

import (
	"context"
	"log/slog"
	"math"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

// FetchPerpAvailableUSDT 返回开仓可用 USDT。
// 统一账户:读 /unified/accounts 的跨保证金可用(子账户 available 严重低估);失败/为 0 时回退 max(futures,spot) 兜底。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	if a.Capabilities().UnifiedWallet {
		if v, err := a.fetchUnifiedUSDT(ctx); err == nil && v > 0 {
			return v, nil
		}
		fut, _ := a.fetchFuturesUSDT(ctx)
		spot, _ := a.fetchSpotUSDT(ctx)
		return math.Max(fut, spot), nil
	}
	return a.fetchFuturesUSDT(ctx)
}

// TopUpPerpMarginUSDT 从 spot 划进 futures,只补差额。
// 统一账户(UnifiedWallet):现货与永续共用同一 USDT 池,spot→futures 划转是 no-op、补不动,
// 直接返回 0(避免误报「已补 X」却可用不变、每周期空转划转)。可用不足只能靠账户实际入金。
func (a *Adapter) TopUpPerpMarginUSDT(ctx context.Context, requiredUSDT float64) (float64, error) {
	if a.Capabilities().UnifiedWallet {
		return 0, nil
	}
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
