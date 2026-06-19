package bybit

import (
	"context"
	"testing"

	"lingxi/internal/venue"
)

func TestBuildCreateBodyQuantizeBELSpotBuy(t *testing.T) {
	a := &Adapter{}
	a.seedInstrumentCache(venue.MarketSpot, "BEL-USDT", venue.Instrument{
		Symbol:   "BEL-USDT",
		Market:   venue.MarketSpot,
		TickSize: 0.0001,
		StepSize: 0.01,
	})

	body, err := a.buildCreateBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot,
		Symbol: "BEL-USDT",
		Side:   venue.SideBuy,
		Type:   venue.OrderTypeIOC,
		Qty:    10.567,
		Price:  0.1017564,
	})
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	if body.Price != "0.1018" {
		t.Fatalf("price = %q, want 0.1018", body.Price)
	}
	if body.Qty != "10.56" {
		t.Fatalf("qty = %q, want 10.56", body.Qty)
	}
}

func TestBuildCreateBodyQuantizeTick001(t *testing.T) {
	a := &Adapter{}
	a.seedInstrumentCache(venue.MarketPerp, "FOO-USDT", venue.Instrument{
		Symbol:   "FOO-USDT",
		Market:   venue.MarketPerp,
		TickSize: 0.01,
		StepSize: 0.001,
	})

	body, err := a.buildCreateBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketPerp,
		Symbol: "FOO-USDT",
		Side:   venue.SideSell,
		Type:   venue.OrderTypeIOC,
		Qty:    1.2345,
		Price:  12.3456,
	})
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	if body.Price != "12.34" {
		t.Fatalf("sell price = %q, want 12.34 (floor)", body.Price)
	}
	if body.Qty != "1.234" {
		t.Fatalf("qty = %q, want 1.234", body.Qty)
	}
}

func TestBuildCreateBodyWithoutCacheKeepsRawPrice(t *testing.T) {
	a := &Adapter{}
	body, err := a.buildCreateBody(context.Background(), venue.OrderRequest{
		Market: venue.MarketSpot,
		Symbol: "BEL-USDT",
		Side:   venue.SideBuy,
		Type:   venue.OrderTypeIOC,
		Qty:    1,
		Price:  0.1017564,
	})
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	if body.Price != "0.1017564" {
		t.Fatalf("uncached price = %q, want raw", body.Price)
	}
}
