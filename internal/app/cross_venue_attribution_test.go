package app

import (
	"context"
	"math"
	"testing"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/gate"
)

// TestRecordCrossVenueExecution_GateCloseContractsToCoin 模拟 HEI 类 bug:gate 平仓 ExecutedQty 报张数,
// 修复后 ClosedQty 应归一化为币数量,trade_pnl 不再爆炸。
func TestRecordCrossVenueExecution_GateCloseContractsToCoin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gateInstr := venue.Instrument{
		Symbol: "HEI-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 0.058823529, QtyUnit: "contract",
	}
	gateVenue := newContractLegFakeVenueReportingContracts(gate.VenueName, gateInstr)
	binanceVenue := newCrossVenueTestVenue(binance.VenueName, 0.001, 8, vNow())
	binanceVenue.instruments = []venue.Instrument{{
		Symbol: "HEI-USDT", Market: venue.MarketPerp, StepSize: 1, ContractSize: 1,
	}}

	e := &CrossVenueExecutor{
		venues: map[string]venue.Venue{gate.VenueName: gateVenue, binance.VenueName: binanceVenue},
		logger: testCrossVenueLogger(),
	}

	entryPrice := 0.46
	exitPrice := 0.48
	bookQty := 100.0
	// gate 适配器误报 1700 张(≈100 币),修复前会把 1700 当币数 → trade_pnl ≈ -801。
	contracts := bookQty / gateInstr.ContractSize
	pos := CrossVenuePosition{
		Symbol:    "HEI-USDT",
		HighVenue: gate.VenueName,
		LowVenue:  binance.VenueName,
		HighLeg: CrossVenueLegState{
			Venue: gate.VenueName, Qty: bookQty, EntryPrice: entryPrice,
		},
		LowLeg: CrossVenueLegState{
			Venue: binance.VenueName, Qty: bookQty, EntryPrice: entryPrice,
		},
		NotionalUSDT: bookQty * entryPrice,
	}
	res := venue.OrderResult{
		ExecutedQty: contracts,
		AvgPrice:    exitPrice,
	}

	coinQty := e.recordCrossVenueExecution(ctx, &pos, cvExecCloseHigh, res, exitPrice)
	if coinQty <= executionQtyTolerance {
		t.Fatalf("coinQty=%v want ~%v", coinQty, bookQty)
	}
	if dev := qtyDeviation(coinQty, bookQty); dev > 0.02 {
		t.Fatalf("normalized close qty=%v want ~%v coin (dev=%v)", coinQty, bookQty, dev)
	}
	if pos.HighLeg.ClosedQty <= executionQtyTolerance || qtyDeviation(pos.HighLeg.ClosedQty, bookQty) > 0.02 {
		t.Fatalf("ClosedQty=%v want ~%v coin", pos.HighLeg.ClosedQty, bookQty)
	}
	if pos.AttributionIncomplete {
		t.Fatal("attribution should be complete when ContractSize lookup succeeds")
	}

	tradePnL := computeCrossVenueTradePnL(pos)
	// 空 gate:(entry-exit)*qty ≈ (0.46-0.48)*100 = -2
	if math.Abs(tradePnL-(-2.0)) > 0.5 {
		t.Fatalf("trade_pnl=%v want ~-2 (幻觉修复前会 ≈-801)", tradePnL)
	}
	if math.Abs(tradePnL) > 50 {
		t.Fatalf("|trade_pnl|=%v exceeds anomaly threshold 50", math.Abs(tradePnL))
	}
}

func TestRecordCrossVenueExecution_GateCloseLookupFailMarksIncomplete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// 无 instruments → lookup 失败,禁止把张数当币数。
	gateVenue := newCrossVenueTestVenue(gate.VenueName, 0.001, 8, vNow())
	gateVenue.contractBased = true
	gateVenue.instruments = nil

	e := &CrossVenueExecutor{
		venues: map[string]venue.Venue{gate.VenueName: gateVenue},
		logger: testCrossVenueLogger(),
	}
	pos := CrossVenuePosition{
		Symbol:    "HEI-USDT",
		HighVenue: gate.VenueName,
		HighLeg:   CrossVenueLegState{Qty: 100, EntryPrice: 0.46},
		LowLeg:    CrossVenueLegState{Qty: 100, EntryPrice: 0.46},
	}
	res := venue.OrderResult{ExecutedQty: 1700, AvgPrice: 0.48}

	coinQty := e.recordCrossVenueExecution(ctx, &pos, cvExecCloseHigh, res, 0.48)
	if coinQty != 0 {
		t.Fatalf("coinQty=%v want 0 on lookup failure", coinQty)
	}
	if !pos.AttributionIncomplete {
		t.Fatal("want attribution_incomplete when ContractSize lookup fails")
	}
	if pos.HighLeg.ClosedQty > executionQtyTolerance {
		t.Fatalf("ClosedQty=%v want 0 when lookup fails", pos.HighLeg.ClosedQty)
	}
}
