package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

var _ venue.ExternalFundFlowProvider = (*Adapter)(nil)

const (
	binanceDepositStatusSuccess  = 1
	binanceWithdrawStatusSuccess = 6
	capitalHistoryPageLimit      = 1000
)

type binanceCapitalMovement struct {
	Amount string `json:"amount"`
	Coin   string `json:"coin"`
	Status int    `json:"status"`
}

// FetchNetExternalFundFlowUSDT 汇总 [since, now] 内外部充提净流入(稳定币按 1:1 USDT,不含所内划转)。
func (a *Adapter) FetchNetExternalFundFlowUSDT(ctx context.Context, since time.Time) (float64, error) {
	if a == nil || a.client == nil {
		return 0, fmt.Errorf("binance: client 未初始化")
	}
	endMs := a.now().UnixMilli()
	startMs := since.UnixMilli()
	if startMs >= endMs {
		return 0, nil
	}
	deposits, err := a.fetchBinanceCapitalMovementsUSDT(ctx, PathCapitalDepositHisrec, startMs, endMs, binanceDepositStatusSuccess)
	if err != nil {
		return 0, err
	}
	withdraws, err := a.fetchBinanceCapitalMovementsUSDT(ctx, PathCapitalWithdrawHistory, startMs, endMs, binanceWithdrawStatusSuccess)
	if err != nil {
		return 0, err
	}
	return deposits - withdraws, nil
}

func (a *Adapter) fetchBinanceCapitalMovementsUSDT(ctx context.Context, path string, startMs, endMs int64, wantStatus int) (float64, error) {
	var total float64
	for offset := 0; ; offset += capitalHistoryPageLimit {
		params := url.Values{}
		params.Set("startTime", strconv.FormatInt(startMs, 10))
		params.Set("endTime", strconv.FormatInt(endMs, 10))
		params.Set("offset", strconv.Itoa(offset))
		params.Set("limit", strconv.Itoa(capitalHistoryPageLimit))
		var batch []binanceCapitalMovement
		if err := a.signedGetJSON(ctx, a.spotBaseURL()+path, params, &batch); err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			if row.Status != wantStatus {
				continue
			}
			total += stableCoinAmountUSDT(row.Coin, row.Amount)
		}
		if len(batch) < capitalHistoryPageLimit {
			break
		}
	}
	return total, nil
}

func stableCoinAmountUSDT(coin, amount string) float64 {
	name := strings.ToUpper(strings.TrimSpace(coin))
	if name == "" {
		return 0
	}
	if _, ok := binanceEquityStableCoins[name]; !ok {
		return 0
	}
	return parseFloat(amount)
}
