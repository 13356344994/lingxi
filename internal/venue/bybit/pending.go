package bybit

import (
	"context"
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

func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	return nil, venue.ErrUnsupported // TODO(Stage3)
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
