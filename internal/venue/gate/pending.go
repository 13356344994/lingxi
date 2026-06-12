package gate

import (
	"context"
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

func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	return nil, venue.ErrUnsupported // TODO: 资金费流水
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
