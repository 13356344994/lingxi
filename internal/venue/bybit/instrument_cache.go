package bybit

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

const instCacheTTL = 10 * time.Minute

type instCacheEntry struct {
	inst      venue.Instrument
	expiresAt time.Time
}

func (a *Adapter) now() time.Time {
	if a != nil && a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

func bybitInstCacheKey(m venue.Market, canonical venue.Symbol) string {
	return string(m) + "/" + strings.ToUpper(strings.TrimSpace(string(canonical)))
}

func bybitInstCategory(m venue.Market) string {
	if m == venue.MarketSpot {
		return categorySpot
	}
	return categoryLinear
}

func (a *Adapter) ensureInstCache() {
	if a == nil {
		return
	}
	a.instMu.Lock()
	defer a.instMu.Unlock()
	if a.instCache == nil {
		a.instCache = make(map[string]instCacheEntry)
	}
	if a.instCategoryAt == nil {
		a.instCategoryAt = make(map[string]time.Time)
	}
	if a.instRefreshInFlight == nil {
		a.instRefreshInFlight = make(map[string]bool)
	}
}

func (a *Adapter) seedInstrumentCache(m venue.Market, s venue.Symbol, inst venue.Instrument) {
	a.ensureInstCache()
	key := bybitInstCacheKey(m, s)
	a.instMu.Lock()
	defer a.instMu.Unlock()
	a.instCache[key] = instCacheEntry{inst: inst, expiresAt: a.now().Add(instCacheTTL)}
}

func (a *Adapter) lookupInstrument(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, error) {
	if a == nil {
		return venue.Instrument{}, fmt.Errorf("bybit: nil adapter")
	}
	canonical := strings.ToUpper(strings.TrimSpace(string(s)))
	key := bybitInstCacheKey(m, venue.Symbol(canonical))

	a.instMu.RLock()
	if e, ok := a.instCache[key]; ok && a.now().Before(e.expiresAt) {
		a.instMu.RUnlock()
		return e.inst, nil
	}
	category := bybitInstCategory(m)
	categoryFresh := false
	if at, ok := a.instCategoryAt[category]; ok && a.now().Sub(at) < instCacheTTL {
		categoryFresh = true
	}
	a.instMu.RUnlock()

	if !categoryFresh {
		if a.t == nil {
			return venue.Instrument{}, fmt.Errorf("bybit: instrument cache miss symbol=%s market=%s (no transport)", canonical, m)
		}
		if err := a.refreshInstrumentCategory(ctx, category); err != nil {
			return venue.Instrument{}, err
		}
	}

	a.instMu.RLock()
	defer a.instMu.RUnlock()
	if e, ok := a.instCache[key]; ok && a.now().Before(e.expiresAt) {
		return e.inst, nil
	}
	return venue.Instrument{}, fmt.Errorf("bybit: 找不到 instrument symbol=%s market=%s", canonical, m)
}

func (a *Adapter) lookupInstrumentOptional(ctx context.Context, m venue.Market, s venue.Symbol) (venue.Instrument, bool) {
	inst, err := a.lookupInstrument(ctx, m, s)
	if err != nil {
		return venue.Instrument{}, false
	}
	return inst, true
}

func (a *Adapter) refreshInstrumentCategory(ctx context.Context, category string) error {
	if a == nil || a.t == nil {
		return fmt.Errorf("bybit: no transport for instrument refresh")
	}
	a.ensureInstCache()

	a.instMu.Lock()
	if at, ok := a.instCategoryAt[category]; ok && a.now().Sub(at) < instCacheTTL {
		a.instMu.Unlock()
		return nil
	}
	if a.instRefreshInFlight[category] {
		a.instMu.Unlock()
		return nil
	}
	a.instRefreshInFlight[category] = true
	a.instMu.Unlock()

	defer func() {
		a.instMu.Lock()
		delete(a.instRefreshInFlight, category)
		a.instMu.Unlock()
	}()

	mkt := venue.MarketPerp
	if category == categorySpot {
		mkt = venue.MarketSpot
	}
	rows, err := a.fetchInstrumentCategory(ctx, category)
	if err != nil {
		return err
	}

	expires := a.now().Add(instCacheTTL)
	a.instMu.Lock()
	defer a.instMu.Unlock()
	for _, it := range rows {
		canonical, _ := a.ToCanonical(it.Symbol, mkt)
		key := bybitInstCacheKey(mkt, canonical)
		minNotional := parseFloat(it.LotSizeFilter.MinNotionalVal)
		if minNotional == 0 {
			minNotional = parseFloat(it.LotSizeFilter.MinOrderAmt)
		}
		step := parseFloat(it.LotSizeFilter.QtyStep)
		if mkt == venue.MarketSpot {
			step = spotLotStepSize(it.LotSizeFilter.QtyStep, it.LotSizeFilter.BasePrecision)
		}
		a.instCache[key] = instCacheEntry{
			inst: venue.Instrument{
				Symbol:      canonical,
				Market:      mkt,
				TickSize:    parseFloat(it.PriceFilter.TickSize),
				StepSize:    step,
				MinNotional: minNotional,
			},
			expiresAt: expires,
		}
	}
	a.instCategoryAt[category] = a.now()
	return nil
}

func quantizePriceForSide(price, tick float64, side venue.Side) float64 {
	if tick <= 0 || price <= 0 {
		return price
	}
	if side == venue.SideBuy {
		return execution.CeilToStep(price, tick)
	}
	return execution.FloorToStep(price, tick)
}

func precisionFromIncrement(increment float64) int {
	if increment <= 0 {
		return 0
	}
	text := strconv.FormatFloat(increment, 'f', -1, 64)
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		return len(strings.TrimRight(text[dot+1:], "0"))
	}
	return 0
}

func formatBybitPrice(price, tick float64) string {
	if tick > 0 {
		prec := precisionFromIncrement(tick)
		factor := math.Pow10(prec)
		rounded := math.Round(price*factor) / factor
		return strconv.FormatFloat(rounded, 'f', prec, 64)
	}
	return fmtNum(price)
}

func formatBybitQty(qty, step float64) string {
	if step > 0 {
		prec := precisionFromIncrement(step)
		factor := math.Pow10(prec)
		rounded := math.Round(qty*factor) / factor
		return strconv.FormatFloat(rounded, 'f', prec, 64)
	}
	return fmtNum(qty)
}
