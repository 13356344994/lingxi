package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

func TestDefaultUnifiedShadowFetchMarketsByVenueConcurrent(t *testing.T) {
	var peak int32
	var active int32
	v := &concurrentCountingVenue{
		fakeFundingVenue: fakeFundingVenue{name: binance.VenueName},
		onFetch: func() {
			cur := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&active, -1)
		},
	}
	symbols := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		symbols = append(symbols, fmt.Sprintf("SYM%02d-USDT", i))
	}
	got := defaultUnifiedShadowFetchMarketsByVenue(context.Background(), map[string]venue.Venue{
		binance.VenueName: v,
	}, symbols)
	if len(got[binance.VenueName]) != len(symbols) {
		t.Fatalf("market snaps = %d, want %d", len(got[binance.VenueName]), len(symbols))
	}
	if peak < 2 {
		t.Fatalf("peak concurrent fetches = %d, want >= 2 (worker pool active)", peak)
	}
	if peak > unifiedShadowMarketFetchConcurrency {
		t.Fatalf("peak concurrent fetches = %d, want <= %d", peak, unifiedShadowMarketFetchConcurrency)
	}
}

type concurrentCountingVenue struct {
	fakeFundingVenue
	onFetch func()
}

func (v *concurrentCountingVenue) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	if v.onFetch != nil {
		v.onFetch()
	}
	return venue.BookTicker{BidPrice: 100, AskPrice: 101}, nil
}
