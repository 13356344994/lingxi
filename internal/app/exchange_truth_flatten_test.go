package app

import (
	"context"
	"testing"

	"lingxi/internal/venue"
)

type truthFlattenVenue struct {
	venue.Venue
	positions []venue.Position
	orders    int
}

func (v *truthFlattenVenue) Name() string { return "mock" }
func (v *truthFlattenVenue) FetchPositions(context.Context) ([]venue.Position, error) {
	return append([]venue.Position(nil), v.positions...), nil
}
func (v *truthFlattenVenue) FetchBookTicker(context.Context, venue.Market, venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{BidPrice: 99, AskPrice: 101}, nil
}
func (v *truthFlattenVenue) PlaceOrder(_ context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	v.orders++
	if !req.ReduceOnly {
		t := venue.OrderResult{}
		return t, nil
	}
	v.positions = nil
	return venue.OrderResult{ExecutedQty: req.Qty, Status: "filled"}, nil
}

func TestFlattenExchangeTruth(t *testing.T) {
	v := &truthFlattenVenue{positions: []venue.Position{{Symbol: "X-USDT", PositionAmt: 2}}}
	if err := flattenExchangeTruth(context.Background(), map[string]venue.Venue{"mock": v}); err != nil {
		t.Fatal(err)
	}
	if v.orders != 1 || len(v.positions) != 0 {
		t.Fatalf("未收敛: orders=%d positions=%v", v.orders, v.positions)
	}
}
