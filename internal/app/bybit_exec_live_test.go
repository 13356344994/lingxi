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
	placed         []venue.OrderRequest
	canceled       []string
	fills          map[string]venue.OrderResult
	placeErr       map[string]error
	queryErr       map[string]error
	positions      []venue.Position
	balances       venue.Balances
	instruments    []venue.Instrument
	fundingIncomes []venue.FundingIncome
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

func (f *fakeBybitTrader) FetchRealizedFunding(_ context.Context, _ venue.Symbol, _ time.Time) ([]venue.FundingIncome, error) {
	return f.fundingIncomes, nil
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

// TestOpenDeltaNeutralPartialSpotTrimsPerp 钉死 FIX_PLAN #3:现货腿部分成交(6/10)时,必须买回多出的永续(4)
// 让两腿对齐到 6,而不是把「永续 10 + 现货 6」当成全量对冲(差额 4 = 裸永续)。
func TestOpenDeltaNeutralPartialSpotTrimsPerp(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.fills["lx-HOMEUSDT-1-os"] = venue.OrderResult{
		ClientOrderID: "lx-HOMEUSDT-1-os", Status: "filled",
		ExecutedQty: 6, AvgPrice: 100, CumQuoteUSDT: 600,
	}
	e := newTestExecutor(tr)
	pos, err := e.OpenDeltaNeutral(context.Background(), samplePlan(), "lx-HOMEUSDT-1", "run-1")
	if err != nil {
		t.Fatalf("部分成交应削减永续后成功: %v", err)
	}
	if pos.BaseQty != 6 {
		t.Fatalf("两腿应对齐到现货成交量 6, got %v", pos.BaseQty)
	}
	var foundTrim bool
	var trimQty float64
	for _, o := range tr.placed {
		if o.Market == venue.MarketPerp && o.Side == venue.SideBuy && o.ReduceOnly {
			foundTrim = true
			trimQty = o.Qty
		}
	}
	if !foundTrim || trimQty != 4 {
		t.Fatalf("应买回永续 4 对齐两腿, foundTrim=%v trimQty=%v placed=%+v", foundTrim, trimQty, tr.placed)
	}
}

// TestOpenDeltaNeutralPartialSpotTrimFailReturnsNaked 钉死:削减永续失败时,返回错误且按永续全量记 BaseQty,
// 供平仓 reduceOnly 收敛 + reconcile 兜底,绝不静默把半量当全量。
func TestOpenDeltaNeutralPartialSpotTrimFailReturnsNaked(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.fills["lx-HOMEUSDT-1-os"] = venue.OrderResult{
		ClientOrderID: "lx-HOMEUSDT-1-os", Status: "filled",
		ExecutedQty: 6, AvgPrice: 100, CumQuoteUSDT: 600,
	}
	tr.placeErr["lx-HOMEUSDT-1-tp"] = errors.New("trim rejected")
	e := newTestExecutor(tr)
	pos, err := e.OpenDeltaNeutral(context.Background(), samplePlan(), "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("削减失败应返回错误(残留裸永续待平)")
	}
	if pos.BaseQty != 10 {
		t.Fatalf("削减失败应按永续全量记 BaseQty=10 供平仓收敛, got %v", pos.BaseQty)
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
	// 平仓自愈会先核对交易所真实状态:这里模拟「永续空 10 + 现货持有 10」的正常在场仓位。
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: -10}}
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"HOME": {Free: 10}}}
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
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

// TestCloseDeltaNeutralNegativeNakedPerpSkipsSpot 钉死 FIX_PLAN #5:负 carry 但现货腿未建立(裸永续,SpotEntryPrice=0)时,
// 平仓只平永续多头,绝不跑「现货买回还币」(否则凭空买入现货 + PnL 用 0 开仓价算错)。
func TestCloseDeltaNeutralNegativeNakedPerpSkipsSpot(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: 10}} // 裸永续多腿在场
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1", Negative: true, SpotEntryPrice: 0}
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
		t.Fatalf("裸永续平仓应成功: %v", err)
	}
	var perpClose bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot {
			t.Fatalf("现货腿未建立时不应下任何现货单, placed=%+v", tr.placed)
		}
		if o.Market == venue.MarketPerp && o.Side == venue.SideSell && o.ReduceOnly {
			perpClose = true
		}
	}
	if !perpClose {
		t.Fatalf("应平永续多头(卖出 reduceOnly), placed=%+v", tr.placed)
	}
}

func TestCloseDeltaNeutralSpotSellsAvailableBalance(t *testing.T) {
	tr := newFakeBybitTrader()
	// 模拟 Bybit 现货买入 taker 费在 base 资产里扣:开仓名义 BaseQty=10,但实际只持有 9.97;现货 lot step=0.01。
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: -10}}
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"HOME": {Free: 9.97}}}
	tr.instruments = []venue.Instrument{{Symbol: "HOME-USDT", Market: venue.MarketSpot, StepSize: 0.01, BaseAsset: "HOME"}}
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
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
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: -10}}
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"HOME": {Free: 10}}}
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1"}
	tr.placeErr["lx-HOMEUSDT-1-cs"] = errors.New("spot sell failed")
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err == nil {
		t.Fatal("一腿平仓失败时应返回错误(供上层保留持仓重试)")
	}
}

// TestOpenDeltaNeutralNegativeBorrowsSpotFirstThenLongsPerp 钉死负 carry 腿序反转(2026-06-12 HOME 实锤):
// 借币现货空腿先行(更容易失败的腿先验证),成交后才开永续多腿。
func TestOpenDeltaNeutralNegativeBorrowsSpotFirstThenLongsPerp(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	plan := samplePlan()
	plan.Negative = true
	plan.FundingRate = -0.001
	pos, err := e.OpenDeltaNeutral(context.Background(), plan, "lx-HOMEUSDT-1", "run-1")
	if err != nil {
		t.Fatalf("负 carry 开仓应成功: %v", err)
	}
	if !pos.Negative {
		t.Fatal("持仓应标记为负 carry")
	}
	if len(tr.placed) != 2 {
		t.Fatalf("应下 2 腿, got %d", len(tr.placed))
	}
	// 第一腿:现货卖出 + 借币(MarginEffect=Borrow)。
	if tr.placed[0].Market != venue.MarketSpot || tr.placed[0].Side != venue.SideSell {
		t.Fatalf("负 carry 第一腿应为现货借币卖出(做空), got %+v", tr.placed[0])
	}
	if tr.placed[0].MarginEffect != venue.MarginEffectBorrow {
		t.Fatalf("负 carry 现货做空腿应带 MarginEffectBorrow(自动借币), got %v", tr.placed[0].MarginEffect)
	}
	// 第二腿:永续多(BUY)。
	if tr.placed[1].Market != venue.MarketPerp || tr.placed[1].Side != venue.SideBuy {
		t.Fatalf("负 carry 第二腿应为永续买入(多), got %+v", tr.placed[1])
	}
}

// TestOpenDeltaNeutralNegativeBorrowFailNoPerpLeg 钉死止血核心:借币卖出失败(如 170207 借币池枯竭)时
// 零敞口干净放弃——绝不开永续腿、绝不产生回补单(旧版永续先行:白开又回补,纯烧手续费)。
func TestOpenDeltaNeutralNegativeBorrowFailNoPerpLeg(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	plan := samplePlan()
	plan.Negative = true
	tr.placeErr["lx-HOMEUSDT-1-os"] = errors.New(`bybit: retCode=170207 retMsg="The loan amount of the platform is not enough"`)
	pos, err := e.OpenDeltaNeutral(context.Background(), plan, "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("负 carry 现货借币卖出失败时应返回错误")
	}
	if pos.BaseQty != 0 {
		t.Fatalf("借币失败不应留下任何持仓记录, got %+v", pos)
	}
	for _, o := range tr.placed {
		if o.Market == venue.MarketPerp {
			t.Fatalf("借币腿失败后绝不应碰永续, placed=%+v", tr.placed)
		}
	}
}

// TestOpenDeltaNeutralNegativePerpFailRevertsSpotBorrow 钉死:现货借币卖出成功但永续多腿失败时,
// 立即买回现货还币(BUY + MarginEffect=Repay)回到零敞口,绝不裸现货空腿。
func TestOpenDeltaNeutralNegativePerpFailRevertsSpotBorrow(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	plan := samplePlan()
	plan.Negative = true
	tr.placeErr["lx-HOMEUSDT-1-op"] = errors.New("perp rejected")
	pos, err := e.OpenDeltaNeutral(context.Background(), plan, "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("永续腿失败时开仓应返回错误")
	}
	if pos.BaseQty != 0 {
		t.Fatalf("回补成功后不应留下持仓记录, got %+v", pos)
	}
	var foundRevert bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot && o.Side == venue.SideBuy && o.MarginEffect == venue.MarginEffectRepay && o.Qty == 10 {
			foundRevert = true
		}
	}
	if !foundRevert {
		t.Fatalf("永续失败后应买回现货还币(BUY+Repay qty=10), placed=%+v", tr.placed)
	}
}

// TestOpenDeltaNeutralNegativePerpFailRevertFailReturnsNakedSpot 钉死:永续失败且现货回补也失败时,
// 必须返回带现货敞口的持仓(SpotEntryPrice>0)+ 错误,供调用方落盘待平,绝不失管。
func TestOpenDeltaNeutralNegativePerpFailRevertFailReturnsNakedSpot(t *testing.T) {
	tr := newFakeBybitTrader()
	e := newTestExecutor(tr)
	plan := samplePlan()
	plan.Negative = true
	tr.placeErr["lx-HOMEUSDT-1-op"] = errors.New("perp rejected")
	tr.placeErr["lx-HOMEUSDT-1-rs"] = errors.New("repay buy rejected")
	pos, err := e.OpenDeltaNeutral(context.Background(), plan, "lx-HOMEUSDT-1", "run-1")
	if err == nil {
		t.Fatal("回补失败应返回错误(裸现货空腿待平)")
	}
	if pos.BaseQty != 10 || pos.SpotEntryPrice <= 0 || !pos.Negative {
		t.Fatalf("应返回带现货敞口的待平持仓, got %+v", pos)
	}
}

func TestCloseDeltaNeutralNegativeShortsPerpAndRepaysSpotBuy(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: 10}}
	e := newTestExecutor(tr)
	// 真实负 carry 持仓:现货借币空头已建立(SpotEntryPrice>0);SpotEntryPrice=0 专表「裸永续」走跳过分支(见另一测试)。
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-HOMEUSDT-1", Negative: true, SpotEntryPrice: 100}
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
		t.Fatalf("负 carry 平仓应成功: %v", err)
	}
	var perpClose, spotRepay bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketPerp && o.Side == venue.SideSell && o.ReduceOnly {
			perpClose = true
		}
		if o.Market == venue.MarketSpot && o.Side == venue.SideBuy && o.MarginEffect == venue.MarginEffectRepay && o.Qty == 10 {
			spotRepay = true
		}
	}
	if !perpClose {
		t.Fatalf("负 carry 平仓永续腿应为卖出 reduceOnly, placed=%+v", tr.placed)
	}
	if !spotRepay {
		t.Fatalf("负 carry 平仓现货腿应为买回还币(BUY+Repay, qty=借入量 10), placed=%+v", tr.placed)
	}
}

// TestCloseDeltaNeutralSelfHealsPhantomPosition 钉死 2026-06-10 MBOX-USDT 幻影持仓死循环:
// 台账有仓但交易所「永续仓位为零 + 现货余额条目缺失(=0)」时,平仓必须自愈为成功(两腿都视为已平),
// 不下任何单、不再返回错误,让上层得以删除台账记录,绝不无限重试。
func TestCloseDeltaNeutralSelfHealsPhantomPosition(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.positions = nil                                                // 永续无仓
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{}} // Bybit UTA 不返回零余额币种条目
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "MBOX-USDT", BaseQty: 5454, SpotEntryPrice: 0.0055, PerpEntryPrice: 0.0055, IdemPrefix: "lx-MBOXUSDT-1781071200"}
	res, err := e.CloseDeltaNeutral(context.Background(), pos)
	if err != nil {
		t.Fatalf("幻影持仓平仓应自愈为成功: %v", err)
	}
	if len(tr.placed) != 0 {
		t.Fatalf("两腿均已为零时不应下任何单, placed=%+v", tr.placed)
	}
	if res.PricePnL != 0 || res.SpotPricePnL != 0 || res.PerpPricePnL != 0 {
		t.Fatalf("无实际成交时价差盈亏应为 0, got %+v", res)
	}
}

// TestCloseDeltaNeutralPerpZeroErrorTreatedAsFlat 钉死:仓位查询与下单之间被外力平掉,
// reduceOnly 撞 110017 "position is zero" 时,该腿视为已平而非失败。
func TestCloseDeltaNeutralPerpZeroErrorTreatedAsFlat(t *testing.T) {
	tr := newFakeBybitTrader()
	tr.positions = []venue.Position{{Symbol: "HOME-USDT", PositionAmt: -10}} // 查询时还在
	tr.balances = venue.Balances{ByAsset: map[string]venue.Balance{"HOME": {Free: 10}}}
	tr.placeErr["lx-HOMEUSDT-1-cp"] = errors.New("bybit: retCode=110017 retMsg=current position is zero, cannot fix reduce-only order qty")
	e := newTestExecutor(tr)
	pos := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, SpotEntryPrice: 100, PerpEntryPrice: 100, IdemPrefix: "lx-HOMEUSDT-1"}
	if _, err := e.CloseDeltaNeutral(context.Background(), pos); err != nil {
		t.Fatalf("永续 110017 应视为已平,整体平仓应成功: %v", err)
	}
	var spotClose bool
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot && o.Side == venue.SideSell {
			spotClose = true
		}
	}
	if !spotClose {
		t.Fatalf("现货腿仍应正常平掉, placed=%+v", tr.placed)
	}
}

// TestPlaceAndConfirmRejectsStaleDuplicateFill 钉死幻影成交根因:同一 symbol+结算窗口平掉后再开,
// 幂等 ID 撞上历史旧单时,绝不能把旧单的成交回执当成本次成交(否则台账有仓、交易所没仓)。
func TestPlaceAndConfirmRejectsStaleDuplicateFill(t *testing.T) {
	tr := newFakeBybitTrader()
	cid := "lx-MBOXUSDT-1781071200-p"
	tr.placeErr[cid] = errors.New("bybit: retCode=170141 retMsg=Duplicate clientOrderId.")
	tr.fills[cid] = venue.OrderResult{
		ClientOrderID: cid, Status: "filled", ExecutedQty: 5454, AvgPrice: 0.0055,
		CreatedAt: time.Now().UTC().Add(-22 * time.Minute), // 22 分钟前的旧单
	}
	e := newTestExecutor(tr)
	req := venue.OrderRequest{Market: venue.MarketPerp, Symbol: "MBOX-USDT", Side: venue.SideSell, Type: venue.OrderTypeMarket, Qty: 5454, ClientOrderID: cid}
	if _, err := e.placeAndConfirm(context.Background(), req); err == nil {
		t.Fatal("撞历史旧单时应拒绝按旧成交确认并返回错误")
	}
}

// TestPlaceAndConfirmAcceptsFreshDuplicateFill 钉死:崩溃后秒级幂等重试(撞到的是本次尝试刚下出去的单)仍应正常确认。
func TestPlaceAndConfirmAcceptsFreshDuplicateFill(t *testing.T) {
	tr := newFakeBybitTrader()
	cid := "lx-HOMEUSDT-1-p"
	tr.placeErr[cid] = errors.New("bybit: retCode=10005 orderLinkId duplicated")
	tr.fills[cid] = venue.OrderResult{
		ClientOrderID: cid, Status: "filled", ExecutedQty: 10, AvgPrice: 100,
		CreatedAt: time.Now().UTC().Add(-5 * time.Second), // 刚创建的单(合法幂等重试)
	}
	e := newTestExecutor(tr)
	req := venue.OrderRequest{Market: venue.MarketPerp, Symbol: "HOME-USDT", Side: venue.SideSell, Type: venue.OrderTypeMarket, Qty: 10, ClientOrderID: cid}
	res, err := e.placeAndConfirm(context.Background(), req)
	if err != nil {
		t.Fatalf("新鲜重复单应视为已受理并确认成交: %v", err)
	}
	if res.ExecutedQty != 10 {
		t.Fatalf("应返回成交回执, got %+v", res)
	}
}

// TestOpenDeltaNeutralStaleDuplicateFailsCleanly 钉死端到端:开仓永续腿撞历史旧单时,
// 开仓应干净失败——不下现货腿、不落幻影/裸腿持仓记录。
func TestOpenDeltaNeutralStaleDuplicateFailsCleanly(t *testing.T) {
	tr := newFakeBybitTrader()
	cid := "lx-MBOXUSDT-1781071200-op"
	tr.placeErr[cid] = errors.New("bybit: retCode=10005 orderLinkId duplicated")
	tr.fills[cid] = venue.OrderResult{
		ClientOrderID: cid, Status: "filled", ExecutedQty: 5454, AvgPrice: 0.0055,
		CreatedAt: time.Now().UTC().Add(-22 * time.Minute),
	}
	e := newTestExecutor(tr)
	plan := samplePlan()
	plan.Symbol = "MBOX-USDT"
	pos, err := e.OpenDeltaNeutral(context.Background(), plan, "lx-MBOXUSDT-1781071200", "run-1")
	if err == nil {
		t.Fatal("撞历史旧单的开仓应失败")
	}
	if pos.BaseQty != 0 {
		t.Fatalf("不应返回任何持仓记录(幻影/裸腿), got %+v", pos)
	}
	for _, o := range tr.placed {
		if o.Market == venue.MarketSpot {
			t.Fatalf("永续腿撞旧单后不应再下现货腿, placed=%+v", tr.placed)
		}
	}
}

func TestBybitLegClientIDTruncates(t *testing.T) {
	long := "lx-VERYLONGSYMBOLNAMEUSDT-1700000000000000"
	id := bybitLegClientID(long, "op")
	if len(id) > 36 {
		t.Fatalf("clientOrderID 应 ≤36 字符, got %d (%s)", len(id), id)
	}
}

// fakeMakerTrader 在 fakeBybitTrader 之上加盘口能力,触发 maker 优先路径。
type fakeMakerTrader struct {
	*fakeBybitTrader
	bid, ask float64
}

func (f *fakeMakerTrader) FetchBookTicker(_ context.Context, _ venue.Market, _ venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{BidPrice: f.bid, AskPrice: f.ask}, nil
}

// TestPlacePerpMakerFirstFillsAsMaker 钉死 maker 优先:盘口可得时第一轮 post-only 贴最优被动价
// (SELL→ask),成交后不再下 taker 单。
func TestPlacePerpMakerFirstFillsAsMaker(t *testing.T) {
	tr := &fakeMakerTrader{fakeBybitTrader: newFakeBybitTrader(), bid: 99.5, ask: 100.5}
	e := newTestExecutor(tr)
	e.SetPerpMakerFirst(true)
	e.makerWait = 20 * time.Millisecond
	req := venue.OrderRequest{Market: venue.MarketPerp, Symbol: "X-USDT", Side: venue.SideSell, Type: venue.OrderTypeMarket, Qty: 10, ClientOrderID: "lx-X-op"}
	res, err := e.placePerpMakerFirst(context.Background(), req, e.now().Add(time.Hour))
	if err != nil {
		t.Fatalf("maker 路径应成功: %v", err)
	}
	if len(tr.placed) != 1 {
		t.Fatalf("全量 maker 成交不应再下 taker 单, placed=%d", len(tr.placed))
	}
	if tr.placed[0].Type != venue.OrderTypePostOnly || tr.placed[0].Price != 100.5 {
		t.Fatalf("应为 post-only 贴最优卖价 100.5: %+v", tr.placed[0])
	}
	if res.ExecutedQty != 10 {
		t.Fatalf("成交数量应为 10, got %v", res.ExecutedQty)
	}
}

// TestPlacePerpMakerFirstFallsBackToTaker 钉死兜底:maker 始终不成交时撤单并按原幂等 ID 市价 taker,
// 合并结果(此处 maker 部分成交 4 + taker 6,均价按数量加权)。
func TestPlacePerpMakerFirstFallsBackToTaker(t *testing.T) {
	tr := &fakeMakerTrader{fakeBybitTrader: newFakeBybitTrader(), bid: 99.5, ask: 100.5}
	e := newTestExecutor(tr)
	e.SetPerpMakerFirst(true)
	e.makerWait = 10 * time.Millisecond
	// 第一轮 maker 部分成交 4 后不再动 → 撤单收部分成交;第二轮挂单失败 → 剩余 6 走 taker 兜底。
	m1 := bybitLegClientID("lx-X-op", "m1")
	tr.fills[m1] = venue.OrderResult{ClientOrderID: m1, Status: "partially_filled", ExecutedQty: 4, AvgPrice: 99, CumQuoteUSDT: 396}
	tr.placeErr[bybitLegClientID("lx-X-op", "m2")] = errors.New("post-only would cross")
	deadline := e.now().Add(time.Hour)
	req := venue.OrderRequest{Market: venue.MarketPerp, Symbol: "X-USDT", Side: venue.SideSell, Type: venue.OrderTypeMarket, Qty: 10, ClientOrderID: "lx-X-op"}
	res, err := e.placePerpMakerFirst(context.Background(), req, deadline)
	if err != nil {
		t.Fatalf("兜底路径应成功: %v", err)
	}
	if len(tr.canceled) == 0 || tr.canceled[0] != m1 {
		t.Fatalf("未成交的 maker 单应被撤: %v", tr.canceled)
	}
	last := tr.placed[len(tr.placed)-1]
	if last.ClientOrderID != "lx-X-op" || last.Type != venue.OrderTypeMarket {
		t.Fatalf("兜底应用原幂等 ID 市价单: %+v", last)
	}
	if last.Qty < 5.9 || last.Qty > 6.1 {
		t.Fatalf("taker 兜底数量应为剩余 6, got %v", last.Qty)
	}
	if res.ExecutedQty != 10 {
		t.Fatalf("合并后成交应为 10, got %v", res.ExecutedQty)
	}
	wantAvg := (4*99.0 + 6*100.0) / 10
	if res.AvgPrice < wantAvg-0.01 || res.AvgPrice > wantAvg+0.01 {
		t.Fatalf("合并均价应≈%.2f, got %v", wantAvg, res.AvgPrice)
	}
}

// TestBybitSpotFeeUSDT 钉死现货手续费折 USDT 口径:Bybit 现货 BUY 费在 base 里扣(feeCurrency 常为空),
// 必须按成交价折算;SELL 费在 quote(USDT)里扣原样返回。回归 MBOX 实例:6.266 MBOX@0.00479 ≈ 0.030U,
// 修复前会被当成 6.266 USDT 入账(盈亏失真 200 倍)。
func TestBybitSpotFeeUSDT(t *testing.T) {
	const sym = "MBOX-USDT"
	cases := []struct {
		name     string
		fee      float64
		feeAsset string
		isBuy    bool
		price    float64
		want     float64
	}{
		{"feeAsset 空+BUY → 按 base 折算", 6.266, "", true, 0.00479, 6.266 * 0.00479},
		{"feeAsset=base → 折算", 6.266, "MBOX", false, 0.00479, 6.266 * 0.00479},
		{"feeAsset 空+SELL → 视为 USDT 原样", 0.03, "", false, 0.00479, 0.03},
		{"feeAsset=USDT → 原样", 0.03, "USDT", true, 0.00479, 0.03},
		{"base 计价但价格缺失 → 原样(不虚构)", 6.266, "MBOX", true, 0, 6.266},
		{"零费 → 0", 0, "", true, 0.00479, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bybitSpotFeeUSDT(tc.fee, tc.feeAsset, sym, tc.isBuy, tc.price)
			if got < tc.want-1e-12 || got > tc.want+1e-12 {
				t.Fatalf("want %.10f got %.10f", tc.want, got)
			}
		})
	}
}

// TestIsBybitBorrowUnavailableError 钉死 170207(借币池借不到)识别:命中后控制器拉黑该标的,
// 同结算窗口不再重试(2026-06-12 HOME 实锤:不识别 → 同窗重试 3 次 + 下一小时原样重演)。
func TestIsBybitBorrowUnavailableError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`bybit: retCode=170207 retMsg="The loan amount of the platform is not enough"`), true},
		{errors.New(`现货借币卖出腿失败(未建仓,未动永续): bybit: retCode=170207 retMsg="The loan amount of the platform is not enough"`), true},
		{errors.New("The Loan Amount Of The Platform is not enough"), true},
		{errors.New("bybit: retCode=170131 Insufficient balance"), false},
	}
	for i, tc := range cases {
		if got := isBybitBorrowUnavailableError(tc.err); got != tc.want {
			t.Fatalf("case %d: want %v got %v", i, tc.want, got)
		}
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
