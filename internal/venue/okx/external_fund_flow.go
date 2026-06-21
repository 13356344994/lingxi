package okx

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

const okxFundMovementStateSuccess = "2"

// FetchNetExternalFundFlowUSDT 汇总 [since, now] 内外部充提净流入(USDT,不含所内划转)。
func (a *Adapter) FetchNetExternalFundFlowUSDT(ctx context.Context, since time.Time) (float64, error) {
	if a == nil || a.t == nil {
		return 0, fmt.Errorf("okx: transport 未初始化")
	}
	endMs := a.now().UnixMilli()
	startMs := since.UnixMilli()
	if startMs >= endMs {
		return 0, nil
	}
	deposits, err := a.fetchOKXFundMovementsUSDT(ctx, pathDepositHistory, startMs, endMs)
	if err != nil {
		return 0, err
	}
	withdraws, err := a.fetchOKXFundMovementsUSDT(ctx, pathWithdrawalHistory, startMs, endMs)
	if err != nil {
		return 0, err
	}
	return deposits - withdraws, nil
}

func (a *Adapter) fetchOKXFundMovementsUSDT(ctx context.Context, path string, startMs, endMs int64) (float64, error) {
	var total float64
	for page := 0; page < 20; page++ {
		q := url.Values{}
		q.Set("ccy", "USDT")
		q.Set("before", strconv.FormatInt(endMs, 10))
		q.Set("after", strconv.FormatInt(startMs, 10))
		q.Set("limit", "100")
		var rows []okxFundMovementRow
		if err := a.t.signedGet(ctx, path, q, &rows); err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row.State != okxFundMovementStateSuccess {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Ccy), "USDT") {
				continue
			}
			total += parseFloat(row.Amt)
		}
		if len(rows) < 100 {
			break
		}
	}
	return total, nil
}

type okxFundMovementRow struct {
	Ccy   string `json:"ccy"`
	Amt   string `json:"amt"`
	State string `json:"state"`
}

func (a *Adapter) now() time.Time {
	return time.Now()
}
