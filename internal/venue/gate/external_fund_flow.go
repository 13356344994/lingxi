package gate

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

const gateWalletMovementStatusDone = "DONE"

// FetchNetExternalFundFlowUSDT 汇总 [since, now] 内外部充提净流入(USDT,不含所内划转)。
func (a *Adapter) FetchNetExternalFundFlowUSDT(ctx context.Context, since time.Time) (float64, error) {
	if a == nil || a.t == nil {
		return 0, fmt.Errorf("gate: transport 未初始化")
	}
	endSec := a.now().Unix()
	startSec := since.Unix()
	if startSec >= endSec {
		return 0, nil
	}
	deposits, err := a.fetchGateWalletMovementsUSDT(ctx, pathWalletDeposits, startSec, endSec)
	if err != nil {
		return 0, err
	}
	withdraws, err := a.fetchGateWalletMovementsUSDT(ctx, pathWalletWithdrawals, startSec, endSec)
	if err != nil {
		return 0, err
	}
	return deposits - withdraws, nil
}

func (a *Adapter) fetchGateWalletMovementsUSDT(ctx context.Context, path string, startSec, endSec int64) (float64, error) {
	var total float64
	offset := 0
	for page := 0; page < 20; page++ {
		q := url.Values{}
		q.Set("currency", "USDT")
		q.Set("from", strconv.FormatInt(startSec, 10))
		q.Set("to", strconv.FormatInt(endSec, 10))
		q.Set("limit", "100")
		q.Set("offset", strconv.Itoa(offset))
		var rows []gateWalletMovementRow
		if err := a.t.signedGet(ctx, path, q, &rows); err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if !strings.EqualFold(strings.TrimSpace(row.Status), gateWalletMovementStatusDone) {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Currency), "USDT") {
				continue
			}
			total += parseFloat(row.Amount)
		}
		if len(rows) < 100 {
			break
		}
		offset += len(rows)
	}
	return total, nil
}

type gateWalletMovementRow struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Status   string `json:"status"`
}

func (a *Adapter) now() time.Time {
	return time.Now()
}
