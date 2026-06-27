package gate

import (
	"context"
	"log/slog"
	"math"
	"time"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

var lastGateAvailDiag time.Time

// FetchPerpAvailableUSDT 返回开仓可用 USDT。
// 统一账户:读 /unified/accounts 的跨保证金可用(子账户 available 严重低估);失败/为 0 时回退 max(futures,spot) 兜底。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	if a.Capabilities().UnifiedWallet {
		uni, uerr := a.fetchUnifiedUSDT(ctx)
		fut, _ := a.fetchFuturesUSDT(ctx)
		spot, _ := a.fetchSpotUSDT(ctx)
		// 诊断(节流 120s):暴露 unified/futures/spot 三个口径与 unified 错误,定位 Gate 可用为何偏低。
		if time.Since(lastGateAvailDiag) > 120*time.Second {
			lastGateAvailDiag = time.Now()
			slog.Info("gate perp available diag", "unified", uni, "unified_err", uerr, "futures", fut, "spot", spot)
		}
		if uerr == nil && uni > 0 {
			return uni, nil
		}
		return math.Max(fut, spot), nil
	}
	return a.fetchFuturesUSDT(ctx)
}

// TopUpPerpMarginUSDT 从 spot 划进 futures,只补差额。
// 统一账户(UnifiedWallet):/wallet/transfers spot→futures 被 Gate 拒绝(PERPETUAL_CONTRACT_TRANSFER_USDT_EXCEPTION),
// 划不动,直接返回 0;统一保证金可用不足需在交易所内把现货 USDT 转入统一账户(运维动作)。
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
