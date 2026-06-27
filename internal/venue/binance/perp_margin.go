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

// minPerpTopUpUSDT 是补保证金的最小划转额:低于此值的闲置零头(常为被抵押锁定的全仓杠杆残额)不搬,
// 避免触发 binance -3020「Transfer out amount exceeds max amount」并每周期刷告警。
const minPerpTopUpUSDT = 1.0

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
	// 资金来源:现货 / 全仓杠杆 / 资金钱包,补进 U 本位合约直到够交易仓本金。
	// 用户 2026-06-28 明确授权:方向量化下把各钱包(含 funding)资金调进合约账户,以便开仓。
	sources := []struct {
		label string
		read  func(context.Context) (float64, error)
		typ   string
	}{
		{"spot", a.fetchSpotFreeUSDT, "MAIN_UMFUTURE"},
		{"cross_margin", a.fetchCrossMarginFreeUSDT, "MARGIN_UMFUTURE"},
		{"funding", a.fetchFundingFreeUSDT, "FUNDING_UMFUTURE"},
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
		// 跳过灰尘级划转(<1 USDT):全仓杠杆等钱包常残留被抵押锁定的零头,转出会触发
		// -3020「exceeds max amount」并每决策周期刷告警噪声;低于此阈值不值得搬。
		if amount < minPerpTopUpUSDT {
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

// FetchSpotAvailableUSDT 返回现货钱包 USDT free(不含 U 本位合约/资金/全仓杠杆)。
func (a *Adapter) FetchSpotAvailableUSDT(ctx context.Context) (float64, error) {
	return a.fetchSpotFreeUSDT(ctx)
}

// FetchFundingAvailableUSDT 返回资金账户 USDT free。
func (a *Adapter) FetchFundingAvailableUSDT(ctx context.Context) (float64, error) {
	return a.fetchFundingFreeUSDT(ctx)
}

// FetchCrossMarginAvailableUSDT 返回全仓杠杆钱包 USDT free。
func (a *Adapter) FetchCrossMarginAvailableUSDT(ctx context.Context) (float64, error) {
	return a.fetchCrossMarginFreeUSDT(ctx)
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

// TransferWallet 在所内 spot/futures/margin 钱包之间划转 USDT(币安 universal transfer)。
// from/to 取 "spot"/"futures"/"margin";金额≤0 为 no-op。
func (a *Adapter) TransferWallet(ctx context.Context, from, to string, amountUSDT float64) error {
	if amountUSDT <= 0 {
		return nil
	}
	transferType, err := walletTransferType(from, to)
	if err != nil {
		return err
	}
	return a.transferUSDT(ctx, transferType, amountUSDT)
}

func walletTransferType(from, to string) (string, error) {
	from = strings.ToLower(strings.TrimSpace(from))
	to = strings.ToLower(strings.TrimSpace(to))
	switch {
	case from == "spot" && to == "futures":
		return "MAIN_UMFUTURE", nil
	case from == "futures" && to == "spot":
		return "UMFUTURE_MAIN", nil
	case from == "spot" && to == "margin":
		return "MAIN_MARGIN", nil
	case from == "margin" && to == "spot":
		return "MARGIN_MAIN", nil
	case from == "margin" && to == "futures":
		return "MARGIN_UMFUTURE", nil
	case from == "futures" && to == "margin":
		return "UMFUTURE_MARGIN", nil
	default:
		return "", fmt.Errorf("binance TransferWallet: unsupported %s -> %s", from, to)
	}
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
