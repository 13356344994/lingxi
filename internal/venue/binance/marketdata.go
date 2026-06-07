package binance

import (
	"context"
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
	if err := a.client.GetJSON(ctx, a.client.FuturesBaseURL()+"/fapi/v1/time", &payload); err != nil {
		return time.Time{}, err
	}
	return msToTime(payload.ServerTime), nil
}

// 以下行情子能力当前由 internal/app 层的 WS 缓存 / 端点拼接提供,P1 再迁入本包,
// 在此之前返回 ErrUnsupported(引擎沿用旧路径,行为不变)。

func (a *Adapter) FetchBookTicker(ctx context.Context, m venue.Market, s venue.Symbol) (venue.BookTicker, error) {
	return venue.BookTicker{}, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchDepth(ctx context.Context, m venue.Market, s venue.Symbol, limit int) (venue.Depth, error) {
	return venue.Depth{}, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchOpenInterest(ctx context.Context, s venue.Symbol) (float64, error) {
	return 0, venue.ErrUnsupported // TODO(P1)
}

func (a *Adapter) FetchRecentQuoteVolume(ctx context.Context, m venue.Market, s venue.Symbol, window time.Duration) (float64, error) {
	return 0, venue.ErrUnsupported // TODO(P1)
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
