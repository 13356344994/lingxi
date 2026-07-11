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

// pending.go 汇集尚未实现的方法,一律返回 venue.ErrUnsupported。
// 引擎按 Capabilities 裁剪,这些调用对契约一致性套件安全(纯返回、不触网)。

func (a *Adapter) PlaceBatch(ctx context.Context, reqs []venue.OrderRequest) ([]venue.OrderResult, error) {
	return nil, venue.ErrUnsupported // TODO: OKX 批量下单端点未接入
}

func (a *Adapter) BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	return venue.ErrUnsupported // SupportsNegativeCarry=false,负 carry 借还未接入
}

func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("instType", instTypeSwap)
	q.Set("instId", raw)
	q.Set("type", "8") // OKX account bills: funding fee
	q.Set("begin", strconv.FormatInt(since.UTC().UnixMilli(), 10))
	q.Set("limit", "100")
	var rows []struct {
		InstID string `json:"instId"`
		Ccy    string `json:"ccy"`
		BalChg string `json:"balChg"`
		PnL    string `json:"pnl"`
		TS     string `json:"ts"`
	}
	if err := a.t.signedGet(ctx, pathAccountBills, q, &rows); err != nil {
		return nil, err
	}
	out := make([]venue.FundingIncome, 0, len(rows))
	for _, row := range rows {
		income := parseFloat(row.BalChg)
		if income == 0 {
			income = parseFloat(row.PnL)
		}
		at := msStringToTime(row.TS)
		if at.IsZero() || at.Before(since.UTC()) {
			continue
		}
		symbol := s
		if strings.TrimSpace(row.InstID) != "" {
			if mapped, merr := a.ToCanonical(row.InstID, venue.MarketPerp); merr == nil {
				symbol = mapped
			}
		}
		if symbol == "" {
			return nil, fmt.Errorf("okx: funding bill 缺少 symbol")
		}
		out = append(out, venue.FundingIncome{Symbol: symbol, Income: income, Asset: row.Ccy, Time: at})
	}
	return out, nil
}

func (a *Adapter) FetchUserTrades(ctx context.Context, m venue.Market, s venue.Symbol, since time.Time) ([]venue.Trade, error) {
	return nil, venue.ErrUnsupported // TODO: fills 成交历史
}

func (a *Adapter) StreamBookTicker(ctx context.Context, syms []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3): WS 行情
}

func (a *Adapter) StreamDepth(ctx context.Context, syms []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3): WS 深度
}
