package main

import (
	"context"
	"testing"
)

func TestNormalizeProtectedLimitPriceAlignsToTick(t *testing.T) {
	buy := normalizeProtectedLimitPrice("BUY", 100.0003, 0.001, 0.05)
	if buy != 100.15 {
		t.Fatalf("unexpected aligned buy price: %v", buy)
	}

	sell := normalizeProtectedLimitPrice("SELL", 100.0003, 0.001, 0.05)
	if sell != 99.9 {
		t.Fatalf("unexpected aligned sell price: %v", sell)
	}
}

func TestFetchBookTickerPrefersWarmCache(t *testing.T) {
	engine := &FundingRateSniperEngine{
		spotTickerCache:    NewBookTickerCache(),
		futuresTickerCache: NewBookTickerCache(),
	}
	engine.spotTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "100.0", AskPrice: "100.1"})
	engine.futuresTickerCache.Update(BookTicker{Symbol: "BTCUSDT", BidPrice: "99.9", AskPrice: "100.0"})

	spotTicker, err := engine.fetchSpotBookTicker(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("fetchSpotBookTicker() error = %v", err)
	}
	if spotTicker.AskPrice != "100.1" {
		t.Fatalf("unexpected spot ticker from cache: %+v", spotTicker)
	}

	futuresTicker, err := engine.fetchFuturesBookTicker(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("fetchFuturesBookTicker() error = %v", err)
	}
	if futuresTicker.BidPrice != "99.9" {
		t.Fatalf("unexpected futures ticker from cache: %+v", futuresTicker)
	}
}
