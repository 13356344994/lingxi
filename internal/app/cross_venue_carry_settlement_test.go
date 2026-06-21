package app

import (
	"math"
	"testing"
	"time"
)

func TestBuildCrossVenueSpotPerpSettlement_NetPnLDoesNotDoubleCountSlippage(t *testing.T) {
	pos := CrossVenueSpotPerpPosition{
		Symbol:             "BTC-USDT",
		SpotVenue:          "binance",
		PerpVenue:          "bybit",
		NotionalUSDT:       100,
		PerpLegFundingUSDT: 0.4,
		OpenFeesUSDT:       0.04,
		CloseFeesUSDT:      0.04,
		SpotLeg: CrossVenueSpotPerpLegState{
			EntryPrice: 100,
			ExitPrice:  100.5,
			ClosedQty:  1,
		},
		PerpLeg: CrossVenueSpotPerpLegState{
			EntryPrice: 100,
			ExitPrice:  99.5,
			ClosedQty:  1,
		},
	}
	rec := buildCrossVenueSpotPerpSettlement(pos, time.Now())

	wantTrade := 1.0 // spot +0.5, perp short +0.5
	if math.Abs(rec.TradePnL-wantTrade) > 1e-9 {
		t.Fatalf("trade_pnl=%v want %v", rec.TradePnL, wantTrade)
	}
	wantNet := 1.32 // 0.4 funding + 1.0 trade - 0.08 fees
	if math.Abs(rec.NetPnL-wantNet) > 1e-9 {
		t.Fatalf("net_pnl=%v want %v (must include trade_pnl, not subtract slippage again)", rec.NetPnL, wantNet)
	}
}

func TestBuildCrossVenuePerpSpotSettlement_NetPnLDoesNotDoubleCountSlippage(t *testing.T) {
	pos := CrossVenuePerpSpotPosition{
		Symbol:             "ETH-USDT",
		SpotVenue:          "okx",
		PerpVenue:          "gate",
		NotionalUSDT:       200,
		PerpLegFundingUSDT: 0.3,
		OpenFeesUSDT:       0.05,
		CloseFeesUSDT:      0.05,
		BorrowCostUSDT:     0.02,
		SpotLeg: CrossVenuePerpSpotLegState{
			EntryPrice: 50,
			ExitPrice:  49.5,
			ClosedQty:  2,
		},
		PerpLeg: CrossVenuePerpSpotLegState{
			EntryPrice: 50,
			ExitPrice:  50.5,
			ClosedQty:  2,
		},
	}
	rec := buildCrossVenuePerpSpotSettlement(pos, time.Now())

	wantTrade := 2.0 // spot short +1.0, perp long +1.0
	if math.Abs(rec.TradePnL-wantTrade) > 1e-9 {
		t.Fatalf("trade_pnl=%v want %v", rec.TradePnL, wantTrade)
	}
	wantNet := 2.18 // 0.3 + 2.0 - 0.10 fees - 0.02 borrow
	if math.Abs(rec.NetPnL-wantNet) > 1e-9 {
		t.Fatalf("net_pnl=%v want %v", rec.NetPnL, wantNet)
	}
}

func TestBuildCrossVenueSpotPerpSettlement_BooksDustResidualCost(t *testing.T) {
	pos := CrossVenueSpotPerpPosition{
		Symbol:               "SOL-USDT",
		PerpLegFundingUSDT:   1.0,
		DustResidualCostUSDT: 0.45,
	}
	rec := buildCrossVenueSpotPerpSettlement(pos, time.Now())
	if math.Abs(rec.DustResidualCost-0.45) > 1e-9 {
		t.Fatalf("dust_residual_cost=%v want 0.45", rec.DustResidualCost)
	}
	wantNet := 0.55
	if math.Abs(rec.NetPnL-wantNet) > 1e-9 {
		t.Fatalf("net_pnl=%v want %v", rec.NetPnL, wantNet)
	}
}
