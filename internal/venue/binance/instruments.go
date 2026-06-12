package binance

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

const instCacheTTL = 10 * time.Minute

type instCacheEntry struct {
	inst      venue.Instrument
	expiresAt time.Time
}

func instCacheKey(m venue.Market, s venue.Symbol) string {
	return m.String() + "/" + string(s)
}

// lookupInstrument 拉单条 instruments 元数据(PlaceOrder 数量精度用),带进程级 TTL 缓存。
func (a *Adapter) lookupInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	key := instCacheKey(m, s)
	a.instMu.RLock()
	if e, ok := a.instCache[key]; ok && a.now().Before(e.expiresAt) {
		a.instMu.RUnlock()
		return e.inst, nil
	}
	a.instMu.RUnlock()

	inst, err := a.fetchInstrument(ctx, m, s)
	if err != nil {
		return venue.Instrument{}, err
	}
	a.instMu.Lock()
	if a.instCache == nil {
		a.instCache = make(map[string]instCacheEntry)
	}
	a.instCache[key] = instCacheEntry{inst: inst, expiresAt: a.now().Add(instCacheTTL)}
	a.instMu.Unlock()
	return inst, nil
}

// fetchInstrument 实拉单条 exchangeInfo(缓存未命中路径)。
func (a *Adapter) fetchInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Instrument{}, err
	}
	if raw == "" {
		return venue.Instrument{}, fmt.Errorf("binance: 空符号")
	}
	path := PathSpotExchangeInfo
	base := a.spotBaseURL()
	if m == venue.MarketPerp {
		path = PathFuturesExchangeInfo
		base = a.futuresBaseURL()
	}
	q := url.Values{}
	q.Set("symbol", raw)
	var payload struct {
		Symbols []exchange.SpotSymbol `json:"symbols"`
	}
	if m == venue.MarketPerp {
		var fpayload struct {
			Symbols []exchange.FuturesSymbol `json:"symbols"`
		}
		if err := a.publicGet(ctx, base+path+"?"+q.Encode(), &fpayload); err != nil {
			return venue.Instrument{}, err
		}
		if len(fpayload.Symbols) == 0 {
			return venue.Instrument{}, fmt.Errorf("binance: 找不到合约 symbol=%s", raw)
		}
		f := fpayload.Symbols[0]
		canonical, _ := a.ToCanonical(f.Symbol, venue.MarketPerp)
		tick, step, minNotional := parseFilters(f.Filters)
		return venue.Instrument{
			Symbol:       canonical,
			Market:       venue.MarketPerp,
			BaseAsset:    strings.ToUpper(exchange.FuturesBaseAsset(f)),
			QuoteAsset:   strings.ToUpper(f.MarginAsset),
			TickSize:     tick,
			StepSize:     step,
			MinNotional:  minNotional,
			QtyPrecision: f.QuantityPrecision,
			ContractSize: 1,
			QtyUnit:      "coin",
		}, nil
	}
	if err := a.publicGet(ctx, base+path+"?"+q.Encode(), &payload); err != nil {
		return venue.Instrument{}, err
	}
	if len(payload.Symbols) == 0 {
		return venue.Instrument{}, fmt.Errorf("binance: 找不到现货 symbol=%s", raw)
	}
	sp := payload.Symbols[0]
	canonical, _ := a.ToCanonical(sp.Symbol, venue.MarketSpot)
	tick, step, minNotional := parseFilters(sp.Filters)
	return venue.Instrument{
		Symbol:       canonical,
		Market:       venue.MarketSpot,
		BaseAsset:    strings.ToUpper(sp.BaseAsset),
		QuoteAsset:   strings.ToUpper(sp.QuoteAsset),
		TickSize:     tick,
		StepSize:     step,
		MinNotional:  minNotional,
		QtyPrecision: sp.BaseAssetPrecision,
		ContractSize: 1,
		QtyUnit:      "coin",
	}, nil
}
