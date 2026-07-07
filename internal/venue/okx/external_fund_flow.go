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

// FetchNetExternalFundFlowUSDT 汇总 [since, now] 内离开/进入本账户体系的净流入(USDT)。
// 覆盖:①链上充值 ②链上提现 ③OKX 账户间内部转账(asset/bills type 116 转出/117 转入)。
// ③是 2026-07-07 硬停事故的修复:用户内部转账 42U 不上链,旧口径(仅链上充提)看不见,
// 风控把转账当成单日回撤 43.4≥35 触发账户级硬停——fail-safe 方向正确但口径有盲区。
// 注意:资金账户↔交易账户的所内划转(type 130/131)刻意不计——总权益口径(FetchEquityUSDT)
// 同时含两个账户,内部对倒不改变总权益,计入反而会造成假平移。
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
	internal, err := a.fetchOKXInternalTransferNetUSDT(ctx, startMs, endMs)
	if err != nil {
		return 0, err
	}
	return deposits - withdraws + internal, nil
}

// okx asset/bills 内部转账类型:116=转出(离开本账户),117=转入。
const (
	okxAssetBillInternalOut = "116"
	okxAssetBillInternalIn  = "117"
)

// fetchOKXInternalTransferNetUSDT 汇总资金账户流水中的内部转账净流入(116/117;USDT)。
func (a *Adapter) fetchOKXInternalTransferNetUSDT(ctx context.Context, startMs, endMs int64) (float64, error) {
	var total float64
	before := endMs
	for page := 0; page < 20; page++ {
		q := url.Values{}
		q.Set("ccy", "USDT")
		q.Set("before", strconv.FormatInt(startMs, 10)) // asset/bills:before=更早分页锚(语义与充提接口相反,按官方文档)
		q.Set("after", strconv.FormatInt(before, 10))
		q.Set("limit", "100")
		var rows []okxAssetBillRow
		if err := a.t.signedGet(ctx, pathAssetBills, q, &rows); err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			break
		}
		minTs := int64(0)
		for _, row := range rows {
			ts, _ := strconv.ParseInt(strings.TrimSpace(row.Ts), 10, 64)
			if minTs == 0 || ts < minTs {
				minTs = ts
			}
			if ts < startMs || ts > endMs {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(row.Ccy), "USDT") {
				continue
			}
			if row.Type == okxAssetBillInternalOut || row.Type == okxAssetBillInternalIn {
				total += parseFloat(row.BalChg) // 116 为负、117 为正,balChg 自带符号
			}
		}
		if len(rows) < 100 || minTs <= startMs {
			break
		}
		before = minTs
	}
	return total, nil
}

type okxAssetBillRow struct {
	Ccy    string `json:"ccy"`
	Type   string `json:"type"`
	BalChg string `json:"balChg"`
	Ts     string `json:"ts"`
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
