package okx

import (
	"context"
	"log/slog"
	"math"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

// FetchPerpAvailableUSDT 返回统一交易账户 USDT 可用(availBal;账户级 availEq 作 fallback)。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	q := url.Values{}
	q.Set("ccy", "USDT")
	var rows []struct {
		AvailEq string `json:"availEq"`
		Details []struct {
			Ccy      string `json:"ccy"`
			AvailBal string `json:"availBal"`
		} `json:"details"`
	}
	if err := a.t.signedGet(ctx, pathBalance, q, &rows); err != nil {
		return 0, err
	}
	for _, acct := range rows {
		for _, d := range acct.Details {
			if strings.EqualFold(d.Ccy, "USDT") {
				if v := parseFloat(d.AvailBal); v > 0 {
					return v, nil
				}
			}
		}
		if v := parseFloat(acct.AvailEq); v > 0 {
			return v, nil
		}
	}
	return 0, nil
}

// TopUpPerpMarginUSDT 从 funding 划进 trading,只补差额。
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
	funding, err := a.fetchFundingUSDT(ctx)
	if err != nil {
		return 0, err
	}
	if funding <= 1e-6 {
		return 0, nil
	}
	amount := truncateTransferUSDT(math.Min(need, funding))
	if amount <= 1e-6 {
		return 0, nil
	}
	if _, err := a.transferUSDT(ctx, WalletFunding, WalletTrading, amount); err != nil {
		return 0, err
	}
	slog.Info("okx perp top-up: funding→trading", "amount", amount)
	return amount, nil
}
