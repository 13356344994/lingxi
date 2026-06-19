package app

import (
	"context"
	"strings"
	"sync"

	"lingxi/internal/venue"
)

const (
	unifiedMarketFetchConcurrency      = 8
	unifiedMarketDepthFetchConcurrency = 8
)

// unified_market_pipeline.go 统一轮动扫描辅助: 按 venue 拉取 spot/perp 行情与可用 USDT(供 defaultScanRotationCandidates 使用)。

type unifiedMarketFetchMarketsFn func(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap

type unifiedMarketFetchAvailableUSDTFn func(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64

type unifiedMarketPipelineFetchDeps struct {
	fetchMarkets       unifiedMarketFetchMarketsFn
	fetchAvailableUSDT unifiedMarketFetchAvailableUSDTFn
}

func unifiedMarketFundingByVenueFromSnapshot(src map[string]map[string]CrossVenueFundingEntry) map[string]map[string]CrossVenueSpotPerpFundingSnap {
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
				Rate:                  entry.Rate,
				IntervalHours:         entry.IntervalHours,
				NextFundingTime:       entry.NextFundingTime,
				SpikeThreshold:        entry.SpikeThreshold,
				FundingHistorySamples: entry.FundingHistorySamples,
			}
		}
		out[venueName] = dstBySym
	}
	return out
}

func defaultUnifiedMarketFetchMarketsByVenue(ctx context.Context, venues map[string]venue.Venue, symbols []string) map[string]map[string]CrossVenueSpotPerpMarketSnap {
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
			bySym := unifiedMarketFetchVenueMarkets(ctx, v, symbols)
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

func unifiedMarketFetchLegMidPrice(ctx context.Context, v venue.Venue, marketType string, sym string) (float64, bool) {
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

func unifiedMarketFetchOneSymbolMarket(ctx context.Context, v venue.Venue, sym string) (CrossVenueSpotPerpMarketSnap, bool) {
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

func unifiedMarketFetchVenueMarkets(ctx context.Context, v venue.Venue, symbols []string) map[string]CrossVenueSpotPerpMarketSnap {
	if len(symbols) == 0 {
		return nil
	}
	instrBySymMarket := unifiedMarketIndexInstruments(ctx, v)
	workers := unifiedMarketFetchConcurrency
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
				mkt, ok := unifiedMarketFetchOneSymbolMarket(ctx, v, sym)
				if !ok {
					continue
				}
				unifiedMarketApplyInstrumentMeta(&mkt, sym, instrBySymMarket)
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

func unifiedMarketCanonicalSymbol(sym string) string {
	return strings.ToUpper(strings.TrimSpace(sym))
}

func unifiedMarketIndexInstruments(ctx context.Context, v venue.Venue) map[string]map[venue.Market]venue.Instrument {
	out := make(map[string]map[venue.Market]venue.Instrument)
	if v == nil {
		return out
	}
	instrs, err := v.FetchInstruments(ctx)
	if err != nil {
		return out
	}
	for _, in := range instrs {
		sym := unifiedMarketCanonicalSymbol(string(in.Symbol))
		if out[sym] == nil {
			out[sym] = make(map[venue.Market]venue.Instrument)
		}
		out[sym][in.Market] = in
	}
	return out
}

func unifiedMarketApplyInstrumentMeta(mkt *CrossVenueSpotPerpMarketSnap, sym string, instrBySymMarket map[string]map[venue.Market]venue.Instrument) {
	if mkt == nil {
		return
	}
	byMkt := instrBySymMarket[unifiedMarketCanonicalSymbol(sym)]
	if len(byMkt) == 0 {
		return
	}
	if spot, ok := byMkt[venue.MarketSpot]; ok {
		mkt.SpotTickSize = spot.TickSize
		mkt.SpotStepSize = spot.StepSize
		mkt.SpotMinNotional = spot.MinNotional
	}
	if perp, ok := byMkt[venue.MarketPerp]; ok {
		mkt.PerpTickSize = perp.TickSize
		mkt.PerpStepSize = perp.StepSize
		mkt.PerpMinNotional = perp.MinNotional
	}
}

// unifiedMarketDepthFetchSymbols 从粗筛 symbol 列表截取二次拉深度子集(限流)。
func unifiedMarketDepthFetchSymbols(symbols []string) []string {
	if len(symbols) <= rotationScanDepthFetchMaxSymbols {
		return symbols
	}
	return symbols[:rotationScanDepthFetchMaxSymbols]
}

// unifiedMarketEnrichMarketsWithDepthVolume 对决策候选二次拉深度与 24h 量,填入 MarketSnap;失败/不支持则留空(不阻断扫描)。
func unifiedMarketEnrichMarketsWithDepthVolume(
	ctx context.Context,
	venues map[string]venue.Venue,
	marketsByVenue map[string]map[string]CrossVenueSpotPerpMarketSnap,
	symbols []string,
) {
	symbols = unifiedMarketDepthFetchSymbols(symbols)
	if len(symbols) == 0 || len(marketsByVenue) == 0 {
		return
	}
	var wg sync.WaitGroup
	for venueName, bySym := range marketsByVenue {
		if len(bySym) == 0 {
			continue
		}
		v := venues[venueName]
		if v == nil {
			continue
		}
		wg.Add(1)
		go func(v venue.Venue, bySym map[string]CrossVenueSpotPerpMarketSnap) {
			defer wg.Done()
			unifiedMarketEnrichVenueMarketsDepthVolume(ctx, v, bySym, symbols)
		}(v, bySym)
	}
	wg.Wait()
}

func unifiedMarketEnrichVenueMarketsDepthVolume(
	ctx context.Context,
	v venue.Venue,
	bySym map[string]CrossVenueSpotPerpMarketSnap,
	symbols []string,
) {
	if len(symbols) == 0 {
		return
	}
	workers := unifiedMarketDepthFetchConcurrency
	if workers <= 0 {
		workers = 1
	}
	if workers > len(symbols) {
		workers = len(symbols)
	}
	symCh := make(chan string)
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
				mkt, ok := bySym[sym]
				if !ok {
					continue
				}
				unifiedMarketEnrichOneMarketDepthVolume(ctx, v, sym, &mkt)
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
}

func unifiedMarketEnrichOneMarketDepthVolume(ctx context.Context, v venue.Venue, sym string, mkt *CrossVenueSpotPerpMarketSnap) {
	if v == nil || mkt == nil {
		return
	}
	sym = strings.TrimSpace(sym)
	if sym == "" || ctx.Err() != nil {
		return
	}
	vsym := venue.Symbol(sym)
	if mkt.HasSpot {
		if depth, err := v.FetchDepth(ctx, venue.MarketSpot, vsym, crossVenueOpenSlippageDepthLimit); err == nil && depthHasLevels(depth) {
			mkt.SpotDepth = &depth
		}
		if vol := fetchVenueQuoteVolume24h(ctx, v, venue.MarketSpot, vsym); vol > 0 {
			mkt.Volume24hSpotUSDT = vol
		}
	}
	if mkt.HasPerp {
		if depth, err := v.FetchDepth(ctx, venue.MarketPerp, vsym, crossVenueOpenSlippageDepthLimit); err == nil && depthHasLevels(depth) {
			mkt.PerpDepth = &depth
		}
		if vol := fetchVenueQuoteVolume24h(ctx, v, venue.MarketPerp, vsym); vol > 0 {
			mkt.Volume24hPerpUSDT = vol
		}
	}
}

func defaultUnifiedMarketFetchAvailableUSDTByVenue(ctx context.Context, venues map[string]venue.Venue, venueNames []string) map[string]float64 {
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
