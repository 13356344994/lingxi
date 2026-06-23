package app

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// contractLegFakeVenue 模拟张本位永续:ExecutedQty 按 normalizeVenueQty 与真实 okx/gate 下单口径一致。
type contractLegFakeVenue struct {
	crossVenueTestVenue
	// reportContractsInExecutedQty 模拟 gate/okx QueryOrder 在 lookupInstrument mult=1 时把张数写入 ExecutedQty。
	reportContractsInExecutedQty bool
}

func newContractLegFakeVenue(name string, instr venue.Instrument) *contractLegFakeVenue {
	v := newCrossVenueTestVenue(name, 0.0001, 8, vNow())
	v.contractBased = true
	v.instruments = []venue.Instrument{instr}
	return &contractLegFakeVenue{crossVenueTestVenue: *v}
}

func newContractLegFakeVenueReportingContracts(name string, instr venue.Instrument) *contractLegFakeVenue {
	v := newContractLegFakeVenue(name, instr)
	v.reportContractsInExecutedQty = true
	return v
}

func vNow() time.Time {
	return time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
}

func (f *contractLegFakeVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	instr := f.instruments[0]
	execCoin := normalizeVenueQty(req.Qty, instr, true)
	if execCoin <= executionQtyTolerance {
		return venue.OrderResult{Status: "rejected", ClientOrderID: req.ClientOrderID},
			errors.New("simulated zero contract qty")
	}
	execQty := execCoin
	if f.reportContractsInExecutedQty && instr.ContractSize > 0 {
		execQty = execCoin / instr.ContractSize
	}
	if req.ReduceOnly && len(f.positions) > 0 {
		cur := f.positions[0].PositionAmt
		if cur < 0 && req.Side == venue.SideBuy {
			f.positions[0].PositionAmt = math.Min(cur+execCoin, 0)
			f.positions[0].MarkPrice = req.Price
			return venue.OrderResult{
				Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
				Status: "filled", ExecutedQty: execQty, AvgPrice: req.Price, CumQuoteUSDT: execCoin * req.Price,
			}, nil
		}
		if cur > 0 && req.Side == venue.SideSell {
			f.positions[0].PositionAmt = math.Max(cur-execCoin, 0)
			f.positions[0].MarkPrice = req.Price
			return venue.OrderResult{
				Symbol: req.Symbol, Market: req.Market, ClientOrderID: req.ClientOrderID,
				Status: "filled", ExecutedQty: execQty, AvgPrice: req.Price, CumQuoteUSDT: execCoin * req.Price,
			}, nil
		}
	}
	amt := execCoin
	if req.Side == venue.SideSell {
		amt = -execCoin
	}
	f.positions = []venue.Position{{Symbol: req.Symbol, PositionAmt: amt, MarkPrice: req.Price, Leverage: 2}}
	return venue.OrderResult{
		Symbol:        req.Symbol,
		Market:        req.Market,
		ClientOrderID: req.ClientOrderID,
		Status:        "filled",
		ExecutedQty:   execQty,
		AvgPrice:      req.Price,
		CumQuoteUSDT:  execCoin * req.Price,
	}, nil
}

func TestNormalizeVenueQtyMatchesOKXContractStep(t *testing.T) {
	t.Parallel()
	instr := venue.Instrument{ContractSize: 10, StepSize: 2} // ctVal=10 lotSz=2
	cases := []struct {
		qty  float64
		want float64
	}{
		{30, 20}, // 修复前错误地得到 30,与 okx 实际 20 张(2×lot)不符
		{70, 60},
		{50, 40},
		{20, 20},
	}
	for _, tc := range cases {
		got := normalizeVenueQty(tc.qty, instr, true)
		if got != tc.want {
			t.Fatalf("qty=%v got=%v want=%v", tc.qty, got, tc.want)
		}
	}
}

func TestAlignCrossVenueQtyCoinBybitContractOKX(t *testing.T) {
	t.Parallel()
	bybitInstr := venue.Instrument{StepSize: 1, ContractSize: 1}
	okxInstr := venue.Instrument{StepSize: 2, ContractSize: 10}
	qty := alignCrossVenueQty(
		30, 1,
		bybitInstr, okxInstr,
		venue.Capabilities{ContractBasedPerp: false},
		venue.Capabilities{ContractBasedPerp: true},
	)
	if qty != 20 {
		t.Fatalf("alignCrossVenueQty=%v want 20 (30 币在 okx lot=2 只能下 20)", qty)
	}
}

func TestCrossVenueOpenEqualCoinQtyMixedLegs(t *testing.T) {
	now := vNow()
	bybitVenue := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	bybitVenue.instruments = []venue.Instrument{{
		Symbol: "H-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1,
	}}
	okxInstr := venue.Instrument{
		Symbol: "H-USDT", Market: venue.MarketPerp, StepSize: 2, ContractSize: 10, QtyUnit: "contract",
	}
	okxVenue := newContractLegFakeVenue(okx.VenueName, okxInstr)

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: bybitVenue, okx.VenueName: okxVenue},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	// 名义 30U、单价 1 → raw 30 币;align 应收敛到 okx 可下的 20 币,两腿一致。
	bybitInstr := bybitVenue.instruments[0]
	qty := alignCrossVenueQty(30, 1, bybitInstr, okxInstr,
		venue.Capabilities{ContractBasedPerp: false},
		venue.Capabilities{ContractBasedPerp: true},
	)
	if qty != 20 {
		t.Fatalf("alignCrossVenueQty=%v want 20", qty)
	}
	plan := crossVenueOpenPlan{
		Opp: CrossVenueOpportunity{Symbol: "H-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate: crossVenueGateResult{
			NotionalUSDT: 30,
			HighInstr:    bybitInstr,
			LowInstr:     okxInstr,
		},
		Qty:        qty,
		RefPrice:   1,
		IdemPrefix: "qty-parity",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if dev := qtyDeviation(pos.HighLeg.Qty, pos.LowLeg.Qty); dev > 1e-9 {
		t.Fatalf("两腿币数量偏差=%v high=%v low=%v", dev, pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
	if pos.HighLeg.Qty != 20 || pos.LowLeg.Qty != 20 {
		t.Fatalf("want both legs 20 coin, got high=%v low=%v", pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
}

func TestAlignCrossVenueQtyBinanceCoinContractOKX(t *testing.T) {
	t.Parallel()
	binanceInstr := venue.Instrument{StepSize: 1, ContractSize: 1}
	okxInstr := venue.Instrument{StepSize: 2, ContractSize: 10}
	qty := alignCrossVenueQty(
		30, 1,
		binanceInstr, okxInstr,
		venue.Capabilities{ContractBasedPerp: false},
		venue.Capabilities{ContractBasedPerp: true},
	)
	if qty != 20 {
		t.Fatalf("alignCrossVenueQty=%v want 20 (30 币在 okx lot=2 只能下 20)", qty)
	}
}

func TestCrossVenueOpenEqualCoinQtyBinanceOkxLegs(t *testing.T) {
	now := vNow()
	binanceVenue := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, now)
	binanceVenue.contractBased = false
	binanceVenue.instruments = []venue.Instrument{{
		Symbol: "H-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1,
	}}
	okxInstr := venue.Instrument{
		Symbol: "H-USDT", Market: venue.MarketPerp, StepSize: 2, ContractSize: 10, QtyUnit: "contract",
	}
	okxVenue := newContractLegFakeVenue(okx.VenueName, okxInstr)

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{binance.VenueName: binanceVenue, okx.VenueName: okxVenue},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	binanceInstr := binanceVenue.instruments[0]
	qty := alignCrossVenueQty(30, 1, binanceInstr, okxInstr,
		venue.Capabilities{ContractBasedPerp: false},
		venue.Capabilities{ContractBasedPerp: true},
	)
	if qty != 20 {
		t.Fatalf("alignCrossVenueQty=%v want 20", qty)
	}
	plan := crossVenueOpenPlan{
		Opp: CrossVenueOpportunity{Symbol: "H-USDT", HighVenue: binance.VenueName, LowVenue: okx.VenueName, SpreadAPR: 2.0},
		Gate: crossVenueGateResult{
			NotionalUSDT: 30,
			HighInstr:    binanceInstr,
			LowInstr:     okxInstr,
		},
		Qty:        qty,
		RefPrice:   1,
		IdemPrefix: "qty-binance-okx",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if dev := qtyDeviation(pos.HighLeg.Qty, pos.LowLeg.Qty); dev > 1e-9 {
		t.Fatalf("两腿币数量偏差=%v high=%v low=%v", dev, pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
	if pos.HighLeg.Qty != 20 || pos.LowLeg.Qty != 20 {
		t.Fatalf("want both legs 20 coin, got high=%v low=%v", pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
}

func TestAlignCrossVenueQtyMatchesGateOrderCoinQty(t *testing.T) {
	t.Parallel()
	gateInstr := venue.Instrument{StepSize: 5, ContractSize: 10}
	got := normalizeVenueQty(70, gateInstr, true)
	want := 50.0
	if got != want {
		t.Fatalf("normalizeVenueQty gate path=%v want %v", got, want)
	}
	// 与 gate qtyToSignedContractSize 往返一致
	n, err := gateQtyToCoin(70, gateInstr)
	if err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("gate order coin qty=%v want %v", n, want)
	}
}

// gateQtyToCoin 复制 gate 适配器下单口径(测试用,避免 internal 包循环依赖)。
func gateQtyToCoin(qty float64, instr venue.Instrument) (float64, error) {
	if instr.ContractSize <= 0 {
		return 0, nil
	}
	contracts := qty / instr.ContractSize
	if instr.StepSize > 0 {
		contracts = math.Floor(contracts/instr.StepSize+1e-12) * instr.StepSize
	} else {
		contracts = math.Floor(contracts + 1e-12)
	}
	if contracts <= 0 {
		return 0, errors.New("zero contracts")
	}
	return contracts * instr.ContractSize, nil
}

func TestReconcileCrossVenueOpenBookQtyUsesMinWhenDeviationHigh(t *testing.T) {
	t.Parallel()
	got := reconcileCrossVenueOpenBookQty(venue.Capabilities{}, venue.Capabilities{ContractBasedPerp: true}, 1300, 400)
	if got != 400 {
		t.Fatalf("got=%v want min=400", got)
	}
}

func TestCarryAlignSpotPerpOpenQtyBinanceSpotOkxPerp(t *testing.T) {
	t.Parallel()
	got := carryAlignSpotPerpOpenQty(30, 1, venue.Instrument{StepSize: 1}, venue.Instrument{StepSize: 2, ContractSize: 10}, venue.Capabilities{}, venue.Capabilities{ContractBasedPerp: true})
	if got != 20 {
		t.Fatalf("got=%v want 20", got)
	}
}

func TestCrossVenueLegExecutedCoinQtyContractReportsContracts(t *testing.T) {
	t.Parallel()
	caps := venue.Capabilities{ContractBasedPerp: true}
	instr := venue.Instrument{StepSize: 100, ContractSize: 3.25}
	res := venue.OrderResult{ExecutedQty: 400, AvgPrice: 0.0307}
	got := crossVenueLegExecutedCoinQty(caps, instr, res, 1300)
	if got != 1300 {
		t.Fatalf("got=%v want 1300 coin (400 contracts × 3.25)", got)
	}
}

func TestCrossVenueLegExecutedCoinQtyAlreadyCoin(t *testing.T) {
	t.Parallel()
	caps := venue.Capabilities{ContractBasedPerp: true}
	instr := venue.Instrument{StepSize: 100, ContractSize: 3.25}
	res := venue.OrderResult{ExecutedQty: 1300, AvgPrice: 0.0307}
	got := crossVenueLegExecutedCoinQty(caps, instr, res, 1300)
	if got != 1300 {
		t.Fatalf("got=%v want 1300 coin", got)
	}
}

func TestCrossVenueOpenBookQtyBybitHighGateLowReportsContracts(t *testing.T) {
	// HOME-USDT 口径:bybit 币本位 1300, gate 适配器 ExecutedQty 误报 400 张(应为 1300 币)。
	now := vNow()
	bybitVenue := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	bybitVenue.instruments = []venue.Instrument{{
		Symbol: "HOME-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1,
	}}
	gateInstr := venue.Instrument{
		Symbol: "HOME-USDT", Market: venue.MarketPerp, StepSize: 100, ContractSize: 3.25, QtyUnit: "contract",
	}
	gateVenue := newContractLegFakeVenueReportingContracts(gate.VenueName, gateInstr)

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: bybitVenue, gate.VenueName: gateVenue},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	refPrice := 0.0307
	qty := float64(1300)
	plan := crossVenueOpenPlan{
		Opp: CrossVenueOpportunity{Symbol: "HOME-USDT", HighVenue: bybit.VenueName, LowVenue: gate.VenueName, SpreadAPR: 2.0},
		Gate: crossVenueGateResult{
			NotionalUSDT: 40,
			HighInstr:    bybitVenue.instruments[0],
			LowInstr:     gateInstr,
		},
		Qty:        qty,
		RefPrice:   refPrice,
		IdemPrefix: "home-book",
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), plan, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if pos.HighLeg.Qty != 1300 || pos.LowLeg.Qty != 1300 {
		t.Fatalf("want both legs 1300 coin, got high=%v low=%v", pos.HighLeg.Qty, pos.LowLeg.Qty)
	}
	wantNotional := 1300 * refPrice
	if math.Abs(pos.NotionalUSDT-wantNotional) > 1e-6 {
		t.Fatalf("notional=%v want %v", pos.NotionalUSDT, wantNotional)
	}
}

func TestCrossVenueOpenVerifyBooksExchangeTruth(t *testing.T) {
	now := vNow()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir),
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusOpeningSecondLeg, IdemPrefix: "verify-book",
	}
	plan := e.crossVenueOpenPlanFromPosition(context.Background(), pos)
	highQty, lowQty, imbalance, err := e.verifyAndFinalizeCrossVenueOpen(context.Background(), &pos, plan, true)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if highQty != 0.5 || lowQty != 0.5 || imbalance > executionQtyTolerance {
		t.Fatalf("want 0.5/0.5 balanced, got high=%v low=%v imbalance=%v", highQty, lowQty, imbalance)
	}
}
