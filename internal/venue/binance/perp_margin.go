package binance

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"

	"lingxi/internal/venue"
)

var _ venue.PerpMarginProvider = (*Adapter)(nil)

// FetchPerpAvailableUSDT 返回 U 本位合约钱包 USDT availableBalance(不含现货/资金/全仓)。
func (a *Adapter) FetchPerpAvailableUSDT(ctx context.Context) (float64, error) {
	var futures []struct {
		Asset            string `json:"asset"`
		AvailableBalance string `json:"availableBalance"`
	}
	if err := a.signedGetJSON(ctx, a.futuresBaseURL()+PathFuturesBalance, nil, &futures); err != nil {
		return 0, err
	}
	for _, row := range futures {
		if strings.EqualFold(strings.TrimSpace(row.Asset), "USDT") {
			return math.Max(parseFloat(row.AvailableBalance), 0), nil
		}
	}
	return 0, nil
}

// TopUpPerpMarginUSDT 从现货/资金/全仓闲置 USDT 划进 U 本位合约,只补 required 与当前可用之差(保守)。
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
	var transferred float64
	sources := []struct {
		label string
		read  func(context.Context) (float64, error)
		typ   string
	}{
		{"spot", a.fetchSpotFreeUSDT, "MAIN_UMFUTURE"},
		{"funding", a.fetchFundingFreeUSDT, "FUNDING_UMFUTURE"},
		{"cross_margin", a.fetchCrossMarginFreeUSDT, "MARGIN_UMFUTURE"},
	}
	for _, src := range sources {
		if need <= 1e-6 {
			break
		}
		avail, rerr := src.read(ctx)
		if rerr != nil {
			slog.Warn("binance perp top-up: read idle wallet failed", "wallet", src.label, "err", rerr)
			continue
		}
		if avail <= 1e-6 {
			continue
		}
		amount := math.Min(need, avail)
		amount = math.Floor(amount*1e8) / 1e8
		if amount <= 1e-6 {
			continue
		}
		if err := a.transferUSDT(ctx, src.typ, amount); err != nil {
			slog.Warn("binance perp top-up: transfer failed", "from", src.label, "type", src.typ, "amount", amount, "err", err)
			continue
		}
		slog.Info("binance perp top-up: transferred USDT to um_futures", "from", src.label, "type", src.typ, "amount", amount)
		transferred += amount
		need -= amount
	}
	return transferred, nil
}

func (a *Adapter) fetchSpotFreeUSDT(ctx context.Context) (float64, error) {
	var spot struct {
		Balances []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"balances"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathSpotAccount, nil, &spot); err != nil {
		return 0, err
	}
	for _, row := range spot.Balances {
		if strings.EqualFold(row.Asset, "USDT") {
			return math.Max(parseFloat(row.Free), 0), nil
		}
	}
	return 0, nil
}

func (a *Adapter) fetchFundingFreeUSDT(ctx context.Context) (float64, error) {
	var funding []struct {
		Asset string `json:"asset"`
		Free  string `json:"free"`
	}
	if err := a.signedPostForm(ctx, a.spotBaseURL()+PathAssetFundingAsset, nil, &funding); err != nil {
		return 0, err
	}
	for _, row := range funding {
		if strings.EqualFold(row.Asset, "USDT") {
			return math.Max(parseFloat(row.Free), 0), nil
		}
	}
	return 0, nil
}

func (a *Adapter) fetchCrossMarginFreeUSDT(ctx context.Context) (float64, error) {
	var margin struct {
		UserAssets []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"userAssets"`
	}
	if err := a.signedGetJSON(ctx, a.spotBaseURL()+PathMarginAccount, nil, &margin); err != nil {
		return 0, err
	}
	for _, row := range margin.UserAssets {
		if strings.EqualFold(row.Asset, "USDT") {
			return math.Max(parseFloat(row.Free), 0), nil
		}
	}
	return 0, nil
}

func (a *Adapter) transferUSDT(ctx context.Context, transferType string, amount float64) error {
	if amount <= 0 {
		return nil
	}
	params := url.Values{}
	params.Set("type", transferType)
	params.Set("asset", "USDT")
	params.Set("amount", fmtNum(amount))
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := a.signedPostForm(ctx, a.spotBaseURL()+PathAssetTransfer, params, &payload); err != nil {
		return err
	}
	if payload.TranID == 0 {
		return fmt.Errorf("binance transfer %s: missing tranId", transferType)
	}
	return nil
}
