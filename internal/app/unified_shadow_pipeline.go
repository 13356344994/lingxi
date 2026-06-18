package app

import (
	"context"
	"strings"
	"sync"

	"lingxi/internal/venue"
)

const unifiedShadowMarketFetchConcurrency = 8

// unified_shadow_pipeline.go 统一轮动扫描辅助: 按 venue 拉取 spot/perp 行情与可用 USDT(供 defaultScanRotationCandidates 使用)。

type unifiedShadowFetchMarketsFn func(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap

type unifiedShadowFetchAvailableUSDTFn func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64

type unifiedShadowPipelineFetchDeps struct {
	fetchMarkets       unifiedShadowFetchMarketsFn
	fetchAvailableUSDT unifiedShadowFetchAvailableUSDTFn
}

func unifiedShadowFundingByVenueFromSnapshot(src map[string]map[string]CrossVenueFundingEntry) map[string]map[string]CrossVenueSpotPerpFundingSnap {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]map[string]CrossVenueSpotPerpFundingSnap, len(src))
	for venueName, bySym := range src {
		if len(bySym) == 0 {
			continue
		}
		dstBySym := make(map[string]CrossVenueSpotPerpFundingSnap, len(bySym))
		for sym, entry := range bySym {
			dstBySym[sym] = CrossVenueSpotPerpFundingSnap{
				Rate:            entry.Rate,
				IntervalHours:   entry.IntervalHours,
				NextFundingTime: entry.NextFundingTime,
				SpikeThreshold:  entry.SpikeThreshold,
			}
		}
		out[venueName] = dstBySym
	}
	return out
}

func defaultUnifiedShadowFetchMarketsByVenue(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
	out := make(map[string]map[string]CrossVenueSpotPerpMarketSnap)
	var outMu sync.Mutex
	var wg sync.WaitGroup
	for venueName, v := range venues {
		if v == nil {
			continue
		}
		wg.Add(1)
		go func(venueName string, v venue.Venue) {
			defer wg.Done()
			bySym := unifiedShadowFetchVenueMarkets(ctx, v, symbols)
			if len(bySym) == 0 {
				return
			}
			outMu.Lock()
			out[venueName] = bySym
			outMu.Unlock()
		}(venueName, v)
	}
	wg.Wait()
	return out
}

func unifiedShadowFetchLegMidPrice(ctx context.Context, v venue.Venue, marketType string, sym string) (float64, bool) {
	sym = strings.TrimSpace(sym)
	if v == nil || sym == "" {
		return 0, false
	}
	if ctx.Err() != nil {
		return 0, false
	}
	var market venue.Market
	switch strings.ToLower(strings.TrimSpace(marketType)) {
	case "perp", "swap", "futures":
		market = venue.MarketPerp
	default:
		market = venue.MarketSpot
	}
	bt, err := v.FetchBookTicker(ctx, market, venue.Symbol(sym))
	if err != nil {
		return 0, false
	}
	mid := (bt.BidPrice + bt.AskPrice) / 2
	if mid <= 0 {
		return 0, false
	}
	return mid, true
}

func unifiedShadowFetchOneSymbolMarket(ctx context.Context, v venue.Venue, sym string) (CrossVenueSpotPerpMarketSnap, bool) {
	sym = strings.TrimSpace(sym)
	if sym == "" {
		return CrossVenueSpotPerpMarketSnap{}, false
	}
	mkt := CrossVenueSpotPerpMarketSnap{}
	if spotBT, err := v.FetchBookTicker(ctx, venue.MarketSpot, venue.Symbol(sym)); err == nil {
		mid := (spotBT.BidPrice + spotBT.AskPrice) / 2
		if mid > 0 {
			mkt.SpotMid = mid
			mkt.HasSpot = true
		}
	}
	if perpBT, err := v.FetchBookTicker(ctx, venue.MarketPerp, venue.Symbol(sym)); err == nil {
		mid := (perpBT.BidPrice + perpBT.AskPrice) / 2
		if mid > 0 {
			mkt.PerpMid = mid
			mkt.HasPerp = true
		}
	}
	if !mkt.HasSpot && !mkt.HasPerp {
		return CrossVenueSpotPerpMarketSnap{}, false
	}
	return mkt, true
}

func unifiedShadowFetchVenueMarkets(ctx context.Context, v venue.Venue, symbols []string) map[string]CrossVenueSpotPerpMarketSnap {
	if len(symbols) == 0 {
		return nil
	}
	workers := unifiedShadowMarketFetchConcurrency
	if workers <= 0 {
		workers = 1
	}
	if workers > len(symbols) {
		workers = len(symbols)
	}
	symCh := make(chan string)
	bySym := make(map[string]CrossVenueSpotPerpMarketSnap)
	var bySymMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sym := range symCh {
				if ctx.Err() != nil {
					return
				}
				mkt, ok := unifiedShadowFetchOneSymbolMarket(ctx, v, sym)
				if !ok {
					continue
				}
				bySymMu.Lock()
				bySym[sym] = mkt
				bySymMu.Unlock()
			}
		}()
	}
	go func() {
		defer close(symCh)
		for _, sym := range symbols {
			select {
			case <-ctx.Done():
				return
			case symCh <- sym:
			}
		}
	}()
	wg.Wait()
	return bySym
}

func defaultUnifiedShadowFetchAvailableUSDTByVenue(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
	out := make(map[string]float64, len(venueNames))
	for _, venueName := range venueNames {
		v := venues[venueName]
		if v == nil {
			continue
		}
		avail, err := fetchVenuePerpAvailableUSDT(ctx, v)
		if err != nil {
			continue
		}
		out[venueName] = avail
	}
	return out
}
