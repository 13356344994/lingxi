package binance

import (
	"context"
	"time"

	"lingxi/internal/venue"
)

// pending.go 汇集「实现逻辑仍在 internal/app 层、尚未下沉到本适配器」的方法。
// 它们当前一律返回 venue.ErrUnsupported,引擎在 P0 仍走旧的 *BinanceClient 直连路径,
// 线上行为不变。各方法按阶段下沉:
//   - Trading(下单/撤单/查单/设杠杆/借还/成交/资金费收入)→ P1「下沉端点知识」
//   - Streaming(行情流 / 用户数据流 listenKey)              → P1
//   - CapitalManagement(钱包快照 / 调仓)                    → P4(迁入 capital.go)

// ---- Trading(P1) ----

func (a *Adapter) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	return venue.OrderResult{}, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) PlaceBatch(ctx context.Context, reqs []venue.OrderRequest) ([]venue.OrderResult, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) CancelOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) error {
	return venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) QueryOrder(ctx context.Context, m venue.Market, s venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	return venue.OrderResult{}, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) SetLeverage(ctx context.Context, s venue.Symbol, lev int) error {
	return venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) SetMarginMode(ctx context.Context, s venue.Symbol, mode venue.MarginMode) error {
	return venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	return venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchUserTrades(ctx context.Context, m venue.Market, s venue.Symbol, since time.Time) ([]venue.Trade, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

// ---- Streaming(P1) ----

func (a *Adapter) StreamBookTicker(ctx context.Context, syms []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) StreamDepth(ctx context.Context, syms []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	return nil, venue.ErrUnsupported // TODO(P1)
}

// ---- CapitalManagement(P4) ----

func (a *Adapter) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	return nil, 0, venue.ErrUnsupported // TODO(P4): 迁入 capital.go
}

func (a *Adapter) PlanAllocation(totalUSDT float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	// P4 实现:25/25/50(spot/cross_margin/um_futures),余量入 funding。当前返回空计划。
	return venue.CapitalAllocationPlan{TotalTradingUSDT: totalUSDT, RemainderWallet: WalletFunding}
}

func (a *Adapter) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	return nil, venue.ErrUnsupported // TODO(P4)
}
