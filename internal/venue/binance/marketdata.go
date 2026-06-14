package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/exchange"
	"lingxi/internal/venue"
)

// FetchInstruments 拉取现货 + 永续元数据并翻译为中性 Instrument(委托 client.FetchExchangeInfos)。
func (a *Adapter) FetchInstruments(ctx context.Context) ([]venue.Instrument, error) {
	spot, futures, err := a.client.FetchExchangeInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]venue.Instrument, 0, len(spot)+len(futures))
	for _, s := range spot {
		canonical, _ := a.ToCanonical(s.Symbol, venue.MarketSpot)
		tick, step, minNotional := parseFilters(s.Filters)
		out = append(out, venue.Instrument{
			Symbol:       canonical,
			Market:       venue.MarketSpot,
			BaseAsset:    strings.ToUpper(s.BaseAsset),
			QuoteAsset:   strings.ToUpper(s.QuoteAsset),
			TickSize:     tick,
			StepSize:     step,
			MinNotional:  minNotional,
			QtyPrecision: s.BaseAssetPrecision,
			ContractSize: 1,
			QtyUnit:      "coin",
		})
	}
	for _, f := range futures {
		canonical, _ := a.ToCanonical(f.Symbol, venue.MarketPerp)
		tick, step, minNotional := parseFilters(f.Filters)
		out = append(out, venue.Instrument{
			Symbol:       canonical,
			Market:       venue.MarketPerp,
			BaseAsset:    strings.ToUpper(exchange.FuturesBaseAsset(f)),
			QuoteAsset:   strings.ToUpper(f.MarginAsset),
			TickSize:     tick,
			StepSize:     step,
			MinNotional:  minNotional,
			QtyPrecision: f.QuantityPrecision,
			ContractSize: 1, // 币安 USDT 永续按币数量,合约面值视作 1
			QtyUnit:      "coin",
		})
	}
	return out, nil
}

// FetchFundingSnapshot 合并 premiumIndex(实时费率/标记价)与 fundingInfo(逐币上下限/周期)为中性快照。
func (a *Adapter) FetchFundingSnapshot(ctx context.Context) ([]venue.FundingSnapshot, error) {
	premium, err := a.client.FetchPremiumIndex(ctx)
	if err != nil {
		return nil, err
	}
	// fundingInfo 只列「偏离默认 ±2%」的 symbol;拉取失败不致命,未收录者回退默认上下限。
	capBySymbol := make(map[string]exchange.FundingInfoItem)
	if infos, infoErr := a.client.FetchFundingInfo(ctx); infoErr == nil {
		for _, it := range infos {
			capBySymbol[strings.ToUpper(it.Symbol)] = it
		}
	}
	out := make([]venue.FundingSnapshot, 0, len(premium))
	for _, p := range premium {
		canonical, _ := a.ToCanonical(p.Symbol, venue.MarketPerp)
		snap := venue.FundingSnapshot{
			Symbol:               canonical,
			MarkPrice:            parseFloat(p.MarkPrice),
			IndexPrice:           parseFloat(p.IndexPrice),
			EstimatedSettlePrice: parseFloat(p.EstimatedSettlePrice),
			LastFundingRate:      parseFloat(p.LastFundingRate),
			NextFundingTime:      msToTime(p.NextFundingTime),
			IntervalHours:        8, // 默认 8h
			RateCap:              defaultFundingCap,
			RateFloor:            -defaultFundingCap,
		}
		if info, ok := capBySymbol[strings.ToUpper(p.Symbol)]; ok {
			if c := parseFloat(info.AdjustedFundingRateCap); c != 0 {
				snap.RateCap = c
			}
			if fl := parseFloat(info.AdjustedFundingRateFloor); fl != 0 {
				snap.RateFloor = fl
			}
			if info.FundingIntervalHours > 0 {
				snap.IntervalHours = info.FundingIntervalHours
			}
		}
		out = append(out, snap)
	}
	return out, nil
}

const defaultFundingCap = 0.02 // 币安默认单次结算资金费上下限 ±2%

// ServerTime 通过 fapi/v1/time 获取交易所服务器时间(委托 client.GetJSON)。
func (a *Adapter) ServerTime(ctx context.Context) (time.Time, error) {
	var payload struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := a.client.GetJSON(ctx, a.client.FuturesBaseURL()+PathFuturesTime, &payload); err != nil {
		return time.Time{}, err
	}
	return msToTime(payload.ServerTime), nil
}

// FetchBookTicker 拉最优买卖一档(bookTicker)。
func (a *Adapter) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.BookTicker{}, err
	}
	path := PathSpotBookTicker
	base := a.spotBaseURL()
	if m == venue.MarketPerp {
		path = PathFuturesBookTicker
		base = a.futuresBaseURL()
	}
	var ticker struct {
		Symbol   string `json:"symbol"`
		BidPrice string `json:"bidPrice"`
		BidQty   string `json:"bidQty"`
		AskPrice string `json:"askPrice"`
		AskQty   string `json:"askQty"`
	}
	if err := a.publicGet(ctx, base+path+"?symbol="+raw, &ticker); err != nil {
		return venue.BookTicker{}, err
	}
	return venue.BookTicker{
		Symbol:   s,
		Market:   m,
		BidPrice: parseFloat(ticker.BidPrice),
		BidQty:   parseFloat(ticker.BidQty),
		AskPrice: parseFloat(ticker.AskPrice),
		AskQty:   parseFloat(ticker.AskQty),
	}, nil
}

// FetchDepth 拉多档盘口(depth)。
func (a *Adapter) FetchDepth(ctx context.Context, m venue.Market, s venue.Symbol, limit int) (venue.Depth, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return venue.Depth{}, err
	}
	if limit <= 0 {
		limit = 50
	}
	path := PathSpotDepth
	base := a.spotBaseURL()
	if m == venue.MarketPerp {
		path = PathFuturesDepth
		base = a.futuresBaseURL()
	}
	var payload struct {
		Bids [][]string `json:"bids"`
		Asks [][]string `json:"asks"`
	}
	q := fmt.Sprintf("%s?symbol=%s&limit=%d", base+path, raw, limit)
	if err := a.publicGet(ctx, q, &payload); err != nil {
		return venue.Depth{}, err
	}
	depth := venue.Depth{Symbol: s, Market: m}
	for _, lv := range payload.Bids {
		if len(lv) >= 2 {
			depth.Bids = append(depth.Bids, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: parseFloat(lv[1])})
		}
	}
	for _, lv := range payload.Asks {
		if len(lv) >= 2 {
			depth.Asks = append(depth.Asks, venue.DepthLevel{Price: parseFloat(lv[0]), Qty: parseFloat(lv[1])})
		}
	}
	return depth, nil
}

// FetchOpenInterest 拉 U 本位永续持仓量(币数量)。
func (a *Adapter) FetchOpenInterest(ctx context.Context, s venue.Symbol) (float64, error) {
	raw, err := a.ToVenue(s, venue.MarketPerp)
	if err != nil {
		return 0, err
	}
	var payload struct {
		OpenInterest string `json:"openInterest"`
	}
	if err := a.publicGet(ctx, a.futuresBaseURL()+PathFuturesOpenInterest+"?symbol="+raw, &payload); err != nil {
		return 0, err
	}
	return parseFloat(payload.OpenInterest), nil
}

// FetchRecentQuoteVolume 用 klines 第 8 列(quote volume)在 window 内求和;window 向上取整到分钟。
func (a *Adapter) FetchRecentQuoteVolume(ctx context.Context, m venue.Market, s venue.Symbol, window time.Duration) (float64, error) {
	raw, err := a.ToVenue(s, m)
	if err != nil {
		return 0, err
	}
	if window <= 0 {
		window = time.Hour
	}
	minutes := int(math.Ceil(window.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	interval, limit := klineIntervalForMinutes(minutes)
	path := PathSpotKlines
	base := a.spotBaseURL()
	if m == venue.MarketPerp {
		path = PathFuturesKlines
		base = a.futuresBaseURL()
	}
	var rows [][]any
	url := fmt.Sprintf("%s?symbol=%s&interval=%s&limit=%d", base+path, raw, interval, limit)
	if err := a.publicGet(ctx, url, &rows); err != nil {
		return 0, err
	}
	sum := 0.0
	for _, row := range rows {
		if len(row) >= 8 {
			sum += parseAnyFloat(row[7])
		}
	}
	return sum, nil
}

// klineIntervalForMinutes 选取 K 线周期与 limit,使覆盖 window 且 limit≤1000。
func klineIntervalForMinutes(minutes int) (interval string, limit int) {
	switch {
	case minutes <= 60:
		return "1m", minutes
	case minutes <= 180:
		limit = (minutes + 2) / 3
		return "3m", limit
	case minutes <= 300:
		limit = (minutes + 4) / 5
		return "5m", limit
	case minutes <= 900:
		limit = (minutes + 14) / 15
		return "15m", limit
	default:
		limit = (minutes + 59) / 60
		if limit > 1000 {
			limit = 1000
		}
		return "1h", limit
	}
}

func parseAnyFloat(v any) float64 {
	switch typed := v.(type) {
	case string:
		return parseFloat(typed)
	case float64:
		return typed
	case json.Number:
		f, _ := typed.Float64()
		return f
	default:
		return 0
	}
}

const instCacheTTL = 10 * time.Minute

type instCacheEntry struct {
	inst      venue.Instrument
	expiresAt time.Time
}

func instCacheKey(m venue.Market, s venue.Symbol) string {
	return m.String() + "/" + string(s)
}

// lookupInstrument 拉单条 instruments 元数据(PlaceOrder 精度用),带进程级 TTL 缓存。
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

// fetchInstrument 实拉 exchangeInfo 并匹配目标 symbol。
// 踩坑实录:U 本位 /fapi/v1/exchangeInfo?symbol=XXX 会忽略 symbol 过滤、返回全量列表;
// 若盲取 Symbols[0] 会拿到 BTCUSDT(tickSize=0.1) 套到 HOME 等低价币 → 价格取整归零拒单。
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
	if m == venue.MarketPerp {
		var fpayload struct {
			Symbols []exchange.FuturesSymbol `json:"symbols"`
		}
		if err := a.publicGet(ctx, base+path+"?"+q.Encode(), &fpayload); err != nil {
			return venue.Instrument{}, err
		}
		f, ok := findFuturesSymbol(fpayload.Symbols, raw)
		if !ok {
			return venue.Instrument{}, fmt.Errorf("binance: 找不到合约 symbol=%s", raw)
		}
		return futuresRowToInstrument(a, f), nil
	}
	var payload struct {
		Symbols []exchange.SpotSymbol `json:"symbols"`
	}
	if err := a.publicGet(ctx, base+path+"?"+q.Encode(), &payload); err != nil {
		return venue.Instrument{}, err
	}
	sp, ok := findSpotSymbol(payload.Symbols, raw)
	if !ok {
		return venue.Instrument{}, fmt.Errorf("binance: 找不到现货 symbol=%s", raw)
	}
	return spotRowToInstrument(a, sp), nil
}

func findFuturesSymbol(symbols []exchange.FuturesSymbol, raw string) (exchange.FuturesSymbol, bool) {
	for _, f := range symbols {
		if strings.EqualFold(f.Symbol, raw) {
			return f, true
		}
	}
	return exchange.FuturesSymbol{}, false
}

func findSpotSymbol(symbols []exchange.SpotSymbol, raw string) (exchange.SpotSymbol, bool) {
	for _, sp := range symbols {
		if strings.EqualFold(sp.Symbol, raw) {
			return sp, true
		}
	}
	return exchange.SpotSymbol{}, false
}

func futuresRowToInstrument(a *Adapter, f exchange.FuturesSymbol) venue.Instrument {
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
	}
}

func spotRowToInstrument(a *Adapter, sp exchange.SpotSymbol) venue.Instrument {
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
	}
}

func parseFilters(filters []exchange.ExchangeFilter) (tick, step, minNotional float64) {
	for _, f := range filters {
		switch f.FilterType {
		case "PRICE_FILTER":
			tick = parseFloat(f.TickSize)
		case "LOT_SIZE":
			step = parseFloat(f.StepSize)
		case "MIN_NOTIONAL", "NOTIONAL":
			if v := parseFloat(f.MinNotional); v > 0 {
				minNotional = v
			} else if v := parseFloat(f.Notional); v > 0 {
				minNotional = v
			}
		}
	}
	return tick, step, minNotional
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
