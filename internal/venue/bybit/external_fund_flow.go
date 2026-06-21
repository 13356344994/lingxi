package bybit

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
	bybitDepositStatusSuccess  = 3
	bybitWithdrawStatusSuccess = "success"
)

// FetchNetExternalFundFlowUSDT 汇总 [since, now] 内外部充提净流入(USDT,不含账户间划转)。
func (a *Adapter) FetchNetExternalFundFlowUSDT(ctx context.Context, since time.Time) (float64, error) {
	if a == nil || a.t == nil {
		return 0, fmt.Errorf("bybit: transport 未初始化")
	}
	endMs := a.now().UnixMilli()
	startMs := since.UnixMilli()
	if startMs >= endMs {
		return 0, nil
	}
	deposits, err := a.fetchBybitDepositUSDT(ctx, startMs, endMs)
	if err != nil {
		return 0, err
	}
	withdraws, err := a.fetchBybitWithdrawUSDT(ctx, startMs, endMs)
	if err != nil {
		return 0, err
	}
	return deposits - withdraws, nil
}

func (a *Adapter) fetchBybitDepositUSDT(ctx context.Context, startMs, endMs int64) (float64, error) {
	var total float64
	cursor := ""
	for page := 0; page < 20; page++ {
		q := url.Values{}
		q.Set("coin", "USDT")
		q.Set("startTime", strconv.FormatInt(startMs, 10))
		q.Set("endTime", strconv.FormatInt(endMs, 10))
		q.Set("limit", "50")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Rows           []bybitDepositRow `json:"rows"`
			NextPageCursor string            `json:"nextPageCursor"`
		}
		if err := a.t.signedGet(ctx, pathDepositQueryRecord, q, &res); err != nil {
			return 0, err
		}
		for _, row := range res.Rows {
			if row.DepositStatus != bybitDepositStatusSuccess {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Coin), "USDT") {
				continue
			}
			total += parseFloat(row.Amount)
		}
		if res.NextPageCursor == "" || len(res.Rows) == 0 {
			break
		}
		cursor = res.NextPageCursor
	}
	return total, nil
}

func (a *Adapter) fetchBybitWithdrawUSDT(ctx context.Context, startMs, endMs int64) (float64, error) {
	var total float64
	cursor := ""
	for page := 0; page < 20; page++ {
		q := url.Values{}
		q.Set("coin", "USDT")
		q.Set("startTime", strconv.FormatInt(startMs, 10))
		q.Set("endTime", strconv.FormatInt(endMs, 10))
		q.Set("limit", "50")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var res struct {
			Rows           []bybitWithdrawRow `json:"rows"`
			NextPageCursor string             `json:"nextPageCursor"`
		}
		if err := a.t.signedGet(ctx, pathWithdrawQueryRecord, q, &res); err != nil {
			return 0, err
		}
		for _, row := range res.Rows {
			if !strings.EqualFold(strings.TrimSpace(row.Status), bybitWithdrawStatusSuccess) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Coin), "USDT") {
				continue
			}
			total += parseFloat(row.Amount)
		}
		if res.NextPageCursor == "" || len(res.Rows) == 0 {
			break
		}
		cursor = res.NextPageCursor
	}
	return total, nil
}

type bybitDepositRow struct {
	Coin          string `json:"coin"`
	Amount        string `json:"amount"`
	DepositStatus int    `json:"depositStatus"`
}

type bybitWithdrawRow struct {
	Coin   string `json:"coin"`
	Amount string `json:"amount"`
	Status string `json:"status"`
}
