package bybit

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// pending.go 汇集尚未实现的方法,一律返回 venue.ErrUnsupported。
// 引擎当前并不路由到 Bybit 下单(仍是币安单所执行),这些方法对契约一致性套件安全(纯返回、不触网)。
// 落地路线:
//   - Trading 写动作(下单/撤单/查单/设杠杆/切保证金模式)→ 已在 trading.go 实现(Stage 3A)
//   - Trading 负 carry / 成交 / 资金费收入                → Stage 3 后续(下方仍 ErrUnsupported)
//   - Streaming(行情流 / 用户数据流)                     → Stage 3B
//   - CapitalManagement(钱包快照 / 调仓)                 → Stage 4(迁入 capital.go)

// ---- Trading 余项(Stage 3 后续) ----

func (a *Adapter) BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	return venue.ErrUnsupported // TODO(Stage3):统一账户借币做空(Capabilities.SupportsNegativeCarry 同步置 true)
}

// FetchRealizedFunding 拉某永续标的自 since 起的真实资金费结算流水(/v5/account/transaction-log, type=SETTLEMENT)。
// 用于平仓时给出「真实收到/支付的资金费」,而非按开仓费率估算。change>0=收到,<0=支付。
func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("accountType", "UNIFIED")
	q.Set("category", categoryLinear)
	q.Set("symbol", raw)
	q.Set("type", "SETTLEMENT")
	if !since.IsZero() {
		q.Set("startTime", strconv.FormatInt(since.UnixMilli(), 10))
	}
	q.Set("limit", "50")
	var res struct {
		List []struct {
			Symbol          string `json:"symbol"`
			Type            string `json:"type"`
			Change          string `json:"change"`
			Funding         string `json:"funding"`
			Currency        string `json:"currency"`
			TransactionTime string `json:"transactionTime"`
		} `json:"list"`
	}
	if err := a.t.signedGet(ctx, pathTransactionLog, q, &res); err != nil {
		return nil, err
	}
	out := make([]venue.FundingIncome, 0, len(res.List))
	for _, it := range res.List {
		if !strings.EqualFold(strings.TrimSpace(it.Type), "SETTLEMENT") {
			continue
		}
		income := parseFloat(it.Change)
		if income == 0 {
			income = parseFloat(it.Funding)
		}
		var ts time.Time
		if ms, e := strconv.ParseInt(strings.TrimSpace(it.TransactionTime), 10, 64); e == nil && ms > 0 {
			ts = time.UnixMilli(ms).UTC()
		}
		out = append(out, venue.FundingIncome{Symbol: s, Income: income, Asset: it.Currency, Time: ts})
	}
	return out, nil
}

func (a *Adapter) FetchUserTrades(ctx context.Context, m venue.Market, s venue.Symbol, since time.Time) ([]venue.Trade, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3)
}

// ---- Streaming(Stage 3) ----

func (a *Adapter) StreamBookTicker(ctx context.Context, syms []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3)
}

func (a *Adapter) StreamDepth(ctx context.Context, syms []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3)
}

func (a *Adapter) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3)
}

// CapitalManagement 已在 capital.go 实现(资金↔统一交易账户划转;UTA 统一保证金,只设「交易总额入统一、
// 余量留资金账户」的单钱包目标,现货/合约 50/50 在下单时分,不做钱包预切)。
