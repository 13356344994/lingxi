package app

import (
	"context"
	"math"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestCrossVenueCloseSlippageBpsEscalatesWithCap(t *testing.T) {
	base := 40.0
	cases := []struct {
		attempt int
		want    float64
	}{
		{1, 40},
		{2, 80},
		{3, 160},
		{4, 250}, // 320 capped
		{10, 250},
		{211, 250},
	}
	for _, tc := range cases {
		got := crossVenueCloseSlippageBps(base, tc.attempt)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("attempt=%d bps=%v want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestCrossVenueCloseIocLimitPriceEscalatesThenPierce(t *testing.T) {
	book := venue.BookTicker{BidPrice: 99, AskPrice: 101}
	base := 40.0

	p1, pierce := crossVenueCloseIocLimitPrice(venue.SideBuy, book, base, 1)
	if pierce {
		t.Fatal("attempt 1 should not pierce")
	}
	want1 := 101 * (1 + 40.0/10000)
	if math.Abs(p1-want1) > 1e-9 {
		t.Fatalf("attempt 1 price=%v want %v", p1, want1)
	}

	p3, pierce := crossVenueCloseIocLimitPrice(venue.SideBuy, book, base, 3)
	if pierce {
		t.Fatal("attempt 3 should not pierce")
	}
	want3 := 101 * (1 + 160.0/10000)
	if math.Abs(p3-want3) > 1e-9 {
		t.Fatalf("attempt 3 price=%v want %v", p3, want3)
	}

	p8, pierce := crossVenueCloseIocLimitPrice(venue.SideBuy, book, base, 8)
	if !pierce {
		t.Fatal("attempt 8 should pierce")
	}
	if math.Abs(p8-101*crossVenueClosePierceBuyMultiplier) > 1e-9 {
		t.Fatalf("pierce buy price=%v want %v", p8, 101*crossVenueClosePierceBuyMultiplier)
	}

	ps, pierce := crossVenueCloseIocLimitPrice(venue.SideSell, book, base, 8)
	if !pierce {
		t.Fatal("attempt 8 sell should pierce")
	}
	if math.Abs(ps-99*crossVenueClosePierceSellMultiplier) > 1e-9 {
		t.Fatalf("pierce sell price=%v want %v", ps, 99*crossVenueClosePierceSellMultiplier)
	}
}

func TestCrossVenueCloseCriticalAlertBackoff(t *testing.T) {
	wantAlert := map[int]bool{
		1: false, 2: false,
		3: true, 4: false, 9: false, 10: true,
		29: false, 30: true, 99: false, 100: true,
		150: false, 200: true, 201: false, 250: false, 300: true,
	}
	for attempt := 1; attempt <= 300; attempt++ {
		got := crossVenueCloseCriticalShouldAlert(attempt)
		if want, ok := wantAlert[attempt]; ok {
			if got != want {
				t.Fatalf("attempt=%d alert=%v want %v", attempt, got, want)
			}
		} else if got {
			t.Fatalf("attempt=%d unexpected alert=true", attempt)
		}
	}
	alertCount := 0
	for attempt := 1; attempt <= 210; attempt++ {
		if crossVenueCloseCriticalShouldAlert(attempt) {
			alertCount++
		}
	}
	if alertCount != 5 { // 3,10,30,100,200
		t.Fatalf("210 rounds should alert 5 times, got %d", alertCount)
	}
}

func TestCrossVenueCloseReduceOnlyQtyFromExchange(t *testing.T) {
	now := testCrossVenueNow()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.3, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	high.clearOnReduceOnly = false
	low.clearOnReduceOnly = false

	cfg := testCrossVenueExecutorConfig()
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "reduce-only qty",
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "ro-qty",
	}
	if err := e.closeCrossVenuePosition(context.Background(), pos, true, "reduce-only qty"); err == nil {
		t.Fatal("expected incomplete close error")
	}
	var highReq, lowReq *venue.OrderRequest
	for i := range high.placeRequests {
		if high.placeRequests[i].ReduceOnly {
			highReq = &high.placeRequests[i]
			break
		}
	}
	for i := range low.placeRequests {
		if low.placeRequests[i].ReduceOnly {
			lowReq = &low.placeRequests[i]
			break
		}
	}
	if highReq == nil {
		t.Fatal("high leg reduce-only order missing")
	}
	if math.Abs(highReq.Qty-0.3) > 1e-9 {
		t.Fatalf("high leg qty=%v want 0.3 (exchange cap)", highReq.Qty)
	}
	if lowReq == nil {
		t.Fatal("low leg reduce-only order missing")
	}
	if math.Abs(lowReq.Qty-0.5) > 1e-9 {
		t.Fatalf("low leg qty=%v want 0.5", lowReq.Qty)
	}
}

func TestCrossVenueCloseRetryUsesEscalatedLimitPrice(t *testing.T) {
	now := testCrossVenueNow()
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	high.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: -0.5, MarkPrice: 100, Leverage: 2}}
	low.positions = []venue.Position{{Symbol: "BTC-USDT", PositionAmt: 0.5, MarkPrice: 100, Leverage: 2}}
	high.clearOnReduceOnly = false
	low.clearOnReduceOnly = false

	cfg := testCrossVenueExecutorConfig()
	cfg.CrossVenueExecutor.TakerSlippageBudgetBps = 40
	cfg.NetworkConfig.RecordDir = t.TempDir()
	cfg.CrossVenueExecutor.LiveTradingEnabled = true
	store := mustCrossVenueStore(t, cfg.NetworkConfig.RecordDir)
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high, okx.VenueName: low},
		store:  store,
		logger: testCrossVenueLogger(),
		nowFn:  func() time.Time { return now },
	}
	pos := CrossVenuePosition{
		Symbol: "BTC-USDT", HighVenue: bybit.VenueName, LowVenue: okx.VenueName,
		Status: CrossVenueStatusClosing, CloseReason: "escalate", CloseAttempts: 2,
		HighLeg: CrossVenueLegState{Qty: 0.5}, LowLeg: CrossVenueLegState{Qty: 0.5},
		OpenedAt: now.Add(-time.Hour), IdemPrefix: "esc",
	}
	_ = e.closeCrossVenuePosition(context.Background(), pos, true, "escalate")
	var lastHighBuy float64
	for _, req := range high.placeRequests {
		if req.ReduceOnly && req.Side == venue.SideBuy {
			lastHighBuy = req.Price
		}
	}
	want := 101 * (1 + 160.0/10000)
	if math.Abs(lastHighBuy-want) > 1e-6 {
		t.Fatalf("attempt 3 high buy limit=%v want %v", lastHighBuy, want)
	}
}

func testCrossVenueNow() time.Time {
	return time.Now().UTC().Add(time.Hour)
}
