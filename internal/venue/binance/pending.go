package binance

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// pending.go 汇集尚未下沉到本适配器的方法,一律返回 venue.ErrUnsupported。
// 引擎在过渡期仍走 internal/app 旧路径;跨所对冲 Stage 3 起改用已实现子集。

// ---- Trading(未实现) ----

func (a *Adapter) PlaceBatch(ctx context.Context, reqs []venue.OrderRequest) ([]venue.OrderResult, error) {
	// 币安 batchOrders 参数复杂(同 symbol 批量/JSON 数组编码),后续单独接入。
	return nil, venue.ErrUnsupported
}

func (a *Adapter) BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error {
	if amt <= 0 {
		return nil
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" {
		return fmt.Errorf("binance BorrowRepay: asset 为空")
	}
	var action string
	switch dir {
	case venue.BorrowDirBorrow:
		action = "BORROW"
	case venue.BorrowDirRepay:
		action = "REPAY"
	default:
		return fmt.Errorf("binance BorrowRepay: 未知方向 %v", dir)
	}
	params := url.Values{}
	params.Set("asset", asset)
	params.Set("isIsolated", "FALSE")
	params.Set("amount", formatRequestAmount(amt))
	params.Set("type", action)
	var payload struct {
		TranID int64 `json:"tranId"`
	}
	if err := a.signedPostForm(ctx, a.spotBaseURL()+PathMarginBorrowRepay, params, &payload); err != nil {
		return err
	}
	if payload.TranID == 0 {
		return fmt.Errorf("binance BorrowRepay: %s 回执缺少 tranId", strings.ToLower(action))
	}
	return nil
}

func (a *Adapter) FetchRealizedFunding(ctx context.Context, s venue.Symbol, since time.Time) ([]venue.FundingIncome, error) {
	// 资金费流水 /fapi/v1/income 仍由 app 层 settlement 使用;后续按需下沉。
	return nil, venue.ErrUnsupported
}

func (a *Adapter) FetchUserTrades(ctx context.Context, m venue.Market, s venue.Symbol, since time.Time) ([]venue.Trade, error) {
	// 成交明细 /fapi/v1/userTrades、/api/v3/myTrades 仍由 app 层 fees 模块使用。
	return nil, venue.ErrUnsupported
}

// ---- Streaming(P1 后续) ----

func (a *Adapter) StreamBookTicker(ctx context.Context, syms []venue.Symbol) (<-chan venue.BookTicker, error) {
	return nil, venue.ErrUnsupported // TODO(P1): WS @bookTicker + listenKey 续期
}

func (a *Adapter) StreamDepth(ctx context.Context, syms []venue.Symbol) (<-chan venue.Depth, error) {
	return nil, venue.ErrUnsupported // TODO(P1): WS depth
}

func (a *Adapter) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	return nil, venue.ErrUnsupported // TODO(P1): user data stream listenKey
}

// ---- CapitalManagement(P4) ----

func (a *Adapter) CaptureMovableSnapshot(ctx context.Context) ([]venue.WalletSnapshot, float64, error) {
	return nil, 0, venue.ErrUnsupported // TODO(P4): 迁入 capital.go
}

func (a *Adapter) PlanAllocation(totalUSDT float64, caps venue.Capabilities) venue.CapitalAllocationPlan {
	// 跨所永续腿:交易总额全部进 U 本位合约,余量入 funding。
	if totalUSDT < 0 {
		totalUSDT = 0
	}
	return venue.CapitalAllocationPlan{
		TotalTradingUSDT: totalUSDT,
		Targets:          map[venue.Wallet]float64{WalletUMFutures: totalUSDT},
		RemainderWallet:  WalletFunding,
	}
}

func (a *Adapter) ExecuteRebalance(ctx context.Context, plan venue.CapitalAllocationPlan) ([]venue.Transfer, error) {
	return nil, venue.ErrUnsupported // TODO(P4)
}
