package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"lingxi/internal/venue"
)

// fakeBybitTrader 是离线下单桩:记录下单/撤单,QueryOrder 默认回「已全量成交」,可按 clientOrderID 注入失败。
type fakeBybitTrader struct {
	placed      []venue.OrderRequest
	canceled    []string
	fills       map[string]venue.OrderResult
	placeErr    map[string]error
	queryErr    map[string]error
	positions   []venue.Position
	balances    venue.Balances
	instruments []venue.Instrument
}

func newFakeBybitTrader() *fakeBybitTrader {
	return &fakeBybitTrader{
		fills:    map[string]venue.OrderResult{},
		placeErr: map[string]error{},
		queryErr: map[string]error{},
	}
}

func (f *fakeBybitTrader) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	f.placed = append(f.placed, req)
	if err := f.placeErr[req.ClientOrderID]; err != nil {
		return venue.OrderResult{}, err
	}
	if _, ok := f.fills[req.ClientOrderID]; !ok {
		f.fills[req.ClientOrderID] = venue.OrderResult{
			ClientOrderID: req.ClientOrderID,
			Status:        "filled",
			ExecutedQty:   req.Qty,
			AvgPrice:      100,
			CumQuoteUSDT:  req.Qty * 100,
		}
	}
	return f.fills[req.ClientOrderID], nil
}

func (f *fakeBybitTrader) CancelOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) error {
	f.canceled = append(f.canceled, clientOrderID)
	return nil
}

func (f *fakeBybitTrader) QueryOrder(_ context.Context, _ venue.Market, _ venue.Symbol, clientOrderID string) (venue.OrderResult, error) {
	if err := f.queryErr[clientOrderID]; err != nil {
		return venue.OrderResult{}, err
	}
	if r, ok := f.fills[clientOrderID]; ok {
		return r, nil
	}
	return venue.OrderResult{ClientOrderID: clientOrderID, Status: "new"}, nil
}

func (f *fakeBybitTrader) FetchPositions(_ context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

func (f *fakeBybitTrader) FetchBalances(_ context.Context) (venue.Balances, error) {
	return f.balances, nil
}

func (f *fakeBybitTrader) FetchInstruments(_ context.Context) ([]venue.Instrument, error) {
	return f.instruments, nil
}

func newTestExecutor(tr bybitTrader) *BybitLiveExecutor {
	e := NewBybitLiveExecutor(tr, nil, "")
	e.confirmTimeout = 200 * time.Millisecond
	e.confirmPoll = 5 * time.Millisecond
	return e
}

func samplePlan() BybitOpenPlan {
	return BybitOpenPlan{
		Symbol:          "HOME-USDT",
		BaseQty:         10,
		SpotPrice:       100,
		PerpPrice:       100,
		SpotNotional:    1000,
		PerpNotional:    1000,
		FundingRate:     0.001,
		NextFundingTime: time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC),
	}
}

func TestOpenDeltaNeutralHappyPath(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	pos, err := e.OpenDeltaNeutral(context.Background(), samplePlan(), "lx-HOMEUSDT-1", "run-1")
	if err != nil {
		t.Fatalf("开仓应成功: %v", err)
	}
	if pos.BaseQty != 10 {
		t.Fatalf("数量应为 10, got %v", pos.BaseQty)
	}
	if len(tr.placed) != 2 {
		t.Fatalf("应下 2 腿(永续空+现货多), got %d", len(tr.placed))
	}
	// 第一腿:永续空。
	if tr.placed[0].Market != venue.MarketPerp || tr.placed[0].Side != venue.SideSell {
		t.Fatalf("第一腿应为永续卖出, got %+v", tr.placed[0])
	}
	// 第二腿:现货多。
	if tr.placed[1].Market != venue.MarketSpot || tr.placed[1].Side != venue.SideBuy {
		t.Fatalf("第二腿应为现货买入, got %+v", tr.placed[1])
	}
	if len(tr.canceled) != 0 {
		t.Fatalf("happy path 不应有撤单, got %v", tr.canceled)
	}
}

func TestOpenDeltaNeutralSpotFailRevertsPerp(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	// 现货腿(后缀 -os)下单失败 → 必须回补永续空(买入 reduceOnly,后缀 -rp)。
	tr.placeErr["lx-HOMEUSDT-1-os"] = errors.New("insufficient balance")
	_, err := e.OpenDeltaNeutral(context.Background(), samplePlan(), "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("现货腿失败时开仓应返回错误")
	}
	// 应有一笔回补单:永续买入 + reduceOnly。
	var foundRevert bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketPerp && o.Side == venue.SideBuy && o.ReduceOnly {
			foundRevert = true
		}
	}
	if !foundRevert {
		t.Fatalf("现货失败后应回补永续空腿(买入 reduceOnly), placed=%+v", tr.placed)
	}
}

func TestOpenDeltaNeutralPerpFailNoSpot(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	tr.placeErr["lx-HOMEUSDT-1-op"] = errors.New("perp rejected")
	_, err := e.OpenDeltaNeutral(context.Background(), samplePlan(), "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("永续腿失败时开仓应返回错误")
	}
	// 不应进入现货腿。
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot {
			t.Fatalf("永续腿失败后不应再开现货腿, placed=%+v", tr.placed)
		}
	}
}

func TestCloseDeltaNeutral(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	if err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
		t.Fatalf("平仓应成功: %v", err)
	}
	if len(tr.placed) != 2 {
		t.Fatalf("平仓应下 2 腿, got %d", len(tr.placed))
	}
	var perpClose, spotClose bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketPerp && o.Side == venue.SideBuy && o.ReduceOnly {
			perpClose = true
		}
		if o.Market == venue.MarketSpot && o.Side == venue.SideSell {
			spotClose = true
		}
	}
	if !perpClose || !spotClose {
		t.Fatalf("平仓应含永续买回(reduceOnly)+ 现货卖出, placed=%+v", tr.placed)
	}
}

func TestCloseDeltaNeutralSpotSellsAvailableBalance(t *testing.T) {
	tr := newFakeBybitTrader()
	// 模拟 Bybit 现货买入 taker 费在 base 资产里扣:开仓名义 BaseQty=10,但实际只持有 9.97;现货 lot step=0.01。
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"HOME": {Free: 9.97}}}
	tr.instruments = []venue.Instrument{{Symbol: "HOME-USDT", Market: venue.MarketSpot, StepSize: 0.01, BaseAsset: "HOME"}}
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	if err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
		t.Fatalf("平仓应成功(按真实余额卖出): %v", err)
	}
	var spotQty float64
	var found bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot && o.Side == venue.SideSell {
			spotQty, found = o.Qty, true
		}
	}
	if !found {
		t.Fatalf("应有现货卖出腿, placed=%+v", tr.placed)
	}
	if spotQty != 9.97 {
		t.Fatalf("现货卖出应按真实可用余额 9.97(而非名义 10), got %v", spotQty)
	}
}

func TestCloseDeltaNeutralLegFailReturnsError(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	tr.placeErr["lx-HOMEUSDT-1-cs"] = errors.New("spot sell failed")
	if err := e.CloseDeltaNeutral(context.Background(), pos); err == nil {
		t.Fatal("一腿平仓失败时应返回错误(供上层保留持仓重试)")
	}
}

func TestBybitLegClientIDTruncates(t *testing.T) {
	long := "lx-VERYLONGSYMBOLNAMEUSDT-1700000000000000"
	id := bybitLegClientID(long, "op")
	if len(id) > 36 {
		t.Fatalf("clientOrderID 应 ≤36 字符, got %d (%s)", len(id), id)
	}
}

func TestIsBybitDuplicateOrderError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("orderLinkId exist"), true},
		{errors.New("retCode 110072"), true},
		{errors.New("some random error"), false},
	}
	for i, tc := range cases {
		if got := isBybitDuplicateOrderError(tc.err); got != tc.want {
			t.Fatalf("case %d: want %v got %v", i, tc.want, got)
		}
	}
}

