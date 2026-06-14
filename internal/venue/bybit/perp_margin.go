package bybit

import (
	"context"
	"log/slog"
	"math"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

// FetchPerpAvailableUSDT 返回 UNIFIED 账户可用于永续的 USDT(totalAvailableBalance 优先,否则 USDT avail)。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	q := url.Values{}
	q.Set("accountType", "UNIFIED")
	q.Set("coin", "USDT")
	var res struct {
		List []struct {
			TotalAvailableBalance string `json:"totalAvailableBalance"`
			Coin                  []struct {
				Coin                string `json:"coin"`
				AvailableToWithdraw string `json:"availableToWithdraw"`
				WalletBalance       string `json:"walletBalance"`
				Locked              string `json:"locked"`
			} `json:"coin"`
		} `json:"list"`
	}
	if err := a.t.signedGet(ctx, pathWalletBalance, q, &res); err != nil {
		return 0, err
	}
	if len(res.List) == 0 {
		return 0, nil
	}
	acct := res.List[0]
	if v := parseFloat(acct.TotalAvailableBalance); v > 0 {
		return v, nil
	}
	for _, c := range acct.Coin {
		if strings.EqualFold(c.Coin, "USDT") {
			free := parseFloat(c.AvailableToWithdraw)
			if free == 0 {
				free = parseFloat(c.WalletBalance) - parseFloat(c.Locked)
			}
			return math.Max(free, 0), nil
		}
	}
	return 0, nil
}

// TopUpPerpMarginUSDT 从 FUND 资金账户划进 UNIFIED,只补差额。
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
	fund, err := a.FetchFundingWalletBalance(ctx, "USDT")
	if err != nil {
		return 0, err
	}
	avail := math.Max(fund["USDT"], 0)
	if avail <= 1e-6 {
		return 0, nil
	}
	amount := truncateTransferUSDT(math.Min(need, avail))
	if amount <= 1e-6 {
		return 0, nil
	}
	if _, err := a.transferUSDT(ctx, WalletFunding, WalletUnified, amount); err != nil {
		return 0, err
	}
	slog.Info("bybit perp top-up: FUND→UNIFIED", "amount", amount)
	return amount, nil
}
