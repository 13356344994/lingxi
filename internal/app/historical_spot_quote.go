package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// 本文件从 historical_monthly_backtest.go 拆出：仅承载历史现货报价上下文解析与现货指标→USDT
// 折算逻辑（同包搬移，行为不变），向单文件 ≤ 800 行门禁靠拢。

type historicalSpotQuoteContext struct {
	SpotPair   string
	QuoteAsset string
}

type historicalSpotQuoteConversion struct {
	SpotPair      string
	QuoteAsset    string
	FXSymbol      string
	RateToUSDT    float64
	DeviationRate float64
}

func resolveHistoricalSpotQuoteContext(client *BinanceClient, futureSymbol string) historicalSpotQuoteContext {
	normalizedFuture := strings.ToUpper(strings.TrimSpace(futureSymbol))
	ctx := historicalSpotQuoteContext{
		SpotPair:   normalizedFuture,
		QuoteAsset: "USDT",
	}
	if client == nil {
		return ctx
	}
	if pair := strings.TrimSpace(client.HistoricalSpotPairForFuture(normalizedFuture)); pair != "" {
		ctx.SpotPair = strings.ToUpper(pair)
	}
	if meta, ok := client.HistoricalSpotMetaForFuture(normalizedFuture); ok {
		if strings.TrimSpace(meta.Symbol) != "" {
			ctx.SpotPair = strings.ToUpper(strings.TrimSpace(meta.Symbol))
		}
		if strings.TrimSpace(meta.QuoteAsset) != "" {
			ctx.QuoteAsset = strings.ToUpper(strings.TrimSpace(meta.QuoteAsset))
		}
	}
	return ctx
}

func historicalSpotKlineAt(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, symbol string, at time.Time, before bool) (MarketKline, error) {
	if before {
		return cache.getSpotKlineBefore(ctx, client, symbol, at)
	}
	return cache.getSpotKline(ctx, client, symbol, at)
}

func historicalKlineReferencePrice(kline MarketKline, before bool) float64 {
	if before {
		return kline.Close
	}
	return kline.Open
}

func historicalSpotQuoteConversionAt(ctx context.Context, client *BinanceClient, cache *monthlyKlineCache, futureSymbol string, at time.Time, before bool) (historicalSpotQuoteConversion, error) {
	quoteCtx := resolveHistoricalSpotQuoteContext(client, futureSymbol)
	conversion := historicalSpotQuoteConversion{
		SpotPair:   quoteCtx.SpotPair,
		QuoteAsset: quoteCtx.QuoteAsset,
		FXSymbol:   "USDT",
		RateToUSDT: 1,
	}
	if quoteCtx.QuoteAsset == "" || quoteCtx.QuoteAsset == "USDT" {
		return conversion, nil
	}
	directSymbol := quoteCtx.QuoteAsset + "USDT"
	if directKline, err := historicalSpotKlineAt(ctx, client, cache, directSymbol, at, before); err == nil {
		rate := historicalKlineReferencePrice(directKline, before)
		if rate <= 0 {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 无效: pair=%s rate=%.8f", directSymbol, rate)
		}
		conversion.FXSymbol = directSymbol
		conversion.RateToUSDT = rate
		conversion.DeviationRate = math.Abs(rate - 1)
		return conversion, nil
	} else {
		inverseSymbol := "USDT" + quoteCtx.QuoteAsset
		inverseKline, inverseErr := historicalSpotKlineAt(ctx, client, cache, inverseSymbol, at, before)
		if inverseErr != nil {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 不可用: future=%s spotPair=%s quote=%s directErr=%v inverseErr=%v", futureSymbol, quoteCtx.SpotPair, quoteCtx.QuoteAsset, err, inverseErr)
		}
		rawRate := historicalKlineReferencePrice(inverseKline, before)
		if rawRate <= 0 {
			return historicalSpotQuoteConversion{}, fmt.Errorf("cross quote fx 无效: pair=%s rate=%.8f", inverseSymbol, rawRate)
		}
		conversion.FXSymbol = inverseSymbol
		conversion.RateToUSDT = 1 / rawRate
		conversion.DeviationRate = math.Abs(conversion.RateToUSDT - 1)
		return conversion, nil
	}
}

func convertHistoricalSpotMetricsToUSDT(price float64, quoteVolume float64, conversion historicalSpotQuoteConversion) (float64, float64) {
	rate := conversion.RateToUSDT
	if rate <= 0 {
		rate = 1
	}
	return price * rate, quoteVolume * rate
}
