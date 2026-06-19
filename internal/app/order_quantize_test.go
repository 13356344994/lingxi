package app

import (
	"lingxi/internal/venue"
	"testing"
)

func TestQuantizePriceToTick_BELRepro(t *testing.T) {
	price := 0.1017564
	tick := 0.0001
	gotBuy := quantizePriceToTick(price, tick, venue.SideBuy)
	if gotBuy != 0.1018 {
		t.Fatalf("buy quantize = %v, want 0.1018", gotBuy)
	}
	gotSell := quantizePriceToTick(price, tick, venue.SideSell)
	if gotSell != 0.1017 {
		t.Fatalf("sell quantize = %v, want 0.1017", gotSell)
	}
}

func TestQuantizePriceToTick_Tick001(t *testing.T) {
	price := 1.23456
	tick := 0.01
	gotBuy := quantizePriceToTick(price, tick, venue.SideBuy)
	if gotBuy != 1.24 {
		t.Fatalf("buy quantize = %v, want 1.24", gotBuy)
	}
	gotSell := quantizePriceToTick(price, tick, venue.SideSell)
	if gotSell != 1.23 {
		t.Fatalf("sell quantize = %v, want 1.23", gotSell)
	}
}

func TestQuantizePriceToTick_ZeroTickUnchanged(t *testing.T) {
	price := 0.1017564
	if got := quantizePriceToTick(price, 0, venue.SideBuy); got != price {
		t.Fatalf("zero tick buy = %v, want unchanged %v", got, price)
	}
}

func TestQuantizeQtyToStep(t *testing.T) {
	if got := quantizeQtyToStep(1.23456, 0.001); got != 1.234 {
		t.Fatalf("qty floor = %v, want 1.234", got)
	}
	if got := quantizeQtyToStep(1.23456, 0); got != 1.23456 {
		t.Fatalf("zero step = %v, want unchanged", got)
	}
}

func TestOpenLegTradableAfterQuantize(t *testing.T) {
	instr := venue.Instrument{StepSize: 0.001, MinNotional: 5}
	if !openLegTradableAfterQuantize(0.1, 100, instr) {
		t.Fatal("expected tradable qty after quantize check")
	}
	if openLegTradableAfterQuantize(0.0001, 100, instr) {
		t.Fatal("expected sub-min qty not tradable")
	}
}

func TestQuantizeOrderPriceQtyMissingTickStillQuantizesQty(t *testing.T) {
	instr := venue.Instrument{TickSize: 0, StepSize: 0.01}
	price, qty := quantizeOrderPriceQty(0.1017564, 10.567, venue.SideBuy, instr, false, nil, "BEL-USDT")
	if price != 0.1017564 {
		t.Fatalf("price with tick=0 = %v, want unchanged", price)
	}
	if qty != 10.56 {
		t.Fatalf("qty with step=0.01 = %v, want 10.56", qty)
	}
}
