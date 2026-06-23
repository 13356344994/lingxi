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

// pending.go 汇集尚未实现的方法,一律返回 venue.ErrUnsupported。

func (a *Adapter) PlaceBatch(ctx context.Context, reqs []venue.OrderRequest) ([]venue.OrderResult, error) {
	return nil, venue.ErrUnsupported // TODO: Gate 批量下单端点未接入
}

func (a *Adapter) BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	return venue.ErrUnsupported // SupportsNegativeCarry=false
}

// gateFuturesAccountBookRow 是 GET /futures/usdt/account_book 单条流水(仅取本方法所需字段)。
// time 为 Unix 秒(浮点,可含小数);change 为变动额字符串(资金费 type=fund);contract 仅 2023-10-30 后有值。
type gateFuturesAccountBookRow struct {
	Time     float64 `json:"time"`
	Change   string  `json:"change"`
	Type     string  `json:"type"`
	Contract string  `json:"contract"`
	Text     string  `json:"text"`
}

// FetchRealizedFunding 拉某 U 本位永续合约自 since 起的真实资金费结算流水。
// 走 GET /api/v4/futures/usdt/account_book(type=fund),与现有签名 GET(account.go/capital.go)同一
// t.signedGet 签名/HTTP 路径。change>0=收到、<0=支付,U 本位单位 USDT。
// 与 bybit 实现同形:返回逐条 FundingIncome(由调用方求和);窗口外/无结算时返回空切片 + nil(已核实=0),
// 从而让 app 层资金费停止回退到估算。
func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	if a == nil || a.t == nil {
		return nil, fmt.Errorf("gate: transport 未初始化")
	}
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return nil, err
	}
	endSec := a.now().Unix()
	var startSec int64
	if !since.IsZero() {
		startSec = since.Unix()
	}
	const pageLimit = 1000 // Gate account_book 单页上限;单合约资金费稀疏,翻页仅作兜底。
	out := make([]venue.FundingIncome, 0, 8)
	offset := 0
	for page := 0; page < 50; page++ {
		q := url.Values{}
		q.Set("contract", raw)
		q.Set("type", "fund")
		if startSec > 0 {
			q.Set("from", strconv.FormatInt(startSec, 10))
		}
		if endSec > 0 {
			q.Set("to", strconv.FormatInt(endSec, 10))
		}
		q.Set("limit", strconv.Itoa(pageLimit))
		q.Set("offset", strconv.Itoa(offset))
		var rows []gateFuturesAccountBookRow
		if err := a.t.signedGet(ctx, pathFuturesAccountBook, q, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !strings.EqualFold(strings.TrimSpace(r.Type), "fund") {
				continue
			}
			// contract 字段 2023-10-30 后才有;有值时再做防御性核对(查询已按 contract 服务端过滤)。
			if c := strings.TrimSpace(r.Contract); c != "" && !strings.EqualFold(c, raw) {
				continue
			}
			out = append(out, venue.FundingIncome{
				Symbol: s,
				Income: parseFloat(r.Change),
				Asset:  "USDT",
				Time:   gateAccountBookTime(r.Time),
			})
		}
		if len(rows) < pageLimit {
			break
		}
		offset += len(rows)
	}
	return out, nil
}

// gateAccountBookTime 把 Gate 的 Unix 秒(浮点)转 UTC time;<=0 视作零值。
func gateAccountBookTime(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	whole := int64(sec)
	nsec := int64((sec - float64(whole)) * 1e9)
	return time.Unix(whole, nsec).UTC()
}

func (a *Adapter) FetchUserTrades(ctx context.Context, m venue.Market, s venue.Symbol, since time.Time) ([]venue.Trade, error) {
	return nil, venue.ErrUnsupported // TODO: 成交历史
}

func (a *Adapter) StreamBookTicker(ctx context.Context, syms []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported // TODO: WS 行情
}

func (a *Adapter) StreamDepth(ctx context.Context, syms []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported // TODO: WS 深度
}

func (a *Adapter) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	return nil, venue.ErrUnsupported // TODO: WS 私有频道
}
