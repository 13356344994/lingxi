package app

import (
	"context"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
	"lingxi/internal/venue/okx"
)

func TestPlaceCrossVenueLegExecMakerImmediateFill(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	cfg := testCrossVenueMakerExecutorConfig()
	e := &CrossVenueExecutor{
		cfg:    cfg,
		venues: map[string]venue.Venue{bybit.VenueName: high},
		logger: testCrossVenueLogger(),
		nowFn:  time.Now,
	}
	res, err := e.placeCrossVenueLegExec(
		context.Background(), high, "BTC-USDT", venue.SideSell, 0.5, "dbg-h1", false, true, false,
		cfg.CrossVenueExecutor.SlippageBudgetBps, time.Now().Add(time.Second),
		venue.Instrument{TickSize: 0.1, StepSize: 0.001}, false,
	)
	if err != nil {
		t.Fatalf("leg exec: %v", err)
	}
	if res.ExecutedQty != 0.5 {
		t.Fatalf("qty=%v", res.ExecutedQty)
	}
}

func TestExecuteCrossVenueOpenMakerSecondLegTakerFallback(t *testing.T) {
	now := time.Now().UTC().Add(time.Hour)
	high := newCrossVenueTestVenue(bybit.VenueName, 0.001, 8, now)
	low := newCrossVenueTestVenue(okx.VenueName, 0.0001, 8, now)
	low.makerPostOnlyNeverFill = true
	low.placeClientIDs = make([]string, 0, 8)
	e := newCrossVenueMakerOpenExecutor(t, high, low)
	if !e.cfg.CrossVenueExecutor.MakerFirst {
		t.Fatal("maker_first should be true")
	}
	pos, err := e.executeCrossVenueOpen(context.Background(), testCrossVenueOpenPlan(), true)
	if err != nil {
		t.Fatalf("open: %v high=%d low=%d", err, *high.placeCount, *low.placeCount)
	}
	if pos.Status != CrossVenueStatusOpen {
		t.Fatalf("status=%s", pos.Status)
	}
}
